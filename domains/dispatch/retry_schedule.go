package dispatch

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

// Timed retry scheduling (会话优化 v4 R1.1 / T3-8).
//
// A RetryScheduler parks failed requests until their retry_at, then hands
// them back to the pipeline for re-execution. Registry mapping (spec §6): a
// retrying request carrying retry_at goes back to pending (RetryAt recorded);
// when the picker fires, the request re-enters in-flight.
//
// The pipeline holds an OPTIONAL instance (nil ⇒ disabled): without a
// scheduler the failover ladder keeps its legacy behavior of re-enqueueing
// immediately, so wiring this in is a strictly local behavior change.

// RetryScheduler defers re-execution of failed requests until retry_at.
// Implementations must be safe for concurrent use; Schedule must be
// non-blocking (admission into a bounded structure only).
type RetryScheduler interface {
	// Schedule parks qr until retryAt. Returns false when the scheduler is
	// closed (the caller should fall back to the immediate path or complete).
	Schedule(qr *QueuedRequest, retryAt time.Time) bool
	// Len reports parked requests (diagnostics/tests).
	Len() int
	// Close stops the picker goroutine. Scheduled items are dropped (the
	// scheduler is a best-effort timing layer, never the owner of record).
	Close()
}

// RetryClock abstracts time for tests (fake-clock injection).
type RetryClock interface {
	Now() time.Time
}

// RetrySleeper parks the picker for d. It may return early when the wake
// channel fires (a newly scheduled item became the earliest one) or ctx is
// canceled (scheduler shutdown); the loop simply recomputes the next wait.
// Injectable so tests advance a fake clock instead of sleeping.
type RetrySleeper func(ctx context.Context, d time.Duration, wake <-chan struct{})

// RealRetryClock is the wall-clock default.
type RealRetryClock struct{}

// Now returns time.Now.
func (RealRetryClock) Now() time.Time { return time.Now() }

// RealRetrySleeper sleeps until d elapses, wake fires, or ctx is canceled.
func RealRetrySleeper(ctx context.Context, d time.Duration, wake <-chan struct{}) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-timer.C:
	}
}

// NextRetryDelay computes the backoff for the retrySeq-th (1-based) retry
// following the R2.4 ladder: 2s base, exponential ×2, clamped at 120s. An
// upstream Retry-After hint (retryAfter > 0) takes priority, still clamped
// to the same [2s, 120s] window (survival parity: the 2s→120s hard clamp of
// survival_coordinator.go).
//
// Jitter is intentionally NOT applied here: the v4 spec assigns ±20% jitter
// to the survival coordinator (T3), and keeping dispatch deterministic keeps
// the retry timeline testable.
func NextRetryDelay(retrySeq int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return clampDuration(retryAfter, retryBaseDelay, retryMaxDelay)
	}
	if retrySeq < 1 {
		retrySeq = 1
	}
	delay := retryBaseDelay
	for i := 1; i < retrySeq; i++ {
		delay *= 2
		if delay >= retryMaxDelay {
			return retryMaxDelay
		}
	}
	return clampDuration(delay, retryBaseDelay, retryMaxDelay)
}

func clampDuration(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// retryHeapItem pairs a request with its due time.
type retryHeapItem struct {
	qr       *QueuedRequest
	retryAt  time.Time
	sequence int64 // tie-breaker keeps FIFO among equal retry_at values
}

// retryHeap is a min-heap ordered by (retryAt, sequence).
type retryHeap []retryHeapItem

func (h retryHeap) Len() int { return len(h) }
func (h retryHeap) Less(i, j int) bool {
	if !h[i].retryAt.Equal(h[j].retryAt) {
		return h[i].retryAt.Before(h[j].retryAt)
	}
	return h[i].sequence < h[j].sequence
}
func (h retryHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *retryHeap) Push(x any)   { *h = append(*h, x.(retryHeapItem)) }
func (h *retryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// HeapRetryScheduler is the default RetryScheduler: a min-heap of due times
// plus one background picker goroutine. The picker is the only goroutine
// that pops from the heap; Schedule only inserts under a mutex and nudges
// the picker through a notify channel.
type HeapRetryScheduler struct {
	pick    func(qr *QueuedRequest, retryAt time.Time)
	clock   RetryClock
	sleeper RetrySleeper

	mu      sync.Mutex
	items   retryHeap
	nextSeq int64
	closed  bool

	notify     chan struct{}
	loopCancel context.CancelFunc
	closeOnce  sync.Once
}

// NewHeapRetryScheduler builds and starts a scheduler. pick runs on the
// picker goroutine at (or just after) each item's retry_at; it must not
// block for long. Nil clock/sleeper default to the real ones.
func NewHeapRetryScheduler(pick func(qr *QueuedRequest, retryAt time.Time), clock RetryClock, sleeper RetrySleeper) *HeapRetryScheduler {
	if clock == nil {
		clock = RealRetryClock{}
	}
	if sleeper == nil {
		sleeper = RealRetrySleeper
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	s := &HeapRetryScheduler{
		pick:       pick,
		clock:      clock,
		sleeper:    sleeper,
		notify:     make(chan struct{}, 1),
		loopCancel: cancel,
	}
	go s.run(loopCtx)
	return s
}

// Schedule parks qr until retryAt (non-blocking).
func (s *HeapRetryScheduler) Schedule(qr *QueuedRequest, retryAt time.Time) bool {
	if s == nil || qr == nil {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.nextSeq++
	heap.Push(&s.items, retryHeapItem{qr: qr, retryAt: retryAt, sequence: s.nextSeq})
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default: // a nudge is already pending
	}
	return true
}

// Len reports the number of parked requests.
func (s *HeapRetryScheduler) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items.Len()
}

// Close stops the picker goroutine exactly once.
func (s *HeapRetryScheduler) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.loopCancel()
	})
}

// run is the picker loop: wait until the earliest retry_at, fire due items.
func (s *HeapRetryScheduler) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		wait, ok := s.nextWait(s.clock.Now())
		if !ok {
			select {
			case <-s.notify:
			case <-ctx.Done():
				return
			}
			continue
		}
		if wait > 0 {
			s.sleeper(ctx, wait, s.notify)
		}
		s.fireDue(s.clock.Now())
	}
}

// nextWait returns how long until the earliest item (0 when already due).
func (s *HeapRetryScheduler) nextWait(now time.Time) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items.Len() == 0 {
		return 0, false
	}
	if s.items[0].retryAt.After(now) {
		return s.items[0].retryAt.Sub(now), true
	}
	return 0, true
}

// fireDue pops and fires every item whose retry_at has passed. It stops as
// soon as Close is observed so a shutdown never re-injects work.
func (s *HeapRetryScheduler) fireDue(now time.Time) {
	for {
		s.mu.Lock()
		if s.closed || s.items.Len() == 0 || s.items[0].retryAt.After(now) {
			s.mu.Unlock()
			return
		}
		item := heap.Pop(&s.items).(retryHeapItem)
		s.mu.Unlock()
		if s.pick != nil {
			func() {
				defer func() { _ = recover() }()
				s.pick(item.qr, item.retryAt)
			}()
		}
	}
}
