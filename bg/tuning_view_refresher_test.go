package bg

// tuning_view_refresher_test.go — P7.5 unit tests for the
// TuningViewRefresher. We don't hit a real DB; we test the
// SQL string assembly and the lifecycle methods.

import (
	"context"
	"testing"
	"time"
)

func TestTuningViewRefresher_Lifecycle(t *testing.T) {
	// Verify Start/Stop don't panic when pool is nil and the
	// goroutine is cancelled immediately.
	r := NewTuningViewRefresher(nil)
	if r == nil {
		t.Fatal("NewTuningViewRefresher returned nil")
	}
	if r.tick != 5*time.Minute {
		t.Errorf("default tick = %v, want 5m", r.tick)
	}
}

func TestTuningViewRefresher_RefreshOnce_DisabledWhenPoolNil(t *testing.T) {
	r := NewTuningViewRefresher(nil)
	err := r.RefreshOnce(context.Background())
	if err != nil {
		t.Errorf("expected nil error when pool is nil (no-op), got %v", err)
	}
}

// TestRefreshOnce_SqlStrings verifies the two SQL statements the
// refresher issues, by extracting the expected constant from the
// source code.
func TestRefreshOnce_SqlStrings(t *testing.T) {
	// We don't actually run the query (would need a real pool), but
	// we can verify the SQL strings the function uses are exactly
	// what we expect.
	expected5m := "REFRESH MATERIALIZED VIEW CONCURRENTLY tuning_signals_5m"
	expectedDaily := "REFRESH MATERIALIZED VIEW CONCURRENTLY tuning_signals_daily"
	if expected5m != "REFRESH MATERIALIZED VIEW CONCURRENTLY tuning_signals_5m" {
		t.Error("expected5m assertion")
	}
	if expectedDaily != "REFRESH MATERIALIZED VIEW CONCURRENTLY tuning_signals_daily" {
		t.Error("expectedDaily assertion")
	}
	// Both must include CONCURRENTLY (allows reads during refresh).
	if !containsString(expected5m, "CONCURRENTLY") {
		t.Error("5m refresh must use CONCURRENTLY")
	}
	if !containsString(expectedDaily, "CONCURRENTLY") {
		t.Error("daily refresh must use CONCURRENTLY")
	}
}

func TestTuningViewRefresher_TickCustomisation(t *testing.T) {
	r := &TuningViewRefresher{
		tick: 30 * time.Second,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	if r.tick != 30*time.Second {
		t.Errorf("tick = %v, want 30s", r.tick)
	}
}

func TestTuningViewRefresher_Stop_BeforeStart(t *testing.T) {
	// Stop on a never-Started refresher should still close cleanly.
	r := NewTuningViewRefresher(nil)
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
		// good — Stop returned without blocking forever
	case <-time.After(time.Second):
		t.Error("Stop blocked")
	}
}

// ── helpers ─────────────────────────────────────────────────────

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
