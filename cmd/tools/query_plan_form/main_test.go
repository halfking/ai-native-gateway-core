// -*- coding: utf-8 -*-
// ★ R179 轮：量具（★ 计划形态分类器）自己的夹具测试。
//
// ★★ 为什么要有这个文件：★★ 本轮实测发现**分类口径本身有缺陷**（★ 详见 main.go 里 classify 的注释）
// ★★ ⇒ ⇒ 而分类口径的缺陷**不会被任何门抓到**（★ 门只读产出的 plan_form 字符串）
// ★★ ⇒ ⇒ 所以必须有一份**夹具**把口径钉死 ⇒ ⇒ ★ 「量具自己也要被量」。
//
// ★★ 夹具一（★ 真实形态）★★：★ 252 生产实测（★ 「按 api_key 过滤会话」的真实计划）——
//
//	Index Scan using session_turns_2026_10_ts_idx on session_turns_2026_10 (rows=357)
//	      Index Cond: (ts >= … AND ts < …)
//	      Filter: (CASE WHEN (api_key_id ~ '^[0-9]+$'::text) THEN (api_key_id)::bigint END = 113)
//	Seq Scan on session_turns_2026_07 t_2 (rows=1)   ← ★ 时间窗裁不掉的旧月分区
//	Seq Scan on session_turns_2026_08 t_3 (rows=1)   ← ★ 同上
//	Index Scan using idx_session_turns_hot_ts on session_turns_hot t (rows=1)
//
// ★★ 旧口径会把它记成 `seq-scan`（★ 因为「有 Seq Scan」）⇒ ⇒ ★★ 而承载数据的那次扫描
// ★★ 明明是**索引扫描 + 残留 Filter** ⇒ ⇒ 正确答案是 `filter`。★★
//
// ★★ 夹具二（★ 假绿夹具）★★：★ 索引扫描 + 残留 Filter，★ 而 `Rows Removed by Filter` = 0
// ★★ （★ EXPLAIN 不带 ANALYZE ⇒ 它是执行期统计 ⇒ 恒为 0）★★
// ★★ ⇒ ⇒ 旧口径（★ 判 `Rows Removed by Filter > 0`）会把它记成 **`seek`** ⇒ ⇒ **假绿**。
package main

import "testing"

// scanNode 造一个会话族扫描节点。
func scanNode(rel, nodeType, indexName, indexCond, filter string, planRows int64, totalCost float64) planNode {
	return planNode{
		NodeType:           nodeType,
		RelationName:       rel,
		IndexName:          indexName,
		IndexCond:          indexCond,
		Filter:             filter,
		PlanRows:           planRows,
		TotalCost:          totalCost,
		ParentRelationship: "Outer",
	}
}

// 真实形态：★ 252 生产实测的那条计划（★ 摘录自 2026-10-29 的只读 EXPLAIN）。
func realProductionPlan() planNode {
	return planNode{
		NodeType: "Append",
		Plans: []planNode{
			{NodeType: "Subquery Scan", Plans: []planNode{
				scanNode("session_turns_hot", "Index Scan", "idx_session_turns_hot_ts",
					"(ts >= '2026-10-21' AND ts < '2026-10-28')", "", 1, 2.51),
			}},
			{NodeType: "Subquery Scan", Plans: []planNode{
				scanNode("session_turns_2026_07", "Seq Scan", "",
					"", "((ts >= … AND ts < …) AND (CASE … END = 113))", 1, 0),
				scanNode("session_turns_2026_08", "Seq Scan", "",
					"", "((ts >= … AND ts < …) AND (CASE … END = 113))", 1, 0),
				scanNode("session_turns_2026_09", "Index Scan", "session_turns_2026_09_ts_idx",
					"(ts >= … AND ts < …)", "(CASE … END = 113)", 1, 2.65),
				scanNode("session_turns_2026_10", "Index Scan", "session_turns_2026_10_ts_idx",
					"(ts >= … AND ts < …)", "(CASE … END = 113)", 357, 17110.77),
			}},
		},
	}
}

func TestClassifyRealProductionPlanIsFilterNotSeqScan(t *testing.T) {
	form, scans, dominant, _, otherSeq := classify(realProductionPlan())
	if form != "filter" {
		t.Fatalf("真实生产计划应判 `filter`（★ 承载数据的那次扫描是索引扫描 + 残留 Filter），实际 %q", form)
	}
	if dominant != "session_turns_2026_10" {
		t.Fatalf("主体关系应是承载行数的那次（session_turns_2026_10），实际 %q", dominant)
	}
	if otherSeq != 2 {
		t.Fatalf("主体之外被顺序扫描的关系数 = %d，期望 2（★ 两个裁不掉的旧月分区）", otherSeq)
	}
	if len(scans) != 5 {
		t.Fatalf("会话族扫描节点数 = %d，期望 5", len(scans))
	}
}

// ★ 假绿夹具：★ 索引扫描 + 残留 Filter ⇒ ⇒ 必须判 `filter`，★ 不得判 `seek`。
func TestClassifyResidualFilterIsNeverSeek(t *testing.T) {
	p := planNode{NodeType: "Append", Plans: []planNode{
		scanNode("session_turns_2026_10", "Index Scan", "session_turns_2026_10_ts_idx",
			"(ts >= … AND ts < …)", "(CASE … END = 113)", 357, 17110.77),
	}}
	form, _, dominant, _, otherSeq := classify(p)
	if form == "seek" {
		t.Fatalf("★ 节点上还有残留 Filter ⇒ ⇒ 谓词没被索引完全接住 ⇒ ⇒ 不得判 `seek`（★ 那是假绿）")
	}
	if form != "filter" {
		t.Fatalf("应判 `filter`，实际 %q", form)
	}
	if dominant != "session_turns_2026_10" || otherSeq != 0 {
		t.Fatalf("主体=%q 其他顺序扫描=%d，期望 session_turns_2026_10 / 0", dominant, otherSeq)
	}
}

// ★ 旧月分区不得当主体：★ 数据分区走索引 + 无残留 Filter ⇒ ⇒ `seek`。
func TestClassifyOldUnprunedPartitionDoesNotDominate(t *testing.T) {
	p := planNode{NodeType: "Append", Plans: []planNode{
		scanNode("session_turns_2026_07", "Seq Scan", "", "", "(ts …)", 1, 0),
		scanNode("session_turns_2026_10", "Index Scan", "session_turns_2026_10_ts_idx",
			"(ts >= … AND ts < … AND (CASE … END = 113))", "", 357, 17110.77),
	}}
	form, _, dominant, _, otherSeq := classify(p)
	if form != "seek" {
		t.Fatalf("应判 `seek`（★ 主体是索引扫描且无残留 Filter），实际 %q", form)
	}
	if dominant != "session_turns_2026_10" {
		t.Fatalf("主体应是 session_turns_2026_10，实际 %q", dominant)
	}
	if otherSeq != 1 {
		t.Fatalf("其余顺序扫描关系数 = %d，期望 1", otherSeq)
	}
}

// ★ 纯顺序扫描 ⇒ `seq-scan`。
func TestClassifyPureSeqScan(t *testing.T) {
	p := planNode{NodeType: "Append", Plans: []planNode{
		scanNode("session_turns_2026_10", "Seq Scan", "", "", "(ts … AND CASE …)", 5000, 12345),
	}}
	form, _, dominant, _, _ := classify(p)
	if form != "seq-scan" {
		t.Fatalf("应判 `seq-scan`，实际 %q", form)
	}
	if dominant != "session_turns_2026_10" {
		t.Fatalf("主体应是 session_turns_2026_10，实际 %q", dominant)
	}
}

// ★ 没有会话族扫描 ⇒ `no-scan`。
func TestClassifyNoSessionFamilyScan(t *testing.T) {
	p := planNode{NodeType: "Aggregate", Plans: []planNode{
		{NodeType: "Seq Scan", RelationName: "stats_usage_daily", Filter: "(x)", PlanRows: 10},
	}}
	form, scans, dominant, _, otherSeq := classify(p)
	if form != "no-scan" {
		t.Fatalf("应判 `no-scan`，实际 %q", form)
	}
	if len(scans) != 0 || dominant != "" || otherSeq != 0 {
		t.Fatalf("no-scan 时不应带扫描信息：scans=%d dominant=%q otherSeq=%d", len(scans), dominant, otherSeq)
	}
}

// ★ 解析器有牙：★ 故意造一个【索引扫描 + 残留 Filter】的夹具，★ 确认它**不**被判成 seek。
// ★ （★ 这条是「反向自证」——★ 防止有人把判据改回 rowsRm > 0。）
func TestClassifyFilterClassIsReachable(t *testing.T) {
	p := planNode{NodeType: "Append", Plans: []planNode{
		scanNode("session_turns_2026_10", "Index Scan", "idx_x",
			"(ts >= …)", "(CASE … END = 7)", 10, 100),
	}}
	form, _, _, _, _ := classify(p)
	if form != "filter" {
		t.Fatalf("`filter` 这一类必须【可达】（★ 否则就是死代码），实际 %q", form)
	}
}

// ★★ 判决对象夹具：★★ 明细表（★ LEFT JOIN 的一侧）**不得**当主体。
// ★ 视图的定义 = FROM session_turns* LEFT JOIN session_turn_details* ⇒ ⇒
// ★ ★ 本轮要判的是「【基表】的访问路径有没有被索引承重」⇒ ⇒ 明细侧行数估得更大时，
// ★ ★ 判决仍必须由基表侧给出（★ 否则就是「答非所问」）。
func TestClassifyDetailsSideNeverDominatesVerdict(t *testing.T) {
	p := planNode{NodeType: "Append", Plans: []planNode{
		{NodeType: "Hash Join", Plans: []planNode{
			// ★ 基表：顺序扫描（★ 形态最差）
			scanNode("session_turns_2026_10", "Seq Scan", "", "", "(ts … AND CASE … END = 7)", 120, 900),
			// ★ 明细表：索引扫描，★ 但**行数估得更大**（★ 旧口径会挑它当主体）
			scanNode("session_turn_details_2026_10", "Index Scan", "session_turn_details_2026_10_request_id_idx",
				"(request_id = …)", "", 5000, 800),
		}},
	}}
	form, _, dominant, _, _ := classify(p)
	if form != "seq-scan" {
		t.Fatalf("判决必须由【基表】给出（★ 基表是顺序扫描 ⇒ seq-scan），实际 %q", form)
	}
	if dominant != "session_turns_2026_10" {
		t.Fatalf("主体应是基表 session_turns_2026_10，实际 %q（★ 明细侧不得当主体）", dominant)
	}
}

// ★★ 只有明细表扫描 ⇒ ★ 视图基表没被扫 ⇒ `no-scan`（★ 答案不来自会话族基表）。
func TestClassifyOnlyDetailsScansIsNoScan(t *testing.T) {
	p := planNode{NodeType: "Nested Loop", Plans: []planNode{
		scanNode("session_turn_details_2026_10", "Index Scan", "idx_req", "(request_id = …)", "", 5000, 800),
	}}
	form, _, dominant, _, _ := classify(p)
	if form != "no-scan" {
		t.Fatalf("只有明细表扫描时不得判成 seek（★ 那会答非所问），实际 %q", form)
	}
	if dominant != "" {
		t.Fatalf("no-scan 时主体应为空，实际 %q", dominant)
	}
}
