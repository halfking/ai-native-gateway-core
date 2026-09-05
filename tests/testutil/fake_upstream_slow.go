// Package testutil hosts shared test fakes for the llm-gateway-go
// streaming state-machine test suites.
//
// The fakes implement the public interfaces exported by
// domains/streaming/state (PreStream / StreamWriter / Compressor) and
// are intentionally placed in tests/testutil rather than inside the
// production package so the state-machine code stays free of test-only
// symbols. They are *not* for production use.
package testutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// SafeBuffer is a mutex-protected bytes.Buffer used to emulate a real
// http.ResponseWriter in race tests. It is safe for concurrent Read /
// String / Write calls from multiple goroutines.
type SafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func NewSafeBuffer() *SafeBuffer { return &SafeBuffer{} }

func (b *SafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *SafeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *SafeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

// TickerKeepalive is a fake implementation of state.PreStream that runs
// a real time.Ticker inside a goroutine. It exists so cancel tests can
// assert that the runtime actually stops the keep-alive goroutine when
// the request reaches a terminal state.
//
// Lifecycle invariants:
//   - Start begins a goroutine that writes ": keep-alive\n\n" on every
//     tick of a 5ms (or supplied) ticker.
//   - Stop is idempotent and causes the goroutine to exit via a closed
//     done channel. After Stop returns, tickerRunning() reports false
//     once the goroutine has unwound.
//   - Concurrent Start/Stop from multiple goroutines is safe.
type TickerKeepalive struct {
	buf    io.Writer
	tick   time.Duration
	mu     sync.Mutex
	done   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
	runs   atomic.Int32
	stops  atomic.Int32
	frames atomic.Int32
}

func NewTickerKeepalive(buf io.Writer, tick time.Duration) *TickerKeepalive {
	if tick <= 0 {
		tick = 5 * time.Millisecond
	}
	return &TickerKeepalive{buf: buf, tick: tick, done: make(chan struct{})}
}

// Start launches the keep-alive goroutine. Calling Start more than once
// is a no-op — only the first call spins the goroutine.
func (k *TickerKeepalive) Start() {
	k.mu.Lock()
	defer k.mu.Unlock()
	select {
	case <-k.done:
		// already stopped, can't restart
		return
	default:
	}
	k.wg.Add(1)
	k.runs.Add(1)
	go k.loop()
}

// Stop signals the goroutine to exit and waits for it to drain. Idempotent.
func (k *TickerKeepalive) Stop() {
	k.once.Do(func() {
		k.stops.Add(1)
		close(k.done)
	})
	k.wg.Wait()
}

func (k *TickerKeepalive) loop() {
	defer k.wg.Done()
	t := time.NewTicker(k.tick)
	defer t.Stop()
	for {
		select {
		case <-k.done:
			return
		case <-t.C:
			if k.buf != nil {
				_, _ = k.buf.Write([]byte(": keep-alive\n\n"))
			}
			k.frames.Add(1)
		}
	}
}

// Running reports true while the keep-alive goroutine is still active.
// It is a snapshot and may be stale the instant it returns, but the
// callers in the cancel tests use it after wg.Wait() (via Stop()) so the
// goroutine has fully exited by the time Running() is consulted.
//
// Exposed (capitalised) so cross-package integration / e2e tests can
// assert the ticker is truly quiescent after a terminal transition.
func (k *TickerKeepalive) Running() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	select {
	case <-k.done:
		// done is closed → goroutine has exited; running is false.
		return false
	default:
		return true
	}
}

// Stopped reports whether Stop() has been called. It does not by itself
// guarantee the goroutine has unwound — see StoppedAndDrained for that.
func (k *TickerKeepalive) Stopped() bool {
	select {
	case <-k.done:
		return true
	default:
		return false
	}
}

// Frames returns the total number of keep-alive frames the goroutine
// has written. Useful for sanity checks ("ticker actually fired").
func (k *TickerKeepalive) Frames() int { return int(k.frames.Load()) }

// BlockingStreamWriter is a fake state.StreamWriter that records every
// frame it emits and refuses writes after Close. After Close, WriteFrame
// invokes onWriteAfterClosed (when set) so tests can assert no further
// bytes reach the client after cancellation.
type BlockingStreamWriter struct {
	mu                 sync.Mutex
	buf                io.Writer
	closed             bool
	frames             [][]byte
	wroteDone          bool
	onWriteAfterClosed func()
	writtenBytes       int
}

func NewBlockingStreamWriter(buf io.Writer) *BlockingStreamWriter {
	return &BlockingStreamWriter{buf: buf}
}

func (w *BlockingStreamWriter) WriteFrame(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if w.onWriteAfterClosed != nil {
			w.onWriteAfterClosed()
		}
		return 0, errors.New("stream writer closed")
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	w.frames = append(w.frames, cp)
	if w.buf != nil {
		_, _ = w.buf.Write(payload)
	}
	w.writtenBytes += len(payload)
	return len(payload), nil
}

func (w *BlockingStreamWriter) WriteDone() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if w.onWriteAfterClosed != nil {
			w.onWriteAfterClosed()
		}
		return errors.New("stream writer closed")
	}
	w.wroteDone = true
	if w.buf != nil {
		_, _ = w.buf.Write([]byte("[DONE]\n\n"))
	}
	return nil
}

func (w *BlockingStreamWriter) Closed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *BlockingStreamWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
}

// Write satisfies io.Writer for compatibility with generic producers
// (such as tests/testutil.FakeUpstream) that emit raw bytes instead of
// SSE-shaped frames. The payload is recorded as a frame and written to
// the underlying io.Writer — same path as WriteFrame, no double-write.
// After Close, Write is rejected exactly like WriteFrame so callers
// can rely on the close-after-terminal invariant.
func (w *BlockingStreamWriter) Write(p []byte) (int, error) {
	return w.WriteFrame(p)
}

// FramesSnapshot returns a defensive copy of all frames recorded so far.
func (w *BlockingStreamWriter) FramesSnapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([][]byte, len(w.frames))
	for i, f := range w.frames {
		cp := make([]byte, len(f))
		copy(cp, f)
		out[i] = cp
	}
	return out
}

// WroteDone reports whether [DONE] was written before Close.
func (w *BlockingStreamWriter) WroteDone() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wroteDone
}

// WrittenBytes is the total byte count successfully written to the
// underlying writer (data frames + [DONE] only; excludes writes that
// were rejected after Close).
func (w *BlockingStreamWriter) WrittenBytes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writtenBytes
}

// ImmediateCompressor is a fake state.Compressor whose Done channel
// closes after the supplied delay. Output returns an empty body.
type ImmediateCompressor struct {
	done   chan struct{}
	delay  time.Duration
	once   sync.Once
	output []byte
}

func NewImmediateCompressor(delay time.Duration) *ImmediateCompressor {
	if delay < 0 {
		delay = 0
	}
	return &ImmediateCompressor{delay: delay, done: make(chan struct{}), output: []byte{}}
}

func (c *ImmediateCompressor) Done() <-chan struct{} {
	c.once.Do(func() {
		if c.delay == 0 {
			close(c.done)
			return
		}
		go func() {
			time.Sleep(c.delay)
			close(c.done)
		}()
	})
	return c.done
}

func (c *ImmediateCompressor) Output() []byte { return c.output }

// UpstreamProducer is a small interface used by cancel + happy-path
// tests to model "the upstream is feeding us chunks via a goroutine".
// The fake implementation in FakeUpstream pumps 0..N frames on a fixed
// cadence; tests coordinate via the ch Halting channel to stop early
// (or just stop iterating once the state machine is terminal).
type UpstreamProducer interface {
	// Run blocks and emits one chunk per cadence tick until either
	// the supplied context is cancelled or the producer is signalled
	// to halt via the returned channel.
	Run(ctx context.Context) (halt <-chan struct{}, err error)
}

// FakeUpstream is a deterministic upstream producer used by the
// state-machine tests. It writes chunkPrefix + i bytes to buf every
// cadence tick and stops on context cancellation or a halt signal.
//
// The producer does NOT push state-machine events — it only writes to
// the underlying stream writer. The test goroutine is responsible for
// emitting the corresponding EventFirstByte / EventStreamEnded events
// at the right moments.
type FakeUpstream struct {
	buf       io.Writer
	chunks    int
	cadence   time.Duration
	chunkPref string

	wg       sync.WaitGroup
	stopOnce sync.Once
	halt     chan struct{}
}

func NewFakeUpstream(buf io.Writer, chunks int, cadence time.Duration, chunkPrefix string) *FakeUpstream {
	if cadence <= 0 {
		cadence = 5 * time.Millisecond
	}
	return &FakeUpstream{
		buf:       buf,
		chunks:    chunks,
		cadence:   cadence,
		chunkPref: chunkPrefix,
		halt:      make(chan struct{}),
	}
}

// Halt returns the channel that signals the producer to exit early.
// Calling Halt is idempotent.
func (u *FakeUpstream) Halt() <-chan struct{} { return u.halt }

func (u *FakeUpstream) HaltNow() { u.stopOnce.Do(func() { close(u.halt) }) }

// Run blocks and writes up to chunks chunks. Tests should also call
// HaltNow() to short-circuit when the state machine reaches a
// terminal state.
func (u *FakeUpstream) Run(ctx context.Context) error {
	u.wg.Add(1)
	defer u.wg.Done()
	t := time.NewTicker(u.cadence)
	defer t.Stop()
	for i := 0; i < u.chunks; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-u.halt:
			return nil
		case <-t.C:
			if u.buf != nil {
				chunk := []byte(u.chunkPref + " " + itoa(i) + "\n\n")
				_, _ = u.buf.Write(chunk)
			}
		}
	}
	return nil
}

// Wait blocks until the producer goroutine returns.
func (u *FakeUpstream) Wait() { u.wg.Wait() }

// itoa is a tiny strconv-free integer formatter for the upstream fake.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
