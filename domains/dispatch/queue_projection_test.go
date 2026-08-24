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
