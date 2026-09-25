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

// known_failure 语义与 autoroute/auto_matching_suite_test.go 对齐（2026-09-25
// 批判复审）：triaged gap 失败不进指标（否则门禁假红）；意外判对（xpass）
// 单独计数供报告清理 stale marker。
func TestKnownFailureExcludedFromMetrics(t *testing.T) {
	m := newClassificationMetrics()
	m.add("chat", "chat", suiteCase{Name: "ok1"}, 0.9, "")
	m.add("code", "chat", suiteCase{Name: "kf-miss", KnownFailure: true}, 0.4, "")
	m.add("chat", "chat", suiteCase{Name: "kf-xpass", KnownFailure: true}, 0.9, "")
	m.finalize()

	if m.Total != 1 || m.Correct != 1 || m.Accuracy != 1 {
		t.Fatalf("total/correct/accuracy = %d/%d/%v, want 1/1/1 (known_failure excluded)", m.Total, m.Correct, m.Accuracy)
	}
	if m.KnownFails != 2 {
		t.Fatalf("knownFails = %d, want 2", m.KnownFails)
	}
	if m.KnownXPass != 1 {
		t.Fatalf("knownXPass = %d, want 1", m.KnownXPass)
	}
	if len(m.Failures) != 0 {
		t.Fatalf("failures = %v, want none", m.Failures)
	}
	if _, ok := m.PerLabel["code"]; ok {
		t.Fatal("known_failure gold label must not create a per-label entry")
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

// R64（P3）：负值舍入方向。round3 走 math.Round（半值远离零），
// floor2 走 math.Floor（阈值只降不升）。
func TestRoundingNegativeValues(t *testing.T) {
	cases := []struct {
		name   string
		in     float64
		wantR3 float64
		wantF2 float64
	}{
		{"positive round", 0.1235, 0.124, 0.12},
		{"positive truncate", 0.999, 0.999, 0.99},
		{"negative half away", -0.1235, -0.124, -0.13},
		{"negative grrq", -90.129, -90.129, -90.13},
		{"negative toward zero forbidden", -0.121, -0.121, -0.13},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := round3(tc.in); got != tc.wantR3 {
				t.Fatalf("round3(%v) = %v, want %v", tc.in, got, tc.wantR3)
			}
			if got := floor2(tc.in); got != tc.wantF2 {
				t.Fatalf("floor2(%v) = %v, want %v", tc.in, got, tc.wantF2)
			}
		})
	}
	// 关键不变量：floor2(v) <= v 恒成立（旧实现对负值朝零截断会抬高阈值）。
	for _, v := range []float64{-90.129, -0.001, 0, 0.999, 100} {
		if floor2(v) > v {
			t.Fatalf("floor2(%v) = %v raised the value", v, floor2(v))
		}
	}
}

// R64（P2）：基线语义校验——零阈值/过期库存必须被拒（exit 2），合法基线放行。
func TestValidateBaseline(t *testing.T) {
	valid := func() *GateBaseline {
		b := &GateBaseline{}
		b.SuiteFiles = []string{"a.jsonl", "b.jsonl"}
		b.TotalCases = 240
		b.Thresholds.MinAccuracy = 1
		b.Thresholds.MinMacroF1 = 1
		b.Thresholds.MinGRRQ = 100
		return b
	}
	suites := []string{"a.jsonl", "b.jsonl"}

	if err := validateBaseline(valid(), suites, 240); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}
	// 同一套件、顺序不同 → 视为一致（-suite 是无序集合）。
	if err := validateBaseline(valid(), []string{"b.jsonl", "a.jsonl"}, 240); err != nil {
		t.Fatalf("reordered suite set should pass: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*GateBaseline)
		total  int
	}{
		{"zero min_accuracy", func(b *GateBaseline) { b.Thresholds.MinAccuracy = 0 }, 240},
		{"zero min_macro_f1", func(b *GateBaseline) { b.Thresholds.MinMacroF1 = 0 }, 240},
		{"zero min_grrq", func(b *GateBaseline) { b.Thresholds.MinGRRQ = 0 }, 240},
		{"negative min_grrq", func(b *GateBaseline) { b.Thresholds.MinGRRQ = -1 }, 240},
		{"total_cases mismatch", func(b *GateBaseline) {}, 241},
		{"suite_files mismatch", func(b *GateBaseline) { b.SuiteFiles = []string{"a.jsonl", "c.jsonl"} }, 240},
		{"missing suite_files", func(b *GateBaseline) { b.SuiteFiles = nil }, 240},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := valid()
			tc.mutate(b)
			if err := validateBaseline(b, suites, tc.total); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

// R64（P3）：CLI 组合矛盾显式拒绝（表驱动）。
func TestValidateFlags(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		explicit []string
		days     int
		limit    int
		wantErr  bool
	}{
		{"regression defaults", "regression", nil, 30, 100, false},
		{"regression gate-default", "regression", []string{"gate-default"}, 30, 100, false},
		{"regression gate+gate-default conflict", "regression", []string{"gate", "gate-default"}, 30, 100, true},
		{"generate clean", "generate", nil, 30, 100, false},
		{"generate days zero floor ok", "generate", nil, 0, 1, false},
		{"generate with -report", "generate", []string{"report"}, 30, 100, true},
		{"generate with -gate", "generate", []string{"gate"}, 30, 100, true},
		{"generate with -gate-default", "generate", []string{"gate-default"}, 30, 100, true},
		{"generate with -write-baseline", "generate", []string{"write-baseline"}, 30, 100, true},
		{"generate with -e2e-report", "generate", []string{"e2e-report"}, 30, 100, true},
		{"generate with -suite", "generate", []string{"suite"}, 30, 100, true},
		{"generate negative days", "generate", nil, -1, 100, true},
		{"generate zero limit", "generate", nil, 30, 0, true},
		{"generate negative limit", "generate", nil, 30, -5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			explicit := map[string]bool{}
			for _, f := range tc.explicit {
				explicit[f] = true
			}
			err := validateFlags(tc.mode, explicit, tc.days, tc.limit)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for mode=%s explicit=%v days=%d limit=%d", tc.mode, tc.explicit, tc.days, tc.limit)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// R64（P3）：-e2e-report 指定但读不到 → 报错（main 里 exit 2），不再静默忽略。
func TestLoadE2EReport(t *testing.T) {
	if s, err := loadE2EReport(""); err != nil || s != nil {
		t.Fatalf("empty path = %v, %v; want nil, nil", s, err)
	}
	if _, err := loadE2EReport(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("unreadable -e2e-report must return an error")
	}

	p := filepath.Join(t.TempDir(), "e2e.jsonl")
	content := "# comment\n" +
		`{"name":"n1","expected_task":"chat","got_task":"chat","pass":true,"served_model":"m-a"}` + "\n" +
		"\n" +
		`{"name":"n2","expected_task":"code","got_task":"chat","pass":false,"served_model":"m-b"}` + "\n" +
		"not-json\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := loadE2EReport(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Total != 2 || s.Pass != 1 || len(s.Failures) != 1 || s.Failures[0].Name != "n2" {
		t.Fatalf("summary = %+v", s)
	}
	if s.Models["m-a"] != 1 || s.Models["m-b"] != 1 {
		t.Fatalf("models = %v", s.Models)
	}
}

// R64（P3）：1:N join 重名候选去重——同 request_id 多行只保留首行。
func TestDedupeByRequestID(t *testing.T) {
	rows := []featureRow{
		{RequestID: "r1", TaskType: "chat"},
		{RequestID: "r2", TaskType: "code"},
		{RequestID: "r1", TaskType: "code"}, // 同 request_id 重放行
		{RequestID: "r3", TaskType: "chat"},
	}
	out := dedupeByRequestID(rows)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[0].RequestID != "r1" || out[0].TaskType != "chat" {
		t.Fatalf("first row must be kept: %+v", out[0])
	}
	if n := len(dedupeByRequestID(nil)); n != 0 {
		t.Fatalf("empty input should yield empty output, got %d", n)
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
