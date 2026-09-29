// Package data - D07 数据测试：分区父表上的保留期 DELETE 不得用裸 `ctid IN`。
//
// ## 这道门拦的是一个已经上 main 的数据损坏缺陷
//
// 2026-09-29「审计二十轮」把 bg/opslog_trimmer.go 里 candidate_failure_logs 的
// 保留期删除从 `id IN (SELECT id ...)` 改成了 `ctid IN (SELECT ctid ...)`，
// 理由是形态提速（411ms → 19ms）。**方向反了**：那张表是**分区父表**，而
// ctid 只在单个分区内唯一，物理地址在分区之间会重复。
//
// 真库复刻实测（PG 17.10，2026-09-29，TEMP 分区表，数据取自生产 83,584 行，
// 全程 BEGIN…ROLLBACK，零持久变更）：
//
//	形态 X  DELETE FROM 父表 WHERE ctid IN (SELECT ctid FROM 父表 …)
//	        子查询选中 5,000 行 → 实删 10,000 行
//	        最坏布局下（9 月分区全过期、10 月分区全未过期）
//	        **误删 4,722 行未过期数据**
//	形态 Y  DELETE FROM 父表 WHERE (tableoid, ctid) IN (SELECT tableoid, ctid …)
//	        实删 5,000 行，误删 0
//
// 机制：子查询返回的 ctid 值被原样下推到**每一个**分区，于是每个分区都会删掉
// 处在同一物理偏移上的行——这些行从未被保留期谓词选中。
//
// ## 为什么现在还没炸
//
// 真库 11 个分区里只有 candidate_failure_logs_2026_09 有数据，其余 10 个是
// 空分区，所以跨分区误删目前命中 0 行。**这不是「没 bug」，是「还没武装」**：
// 写入 318 行/天、约 105MB/月，第二个非空月分区出现后每 tick 都会开始误删。
// 判定按「已 armed 且机制确定」计 P1，不按「今天损失多少行」计。
//
// 被误删的是**近期**失败记录，而 bg/candidate_failure_monitor.go 的
// checkAutoCool 读的正是最近 5 分钟的 candidate_failure_logs，
// model_probe_passive_boost 同样依赖它——删掉的正是让自动冷却能触发的那批行。
//
// ## 顺带记下一条被污染的实测数字
//
// 那轮注释里的「411ms → 19ms（22 倍）」是在**单个叶子分区**上量的，而代码删的是
// 父表；且形态 Y 在父表上实测 5,000 行需 16.8ms，量级本就接近 19ms。结论「去掉
// ORDER BY 有收益」成立，但「22 倍」不能作为父表上的提速依据。
//
// ## 门的作用
//
// 把「分区父表禁用裸 ctid」固化成契约，并钉住 knownBareCTIDDefects 棘轮：
// 修好一处就把登记删掉并调小棘轮，避免有人忘了更新登记而静默放过。
//
// 叶子分区（candidate_failure_logs_2026_09）用裸 ctid 是**正确**的——
// 单个分区内 ctid 唯一。本门只拦父表名，不拦叶子名。
package data

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// partitionedParents 是真库 2026-09-29 查 `pg_inherits JOIN pg_class relkind='p'`
// 得到的全部分区父表（35 张）。登记而非动态探测，是因为这道门不连库；
// 新增分区父表时请补进来，否则新表上的裸 ctid 不会被拦。
var partitionedParents = map[string]bool{
	"dal.messages":                 true,
	"gateway.session_bodies":       true,
	"gateway.session_turns":        true,
	"gateway.sessions":             true,
	"orchestrator.audit_logs":      true,
	"platform.platform_outbox":     true,
	"auto_route_selections":        true,
	"cache_metrics":                true,
	"candidate_failure_logs":       true,
	"credential_model_index":       true,
	"credit_ledger":                true,
	"dashboard_access_events":      true,
	"handoff_logs":                 true,
	"instance_heartbeats":          true,
	"mock_probe_history":           true,
	"model_probe_runs":             true,
	"request_logs":                 true,
	"request_logs_bodies":          true,
	"request_wal":                  true,
	"routing_decision_log":         true,
	"routing_decision_log_archive": true,
	"session_bodies":               true,
	"session_censors":              true,
	"session_memora":               true,
	"session_module_executions":    true,
	"session_tools":                true,
	"session_turn_details":         true,
	"session_turns":                true,
	"sessions":                     true,
	"stats_event_inbox":            true,
	"supplier_errors":              true,
	"system_probe_runs":            true,
	"tool_usage_stats":             true,
	"usage_facts":                  true,
	"usage_ledger":                 true,
}

// knownBareCTIDDefects 登记**已确认在用裸 ctid 打分区父表**的位置。
//
// 为什么不直接让门为它一直红：长期红的门会被习惯性忽略，久了等于没有门。
// 登记 + 棘轮是折中——门只在出现登记表以外的新位置时红。
var knownBareCTIDDefects = map[string]string{
	"bg/opslog_trimmer.go": "candidate_failure_logs 是 11 分区父表；实测跨分区误删 4,722 行/批",
}

const expectedBareCTIDDefects = 1

// bareCTIDStmtRe 抓一条 DELETE 的语句体。终止条件是行尾反引号或双引号，
// 与 retention_trim_index_test.go 的扫描口径保持一致。
var bareCTIDStmtRe = regexp.MustCompile("(?is)DELETE\\s+FROM\\s+(?:(\\w+)\\.)?(\\w+)(.{0,800}?)(?:LIMIT\\s+\\$?\\d+)?[\x60\"]")

// TestData_RetentionDelete_NoBareCTIDOnPartitionedParent 是本门的主体。
func TestData_RetentionDelete_NoBareCTIDOnPartitionedParent(t *testing.T) {
	violations := map[string][]string{} // 源文件 → 位置列表

	for _, hit := range scanCTIDDeletes(t) {
		if !partitionedParents[hit.table] {
			continue
		}
		// (tableoid, ctid) 复合键是分区父表上的正确形态，不算违规。
		if strings.Contains(hit.stmt, "(tableoid, ctid)") ||
			strings.Contains(hit.stmt, "(tableoid , ctid)") {
			continue
		}
		if !strings.Contains(hit.stmt, "ctid IN") {
			continue
		}
		violations[hit.source] = append(violations[hit.source], hit.table)
	}

	// 棘轮：登记表以外的违规要红；登记表内每条要对应得上实际出现的表。
	unexpected := 0
	for src, tables := range violations {
		file := src
		if i := strings.Index(src, ":"); i >= 0 {
			file = src[:i]
		}
		known, registered := knownBareCTIDDefects[file]
		if !registered {
			unexpected++
			t.Errorf("%s 对分区父表用了裸 ctid IN：%v\n"+
				"    ctid 只在单个分区内唯一，物理地址在分区之间会重复；子查询返回的 ctid\n"+
				"    会被下推到每一个分区，于是每个分区都删掉处在同一偏移上的行——\n"+
				"    这些行从未被保留期谓词选中。\n"+
				"    实测（真库数据复刻，最坏布局）：选中 5,000 → 实删 10,000，\n"+
				"    误删 4,722 行未过期数据。\n"+
				"    正确形态：WHERE (tableoid, ctid) IN (SELECT tableoid, ctid FROM …)。\n"+
				"    叶子分区（<父表>_2026_09）用裸 ctid 是对的，单分区内 ctid 唯一——\n"+
				"    本门只拦父表名。若确实要新增登记，请同时把位置写进 knownBareCTIDDefects。",
				src, uniqueStrings(tables))
			continue
		}
		for _, tbl := range uniqueStrings(tables) {
			if !strings.Contains(known, tbl) {
				t.Errorf("%s 的登记条目写的是「%s」，但实际出现的是表 %s——请同步登记。",
					src, known, tbl)
			}
		}
	}

	if len(knownBareCTIDDefects) != expectedBareCTIDDefects {
		t.Errorf("knownBareCTIDDefects 有 %d 条，expectedBareCTIDDefects = %d。"+
			"修好一条就把登记删掉并调小棘轮；新增一条要同时调大。",
			len(knownBareCTIDDefects), expectedBareCTIDDefects)
	}
	if unexpected > 0 {
		t.Logf("共 %d 个源文件出现登记表以外的裸 ctid 打分区父表", unexpected)
	}
}

// TestData_RetentionDelete_CandidateFailureLogsUsesTableoidCTID 把
// candidate_failure_logs 单独钉死。它是本轮唯一已确认在犯的表，泛化门之外
// 再钉一条，是为了让「这张表」在代码评审里一眼可见。
func TestData_RetentionDelete_CandidateFailureLogsUsesTableoidCTID(t *testing.T) {
	found := false
	for _, hit := range scanCTIDDeletes(t) {
		if hit.table != "candidate_failure_logs" {
			continue
		}
		found = true
		if !strings.Contains(hit.stmt, "(tableoid, ctid)") {
			t.Errorf("%s 的 candidate_failure_logs 删除未用 (tableoid, ctid) 复合键。\n"+
				"    实测：裸 ctid 在 11 分区父表上删 10,000 行（应删 5,000），\n"+
				"    最坏布局误删 4,722 行未过期数据；复合键精确 5,000、误删 0。\n"+
				"    被误删的是近期失败记录，checkAutoCool / model_probe_passive_boost\n"+
				"    正是靠最近几分钟的这批行触发自动冷却。", hit.source)
		}
	}
	if !found {
		t.Fatal("全仓没扫到 candidate_failure_logs 的保留期 DELETE —— " +
			"要么它被改名/删除，要么扫描目录清单需要更新。门不能因为找不到对象就绿。")
	}
}

type ctidDeleteHit struct {
	table  string
	stmt   string
	source string // file:line
}

func scanCTIDDeletes(t *testing.T) []ctidDeleteHit {
	t.Helper()
	root := repoRoot(t)
	var out []ctidDeleteHit

	for _, dir := range retentionSrcDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("源码目录不可读 %s: %v —— 门不能因为找不到源码就静默放行", dir, err)
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "tests" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			src := string(b)
			rel, _ := filepath.Rel(root, path)
			for _, m := range bareCTIDStmtRe.FindAllStringSubmatch(src, -1) {
				out = append(out, ctidDeleteHit{
					table:  m[2],
					stmt:   m[0],
					source: fmtLine(rel, src, strings.Index(src, m[0])),
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s 失败: %v", dir, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].source < out[j].source })
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
