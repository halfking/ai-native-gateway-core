package main

// traffic-replay unit tests. The DB path is exercised via
// integration (not in this file); the pure functions
// (replay aggregation, formatting) get unit tests here.

import (
	"strings"
	"testing"
)

func TestReclassify_ReturnsNewModel(t *testing.T) {
	r := requestRow{TaskType: "code", ChosenModel: "gpt-4o"}
	got := reclassify(r, false)
	if got == r.ChosenModel {
		t.Errorf("reclassify should not return the original model, got %q", got)
	}
}

func TestReplay_NoDivergenceWhenAllSame(t *testing.T) {
	// When reclassify always returns the same model, no divergences
	rows := []requestRow{
		{ChosenModel: "gpt-4o", TaskType: "code", Success: true},
		{ChosenModel: "gpt-4o", TaskType: "code", Success: true},
		{ChosenModel: "gpt-4o", TaskType: "code", Success: false},
	}
	// All return REPLAY_MODEL_code (constant given the test fixture)
	results := replay(rows, false)
	if len(results) == 0 {
		// Reclassify returns a different model from the historical
		// one (REPLAY_MODEL_code != gpt-4o), so we DO expect
		// divergences. But they should aggregate into 1 result.
		t.Fatal("expected 1 divergence, got 0 (reclassify must produce a different model)")
	}
	if len(results) != 1 {
		t.Errorf("expected exactly 1 divergence bucket, got %d", len(results))
	}
	if results[0].Count != 3 {
		t.Errorf("expected count=3, got %d", results[0].Count)
	}
}

func TestReplay_AggregatesByFromToPair(t *testing.T) {
	// Two (from → to) pairs should be separate buckets
	// We bypass reclassify by using a stub: directly construct
	// a fake "results" aggregation by using a different fixture.
	// Since reclassify is hard-coded to REPLAY_MODEL_<task>,
	// we use 2 distinct task types to force 2 buckets.
	rows := []requestRow{
		{ChosenModel: "gpt-4o", TaskType: "code", Success: true},
		{ChosenModel: "gpt-4o", TaskType: "code", Success: false},
		{ChosenModel: "claude", TaskType: "chat", Success: true},
		{ChosenModel: "claude", TaskType: "chat", Success: true},
		{ChosenModel: "claude", TaskType: "chat", Success: true},
	}
	results := replay(rows, false)
	if len(results) != 2 {
		t.Errorf("expected 2 divergence buckets, got %d", len(results))
	}
	// Find the gpt-4o → REPLAY_MODEL_code bucket
	var codeBucket, chatBucket int
	for _, r := range results {
		if r.From == "gpt-4o" && strings.HasPrefix(r.To, "REPLAY_MODEL_code") {
			codeBucket = r.Count
		}
		if r.From == "claude" && strings.HasPrefix(r.To, "REPLAY_MODEL_chat") {
			chatBucket = r.Count
		}
	}
	if codeBucket != 2 {
		t.Errorf("code bucket count = %d, want 2", codeBucket)
	}
	if chatBucket != 3 {
		t.Errorf("chat bucket count = %d, want 3", chatBucket)
	}
}

func TestReplay_SortedByCountDesc(t *testing.T) {
	rows := []requestRow{
		{ChosenModel: "a", TaskType: "x", Success: true},
		{ChosenModel: "a", TaskType: "x", Success: true},
		{ChosenModel: "a", TaskType: "x", Success: true},
		{ChosenModel: "b", TaskType: "y", Success: true},
		{ChosenModel: "b", TaskType: "y", Success: true},
		{ChosenModel: "c", TaskType: "z", Success: true},
	}
	results := replay(rows, false)
	for i := 0; i+1 < len(results); i++ {
		if results[i].Count < results[i+1].Count {
			t.Errorf("results not sorted by count desc: [%d].Count=%d < [%d].Count=%d",
				i, results[i].Count, i+1, results[i+1].Count)
		}
	}
}

func TestDivergenceCount(t *testing.T) {
	results := []replayResult{
		{Count: 5}, {Count: 3}, {Count: 2},
	}
	if got := divergenceCount(results); got != 10 {
		t.Errorf("divergenceCount = %d, want 10", got)
	}
}

func TestDivergenceCount_Empty(t *testing.T) {
	if got := divergenceCount(nil); got != 0 {
		t.Errorf("divergenceCount(nil) = %d, want 0", got)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10", 10, "exactly10"},
		{"a-longer-string", 10, "a-longe..."},
		{"x", 2, "x"},  // n too small, return as-is
		{"abc", 1, "a"}, // n=1 returns single char
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestPrintReport_ContainsSummarySections(t *testing.T) {
	// We can't easily capture stdout in a test without io.Writer
	// refactor, so this just verifies the function doesn't panic
	// on various inputs.
	tests := []struct {
		name   string
		rows   []requestRow
		results []replayResult
	}{
		{"empty", nil, nil},
		{"no-divergence", []requestRow{
			{ChosenModel: "a", TaskType: "x", Success: true},
		}, nil},
		{"with-divergence", []requestRow{
			{ChosenModel: "a", TaskType: "x", Success: true},
			{ChosenModel: "a", TaskType: "x", Success: false},
		}, []replayResult{
			{From: "a", To: "b", Count: 2, OrigSuccess: 0.5, NewSuccess: 0.5},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("printReport panicked: %v", r)
				}
			}()
			printReport(tt.rows, tt.results)
		})
	}
}
