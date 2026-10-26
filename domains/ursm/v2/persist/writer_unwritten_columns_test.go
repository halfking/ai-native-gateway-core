package persist

// 守卫（2026-10-18 §10.110.64）：**表里每一列都必须有归属**。
//
// # 背景：56 列里有 10 列从来没有被写过
//
// 生产实测（两把独立的尺子互相印证）：
//
//	尺子 A（生产）    最新一轮 1816 行：这 10 列 count(col) = 0
//	                 最近 200 轮累计：也是 0
//	尺子 B（本文件）  writer.go 的 INSERT 只写 46 列，表有 56 列
//	                 ⇒ 差的 10 列与尺子 A 的 10 列**完全重合**
//
// 这 10 列：`lat_p95_ms` / `price_in_per_1m` / `price_out_per_1m` /
// `billing_mode` / `trust_level` / `baseurl_latency_ms` /
// `conc_used` / `conc_limit` / `fp_used` / `fp_limit`。
//
// 上游 Redis 的 node hash 里也**一个都没有**（抽样 3 个 hash 确认），
// store 层除 `lat_p95_ms` 外无人读取 ⇒ 三重死：无生产者、无消费者、无数据。
//
// # 为什么要有这道门
//
// 死列的**物理代价很小**（NULL 列在 null bitmap 里只占 1 位），
// 真正的代价是**接口说谎**：`SELECT conc_used FROM ...` 永远返回 NULL，
// 而查询者无从知道「这个字段上游根本不存在」。
//
// 下一批列若是同样静默长出来，只有这道门会响。
//
// # ⚠ 这道门的取样面纪律（本轮为此付了三次代价）──
//
// 1. **表名必须锚进去**：全文件 grep `payload jsonb ... NOT NULL`
//    命中的是 `analysis_events`（§63 的第四次量具事故）。
//    ⇒ 这里用 `CREATE TABLE ... ursm_node_snapshot_min (` 起、
//      `\n);` 止截段。
// 2. **提取结果必须自检**：提取到 0 列、或锚列缺失，立即失败。
//    本轮曾两次拿着「提取返回 0」的读数去算差集。
// 3. 列清单结尾的 `)` 会粘到最后一个标识符上，必须先剥掉。

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// baselineSchemaPath 由包目录（domains/ursm/v2/persist）上溯到仓库根。
const baselineSchemaPath = "../../../../sql/schema/01-schema.sql"

var (
	reCreateTableHead = regexp.MustCompile(`(?i)CREATE TABLE (?:public\.)?ursm_node_snapshot_min\s*\(`)
	reUnquotedIdent   = regexp.MustCompile(`(?m)^\s*([a-z][a-z0-9_]*)\s+[a-z]`)
	reInsertHead      = regexp.MustCompile(`INSERT INTO ursm_node_snapshot_min\s*\(`)
)

// unwrittenByDesign 是「刻意不由 persist 写入」的列登记。
//
// ⚠ 它必须**极小且逐条写明理由**：每一条都是「结构上存在但没人填」的
//
//	正式承认。任何新条目都要能回答：谁需要它？不需要就让它变红。
var unwrittenByDesign = map[string]string{
	"lat_p95_ms":         "上游 Redis node hash 从不写该字段（抽样确认），store/pipeline.go:135 读它只会得到 0",
	"price_in_per_1m":    "上游无此字段；pricing 属 818 刻意保留在 payload 的前向兼容仓",
	"price_out_per_1m":   "同上",
	"billing_mode":       "同上",
	"trust_level":        "同上",
	"baseurl_latency_ms": "同上",
	"conc_used":          "concurrency 族，上游无生产；留在 payload 前向兼容仓",
	"conc_limit":         "同上",
	"fp_used":            "fingerprint 族，上游无生产",
	"fp_limit":           "同上",
}

// tableColumnsFromBaseline 从 baseline 抽出这张表的列名。
func tableColumnsFromBaseline(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(baselineSchemaPath)
	if err != nil {
		t.Fatalf("read baseline %s: %v", baselineSchemaPath, err)
	}
	loc := reCreateTableHead.FindStringIndex(string(b))
	if loc == nil {
		t.Fatalf("baseline has no CREATE TABLE for ursm_node_snapshot_min — " +
			"the anchor went stale; an empty read would make this gate vacuous")
	}
	rest := string(b)[loc[1]:]
	end := strings.Index(rest, "\n);")
	if end < 0 {
		t.Fatal("baseline CREATE TABLE block never terminates with \\n);")
	}
	block := rest[:end]

	// 剥掉行注释后再按逗号切；列定义形如 `name type [约束]`。
	var cols []string
	for _, part := range strings.Split(block, ",") {
		part = reStripLineComment.ReplaceAllString(part, "")
		m := reUnquotedIdent.FindStringSubmatch(strings.TrimSpace(part))
		if m != nil {
			cols = append(cols, m[1])
		}
	}
	// ★ 量具自检：真实表有 56 列，且这几个锚列必须都在。
	//
	// ⚠ 锚列**只能选 baseline 与 INSERT 都有的**。`cool_reason` 是 818 用
	//   ALTER TABLE 加的，baseline 这张表的 CREATE TABLE 里**没有它**
	//   （该块最后一行是 `payload jsonb`）—— 它属于 818 之前的世代。
	//   选它当锚会把门直接判成"提取器陈旧"，而真问题是基线滞后。
	if len(cols) == 0 {
		t.Fatal("extracted 0 columns from the baseline — gate would be vacuous")
	}
	for _, anchor := range []string{"snapshot_ts", "payload", "lat_p95_ms", "fp_limit"} {
		found := false
		for _, c := range cols {
			if c == anchor {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("baseline extraction lost the anchor column %q — stale extractor", anchor)
		}
	}
	return cols
}

var reStripLineComment = regexp.MustCompile(`--[^\n]*`)

// insertColumnsFromWriter 从 writer.go 的 INSERT 抽出列清单。
func insertColumnsFromWriter(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile("writer.go")
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	loc := reInsertHead.FindStringIndex(string(b))
	if loc == nil {
		t.Fatal("writer.go has no INSERT INTO ursm_node_snapshot_min")
	}
	rest := string(b)[loc[1]:]
	rel := regexp.MustCompile(`(?m)^\s*VALUES\b`).FindStringIndex(rest)
	if rel == nil {
		t.Fatal("INSERT column list never reaches a VALUES line")
	}
	raw := reStripLineComment.ReplaceAllString(rest[:rel[0]], "")
	// ★ 列清单结尾的 ')' 会粘到最后一个标识符上。
	raw = strings.ReplaceAll(raw, ")", " ")

	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(id) {
			out[id] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("extracted 0 columns from the INSERT — gate would be vacuous")
	}
	for _, anchor := range []string{"snapshot_ts", "payload", "cool_reason"} {
		if !out[anchor] {
			t.Fatalf("INSERT extraction lost the anchor column %q — stale extractor", anchor)
		}
	}
	return out
}

// TestEveryTableColumnHasAWriter 是本门的主判据：
// 表里每一列，要么被 INSERT 写，要么被登记在 unwrittenByDesign。
//
// ⚠ 全集取 **baseline ∪ INSERT**：这张表的 baseline 是 818 之前的世代
//
//	（缺 818 用 ALTER TABLE 加的 24 列），单用 baseline 会把它们
//	误判成"表里有、没人写"的孤儿列。
func TestEveryTableColumnHasAWriter(t *testing.T) {
	inserted := insertColumnsFromWriter(t)
	table := tableColumnsFromBaseline(t)

	universe := map[string]bool{}
	for _, c := range table {
		universe[c] = true
	}
	for c := range inserted {
		universe[c] = true
	}

	var orphans []string
	for c := range universe {
		if inserted[c] {
			continue
		}
		if _, declared := unwrittenByDesign[c]; !declared {
			orphans = append(orphans, c)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("these table columns are neither written by the INSERT nor declared "+
			"in unwrittenByDesign:\n  %v\n"+
			"each one is permanently NULL (verify against production before believing it), "+
			"and queries against it will silently return NULL forever", orphans)
	}
}

// 登记表自身不许漂：登记的列必须**真的存在于表里**。
// 否则一次改名就会留下一条对空气的注释，看起来像是「已审查过」。
func TestUnwrittenByDesignColumnsStillExist(t *testing.T) {
	exists := map[string]bool{}
	for _, c := range tableColumnsFromBaseline(t) {
		exists[c] = true
	}
	var stale []string
	for name := range unwrittenByDesign {
		if !exists[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("unwrittenByDesign lists columns the table no longer has: %v\n"+
			"either the column was dropped (remove the entry) or the baseline "+
			"extraction went stale", stale)
	}
}

// 登记的列必须**确实没被 INSERT 写**。
// 「有写入者却被登记成死列」会让门变松：真出现静默空列时它不再响。
func TestUnwrittenByDesignColumnsAreActuallyUnwritten(t *testing.T) {
	inserted := insertColumnsFromWriter(t)
	var wrong []string
	for name := range unwrittenByDesign {
		if inserted[name] {
			wrong = append(wrong, name)
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("unwrittenByDesign claims these are never written, but the INSERT "+
			"does write them: %v\n"+
			"they are alive — remove them from the registry or the gate goes blind "+
			"exactly when a genuinely dead column appears", wrong)
	}
}
