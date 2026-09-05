package dispatch

import "testing"

func TestTotalExecutionQueuePreservesCapacityAcrossDequeue(t *testing.T) {
	q := newTotalExecutionQueue(2)
	first := NewQueuedRequest("total-1", "tenant", "model", nil, nil)
	second := NewQueuedRequest("total-2", "tenant", "model", nil, nil)
	third := NewQueuedRequest("total-3", "tenant", "model", nil, nil)
	if !q.tryEnqueue(first) || !q.tryEnqueue(second) {
		t.Fatal("expected first two requests to enter total queue")
	}
	<-q.ch // The drainer owns this request but it still consumes capacity.
	if q.tryEnqueue(third) {
		t.Fatal("dequeued request must retain total capacity until handoff completes")
	}
	q.release(first)
	if !q.tryEnqueue(third) {
		t.Fatal("released total capacity must admit the next request")
	}
}

func TestTotalExecutionQueueReleaseIsIdempotent(t *testing.T) {
	q := newTotalExecutionQueue(1)
	request := NewQueuedRequest("total", "tenant", "model", nil, nil)
	if !q.tryEnqueue(request) {
		t.Fatal("expected request admission")
	}
	q.release(request)
	q.release(request)
	if q.depth() != 0 {
		t.Fatalf("depth = %d, want 0 after duplicate release", q.depth())
	}
}
