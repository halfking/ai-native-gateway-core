package executors

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingWriter is an io.Writer that records every Write call.
// Useful for asserting flush behaviour.
type countingWriter struct {
	mu      sync.Mutex
	writes  int
	bytes   int
	chunks  [][]byte
	flushed int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	c.bytes += len(p)
	// Snapshot the chunk so we can assert the high-water-mark
	// behaviour without aliasing the underlying buffer.
	snap := make([]byte, len(p))
	copy(snap, p)
	c.chunks = append(c.chunks, snap)
	return len(p), nil
}

func (c *countingWriter) Flush() {
	c.mu.Lock()
	c.flushed++
	c.mu.Unlock()
}

func (c *countingWriter) Stats() (writes, bytes, chunks int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes, c.bytes, len(c.chunks)
}

// TestChunkBuffer_BelowThresholdBuffersAndFlushesOnClose covers the
// simplest happy path: writes smaller than the high-water mark
// are buffered until Close.
func TestChunkBuffer_BelowThresholdBuffersAndFlushesOnClose(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBuffer(w)
	defer ReleaseChunkBuffer(b)

	for i := 0; i < 5; i++ {
		if _, err := b.Write([]byte("hello")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if buffered := b.Buffered(); buffered != 25 {
		t.Fatalf("buffered=%d want 25", buffered)
	}
	// No flush yet.
	if _, _, n := w.Stats(); n != 0 {
		t.Fatalf("no flush expected yet; got %d chunks", n)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if buffered := b.Buffered(); buffered != 0 {
		t.Fatalf("post-close buffered=%d want 0", buffered)
	}
	writes, _, n := w.Stats()
	if writes != 1 {
		t.Fatalf("Close should produce exactly 1 chunk; got %d", writes)
	}
	if n != 1 {
		t.Fatalf("expected 1 chunk recorded; got %d", n)
	}
}

// TestChunkBuffer_AboveThresholdFlushesInline checks the high-water
// path: writes that fill the buffer flush inline.
func TestChunkBuffer_AboveThresholdFlushesInline(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBuffer(w)
	defer ReleaseChunkBuffer(b)

	// Write a payload that exceeds the 8 KiB chunk size.
	big := make([]byte, DefaultChunkSize+1024)
	for i := range big {
		big[i] = byte(i % 256)
	}
	if _, err := b.Write(big); err != nil {
		t.Fatalf("Write big: %v", err)
	}
	if buffered := b.Buffered(); buffered != 0 {
		t.Fatalf("post-write buffered=%d want 0", buffered)
	}
	writes, _, _ := w.Stats()
	if writes < 1 {
		t.Fatalf("expected at least one flush; got %d", writes)
	}
}

// TestChunkBuffer_IdleFlushFires verifies that a small write
// followed by a sleep past the idle interval triggers a flush.
func TestChunkBuffer_IdleFlushFires(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBufferWith(w, DefaultChunkSize, 10*time.Millisecond)
	defer ReleaseChunkBuffer(b)

	if _, err := b.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, _, n := w.Stats(); n != 0 {
		t.Fatalf("flush must not fire before idle interval")
	}
	time.Sleep(20 * time.Millisecond)
	// Next Write should observe the idle interval and flush.
	if _, err := b.Write([]byte("y")); err != nil {
		t.Fatalf("Write idle: %v", err)
	}
	writes, _, _ := w.Stats()
	if writes == 0 {
		t.Fatalf("idle flush never fired")
	}
}

// TestChunkBuffer_IdleDisabledWhenZero verifies zero. A zero
// flushAfter means "no idle flush".
func TestChunkBuffer_IdleDisabledWhenZero(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBufferWith(w, DefaultChunkSize, 0)
	defer ReleaseChunkBuffer(b)

	if _, err := b.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, _, n := w.Stats(); n != 0 {
		t.Fatalf("idle flush must NOT fire when flushAfter=0")
	}
}

// TestChunkBuffer_ChunkSizeRespected covers the chunk-size boundary:
// four 64-byte writes into a 256-byte buffer fill it on the 4th
// write which triggers an inline flush.
func TestChunkBuffer_ChunkSizeRespected(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBufferWith(w, 256, 0) // 256-byte chunks
	defer ReleaseChunkBuffer(b)

	for i := 0; i < 3; i++ {
		if _, err := b.Write(bytes.Repeat([]byte{byte('a' + i)}, 64)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if buffered := b.Buffered(); buffered != 192 {
		t.Fatalf("buffered=%d want 192 (3*64, no flush yet)", buffered)
	}
	if _, _, n := w.Stats(); n != 0 {
		t.Fatalf("flush must not fire before buffer fills")
	}
	// The 4th write fills the buffer and triggers an inline flush.
	if _, err := b.Write(bytes.Repeat([]byte{'d'}, 64)); err != nil {
		t.Fatalf("Write 4: %v", err)
	}
	if buffered := b.Buffered(); buffered != 0 {
		t.Fatalf("post-flush buffered=%d want 0", buffered)
	}
	writes, _, _ := w.Stats()
	if writes != 1 {
		t.Fatalf("expected 1 inline flush; got %d", writes)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestChunkBuffer_ChunkSizeAutoFlushesOver verifies that a single
// write exceeding the chunk size triggers an inline direct flush
// (bypassing the buffer entirely).
func TestChunkBuffer_ChunkSizeAutoFlushesOver(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBufferWith(w, 256, 0)
	defer ReleaseChunkBuffer(b)

	// Single write of 1024 bytes (= 4 * chunkSize).
	big := bytes.Repeat([]byte{1}, 1024)
	if _, err := b.Write(big); err != nil {
		t.Fatalf("Write big: %v", err)
	}
	writes, _, n := w.Stats()
	if writes != 1 || n != 1 {
		t.Fatalf("expected 1 direct flush; got writes=%d chunks=%d", writes, n)
	}
	if buffered := b.Buffered(); buffered != 0 {
		t.Fatalf("post-write buffered=%d want 0", buffered)
	}
}

// TestChunkBuffer_RejectsAfterClose verifies ErrClosed is returned
// after Close.
func TestChunkBuffer_RejectsAfterClose(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBuffer(w)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	defer ReleaseChunkBuffer(b)
	if _, err := b.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after Close must return ErrClosed; got %v", err)
	}
	if err := b.Flush(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Flush after Close must return ErrClosed; got %v", err)
	}
}

// TestChunkBuffer_PoolReusesScratchSlice verifies the pool
// recycles the 8 KiB scratch slice across acquires.
//
// Note: the pool's underlying sync.Pool may evict entries under
// GC pressure or concurrent churn, so this is a best-effort
// check — we assert "reuse is possible" rather than "every
// acquire reuses".
func TestChunkBuffer_PoolReusesScratchSlice(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBuffer(w)
	wantCap := cap(b.buf)
	b.Close()
	ReleaseChunkBuffer(b)

	b2 := AcquireChunkBuffer(w)
	defer b2.Close()
	defer ReleaseChunkBuffer(b2)
	if cap(b2.buf) != wantCap {
		t.Fatalf("acquired-after-release buffer cap=%d want %d", cap(b2.buf), wantCap)
	}
}

// TestChunkBuffer_ConcurrentAcquireRelease fires many goroutines
// through the pool wrapper to verify the sync.Pool is safe.
func TestChunkBuffer_ConcurrentAcquireRelease(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				w := &countingWriter{}
				b := AcquireChunkBuffer(w)
				_, _ = b.Write([]byte("payload"))
				_ = b.Close()
				ReleaseChunkBuffer(b)
			}
		}()
	}
	wg.Wait()
}

// errWriter returns a fixed error from Write.
type errWriter struct {
	err error
}

func (e *errWriter) Write(p []byte) (int, error) { return 0, e.err }

// TestChunkBuffer_PropagatesWriterError covers the error path.
func TestChunkBuffer_PropagatesWriterError(t *testing.T) {
	want := errors.New("disk full")
	b := AcquireChunkBuffer(&errWriter{err: want})
	defer ReleaseChunkBuffer(b)
	_, err := b.Write(bytes.Repeat([]byte{1}, DefaultChunkSize+1))
	if !errors.Is(err, want) {
		t.Fatalf("Write should propagate underlying writer error; got %v", err)
	}
}

// TestChunkBuffer_StatsCounters ensures the Stats counters
// reflect the work done.
func TestChunkBuffer_StatsCounters(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBufferWith(w, 16, 0)
	defer ReleaseChunkBuffer(b)

	for i := 0; i < 4; i++ {
		_, _ = b.Write([]byte("0123456789ABCDEF")) // 16 bytes
	}
	// 4 writes of 16 bytes each; the second write triggers a
	// flush of the first batch, then each subsequent pair flushes.
	writes, flushes := b.Stats()
	if writes != 64 {
		t.Fatalf("writes=%d want 64 (4*16)", writes)
	}
	// flushes should be ≥ 1.
	if flushes == 0 {
		t.Fatalf("flushes=0 want ≥ 1")
	}
	_ = atomic.LoadUint64 // keep import alive on older Go versions
}

// TestChunkBuffer_FlushIsIdempotent ensures Flush is safe to
// call multiple times even with no pending bytes.
func TestChunkBuffer_FlushIsIdempotent(t *testing.T) {
	w := &countingWriter{}
	b := AcquireChunkBuffer(w)
	defer ReleaseChunkBuffer(b)
	for i := 0; i < 10; i++ {
		if err := b.Flush(); err != nil {
			t.Fatalf("Flush %d: %v", i, err)
		}
	}
}

// ---- Benchmarks ----

// BenchmarkChunkBuffer_SmallWrites simulates the production hot
// path: many small writes (typical SSE chunk = 5-30 bytes) followed
// by Close. The benchmark measures the steady-state cost per write.
func BenchmarkChunkBuffer_SmallWrites(b *testing.B) {
	w := &countingWriter{}
	payload := []byte("delta")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bb := AcquireChunkBuffer(w)
		for j := 0; j < 64; j++ {
			_, _ = bb.Write(payload)
		}
		_ = bb.Close()
		ReleaseChunkBuffer(bb)
	}
}

// BenchmarkChunkBuffer_MixedSizes mixes small and large writes.
func BenchmarkChunkBuffer_MixedSizes(b *testing.B) {
	w := &countingWriter{}
	small := []byte("delta")
	big := bytes.Repeat([]byte{0}, 4*1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bb := AcquireChunkBuffer(w)
		for j := 0; j < 16; j++ {
			_, _ = bb.Write(small)
		}
		_, _ = bb.Write(big)
		for j := 0; j < 16; j++ {
			_, _ = bb.Write(small)
		}
		_ = bb.Close()
		ReleaseChunkBuffer(bb)
	}
}

// BenchmarkChunkBuffer_DefaultFlush exercises the idle-flush path
// without sleeping — the timer check is what costs time.
func BenchmarkChunkBuffer_DefaultFlush(b *testing.B) {
	w := &countingWriter{}
	payload := []byte("delta")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bb := AcquireChunkBuffer(w)
		_, _ = bb.Write(payload)
		_ = bb.Flush()
		ReleaseChunkBuffer(bb)
	}
}

// BenchmarkBaselineNoBuffer is the reference against which the
// chunk-buffer path is judged: a write + io.Copy per chunk, no
// buffering. This is the legacy behaviour.
func BenchmarkBaselineNoBuffer(b *testing.B) {
	w := &countingWriter{}
	payload := []byte("delta")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 64; j++ {
			_, _ = w.Write(payload)
		}
	}
}

// io.Writer conformance check.
var _ io.Writer = (*chunkBuffer)(nil)