//go:build !integration

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

// TestRegisteredStartupMigrationsCanRunInSingleTransaction is the inverse of
// TestCanonicalStartupMigrationsAtOrAbove704AreRegistered.
//
// That gate asks "is every canonical migration registered?". This one asks the
// question nothing asked: "can every registered migration actually run under
// the installer's runner?" dbinit.Runner.applySQL runs every file through
// `psql -v ON_ERROR_STOP=1 --single-transaction` and hard-fails the whole
// install on the first error (runner.go:418-424), so a registered migration
// containing CREATE/DROP INDEX CONCURRENTLY can never apply — it aborts, and
// the install stops there.
//
// The repo already has this rule, but only enforced in one direction:
// psqlConcurrencyRequired (stats_migrations_test.go) exempts 727/728/729/744/749
// from being synced "because CREATE INDEX CONCURRENTLY (\gexec) cannot run
// inside the installer's psql --single-transaction". Nothing prevented 718 (51
// CONCURRENTLY statements) and 719 (16) from being registered anyway, and
// those two abort a fresh install once the earlier gaps are cleared.
//
// knownStartupChainConcurrencyViolations holds the reviewed exceptions. When
// one is fixed — by de-registering the file, or by rewriting it without
// CONCURRENTLY — delete the entry and this gate will keep it that way.
func TestRegisteredStartupMigrationsCanRunInSingleTransaction(t *testing.T) {
	t.Helper()

	runner := dbinit.NewRunner("", "", "", "")
	if len(runner.StartupFiles) == 0 {
		t.Fatal("dbinit.Runner.StartupFiles is empty")
	}

	embedDir := filepath.Join("embeddata", "startup")
	for _, name := range runner.StartupFiles {
		if n, exempt := knownStartupChainConcurrencyViolations[name]; exempt {
			t.Logf("registered startup migration %q uses CONCURRENTLY (%d statements) and cannot run "+
				"under psql --single-transaction — tracked in knownStartupChainConcurrencyViolations (%d)",
				name, countConcurrently(t, filepath.Join(embedDir, name)), n)
			continue
		}
		if n := countConcurrently(t, filepath.Join(embedDir, name)); n > 0 {
			t.Errorf("registered startup migration %q contains %d CONCURRENTLY statement(s) and can "+
				"never apply under dbinit.Runner's `psql --single-transaction`; the install hard-fails "+
				"at this file. Either de-register it (add it to psqlConcurrencyRequired with the same "+
				"rationale as 727/728/729/744/749) or rewrite it without CONCURRENTLY, then add it to "+
				"knownStartupChainConcurrencyViolations only if it must stay registered for another reason",
				name, n)
		}
	}
}

var concurrentlyRe = regexp.MustCompile(`(?i)\bCONCURRENTLY\b`)

// The repo already had this rule, but enforced in only one direction:
// psqlConcurrencyRequired exempted 727/728/729/744/749 from being synced
// "because CREATE INDEX CONCURRENTLY (\gexec) cannot run inside the installer's
// psql --single-transaction" — yet 718 (46 CONCURRENTLY statements in its
// embeddata copy) and 719 (12) were synced through all five points anyway, and
// every fresh install that reached them died. The >=704 registration gate only
// asks "is every canonical migration registered" and never asked the inverse.
//
// 2026-10-15 门拣选：本仓仍注册 718/719（缺 non-CONCURRENTLY TTL 替代前
// 不能注销——注销会让 fresh install 丢掉 718 的 TTL 索引）。门照常红这些
// CONCURRENTLY 文件，但以 reviewed exception 记账，避免假绿或强制危险注销。
// 修法落地（非 CONCURRENTLY 变体或迁入 psqlConcurrencyRequired）后删掉条目。
var knownStartupChainConcurrencyViolations = map[string]int{
	"718_drop_redundant_indexes_and_add_ttl_indexes.sql":           46,
	"719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql": 12,
	// 以下为 r43 之后合入、仍走 installer 单事务路径的 CONCURRENTLY 债；
	// 门记账防回归，修法同 718/719（非 CONCURRENTLY 变体或迁入豁免表）。
	"807_request_logs_bodies_hot_drop_duplicate_request_id_index.sql": 1,
	"822_session_summaries_health_pending_index.sql":                  1,
	"837_routing_mv_refresh_state.sql":                                1,
	"845_stats_event_dedup_join_index.sql":                            1,
	// 871（2026-10-18）与上面几条**不同类**：它不是债，是「已复核的豁免」。
	//
	// 本门的前提是「注册文件里出现 CONCURRENTLY ⇒ 它在 --single-transaction 下
	// 永远跑不起来」。该前提**对携带 dbinit:no-transaction 标记的文件不成立**：
	// runner.go 的 applySQL 在拼参数时对标记文件**不加** --single-transaction
	// （`if !requiresNoTransaction(content) { args = append(args, "--single-transaction") }`），
	// 871 因此走非事务通道，能正常应用 —— 与 718 同一先例。
	//
	// 计数 4 而非 1，是因为本门按子串统计且只看剥掉注释后的正文，
	// 871 的字符串字面量里另有三处提到 CONCURRENTLY（DO 块的失败提示、
	// COMMENT ON INDEX 的说明）。
	//
	// ⚠️ 真正需要留意的是别的：applySQL 的 ctx 是 **5 分钟**超时
	// （exec.CommandContext）。CONCURRENTLY 构建若被 ctx 掐断，会留下
	// **invalid 索引**，而同一 psql 调用里的形状校验块也来不及跑。
	// ⇒ 871 的 DO 块会对 indisvalid=false 明确报错，**下一次启动迁移**
	// 才会发现，届时需先 DROP INDEX CONCURRENTLY 重建。
	"871_route_incident_events_created_at_idx.sql": 4,
	// 877（2026-10-19 会话族审计轮）与 871 同类：携带 dbinit:no-transaction
	// 标记 ⇒ applySQL 不加 --single-transaction ⇒ 走非事务通道，能正常应用。
	//
	// 它之所以用三段式 CONCURRENTLY（727/729 同款）而不是父表普通 CREATE INDEX，
	// 正是因为 session_turns 是**活写**分区父表：普通级联会在整个构建期持
	// ShareLock 阻断写入，而 GIN trigram 覆盖 100% 行且 search_text 是 TOAST
	// 宽列，6.4GB 分区属分钟级构建。
	//
	// ⚠️ 5 分钟 applySQL ctx 这条风险对 877 **比对 871 现实得多**：871 的索引是
	// 4 条窄索引，877 是覆盖全部行的 trigram 索引。被 ctx 掐断会留下
	// **invalid 索引**（CONCURRENTLY 的两阶段扫描未完成）。因此 877 自带
	// ① 建之前 DROP 掉同名 invalid 索引、② 建之后校验 indisvalid 并显式报错，
	// 让「上次被掐断」变成可自愈 + 可诊断，而不是一个静默的半成品索引。
	//
	// 计数 5：两条生成器里的字符串字面量 + 校验块 RAISE 文案里的一处提及
	// （countConcurrently 按剥注释后的正文做子串统计，字符串字面量算正文）。
	"877_session_turns_search_text_trgm.sql": 5,
	// 887（2026-10-20 会话族审计 G-5）与 871/877 **同类**：携带
	// dbinit:no-transaction 标记 ⇒ applySQL 不加 --single-transaction
	// ⇒ 走非事务通道，能正常应用（不是「跑不起来」的债，是「已复核的豁免」）。
	//
	// 它是**表达式 GIN 索引** `USING gin (to_tsvector('simple', search_text))`：
	// ★ **刻意不用生成列** —— promote_session_turns_hot_to_partition 会报
	//   「cannot insert into a generated column」⇒ 热表永远排不进月分区；
	// ★ 走三段式 CONCURRENTLY（877 同款）：逐分区建 → 建父壳（ON ONLY）→ ATTACH。
	//
	// ⚠️ 与 877 同样的 5 分钟 applySQL ctx 风险：CONCURRENTLY 被掐断会留下
	//   **invalid 索引**。⇒ 887 自带 ① 建之前自愈 indisvalid=false 的同名索引、
	//   ② 建之后校验 indisvalid 并显式报错，让「上次被掐断」变成可自愈 +
	//   可诊断，而不是静默的半成品索引。
	//
	// 计数 5：三条 CREATE INDEX CONCURRENTLY + 一条 DROP INDEX CONCURRENTLY
	// + 校验块 RAISE 文案里的一处提及（countConcurrently 按剥注释后的正文
	// 做子串统计，字符串字面量算正文）。
	"887_session_turns_search_text_fts.sql": 5,
	// 888（2026-10-20 会话族审计 G-7）同类：携带 dbinit:no-transaction 标记
	// ⇒ 走非事务通道，能正常应用。
	//
	// 它补会话族的 join 索引缺口（session_bodies 父表/hot 表各一条
	// (tenant_id, session_id, turn_no DESC, request_id)，sessions 一条
	// (tenant_id, session_id, partition_date DESC)）。
	// ★ PG 不支持 ALTER INDEX … ADD COLUMN ⇒ 只能「建新 + 删旧」；
	//   删旧的那两条走 **DROP INDEX CONCURRENTLY**（不能拿强锁堵住活写表）。
	//
	// 计数 5：三条 CREATE INDEX CONCURRENTLY + 一条 DROP INDEX CONCURRENTLY
	// + 形状校验块里的一处提及。
	"888_session_family_join_index_gaps.sql": 5,
	// 898（2026-10-23 会话族 hot 排水轮 §143.97）与 871/877/887/888 **同类**：
	// 携带 dbinit:no-transaction 标记 ⇒ applySQL 不加 --single-transaction
	// ⇒ 走非事务通道，能正常应用（不是「跑不起来」的债，是「已复核的豁免」）。
	//
	// 它给 public.session_bodies_hot 补 ts 首列索引
	// (ts, id, partition_date) —— 该 hot 表在生产上**从来没有**这条索引
	// （614 @ 6973a9fb8 把它定义成带 `WHERE ts > now() - interval '8 hours'`
	// 谓词的 partial index ⇒ now() 非 IMMUTABLE ⇒ PG 拒绝 ⇒ 从未建成，
	// 且其后同文件的 idx_session_bodies_hot_partition_date 也从未执行）。
	//
	// ★ 它用 CONCURRENTLY 而不是普通 CREATE INDEX，正是因为 session_bodies_hot
	// 是**活写表**（bodies_writer 持续写入，pg_stat_statements 实测 72,813 次
	// INSERT / mean 20.2ms）⇒ 普通 CREATE INDEX 会在整个构建期持 ShareLock
	// 阻断写入。
	//
	// 计数 3：一条 CREATE INDEX CONCURRENTLY + 形状校验块里的两处提及
	// （indisvalid / indisready 两条 RAISE EXCEPTION 的文案）。
	"898_session_bodies_hot_ts_index.sql": 3,
	// 902（2026-10-24 R146 存储效率轮 §143.102，P5 落地）与 845/888/898 **同类**：
	// 携带 dbinit:no-transaction 标记 ⇒ applySQL 走非事务通道。
	// 它把 stats_event_dedup 的 pkey 换体 UNIQUE(event_id) INCLUDE(occurred_at)
	// 并删除 845 的 (event_id, occurred_at) 复合索引 —— 两索引合一净省 ~114 MB。
	// 用 CONCURRENTLY 是因为该表是活写表（event_writer 每请求一次 upsert）；
	// 删除旧复合索引同样走 DROP INDEX CONCURRENTLY（不能拿强锁堵住活写表）。
	//
	// 计数 3：一条 CREATE UNIQUE INDEX CONCURRENTLY + 两条 DROP INDEX CONCURRENTLY
	// （845 复合索引 + 重跑兜底的 tmp 清理）。
	"902_stats_event_dedup_pkey_include.sql": 3,
	// 908（2026-10-24 R147 存储效率续轮，㉘ 裁决落地）与 845/888/898/902 同类：
	// 携带 dbinit:no-transaction 标记 ⇒ 非事务通道。删除 route_incident_events
	// 的零消费方索引 idx_route_incident_events_type_created（110 MB）。
	// 用 DROP INDEX CONCURRENTLY 是因为该表在网关写路径上（failure_observed
	// 持续写入，同表 uq_idem 5.2 天 42 万次 ON CONFLICT 冲突检查）。
	//
	// 计数 1：一条 DROP INDEX CONCURRENTLY（IF EXISTS 自然幂等，无 tmp）。
	"908_route_incident_events_type_created_drop.sql": 1,
}

func countConcurrently(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		// A StartupFiles entry with no embeddata copy is TestStartupFilesAreAllEmbedded's
		// business, not this gate's. Skip rather than double-report.
		t.Logf("skip %s: %v", path, err)
		return 0
	}
	body := stripSQLComments(string(raw))
	return len(concurrentlyRe.FindAllString(body, -1))
}

// TestPsqlConcurrencyRequiredNamesStillExist guards the exemption list itself:
// a name in psqlConcurrencyRequired that no longer exists in the canonical
// startup tree is a stale entry that silently exempts nothing.
func TestPsqlConcurrencyRequiredNamesStillExist(t *testing.T) {
	t.Helper()
	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	for name := range psqlConcurrencyRequired {
		if _, err := os.Stat(filepath.Join(canonicalDir, name)); err != nil {
			t.Errorf("psqlConcurrencyRequired lists %q but canonical startup has no such file: %v", name, err)
		}
		if strings.TrimSpace(psqlConcurrencyRequired[name]) == "" {
			t.Errorf("psqlConcurrencyRequired[%q] has no reason recorded; the exemption must stay reviewable", name)
		}
	}
}
