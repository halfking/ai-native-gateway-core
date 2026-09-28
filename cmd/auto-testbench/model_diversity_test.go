package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R77 D11-#8：候选池「多样性」指标此前只数**候选条数**，而 candidates_top3 的
// 三个条目是**三个凭据**，不是三个模型/三个上游。
//
// 实测证据（docs/audit/2026-09-28-auto-matching-e2e-results-r77-l5fixed.jsonl，
// L-5 修复后 240 例）：217/240（90.4%）的 top3 里只有 **1 个不同模型**，其余 23 例
// 有 2 个。`single_candidate_pools` 在这些例上全是 0——指标报「候选池健康」，而
// 模型级多样性其实等于 1。这是 D11-#7（md 出口盲点）的同型问题在指标语义上的复发：
// 数字进了三个出口，但它数的东西不是人以为的那个东西。
//
// 更进一步：凭据级多样性也不等于上游多样性。观测到的 failover 链
// request 63b1ecc1 是 cred 21 → cred 42，两条凭据同属 provider 14 MiniMax、
// 同一 `api.minimaxi.com`；429 侧 creds 3/4/25/49 则全部指向 `token.sensenova.cn`
// （三条不同 provider 记录 24/24/24/33089 却是同一 host）。所以「3 候选可退」这个
// 隐含假设在当前凭据池下经常不成立。
//
// 本轮只做**可观测性**：把 distinct-model 维度加进摘要与 md 出口，并如实标注
// 单候选池数的是条数不是模型数。**不改路由决策**——「候选池是否应当强制模型或上游
// 多样性」是路由策略裁决，需要 owner 拍板，不在审计轮里替 owner 定。

// TestE2ESummaryCountsDistinctModelsNotJustCandidates 钉住核心语义：
// 三个候选同一个模型时，候选条数 = 3（单候选池不计数），但 distinct model = 1
// （必须计入 model-monotone 计数）。
func TestE2ESummaryCountsDistinctModelsNotJustCandidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e2e.jsonl")

	// 4 例：3 例 top3 全同模型（凭据级多样性），1 例含 2 个不同模型。
	rows := []struct{ name, a, b, c string }{
		{"mono1", "glm-5.2", "glm-5.2", "glm-5.2"},
		{"mono2", "glm-5.2", "glm-5.2", "glm-5.2"},
		{"mono3", "deepseek-v4-flash", "deepseek-v4-flash", "deepseek-v4-flash"},
		{"mixed", "glm-5.2", "glm-5.1", "glm-5.1"},
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, `{"name":"`+r.name+`","bucket":"t","expected_task":"code","got_task":"code","pass":true,`+
			`"decision":{"task_type":"code","chosen_model":"`+r.a+`","fallback_used":false,"candidates_top3":[`+
			`{"model":"`+r.a+`","route_tier":"primary","match_score":50},`+
			`{"model":"`+r.b+`","route_tier":"primary","match_score":48},`+
			`{"model":"`+r.c+`","route_tier":"primary","match_score":47}]}}`)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := loadE2EReport(path)
	if err != nil {
		t.Fatalf("loadE2EReport: %v", err)
	}
	if s.Decided != 4 {
		t.Fatalf("Decided = %d, want 4", s.Decided)
	}
	// 4 例都是 3 条候选 → single_candidate_pools 应为 0（这是旧指标的读数，
	// 它看不出模型级坍缩，正是本条要暴露的盲点）。
	if s.SingleCandidate != 0 {
		t.Errorf("SingleCandidate = %d, want 0 (all four pools have 3 entries)", s.SingleCandidate)
	}
	// 但前 3 例是 model-monotone。
	if s.ModelMonotone != 3 {
		t.Errorf("ModelMonotone = %d, want 3 — 候选条数是 3 但只有 1 个不同模型，"+
			"这正是 SingleCandidate 看不见的那一层", s.ModelMonotone)
	}
	if s.DistinctModelRate() != 0.75 {
		t.Errorf("DistinctModelRate() = %.4f, want 0.75 (3 of 4 cases have a single distinct model)",
			s.DistinctModelRate())
	}
}

// TestE2ESummaryModelMonotoneCountsCollapsedPoolsToo 边界：fallback 坍缩的
// 单候选池本身就是 model-monotone，必须被计入，不能因为候选条数为 1 就漏掉。
func TestE2ESummaryModelMonotoneCountsCollapsedPoolsToo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e2e.jsonl")
	lines := []string{
		`{"name":"collapsed","bucket":"t","expected_task":"code_audit","got_task":"code_audit","pass":null,` +
			`"decision":{"task_type":"code_audit","chosen_model":"minimax-m3","fallback_used":true,` +
			`"candidates_top3":[{"model":"minimax-m3","route_tier":"","match_score":0}]}}`,
		// healthy 用 glm-5.2 ×3 —— 这是线上真实形态：三条候选是同一个模型的
		// 三个凭据。它不是「单候选池」（条目数 3），但同样没有模型级选择。
		`{"name":"healthy","bucket":"t","expected_task":"code","got_task":"code","pass":true,` +
			`"decision":{"task_type":"code","chosen_model":"glm-5.2","fallback_used":false,` +
			`"candidates_top3":[{"model":"glm-5.2","route_tier":"primary","match_score":50},` +
			`{"model":"glm-5.2","route_tier":"primary","match_score":48},` +
			`{"model":"glm-5.2","route_tier":"primary","match_score":47}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadE2EReport(path)
	if err != nil {
		t.Fatalf("loadE2EReport: %v", err)
	}
	if s.Decided != 2 {
		t.Fatalf("Decided = %d, want 2", s.Decided)
	}
	// collapsed 例 1 候选 → SingleCandidate 1；healthy 例 3 候选不同模型 2 个。
	if s.SingleCandidate != 1 {
		t.Errorf("SingleCandidate = %d, want 1", s.SingleCandidate)
	}
	// 两者都是 model-monotone（1 个不同模型）。
	if s.ModelMonotone != 2 {
		t.Errorf("ModelMonotone = %d, want 2 — 坍缩的单候选池与同模型三凭据都是 "+
			"model-monotone，只是前者更极端", s.ModelMonotone)
	}
}

// TestE2EMarkdownReportsModelDiversity 钉住 D11-#7 的三出口纪律：新指标必须同时进
// md 人读产物。只进 console/JSON 等于人读侧仍然是盲点。
func TestE2EMarkdownReportsModelDiversity(t *testing.T) {
	md := e2eMarkdownSection(&e2eSummary{
		Total:            240,
		Pass:             240,
		Decided:          240,
		ClassCorrect:     240,
		ClassAccuracyVal: 1.0,
		ModelMonotone:    217,
		SingleCandidate:  0,
	})
	for _, want := range []string{"model-monotone", "217/240"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown section missing %q — 人读产物会看不到模型级多样性盲点:\n%s", want, md)
		}
	}
}
