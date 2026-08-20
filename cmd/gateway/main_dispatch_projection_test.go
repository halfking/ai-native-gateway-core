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
	errCh := make(chan string, 1)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if view := holder.Snapshot(); view == nil {
					select {
					case errCh <- "snapshot must never be nil":
					default:
					}
					return
				}
				if snapshot := holder.SnapshotWaterfall(10, "", 0); snapshot.Requests == nil {
					select {
					case errCh <- "waterfall requests must never be nil":
					default:
					}
					return
				}
			}
		}()
	}

	holder.Close()
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}

	if snapshot := holder.Snapshot(); snapshot.Wired {
		t.Fatalf("closed holder returned wired snapshot: %+v", snapshot)
	}
	if waterfall := holder.SnapshotWaterfall(10, "", 0); waterfall.Wired || waterfall.Requests == nil {
		t.Fatalf("closed holder waterfall = %+v", waterfall)
	}
}
