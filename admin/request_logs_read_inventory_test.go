package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// requestLogsReadInventory is the **complete** list of production files that read
// the request_logs family, with the number of read call sites in each.
//
// # 为什么要有这张表
//
// 审计 §5.5.5「会话域视图依赖的最终分类」覆盖 14 个文件，而实际读
// `request_logs*` 的生产文件有 104 个、237 个调用点（2026-10-01 实测）。
// §5.5.8 的标题写的是「第一版表不完整，逐条补齐」——**但那张表从未覆盖到
// 其余 90 个文件，且它自称补全过**。没有任何机制会在这个缺口扩大时报红。
//
// S4（`storage.request_logs_write_enabled=false`）会让这 237 处全部读到
// 不再增长的数据。在逐点评估完成之前，**这张表的作用不是分类，是钉住覆盖面**：
// 新增一个读 request_logs 的文件、或某个文件的读点数变了，这道门立刻变红，
// 逼迫做一次显式复核，而不是让清单在无人察觉的情况下继续漂移。
//
// # 口径（换正则就不是这个数，改这里之前先读审计 §8.5）
//
//	grep -rniE "(from|join)[[:space:]]+request_logs(_[a-z_]+)?\b" --include=*.go . \
//	  | grep -v "_test\.go" | grep -v "^\./docs"
//
//	2026-10-01 实测：**277 命中 − 40 条注释行 = 237 真实调用点 / 104 个文件**。
//	注释行必须排除：40 条里 36 条是小写 `from`，不剔会得 277 而非 237。
//
//	2026-10-05 加 `join`（理由见 requestLogsReadPattern 的注释）：
//	**109 文件 / 271 调用点**。差的 3 文件是只 JOIN 不 FROM 的活读方，
//	差 31 调用点是已登记文件里此前未被计入的 JOIN 腿。
//
// ⚠ 这两个数**都是历史值**：仓库每天在动，`TestRequestLogsReadInventoryIsComplete`
// 的红绿以本门当前扫描结果为准，本注释只记录口径变更的历史与理由。
//
// # 刻意不自动分类
//
// 我试过按「语句窗口内有无 gw_session_id / request_id =」自动分 A/B/C/D，
// 拿 §5.5.5 已人工核定的 14 个点交叉验证，**判错 5 个**，且错得最重的
// `session_turns_tree.go` / `session_turns_unified.go`（`parent_request_id
// = ANY` 被判成「无会话头谓词」）。**所以这道门只数个数，不下判定。**
var requestLogsReadInventory = map[string]int{
	"admin/analytics.go":                    2,
	"admin/attachments_routes.go":           2,
	"admin/attempt_quality_api.go":          1,
	"admin/auto_route.go":                   1,
	"admin/auto_route_outcome_freshness.go": 1,
	"admin/auto_route_correlations.go":      5,
	"admin/auto_title_generator.go":         3,
	"admin/body_resolver.go":                4,
	"admin/compression_sessions.go":         5,
	"admin/compression_stats.go":            7,
	"admin/credential_monitor.go":           3,
	"admin/credential_monitor_heatmap.go":   1,
	"admin/credential_success_rate.go":      2,
	"admin/data_lifecycle.go":               16,
	"admin/data_lifecycle_attachments.go":   6,
	"admin/data_lifecycle_blobs.go":         5,
	"admin/data_lifecycle_metrics.go":       1,
	"admin/diagnostics_credential.go":       1,
	"admin/live_stream_sse.go":              2,
	"admin/logs.go":                         5,
	"admin/logs_summary.go":                 4,
	"admin/memora_handlers.go":              5,
	"admin/model_routing_diagnostic.go":     1,
	"admin/model_status.go":                 2,
	"admin/no_topic_session.go":             6,
	"admin/probe_history.go":                1,
	"admin/provider_diagnose.go":            2,
	"admin/provider_models.go":              2,
	"admin/providers.go":                    1,
	"admin/quality_correlations.go":         2,
	"admin/request_trace.go":                6,
	"admin/route_incidents.go":              1,
	"admin/routing.go":                      1,
	// 2026-10-05：只 JOIN 不 FROM，此前对本门**完全不可见**（假零）。
	// 会话对比 API 的 bodies 腿；turn 腿早已走会话族。
	"admin/session_analytics_breakdown.go":  2,
	"admin/session_analytics_timeseries.go": 3,
	"admin/session_bodies_batch.go":         2,
	"admin/session_detail_v2.go":            2,
	// 同上：只剩 JOIN 不 FROM。
	// ⚠ §9.230 起**会话导出/对比那两个**（原 session_compare.go / session_export.go）
	// 已不在本表：它们的 bodies 腿改走 sessionBodiesFromSQL()，源码里不再有
	// v1 关系名字面量。v1 那一臂的登记在 indirectRequestLogsReaders
	// （admin/session_bodies_source.go）。
	"admin/session_extract.go":        3,
	"admin/session_management_api.go": 1,
	// 同上：`JOIN request_logs_with_current_month rl ON rl.id = slr.last_request_id`，
	// 为在线会话列表补 tenant_id —— 走的是 canonical 视图的 v1 臂。
	"admin/session_online.go":           1,
	"admin/session_sanitize_matches.go": 3,
	"admin/session_summary_v2.go":       1,
	"admin/session_tenant.go":           2,
	"admin/session_timeline_query.go":   1,
	"admin/session_title.go":            2,
	"admin/session_turns_tree.go":       1,
	"admin/session_turns_unified.go":    1,
	"admin/swim_lane_init.go":           1,
	"admin/telemetry.go":                1,
	"admin/tenants.go":                  4,
	"admin/top_problems.go":             2,
	"admin/unified_detail.go":           6,
	"admin/usage.go":                    3,
	// 2026-10-02: usage trend-series detail 路径读当月视图（与 board fallback 同源）。
	"admin/usage_trend_series.go": 2,
	// 2026-10-03: usage_enhanced.go 读 request_logs 两处，但扫描器只数到 1 处 ——
	//
	//   ① usageCacheEconomics 的压缩请求数（"FROM request_logs rl"，SQL 字面量）
	//   ② usageCostTrend 的 work_type / intent 基表（planCostTrend 里的
	//      Go 字符串 BaseTable: "request_logs rl"）
	//
	// ② 不是 SQL 文本，requestLogsReadPattern 的 `from\s+request_logs` 看不见它。
	// 本表登记的是**扫描器测到的值** 1，不是真实调用点数 2 —— 别把它读成
	// 「这里只读了一处」。
	//
	// ② 这条路径由另一道门从结构上覆盖：planCostTrend 的维度归属断言
	// （TestPlanCostTrend_DimensionProvenance）直接断言 work_type/intent 的
	// 基表就是 request_logs rl，比 grep 计数强。扫描器看不见 ≠ 没有覆盖。
	// 要把 ② 也数进来，得把模式扩到「裸表名字符串」，那会改动本表其余条目
	// （审计 §8.5），留给需要它的那一轮单独评估。
	"admin/usage_enhanced.go":                   1,
	"admin/work_types.go":                       4,
	"autoroute/recommend_v2.go":                 2,
	"bg/auto_index_refresher.go":                4,
	"bg/auto_route_affinity_worker.go":          2,
	"bg/candidate_failure_monitor.go":           2,
	"bg/credential_recovery.go":                 2,
	"bg/credential_selfcheck.go":                3,
	"bg/daily_probe_audit.go":                   1,
	"bg/integrity_fingerprint_drift.go":         1,
	"bg/integrity_fingerprint_probe.go":         2,
	"bg/ledger_reconciliation.go":               1,
	"bg/lite_retention_worker.go":               2,
	"bg/model_probe.go":                         3,
	"bg/model_tier.go":                          1,
	"bg/passive_probe_listener.go":              5,
	"bg/shared_pick.go":                         1,
	"bg/stats_minute_rollup.go":                 3,
	"bg/stats_minute_rollup_retire.go":          3,
	"bg/today_success_probe.go":                 1,
	"cmd/compression-bench/main.go":             2,
	"cmd/gateway/dual_read_validator.go":        4,
	"cmd/gateway/main_v3_wiring.go":             1,
	"cmd/gateway/output_compliance_control.go":  1,
	"cmd/gateway/waterfall_by_request.go":       1,
	"cmd/gateway/waterfall_db.go":               1,
	"cmd/scenario_driver/main.go":               4,
	"cmd/tools/backfill_session_bodies/main.go": 2,
	// 第 6 处 = HasV1RowsInRange（§R44/移交.1 端边界守门的实测探测，R45 轮加入）：
	// LIMIT 1，家族过滤（tenant + 会话头）与同文件 LoadV1TimeRange 逐字一致——
	// 探测必须对「窗口声称要比对的行」作答，母表腿是有意的。生命周期与该 loader
	// 其余 5 处同面：S4 停写 request_logs 时 v1 校验器连同探测一起退役。
	"cmd/tools/validate_sessions_v2/loader.go":        6,
	"cmd/traffic-replay/main.go":                      1,
	"db/db.go":                                        3,
	"db/probe_views_unified.go":                       3,
	"discovery/discovery.go":                          1,
	"domains/analysis/optimizer.go":                   3,
	"domains/analysis/request_summary.go":             1,
	"domains/attachments/handler.go":                  1,
	"domains/credentialstate/popularity_tracker.go":   1,
	"domains/hooks/goal/history_store.go":             2,
	"domains/hooks/observability/telemetry/client.go": 5,
	"domains/providerprofile/adapters.go":             4,
	"domains/routeincident/store.go":                  1,
	"domains/sessionforensics/export.go":              5,
	"domains/sessionsummary/summarizer.go":            5,
	"domains/sessionsummary/system_prompt_prefix.go":  2,
	"domains/streaming/anomaly_harvester.go":          1,
	"domains/streaming/model_alternatives.go":         1,
	"internal/collector/gateway_adapters.go":          3,
	"internal/quality/minute_aggregator.go":           1,
	"internal/summarystore/store.go":                  2,
	"internal/trace/trace.go":                         2,
	"storage/sqlite/request_log_store.go":             2,
	"tests/session_audit/cmd/audit-test/main.go":      1,
	"tests/test_popularity_tracker.go":                2,
}

// TestRequestLogsReadInventoryIsComplete fails when the set of production files
// reading request_logs drifts away from the table above.
//
// It fails on BOTH directions, and that is the point:
//
//   - a new file appears  → someone started reading the table S4 would stop
//     writing; it has not been assessed;
//   - a listed file loses a call site (or disappears) → the table is stale and
//     every number downstream of it is stale too.
//
// A guard that only fires on additions looks like coverage while the real risk
// is drift in both directions.
func TestRequestLogsReadInventoryIsComplete(t *testing.T) {
	root := repoRootFromCaller(t)
	actual := scanRequestLogsReaders(t, root)

	var missing, stale []string
	for file, n := range actual {
		want, ok := requestLogsReadInventory[file]
		switch {
		case !ok:
			missing = append(missing, file)
		case want != n:
			stale = append(stale, file+": table says "+strconv.Itoa(want)+
				", code has "+strconv.Itoa(n))
		}
	}
	for file, want := range requestLogsReadInventory {
		if _, ok := actual[file]; !ok {
			stale = append(stale, file+": table says "+strconv.Itoa(want)+", code has 0")
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	total := 0
	for _, n := range actual {
		total += n
	}
	t.Logf("request_logs readers: %d files / %d call sites (table: %d files / %d call sites)",
		len(actual), total, len(requestLogsReadInventory), sumInventory())

	if len(missing) > 0 {
		t.Errorf("%d production file(s) read request_logs but are absent from "+
			"requestLogsReadInventory — S4 would silently stop feeding them:\n  %s\n"+
			"Add them to the table with their call-site count and assess each one "+
			"(audit §8.5); do not paper over this by widening the scan.",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("requestLogsReadInventory is stale — every count derived from it is stale too:\n  %s\n"+
			"Re-scan and re-review the affected file(s) before updating the table.",
			strings.Join(stale, "\n  "))
	}
}

func sumInventory() int {
	n := 0
	for _, v := range requestLogsReadInventory {
		n += v
	}
	return n
}

// requestLogsReadPattern is the Go-side twin of the grep in the file comment.
// Go's regexp has no lookaround, so comment filtering happens in scanRequestLogsReaders.
//
// # 2026-10-05：`from` 独占 ⇒ 三个活着的 API 读方对本门**完全不可见**
//
// 原模式是 `from\s+request_logs(_[a-z_]+)?\b`，即**只有 FROM 算读点**。
// 一段 SQL 只要不写 FROM、只写 JOIN，它在本门眼里就是**零读点** ——
// 不是「少算了一个」，是「整个文件不进总体」。实测抓到 3 个：
//
//	admin/session_online.go    :112  JOIN request_logs_with_current_month rl
//	admin/session_compare.go   :903  LEFT JOIN request_logs_bodies_with_current_month rb
//	admin/session_export.go    :227  LEFT JOIN request_logs_bodies_with_current_month rb
//
// 三条都是**活的查询**（不是注释、不是死代码），而 `admin/session_compare.go`
// 与 `admin/session_export.go` 是**会话导出/对比 API 仍然挂在 v1 上的唯一原因**：
// 它们的 turn 腿早已走会话族（`SessionFamilyTurnsForSessionSQL()`），
// 剩下的 bodies 腿还是 `request_logs_bodies_with_current_month`。
//
// 方向：这是**假零**。它让「request_logs 有 106 个读方」这句话在退役清单上
// 少了三个，而少的正好是退役时才会暴露的那类（§9.199「已 repoint 的读方」
// 与「从未被分析的文件」混在一个 clean 桶里的同族，但成因在**扫描器**而不是分类器）。
//
// ⇒ 模式扩成 `from|join`。**不是**加一条「已知例外」清单——那只是把同一个洞
// 从扫描器搬到登记表，而登记表需要人记得更新；JOIN 到 v1 关系**按定义**就是
// 一个 v1 读点，没有误报空间。
//
// # 2026-10-05：同一批里的两个文件在本表**退场**（审计 §9.230）
//
// 上面那张名单里的 `admin/session_compare.go` / `admin/session_export.go`
// 在本轮被改成经 `sessionBodiesFromSQL()` 取 bodies 源，于是它们源码里
// **不再有** v1 关系名的字面量 ⇒ 本表（按行正则扫字面量）测到 0 处。
//
// ⚠ **「本表测到 0」不等于「它不读 v1」**——开关默认那一臂就是
// `return "request_logs_bodies_with_current_month rb"`。把这两条从本表删掉
// 之前必须先确认它们在别处有家，否则就是 §9.45「把不知道报成安全」的同一次复发。
// ⇒ 处置按本仓**已有**的两张表分工，不新造第三套：
//
//	字面量读方 → requestLogsReadInventory（本表）
//	间接读方   → indirectRequestLogsReaders（§9.49 建的那张）
//	两者并集   → allKnownRequestLogsReaderFiles()
//
// 与 `bg/auto_route_settle_sql.go` 完全同形：那个文件同样因为关系名在 Go
// 标识符里而不在本表，登记在间接表。⇒ 本表从 109 降到 107，**不是读方变少了**。
var requestLogsReadPattern = regexp.MustCompile(`(?i)(from|join)\s+request_logs(_[a-z_]+)?\b`)

func scanRequestLogsReaders(t *testing.T, root string) map[string]int {
	t.Helper()
	out := map[string]int{}
	skipDir := map[string]bool{".git": true, "docs": true, "node_modules": true, "vendor": true}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		n := 0
		for _, line := range strings.Split(string(src), "\n") {
			if !requestLogsReadPattern.MatchString(line) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			// 注释不是调用点。40 条注释里 36 条是小写 `from`，
			// 不剔会得 277 而非 237（审计 §8.5）。
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			n++
		}
		if n > 0 {
			out[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return out
}

// repoRootFromCaller walks up from this file to the directory holding go.mod.
func repoRootFromCaller(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}
