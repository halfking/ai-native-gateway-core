package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-09-28 auto-routing audit, follow-up.
//
// Two defects in the E2E merge layer, both discovered while trying to read the
// selection layer out of a real audit run:
//
//  1. `pass` is emitted as JSON null when the case errored upstream. Decoding
//     that into a bool yields false, so every 429 was reported as a
//     classification FAIL. During the audit that produced "0/240 pass" while
//     the classification layer was in fact 240/240 — the number a reader would
//     act on was wrong in the pessimistic direction, and a real regression
//     could not be told apart from an upstream blip.
//
//  2. The selection layer was invisible. e2eRow did not decode `decision` at
//     all, so a total collapse of the candidate pool onto the 48h popularity
//     fallback — the exact defect the audit found in 124/240 cases — produced
//     no signal in the merged report.

func writeE2E(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "e2e.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// row builds one audit JSONL line. wantPass == nil emits `"pass": null`, which
// is what autoroute-e2e-audit writes for an upstream-errored case.
// decision.task_type is driven by `got`, mirroring the real audit where
// got_task is read back out of X-Gw-Auto-Decision.
func row(name, want, got string, wantPass *bool, fallback bool, ncand int) string {
	cands := make([]map[string]any, 0, ncand)
	for i := 0; i < ncand; i++ {
		cands = append(cands, map[string]any{
			"model": name + "-cand", "route_tier": "primary", "match_score": 50,
		})
	}
	payload := map[string]any{
		"name":          name,
		"bucket":        "b",
		"expected_task": want,
		"got_task":      got,
		"served_model":  name + "-model",
		"http_status":   200,
		"decision": map[string]any{
			"task_type": got, "chosen_model": name + "-model",
			"fallback_used": fallback, "candidates_top3": cands,
		},
	}
	if wantPass == nil {
		payload["pass"] = nil
		payload["error"] = "upstream rate_limit_exceeded: Rate limit exceeded"
	} else {
		payload["pass"] = *wantPass
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func boolp(b bool) *bool { return &b }

// An upstream errored case must not be counted as a classification FAIL, and
// its decision must still contribute to the classification verdict.
func TestLoadE2EReport_UpstreamErrorIsNotAClassificationFail(t *testing.T) {
	// pass:true, then a 429 that was still classified correctly, then a real miss.
	path := writeE2E(t,
		row("ok", "code", "code", boolp(true), false, 3),
		row("errored-but-correct", "code", "code", nil, false, 3),
		row("real-miss", "code", "chat", boolp(false), false, 3),
	)
	s, err := loadE2EReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 3 {
		t.Fatalf("Total = %d, want 3", s.Total)
	}
	if s.Pass != 1 {
		t.Errorf("Pass = %d, want 1", s.Pass)
	}
	if s.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (the 429 row)", s.Errors)
	}
	// Only the genuine miss belongs in Failures.
	if len(s.Failures) != 1 {
		t.Fatalf("Failures = %d, want 1 (got %+v)", len(s.Failures), s.Failures)
	}
	if s.Failures[0].Name != "real-miss" {
		t.Errorf("failure name = %q, want real-miss", s.Failures[0].Name)
	}
	// The 429 row still carried a correct decision, so it must count.
	if s.Decided != 3 {
		t.Errorf("Decided = %d, want 3", s.Decided)
	}
	if s.ClassCorrect != 2 {
		t.Errorf("ClassCorrect = %d, want 2 (errored-but-correct + ok)", s.ClassCorrect)
	}
}

// A row whose dispatch errored but whose decision was nonetheless wrong is a
// real miss that pass==null would otherwise have hidden.
func TestLoadE2EReport_ErrorMaskingAMissIsStillAFailure(t *testing.T) {
	s, err := loadE2EReport(writeE2E(t,
		row("errored-and-wrong", "code", "chat", nil, false, 3),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Failures) != 1 {
		t.Fatalf("Failures = %d, want 1 — an errored row with a wrong decision is still a miss", len(s.Failures))
	}
	if s.ClassCorrect != 0 || s.Decided != 1 {
		t.Errorf("ClassCorrect/Decided = %d/%d, want 0/1", s.ClassCorrect, s.Decided)
	}
	if s.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (it did error upstream too)", s.Errors)
	}
}

// The headline 2026-09-28 numbers, as a regression pin: a fully-collapsed run
// must report 100% classification accuracy alongside a 100% collapse rate.
// These are the two facts that were previously indistinguishable.
func TestLoadE2EReport_SelectionCollapseIsVisible(t *testing.T) {
	var lines []string
	// 2 cases keep their pool, 3 collapse onto the popularity fallback.
	lines = append(lines,
		row("kept-1", "code", "code", nil, false, 3),
		row("kept-2", "chat", "chat", nil, false, 3),
		row("collapsed-1", "code", "code", nil, true, 1),
		row("collapsed-2", "creative", "creative", nil, true, 1),
		row("collapsed-3", "vision", "vision", nil, true, 1),
	)
	s, err := loadE2EReport(writeE2E(t, lines...))
	if err != nil {
		t.Fatal(err)
	}
	if s.Decided != 5 {
		t.Fatalf("Decided = %d, want 5", s.Decided)
	}
	if s.ClassAccuracy() != 1.0 {
		t.Errorf("ClassAccuracy = %v, want 1.0 (every decision was correct)", s.ClassAccuracy())
	}
	if s.FallbackCollapsed != 3 {
		t.Errorf("FallbackCollapsed = %d, want 3", s.FallbackCollapsed)
	}
	if got := s.CollapseRate(); got != 3.0/5.0 {
		t.Errorf("CollapseRate = %v, want 0.6", got)
	}
	if s.SingleCandidate != 3 {
		t.Errorf("SingleCandidate = %d, want 3 (only the collapsed rows)", s.SingleCandidate)
	}
	if s.CollapseByTask["code"] != 1 || s.CollapseByTask["creative"] != 1 || s.CollapseByTask["vision"] != 1 {
		t.Errorf("CollapseByTask = %v, want one each for code/creative/vision", s.CollapseByTask)
	}
	if len(s.CollapseByTask) != 3 {
		t.Errorf("CollapseByTask has %d entries, want 3 — the healthy tasks must not be attributed",
			len(s.CollapseByTask))
	}
	// A fully healthy run must report zero, not "unknown".
	if s.CollapseRate() <= 0 {
		t.Error("collapse rate must be a real number, not zero-by-absence")
	}
}

func TestLoadE2EReport_HealthyRunHasNoCollapse(t *testing.T) {
	s, err := loadE2EReport(writeE2E(t,
		row("a", "code", "code", boolp(true), false, 3),
		row("b", "vision", "vision", boolp(true), false, 3),
	))
	if err != nil {
		t.Fatal(err)
	}
	if s.CollapseRate() != 0 {
		t.Errorf("CollapseRate = %v, want 0 for a healthy run", s.CollapseRate())
	}
	if s.SingleCandidate != 0 {
		t.Errorf("SingleCandidate = %d, want 0", s.SingleCandidate)
	}
	if s.Errors != 0 {
		t.Errorf("Errors = %d, want 0", s.Errors)
	}
	if len(s.CollapseByTask) != 0 {
		t.Errorf("CollapseByTask = %v, want empty", s.CollapseByTask)
	}
}

// Rows with no decision (e.g. a 401 before the hot path) must not be counted
// as decided, and must not silently inflate the classification denominator.
func TestLoadE2EReport_RowsWithoutDecisionAreExcluded(t *testing.T) {
	raw := `{"name":"no-decision","bucket":"b","expected_task":"code","got_task":"","pass":null,` +
		`"error":"invalid_key","served_model":""}`
	rawOK := `{"name":"has-decision","bucket":"b","expected_task":"code","got_task":"code","pass":true,` +
		`"decision":{"task_type":"code","chosen_model":"m","fallback_used":false,` +
		`"candidates_top3":[{"model":"a"},{"model":"b"},{"model":"c"}]}}`
	s, err := loadE2EReport(writeE2E(t, raw, rawOK))
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 2 {
		t.Fatalf("Total = %d, want 2", s.Total)
	}
	if s.Decided != 1 {
		t.Fatalf("Decided = %d, want 1", s.Decided)
	}
	if s.ClassAccuracy() != 1.0 {
		t.Errorf("ClassAccuracy = %v, want 1.0 over the single decided row", s.ClassAccuracy())
	}
	if s.Errors != 1 {
		t.Errorf("Errors = %d, want 1", s.Errors)
	}
}

func TestLoadE2EReport_EmptyAndMissingInputs(t *testing.T) {
	if s, err := loadE2EReport(""); err != nil || s != nil {
		t.Errorf("loadE2EReport(\"\") = (%v, %v), want (nil, nil)", s, err)
	}
	if _, err := loadE2EReport(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Error("missing path must be a usage error (R64 contract), not a silent skip")
	}
	// A file of only comments must not panic and must report zero decided.
	s, err := loadE2EReport(writeE2E(t, "# comment", ""))
	if err != nil {
		t.Fatal(err)
	}
	if s.Decided != 0 || s.CollapseRate() != 0 || s.ClassAccuracy() != 0 {
		t.Errorf("empty report = %+v, want all-zero metrics", s)
	}
}

// The derived rates must reach the JSON report, since that is what CI and any
// downstream comparison would read.
func TestE2ESummarySerialisesSelectionMetrics(t *testing.T) {
	s, err := loadE2EReport(writeE2E(t,
		row("a", "code", "code", nil, true, 1),
		row("b", "code", "code", nil, false, 3),
	))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		`"fallback_collapsed":1`, `"single_candidate_pools":1`,
		`"collapse_rate":0.5`, `"classification_accuracy":1`,
		`"upstream_errors":2`, `"decided":2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON report missing %s\ngot: %s", want, out)
		}
	}
}

// The markdown report is the artifact a human actually reads, so it must carry
// the selection layer too. Before this was pinned, a run with 124/240 collapsed
// cases and a healthy run rendered identical markdown — the R75 blind spot.
// The selection layer must be able to FAIL a build, not only be reported.
// Before this gate existed, a run that collapsed 124/240 onto the 48h
// popularity fallback still reported "GATE: PASS", because the gate only knew
// about accuracy / macro_f1 / grrq and the baseline was saturated at 1/1/100.
func TestEvaluateE2EGate_CollapseRateFailsTheGate(t *testing.T) {
	b := &e2eBaseline{MaxCollapseRate: 0.05}

	// 3 of 5 decided cases collapsed = 0.6, far above the threshold.
	collapsed, err := loadE2EReport(writeE2E(t,
		row("kept-1", "code", "code", nil, false, 3),
		row("kept-2", "chat", "chat", nil, false, 3),
		row("collapsed-1", "code", "code", nil, true, 1),
		row("collapsed-2", "creative", "creative", nil, true, 1),
		row("collapsed-3", "vision", "vision", nil, true, 1),
	))
	if err != nil {
		t.Fatal(err)
	}
	evaluated, line, failed := evaluateE2EGate(b, collapsed)
	if !evaluated {
		t.Fatal("gate must evaluate when an E2E report with decided cases is present")
	}
	if !failed {
		t.Errorf("collapse rate 0.6 above max 0.05 must fail the gate; line=%q", line)
	}

	// The healthy run must pass, and must say so.
	healthy, err := loadE2EReport(writeE2E(t,
		row("a", "code", "code", boolp(true), false, 3),
		row("b", "vision", "vision", boolp(true), false, 3),
	))
	if err != nil {
		t.Fatal(err)
	}
	evaluated, line, failed = evaluateE2EGate(b, healthy)
	if !evaluated || failed {
		t.Errorf("healthy run must pass the selection gate; evaluated=%v failed=%v line=%q",
			evaluated, failed, line)
	}
}

// No E2E report means the collapse rate is unknown, not zero. Treating that as
// a pass would recreate the very blind spot this gate closes.
func TestEvaluateE2EGate_NoE2ERunIsSkippedNotPassed(t *testing.T) {
	b := &e2eBaseline{MaxCollapseRate: 0.05}
	evaluated, line, failed := evaluateE2EGate(b, nil)
	if evaluated {
		t.Error("no E2E report must not be reported as evaluated")
	}
	if failed {
		t.Error("no E2E report must not fail the run")
	}
	if !strings.Contains(line, "not evaluated") {
		t.Errorf("skip must be stated explicitly, got %q", line)
	}

	// An E2E report where nothing produced a decision is the same situation.
	empty, err := loadE2EReport(writeE2E(t, "# comment", ""))
	if err != nil {
		t.Fatal(err)
	}
	if evaluated, _, failed := evaluateE2EGate(b, empty); evaluated || failed {
		t.Error("an E2E report with zero decided cases must be skipped, not gated")
	}
}

// A zero threshold would fail on legitimate empty-pool fallbacks, and a
// threshold above 1 gates nothing — both are usage errors, not gate failures.
func TestValidateE2EBaseline_RejectsUngatableThresholds(t *testing.T) {
	if err := validateE2EBaseline(&e2eBaseline{MaxCollapseRate: 0}); err == nil {
		t.Error("max_collapse_rate = 0 must be rejected (fails on legitimate fallbacks)")
	}
	if err := validateE2EBaseline(&e2eBaseline{MaxCollapseRate: -0.1}); err == nil {
		t.Error("negative max_collapse_rate must be rejected")
	}
	if err := validateE2EBaseline(&e2eBaseline{MaxCollapseRate: 1.5}); err == nil {
		t.Error("max_collapse_rate > 1 must be rejected")
	}
	if err := validateE2EBaseline(&e2eBaseline{MaxCollapseRate: 0.05}); err != nil {
		t.Errorf("0.05 must be accepted: %v", err)
	}
}

// The checked-in baseline must stay loadable and semantically valid; a broken
// file would turn every E2E run into a usage error. The default constant is
// repo-relative (scripts/auto-testbench.sh runs from the repo root) while this
// test runs from the package directory, so both forms are checked.
func TestE2EBaselineFileIsValid(t *testing.T) {
	if want := "cmd/auto-testbench/testdata/e2e_baseline.json"; defaultE2EBaseline != want {
		t.Errorf("defaultE2EBaseline = %q, want %q (scripts/auto-testbench.sh resolves it from the repo root)", defaultE2EBaseline, want)
	}
	b, err := loadE2EBaseline(filepath.Join("testdata", "e2e_baseline.json"))
	if err != nil {
		t.Fatalf("checked-in e2e baseline is unusable: %v", err)
	}
	if b.MaxCollapseRate <= 0 || b.MaxCollapseRate > 1 {
		t.Errorf("checked-in max_collapse_rate = %v, want (0,1]", b.MaxCollapseRate)
	}
	if b.Source == "" || b.Notes == "" {
		t.Error("checked-in e2e baseline must record its source and rationale")
	}
}

func TestE2EMarkdownSection_SurfacesSelectionLayer(t *testing.T) {
	s, err := loadE2EReport(writeE2E(t,
		row("kept", "code", "code", nil, false, 3),
		row("collapsed-code", "code", "code", nil, true, 1),
		row("collapsed-creative", "creative", "creative", nil, true, 1),
	))
	if err != nil {
		t.Fatal(err)
	}
	md := e2eMarkdownSection(s)
	for _, want := range []string{
		"上游报错 3 例",               // dispatch failures are not verdicts
		"选型层（取到决策的 3 例）",         // denominator is decided cases
		"坍缩到 48h 热度兜底 2（0.6667）", // collapse rate, not just a count
		"单候选池 2",
		"分类层（取到决策的 3 例）：3/3（1.0000）",
		"### 坍缩分任务归因",
		"| code | 1/2 |", // one of two decided code cases collapsed
		"| creative | 1/1 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown E2E section missing %q\ngot:\n%s", want, md)
		}
	}
}

// A healthy run must render a real zero, and must not claim a collapse
// attribution table it has no data for.
func TestE2EMarkdownSection_HealthyRunHasNoAttributionTable(t *testing.T) {
	s, err := loadE2EReport(writeE2E(t,
		row("a", "code", "code", boolp(true), false, 3),
		row("b", "vision", "vision", boolp(true), false, 3),
	))
	if err != nil {
		t.Fatal(err)
	}
	md := e2eMarkdownSection(s)
	if !strings.Contains(md, "坍缩到 48h 热度兜底 0（0.0000）") {
		t.Errorf("healthy run must report a real zero collapse rate\ngot:\n%s", md)
	}
	if strings.Contains(md, "### 坍缩分任务归因") {
		t.Errorf("healthy run must not emit an empty attribution table\ngot:\n%s", md)
	}
	if strings.Contains(md, "上游报错") && !strings.Contains(md, "上游报错 0 例") {
		t.Errorf("upstream error line must be present and zero\ngot:\n%s", md)
	}
}
