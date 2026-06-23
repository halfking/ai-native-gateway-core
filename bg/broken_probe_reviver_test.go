package bg

import (
	"context"
	"testing"
	"time"
)

func TestNewBrokenProbeReviver_Defaults(t *testing.T) {
	w := NewBrokenProbeReviver(nil, 0, 0)
	if w == nil { t.Fatal("nil") }
	if w.interval != 30*time.Minute { t.Errorf("interval=%v want 30m", w.interval) }
	if w.reviveAfter != 30*time.Minute { t.Errorf("reviveAfter=%v want 30m", w.reviveAfter) }
}

func TestBrokenProbeReviver_Revive_NilPool(t *testing.T) {
	w := NewBrokenProbeReviver(nil, 0, 0)
	if err := w.revive(context.Background()); err != nil {
		t.Errorf("nil pool should be no-op, got %v", err)
	}
}

func TestBrokenProbeReviver_Stop_Idempotent(t *testing.T) {
	w := NewBrokenProbeReviver(nil, 10*time.Hour, 10*time.Hour)
	w.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	w.Stop()
	w.Stop() // must not panic
}
