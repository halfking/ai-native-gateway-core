package modelquality

import (
	"context"
	"testing"
)

// TestCalculateScore_PropagatesTriggerKindAndCounts guards the audit fix that
// made model_iq_runs record the real benchmark type, trigger, and question
// counts instead of hardcoded placeholders. The DB writer derives these fields
// from the QualityScore, so they must survive CalculateScore.
func TestCalculateScore_PropagatesTriggerKindAndCounts(t *testing.T) {
	calc := &ScoreCalculator{}
	report := &BenchmarkReport{
		ID:             "r-1",
		BenchmarkType:  BenchmarkTypeMMLULite,
		TriggerKind:    "anomaly",
		ModelName:      "glm-5.2",
		Provider:       "zhipu",
		CredentialID:   42,
		TotalQuestions: 50,
		CorrectCount:   40,
		Accuracy:       80,
		Results:        []TestResult{{QuestionID: "q1", Latency: 800, Correct: true}},
	}
	score := calc.CalculateScore(report)

	if score.TriggerKind != "anomaly" {
		t.Fatalf("TriggerKind not propagated: want anomaly, got %q", score.TriggerKind)
	}
	if score.BenchmarkType != BenchmarkTypeMMLULite {
		t.Fatalf("BenchmarkType not propagated: got %q", score.BenchmarkType)
	}
	if score.TotalQuestions != 50 || score.CorrectCount != 40 {
		t.Fatalf("counts not propagated: want 50/40, got %d/%d", score.TotalQuestions, score.CorrectCount)
	}
	if score.CredentialID != 42 {
		t.Fatalf("CredentialID not propagated: got %d", score.CredentialID)
	}
}

// TestNormalizeTriggerKind covers the mapping from internal monitor trigger
// labels (scheduled / anomaly:<reason> / on_demand) to the stable DB enum.
func TestNormalizeTriggerKind(t *testing.T) {
	cases := map[string]string{
		"scheduled":          "scheduled",
		"":                   "scheduled",
		"anomaly:score_drop": "anomaly",
		"anomaly":            "anomaly",
		"on_demand":          "on_demand",
	}
	for in, want := range cases {
		if got := normalizeTriggerKind(in); got != want {
			t.Errorf("normalizeTriggerKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNodeModelName confirms node identity prefers the raw_model_name over the
// request model (outbound_model_name / endpoint ID), so history stays keyed on
// the stable identity even when the request uses an endpoint ID.
func TestNodeModelName(t *testing.T) {
	if got := nodeModelName(CredentialNode{RawModelName: "doubao-seed-2.0-pro", RawModel: "ep-2024"}); got != "doubao-seed-2.0-pro" {
		t.Errorf("prefer raw_model_name: got %q", got)
	}
	if got := nodeModelName(CredentialNode{RawModel: "gpt-4o"}); got != "gpt-4o" {
		t.Errorf("fallback to request model: got %q", got)
	}
}

// TestDBStorage_NilPoolSaveScore guards against a panic when SaveScore is
// called on a zero-value DBStorage (defensive — main.go always injects a pool,
// but the compile-time interface assertion should still hold).
func TestDBStorage_NilPoolSaveScore(t *testing.T) {
	s := &DBStorage{}
	if err := s.SaveScore(context.Background(), &QualityScore{CredentialID: 1}); err == nil {
		t.Fatal("expected error on nil pool, got nil")
	}
}
