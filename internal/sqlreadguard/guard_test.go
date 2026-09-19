package sqlreadguard

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// 读面纪律机制化（R46 §五#11 → R47 落地）：
//
// 生产读面对 request_logs 的裸引用（母表单腿读）对最近 8h 仍只在
// request_logs_hot 的行全盲（hot 默认仅保留 8h）。R44/R45/R46 连续三轮
// 在 admin 读面抓到同类缺陷后，本轮把纪律从"逐轮人工复核"升级为
// grep 守卫测试。合法形态只有两种：
//  1. hot∪母表双腿（显式 UNION ALL / 双 EXISTS / 双腿视图
//     request_logs_with_current_month*）；
//  2. 白名单带因豁免（LEGIT / DEBT / TOOLING，见下）。
//
// 白名单条目前缀分类（可 grep 盘点）：
//   - "LEGIT:"        有意的母表读——双腿之一、DDL 体、存储维护面、
//     SQLite 引擎、专职校验器、离线工具；
//   - "DEBT(R47):"    盲区债——真实单腿读，待逐文件双腿化；每清一个
//     从本表删除条目（守卫对"文件已无命中"的残留条目报错，防债务隐身）。
//
// 注释提及自动豁免：命中位置之前出现 "//"（Go）或行首 "--"（SQL 字符串
// 内注释），视为文档性提及而非可执行读面。
//
// 扫描范围：生产 Go 面（admin/autoroute/bg/cmd/db/discovery/domains/
// internal/provider/proxy/taskprofile 的 *.go，排除 *_test.go 与本目录）+
// sql/objects/ 与 scripts/analysis/ 的 *.sql。DDL/迁移目录
// （sql/migrations、deploy、installer）非读面，不扫。

var bareRequestLogsRe = regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+(public\.)?request_logs\b`)

var sqlReadGuardAllowFiles = map[string]string{
	// ---- LEGIT：双腿之母表腿（有意读） ----
	"admin/analytics.go":        "LEGIT: 决策回放双腿之母表腿（R46 F9，hot 腿同查询内联）",
	"admin/request_trace.go":    "LEGIT: hot∪母表双腿 UNION ALL（既定正面模板）",
	"admin/session_tenant.go":   "LEGIT: assertTaskInTenant 双 EXISTS 之母表腿（R47 修复）",
	"internal/trace/trace.go":   "LEGIT: hot∪母表双腿",
	"bg/auto_route_affinity_worker.go": "LEGIT: NOT EXISTS 探测之母表腿（R46 F4 裁决落地形态）",

	// ---- LEGIT：引擎/DDL/维护/工具 ----
	"bg/lite_retention_worker.go":       "LEGIT: SQLite 引擎 DELETE（? 占位，非 PG hot/mother 体系）",
	"cmd/gateway/dual_read_validator.go": "LEGIT: 专职双腿一致性校验器",
	"db/db.go":                           "LEGIT: routing_analytics_source DDL 体",
	"db/request_logs_view_schema.go":     "LEGIT: 视图 DDL 体",
	"admin/data_lifecycle.go":            "LEGIT: 存储生命周期维护面（全历史体积/清理属设计语义）",
	"admin/data_lifecycle_attachments.go": "LEGIT: 存储生命周期维护面",
	"admin/data_lifecycle_metrics.go":    "LEGIT: 存储生命周期维护面",
	"bg/credential_recovery.go":          "LEGIT: 恢复扫描需全历史窗口（404 二次确认 6h 终判的旧证据只在母表）",
	"cmd/tools/validate_sessions_v2/loader.go":           "TOOLING: 离线校验工具",
	"cmd/tools/backfill_sessions_v2_v2/main.go":          "TOOLING: 离线回填工具",
	"cmd/tools/backfill_session_bodies/main.go":          "TOOLING: 离线回填工具",
	"cmd/tools/backfill_session_bodies/derive.go":        "TOOLING: 离线回填工具",
	"cmd/traffic-replay/main.go":                         "TOOLING: 离线回放工具",
	"cmd/compression-bench/main.go":                      "TOOLING: 离线基准工具",

	// ---- DEBT(R47)：admin 读面盲区债（R46 §五#11 同类，待双腿化） ----
	"admin/session_list.go":               "DEBT(R47): 近窗聚合裸母表，24h 窗漏最后 8h",
	"admin/usage.go":                      "DEBT(R47): RPM 峰值桶裸母表",
	"admin/session_extract.go":            "DEBT(R47): 裸母表",
	"admin/session_analytics_timeseries.go": "DEBT(R47): 裸母表",
	"admin/session_analytics_handler.go":  "DEBT(R47): 裸母表",
	"admin/session_panorama_handler.go":   "DEBT(R47): 裸母表",
	"admin/memora_handlers.go":            "DEBT(R47): 裸母表",
	"admin/quality_correlations.go":       "DEBT(R47): 裸母表",
	"admin/provider_models.go":            "DEBT(R47): 裸母表",
	"admin/probe_history.go":              "DEBT(R47): 裸母表",
	"admin/session_sanitize_matches.go":   "DEBT(R47): 裸母表",
	"admin/session_online.go":             "DEBT(R47): JOIN 裸母表",
	"admin/session_export.go":             "DEBT(R47): 裸母表",
	"cmd/gateway/output_compliance_control.go": "DEBT(R47): 网关运行时读面裸母表",
	"cmd/gateway/main_v3_wiring.go":            "DEBT(R47): 接线读面裸母表",
	"domains/analysis/optimizer.go":            "DEBT(R47): 裸母表",
	"domains/analysis/request_summary.go":      "DEBT(R47): 裸母表",
	"domains/analysis/projectattr/store.go":    "DEBT(R47): 裸母表",
	"domains/sessionforensics/export.go":       "DEBT(R47): 裸母表",
	"domains/providerprofile/pg_reconciliation_store.go": "DEBT(R47): 裸母表",
	"domains/hooks/goal/history_store.go":      "DEBT(R47): 裸母表",
	"domains/hooks/observability/telemetry/client.go": "DEBT(R47): 裸母表",
	"autoroute/recommend_v2.go":               "DEBT(R47): 裸母表",
	"discovery/discovery.go":                  "DEBT(R47): 裸母表",
}

var sqlReadGuardAllowSQLFiles = map[string]string{
	"sql/objects/views/request_logs_with_current_month.sql":          "LEGIT: 双腿视图定义本体",
	"sql/objects/views/request_logs_bodies_progress.sql":             "LEGIT: 视图定义体",
	"sql/objects/views/v_node_switch_analysis.sql":                   "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/customer_cost_view.sql":                       "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/model_cost_per_task_view.sql":                 "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/v_timeout_effectiveness.sql":                  "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/v_continuation_effectiveness.sql":             "DEBT(R47): 视图定义裸母表",
	"sql/objects/functions/credential_most_used_model_integer_integer.sql": "DEBT(R47): 函数体裸母表",
	"sql/objects/functions/get_last_successful_request_character_varying_integer.sql": "DEBT(R47): 函数体裸母表",
	"sql/objects/tables/passive_probe_state.sql":                     "LEGIT: DDL 体",
	"sql/objects/other/backfill_request_logs_bodies_integer.sql":     "LEGIT: 回填 DDL/维护体",
}

const sqlReadGuardInlineMarker = "sqlreadguard:allow"

func TestNoBareRequestLogsMotherReads(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)

	scanDirs := []string{"admin", "autoroute", "bg", "cmd", "db", "discovery", "domains", "internal", "provider", "proxy", "taskprofile"}
	var goFiles []string
	for _, d := range scanDirs {
		dir := filepath.Join(root, d)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" || d.Name() == "sqlreadguard" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				goFiles = append(goFiles, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}

	var violations []string
	rel := func(p string) string {
		r, err := filepath.Rel(root, p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}

	for _, path := range goFiles {
		hits := sqlReadGuardMatches(t, path, "//")
		if len(hits) == 0 {
			continue
		}
		reason, ok := sqlReadGuardAllowFiles[rel(path)]
		if !ok {
			for _, h := range hits {
				violations = append(violations, rel(path)+":"+itoa(h)+" 裸 request_logs 读（双腿化、内联 "+
					sqlReadGuardInlineMarker+" 或登记白名单）")
			}
			continue
		}
		_ = reason // 文件级豁免成立
	}

	// SQL 对象定义体（视图/函数/维护 DDL）。
	for dir := range map[string]bool{"sql/objects": true, "scripts/analysis": true} {
		sqldir := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(sqldir); err != nil {
			continue
		}
		err := filepath.WalkDir(sqldir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".sql") {
				return nil
			}
			if hits := sqlReadGuardMatches(t, path, "--"); len(hits) > 0 {
				if _, ok := sqlReadGuardAllowSQLFiles[rel(path)]; !ok {
					for _, h := range hits {
						violations = append(violations, rel(path)+":"+itoa(h)+" 裸 request_logs 读")
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("生产读面出现 %d 处裸 request_logs 母表读（8h 盲区）：\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestSQLReadGuardWhitelistCurrent 检查白名单自清洁：条目对应的文件已无
// 命中（被删除或已双腿化）时报错，强制从白名单移除——防止"白名单债务
// 隐身"（conventions.md §3 的机制化延伸）。
func TestSQLReadGuardWhitelistCurrent(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)
	for _, m := range []struct {
		entries map[string]string
		comment string
	}{
		{sqlReadGuardAllowFiles, "Go"},
		{sqlReadGuardAllowSQLFiles, "SQL"},
	} {
		for path := range m.entries {
			full := filepath.Join(root, filepath.FromSlash(path))
			if _, err := os.Stat(full); err != nil {
				t.Errorf("白名单条目文件不存在（请移除条目）: %s", path)
				continue
			}
			if len(sqlReadGuardMatches(t, full, "//")) == 0 && len(sqlReadGuardMatches(t, full, "--")) == 0 {
				t.Errorf("白名单条目已无裸读命中（修复完成后请移除条目）: %s", path)
			}
		}
	}
}

// sqlReadGuardMatches 返回文件中裸 request_logs 读的行号列表；lineComment
// 为该文件类型的行注释前缀（Go="//"，SQL="--"），命中位置之前出现该前缀
// 或整行为 SQL 注释时视为文档性提及，不计。
func sqlReadGuardMatches(t *testing.T, path, lineComment string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lines []int
	for i, line := range strings.Split(string(data), "\n") {
		loc := bareRequestLogsRe.FindStringIndex(line)
		if loc == nil {
			continue
		}
		// Go 行注释（"//"）与 SQL 行注释（"--"，含 Go 原始字符串内的
		// SQL 注释行）均视为文档性提及；命中位置之前出现 "//" 同理。
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, lineComment) || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(line[:loc[0]], lineComment) {
			continue
		}
		if strings.Contains(line, sqlReadGuardInlineMarker) {
			continue
		}
		lines = append(lines, i+1)
	}
	return lines
}

func sqlReadGuardRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
