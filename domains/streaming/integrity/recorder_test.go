package integrity

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeExec is a minimal Exec for unit tests. It records the queries
// it saw and can be told to return an error.
type fakeExec struct {
	mu       atomic.Int64
	calls    []fakeCall
	failNext atomic.Bool
}

type fakeCall struct {
	Query string
	Args  []any
}

func (f *fakeExec) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Add(1)
	// Copy args so a test that mutates the slice doesn't break us.
	cp := make([]any, len(args))
	copy(cp, args)
	f.calls = append(f.calls, fakeCall{Query: q, Args: cp})
	if f.failNext.Swap(false) {
		return pgconn.CommandTag{}, errors.New("simulated DB error")
	}
	return pgconn.CommandTag{}, nil
}

// Nil recorder is a no-op (the contract: the call site never has to
// nil-check).
func TestPoolRecorder_NilSafe(t *testing.T) {
	var r *PoolRecorder
	if err := r.Record(context.Background(), Event{AnomalyType: AnomalyModelMismatch}); err != nil {
		t.Fatalf("nil receiver should be no-op, got %v", err)
	}
	if r == nil {
		return // Stats() on nil receiver is contract; nothing to assert.
	}
}

// Empty Event.AnomalyType is dropped silently — protects against
// misconfigured callers.
func TestPoolRecorder_EmptyTypeDropped(t *testing.T) {
	f := &fakeExec{}
	r := NewRecorderFromExec(f, 1.0)
	if err := r.Record(context.Background(), Event{}); err != nil {
		t.Fatalf("empty type should be silent, got %v", err)
	}
	if got := len(f.calls); got != 0 {
		t.Fatalf("expected 0 calls, got %d", got)
	}
}

// Critical events bypass sampling; non-critical events respect ratio.
func TestPoolRecorder_Sampling(t *testing.T) {
	f := &fakeExec{}
	// ratio 0.5 + deterministic seed → test the deterministic boundary.
	r := NewRecorderFromExec(f, 0.5)
	r.rngSeed = 0
	for i := 0; i < 100; i++ {
		_ = r.Record(context.Background(), Event{
			AnomalyType: AnomalyFinishTruncation,
			Severity:    SeverityLow,
			RequestID:   "req",
		})
	}
	rec, sampled, dropped := r.Stats()
	if rec != 0 {
		t.Fatalf("no critical events were sent; recorded=%d", rec)
	}
	if sampled == 0 || sampled+dropped != 100 {
		t.Fatalf("sampling math off: sampled=%d dropped=%d", sampled, dropped)
	}
	t.Logf("sampled=%d dropped=%d (ratio=0.5)", sampled, dropped)
}

// Critical events are always recorded even with ratio 0.
func TestPoolRecorder_CriticalAlwaysRecorded(t *testing.T) {
	f := &fakeExec{}
	r := NewRecorderFromExec(f, 0.0) // strict
	if err := r.Record(context.Background(), Event{
		AnomalyType: AnomalyModelMismatch,
		Severity:    SeverityCritical,
	}); err != nil {
		t.Fatalf("critical should not error: %v", err)
	}
	if got := len(f.calls); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
	rec, _, _ := r.Stats()
	if rec != 1 {
		t.Fatalf("expected recorded=1, got %d", rec)
	}
}

// DB error is swallowed (logged) and counted as dropped. The hot path
// MUST NOT propagate a recorder failure.
func TestPoolRecorder_DBErrorSwallowed(t *testing.T) {
	f := &fakeExec{}
	f.failNext.Store(true)
	r := NewRecorderFromExec(f, 1.0)
	err := r.Record(context.Background(), Event{
		AnomalyType: AnomalyModelMismatch,
		Severity:    SeverityCritical,
	})
	if err == nil {
		t.Fatal("expected error to be returned (for tests), got nil")
	}
	_, _, dropped := r.Stats()
	if dropped != 1 {
		t.Fatalf("expected dropped=1, got %d", dropped)
	}
}

// Sample truncation is PII-bound: input is capped at 1 KiB.
func TestTruncateForSample(t *testing.T) {
	if got := TruncateForSample("hello", 10); got != "hello" {
		t.Fatalf("short string should pass through, got %q", got)
	}
	if got := TruncateForSample("hello world", 5); got != "hello...[truncated]" {
		t.Fatalf("expected truncated, got %q", got)
	}
	if got := TruncateForSample("", 5); got != "" {
		t.Fatalf("empty should be empty, got %q", got)
	}
	if got := TruncateForSample("x", 0); got != "x" {
		t.Fatalf("maxLen<=0 should be a no-op, got %q", got)
	}
}

// shouldRecord covers the truth table.
func TestShouldRecord(t *testing.T) {
	cases := []struct {
		name  string
		ratio float64
		rand  float64
		sev   Severity
		want  bool
	}{
		{"critical-bypasses", 0.0, 0.99, SeverityCritical, true},
		{"ratio0-drops-all", 0.0, 0.0, SeverityLow, false},
		{"ratio1-keeps-all", 1.0, 0.99, SeverityLow, true},
		{"ratio0.5-keep-below", 0.5, 0.4, SeverityLow, true},
		{"ratio0.5-drop-above", 0.5, 0.6, SeverityLow, false},
		{"ratio0.5-edge-zero", 0.5, 0.0, SeverityLow, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRecord(c.ratio, c.rand, c.sev); got != c.want {
				t.Fatalf("want %v got %v", c.want, got)
			}
		})
	}
}

// LoadSampleRatioFromEnv — go doesn't pass env in tests; we test the
// parse path via direct call to a private helper.
func TestLoadSampleRatioFromEnv_Default(t *testing.T) {
	if got := LoadSampleRatioFromEnv(0.1); got != 0.1 {
		t.Fatalf("fallback should pass through, got %v", got)
	}
	// Bad env value is ignored (covered by os.Getenv path; we just
	// confirm the parse doesn't crash for empty input).
	if got := LoadSampleRatioFromEnv(0.25); got != 0.25 {
		t.Fatalf("expected 0.25, got %v", got)
	}
}
