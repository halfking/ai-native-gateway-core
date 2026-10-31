package dispatch

import (
	"container/heap"
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
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
// Jitter is intentionally NOT applied here: NextRetryDelay stays a pure
// ladder (the timeline contract TestNextRetryDelayLadder pins); callers
// spread the result with ApplyRetryJitter at the scheduling site.
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

// RetryJitterRand is the ±20% retry-backoff jitter seam (P0-C, audit
// 2026-10-28 §4): nil → math/rand.Float64. Overridable in tests so jitter
// boundaries are pinned deterministically. Mirrors the survival
// coordinator's JitterRand field (survival_coordinator.go:165).
var RetryJitterRand func() float64

// retryJitterEnabled gates ApplyRetryJitter from hotconfig
// (llmgw_dispatch_retry_jitter_enabled, default ON — anti retry-storm is
// the P0-C goal; flipping it off restores the exact pre-P0-C timeline).
var retryJitterEnabled atomic.Bool

// SetRetryJitterEnabled flips the dispatch-layer ±20% backoff jitter
// (hotconfig reload path). The survival layer keeps its own always-on
// jitter; this only governs the dispatch retry ladder.
func SetRetryJitterEnabled(on bool) { retryJitterEnabled.Store(on) }

// RetryJitterEnabled reports whether dispatch-layer jitter is active.
func RetryJitterEnabled() bool { return retryJitterEnabled.Load() }

// ApplyRetryJitter spreads one backoff step by ±20%, the SAME formula as
// the survival layer's applyBackoffJitter:
//
//	jitter = d × 0.2 × (2·r − 1)   // r ∈ [0,1) → −20%..+20%
//	final  = d + jitter            // never < 0
//
// Single implementation for both layers: streaming.applyBackoffJitter
// delegates here (同一把尺子一份实现). Honors the llmgw_dispatch_retry_
// jitter_enabled hotconfig gate; when disabled (or d ≤ 0) the input passes
// through unchanged.
func ApplyRetryJitter(d time.Duration, randFloat func() float64) time.Duration {
	if !retryJitterEnabled.Load() {
		return d
	}
	return ApplyRetryJitterAlways(d, randFloat)
}

// ApplyRetryJitterAlways is the ungated core of ApplyRetryJitter: the
// survival layer delegates here unconditionally (its jitter is not subject
// to the dispatch hotconfig gate).
func ApplyRetryJitterAlways(d time.Duration, randFloat func() float64) time.Duration {
	if d <= 0 {
		return d
	}
	if randFloat == nil {
		randFloat = RetryJitterRand
	}
	if randFloat == nil {
		randFloat = rand.Float64
	}
	factor := 1 + 0.2*(2*randFloat()-1) // ∈ [0.8, 1.2)
	jittered := time.Duration(float64(d) * factor)
	if jittered < 0 {
		return 0
	}
	return jittered
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
	onClose func(qr *QueuedRequest)
	clock   RetryClock
	sleeper RetrySleeper

	// maxItems (audit 2026-09-14 R28 #15b) bounds the parked heap. 0 (or
	// negative) keeps the legacy unbounded behavior. When the heap is at
	// capacity Schedule returns false and the caller falls back to its
	// existing immediate path (tryEnqueueCred / model-change escalation) —
	// the scheduler is best-effort and never the owner of record, so refusing
	// admission is always safe.
	maxItems int

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
// block for long. Nil clock/sleeper default to the real ones. Parked items
// are dropped on Close (legacy behaviour). maxItems bounds the heap
// (Schedule returns false at capacity); pass 0 for the legacy unbounded heap.
func NewHeapRetryScheduler(pick func(qr *QueuedRequest, retryAt time.Time), clock RetryClock, sleeper RetrySleeper, maxItems int) *HeapRetryScheduler {
	return NewHeapRetrySchedulerWithCloseHandler(pick, nil, clock, sleeper, maxItems)
}

// NewHeapRetrySchedulerWithCloseHandler additionally invokes onClose for
// every still-parked request when Close runs, letting the pipeline complete
// those requests (e.g. with ErrShutdown) so their Submit callers do not
// block forever during graceful shutdown. Nil onClose keeps the drop
// behaviour. maxItems bounds the parked heap (0 = unbounded, audit
// 2026-09-14 R28 #15b).
func NewHeapRetrySchedulerWithCloseHandler(
	pick func(qr *QueuedRequest, retryAt time.Time),
	onClose func(qr *QueuedRequest),
	clock RetryClock,
	sleeper RetrySleeper,
	maxItems int,
) *HeapRetryScheduler {
	if clock == nil {
		clock = RealRetryClock{}
	}
	if sleeper == nil {
		sleeper = RealRetrySleeper
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	s := &HeapRetryScheduler{
		pick:       pick,
		onClose:    onClose,
		clock:      clock,
		sleeper:    sleeper,
		maxItems:   maxItems,
		notify:     make(chan struct{}, 1),
		loopCancel: cancel,
	}
	go s.run(loopCtx)
	return s
}

// Schedule parks qr until retryAt (non-blocking). Returns false when the
// scheduler is closed OR the heap is at its maxItems bound (audit 2026-09-14
// R28 #15b); both cases route the caller to its pre-existing fallback path.
func (s *HeapRetryScheduler) Schedule(qr *QueuedRequest, retryAt time.Time) bool {
	if s == nil || qr == nil {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if s.maxItems > 0 && s.items.Len() >= s.maxItems {
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

// Close stops the picker goroutine exactly once. When a close handler was
// supplied, every still-parked request is handed to it (outside the lock)
// so owners can complete them; without a handler parked items are dropped.
func (s *HeapRetryScheduler) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		pending := make([]*QueuedRequest, 0, s.items.Len())
		for s.items.Len() > 0 {
			item := heap.Pop(&s.items).(retryHeapItem)
			pending = append(pending, item.qr)
		}
		onClose := s.onClose
		s.mu.Unlock()
		s.loopCancel()
		if onClose == nil {
			return
		}
		for _, qr := range pending {
			func() {
				defer func() { _ = recover() }()
				onClose(qr)
			}()
		}
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
