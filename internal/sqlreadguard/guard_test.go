package sqlreadguard

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
	"admin/analytics.go":               "LEGIT: 决策回放双腿之母表腿（R46 F9，hot 腿同查询内联）",
	"admin/request_trace.go":           "LEGIT: hot∪母表双腿 UNION ALL（既定正面模板）",
	"admin/session_tenant.go":          "LEGIT: assertTaskInTenant 跨租户权限门，session 族两腿 + v1 双 EXISTS 母表腿并联（S4 停写后不得只剩 v1）",
	"admin/session_detail_v2.go":       "LEGIT: resolveSessionID 反向臂 hot∪母表双腿之母表腿（R71，hot 腿同查询内联）",
	"internal/trace/trace.go":          "LEGIT: hot∪母表双腿",
	"bg/auto_route_affinity_worker.go": "LEGIT: NOT EXISTS 探测之母表腿（R46 F4 裁决落地形态）",

	// ---- LEGIT：引擎/DDL/维护/工具 ----
	"bg/lite_retention_worker.go":               "LEGIT: SQLite 引擎 DELETE（? 占位，非 PG hot/mother 体系）",
	"cmd/gateway/dual_read_validator.go":        "LEGIT: 专职双腿一致性校验器",
	"db/db.go":                                  "LEGIT: routing_analytics_source DDL 体",
	"db/request_logs_view_schema.go":            "LEGIT: 视图 DDL 体",
	"admin/data_lifecycle.go":                   "LEGIT: 存储生命周期维护面（全历史体积/清理属设计语义）",
	"admin/data_lifecycle_attachments.go":       "LEGIT: 存储生命周期维护面",
	"admin/data_lifecycle_metrics.go":           "LEGIT: 存储生命周期维护面",
	"bg/credential_recovery.go":                 "LEGIT: 恢复扫描需全历史窗口（404 二次确认 6h 终判的旧证据只在母表）",
	"cmd/tools/validate_sessions_v2/loader.go":  "TOOLING: 离线校验工具",
	"cmd/tools/backfill_session_bodies/main.go": "TOOLING: 离线回填工具",
	"cmd/traffic-replay/main.go":                "TOOLING: 离线回放工具",
	"cmd/compression-bench/main.go":             "TOOLING: 离线基准工具",
	// R72 移除三条滞留条目（自清洁盲区修复后由守卫报出，均仅剩注释命中）：
	// admin/session_export.go（已双腿化为 request_logs_with_current_month 视图）、
	// cmd/tools/backfill_sessions_v2_v2/main.go、cmd/tools/backfill_session_bodies/derive.go。

	// ---- DEBT(R47)：admin 读面盲区债（R46 §五#11 同类，待双腿化） ----
	// session_list.go / usage.go / session_online.go 已在 R48 §5 双腿化为
	// request_logs_with_current_month 视图（view 已在白名单 LEGIT），对应
	// 白名单条目按 self-cleaning 守卫自动清除（TestSQLReadGuardWhitelistCurrent）。
	"admin/memora_handlers.go":                 "DEBT(R47): 裸母表",
	"admin/quality_correlations.go":            "DEBT(R47): 裸母表",
	"admin/provider_models.go":                 "DEBT(R47): 裸母表",
	"admin/session_sanitize_matches.go":        "DEBT(R47): 裸母表",
	"cmd/gateway/output_compliance_control.go": "DEBT(R47): 网关运行时读面裸母表",
	"cmd/gateway/main_v3_wiring.go":            "DEBT(R47): 接线读面裸母表",
	"domains/analysis/optimizer.go":            "DEBT(R47): 裸母表",
	"domains/analysis/request_summary.go":      "DEBT(R47): 裸母表",
	"domains/analysis/projectattr/store.go":    "DEBT(R47): 裸母表",
	"domains/sessionforensics/export.go":       "DEBT(R47): 裸母表",
	// domains/providerprofile/pg_reconciliation_store.go 条目已按 R28-B-1（round 30）
	// 聚合源切换移除：该文件现已只读计帐侧月分区，无 request_logs 裸读命中，
	// 由 TestSQLReadGuardWhitelistCurrent 自清洁守卫报出。
	"domains/hooks/goal/history_store.go":             "DEBT(R47): 裸母表",
	"domains/hooks/observability/telemetry/client.go": "DEBT(R47): 裸母表",
	"autoroute/recommend_v2.go":                       "DEBT(R47): 裸母表",
	"discovery/discovery.go":                          "DEBT(R47): 裸母表",
}

var sqlReadGuardAllowSQLFiles = map[string]string{
	"sql/objects/views/request_logs_with_current_month.sql":                           "LEGIT: 双腿视图定义本体",
	"sql/objects/views/request_logs_bodies_progress.sql":                              "LEGIT: 视图定义体",
	"sql/objects/views/v_node_switch_analysis.sql":                                    "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/customer_cost_view.sql":                                        "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/model_cost_per_task_view.sql":                                  "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/v_timeout_effectiveness.sql":                                   "DEBT(R47): 视图定义裸母表",
	"sql/objects/views/v_continuation_effectiveness.sql":                              "DEBT(R47): 视图定义裸母表",
	"sql/objects/functions/credential_most_used_model_integer_integer.sql":            "DEBT(R47): 函数体裸母表",
	"sql/objects/functions/get_last_successful_request_character_varying_integer.sql": "DEBT(R47): 函数体裸母表",
	"sql/objects/tables/passive_probe_state.sql":                                      "LEGIT: DDL 体",
	"sql/objects/other/backfill_request_logs_bodies_integer.sql":                      "LEGIT: 回填 DDL/维护体",
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
// debtBaseline 是 202 号登记的 DEBT(R47) 基线：白名单里当前**已登记**的
// 裸 request_logs 母表读文件（Go 15 + SQL 7 = 22 条）。
//
// 为什么要有它：TestSQLReadGuardWhitelistCurrent 只实现了棘轮的**一半** ——
// 它管住「条目不得比需要活得更久」（文件已双腿化就必须移除），但**管不住
// 「债务可以静默变多」**：给一个新文件加一条 `DEBT(R47):` 白名单，门会一直绿。
//
// 这正是本仓自己写下的失效形态（sql/schema/integration_gate_test.go:429-431）：
// 「一个已知缺口清单只有在**新增缺口是致命的**时才算棘轮。如果 harness 只是
// 打印失败，那么未来每一个新的缺口都会被吸收进同一次绿色运行里，清单最终变成
// 没人再读的豁免。」—— `TestGateRatchetsUnlistedStartupGaps` 已经在那边落实了，
// 本门此前没有。
//
// 语义选择：只对**新增**报错，对**移除**自动接受。
//   - 移除 = 债务被偿还，不需要任何人改本文件；
//   - 若改成「集合必须逐字相等」，每还一笔债都要来改基线，基线本身变成噪音，
//     于是大家开始不看它 —— 棘轮就退化成了清单。
//
// ⇒ 「删一条加一条」也堵得住：那是新增，会红。
var debtBaseline = map[string]bool{
	// Go 读面
	"admin/memora_handlers.go":                        true,
	"admin/quality_correlations.go":                   true,
	"admin/provider_models.go":                        true,
	"admin/session_sanitize_matches.go":               true,
	"cmd/gateway/output_compliance_control.go":        true,
	"cmd/gateway/main_v3_wiring.go":                   true,
	"domains/analysis/optimizer.go":                   true,
	"domains/analysis/request_summary.go":             true,
	"domains/analysis/projectattr/store.go":           true,
	"domains/sessionforensics/export.go":              true,
	"domains/hooks/goal/history_store.go":             true,
	"domains/hooks/observability/telemetry/client.go": true,
	"autoroute/recommend_v2.go":                       true,
	"discovery/discovery.go":                          true,
	// SQL 读面（视图定义体 / 函数体）
	"sql/objects/views/v_node_switch_analysis.sql":                                    true,
	"sql/objects/views/customer_cost_view.sql":                                        true,
	"sql/objects/views/model_cost_per_task_view.sql":                                  true,
	"sql/objects/views/v_timeout_effectiveness.sql":                                   true,
	"sql/objects/views/v_continuation_effectiveness.sql":                              true,
	"sql/objects/functions/credential_most_used_model_integer_integer.sql":            true,
	"sql/objects/functions/get_last_successful_request_character_varying_integer.sql": true,
}

func debtEntries() map[string]bool {
	out := map[string]bool{}
	for path, reason := range sqlReadGuardAllowFiles {
		if strings.Contains(reason, "DEBT") {
			out[path] = true
		}
	}
	for path, reason := range sqlReadGuardAllowSQLFiles {
		if strings.Contains(reason, "DEBT") {
			out[path] = true
		}
	}
	return out
}

// TestDebtRatchetDoesNotGrow 补上棘轮缺失的那一半。
func TestDebtRatchetDoesNotGrow(t *testing.T) {
	debt := debtEntries()

	// 覆盖面下限：先证「抽到东西了」。空集合同样满足「没有新增」，
	// 抽取逻辑坏掉时这道门会安静通过（201 号 §102 的教训）。
	if len(debt) < 20 {
		t.Fatalf("只从白名单里抽出 %d 条 DEBT 条目（下限 20）：DEBT 识别很可能已失效，"+
			"此时「没有新增」会与「一条都没抽到」同步为真。抽到的：%v", len(debt), keysSorted(debt))
	}

	var added []string
	for p := range debt {
		if !debtBaseline[p] {
			added = append(added, p)
		}
	}
	sort.Strings(added)
	if len(added) > 0 {
		t.Errorf("DEBT(R47) 盲区债新增了 %d 条（当前 %d 条，基线 %d 条）：\n  %s\n"+
			"  这道门只拦「存量」的话，新迁移或新读面加一条白名单就会一直绿，直到清单变成没人读的豁免"+
			"（本仓 sql/schema/integration_gate_test.go:429-431 已写下这条失效形态，"+
			"TestGateRatchetsUnlistedStartupGaps 也在那边落实了）。\n"+
			"  要新增必须同时：(1) 更新 debtBaseline；(2) 在理由里写清为何本处不能双腿化；"+
			"  (3) 确认该读面确实只影响 <8h 窗口。若本条其实不构成债，"+
			"  请先修代码再登记 —— 优先把理由从 DEBT(R47) 改成 LEGIT 并给出依据。",
			len(added), len(debt), len(debtBaseline), strings.Join(added, "\n  "))
	}
	t.Logf("DEBT(R47) 现状：%d 条（基线 %d），新增 %d 条", len(debt), len(debtBaseline), len(added))
}

func keysSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

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
			// R72 修正（D14#2）：按文件自己的注释类型做失配判定。旧写法
			// 双腿与（// 与 -- 两个遍历都为 0 才报）——Go 文件的纯 // 注释
			// 命中在 -- 遍历里不被豁免而恒计数 ≥1，导致"只剩注释命中"的
			// 条目永不报滞留（白名单债务隐身，与守卫目的相反）。
			comment := "//"
			if m.comment == "SQL" {
				comment = "--"
			}
			if len(sqlReadGuardMatches(t, full, comment)) == 0 {
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

// ─────────────────────────────────────────────────────────────────────────────
// D07 写侧对偶守卫（P0-2，2026-09-30 三十五轮）
//
// 既有 TestNoBareRequestLogsMotherReads 只管**读**面（裸 FROM|JOIN 母表）。
// 但 D07 的纪律是「更新/删除只落 hot」——**写**面同样需要机械门。原状下
// 「只落 hot」纯靠人工 code review，与域文档 §3#1 把它定为 P1 级纪律的
// 期望不符：读面有门、写面没有，正是最容易悄悄漂移的不对称。
//
// 现实基线：全生产面直写 request_logs 母表的语句只有 1 处，且早已在读守卫
// 白名单里登记为 LEGIT（SQLite 引擎 DELETE，? 占位，非 PG hot/mother 体系）。
// 门本身因此很干净——这正是纪律在实践中生效的证据，而非「没什么可查」。

// bareRequestLogsWriteRe 匹配对 request_logs 母表的直写（INSERT/UPDATE/DELETE）。
var bareRequestLogsWriteRe = regexp.MustCompile(`(?i)\b(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+(public\.)?request_logs\b`)

// sqlWriteGuardAllowFiles 是写面 LEGIT 白名单，理由必须写清楚「为什么不是
// 直写母表」，便于未来有人误以为它是待还债项。
var sqlWriteGuardAllowFiles = map[string]string{
	"bg/lite_retention_worker.go": "LEGIT: SQLite 引擎 DELETE（? 占位，非 PG hot/mother 体系）",
}

const sqlWriteGuardInlineMarker = "sqlwriteguard:allow"

func TestNoBareRequestLogsMotherWrites(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)

	scanDirs := []string{"admin", "autoroute", "bg", "cmd", "db", "discovery", "domains", "internal", "provider", "proxy", "taskprofile"}
	var violations []string
	for _, d := range scanDirs {
		dir := filepath.Join(root, d)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "vendor" || entry.Name() == "sqlreadguard" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			relPath, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			rel := filepath.ToSlash(relPath)
			if _, allowed := sqlWriteGuardAllowFiles[rel]; allowed {
				return nil
			}
			for _, line := range bareWriteLines(path) {
				violations = append(violations, rel+":"+itoa(line)+
					" 直写 request_logs 母表（应走 hot 分区；或登记 sqlWriteGuardAllowFiles 说明理由）")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("生产写面出现 %d 处直写 request_logs 母表（绕过 hot 分区）：\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestSqlWriteGuardAllowlistCurrent mirrors the read-side self-cleaning check: a
// whitelist entry that no longer contains a direct write must be removed, so
// exemptions cannot silently accumulate.
func TestSqlWriteGuardAllowlistCurrent(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)
	for path, reason := range sqlWriteGuardAllowFiles {
		full := filepath.Join(root, filepath.FromSlash(path))
		if _, err := os.Stat(full); err != nil {
			t.Errorf("写面白名单条目文件不存在（请移除条目）: %s", path)
			continue
		}
		if len(bareWriteLines(full)) == 0 {
			t.Errorf("写面白名单条目已无直写命中（请移除条目）: %s —— %s", path, reason)
		}
	}
}

// bareWriteLines returns 1-based line numbers of non-comment direct writes to
// the request_logs mother table. Mirrors the read-side comment exemptions:
// Go line comments and SQL line comments inside raw strings are documentation.
func bareWriteLines(path string) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []int
	for i, line := range strings.Split(string(data), "\n") {
		loc := bareRequestLogsWriteRe.FindStringIndex(line)
		if loc == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(line[:loc[0]], "//") {
			continue
		}
		if strings.Contains(line, sqlWriteGuardInlineMarker) {
			continue
		}
		lines = append(lines, i+1)
	}
	return lines
}
