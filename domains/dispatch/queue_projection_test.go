package dispatch

import (
	"fmt"
	"sync"
	"testing"
)

func TestQueueProjectionAppliesOutOfOrderDeltasAndReturnsSortedDetachedSnapshot(t *testing.T) {
	projection := NewQueueProjection()
	projection.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "z-model", Depth: 1, Delta: 1})
	projection.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "a-model", Depth: 1, Delta: 1})
	projection.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "z-model", Depth: 0, Delta: -1})
	projection.ObserveQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: 22, Mode: ModeConcurrency, Depth: 1, Delta: 1})
	projection.ObserveQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: 22, Delta: -1})
	projection.ObserveQueue(QueueObservation{Kind: QueueInFlight, InFlight: 1, Delta: 1})

	view := projection.Snapshot()
	if view.Pipeline == nil || view.Pipeline.Depth != 1 || view.Pipeline.InFlight != 1 {
		t.Fatalf("projection stats = %+v", view.Pipeline)
	}
	if len(view.Credentials) != 0 {
		t.Fatalf("zero-depth credential lane must be removed: %+v", view.Credentials)
	}
	if len(view.Models) != 1 || view.Models[0].Model != "a-model" {
		t.Fatalf("zero-depth model lane must be removed and remaining models sorted: %+v", view.Models)
	}
	projection.ObserveQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: 22, Mode: ModeConcurrency, Delta: 1})
	view = projection.Snapshot()
	if len(view.Credentials) != 1 || view.Credentials[0].Mode != ModeConcurrency {
		t.Fatalf("re-enqueued credential lane must restore its mode: %+v", view.Credentials)
	}
	view.Models[0].Depth = 99
	if got := projection.Snapshot().Models[0].Depth; got == 99 {
		t.Fatal("snapshot exposed mutable projection state")
	}
}

func TestQueueProjectionConcurrentObserveSnapshotAndClose(t *testing.T) {
	projection := NewQueueProjection()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				projection.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "model", Delta: 1, Depth: int64(j + 1)})
				_ = projection.Snapshot()
				if i == 0 && j == 100 {
					projection.Close()
				}
			}
		}(i)
	}
	wg.Wait()
	projection.Close()
	if snapshot := projection.Snapshot(); snapshot.Wired {
		t.Fatalf("closed projection reported wired: %+v", snapshot)
	}
}

func TestQueueProjectionWaterfallIsDetachedAndCloseStopsObservations(t *testing.T) {
	projection := NewQueueProjection()
	completed := WaterfallRequest{
		RequestID: "request-1",
		Model:     "model-a",
		Attempts: []WaterfallAttempt{{
			AttemptID: "attempt-1",
			AttemptNo: 1,
		}},
	}
	projection.ObserveQueue(QueueObservation{Kind: QueueRequestCompleted, Completed: &completed})

	snapshot := projection.SnapshotWaterfall(1, "", 0, "")
	if !snapshot.Wired || len(snapshot.Requests) != 1 {
		t.Fatalf("waterfall snapshot = %+v", snapshot)
	}
	snapshot.Requests[0].Attempts[0].AttemptID = "mutated"
	again := projection.SnapshotWaterfall(1, "", 0, "")
	if got := again.Requests[0].Attempts[0].AttemptID; got != "attempt-1" {
		t.Fatalf("waterfall attempts were not detached: %q", got)
	}

	projection.Close()
	projection.ObserveQueue(QueueObservation{Kind: QueueRequestCompleted, Completed: &WaterfallRequest{RequestID: "request-2"}})
	closed := projection.SnapshotWaterfall(10, "", 0, "")
	if closed.Wired || len(closed.Requests) != 0 {
		t.Fatalf("closed projection waterfall = %+v", closed)
	}
}

func TestQueueProjectionOverflowDegradedIsVisibleToConcurrentReaders(t *testing.T) {
	projection := NewQueueProjection()
	projection.ObserveQueue(QueueObservation{Kind: QueueOverflow, OverflowReason: "queue_full"})
	for i := 0; i < 8; i++ {
		view := projection.Snapshot()
		if view.Pipeline == nil || !view.Pipeline.Degraded {
			t.Fatalf("reader %d did not observe overflow degradation: %+v", i, view.Pipeline)
		}
	}
}

// pushCompleted feeds one qualifying completion with the given wait.
func pushCompleted(projection *QueueProjection, requestID string, waitMS int) {
	projection.ObserveQueue(QueueObservation{Kind: QueueRequestCompleted, Completed: &WaterfallRequest{
		RequestID:      requestID,
		ArrivedAt:      "2026-08-24T00:00:00Z",
		CredDequeuedAt: "2026-08-24T00:00:01Z",
		QueueWaitMS:    waitMS,
	}})
}

func TestQueueProjectionPercentilesMatchWindowSemantics(t *testing.T) {
	projection := NewQueueProjection()
	// Empty window: percentiles absent, not zero.
	if view := projection.Snapshot(); view.Pipeline.WaitingMsP50 != nil || view.Pipeline.WaitingMsP95 != nil {
		t.Fatalf("empty window percentiles = %v/%v, want nil", view.Pipeline.WaitingMsP50, view.Pipeline.WaitingMsP95)
	}

	// Non-qualifying samples (missing dequeue boundary, zero wait) must be
	// excluded from the window.
	projection.ObserveQueue(QueueObservation{Kind: QueueRequestCompleted, Completed: &WaterfallRequest{
		RequestID: "no-boundary", ArrivedAt: "2026-08-24T00:00:00Z", QueueWaitMS: 9999,
	}})
	projection.ObserveQueue(QueueObservation{Kind: QueueRequestCompleted, Completed: &WaterfallRequest{
		RequestID: "zero-wait", ArrivedAt: "t", CredDequeuedAt: "t", QueueWaitMS: 0,
	}})
	if view := projection.Snapshot(); view.Pipeline.WaitingMsP50 != nil {
		t.Fatalf("non-qualifying samples must not produce percentiles, got p50=%v", view.Pipeline.WaitingMsP50)
	}

	// 100 qualifying samples with waits 1..100: nearest-rank p50=50, p95=95.
	for w := 1; w <= 100; w++ {
		pushCompleted(projection, fmt.Sprintf("req-%03d", w), w)
	}
	view := projection.Snapshot()
	if view.Pipeline.WaitingMsP50 == nil || *view.Pipeline.WaitingMsP50 != 50 {
		t.Fatalf("p50 = %v, want 50", view.Pipeline.WaitingMsP50)
	}
	if view.Pipeline.WaitingMsP95 == nil || *view.Pipeline.WaitingMsP95 != 95 {
		t.Fatalf("p95 = %v, want 95", view.Pipeline.WaitingMsP95)
	}

	// Eviction wrap: push 200 more samples (waits 101..300). The ring cap is
	// 200, so the window becomes exactly waits 101..300: sorted index 99 →
	// 200 for p50; nearest-rank 95% → index ceil(0.95*200)-1 = 189 → 290.
	for w := 101; w <= 300; w++ {
		pushCompleted(projection, fmt.Sprintf("req-%03d", w), w)
	}
	view = projection.Snapshot()
	if *view.Pipeline.WaitingMsP50 != 200 {
		t.Fatalf("post-eviction p50 = %d, want 200", *view.Pipeline.WaitingMsP50)
	}
	if *view.Pipeline.WaitingMsP95 != 290 {
		t.Fatalf("post-eviction p95 = %d, want 290", *view.Pipeline.WaitingMsP95)
	}

	// Detached percentile boxes: mutating the returned value must not leak
	// into the next snapshot.
	*view.Pipeline.WaitingMsP50 = -1
	if again := projection.Snapshot(); *again.Pipeline.WaitingMsP50 != 200 {
		t.Fatalf("percentile boxes are shared, got %d", *again.Pipeline.WaitingMsP50)
	}
}

func TestQueueProjectionPercentileWindowSurvivesDuplicateWaits(t *testing.T) {
	projection := NewQueueProjection()
	// Duplicated wait values must remove exactly one instance on eviction.
	for i := 0; i < waterfallRingCap+50; i++ {
		pushCompleted(projection, fmt.Sprintf("dup-%03d", i), 42)
	}
	view := projection.Snapshot()
	if view.Pipeline.WaitingMsP50 == nil || *view.Pipeline.WaitingMsP50 != 42 {
		t.Fatalf("duplicate-wait p50 = %v, want 42", view.Pipeline.WaitingMsP50)
	}
	if *view.Pipeline.WaitingMsP95 != 42 {
		t.Fatalf("duplicate-wait p95 = %v, want 42", view.Pipeline.WaitingMsP95)
	}
}

func BenchmarkQueueProjectionSnapshot(b *testing.B) {
	projection := benchmarkQueueProjection()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = projection.Snapshot()
	}
}

func BenchmarkQueueProjectionSnapshotWaterfall(b *testing.B) {
	projection := benchmarkQueueProjection()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = projection.SnapshotWaterfall(50, "", 0, "")
	}
}

func benchmarkQueueProjection() *QueueProjection {
	projection := NewQueueProjection()
	for i := 0; i < 24; i++ {
		projection.ObserveQueue(QueueObservation{
			Kind:  QueueModelDepth,
			Model: fmt.Sprintf("model-%02d", i),
			Depth: int64(i + 1),
		})
		projection.ObserveQueue(QueueObservation{
			Kind:         QueueCredentialDepth,
			CredentialID: i + 1,
			Mode:         ModeConcurrency,
			Depth:        int64(i + 2),
			Limit:        32,
		})
	}
	for i := 0; i < waterfallRingCap; i++ {
		projection.ObserveQueue(QueueObservation{
			Kind: QueueRequestCompleted,
			Completed: &WaterfallRequest{
				RequestID:      fmt.Sprintf("request-%03d", i),
				Model:          fmt.Sprintf("model-%02d", i%24),
				ArrivedAt:      "2026-08-24T00:00:00Z",
				CredDequeuedAt: "2026-08-24T00:00:00Z",
				QueueWaitMS:    i + 1,
				Attempts:       []WaterfallAttempt{{AttemptID: "attempt-1"}},
			},
		})
	}
	return projection
}
