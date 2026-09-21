package executors

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingContextLimitUpdater struct {
	mu    sync.Mutex
	calls int
}

func (u *recordingContextLimitUpdater) UpdateContextLimit(context.Context, int, string, int) (bool, error) {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	return true, nil
}

func (u *recordingContextLimitUpdater) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func TestContextLimitUpdateQueueStopDrains(t *testing.T) {
	updater := &recordingContextLimitUpdater{}
	q := NewContextLimitUpdateQueue(updater, 4)
	for i := 0; i < 4; i++ {
		if !q.Enqueue(7, i+1, "model", 1000) {
			t.Fatalf("enqueue %d failed", i)
		}
	}
	q.Stop()
	if got := updater.Count(); got != 4 {
		t.Fatalf("processed %d updates, want 4", got)
	}
}

func TestContextLimitUpdateQueueRejectsAfterStop(t *testing.T) {
	updater := &recordingContextLimitUpdater{}
	q := NewContextLimitUpdateQueue(updater, 1)
	q.Stop()
	if q.Enqueue(7, 1, "model", 1000) {
		t.Fatal("enqueue succeeded after Stop")
	}
}

func TestContextLimitUpdateQueueConcurrentStopAndEnqueue(t *testing.T) {
	updater := &recordingContextLimitUpdater{}
	q := NewContextLimitUpdateQueue(updater, 32)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				q.Enqueue(7, i, "model", 1000)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond)
		q.Stop()
	}()
	wg.Wait()
	q.Stop()
}
