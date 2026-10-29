// -*- coding: utf-8 -*-
// ★ R181 轮量具产物（★ `query_plan_form` 的 `sort_keys` 列 + 排序列抽取）的
// ★ **自己的**夹具测试 —— 2026-10-29。
//
// ★★ 为什么要有这个文件：★★ R181 轮把「排序列观测」作为**量具产物**带进了
// ★★ 仓库（collectSortKeys + TSV 末尾列），但**零测试覆盖** ⇒ ⇒
// ★★ 一个没有任何夹具的量具产物，等于一个**没人量过**的量具 ⇒ ⇒
// ★★ 它既可能根本没接线（列恒空），也可能口径错了（抽出来的不是排序列），
// ★★ 而这两种情况**都不会被任何现有门抓到**（门只读产出的 plan_form 字符串）。
// ★★ ⇒ 「量具自己也要被量」（同 main_test.go 的立论）。
//
// ★★ 口径（承 R181 轮的立意）：★★
//
//	★ 形态轴只判「谓词有没有被索引接住」（seek / filter / seq-scan），
//	★ ⇒ ⇒ 「seek」会**掩盖**「排序没被索引接住」——
//	★ ⇒ ⇒ 一条语句可以【谓词走索引、排序却要额外 Sort】⇒ ⇒ 那正是慢读点的典型形态
//	★ ⇒ ⇒ ⇒ **「形态」与「代价」必须同时看** ⇒ ⇒ 排序这一维必须**单独观测**。
//
// ★★ 夹具一（★ 真实计划形态）：★★ ★ 2026-10-29 本地真库 EXPLAIN (FORMAT JSON) 实测：
//
//	Limit
//	  Sort  ← ★★ 这里就是「谓词走索引、排序却要额外 Sort」的那个点
//	    Result
//	      Append
//	        Subquery Scan → Seq Scan (session_turns_2026_10)
//	        …
//
// ★★ 夹具二（★ 假绿夹具）：★★ 计划里**没有** Sort 节点 ⇒ sort_keys 必须为空。
// ★★ **空 ≠ 「排序一定被接住」** ⇒ 它也可能是这条语句本来就不排序 ⇒
// ★★ 所以空值本身不能当成「排序已被索引接住」的证据（这一点写进了 TSV 表头）。
package main

import (
	"strings"
	"testing"
)

// sortPlanFixture 造一棵带 Sort 节点的真实形态计划树。
//
// ★ 结构照抄本地真库 EXPLAIN (FORMAT JSON) 的实测输出（2026-10-29）：
// Limit → Sort → Result → Append →（Subquery Scan → Seq Scan）× N。
func sortPlanFixture() planNode {
	return planNode{
		NodeType: "Limit",
		Plans: []planNode{
			{
				NodeType: "Sort",
				SortKey:  []string{"(date_trunc('hour'::text, \"*SELECT* 1\".ts))"},
				Plans: []planNode{
					{
						NodeType: "Result",
						Plans: []planNode{
							{
								NodeType: "Append",
								Plans: []planNode{
									{NodeType: "Subquery Scan", Plans: []planNode{
										scanNode("session_turns_2026_10", "Seq Scan", "", "", "", 1204, 88.1),
									}},
									{NodeType: "Subquery Scan", Plans: []planNode{
										scanNode("session_turns_hot", "Seq Scan", "", "", "", 37, 3.2),
									}},
								},
							},
						},
					},
				},
			},
		},
	}
}

// TestCollectSortKeysFindsSortNodeInNestedPlan：★ 排序列必须被抽到，
// 且**位置无关**（Sort 埋在 Limit → Sort → Result → Append 之下）。
func TestCollectSortKeysFindsSortNodeInNestedPlan(t *testing.T) {
	got := collectSortKeys(sortPlanFixture())
	if len(got) != 1 {
		t.Fatalf("sort_keys = %v, want 1 项（计划里有一个 Sort 节点）", got)
	}
	if !strings.Contains(got[0], "date_trunc") {
		t.Fatalf("sort_keys[0] = %q, want 含 date_trunc 的排序列", got[0])
	}
}

// TestCollectSortKeysKeepsDirectionAsIs：★ 保留原样（含 DESC / ASC 方向修饰）。
//
// 为什么重要：方向是判据的一部分——「ORDER BY ts DESC」与「ORDER BY ts」
// 在索引方向不同时是两种形态；把方向洗掉 ⇒ 两形态被混为一谈。
func TestCollectSortKeysKeepsDirectionAsIs(t *testing.T) {
	root := planNode{NodeType: "Sort", SortKey: []string{"ts DESC", "tenant_id ASC"}}
	got := collectSortKeys(root)
	if len(got) != 2 || got[0] != "ts DESC" || got[1] != "tenant_id ASC" {
		t.Fatalf("sort_keys = %v, want [ts DESC tenant_id ASC]（顺序与方向都要保留）", got)
	}
}

// TestCollectSortKeysIncludesIncrementalSort：★ Incremental Sort 也要算。
//
// 为什么：PG 会在「前导键已接住、尾部几列没接住」时给出 Incremental Sort
// —— 那**正是**「seek 掩盖排序没被接住」的那种形态，只认 "Sort" 会漏掉它。
func TestCollectSortKeysIncludesIncrementalSort(t *testing.T) {
	root := planNode{NodeType: "Limit", Plans: []planNode{
		{NodeType: "Incremental Sort", SortKey: []string{"tenant_id", "ts DESC"}},
	}}
	got := collectSortKeys(root)
	if len(got) != 2 || got[0] != "tenant_id" || got[1] != "ts DESC" {
		t.Fatalf("sort_keys = %v, want [tenant_id ts DESC]", got)
	}
}

// TestCollectSortKeysEmptyWhenNoSortNode：★ 没有 Sort 节点 ⇒ 空。
//
// ★ 这不是「排序被索引接住」的证据（★ 表头已写明）—— 但它**是**
// 「抽取器没有凭空造列」的证据（★ 假绿夹具）。
func TestCollectSortKeysEmptyWhenNoSortNode(t *testing.T) {
	root := planNode{NodeType: "Limit", Plans: []planNode{
		{NodeType: "Index Scan", IndexName: "session_turns_2026_10_ts_idx"},
	}}
	if got := collectSortKeys(root); len(got) != 0 {
		t.Fatalf("sort_keys = %v, want 空（计划里没有 Sort 节点）", got)
	}
}

// TestCollectSortKeysSkipsBlankAndTrims：★ 空白键跳过、前后空白裁掉。
func TestCollectSortKeysSkipsBlankAndTrims(t *testing.T) {
	root := planNode{NodeType: "Sort", SortKey: []string{"  ts DESC  ", "", "   ", "tenant_id"}}
	got := collectSortKeys(root)
	if len(got) != 2 || got[0] != "ts DESC" || got[1] != "tenant_id" {
		t.Fatalf("sort_keys = %v, want [ts DESC tenant_id]", got)
	}
}

// TestCollectSortKeysCollectsEverySortNodeInTreeOrder：★ 多个 Sort 节点按
// 树序**全部**收集（不是只取第一个）。
//
// 为什么：一条语句可以有「外层 Sort」+「内层 Incremental Sort」两级；
// 只取第一个会漏掉内层那一级的排序列。
func TestCollectSortKeysCollectsEverySortNodeInTreeOrder(t *testing.T) {
	root := planNode{NodeType: "Limit", Plans: []planNode{
		{NodeType: "Sort", SortKey: []string{"outer"}},
		{NodeType: "Sort", SortKey: []string{"inner"}},
	}}
	got := collectSortKeys(root)
	if len(got) != 2 || got[0] != "outer" || got[1] != "inner" {
		t.Fatalf("sort_keys = %v, want [outer inner]", got)
	}
}

// TestWriteRowEmitsSortKeysAsLastColumn 是**接线**门：产物必须真的落到
// TSV 的末尾列上。
//
// 为什么必须单独钉：R181 轮把这一列**加在末尾**（既有消费方按列序读
// parts[0..15]，不受影响）—— 但「加在末尾」这件事本身没有门：列写错位、
// 写成第 3 列、或者写成空串，都不会被任何现有门抓到 ⇒ ⇒ 量具产物会
// **静默降级**成「一列空」。
func TestWriteRowEmitsSortKeysAsLastColumn(t *testing.T) {
	var out strings.Builder
	writeRow(&out, Shape{ShapeID: "shape-1"}, "local", "seek",
		"idx_a", "session_turns_hot:Index Scan", "ts", "",
		0, 42, 7, "file.go:1", "$1", "", "session_turns_hot", "", 0,
		"ts DESC;tenant_id ASC")
	fields := strings.Split(strings.TrimRight(out.String(), "\n"), "\t")
	if len(fields) != 17 {
		t.Fatalf("TSV 行有 %d 列, want 17（末尾列是 sort_keys）", len(fields))
	}
	if fields[16] != "ts DESC;tenant_id ASC" {
		t.Fatalf("末尾列 = %q, want %q（sort_keys 必须落在末尾列）", fields[16], "ts DESC;tenant_id ASC")
	}
	// 既有消费方按列序读 parts[0..15] ⇒ 前 16 列的位置不能被这一列挤动。
	if fields[0] != "shape-1" || fields[2] != "seek" || fields[15] != "0" {
		t.Fatalf("前 16 列被 sort_keys 挤动了：%v", fields[:16])
	}
}

// TestWriteRowEmitsEmptySortKeysWhenNoSort：★ 没有排序列时末尾列是空串
// （而不是 "-" / "none" 之类占位）—— 消费方按「空 = 没有 Sort 节点」判。
func TestWriteRowEmitsEmptySortKeysWhenNoSort(t *testing.T) {
	var out strings.Builder
	writeRow(&out, Shape{ShapeID: "shape-2"}, "local", "seek",
		"", "", "", "", 0, 1, 3, "file.go:2", "", "", "", "", 0, "")
	fields := strings.Split(strings.TrimRight(out.String(), "\n"), "\t")
	if len(fields) != 17 {
		t.Fatalf("TSV 行有 %d 列, want 17", len(fields))
	}
	if fields[16] != "" {
		t.Fatalf("末尾列 = %q, want 空串", fields[16])
	}
}
