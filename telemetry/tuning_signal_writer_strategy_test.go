package telemetry

// tuning_signal_writer_strategy_test.go — P7.1: tests for the
// strategy column promotion. These verify the write-path sends the
// correct placeholder count (18 per row, including the new
// strategy column) and that the default-value handling is correct.

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakePool is a minimal poolExec that captures every call.
type fakePool struct {
	calls    []string
	lastArgs []any
}

func (f *fakePool) Exec(_ context.Context, sql string, args ...any) (commandTag, error) {
	f.calls = append(f.calls, sql)
	f.lastArgs = args
	return nilTuningTag{}, nil
}

func TestInsertBatch_StrategyColumnInQuery(t *testing.T) {
	rec := &fakePool{}
	w := &tuningWriter{pool: rec}

	// Three signals with different strategies
	signals := []TuningSignal{
		{RequestID: "r1", TaskType: "code", Classifier: "heuristic", Strategy: "baseline_heuristic"},
		{RequestID: "r2", TaskType: "chat", Classifier: "heuristic", Strategy: "pattern_layered"},
		{RequestID: "r3", TaskType: "code", Classifier: "llm", Strategy: "llm_fallback"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.insertBatch(ctx, signals); err != nil {
		t.Fatalf("insertBatch: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 SQL call, got %d", len(rec.calls))
	}
	sql := rec.calls[0]

	// 1. Column list must include strategy
	if !strings.Contains(sql, "strategy") {
		t.Error("INSERT column list missing 'strategy'")
	}

	// 2. Placeholder count should equal 18 * signals (one per column per row).
	placeholders := regexp.MustCompile(`\$\d+`).FindAllString(sql, -1)
	if got := len(placeholders); got != 18*len(signals) {
		t.Errorf("placeholder count = %d, want %d (18 cols × %d rows)",
			got, 18*len(signals), len(signals))
	}

	// 3. ON CONFLICT DO NOTHING must be present (idempotent inserts)
	if !strings.Contains(sql, "ON CONFLICT DO NOTHING") {
		t.Error("missing ON CONFLICT DO NOTHING")
	}
}

func TestInsertBatch_EmptyStrategyDefaultsToPatternLayered(t *testing.T) {
	// Empty Strategy should be normalised to 'pattern_layered' before
	// being passed to the insert (matches the SQL DEFAULT).
	rec := &fakePool{}
	w := &tuningWriter{pool: rec}

	signals := []TuningSignal{
		{RequestID: "r1", TaskType: "chat", Classifier: "heuristic", Strategy: ""}, // empty
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.insertBatch(ctx, signals); err != nil {
		t.Fatalf("insertBatch: %v", err)
	}

	// 18 args per row × 1 row = 18 args. The 18th (index 17) is strategy.
	if got := rec.lastArgs[17]; got != "pattern_layered" {
		t.Errorf("default strategy arg = %v, want 'pattern_layered'", got)
	}
}

func TestInsertBatch_StrategyPassthrough(t *testing.T) {
	// Non-empty strategy values should be passed through unchanged.
	for _, want := range []string{"baseline_heuristic", "pattern_layered", "llm_fallback"} {
		signals := []TuningSignal{
			{RequestID: "r1", TaskType: "chat", Classifier: "heuristic", Strategy: want},
		}
		rec := &fakePool{}
		w := &tuningWriter{pool: rec}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := w.insertBatch(ctx, signals); err != nil {
			t.Fatalf("insertBatch: %v", err)
		}
		if got := rec.lastArgs[17]; got != want {
			t.Errorf("strategy %q: arg = %v", want, got)
		}
	}
}
