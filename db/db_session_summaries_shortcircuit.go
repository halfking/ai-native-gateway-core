package db

// db_session_summaries_shortcircuit.go — session_summaries 形状对账的**只读**
// 完整度探测。
//
// 存在的理由是一次线上取证（2026-10-11，245 build_seq 2530 部署）：候选槽
// 连续三轮启动都卡在 ensureSessionSummariesCanonical 的第一条
// `ALTER TABLE public.session_summaries ADD COLUMN IF NOT EXISTS tenant_id` 上，
// 等待 ACCESS EXCLUSIVE 直到 context deadline exceeded，healthz 起不来，
// 部署安全网回滚。同一批日志里，排在它前面的 847/848/704 三条都打了
// "catalog short-circuit"，只有这条没有——形状早已齐全，却每次开机都要
// 独占锁一张热表。
//
// 守卫必须覆盖**四类**对象，缺一即回落到原 ensure 路径：
//
//	列（54 条，见 sessionSummariesCanonicalColumns）
//	索引（6 条，含 session_key 唯一索引）
//	约束（3 条 CHECK/UNIQUE）
//	user_intent 的 varchar(200) widen（692；短了就仍有 22001 失败面）
//
// 只读：全部是 information_schema / pg_indexes / pg_constraint 查询，不碰
// DDL，因此本身不会成为新的锁源。
//
// 任何探测出错一律返回 false（走原路径），与 columnsAllPresent 的语义一致：
// 守卫只能让"已经没问题"的库跳过 DDL，绝不能让它跳过"还有问题"的库。

import (
	"context"
	"fmt"
	"strings"
)

// sessionSummariesCanonicalColumns 是下面那条 reconcile SQL 会补齐的列清单。
//
// 由 db/db_session_summaries_shortcircuit_test.go 从本包的
// `ADD COLUMN IF NOT EXISTS <name>` 逐条抽取并与本清单比对：往 SQL 里加了列
// 却忘了加进这里，测试变红。漏一条的后果是自愈静默跳过本该执行的 DDL——
// 那正是这个守卫唯一可能造成的伤害，所以让它不可能悄悄发生。
var sessionSummariesCanonicalColumns = []string{
	"tenant_id",
	"session_key",
	"first_request_at",
	"last_request_at",
	"duration_seconds",
	"request_count",
	"success_count",
	"error_count",
	"total_cost_usd",
	"input_cost_usd",
	"output_cost_usd",
	"total_prompt_tokens",
	"total_completion_tokens",
	"total_tokens",
	"avg_latency_ms",
	"min_latency_ms",
	"max_latency_ms",
	"models_used",
	"primary_model",
	"model_switch_count",
	"title",
	"summary",
	"key_topics",
	"user_intent",
	"quality_score",
	"compliance_status",
	"compliance_issues_count",
	"prompt_injection_detected",
	"pii_detected",
	"toxic_output_detected",
	"work_types",
	"providers",
	"client_models",
	"last_summarized_at",
	"summary_version",
	"handoff_count",
	"last_handoff_at",
	"health_score",
	"health_grade",
	"range",
	"last_health_at",
	"tokens_at_trigger",
	"messages_at_trigger",
	"last_trigger_reason",
	"last_trigger_at",
	"gw_project_id",
	"gw_task_id",
	"user_tags",
	"session_status",
	"outcome",
	"agent_type",
	"expert_type",
	"tags",
	"search_vector",
}

// sessionSummariesCanonicalIndexes / Constraints 与 SQL 里的 CREATE INDEX、
// ADD CONSTRAINT 一一对应。
var sessionSummariesCanonicalIndexes = []string{
	"session_summaries_session_key_uidx",
	"idx_session_summaries_project",
	"idx_session_summaries_task",
	"idx_session_summaries_user_tags",
	"idx_session_summaries_status_time",
	"idx_session_summaries_search",
}

var sessionSummariesCanonicalConstraints = []string{
	"chk_session_status",
	"session_summaries_quality_score_check",
	"session_summaries_session_key_per_tenant",
}

// sessionSummariesCanonicalComplete 报告这张表是否已经处于 reconcile SQL 想要
// 的终态。四类对象齐全且 user_intent 已 widen 才为 true。
func (d *DB) sessionSummariesCanonicalComplete(ctx context.Context) bool {
	if d == nil || d.pool == nil {
		return false
	}
	if !d.columnsAllPresent(ctx, "session_summaries", sessionSummariesCanonicalColumns) {
		return false
	}
	quoted := func(names []string) string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = "'" + n + "'"
		}
		return strings.Join(out, ",")
	}

	// ★ §143.16（2026-10-21）：裸 uidx 的必要性是**条件性的** ——
	//   主键 session_summaries_pkey(session_key) 在位时，它与主键键集逐字重复
	//   （生产实测两份各 ~67MiB，且该表持续写入）⇒ 不需要它；
	//   主键不在位时，它才是 ON CONFLICT (session_key) 唯一的非部分唯一仲裁索引 ⇒ 必须在。
	// ⇒ 只有「缺它 **且** 也没有别的仲裁索引」才算真的缺。
	//   （本条只在 uidx 真的缺失时才会生效 ⇒ 无需再排除「它自己」当仲裁索引。）
	// ⚠ 判据与 bootstrap 的 CREATE 守卫共用同一段 SQL（常量），杜绝两处漂移。
	missingIndexSQL := `SELECT count(*) FROM unnest(ARRAY[%s]) AS want(name)
		  WHERE NOT EXISTS (SELECT 1 FROM pg_indexes
		                     WHERE schemaname='public' AND tablename='session_summaries'
		                       AND indexname = want.name)
		    AND NOT (
		        want.name = 'session_summaries_session_key_uidx'
		        AND EXISTS (` + sessionSummariesSessionKeyArbiterSQL + `)
		    )`
	var missingIndexes int
	if err := d.pool.QueryRow(ctx, fmt.Sprintf(missingIndexSQL,
		quoted(sessionSummariesCanonicalIndexes))).Scan(&missingIndexes); err != nil {
		return false
	}
	if missingIndexes != 0 {
		return false
	}

	var missingConstraints int
	if err := d.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT count(*) FROM unnest(ARRAY[%s]) AS want(name)
		  WHERE NOT EXISTS (SELECT 1 FROM pg_constraint
		                     WHERE conname = want.name
		                       AND conrelid = 'public.session_summaries'::regclass)`,
		quoted(sessionSummariesCanonicalConstraints))).Scan(&missingConstraints); err != nil {
		return false
	}
	if missingConstraints != 0 {
		return false
	}

	// 692：user_intent 必须已 widen 到 varchar(200)。仍是窄的就仍有 22001
	// 失败面，而这条判定只有 information_schema 读得到，列存在性检查看不到。
	var intentWidth *int
	if err := d.pool.QueryRow(ctx, `
		SELECT character_maximum_length
		  FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='session_summaries'
		   AND column_name='user_intent'`).Scan(&intentWidth); err != nil {
		return false
	}
	return intentWidth != nil && *intentWidth >= 200
}
