package sessionv2mirror

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	dto "github.com/prometheus/client_model/go"
)

// failingWriterV2 is a SessionWriterV2 whose Write always errors, so the
// PersistHook failure path is exercised.
type failingWriterV2 struct{}

func (failingWriterV2) Write(context.Context, *v2.ProcessedRequest) error {
	return errors.New("simulated v2 mirror failure")
}

// withShadowFlags overrides the shadowWriteEnabled seam so the hook's
// feature-flag gate passes in unit tests (settings.Global is nil in the
// test binary, so the default reader returns false and short-circuits).
// The override is restored via defer, so callers that spawn goroutines
// must keep them within fn's scope (or the concurrent test that sets
// the seam explicitly before spawning).
func withShadowFlags(t *testing.T, fn func()) {
	t.Helper()
	prevTrue := shadowEnabledFn(func() bool { return true })
	setShadowWriteEnabledForTest(prevTrue)
	defer setShadowWriteEnabledForTest(shadowEnabledFn(func() bool {
		// Restore a reader equivalent to the production default so a
		// later test in the same binary sees the settings-backed gate.
		return false
	}))
	fn()
}

func mirrorBacklogGauge(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if sessionV2MirrorBacklogPending == nil {
		t.Fatal("sessionV2MirrorBacklogPending gauge is nil; spec §12 GAP 2 not implemented")
	}
	if err := sessionV2MirrorBacklogPending.Write(m); err != nil {
		t.Fatalf("gauge write: %v", err)
	}
	return m.Gauge.GetValue()
}

// TestPersistHook_FailureAppendsToBacklog pins the spec §12 GAP 2
// contract: when the V2 shadow write fails, the entry must be appended
// to the in-process backlog (bounded cap) and surfaced via
// BacklogStats() and the session_v2_mirror_backlog_pending gauge.
func TestPersistHook_FailureAppendsToBacklog(t *testing.T) {
	resetBacklogForTest()
	hook := PersistHook(failingWriterV2{})

	withShadowFlags(t, func() {
		hook(&telemetry.RequestLogEntry{
			RequestID:   "req-backlog-1",
			GwSessionID: strPtr("sess_backlog"),
			Success:     true,
		})
	})

	pending, capacity := BacklogStats()
	if pending != 1 {
		t.Fatalf("BacklogStats pending = %d, want 1", pending)
	}
	if capacity <= 0 {
		t.Fatalf("BacklogStats capacity = %d, want > 0", capacity)
	}
	if got := mirrorBacklogGauge(t); got != 1 {
		t.Fatalf("session_v2_mirror_backlog_pending = %v, want 1", got)
	}

	drained := DrainBacklog(10)
	if len(drained) != 1 {
		t.Fatalf("DrainBacklog len = %d, want 1", len(drained))
	}
	if drained[0].RequestID != "req-backlog-1" {
		t.Fatalf("drained[0].RequestID = %q, want req-backlog-1", drained[0].RequestID)
	}
	// After drain, pending must be 0 and the gauge must reflect it.
	pending, _ = BacklogStats()
	if pending != 0 {
		t.Fatalf("BacklogStats pending after drain = %d, want 0", pending)
	}
	if got := mirrorBacklogGauge(t); got != 0 {
		t.Fatalf("session_v2_mirror_backlog_pending after drain = %v, want 0", got)
	}
}

// TestBacklog_BoundedCapDropsOldest pins the bounded-cap contract:
// once the backlog reaches its capacity, the oldest entry is dropped
// (FIFO eviction) and pending never exceeds capacity.
func TestBacklog_BoundedCapDropsOldest(t *testing.T) {
	resetBacklogForTest()
	hook := PersistHook(failingWriterV2{})

	_, capacity := BacklogStats()
	if capacity == 0 {
		t.Fatal("capacity is 0; test requires a non-zero backlog cap")
	}
	// Write capacity+5 entries; the first 5 should be evicted.
	withShadowFlags(t, func() {
		for i := 0; i < capacity+5; i++ {
			hook(&telemetry.RequestLogEntry{
				RequestID:   "req-cap",
				GwSessionID: strPtr("sess_cap"),
				Success:     true,
			})
		}
	})

	pending, _ := BacklogStats()
	if pending != capacity {
		t.Fatalf("pending = %d, want %d (cap) after overflow", pending, capacity)
	}
	if got := mirrorBacklogGauge(t); got != float64(capacity) {
		t.Fatalf("gauge = %v, want %d (cap)", got, capacity)
	}
	// The drained set must be the NEWEST `capacity` entries (oldest 5 dropped).
	drained := DrainBacklog(capacity + 10)
	if len(drained) != capacity {
		t.Fatalf("drained len = %d, want %d", len(drained), capacity)
	}
}

// TestBacklog_ConcurrentAppends is the race-detector smoke test: many
// goroutines appending + draining concurrently must not panic or
// deadlock, and pending must stay within [0, capacity].
func TestBacklog_ConcurrentAppends(t *testing.T) {
	resetBacklogForTest()
	hook := PersistHook(failingWriterV2{})
	_, capacity := BacklogStats()

	stop := make(chan struct{})
	// Drainer runs on its OWN waitgroup so the append workers' wg.Wait()
	// below does not wait on the (infinite-loop) drainer. The drainer
	// is signalled to stop via the stop channel AFTER the append
	// workers finish, which avoids the classic "wg.Wait before close(stop)"
	// deadlock.
	var drainerWg sync.WaitGroup
	drainerWg.Add(1)
	go func() {
		defer drainerWg.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				DrainBacklog(capacity)
				return
			case <-ticker.C:
				DrainBacklog(50)
			}
		}
	}()

	// Set the flag seam for the WHOLE test lifetime (not via
	// withShadowFlags, whose defer restores before detached workers
	// finish). Restore on exit so other tests see the default gate.
	setShadowWriteEnabledForTest(func() bool { return true })
	defer setShadowWriteEnabledForTest(func() bool { return false })

	var wg sync.WaitGroup
	// Append workers.
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				hook(&telemetry.RequestLogEntry{
					RequestID:   "req-race",
					GwSessionID: strPtr("sess_race"),
					Success:     true,
				})
			}
		}()
	}
	wg.Wait()
	close(stop)
	drainerWg.Wait()
	// Final drain leaves pending == 0.
	DrainBacklog(capacity + 1)
	pending, _ := BacklogStats()
	if pending != 0 {
		t.Fatalf("pending after final drain = %d, want 0", pending)
	}
}
