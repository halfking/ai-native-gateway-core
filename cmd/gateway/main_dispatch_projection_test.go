package main

import (
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

func TestQueueProjectionHolderConcurrentSnapshotAndClose(t *testing.T) {
	var holder queueProjectionHolder
	holder.Store(dispatch.NewQueueProjection())

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if view := holder.Snapshot(); view == nil {
					t.Fatal("snapshot must never be nil")
				}
				if snapshot := holder.SnapshotWaterfall(10, "", 0); snapshot.Requests == nil {
					t.Fatal("waterfall requests must never be nil")
				}
			}
		}()
	}

	holder.Close()
	wg.Wait()

	if snapshot := holder.Snapshot(); snapshot.Wired {
		t.Fatalf("closed holder returned wired snapshot: %+v", snapshot)
	}
	if waterfall := holder.SnapshotWaterfall(10, "", 0); waterfall.Wired || waterfall.Requests == nil {
		t.Fatalf("closed holder waterfall = %+v", waterfall)
	}
}
