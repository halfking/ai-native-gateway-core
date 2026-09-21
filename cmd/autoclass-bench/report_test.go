package main

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestReportAccuracyAndMacroF1(t *testing.T) {
	rep := newReport([]string{"a", "b"})
	rep.add("a", "a", "", 10) // TP a
	rep.add("a", "a", "", 20) // TP a
	rep.add("a", "b", "", 30) // FN a, FP b
	rep.add("b", "b", "", 40) // TP b

	if rep.total != 4 || rep.correct != 3 {
		t.Fatalf("total=%d correct=%d, want 4/3", rep.total, rep.correct)
	}
	if !near(rep.accuracy(), 0.75) {
		t.Errorf("accuracy=%f, want 0.75", rep.accuracy())
	}
	// a: P=1, R=2/3, F1=0.8; b: P=0.5, R=1, F1=2/3; macro=(0.8+2/3)/2
	want := (0.8 + 2.0/3.0) / 2
	if !near(rep.macroF1(), want) {
		t.Errorf("macroF1=%f, want %f", rep.macroF1(), want)
	}
}

func TestReportInvalidPredictionNotAFalsePositive(t *testing.T) {
	rep := newReport([]string{"a", "b"})
	rep.add("a", "bogus-label", "", 5)

	if rep.invalid != 1 {
		t.Fatalf("invalid=%d, want 1", rep.invalid)
	}
	if _, ok := rep.stats["bogus-label"]; ok {
		t.Error("invalid label must not create per-label stats")
	}
	if rep.stats["a"].FN != 1 {
		t.Errorf("FN=%d, want 1", rep.stats["a"].FN)
	}
}

func TestReportErrorsExcludedFromAccuracy(t *testing.T) {
	rep := newReport([]string{"a"})
	rep.add("a", "a", "", 10)
	rep.add("a", "", "http_429", 0)
	rep.add("a", "", "timeout", 0)

	if rep.total != 1 || rep.correct != 1 {
		t.Fatalf("total=%d correct=%d, want 1/1 — errors must not deflate accuracy", rep.total, rep.correct)
	}
	if rep.errors["http_429"] != 1 || rep.errors["timeout"] != 1 {
		t.Errorf("errors=%v, want 429:1 timeout:1", rep.errors)
	}
	if len(rep.latencies) != 1 {
		t.Errorf("error samples must not contribute latencies, got %d", len(rep.latencies))
	}
}

func TestReportLatencyPercentileNearestRank(t *testing.T) {
	rep := newReport([]string{"a"})
	for _, ms := range []float64{40, 10, 30, 20} {
		rep.add("a", "a", "", ms)
	}
	if !near(rep.latencyPercentile(0.50), 20) {
		t.Errorf("p50=%f, want 20", rep.latencyPercentile(0.50))
	}
	if !near(rep.latencyPercentile(0.95), 40) {
		t.Errorf("p95=%f, want 40", rep.latencyPercentile(0.95))
	}
	if !near(rep.latencyPercentile(1), 40) {
		t.Errorf("p100=%f, want 40", rep.latencyPercentile(1))
	}
}

func TestExtractLabel(t *testing.T) {
	valid := func(s string) bool { return s == "code" || s == "chat" }

	cases := []struct {
		name    string
		content string
		want    string
		wantOK  bool
	}{
		{"bare", "code", "code", true},
		{"padded", "  code \n", "code", true},
		{"quoted", "\"code\"", "code", true},
		{"fenced", "```\ncode\n```", "code", true},
		{"json_label", `{"label": "code"}`, "code", true},
		{"json_task_type", `{"task_type": "chat"}`, "chat", true},
		{"unknown", "painting", "painting", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractLabel(tc.content, valid)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("extractLabel(%q) = (%q, %v), want (%q, %v)", tc.content, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestTopMisclassifications(t *testing.T) {
	rep := newReport([]string{"a", "b", "c"})
	rep.add("a", "b", "", 1)
	rep.add("a", "b", "", 1)
	rep.add("b", "c", "", 1)
	rep.add("a", "a", "", 1) // correct — must be excluded

	mis := rep.topMisclassifications(10)
	if len(mis) != 2 {
		t.Fatalf("len=%d, want 2", len(mis))
	}
	if mis[0].Expected != "a" || mis[0].Predicted != "b" || mis[0].Count != 2 {
		t.Errorf("top=%+v, want a->b x2 first", mis[0])
	}
}
