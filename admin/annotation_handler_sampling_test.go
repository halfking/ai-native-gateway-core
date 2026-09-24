package admin

// annotation_handler_sampling_test.go — 采样策略 v2（P0⑤，2026-09-24，
// docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md §4.5）的纯函数单测。
//
// 覆盖：strategy/per_strata 参数解析（默认/边界/非法值）、WHERE 构造的
// 占位符编号连续性、以及 querySamples 各策略的 SQL 形状关键片段（分歧
// 排序键、分层窗口函数、recent 的分页尾巴）。SQL 的真实执行由 Linux CI
// 的 admin 集成测试与线上标注工作台覆盖。

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseSamplingParams(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantStrat  string
		wantPerStr int
		wantErr    bool
	}{
		{"default", "", "recent", 5, false},
		{"explicit recent", "strategy=recent", "recent", 5, false},
		{"disagreement", "strategy=disagreement", "disagreement", 5, false},
		{"stratified", "strategy=stratified", "stratified", 5, false},
		{"stratified quota", "strategy=stratified&per_strata=12", "stratified", 12, false},
		{"quota zero falls back", "strategy=stratified&per_strata=0", "stratified", 5, false},
		{"quota capped", "strategy=stratified&per_strata=99", "stratified", 20, false},
		{"invalid strategy", "strategy=random", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			gotStrat, gotPer, err := parseSamplingParams(q)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if gotStrat != tc.wantStrat || gotPer != tc.wantPerStr {
				t.Fatalf("got %q/%d, want %q/%d", gotStrat, gotPer, tc.wantStrat, tc.wantPerStr)
			}
		})
	}
}

func TestBuildSamplesWherePlaceholders(t *testing.T) {
	// 无可选过滤：只有置信度区间一对占位符。
	where, args, next := buildSamplesWhere(samplesQueryOpts{minConfidence: 0, maxConfidence: 1})
	if !strings.Contains(where, "$1") || !strings.Contains(where, "$2") || next != 3 {
		t.Fatalf("bare where = %q next=%d", where, next)
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want 2", args)
	}

	// 全量过滤：start/end/confidence/annotator，编号必须连续无空洞。
	annotated := true
	where, args, next = buildSamplesWhere(samplesQueryOpts{
		startDate: "2026-09-01", endDate: "2026-09-24",
		minConfidence: 0.3, maxConfidence: 0.9,
		annotatedFilter: &annotated, annotatorFilter: "alice",
	})
	for i := 1; i <= len(args); i++ {
		if !strings.Contains(where, "$"+itoaForTest(i)) {
			t.Fatalf("placeholder $%d missing in %q", i, where)
		}
	}
	if next != len(args)+1 {
		t.Fatalf("next=%d, want %d (one past the last arg)", next, len(args)+1)
	}
	if !strings.Contains(where, "tha.request_id IS NOT NULL") || !strings.Contains(where, "tha.annotator = $") {
		t.Fatalf("filters missing: %q", where)
	}
}

// TestQuerySamplesSQLShape 用 nil 池触发 SQL 构造路径?不行——querySamples 直接
// 执行。改为对三个策略的关键 SQL 片段做构造器级断言：把 dataSQL 的构造
// 逻辑收敛在 buildSamplesDataSQL 里后本测试可直接覆盖。
func TestBuildSamplesDataSQLShape(t *testing.T) {
	// recent：保持既有形状（ORDER BY ts DESC + LIMIT/OFFSET）。
	recent := buildSamplesDataSQL(samplesQueryOpts{strategy: "recent"}, "ars.confidence >= $1 AND ars.confidence <= $2", 3)
	if !strings.Contains(recent, "ORDER BY ars.ts DESC") || !strings.Contains(recent, "LIMIT $3 OFFSET $4") {
		t.Fatalf("recent SQL shape drifted: %s", recent)
	}
	if strings.Contains(recent, "classifier <>") {
		t.Fatalf("recent must not depend on classifier column: %s", recent)
	}

	// disagreement：分歧行（非 heuristic 分类器）优先。
	dis := buildSamplesDataSQL(samplesQueryOpts{strategy: "disagreement"}, "ars.confidence >= $1 AND ars.confidence <= $2", 3)
	if !strings.Contains(dis, "(ars.classifier <> 'heuristic') DESC, ars.ts DESC") {
		t.Fatalf("disagreement ordering missing: %s", dis)
	}
	if !strings.Contains(dis, "LIMIT $3 OFFSET $4") {
		t.Fatalf("disagreement must keep paging: %s", dis)
	}

	// stratified：窗口函数配额 + 四档置信度桶 + 外层显式投影（无 rn 列）。
	strat := buildSamplesDataSQL(samplesQueryOpts{strategy: "stratified"}, "ars.confidence >= $1 AND ars.confidence <= $2", 3)
	for _, frag := range []string{
		"row_number() OVER (",
		"PARTITION BY ars.task_type",
		"WHEN ars.confidence >= 0.85 THEN 4",
		"WHEN ars.confidence >= 0.70 THEN 3",
		"WHEN ars.confidence >= 0.50 THEN 2",
		"WHERE sample_rn <= $3",
	} {
		if !strings.Contains(strat, frag) {
			t.Fatalf("stratified SQL missing %q: %s", frag, strat)
		}
	}
	// 外层投影不含 sample_rn（Scan 列数与其余策略一致 = 11 列）。
	selectPart := strat[strings.Index(strat, "SELECT ranked."):strings.Index(strat, "FROM (")]
	if strings.Contains(selectPart, "sample_rn") {
		t.Fatalf("outer projection must not expose sample_rn: %s", selectPart)
	}
	if strings.Count(selectPart, ",") != 10 { // 11 列 = 10 个逗号
		t.Fatalf("outer projection column count wrong: %q", selectPart)
	}
}

func itoaForTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
