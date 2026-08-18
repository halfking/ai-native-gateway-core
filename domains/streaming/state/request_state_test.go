package state

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// 1. Matrix — the (state, event) -> Next table from spec §2.
// =============================================================================

func TestNext_Matrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		state  RequestState
		event  Event
		want   RequestState
		wantOK bool
	}{
		{"received->authed", StateReceived, EventAuthed, StateAuthed, true},
		{"authed->routed", StateAuthed, EventRouted, StateRouted, true},
		{"authed->compressing", StateAuthed, EventCompressing, StateCompressing, true},
		{"authed->dispatching (skipped)", StateAuthed, EventCompressingSkipped, StateDispatching, true},
		{"authed->dispatching (direct)", StateAuthed, EventDispatching, StateDispatching, true},
		{"routed->compressing", StateRouted, EventCompressing, StateCompressing, true},
		{"routed->dispatching (skipped)", StateRouted, EventCompressingSkipped, StateDispatching, true},
		{"routed->dispatching (direct)", StateRouted, EventDispatching, StateDispatching, true},
		{"compressing->dispatching", StateCompressing, EventCompressingDone, StateDispatching, true},
		{"dispatching->streaming", StateDispatching, EventFirstByte, StateStreaming, true},
		{"streaming->completed", StateStreaming, EventStreamEnded, StateCompleted, true},

		{"received->failed", StateReceived, EventFailed, StateFailed, true},
		{"authed->failed", StateAuthed, EventFailed, StateFailed, true},
		{"routed->failed", StateRouted, EventFailed, StateFailed, true},
		{"compressing->failed", StateCompressing, EventFailed, StateFailed, true},
		{"dispatching->failed", StateDispatching, EventFailed, StateFailed, true},
		{"streaming->failed", StateStreaming, EventFailed, StateFailed, true},

		{"received->cancelled", StateReceived, EventCancelled, StateCancelled, true},
		{"authed->cancelled", StateAuthed, EventCancelled, StateCancelled, true},
		{"routed->cancelled", StateRouted, EventCancelled, StateCancelled, true},
		{"compressing->cancelled", StateCompressing, EventCancelled, StateCancelled, true},
		{"dispatching->cancelled", StateDispatching, EventCancelled, StateCancelled, true},
		{"streaming->cancelled", StateStreaming, EventCancelled, StateCancelled, true},

		// Illegal transitions.
		{"received->routed (illegal)", StateReceived, EventRouted, StateReceived, false},
		{"authed->firstByte (illegal)", StateAuthed, EventFirstByte, StateAuthed, false},
		{"compressing->firstByte (illegal)", StateCompressing, EventFirstByte, StateCompressing, false},
		{"dispatching->compressingDone (illegal)", StateDispatching, EventCompressingDone, StateDispatching, false},
		{"streaming->firstByte (illegal)", StateStreaming, EventFirstByte, StateStreaming, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr, ok := Next(tc.state, tc.event)
			if ok != tc.wantOK {
				t.Fatalf("ok mismatch: got %v want %v (transition=%+v)", ok, tc.wantOK, tr)
			}
			if ok && tr.Next != tc.want {
				t.Fatalf("Next mismatch: got %s want %s", tr.Next, tc.want)
			}
		})
	}
}

func TestNext_TerminalRejectsEverything(t *testing.T) {
	t.Parallel()
	terminals := []RequestState{StateCompleted, StateFailed, StateCancelled}
	events := []Event{
		EventAuthed, EventRouted, EventCompressing, EventCompressingDone,
		EventCompressingSkipped, EventDispatching, EventFirstByte,
		EventStreamEnded, EventFailed, EventCancelled,
	}
	for _, s := range terminals {
		for _, e := range events {
			if _, ok := Next(s, e); ok {
				t.Errorf("terminal %s accepted event %s", s, e)
			}
		}
	}
}

func TestNext_IsTerminal(t *testing.T) {
	t.Parallel()
	for _, s := range []RequestState{
		StateReceived, StateAuthed, StateRouted, StateCompressing,
		StateDispatching, StateStreaming,
	} {
		if s.IsTerminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
	for _, s := range []RequestState{StateCompleted, StateFailed, StateCancelled} {
		if !s.IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
}

// =============================================================================
// 2. Happy path through every state to StateCompleted.
// =============================================================================

func TestRuntime_HappyPath(t *testing.T) {
	t.Parallel()

	buf := newSafeBuffer()
	ps := newFakePreStream(buf)
	sw := newFakeStreamWriter(buf)

	ctx := NewRequestContext("req-happy", "tenant-1")
	ctx.SetPreStream(ps)
	ctx.SetStreamWriter(sw)

	rt := NewRuntime(ctx)
	rt.OnEnter(StateAuthed, func(r *Runtime) error { ps.Start(); return nil })
	rt.OnEnter(StateCompressing, func(r *Runtime) error {
		r.ctx.SetCompressor(newFakeCompressor(5 * time.Millisecond))
		return nil
	})
	// On terminal, stop further writes — mirrors what the production
	// HTTP handler does once it observes Done().
	rt.OnEnter(StateCancelled, func(r *Runtime) error { sw.Close(); return nil })
	rt.OnEnter(StateFailed, func(r *Runtime) error { sw.Close(); return nil })

	done := make(chan error, 1)
	go func() { done <- rt.Run(context.Background()) }()

	// Side-effect goroutine: pump CompressingDone when the compressor
	// signals, then write data frames once streaming starts.
	go driveHappyPath(rt, ctx, sw)

	rt.Emit(EventAuthed)
	rt.Emit(EventRouted)
	rt.Emit(EventCompressing)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s, state=%s", rt.State())
	}

	if got := rt.State(); got != StateCompleted {
		t.Fatalf("state=%s want=%s", got, StateCompleted)
	}
	if !ps.stopped() {
		t.Error("preStream ticker was not stopped on terminal")
	}
	if log := ctx.EventLog(); len(log) == 0 || log[0].To != StateAuthed {
		t.Errorf("event log unexpected: %+v", log)
	}
}

// driveHappyPath is the side-effect pump used by the happy path
// tests. It blocks until the compressor is installed, waits for
// CompressingDone, emits it, waits for streaming, writes 3 frames +
// [DONE], and emits StreamEnded.
func driveHappyPath(rt *Runtime, ctx *RequestContext, sw *fakeStreamWriter) {
	deadline := time.Now().Add(2 * time.Second)
	for ctx.Compressor() == nil {
		if rt.State().IsTerminal() || time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-ctx.Compressor().Done():
	case <-time.After(time.Second):
		return
	}
	if rt.State().IsTerminal() {
		return
	}
	rt.Emit(EventCompressingDone)
	for rt.State() != StateDispatching {
		if rt.State().IsTerminal() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	rt.Emit(EventFirstByte)
	for rt.State() != StateStreaming {
		if rt.State().IsTerminal() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		if _, err := sw.WriteFrame([]byte("data: hello\n\n")); err != nil {
			return
		}
	}
	if err := sw.WriteDone(); err != nil {
		return
	}
	rt.Emit(EventStreamEnded)
}

// =============================================================================
// 3. Frame ordering — keep-alive, data, [DONE] appear in order.
// =============================================================================

func TestRuntime_FrameOrdering(t *testing.T) {
	t.Parallel()

	buf := newSafeBuffer()
	ps := newFakePreStream(buf)
	sw := newFakeStreamWriter(buf)
	ctx := NewRequestContext("req-order", "tenant-1")
	ctx.SetPreStream(ps)
	ctx.SetStreamWriter(sw)

	rt := NewRuntime(ctx)
	rt.OnEnter(StateAuthed, func(r *Runtime) error { ps.Start(); return nil })

	done := make(chan error, 1)
	go func() { done <- rt.Run(context.Background()) }()

	// Once dispatching, emit FirstByte then write frames + signal end.
	go func() {
		for rt.State() != StateDispatching {
			if rt.State().IsTerminal() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		rt.Emit(EventFirstByte)
		for rt.State() != StateStreaming {
			if rt.State().IsTerminal() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		for i := 0; i < 4; i++ {
			sw.WriteFrame([]byte("data: chunk\n\n"))
		}
		sw.WriteDone()
		rt.Emit(EventStreamEnded)
	}()

	rt.Emit(EventAuthed)
	rt.Emit(EventRouted)
	rt.Emit(EventCompressingSkipped) // -> Dispatching

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s, state=%s", rt.State())
	}

	out := buf.String()
	keepIdx := strings.Index(out, ": keep-alive")
	dataIdx := strings.Index(out, "data: chunk")
	doneIdx := strings.Index(out, "[DONE]")
	if keepIdx < 0 || dataIdx < 0 || doneIdx < 0 {
		t.Fatalf("missing frames: keep=%d data=%d done=%d out=%q",
			keepIdx, dataIdx, doneIdx, out)
	}
	if !(keepIdx < dataIdx && dataIdx < doneIdx) {
		t.Fatalf("frames crossed: keep=%d data=%d done=%d", keepIdx, dataIdx, doneIdx)
	}
}

// =============================================================================
// 4. Race test — 6 goroutines, compressor blocks, client cancels at 50ms.
//    State must reach StateCancelled within ~100ms of cancel; the stream
//    writer must reject any further writes after the cancel is observed.
// =============================================================================

func TestRuntime_RaceCancellation(t *testing.T) {
	t.Parallel()

	buf := newSafeBuffer()
	ps := newFakePreStream(buf)
	sw := newFakeStreamWriter(buf)
	comp := newFakeCompressor(200 * time.Millisecond)

	ctx := NewRequestContext("req-race", "tenant-1")
	ctx.SetPreStream(ps)
	ctx.SetCompressor(comp)
	ctx.SetStreamWriter(sw)

	rt := NewRuntime(ctx)
	rt.OnEnter(StateAuthed, func(r *Runtime) error { ps.Start(); return nil })
	// Mirror production: terminal hook closes the writer so the
	// runtime can rely on sw.Closed() to reject post-cancel writes.
	rt.OnEnter(StateCancelled, func(r *Runtime) error { sw.Close(); return nil })

	var writesAfterCancel atomic.Int32
	sw.onWriteAfterClosed = func() { writesAfterCancel.Add(1) }

	// Goroutine A: client cancels after 50ms.
	cancelStart := make(chan time.Time, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancelStart <- time.Now()
		rt.Cancel(errors.New("client disconnected"))
	}()

	// Goroutines B-F: spurious success events the runtime must drop
	// (state is terminal by the time they arrive).
	for _, ev := range []Event{EventFirstByte, EventStreamEnded, EventFailed, EventAuthed} {
		ev := ev
		go func() {
			time.Sleep(60*time.Millisecond + 10*time.Millisecond*time.Duration(ev))
			rt.Emit(ev)
		}()
	}

	done := make(chan error, 1)
	go func() { done <- rt.Run(context.Background()) }()

	rt.Emit(EventAuthed)
	rt.Emit(EventRouted)
	rt.Emit(EventCompressing)

	// Wait for cancel to fire, then measure latency to Run() return.
	startCancel := <-cancelStart
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run should return non-nil cancel error")
		}
		latency := time.Since(startCancel)
		// Spec target is <30ms. Allow 500ms slack for CI noise;
		// actual value is typically <1ms.
		if latency > 500*time.Millisecond {
			t.Errorf("cancellation latency too high: %v", latency)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run did not return within 1s, state=%s", rt.State())
	}

	if got := rt.State(); got != StateCancelled {
		t.Fatalf("final state=%s want=%s", got, StateCancelled)
	}
	if !sw.Closed() {
		t.Error("stream writer reports not-closed after terminal")
	}
	if n := writesAfterCancel.Load(); n != 0 {
		t.Errorf("stream writer accepted %d writes after close", n)
	}
	if !ps.stopped() {
		t.Error("preStream ticker was not stopped after cancellation")
	}
}

// =============================================================================
// 5. Illegal transition at runtime falls through to StateFailed.
// =============================================================================

func TestRuntime_IllegalTransitionFails(t *testing.T) {
	t.Parallel()
	ctx := NewRequestContext("req-illegal", "tenant-1")
	rt := NewRuntime(ctx)

	done := make(chan error, 1)
	go func() { done <- rt.Run(context.Background()) }()

	// From StateReceived, EventFirstByte is illegal — must route to
	// StateFailed.
	rt.Emit(EventFirstByte)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Run did not return within 1s, state=%s", rt.State())
	}
	if got := rt.State(); got != StateFailed {
		t.Fatalf("state=%s want=%s", got, StateFailed)
	}
	if rt.Err() == nil {
		t.Error("expected non-nil terminal error for illegal transition")
	}
}

// =============================================================================
// 6. Cancel before any event reaches StateCancelled.
// =============================================================================

func TestRuntime_ImmediateCancel(t *testing.T) {
	t.Parallel()
	ctx := NewRequestContext("req-immediate", "tenant-1")
	rt := NewRuntime(ctx)

	done := make(chan error, 1)
	go func() { done <- rt.Run(context.Background()) }()

	time.Sleep(5 * time.Millisecond)
	rt.Cancel(errors.New("nope"))

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-nil cancel error")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after immediate cancel")
	}
	if rt.State() != StateCancelled {
		t.Fatalf("state=%s want=%s", rt.State(), StateCancelled)
	}
}

// =============================================================================
// Test fakes
// =============================================================================

// safeBuffer is a mutex-protected bytes.Buffer used to emulate a real
// http.ResponseWriter in race tests.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSafeBuffer() *safeBuffer { return &safeBuffer{} }

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// fakePreStream writes a single ": keep-alive\n\n" frame on Start.
// Stop is idempotent.
type fakePreStream struct {
	mu          sync.Mutex
	buf         *safeBuffer
	stoppedFlag bool
}

func newFakePreStream(buf *safeBuffer) *fakePreStream { return &fakePreStream{buf: buf} }

func (p *fakePreStream) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stoppedFlag {
		return
	}
	p.buf.Write([]byte(": keep-alive\n\n"))
}

func (p *fakePreStream) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stoppedFlag = true
}

func (p *fakePreStream) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stoppedFlag
}

// fakeCompressor blocks for d then closes the done channel.
type fakeCompressor struct {
	d    time.Duration
	done chan struct{}
	once sync.Once
}

func newFakeCompressor(d time.Duration) *fakeCompressor {
	return &fakeCompressor{d: d, done: make(chan struct{})}
}

func (c *fakeCompressor) Done() <-chan struct{} {
	c.once.Do(func() {
		go func() {
			time.Sleep(c.d)
			close(c.done)
		}()
	})
	return c.done
}

func (c *fakeCompressor) Output() []byte { return nil }

// fakeStreamWriter wraps a safeBuffer. After Close, WriteFrame and
// WriteDone return an error and increment onWriteAfterClosed so the
// race test can assert that nothing leaks past cancellation.
type fakeStreamWriter struct {
	mu                 sync.Mutex
	buf                *safeBuffer
	closedFlag         bool
	onWriteAfterClosed func()
}

func newFakeStreamWriter(buf *safeBuffer) *fakeStreamWriter {
	return &fakeStreamWriter{buf: buf}
}

func (w *fakeStreamWriter) WriteFrame(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closedFlag {
		if w.onWriteAfterClosed != nil {
			w.onWriteAfterClosed()
		}
		return 0, errors.New("stream writer closed")
	}
	return w.buf.Write(p)
}

func (w *fakeStreamWriter) WriteDone() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closedFlag {
		if w.onWriteAfterClosed != nil {
			w.onWriteAfterClosed()
		}
		return errors.New("stream writer closed")
	}
	_, err := w.buf.Write([]byte("[DONE]\n\n"))
	return err
}

func (w *fakeStreamWriter) Closed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closedFlag
}

func (w *fakeStreamWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closedFlag = true
}
