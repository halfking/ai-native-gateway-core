package dispatch

import "sync/atomic"

// totalExecutionQueue is the FIFO execution queue before model routing. It is
// intentionally separate from LifecycleRegistry, which only projects request
// state and must never define execution capacity.
type totalExecutionQueue struct {
	ch       chan *QueuedRequest
	capacity int64
	queued   atomic.Int64
}

func newTotalExecutionQueue(capacity int) *totalExecutionQueue {
	if capacity <= 0 {
		capacity = DefaultRegistryCapacity
	}
	return &totalExecutionQueue{ch: make(chan *QueuedRequest, capacity), capacity: int64(capacity)}
}

func (q *totalExecutionQueue) tryEnqueue(qr *QueuedRequest) bool {
	if q == nil || qr == nil {
		return false
	}
	for {
		used := q.queued.Load()
		if used >= q.capacity {
			return false
		}
		if q.queued.CompareAndSwap(used, used+1) {
			break
		}
	}
	select {
	case q.ch <- qr:
		return true
	default:
		q.queued.Add(-1)
		return false
	}
}

func (q *totalExecutionQueue) depth() int {
	if q == nil {
		return 0
	}
	return int(q.queued.Load())
}

func (q *totalExecutionQueue) release(qr *QueuedRequest) {
	if q != nil && qr != nil && qr.totalQueueDone.CompareAndSwap(false, true) {
		q.queued.Add(-1)
	}
}
