package bg

import (
	"context"
	"testing"
	"time"
)

func TestStorageRetentionWorkerStopBeforeStartDoesNotBlock(t *testing.T) {
	w := NewStorageRetentionWorker(nil, "", nil)
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked before Start")
	}
}

func TestStorageRetentionWorkerStartAndStopAreIdempotent(t *testing.T) {
	w := NewStorageRetentionWorker(nil, "", nil)
	w.CheckInterval = time.Hour
	w.Start(context.Background())
	w.Start(context.Background())
	w.Stop()
	w.Stop()
}

func TestStorageRetentionWorkerStopsAfterParentContextCancellation(t *testing.T) {
	w := NewStorageRetentionWorker(nil, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked after context cancellation")
	}
}
