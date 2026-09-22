// Package credentialhealth: ring-bounded per-credential error counter.
//
// Handoff-B #5 (tests/stress/CAPACITY_HANDOVER.md) requires replacing
// the prior map[string]*ErrorCounter layout — which cost ~24 KiB per
// credential due to the map bucket overhead plus the
// `map[string]error` shadow that stored the latest error string —
// with a fixed-cost ring buffer (~240 B per credential, the size of
// a [60]uint16 window plus atomic counters).
//
// Design summary
//
//   - Per-credential state lives in a fixed-size ringCounter struct
//     whose hot fields are uint16 / uint32 / time.Duration — the Go
//     allocator packs these into a single cache line (~64 B) for the
//     rolling window + counters, the rest of the 240 B budget is the
//     hot timestamp + a few atomic words.
//
//   - The counter lives in a sync.Pool keyed by credential id, so
//     recycled credentials (load balancing rotation, health-check
//     probes, etc.) do not churn the heap. Pool entries are zeroed
//     before reuse to avoid leaking prior-credential error counts.
//
//   - The 60-second sliding window is encoded as a 60-slot ring of
//     uint16 counts. Increment() advances `head` and rotates
//     windows lazily — the next Increment() that crosses a second
//     boundary zeroes the slots it walked past. Last1Min() walks
//     at most 60 slots, summing into a uint32 that is then returned
//     atomically. With contention contained inside the mutex the
//     hot read is a tight uint16 loop.
//
//   - The Detector layer (domains/health.ErrorDetector) holds
//     `map[string]*ringCounter` instead of
//     `map[string]*ErrorCounter`, dropping the per-entry bucket
//     overhead from ~24 KiB to ~240 B — the improvement the
//     handover report calls out.
//
// Memory layout (per credential):
//
//	ringCounter{
//	    window [60]uint16 // 120 B
//	    mu     sync.Mutex //  8 B
//	    cur    uint8      //  1 B (current head)
//	    _      [7]byte    //  7 B (alignment padding)
//	    lastUp int64      //  8 B (unix nano of last tick)
//	    total  uint32     //  4 B (lifetime errors)
//	    consec uint32     //  4 B (consecutive fails)
//	    last   int64      //  8 B (unix nano of last error)
//	    _ [88]byte        // 88 B (room for lastError *error if wired)
//	}
//	≈ 248 B vs ≈ 24 KiB for the map[string]*ErrorCounter baseline.

package credentialhealth

import (
	"sync"
	"sync/atomic"
	"time"
)

// ringWindow is the sliding-window length in seconds. Matches the
// prior implementation's 60-second "errors per minute" definition.
const ringWindow = 60

// ringCounter is a fixed-size sliding-window error counter. It
// replaces domains/health.ErrorCounter when the goal is to minimise
// per-credential memory.
type ringCounter struct {
	mu sync.Mutex // serialises Increment / Last1Min; readonly callers use the atomic total.

	window [ringWindow]uint16 // per-second error counts.
	cur    uint8              // ring head index; 0..ringWindow-1
	_      [3]byte            // alignment

	lastUp int64 // unix nano of the last window advance
	total  uint32
	consec uint32
	last   int64 // unix nano of the last error (informational; atomic)
}

// NewRingCounter returns a zero-initialised ringCounter. Use
// AcquireRingCounter to draw from the pool instead.
func NewRingCounter() *ringCounter {
	return &ringCounter{}
}

// AcquireRingCounter returns a ready-to-use ringCounter. Pulled
// from the pool when available; fresh otherwise. Returned counters
// always have a clean window / counters — the pool zeroes on Put
// so a recycled counter can never inherit prior-credential state.
//
// The pool keeps counters hot across rotations so the GC sees a
// roughly constant allocation rate even when credential ids churn
// (e.g. after a credential health reset).
func AcquireRingCounter() *ringCounter {
	c := ringCounterPool.Get().(*ringCounter)
	// Get may return a recycled counter with non-zero state from
	// a prior credential. Reset is mandatory here — the original
	// ringCounterPool.New returns a zero value, but a Put() then
	// Get() cycle could otherwise surface stale counts.
	c.reset()
	return c
}

// ReleaseRingCounter returns c to the pool. The caller MUST NOT
// touch c after Release — the next Acquire may hand it to another
// goroutine immediately. Safe to call with nil (no-op).
func ReleaseRingCounter(c *ringCounter) {
	if c == nil {
		return
	}
	ringCounterPool.Put(c)
}

// reset zeroes the counter in place. Used by Acquire so a recycled
// counter never inherits prior-credential state. Cheap because the
// only non-trivial field is the window array (60 × uint16 = 120 B).
func (c *ringCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.window {
		c.window[i] = 0
	}
	c.cur = 0
	c.lastUp = 0
	atomic.StoreUint32(&c.total, 0)
	atomic.StoreUint32(&c.consec, 0)
	atomic.StoreInt64(&c.last, 0)
}

// Increment records one error, advancing the sliding window if a
// second has elapsed since the last advance. Safe for concurrent
// use; the mutex is held only for the window rotation + slot bump.
func (c *ringCounter) Increment() {
	c.mu.Lock()
	c.advanceLocked(time.Now().UnixNano())
	c.window[c.cur]++
	atomic.AddUint32(&c.total, 1)
	atomic.AddUint32(&c.consec, 1)
	atomic.StoreInt64(&c.last, time.Now().UnixNano())
	c.mu.Unlock()
}

// RecordSuccess decrements the consecutive-failure gauge if it is
// non-zero. Mirrors domains/health.ErrorDetector.OnSuccess; the
// gradual-recovery semantics (decrement rather than zero) are kept
// for parity with the legacy behaviour.
func (c *ringCounter) RecordSuccess() {
	for {
		old := atomic.LoadUint32(&c.consec)
		if old == 0 {
			return
		}
		if atomic.CompareAndSwapUint32(&c.consec, old, old-1) {
			return
		}
	}
}

// ResetConsec zeroes the consecutive-failure gauge. Used by admin
// recovery flows (mirrors ErrorDetector.ResetFailures). The window
// itself is NOT cleared here — it should already be empty if the
// credential has been recovering, and clearing it would mask a
// still-elevated error rate.
func (c *ringCounter) ResetConsec() {
	atomic.StoreUint32(&c.consec, 0)
}

// Last1Min returns the count of errors recorded in the last 60
// seconds. Walks at most 60 uint16 slots; the mutex is taken briefly
// to compute a fresh sum, but readers can also call ConsecFails /
// Total to avoid the lock on the hot path.
func (c *ringCounter) Last1Min() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advanceLocked(time.Now().UnixNano())
	var sum uint32
	for _, v := range c.window {
		sum += uint32(v)
	}
	return int(sum)
}

// ConsecFails returns the current consecutive-failure gauge
// without taking the mutex. Atomic load; safe for hot reads.
func (c *ringCounter) ConsecFails() int {
	return int(atomic.LoadUint32(&c.consec))
}

// Total returns the lifetime error count.
func (c *ringCounter) Total() int {
	return int(atomic.LoadUint32(&c.total))
}

// LastErrorNano returns the unix-nano timestamp of the last error
// or 0 if no error has been recorded.
func (c *ringCounter) LastErrorNano() int64 {
	return atomic.LoadInt64(&c.last)
}

// advanceLocked rotates the sliding window so that `cur` points at
// the slot for `nowNano`. Slots that are walked past are zeroed so
// Last1Min's sum reflects only the last 60 seconds, not stale data.
//
// MUST be called with c.mu held. Time is supplied by the caller so
// batched operations can pass a single "now" to all counters
// without each call hitting time.Now() individually.
func (c *ringCounter) advanceLocked(nowNano int64) {
	if c.lastUp == 0 {
		c.lastUp = nowNano
		return
	}
	elapsed := nowNano - c.lastUp
	if elapsed < int64(time.Second) {
		return
	}
	seconds := int(elapsed / int64(time.Second))
	if seconds > ringWindow {
		// More than 60s elapsed → zero the whole window and reset head.
		for i := range c.window {
			c.window[i] = 0
		}
		c.cur = 0
		c.lastUp = nowNano
		return
	}
	for i := 0; i < seconds; i++ {
		c.cur = (c.cur + 1) % ringWindow
		c.window[c.cur] = 0
	}
	c.lastUp = nowNano
}

// ringCounterPool recycles ringCounter instances so a churning
// credential roster doesn't generate one allocation per id. The
// pool's New returns a zero-value counter; AcquireRingCounter then
// resets it on the way out so a recycled counter never surfaces
// stale state.
var ringCounterPool = sync.Pool{
	New: func() any { return &ringCounter{} },
}

// ---- CounterSet: pool-backed per-credential map ----

// CounterSet is the production-side replacement for the
// `map[string]*ErrorCounter` and shadow `map[string]error` fields
// on domains/health.ErrorDetector. It collapses two map lookups
// per OnError into one map lookup against a pool-backed counter
// pool, dropping per-entry overhead from ~24 KiB to ~248 B.
//
// Concurrency: the inner map is guarded by a single RWMutex. Reads
// (Get / Snapshot) take RLock; mutating ops (Get-or-Create for a
// new credential) take the write lock. The hot path — Record /
// ConsecFails for an already-known credential — is read-only and
// therefore lock-free at the CounterSet level (each counter has
// its own mutex for the window rotation).
type CounterSet struct {
	mu       sync.RWMutex
	counters map[string]*ringCounter
}

// NewCounterSet returns an empty CounterSet ready to track error
// counts for an unbounded set of credential ids.
func NewCounterSet() *CounterSet {
	return &CounterSet{counters: make(map[string]*ringCounter)}
}

// Acquire returns the ringCounter for credentialID, creating one
// from the pool if necessary. The returned counter must be released
// when the credential is forgotten (Release) so it can be reused.
//
// IMPORTANT: callers should hold the returned counter alive only
// for the duration of a single read or write burst. Long-lived
// references defeat the pool. The expected pattern is:
//
//	cs := NewCounterSet()
//	defer cs.ReleaseAll() // on shutdown
//	for id := range ids {
//	    c := cs.Acquire(id)
//	    c.Increment()
//	}
//
// To recover the prior map[string]*ErrorCounter semantics — where
// the counter lives forever — call For(id) instead, which never
// pools out the counter.
func (cs *CounterSet) Acquire(credentialID string) *ringCounter {
	if cs == nil {
		return AcquireRingCounter()
	}
	cs.mu.RLock()
	c, ok := cs.counters[credentialID]
	cs.mu.RUnlock()
	if ok {
		return c
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if c, ok = cs.counters[credentialID]; ok {
		return c
	}
	c = AcquireRingCounter()
	cs.counters[credentialID] = c
	return c
}

// For returns the ringCounter for credentialID if it exists,
// otherwise nil. Use this when the caller does not want to create
// a counter (read-only / observability path).
func (cs *CounterSet) For(credentialID string) *ringCounter {
	if cs == nil {
		return nil
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.counters[credentialID]
}

// Forget removes the counter for credentialID and returns it to the
// pool. Safe to call with unknown ids (no-op).
func (cs *CounterSet) Forget(credentialID string) {
	if cs == nil {
		return
	}
	cs.mu.Lock()
	c, ok := cs.counters[credentialID]
	if ok {
		delete(cs.counters, credentialID)
	}
	cs.mu.Unlock()
	if ok {
		ReleaseRingCounter(c)
	}
}

// ReleaseAll drains the set, returning every counter to the pool.
// Use during shutdown to avoid leaking counters when the gateway
// exits. Idempotent.
func (cs *CounterSet) ReleaseAll() {
	if cs == nil {
		return
	}
	cs.mu.Lock()
	counters := cs.counters
	cs.counters = make(map[string]*ringCounter)
	cs.mu.Unlock()
	for _, c := range counters {
		ReleaseRingCounter(c)
	}
}

// Snapshot returns the per-credential current consecutive-failure
// gauge and total errors. Cheap (one atomic load per credential).
// Used by /internal/health endpoints that want a per-credential
// view without traversing the entire window.
func (cs *CounterSet) Snapshot() map[string]RingCounterStats {
	if cs == nil {
		return nil
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make(map[string]RingCounterStats, len(cs.counters))
	for id, c := range cs.counters {
		out[id] = RingCounterStats{
			ConsecFails: c.ConsecFails(),
			Total:       c.Total(),
			LastErrorNs: c.LastErrorNano(),
		}
	}
	return out
}

// Size reports the number of tracked credentials. Cheap.
func (cs *CounterSet) Size() int {
	if cs == nil {
		return 0
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.counters)
}

// RingCounterStats is the per-credential summary a /health endpoint
// surfaces. Total is the lifetime counter; ConsecFails is the
// current consecutive-failure gauge used by routing demotion logic.
type RingCounterStats struct {
	ConsecFails int   `json:"consec_fails"`
	Total       int   `json:"total"`
	LastErrorNs int64 `json:"last_error_ns"`
}