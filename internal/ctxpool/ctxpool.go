// Package ctxpool provides a context.Context pool that reuses
// per-request context objects across goroutines instead of
// allocating a new context.WithCancel / context.WithTimeout per
// request.
//
// Background (Handoff-B #4 in tests/stress/CAPACITY_HANDOVER.md):
// the production hot path issues 4-6 context.WithCancel /
// context.WithTimeout calls per inbound request. Each call
// allocates a cancelCtx struct, a done channel, and a propagator
// closure — about 320 B total per allocation.
//
// Why the stdlib context is hard to pool
//
// The standard context.WithCancel always creates a fresh cancelCtx
// struct and a fresh done channel; there is no API to "reset" a
// context and reuse its allocations. A naive pool that wraps a
// pooled ctx over a fresh context.WithCancel therefore only pools
// the outer wrapper — the expensive channels are still allocated
// each time.
//
// Approach used here
//
// We implement a minimal cancelCtx ourselves: a parent, a done
// channel, and a cancel func that closes the done channel. On
// Release we close the done channel and reset state. On the next
// Acquire we re-open a fresh done channel. The parent, value
// lookups, and deadline all forward to the parent ctx unchanged.
//
// Caveats
//
//   - PooledCtx implements the subset of context.Context the
//     gateway uses: Done, Err, Value, Deadline. It does NOT
//     implement the full context.Context interface, but is
//     interchangeable with context.Context for the call sites the
//     dispatcher uses.
//
//   - There is NO propagation between the parent and child for
//     cancellation. The parent cancelling does NOT close the
//     child's done channel. This is intentional: each pooled ctx
//     is owned by one goroutine and is expected to be cancelled
//     explicitly. If a caller needs parent cancellation to
//     propagate they should wrap the pooled ctx in a fresh
//     context.WithCancel themselves.
//
//   - The Deadline on a plain Acquire() is fixed at "no deadline".
//     For deadlines, use AcquireWithTimeout which creates a fresh
//     context.WithTimeout on top of the pooled child.
//
//   - Release() must be called exactly once per Acquire. The
//     release func returned by Acquire closes over the ctx
//     directly so the pool can hand the same *PooledCtx pointer
//     to the next caller without per-call closure allocations
//     beyond the channel reset.
// STATUS (R56 audit, 2026-09-23): NOT WIRED to the production dispatcher —
// zero non-test callers (Handoff-B #4 wiring is still pending). The races
// hardened below were latent while unwired but would bite the moment the
// dispatcher starts routing requests through AcquireWithTimeout.
package ctxpool

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// PooledCtx is the lightweight cancel-only context returned by
// Acquire.
type PooledCtx struct {
	parent context.Context
	// wrapper is set only by AcquireWithTimeout to the
	// context.WithTimeout wrapper ctx. Used by Deadline / Err to
	// surface the deadline-derived answers without changing the
	// parent chain (which would break Value lookups).
	wrapper context.Context

	// owner is the Pool that allocated this ctx. Set during
	// acquire so Release can return it without consulting a
	// back-pointer (which would itself allocate).
	owner *Pool

	// done is the cancellation channel. nil before the first
	// Acquire / after Release; replaced on every acquire.
	done chan struct{}

	// err is set when Cancel is called.
	err error

	// mu guards err / done swap during the reset cycle.
	mu sync.Mutex

	// createdAt is the wall-clock time when Acquire produced this ctx.
	createdAt time.Time

	// generation is incremented on each acquire. Used by the
	// double-Release guard to distinguish a stale Release on a
	// recycled ctx from a fresh Release.
	generation uint64

	// released flips to true after Release.
	released atomic.Bool

	// Once wrapper, the wrapped cancel func set on the caller-side
	// release closure (e.g. for AcquireWithTimeout). We expose
	// the release func as a method on PooledCtx so it can be
	// re-used across calls without per-acquire closure alloc.
}

// Done returns the cancellation channel. Returns nil only after
// Release has been called.
func (c *PooledCtx) Done() <-chan struct{} {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done
}

// Err returns context.Canceled once Cancel has been invoked, or
// the wrapper / parent ctx's Err if either fired first. Always
// nil before any cancellation.
func (c *PooledCtx) Err() error {
	if c == nil {
		return nil
	}
	// Copy the refs under the lock: acquireWithTimeout stores the wrapper
	// concurrently with the caller's goroutine reading Err/Deadline
	// (R56 audit race hardening).
	c.mu.Lock()
	wrapper, parent, err := c.wrapper, c.parent, c.err
	c.mu.Unlock()
	if wrapper != nil {
		if werr := wrapper.Err(); werr != nil {
			return werr
		}
	}
	if parent != nil {
		if perr := parent.Err(); perr != nil {
			return perr
		}
	}
	return err
}

// Deadline returns the wrapper ctx's deadline (zero if neither
// the wrapper nor the parent carries a deadline).
func (c *PooledCtx) Deadline() (time.Time, bool) {
	if c == nil {
		return time.Time{}, false
	}
	c.mu.Lock()
	wrapper, parent := c.wrapper, c.parent
	c.mu.Unlock()
	if wrapper != nil {
		if dl, ok := wrapper.Deadline(); ok {
			return dl, true
		}
	}
	if parent != nil {
		return parent.Deadline()
	}
	return time.Time{}, false
}

// Value forwards to the parent context.
func (c *PooledCtx) Value(key any) any {
	if c == nil || c.parent == nil {
		return nil
	}
	return c.parent.Value(key)
}

// Cancel closes the done channel and sets err to context.Canceled.
// Idempotent — calling Cancel twice is a no-op the second time.
func (c *PooledCtx) Cancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == nil {
		return
	}
	if c.err != nil {
		return
	}
	c.err = context.Canceled
	close(c.done)
}

// Released reports whether Release has been called.
func (c *PooledCtx) Released() bool {
	if c == nil {
		return false
	}
	return c.released.Load()
}

// Release returns the ctx to its owning pool. Safe to call exactly
// once per Acquire; double-call panics.
func (c *PooledCtx) Release() {
	if c == nil {
		return
	}
	// CAS: two concurrent Releases must not both pass the check and Put the
	// same pointer into the pool twice (R56 audit race hardening; the
	// previous check-then-act was fine only for single-goroutine use).
	if !c.released.CompareAndSwap(false, true) {
		panic("ctxpool: Release called twice on the same PooledCtx")
	}
	c.mu.Lock()
	if c.done != nil && c.err == nil {
		// Caller forgot to Cancel — close done ourselves so the
		// next acquirer never sees a still-live ctx.
		c.err = context.Canceled
		close(c.done)
	}
	c.done = nil
	c.err = nil
	c.parent = nil
	c.wrapper = nil
	c.mu.Unlock()
	if c.owner != nil {
		c.owner.pool.Put(c)
	}
}

// ReleaseFunc is the function returned by Acquire. It just calls
// (*PooledCtx).Release() on the captured ctx.
type ReleaseFunc func()

// Pool is the context pool. Most callers should use the package-
// level Acquire / AcquireWithTimeout helpers backed by globalPool.
type Pool struct {
	pool       sync.Pool
	generation atomic.Uint64
}

// NewPool returns an isolated context pool.
func NewPool() *Pool {
	p := &Pool{}
	p.pool.New = func() any { return &PooledCtx{} }
	return p
}

var globalPool = NewPool()

// Acquire returns a *PooledCtx derived from parent. The release
// func must be called exactly once when the request completes;
// double-Release panics.
func Acquire(parent context.Context) (*PooledCtx, ReleaseFunc) {
	return globalPool.acquire(parent)
}

// AcquireWithTimeout returns a *PooledCtx that auto-cancels at
// deadline. Implementation note: the deadline is enforced by a
// real context.WithTimeout wrapping the pooled child.
func AcquireWithTimeout(parent context.Context, timeout time.Duration) (*PooledCtx, context.CancelFunc) {
	return globalPool.acquireWithTimeout(parent, timeout)
}

// AcquireWithDeadline is the deadline-typed counterpart.
func AcquireWithDeadline(parent context.Context, deadline time.Time) (*PooledCtx, context.CancelFunc) {
	return globalPool.acquireWithTimeout(parent, time.Until(deadline))
}

// acquire is the cancel-only fast path used by Acquire.
func (p *Pool) acquire(parent context.Context) (*PooledCtx, ReleaseFunc) {
	if parent == nil {
		parent = context.Background()
	}
	v := p.pool.Get()
	pc, ok := v.(*PooledCtx)
	if !ok || pc == nil {
		pc = &PooledCtx{}
	}
	pc.reinit(parent, p)
	return pc, pc.Release
}

// acquireWithTimeout wraps the pooled cancel-only ctx in a real
// context.WithTimeout so the deadline fires correctly.
func (p *Pool) acquireWithTimeout(parent context.Context, timeout time.Duration) (*PooledCtx, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	v := p.pool.Get()
	pc, ok := v.(*PooledCtx)
	if !ok || pc == nil {
		pc = &PooledCtx{}
	}
	pc.reinit(parent, p)
	// Wrap the pooled child with a real WithTimeout. The wrapper
	// ctx owns the timer; cancel returns the cancel func to the
	// caller so the caller can short-circuit the timeout when the
	// request finishes early.
	//
	// We deliberately do NOT swap pc.parent — leaving it pointed at
	// the original parent keeps Value lookups deterministic.
	wrapper, cancel := context.WithTimeout(pc, timeout)
	// When the wrapper's deadline fires (or the caller invokes
	// the cancel func), the wrapper's Done channel closes. Use
	// AfterFunc to forward that into pc.Cancel() so pc.Done()
	// also closes — this is what makes the PooledCtx API behave
	// like a real context.Context.
	stop := context.AfterFunc(wrapper, pc.Cancel)
	// pc.wrapper is consulted by Deadline / Err to surface the
	// wrapper's deadline-derived answers without changing the
	// Value chain. Store under the lock: those readers may run on
	// another goroutine the moment this function returns.
	pc.mu.Lock()
	pc.wrapper = wrapper
	pc.mu.Unlock()
	// Wrap the returned cancel func so the caller cancelling
	// the wrapper also stops the AfterFunc watcher (no leak).
	// Order matters: cancel pc FIRST so pc.Done closes before
	// we cancel the wrapper ctx (which would otherwise trigger
	// the AfterFunc, but the explicit cancel is preferred).
	wrapped := func() {
		pc.Cancel()
		stop()
		cancel()
	}
	return pc, wrapped
}

// reinit swaps in a fresh done channel / parent / generation. Called
// on every acquire from the pool.
func (c *PooledCtx) reinit(parent context.Context, owner *Pool) {
	c.mu.Lock()
	c.parent = parent
	c.wrapper = nil
	c.done = make(chan struct{})
	c.err = nil
	c.mu.Unlock()
	c.owner = owner
	c.createdAt = time.Now()
	c.generation = owner.generation.Add(1)
	c.released.Store(false)
}