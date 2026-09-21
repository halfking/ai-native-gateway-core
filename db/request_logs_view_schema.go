package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// ensureRequestLogsCurrentMonthView mirrors
// sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql and
// sql/migrations/startup/710_request_logs_view_session_family_v2.sql.
//
// 2026-09-07 事故自愈：共享 PG 上的 canonical 查询视图
// request_logs_with_current_month 被带外操作删掉（577/610 的包装视图
// …_without_customer_id、…_without_request_class_due_at 仍在，顶层缺失），
// /api/logs 全部请求 42P01。写路径（telemetry → request_logs_hot）不受影响，
// 只有读路径挂——这类"表在、视图没了"的状态没有任何 migration 会重建
// （610 之后再无 migration 触碰该视图），网关也没有启动自愈，于是持续报错。
//
// 2026-09-14 存储优化方案 v2 S2（plan §3 D6 / §4）：canonical 升级为
// session 家族拼装体——session_turns(_hot) 113 列会话投影 UNION ALL v1 体
// （700 形态冻结）× 反连接（request_id 已入 turns 的 v1 行不再输出，双写期
// 不重复计数）。约 40 个读方同名同列契约零改动切换（migration 710 与本函数
// 双轨同体；db/view_schema_v2_contract_test.go 校验两者 viewdef 等价）。
// session_turns 缺表的极简/陈旧库回退 v1 体（legacy 链保持既有形状探测）。
//
// 重建按 577 + 610 + 696 + 700 的包装链分阶段补齐，列契约 = 基础 108 列 UNION
// + customer_id (577) + request_class/due_at (610) + system_fingerprint (696)
// + raw_model_name (700，仅冻结链需追加；动态推导的基础交集自带)。
// 基础列取 hot∩parent 交集（排除后追加列），因此 hot 的 HOT_ONLY 列
// （caller_id 等）永远不会撑爆 UNION——这正是 341 式 "SELECT * FROM hot
// UNION ALL SELECT * FROM parent" 重放在 603 之后必失败、进而留下"视图已删
// 未建"的根因。700 的缘起：drift scanner（83bf582dd 起）SELECT
// raw_model_name FROM 本视图，而冻结基础交集早于 485，该列从未出现在链上，
// 每周期 42703（本机 2089 部署即时炸出，生产 252 视图同构）。
//
// 编号 SQL 文件供 DBA 同步流程与 deploy-local 修复轨道使用；本函数保证
// 二进制启动即生效（幂等：视图健康且为 v2 体时零 DDL）。
func (d *DB) ensureRequestLogsCurrentMonthView(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// 734：details 特征层在场时，最新 v3 体须含 session_turn_details JOIN；
	// 老库（733 未跑）回退 710 形态仍视为健康，零 DDL。
	var detailsFamilyExists bool
	if err := d.pool.QueryRow(ctx, `
		SELECT to_regclass('public.session_turn_details_hot') IS NOT NULL
		   AND to_regclass('public.session_turn_details') IS NOT NULL
	`).Scan(&detailsFamilyExists); err != nil {
		return fmt.Errorf("probe session_turn_details family: %w", err)
	}
	var canonicalExists, bodyIsV2 bool
	// 体形探测基于 pg_views 行内求值（视图缺失时 EXISTS 子查询零行，
	// pg_get_viewdef 不会被求值——不能直接对 '...'::regclass 取视图定义，
	// 视图缺失会 42P01 炸掉整个探针）。
	if err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
		),
		EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
			  AND pg_get_viewdef(schemaname || '.' || viewname, true) LIKE '%session_turns%'
			  AND (NOT $1 OR pg_get_viewdef(schemaname || '.' || viewname, true) LIKE '%session_turn_details%')
		)
	`, detailsFamilyExists).Scan(&canonicalExists, &bodyIsV2); err != nil {
		return fmt.Errorf("probe request_logs_with_current_month existence: %w", err)
	}
	if canonicalExists && bodyIsV2 {
		return nil
	}
	if !canonicalExists {
		slog.Warn("request_logs_with_current_month missing; self-healing view chain " +
			"(mirror of sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql)")
	}

	var classWrapperExists, baseWrapperExists bool
	if err := d.pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM pg_views WHERE schemaname = 'public'
				AND viewname = 'request_logs_with_current_month_without_request_class_due_at'),
			EXISTS (SELECT 1 FROM pg_views WHERE schemaname = 'public'
				AND viewname = 'request_logs_with_current_month_without_customer_id')
	`).Scan(&classWrapperExists, &baseWrapperExists); err != nil {
		return fmt.Errorf("probe request_logs view wrappers: %w", err)
	}

	if !baseWrapperExists {
		// 基础 108 列 UNION：hot∩parent 交集（按 hot 列序），排除 575/608
		// 时代后追加的 customer_id/request_class/due_at。
		var baseCols string
		if err := d.pool.QueryRow(ctx, `
			SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
			  FROM pg_attribute h
			  JOIN pg_class hc ON hc.oid = h.attrelid
			  JOIN pg_namespace hn ON hn.oid = hc.relnamespace
			  JOIN pg_attribute p ON p.attrelid = ('public.request_logs')::regclass
			                     AND p.attname = h.attname
			  JOIN pg_class pc ON pc.oid = p.attrelid
			  JOIN pg_namespace pn ON pn.oid = pc.relnamespace
			 WHERE hn.nspname = 'public'
			   AND hc.relname = 'request_logs_hot'
			   AND pn.nspname = 'public'
			   AND pc.relname = 'request_logs'
			   AND h.attnum > 0 AND NOT h.attisdropped
			   AND p.attnum > 0 AND NOT p.attisdropped
			   AND h.attname NOT IN ('customer_id', 'request_class', 'due_at')
		`).Scan(&baseCols); err != nil {
			return fmt.Errorf("derive request_logs base view columns: %w", err)
		}
		if baseCols == "" {
			return fmt.Errorf("request_logs view self-heal: empty base column list " +
				"(request_logs_hot/request_logs missing? run migration 341 first)")
		}
		if _, err := d.pool.Exec(ctx, fmt.Sprintf(`
			CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
			SELECT %s FROM public.request_logs_hot
			UNION ALL
			SELECT %s FROM public.request_logs
		`, baseCols, baseCols)); err != nil {
			return fmt.Errorf("rebuild request_logs base wrapper view: %w", err)
		}
	}

	if !classWrapperExists {
		// Migration 577 阶段：lateral 追加 customer_id（hot 优先）。
		if _, err := d.pool.Exec(ctx, `
			CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
			SELECT v.*, m.customer_id
			FROM public.request_logs_with_current_month_without_customer_id v
			LEFT JOIN LATERAL (
				SELECT customer_id FROM public.request_logs_hot
				WHERE request_id = v.request_id AND ts = v.ts
				UNION ALL
				SELECT customer_id FROM public.request_logs
				WHERE request_id = v.request_id AND ts = v.ts
				LIMIT 1
			) m ON true
		`); err != nil {
			return fmt.Errorf("rebuild request_logs customer_id wrapper view: %w", err)
		}
	}

	// 会话族在场（430+707）→ canonical 直接上 v2 拼装体。v2 体内部内联的
	// v1 分支按 700 形态经 lateral 自带 customer_id/request_class/due_at/
	// system_fingerprint/raw_model_name，对动态重建链（基础交集已含 fp/raw）
	// 与冻结链（696/700 lateral 追加）同构——无需再探测包装形状。
	var sessionTurnsExist bool
	if err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'session_turns'
		)
	`).Scan(&sessionTurnsExist); err != nil {
		return fmt.Errorf("probe session_turns presence: %w", err)
	}
	if sessionTurnsExist {
		// 列数契约守卫（与 710 同规）：会话分支固定 113 列，canonical 列数
		// ≠ 113 即冻结基础交集契约漂移，强行 UNION 必失败——保留现状，
		// 由视图契约修复流程先归位。
		if canonicalExists {
			var canonCols int
			if err := d.pool.QueryRow(ctx, `
				SELECT count(*) FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_with_current_month'
			`).Scan(&canonCols); err != nil {
				return fmt.Errorf("probe request_logs_with_current_month column count: %w", err)
			}
			if canonCols != 113 {
				slog.Warn("request_logs_with_current_month column count != 113 (frozen contract drift); keeping v1 body",
					"columns", canonCols)
				return nil
			}
			slog.Info("request_logs_with_current_month carries the v1 body; upgrading to the " +
				"session-family v2 body (mirror of sql/migrations/startup/710_request_logs_view_session_family_v2.sql)")
		}
		baseHasFP, baseHasRaw, err := d.baseWrapperShape(ctx)
		if err != nil {
			return err
		}
		if _, err := d.pool.Exec(ctx, canonicalV2DDL(baseHasFP, baseHasRaw, detailsFamilyExists)); err != nil {
			return fmt.Errorf("rebuild request_logs_with_current_month as session-family v2: %w", err)
		}
		if _, err := d.pool.Exec(ctx, `COMMENT ON VIEW public.request_logs_with_current_month IS `+
			canonicalV2Comment); err != nil {
			return fmt.Errorf("comment request_logs_with_current_month v2: %w", err)
		}
		slog.Info("request_logs_with_current_month view upgraded to session-family v2 body "+
			"(session_turns(_hot) projection UNION ALL frozen v1 body with anti-join dedup)",
			"details_join", detailsFamilyExists)
		return nil
	}

	// 无会话族（极简/陈旧库）：维持 v1 体。610+696+700 阶段：lateral 追加
	// request_class/due_at（canonical），并按基础包装形状与底层表实际列况
	// 决定是否追加 system_fingerprint（696）与 raw_model_name（700，与
	// sql/migrations/startup/700 同形）。post-603 动态推导的基础交集已含两列
	// （680 引导/自愈重建路径），此时 canonical 经 v.* 继承，再 lateral 追加
	// 会重复列（CREATE 直接失败）；pre-485 冻结链（生产/本机现网）基础包装
	// 缺列，需 lateral 追加。lateral 的列引用同样按 hot 侧实况裁剪——
	// hot/parent 本身缺列时（极简测试桩、落后于 485/603 的库）自愈不得比
	// 缺列现状更坏。列名消费者不受形状间列序差异影响。
	if canonicalExists {
		// v1 体已在且会话族缺席：无需任何变更（保持既有形状，零 DDL）。
		return nil
	}
	baseHasFingerprint, baseHasRawModelName, err := d.baseWrapperShape(ctx)
	if err != nil {
		return err
	}
	var hotHasFingerprint, hotHasRawModelName bool
	if err := d.pool.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_hot'
				  AND column_name = 'system_fingerprint'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_hot'
				  AND column_name = 'raw_model_name'
			)
	`).Scan(&hotHasFingerprint, &hotHasRawModelName); err != nil {
		return fmt.Errorf("probe request_logs_hot appended columns: %w", err)
	}
	appendSelects := []string{"source.request_class", "source.due_at"}
	lateralHotSelects := []string{"h.request_class", "h.due_at"}
	lateralParentSelects := []string{"p.request_class", "p.due_at"}
	if !baseHasFingerprint && hotHasFingerprint {
		appendSelects = append(appendSelects, "source.system_fingerprint")
		lateralHotSelects = append(lateralHotSelects, "h.system_fingerprint")
		lateralParentSelects = append(lateralParentSelects, "p.system_fingerprint")
	}
	if !baseHasRawModelName && hotHasRawModelName {
		appendSelects = append(appendSelects, "source.raw_model_name")
		lateralHotSelects = append(lateralHotSelects, "h.raw_model_name")
		lateralParentSelects = append(lateralParentSelects, "p.raw_model_name")
	}
	canonicalDDL := fmt.Sprintf(`
		CREATE VIEW public.request_logs_with_current_month AS
		SELECT v.*, %s
		FROM public.request_logs_with_current_month_without_request_class_due_at v
		LEFT JOIN LATERAL (
			SELECT %s
			FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT %s
			FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) source ON true
	`, strings.Join(appendSelects, ", "),
		strings.Join(lateralHotSelects, ", "),
		strings.Join(lateralParentSelects, ", "))
	if _, err := d.pool.Exec(ctx, canonicalDDL); err != nil {
		return fmt.Errorf("rebuild request_logs_with_current_month view: %w", err)
	}
	slog.Info("request_logs_with_current_month view chain restored (self-heal 680+696+700)",
		"base_wrapper_carries_fingerprint", baseHasFingerprint,
		"base_wrapper_carries_raw_model_name", baseHasRawModelName,
		"appended_columns", strings.Join(appendSelects, ", "))
	return nil
}

// baseWrapperShape probes whether the base intersection wrapper already
// carries system_fingerprint/raw_model_name (dynamic-rebuild chain) or not
// (frozen pre-485 chain). Missing base wrapper (nothing to probe) reports
// both false — the lateral-append shape, matching the ensure's rebuild order.
func (d *DB) baseWrapperShape(ctx context.Context) (fp, raw bool, err error) {
	err = d.pool.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_with_current_month_without_customer_id'
				  AND column_name = 'system_fingerprint'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_with_current_month_without_customer_id'
				  AND column_name = 'raw_model_name'
			)
	`).Scan(&fp, &raw)
	if err != nil {
		return false, false, fmt.Errorf("probe base wrapper appended columns: %w", err)
	}
	return fp, raw, nil
}

// sessionFamilyProjectionV2 是 710 会话分支的 113 列投影（别名固定 t；hot 与
// parent 两分支复用同一文本）。列映射契约（直映/派生/守卫 CAST/NULL 补位）
// 逐列登记见 sql/migrations/startup/710_request_logs_view_session_family_v2.sql
// 头注释；修改任一侧必须同步另一侧（contract 测试校验 viewdef 等价）。
// projectionExprsV2 holds the 113 session-branch expressions in canonical
// column order. init-time composition joins them with
// canonicalColumnOrderV2 into `+"`"+`expr AS name`+"`"+` pairs — bare NULL casts would
// otherwise name view columns after their type ("int8"/"text"), colliding
// into 42701 duplicate column names.
var projectionExprsV2 = []string{
	"NULL::bigint",
	"t.request_id",
	"t.ts",
	"t.tenant_id::text",
	"(CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END)",
	"(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END)",
	"t.end_user_id",
	"NULL::text",
	"t.model",
	"(CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END)",
	"NULL::bigint",
	"t.canonical_id",
	"NULL::text",
	"t.submit_mode",
	"t.prompt_tokens",
	"t.completion_tokens",
	"NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0)",
	"t.cost_usd::numeric(14,8)",
	"t.latency_ms",
	"t.success",
	"t.error_kind",
	"t.search_text",
	"t.cache_read_tokens",
	"t.cache_write_tokens",
	"t.identity_hash",
	"t.virtual_client_id",
	"NULL::text",
	"NULL::text",
	"NULL::boolean",
	"t.stream_first_chunk_ms",
	"t.stream_chunk_count",
	"t.stream_interrupted",
	"t.stream_done_sent",
	"t.request_checksum",
	"t.response_checksum",
	"NULL::text",
	"t.egress_protocol",
	"t.failure_stage",
	"t.failure_detail_code",
	"t.request_preview",
	"t.transform_summary",
	"t.response_preview",
	"t.stream_done_sent",
	"t.cost_display::numeric(14,8)",
	"t.cost_currency",
	"t.usage_source",
	"(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)",
	"NULL::text",
	"(CASE WHEN t.success IS NULL THEN NULL WHEN t.success THEN 'success' WHEN t.status_code = 429 THEN 'rate_limited' ELSE 'failure' END)",
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"t.is_auto_request",
	"t.task_type",
	"NULL::text",
	"(CASE WHEN NULLIF(t.auto_decision, '') IS NOT NULL THEN t.auto_decision::jsonb END)",
	"t.auto_confidence::numeric(4,3)",
	"t.work_type",
	"t.task_type_chosen",
	"NULL::numeric(4,3)",
	"NULL::text",
	"NULL::text",
	"t.credits_charged",
	"t.parent_request_id",
	"NULL::text",
	"t.compression_strategy",
	"t.compression_meta",
	"NULL::integer",
	"NULL::integer",
	"NULL::jsonb",
	"NULL::text[]",
	"NULL::jsonb",
	"NULL::numeric(3,2)",
	"t.upstream_finish_reason",
	"t.tools",
	"t.client_endpoint",
	"t.client_timeout",
	"NULL::integer",
	"NULL::integer",
	"t.client_request_id",
	"t.upstream_status_code",
	"NULL::text[]",
	"NULL::text",
	"NULL::text",
	"NULL::jsonb",
	"(CASE WHEN t.attachment_count IS NULL THEN NULL WHEN t.attachment_count > 0 THEN true ELSE false END)",
	"t.attachment_count",
	"t.routing_attempts",
	"t.routing_summary",
	"t.agent_name",
	"t.agent_type",
	"t.client_protocol::character varying(50)",
	"t.canonical_model",
	"t.t0_arrived_at",
	"t.t1_total_enqueued_at",
	"t.t2_total_dequeued_at",
	"t.t3_model_enqueued_at",
	"t.t4_model_dequeued_at",
	"t.t5_cred_enqueued_at",
	"t.t6_cred_dequeued_at",
	"t.t7_forward_start_at",
	"t.t8_response_start_at",
	"t.t9_response_end_at",
	"NULL::text",
	"t.is_final_success",
	"t.origin_actor::character varying(255)",
	"t.customer_id",
	"NULL::text",
	"NULL::timestamptz",
	"t.system_fingerprint",
	"t.raw_model_name",
}

// sessionFamilyProjectionV2 is the session branch projection for BOTH turn
// sources (alias fixed to t), each expression explicitly named to the frozen
// contract. Column mapping contract: sql/migrations/startup/
// 710_request_logs_view_session_family_v2.sql header.
// detailsProjectionColumns is the 734 view-contract group: details column
// name → the v1 frozen-contract NULL type. withDetails=true projections emit
// `d.<name>` at these positions; false falls back to the 710 NULL placeholders.
// Types match public.session_turn_details DDL (migration 733) exactly, so the
// UNION ALL positional typing is unchanged in both shapes.
var detailsProjectionColumns = map[string]string{
	"client_model": "text", "provider_id": "bigint", "client_profile": "text",
	"virtual_ip": "text", "virtual_mac": "text", "affinity_hit": "boolean",
	"transform_rule_id": "text", "gw_task_id": "text", "api_key_prefix": "text",
	"owner_user": "text", "application_code": "text", "key_alias": "text",
	"api_key_owner_user": "text", "auto_profile": "text", "confidence_num": "numeric(4,3)",
	"model_chosen": "text", "strategy_used": "text", "compression_reason": "text",
	"outbound_msg_count": "integer", "outbound_token_est": "integer", "outbound_msg_hashes": "jsonb",
	"quality_flags": "text[]", "quality_fix_actions": "jsonb", "quality_score": "numeric(3,2)",
	"stream_chunk_errors": "integer", "stream_chunks_sent": "integer", "attachments": "jsonb",
	"request_type": "text", "request_class": "text", "due_at": "timestamptz",
}

// buildSessionProjectionExprs composes the 113 session-branch expressions by
// canonical column name: details positions flip between NULL placeholders
// (710 shape) and d.<col> (734 shape); everything else stays positional with
// projectionExprsV2.
func buildSessionProjectionExprs(withDetails bool) []string {
	out := make([]string, len(projectionExprsV2))
	for i, name := range canonicalColumnOrderV2 {
		if withDetails {
			if _, ok := detailsProjectionColumns[name]; ok {
				out[i] = "d." + name
				continue
			}
		}
		out[i] = projectionExprsV2[i]
	}
	return out
}

// sessionFamilyProjection renders the named projection (details-aware). Both
// the view-ensure chain and native readers go through this one composer.
func sessionFamilyProjection(withDetails bool) string {
	exprs := buildSessionProjectionExprs(withDetails)
	if len(exprs) != len(canonicalColumnOrderV2) {
		panic(fmt.Sprintf("session projection drift: %d exprs vs %d contract names",
			len(exprs), len(canonicalColumnOrderV2)))
	}
	parts := make([]string, len(exprs))
	for i, expr := range exprs {
		parts[i] = expr + " AS " + canonicalColumnOrderV2[i]
	}
	return strings.Join(parts, ",\n\t\t")
}

// canonicalV2Comment 与迁移 734 的 COMMENT ON VIEW 同文（COMMENT 的 IS 只收
// 单个字面量，不能像 SQL 赋值那样 || 拼接）。
const canonicalV2Comment = `'会话存储解耦 v3（734）: 710 拼装体升级——session 分支 LEFT JOIN session_turn_details(_hot)（733 特征层，键 tenant_id+request_id+partition_date），30 个 NULL 占位列换真实特征列（client_model/quality_*/stream_chunk_errors/request_class 等）。LEFT 语义：details 缺行时 NULL，与 710 逐位兼容。仍 NULL：id/test_col/test_tab_indent/provider_model。'`

// canonicalColumnOrderV2 是 113 列的契约顺序（= 现网 canonical 视图列序）。
// UNION ALL 按位置匹型：会话分支按此顺序展开投影；v1 分支内层（包装链 +
// lateral）在冻结/动态两形态下列序不同（fp/raw 位置漂移），故外面包一层
// 按名归一化的显式投影——两形态下内层都恰好携带全部 113 个名字。
var canonicalColumnOrderV2 = []string{
	"id", "request_id", "ts", "tenant_id", "application_id", "api_key_id",
	"end_user_id", "client_model", "outbound_model", "credential_id",
	"provider_id", "canonical_id", "client_profile", "request_mode",
	"prompt_tokens", "completion_tokens", "total_tokens", "cost_usd",
	"latency_ms", "success", "error_kind", "search_text", "cache_read_tokens",
	"cache_write_tokens", "identity_hash", "virtual_client_id", "virtual_ip",
	"virtual_mac", "affinity_hit", "stream_first_chunk_ms", "stream_chunk_count",
	"stream_interrupted", "stream_done_sent", "request_checksum",
	"response_checksum", "transform_rule_id", "egress_protocol",
	"failure_stage", "failure_detail_code", "request_preview",
	"transform_summary", "response_preview", "stream_done_received",
	"cost_display", "cost_currency", "usage_source", "gw_session_id",
	"gw_task_id", "request_status", "api_key_prefix", "owner_user",
	"application_code", "key_alias", "api_key_owner_user", "is_auto_request",
	"task_type", "auto_profile", "auto_decision", "auto_confidence",
	"work_type", "task_type_chosen", "confidence_num", "model_chosen",
	"strategy_used", "credits_charged", "parent_request_id",
	"compression_reason", "compression_strategy", "compression_meta",
	"outbound_msg_count", "outbound_token_est", "outbound_msg_hashes",
	"quality_flags", "quality_fix_actions", "quality_score",
	"upstream_finish_reason", "tool_calls", "client_endpoint", "client_timeout",
	"stream_chunk_errors", "stream_chunks_sent", "client_request_id",
	"upstream_status_code", "test_col", "test_tab_indent", "provider_model",
	"attachments", "has_attachments", "attachment_count", "routing_attempts",
	"routing_summary", "agent_name", "agent_type", "client_protocol",
	"canonical_model", "t0_arrived_at", "t1_total_enqueued_at",
	"t2_total_dequeued_at", "t3_model_enqueued_at", "t4_model_dequeued_at",
	"t5_cred_enqueued_at", "t6_cred_dequeued_at", "t7_forward_start_at",
	"t8_response_start_at", "t9_response_end_at", "request_type",
	"is_final_success", "origin_actor", "customer_id", "request_class",
	"due_at", "system_fingerprint", "raw_model_name",
}

// canonicalV2DDL 组装 710/734 视图体。baseHasFP/baseHasRaw 描述基础包装
// （request_logs_with_current_month_without_customer_id）是否已自带
// system_fingerprint/raw_model_name——冻结链（577/610 时代交集，生产/本机
// 现网）不带，v1 分支按 700 形态 lateral 追加 4 列；动态重建链（680 引导/
// 自愈，post-603 交集）自带，追加会重复列，v1 分支只 lateral 追加
// request_class/due_at。两形态下 v1 分支内层恒携带全部 113 个列名（顺序
// 不同：动态链 fp/raw 位于 customer/class/due 之前），外层按
// canonicalColumnOrderV2 按名归一化后与会话分支按位置对齐。反连接守卫走
// idx_session_turns_request / idx_session_turns_hot_request。
//
// hasDetails（734）：true 时 session 分支 LEFT JOIN session_turn_details(_hot)
// 特征层（键 tenant_id+request_id+partition_date），30 个 NULL 占位换 d.<col>；
// false（733 未跑的陈旧库）回退 710 形态，零 d.* 引用。
func canonicalV2DDL(baseHasFP, baseHasRaw, hasDetails bool) string {
	appendCols := []string{"source.request_class", "source.due_at"}
	if !baseHasFP {
		appendCols = append(appendCols, "source.system_fingerprint")
	}
	if !baseHasRaw {
		appendCols = append(appendCols, "source.raw_model_name")
	}
	lateralCols := []string{"h.request_class", "h.due_at"}
	if !baseHasFP {
		lateralCols = append(lateralCols, "h.system_fingerprint")
	}
	if !baseHasRaw {
		lateralCols = append(lateralCols, "h.raw_model_name")
	}
	parentCols := make([]string, len(lateralCols))
	for i, c := range lateralCols {
		parentCols[i] = "p" + strings.TrimPrefix(c, "h")
	}
	proj := sessionFamilyProjection(hasDetails)
	hotJoin := ""
	parentJoin := ""
	if hasDetails {
		hotJoin = `
	LEFT JOIN public.session_turn_details_hot d
	  ON d.tenant_id = t.tenant_id
	 AND d.request_id = t.request_id
	 AND d.partition_date = t.partition_date`
		parentJoin = `
	LEFT JOIN public.session_turn_details d
	  ON d.tenant_id = t.tenant_id
	 AND d.request_id = t.request_id
	 AND d.partition_date = t.partition_date`
	}
	return fmt.Sprintf(`
	CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
	SELECT %s
	FROM public.session_turns_hot t%s
	UNION ALL
	SELECT %s
	FROM public.session_turns t%s
	UNION ALL
	SELECT %s
	FROM (
		SELECT v.*, %s
		FROM public.request_logs_with_current_month_without_request_class_due_at v
		LEFT JOIN LATERAL (
			SELECT %s
			FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT %s
			FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) source ON true
	) rl
	WHERE NOT EXISTS (SELECT 1 FROM public.session_turns_hot th WHERE th.request_id = rl.request_id)
	  AND NOT EXISTS (SELECT 1 FROM public.session_turns tp WHERE tp.request_id = rl.request_id)`,
		proj, hotJoin,
		proj, parentJoin,
		strings.Join(canonicalColumnOrderV2, ", "),
		strings.Join(appendCols, ", "),
		strings.Join(lateralCols, ", "),
		strings.Join(parentCols, ", "))
}

// SessionFamilyTurnsSourceSQL returns a parenthesized FROM-source emitting the
// frozen 113-column request-logs shape straight from the session-turn family
// (hot ∪ parent, details-joined per 734), for S3 wave-1 native readers
// (plan §4-S3: admin 日志读端分波去视图化). It reuses
// sessionFamilyProjection(true) so the view ensure chain and native readers
// share one copy of the column-mapping contract.
//
// Callers require migration 733 (session_turn_details family) to be applied —
// native readers run post-migration by construction (startup migrations
// precede serving).
//
// The returned source carries no alias — callers append one (e.g. `rl`).
// Unlike the 710 view it omits the frozen-v1 branch and anti-join entirely:
// rows promoted out of request_logs history are NOT covered, so native mode
// is only correct while request_logs still holds every not-yet-retired row
// (i.e. before S4 stop-write + TTL retirement of windows that predate the
// session mirror).
func SessionFamilyTurnsSourceSQL() string {
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date)"
}
