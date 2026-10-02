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
//	grep -rniE "from[[:space:]]+request_logs(_[a-z_]+)?\b" --include=*.go . \
//	  | grep -v "_test\.go" | grep -v "^\./docs"
//
//	277 命中 − 40 条注释行 = **237 真实调用点 / 104 个文件**。
//	注释行必须排除：40 条里 36 条是小写 `from`，不剔会得 277 而非 237。
//
// # 刻意不自动分类
//
// 我试过按「语句窗口内有无 gw_session_id / request_id =」自动分 A/B/C/D，
// 拿 §5.5.5 已人工核定的 14 个点交叉验证，**判错 5 个**，且错得最重的
// `session_turns_tree.go` / `session_turns_unified.go`（`parent_request_id
// = ANY` 被判成「无会话头谓词」）。**所以这道门只数个数，不下判定。**
var requestLogsReadInventory = map[string]int{
	"admin/analytics.go":                    2,
	"admin/attachments_routes.go":           1,
	"admin/attempt_quality_api.go":          1,
	"admin/auto_route.go":                   1,
	"admin/auto_route_correlations.go":      5,
	"admin/auto_title_generator.go":         2,
	"admin/body_resolver.go":                2,
	"admin/compression_sessions.go":         3,
	"admin/compression_stats.go":            4,
	"admin/credential_monitor.go":           3,
	"admin/credential_monitor_heatmap.go":   1,
	"admin/credential_success_rate.go":      2,
	"admin/data_lifecycle.go":               15,
	"admin/data_lifecycle_attachments.go":   6,
	"admin/data_lifecycle_blobs.go":         3,
	"admin/data_lifecycle_metrics.go":       1,
	"admin/diagnostics_credential.go":       1,
	"admin/live_stream_sse.go":              2,
	"admin/logs.go":                         5,
	"admin/logs_summary.go":                 2,
	"admin/memora_handlers.go":              4,
	"admin/model_routing_diagnostic.go":     1,
	"admin/model_status.go":                 2,
	"admin/no_topic_session.go":             4,
	"admin/probe_history.go":                1,
	"admin/provider_diagnose.go":            2,
	"admin/provider_models.go":              2,
	"admin/providers.go":                    1,
	"admin/quality_correlations.go":         1,
	"admin/request_trace.go":                6,
	"admin/route_incidents.go":              1,
	"admin/routing.go":                      1,
	"admin/session_analytics_breakdown.go":  2,
	"admin/session_analytics_timeseries.go": 3,
	"admin/session_bodies_batch.go":         2,
	"admin/session_detail_v2.go":            2,
	"admin/session_extract.go":              3,
	"admin/session_management_api.go":       1,
	"admin/session_sanitize_matches.go":     2,
	"admin/session_summary_v2.go":           1,
	"admin/session_tenant.go":               2,
	"admin/session_timeline_query.go":       1,
	"admin/session_title.go":                1,
	"admin/session_turns_tree.go":           1,
	"admin/session_turns_unified.go":        1,
	"admin/swim_lane_init.go":               1,
	"admin/telemetry.go":                    1,
	"admin/tenants.go":                      4,
	"admin/top_problems.go":                 2,
	"admin/unified_detail.go":               6,
	"admin/usage.go":                        3,
	// 2026-10-02: usage trend-series detail 路径读当月视图（与 board fallback 同源）。
	"admin/usage_trend_series.go":                     2,
	"admin/work_types.go":                             4,
	"autoroute/recommend_v2.go":                       2,
	"bg/auto_index_refresher.go":                      4,
	"bg/auto_route_affinity_worker.go":                2,
	"bg/auto_route_settle_worker.go":                  2,
	"bg/candidate_failure_monitor.go":                 2,
	"bg/credential_recovery.go":                       2,
	"bg/credential_selfcheck.go":                      3,
	"bg/daily_probe_audit.go":                         1,
	"bg/integrity_fingerprint_drift.go":               1,
	"bg/integrity_fingerprint_probe.go":               2,
	"bg/ledger_reconciliation.go":                     1,
	"bg/lite_retention_worker.go":                     2,
	"bg/model_probe.go":                               3,
	"bg/model_tier.go":                                1,
	"bg/passive_probe_listener.go":                    4,
	"bg/shared_pick.go":                               1,
	"bg/stats_minute_rollup.go":                       3,
	"bg/stats_minute_rollup_retire.go":                3,
	"bg/today_success_probe.go":                       1,
	"cmd/compression-bench/main.go":                   1,
	"cmd/gateway/dual_read_validator.go":              4,
	"cmd/gateway/main_v3_wiring.go":                   1,
	"cmd/gateway/output_compliance_control.go":        1,
	"cmd/gateway/waterfall_by_request.go":             1,
	"cmd/gateway/waterfall_db.go":                     1,
	"cmd/scenario_driver/main.go":                     4,
	"cmd/tools/backfill_session_bodies/main.go":       2,
	"cmd/tools/validate_sessions_v2/loader.go":        4,
	"cmd/traffic-replay/main.go":                      1,
	"db/db.go":                                        3,
	"db/probe_views_unified.go":                       3,
	"discovery/discovery.go":                          1,
	"domains/analysis/optimizer.go":                   3,
	"domains/analysis/request_summary.go":             1,
	"domains/attachments/handler.go":                  1,
	"domains/credentialstate/popularity_tracker.go":   1,
	"domains/hooks/goal/history_store.go":             1,
	"domains/hooks/observability/telemetry/client.go": 5,
	"domains/providerprofile/adapters.go":             4,
	"domains/routeincident/store.go":                  1,
	"domains/sessionforensics/export.go":              3,
	"domains/sessionsummary/summarizer.go":            3,
	"domains/sessionsummary/system_prompt_prefix.go":  1,
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
var requestLogsReadPattern = regexp.MustCompile(`(?i)from\s+request_logs(_[a-z_]+)?\b`)

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
