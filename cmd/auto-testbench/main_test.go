package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

func TestLabelStatsF1(t *testing.T) {
	// 2 gold hits, 1 miss to another label, 1 foreign false positive.
	s := labelStats{tp: 2, fp: 1, fn: 1}
	if got := s.precision(); got != 2.0/3.0 {
		t.Fatalf("precision = %v, want 2/3", got)
	}
	if got := s.recall(); got != 2.0/3.0 {
		t.Fatalf("recall = %v, want 2/3", got)
	}
	if got := s.f1(); got != 2.0/3.0 {
		t.Fatalf("f1 = %v, want 2/3", got)
	}
	if got := (labelStats{}).f1(); got != 0 {
		t.Fatalf("empty f1 = %v, want 0", got)
	}
}

func TestClassificationMetricsFinalize(t *testing.T) {
	m := newClassificationMetrics()
	// 3 chat 全对; 2 code 中 1 个被判 reasoning; 1 planning 对。
	m.add("chat", "chat", suiteCase{Name: "a"}, 0.9, "")
	m.add("chat", "chat", suiteCase{Name: "b"}, 0.9, "")
	m.add("chat", "chat", suiteCase{Name: "c"}, 0.9, "")
	m.add("code", "reasoning", suiteCase{Name: "d"}, 0.6, "")
	m.add("code", "code", suiteCase{Name: "e"}, 0.9, "")
	m.add("planning", "planning", suiteCase{Name: "f"}, 0.9, "")
	// generated 候选行：不进任何指标。
	m.add("chat", "code", suiteCase{Name: "g", Generated: true}, 0.5, "")
	m.finalize()

	if m.Total != 6 || m.Correct != 5 {
		t.Fatalf("total/correct = %d/%d, want 6/5 (generated excluded)", m.Total, m.Correct)
	}
	if m.Generated != 1 {
		t.Fatalf("generated = %d, want 1", m.Generated)
	}
	if want := 5.0 / 6.0; m.Accuracy != want {
		t.Fatalf("accuracy = %v, want %v", m.Accuracy, want)
	}
	// chat: P=1 R=1 F1=1; code: P=1(.5? no: tp=1,fp=0)→1, R=0.5→F1=2/3;
	// planning: F1=1; reasoning: tp=0,fp=1,fn=0→P=0→F1=0。
	if m.LabelF1["chat"] != 1 || m.LabelF1["planning"] != 1 {
		t.Fatalf("chat/planning F1 = %v/%v, want 1/1", m.LabelF1["chat"], m.LabelF1["planning"])
	}
	if m.LabelF1["code"] != 2.0/3.0 {
		t.Fatalf("code F1 = %v, want 2/3", m.LabelF1["code"])
	}
	if m.LabelF1["reasoning"] != 0 {
		t.Fatalf("reasoning F1 = %v, want 0", m.LabelF1["reasoning"])
	}
	wantMacro := (1 + 2.0/3.0 + 1 + 0) / 4
	if m.MacroF1 != wantMacro {
		t.Fatalf("macroF1 = %v, want %v", m.MacroF1, wantMacro)
	}
	if len(m.Failures) != 1 || m.Failures[0].Name != "d" {
		t.Fatalf("failures = %v, want [d]", m.Failures)
	}
}

func TestGRRQFormula(t *testing.T) {
	if got := grrq(1, 0); got != 100 {
		t.Fatalf("grrq(1,0) = %v, want 100", got)
	}
	// 过配率 10% → 罚 3 分。
	if got := grrq(0.95, 0.1); got != 92 {
		t.Fatalf("grrq(0.95,0.1) = %v, want 92", got)
	}
}

func TestL1TierMappingCoversAllTaskTypes(t *testing.T) {
	for _, tt := range autoroute.AllTaskTypes {
		tier := resolveTier(string(tt))
		if tier != "tier-a" && tier != "tier-b" && tier != "tier-c" {
			t.Fatalf("task %s resolved to unknown tier %q", tt, tier)
		}
		if _, ok := l1TierDefaults[string(tt)]; !ok {
			t.Errorf("task %s missing from l1TierDefaults (falls to tier-a)", tt)
		}
	}
	// chat 默认经济档——过配定义的前提。
	if resolveTier("chat") != "tier-c" {
		t.Fatalf("chat tier = %s, want tier-c", resolveTier("chat"))
	}
}

func TestOverProvisionAccounting(t *testing.T) {
	tm := &tierMetrics{TierDistribution: map[string]int{}}
	// chat 判对（tier-c）：不过配。chat 被判成 code_audit（tier-a）：过配。
	// chat 被判成 code（tier-b）：按"落 tier-a"口径不过配。code 金标签不参与。
	tm.add("chat", suiteCase{Name: "a"}, "tier-c")
	tm.add("chat", suiteCase{Name: "b"}, "tier-a")
	tm.add("chat", suiteCase{Name: "c"}, "tier-b")
	tm.add("code", suiteCase{Name: "d"}, "tier-a")
	tm.add("chat", suiteCase{Name: "e", Generated: true}, "tier-a")
	tm.finalize()
	if tm.SimpleTotal != 3 {
		t.Fatalf("simpleTotal = %d, want 3", tm.SimpleTotal)
	}
	if tm.OverProvisioned != 1 {
		t.Fatalf("overProvisioned = %d, want 1", tm.OverProvisioned)
	}
	if want := 1.0 / 3.0; tm.OverProvisionRate != want {
		t.Fatalf("rate = %v, want %v", tm.OverProvisionRate, want)
	}
}

func TestGateEvaluation(t *testing.T) {
	b := &GateBaseline{}
	b.Thresholds.MinAccuracy = 0.95
	b.Thresholds.MinMacroF1 = 0.95
	b.Thresholds.MinGRRQ = 90

	cm := newClassificationMetrics()
	cm.Accuracy = 0.96
	cm.MacroF1 = 0.95
	if v := evaluateGate(b, cm, 91); !v.Passed {
		t.Fatalf("expected pass, got %v", v.Lines)
	}
	cm.Accuracy = 0.94
	if v := evaluateGate(b, cm, 91); v.Passed {
		t.Fatalf("expected fail on accuracy, got pass")
	}
	cm.Accuracy = 0.96
	if v := evaluateGate(b, cm, 89.9); v.Passed {
		t.Fatalf("expected fail on grrq, got pass")
	}
}

func TestCandidateFromRowPrivacy(t *testing.T) {
	lang, mm, code := "zh", true, false
	r := featureRow{
		RequestID:        "req-abc",
		TaskType:         "code",
		HumanTaskType:    "chat",
		Classifier:       strPtr("heuristic"),
		Confidence:       0.83,
		DetectedLanguage: &lang,
		HasMultimedia:    &mm,
		HasCode:          &code,
	}
	c := candidateFromRow("corrections", r)
	if !c.Generated || c.LabelSource != "human_correction" {
		t.Fatalf("generated/label_source = %v/%s", c.Generated, c.LabelSource)
	}
	if c.ExpectedTask != "chat" {
		t.Fatalf("gold = %s, want chat (human label)", c.ExpectedTask)
	}
	if !strings.Contains(c.Prompt, "generated-candidate") {
		t.Fatalf("prompt must be a placeholder, got %q", c.Prompt)
	}
	if c.Name != "gen_corrections_"+hash8("req-abc") {
		t.Fatalf("name = %s", c.Name)
	}
	if !c.Image {
		t.Fatalf("multimedia indicator should map to image=true")
	}
	if !strings.Contains(c.ExpectNote, "auto=code -> human=chat") {
		t.Fatalf("expect_note should record the correction verdict: %s", c.ExpectNote)
	}
	// selections 行无 human 标签 → 弱金标签回退 auto 值。
	c2 := candidateFromRow("selections", featureRow{RequestID: "r2", TaskType: "vision"})
	if c2.ExpectedTask != "vision" || c2.LabelSource != "auto_stratified" {
		t.Fatalf("selections candidate = %+v", c2)
	}
}

func TestSuiteLoaderInvariants(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	content := "# comment line\n" +
		`{"name":"n1","expected_task":"chat","prompt":"你好"}` + "\n\n" +
		`{"name":"n2","expected_task":"chat","prompt":"早"}` + "\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := loadSuiteFiles([]string{p})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("cases = %d, want 2 (comment+blank skipped)", len(cases))
	}

	dup := filepath.Join(dir, "dup.jsonl")
	dupContent := `{"name":"n1","expected_task":"chat","prompt":"x"}` + "\n"
	if err := os.WriteFile(dup, []byte(dupContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSuiteFiles([]string{p, dup}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}

	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte(`{"name":"n3","prompt":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSuiteFiles([]string{bad}); err == nil {
		t.Fatal("expected missing expected_task error")
	}
}

// 回归冒烟：套件文件在默认路径存在且可加载（套件本身的分类正确性由
// autoroute 包内 TestAutoMatchingSuiteHeuristic 钉桩，这里只钉 loader 契约）。
func TestDefaultSuiteFilesLoadable(t *testing.T) {
	for _, f := range strings.Split(defaultSuites, ",") {
		if _, err := os.Stat(resolveRepoPath(f)); err != nil {
			t.Fatalf("default suite %s missing: %v (P0② 套件文件必须随仓分发)", f, err)
		}
	}
	cases, err := loadSuiteFiles(strings.Split(defaultSuites, ","))
	if err != nil {
		t.Fatalf("load default suites: %v", err)
	}
	if len(cases) < 200 {
		t.Fatalf("default suites total %d cases, want >=200 (P0② 套件扩充门禁)", len(cases))
	}
}

func strPtr(s string) *string { return &s }
