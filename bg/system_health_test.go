package bg

import (
	"testing"
	"time"
)

// TestSystemHealthWorker_LastDefaultsToSuspect ensures a freshly
// constructed worker (no pool, no tick yet) returns "suspect" — the
// homepage H badge should never be blank.
func TestSystemHealthWorker_LastDefaultsToSuspect(t *testing.T) {
	w := NewSystemHealthWorker(nil)
	if got := w.Last().Status; got != "suspect" {
		t.Fatalf("expected default suspect, got %q", got)
	}
}

// TestSystemHealthWorker_StartNoopOnNilPool ensures a nil pool does
// not start the goroutine (so the worker cannot silently swallow
// panics in production with db disabled).
func TestSystemHealthWorker_StartNoopOnNilPool(t *testing.T) {
	w := NewSystemHealthWorker(nil)
	w.Start(neverContext())
	if got := w.Last().Status; got != "suspect" {
		t.Fatalf("expected suspect to persist with no pool, got %q", got)
	}
}

// TestSystemHealthWorker_IntervalConstant matches the spec (30s).
func TestSystemHealthWorker_IntervalConstant(t *testing.T) {
	if SystemHealthInterval != 30*time.Second {
		t.Fatalf("expected 30s, got %v", SystemHealthInterval)
	}
}

type closedCtx struct{}

func (closedCtx) Deadline() (time.Time, bool)       { return time.Time{}, true }
func (closedCtx) Done() <-chan struct{}             { ch := make(chan struct{}); close(ch); return ch }
func (closedCtx) Err() error                        { return nil }
func (closedCtx) Value(any) any                     { return nil }

func neverContext() closedCtx { return closedCtx{} }
