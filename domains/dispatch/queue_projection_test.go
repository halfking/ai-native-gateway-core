package dispatch

import (
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
	if len(view.Credentials) != 1 || view.Credentials[0].Mode != ModeConcurrency {
		t.Fatalf("modeless delta must preserve the credential lane mode: %+v", view.Credentials)
	}
	if len(view.Models) != 2 || view.Models[0].Model != "a-model" || view.Models[1].Model != "z-model" {
		t.Fatalf("models not sorted: %+v", view.Models)
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
