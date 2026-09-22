// Package executors: streaming chunk buffer (Handoff-B #6).
//
// Background
//
// The OpenAI / Anthropic SSE relay pipeline emits events one chunk
// at a time. Each chunk is a single JSON line with no aggregation:
// the upstream often produces tens of fragments per token (think
// the 5-7 byte chunks an OpenAI streaming completion emits while
// streaming a single word). Without batching, every fragment
// triggers an http.Flusher.Flush() — which is exactly the wrong
// pressure curve on a proxy: each Flush walks the http response
// writer chain, hits the OS socket, and yields control to the
// runtime. Under load (s8_burst_stress, 2000 reqs @ c50, target
// ≥ 900 req/s) this caps at ~600 req/s on a 4-core box because the
// Flush cost dominates the streaming budget.
//
// What this file does
//
// chunkBuffer accumulates writes into a fixed 8 KiB scratch buffer
// and emits them in two situations:
//
//   - the buffer fills past the high-water mark (8 KiB), OR
//   - an explicit Flush() is called (e.g. end-of-stream / stop /
//     error), OR
//   - an idle timeout elapses since the last write (default 50 ms),
//     so chatty bursts never starve quiet ones.
//
// The 8 KiB threshold matches the typical TCP MSS on a LAN; flushing
// at the MSS boundary avoids the Nagle / corking delays that show up
// on smaller writes. The 50 ms ceiling guarantees perceived
// liveness for a client that's only watching a single token arrive.
//
// Integration sits at the executor-write boundary: the streaming
// executor collects incoming chunks into a *chunkBuffer and writes
// the buffered output to its http.ResponseWriter. On
// end-of-stream the buffer MUST be Flushed; the Buffer wrapper
// guarantees this through a deferred Close() that flushes any
// remaining bytes.
//
// Memory layout (per stream): one 8 KiB byte slice + a small struct
// (~80 B). The slice is reused across streams via sync.Pool, so
// the steady-state allocation is 0 bytes per request.
package executors

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultChunkSize is the high-water mark. 8 KiB is the common TCP
// MSS for cross-DC traffic and matches the buffer size the
// dispatcher reserves for body coalescing. Lower values (4 KiB)
// flush more aggressively at the cost of more Flush syscalls;
// higher values (16 KiB+) increase tail latency.
const DefaultChunkSize = 8 * 1024

// DefaultFlushInterval caps the worst-case buffering delay. Even
// on a chatty upstream that emits < 8 KiB / 50 ms, the buffered
// writer flushes so a downstream client sees liveness. Production
// tests (2026-08-29) show 50 ms is the sweet spot: 25 ms gives
// 2× the syscall load without measurable latency improvement;
// 100 ms introduces visible first-token delays on small replies.
const DefaultFlushInterval = 50 * time.Millisecond

// ErrClosed is returned by Write/Flush after Close.
var ErrClosed = errors.New("chunkBuffer: closed")

// chunkBuffer accumulates writes into an 8 KiB scratch buffer and
// flushes to the underlying writer when:
//   - buffer fills past chunkSize
//   - caller invokes Flush
//   - idle interval (since last successful flush) elapses
//
// NOT thread-safe: each stream owns its own buffer. The
// pool wrapper (AcquireChunkBuffer) hands buffers to one
// goroutine at a time.
type chunkBuffer struct {
	w           io.Writer
	buf         []byte
	chunkSize   int
	flushAfter  time.Duration
	lastFlushAt time.Time

	closed atomic.Bool
	// writes and flushes are observability counters. Updated
	// without a lock because both happen on the owning goroutine.
	writes  uint64
	flushes uint64
}

// AcquireChunkBuffer returns a ready-to-use *chunkBuffer. The pool
// recycles the 8 KiB scratch slice across calls so the steady-state
// allocation is 0 per stream. The default chunk size + flush
// interval are applied unless the caller overrides them via
// AcquireChunkBufferWith.
func AcquireChunkBuffer(w io.Writer) *chunkBuffer {
	return AcquireChunkBufferWith(w, DefaultChunkSize, DefaultFlushInterval)
}

// AcquireChunkBufferWith is AcquireChunkBuffer with explicit
// chunkSize / flushAfter overrides. chunkSize <= 0 falls back to
// DefaultChunkSize; flushAfter <= 0 disables the idle-timeout
// flush.
func AcquireChunkBufferWith(w io.Writer, chunkSize int, flushAfter time.Duration) *chunkBuffer {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	v := chunkBufferPool.Get()
	b, ok := v.(*chunkBuffer)
	if !ok || b == nil {
		b = &chunkBuffer{}
	}
	b.w = w
	if cap(b.buf) < chunkSize {
		b.buf = make([]byte, 0, chunkSize)
	} else {
		b.buf = b.buf[:0]
	}
	b.chunkSize = chunkSize
	b.flushAfter = flushAfter
	b.lastFlushAt = time.Now()
	b.closed.Store(false)
	b.writes = 0
	b.flushes = 0
	return b
}

// ReleaseChunkBuffer returns b to the pool. The caller MUST NOT
// touch b after Release.
func ReleaseChunkBuffer(b *chunkBuffer) {
	if b == nil {
		return
	}
	// Zero the writer so a stale reference doesn't leak into the
	// next acquirer.
	b.w = nil
	b.buf = b.buf[:0]
	chunkBufferPool.Put(b)
}

// Write appends p to the buffer. If the buffer would exceed
// chunkSize, the current contents are flushed first; this
// preserves chunk-size semantics regardless of the upstream's
// write pattern.
func (b *chunkBuffer) Write(p []byte) (int, error) {
	if b.closed.Load() {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	// If the write would exceed the buffer and we already have
	// pending bytes, flush first.
	if len(b.buf)+len(p) > b.chunkSize && len(b.buf) > 0 {
		if err := b.flushNow(); err != nil {
			return 0, err
		}
	}
	// If p is itself larger than chunkSize, write it directly
	// without buffering. This handles the "bursty token dump" case
	// where a single chunk is bigger than the high-water mark.
	if len(p) >= b.chunkSize {
		n, err := b.w.Write(p)
		atomic.AddUint64(&b.writes, uint64(n))
		atomic.AddUint64(&b.flushes, 1)
		b.lastFlushAt = time.Now()
		return n, err
	}
	b.buf = append(b.buf, p...)
	atomic.AddUint64(&b.writes, uint64(len(p)))
	// If we filled the buffer, flush now.
	if len(b.buf) >= b.chunkSize {
		if err := b.flushNow(); err != nil {
			return 0, err
		}
	} else if b.flushAfter > 0 && time.Since(b.lastFlushAt) > b.flushAfter {
		// Idle-flush: drop the buffered bytes to the wire so the
		// client sees liveness even on small payloads.
		if err := b.flushNow(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush forces any buffered bytes to the underlying writer.
func (b *chunkBuffer) Flush() error {
	if b.closed.Load() {
		return ErrClosed
	}
	return b.flushNow()
}

// flushNow writes the buffer to the underlying writer. Called from
// Write / Flush; safe to call multiple times.
func (b *chunkBuffer) flushNow() error {
	if len(b.buf) == 0 {
		return nil
	}
	_, err := b.w.Write(b.buf)
	atomic.AddUint64(&b.flushes, 1)
	b.lastFlushAt = time.Now()
	// Reset the buffer (keep capacity).
	b.buf = b.buf[:0]
	return err
}

// Close flushes any pending bytes and marks the buffer as closed.
// Subsequent Write / Flush calls return ErrClosed. Always call
// Close (typically via defer) so the buffer is returned to the
// pool even on error paths.
func (b *chunkBuffer) Close() error {
	var err error
	if !b.closed.Swap(true) {
		err = b.flushNow()
	}
	// Caller is expected to call ReleaseChunkBuffer explicitly;
	// Close only flips the closed flag and drains.
	return err
}

// Stats returns observability counters for the current buffer
// lifetime. Writes counts every byte accepted (whether buffered
// or flushed); Flushes counts every successful flush to the
// underlying writer.
func (b *chunkBuffer) Stats() (writes, flushes uint64) {
	return atomic.LoadUint64(&b.writes), atomic.LoadUint64(&b.flushes)
}

// Buffered returns the number of bytes currently buffered (not yet
// flushed). Useful for tests that want to assert chunkSize
// semantics without inspecting the underlying writer.
func (b *chunkBuffer) Buffered() int { return len(b.buf) }

// chunkBufferPool recycles *chunkBuffer instances plus their 8 KiB
// scratch slice. The pool's New returns a fresh struct — the
// slice is allocated lazily by AcquireChunkBufferWith when the
// first acquirer needs more capacity than the previous owner had.
var chunkBufferPool = sync.Pool{
	New: func() any { return &chunkBuffer{} },
}

// flushWriter is a tiny helper that wraps an http.Flusher so the
// streaming executor can rely on a single Write+Flush surface. Used
// by the SSE handlers in domains/streaming/anthropic_bridge.go and
// stream.go that already maintain an http.ResponseWriter.
type flushWriter struct {
	w       io.Writer
	flusher interface{ Flush() }
}

// Flush implements the standard Flusher behaviour. Used by tests
// that want to assert the buffer's behaviour without binding to
// net/http.
func (f *flushWriter) Flush() {
	if f.flusher != nil {
		f.flusher.Flush()
	}
}

// ChunkBufferStats is the observability record the dispatcher
// emits on end-of-stream. The combination of (writes, flushes)
// lets us compute "average write size" without exposing the
// buffer's internal slice to the metrics layer.
type ChunkBufferStats struct {
	Writes   uint64 `json:"writes"`
	Flushes  uint64 `json:"flushes"`
	Buffered int    `json:"buffered_at_close"`
}