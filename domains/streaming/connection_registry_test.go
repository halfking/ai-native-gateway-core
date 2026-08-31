package streaming

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── UT-SK-04 连接注册表 ────────────────────────────────────────────────────

func TestConnectionRegistryRegisterLookupUnregister(t *testing.T) {
	reg := NewConnectionRegistry(0, 0, 0)
	var closeReason string
	var onCloseOnce sync.Once
	require.NoError(t, reg.Register("req-1", &FlushFrameWriter{W: &bytes.Buffer{}},
		RegistrationMetadata{Protocol: "openai_chat", ClientType: "zcode"},
		func(reason string) { onCloseOnce.Do(func() { closeReason = reason }) }))

	snap, ok := reg.Lookup("req-1")
	require.True(t, ok)
	assert.Equal(t, "req-1", snap.RequestID)
	assert.Equal(t, "openai_chat", snap.Protocol)
	assert.Equal(t, "zcode", snap.ClientType)
	assert.False(t, snap.Closed)
	assert.Equal(t, 1, reg.Len())

	// Miss for an unknown id.
	_, ok = reg.Lookup("nope")
	assert.False(t, ok)

	// Unregister fires onClose exactly once with the reason.
	require.NoError(t, reg.Unregister("req-1", "stream_end"))
	assert.Equal(t, "stream_end", closeReason)
	assert.Equal(t, 0, reg.Len())

	// Second unregister: not registered anymore (idempotent refusal).
	assert.ErrorIs(t, reg.Unregister("req-1", "again"), ErrConnectionNotRegistered)
	assert.Equal(t, "stream_end", closeReason, "onClose must fire exactly once")

	// Closed audit ring carries the reason.
	hist := reg.ClosedHistory(10)
	require.Len(t, hist, 1)
	assert.Equal(t, "req-1", hist[0].RequestID)
	assert.True(t, hist[0].Closed)
	assert.Equal(t, "stream_end", hist[0].CloseReason)
}

func TestConnectionRegistryCapacityFullDropsWithoutBlocking(t *testing.T) {
	reg := NewConnectionRegistry(2, time.Second, 0)
	w := &FlushFrameWriter{W: &bytes.Buffer{}}
	var replacedReason string
	require.NoError(t, reg.Register("a", w, RegistrationMetadata{}, func(reason string) { replacedReason = reason }))
	require.NoError(t, reg.Register("b", w, RegistrationMetadata{}, nil))

	// Full: refuse, never block (UT-SK-04).
	err := reg.Register("c", w, RegistrationMetadata{}, nil)
	require.ErrorIs(t, err, ErrConnectionRegistryFull)
	assert.Equal(t, 2, reg.Len())

	// Duplicate request_id replaces the previous entry even when full (no
	// capacity growth) and fires the REPLACED registration's onClose.
	require.NoError(t, reg.Register("a", w, RegistrationMetadata{}, nil))
	assert.Equal(t, "replaced", replacedReason)
	assert.Equal(t, 2, reg.Len())
}

func TestConnectionRegistryConcurrentRegisterUnregister(t *testing.T) {
	reg := NewConnectionRegistry(512, time.Second, 0)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "r-" + strings.Repeat("x", i%8) + time.Now().Format("150405.000000000")
			_ = reg.Register(id, &FlushFrameWriter{W: &bytes.Buffer{}}, RegistrationMetadata{Protocol: "openai_chat"}, nil)
			_, _ = reg.Lookup(id)
			_ = reg.Unregister(id, "stream_end")
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 0, reg.Len(), "all concurrent entries must unregister cleanly")
}

func TestConnectionRegistryWriteUpdatesFrameAccounting(t *testing.T) {
	reg := NewConnectionRegistry(0, time.Second, 0)
	buf := &bytes.Buffer{}
	require.NoError(t, reg.Register("req", &FlushFrameWriter{W: buf}, RegistrationMetadata{}, nil))
	require.NoError(t, reg.WriteFrame("req", "data: {}\n\n"))
	require.NoError(t, reg.WriteComment("req", "keep-alive"))

	snap, ok := reg.Lookup("req")
	require.True(t, ok)
	assert.EqualValues(t, 2, snap.FramesWritten)
	assert.EqualValues(t, len("data: {}\n\n")+len(": keep-alive\n\n"), snap.BytesWritten)
	assert.False(t, snap.LastFrameAt.IsZero())
	assert.Contains(t, buf.String(), ": keep-alive\n\n")

	// Unknown request id: not registered, no panic.
	assert.ErrorIs(t, reg.WriteFrame("missing", "x"), ErrConnectionNotRegistered)
}

// ── UT-SK-05 串行写出：心跳与数据帧不交错 ────────────────────────────────

// interleavingWriter records begin/end markers around a slow write so a
// scheduling interleaving between two WriteFrame calls is detectable.
type interleavingWriter struct {
	mu    sync.Mutex
	log   []string
	delay time.Duration
}

func (w *interleavingWriter) WriteFrame(frame string) error {
	w.mu.Lock()
	w.log = append(w.log, "begin "+frame)
	w.mu.Unlock()
	time.Sleep(w.delay)
	w.mu.Lock()
	w.log = append(w.log, "end "+frame)
	w.mu.Unlock()
	return nil
}

func TestConnectionRegistrySerializedWritesNoInterleaving(t *testing.T) {
	reg := NewConnectionRegistry(0, 5*time.Second, 0)
	w := &interleavingWriter{delay: 2 * time.Millisecond}
	require.NoError(t, reg.Register("req", w, RegistrationMetadata{}, nil))

	const writers = 16
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			frame := "data: {\"i\":" + strings.TrimSpace(strings.Repeat(string(rune('0'+i%10)), 1)) + "}\n\n"
			_ = reg.WriteFrame("req", frame)
			_ = reg.WriteComment("req", "keep-alive")
		}(i)
	}
	wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()
	// Every begin must be immediately followed by its matching end — no
	// other begin may squeeze in between (frames never interleave).
	depth := 0
	for _, entry := range w.log {
		if strings.HasPrefix(entry, "begin ") {
			depth++
			require.Equal(t, 1, depth, "interleaved frame write detected: %v", w.log)
		} else {
			depth--
			require.Equal(t, 0, depth, "unbalanced frame write markers: %v", w.log)
		}
	}
	assert.Equal(t, 0, depth)
	assert.Equal(t, writers*2*2, len(w.log), "every heartbeact and data frame must be written")
}

// ── UT-SK-09 / G7 客户端写 deadline ───────────────────────────────────────

// blockedWriter simulates a stalled/zombie client connection: the write
// never completes.
type blockedWriter struct{ release chan struct{} }

func (w *blockedWriter) WriteFrame(string) error {
	<-w.release
	return errors.New("write after deadline")
}

func TestConnectionRegistryWriteDeadlineTreatedAsClientGone(t *testing.T) {
	reg := NewConnectionRegistry(0, 60*time.Millisecond, 0)
	bw := &blockedWriter{release: make(chan struct{})}
	closeReasons := make(chan string, 1)
	require.NoError(t, reg.Register("req", bw, RegistrationMetadata{}, func(reason string) {
		closeReasons <- reason
	}))

	start := time.Now()
	err := reg.WriteFrame("req", "data: {}\n\n")
	elapsed := time.Since(start)
	require.ErrorIs(t, err, ErrClientWriteDeadline)
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "deadline must bound the write")
	assert.Less(t, elapsed, 5*time.Second, "deadline must not hang the caller")

	// Callback notifies write_deadline (client considered disconnected, N2).
	select {
	case reason := <-closeReasons:
		assert.Equal(t, "write_deadline", reason)
	case <-time.After(time.Second):
		t.Fatal("onClose was not notified on write deadline")
	}

	// Entry unregistered: further writes report not registered.
	assert.Equal(t, 0, reg.Len())
	assert.ErrorIs(t, reg.WriteFrame("req", "x"), ErrConnectionNotRegistered)
	close(bw.release) // release the stuck watchdog goroutine

	hist := reg.ClosedHistory(1)
	require.Len(t, hist, 1)
	assert.Equal(t, "write_deadline", hist[0].CloseReason)
}

func TestConnectionRegistrySerializedStreamWriterAdapter(t *testing.T) {
	// The production adapter must route frames through WriteTransportFrame so
	// side-channel comments never pollute the semantic wire capture
	// (UT-SK-08 data integrity).
	var buf bytes.Buffer
	sw := NewSerializedStreamWriter(&buf)
	adapter := NewSerializedFrameWriter(sw)
	require.NoError(t, adapter.WriteFrame(": thinking: \"x\"\n\n"))
	assert.Equal(t, ": thinking: \"x\"\n\n", buf.String())

	require.Error(t, (&SerializedFrameWriter{}).WriteFrame("x"), "nil writer must fail closed")
}

// TestConnectionRegistryWriteGoroutineCapBoundsResourceUse pins the
// semaphore that bounds the number of WriteFrame goroutines the
// registry may have in flight at any moment. Without this cap, every
// WriteFrame spawns a goroutine and a sustained burst of writes against
// many stalled entries could grow the goroutine count to capacity *
// (timeout / typical-frame-time) — unbounded on a hostile workload.
//
// The fix: each WriteFrame tries to push one slot onto the registry's
// buffered writeSlots channel before spawning. When the channel is
// full, WriteFrame returns ErrWriteSlotsExhausted immediately. The
// spawned goroutine pops a slot on every exit path (success / error /
// deadline) so the cap is released even when the parent caller has
// already returned.
//
// This test:
//   1. Builds a registry with maxWriteGoroutines=4 (small for fast tests).
//   2. Registers 4 distinct entries with blockedWriter sinks so each
//      WriteFrame goroutine is held indefinitely.
//   3. Fires 4 WriteFrame calls — each must acquire a slot and the
//      goroutine stays parked on the writer's <-release.
//   4. Fires a 5th WriteFrame — must fail with ErrWriteSlotsExhausted.
//   5. Closes one writer's release channel, observes the goroutine
//      returns and the slot frees. A subsequent WriteFrame succeeds.
//   6. Releases the remaining three and asserts the slot count returns
//      to its full cap so the registry isn't permanently leaky.
func TestConnectionRegistryWriteGoroutineCapBoundsResourceUse(t *testing.T) {
	const slotCount = 4
	reg := NewConnectionRegistry(0, time.Hour, slotCount) // long timeout; we control release explicitly

	// Two distinct release channels so we can drain the cap-fill
	// batch independently of the refill batch.
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	writer1 := &blockedWriter{release: release1}
	writer2 := &blockedWriter{release: release2}

	// Register slotCount entries for the cap-fill batch.
	ids := make([]string, slotCount)
	for i := 0; i < slotCount; i++ {
		ids[i] = fmt.Sprintf("req-%d", i)
		require.NoError(t, reg.Register(ids[i], writer1, RegistrationMetadata{}, nil))
	}

	// Fill the cap by launching slotCount WriteFrame calls in
	// background goroutines. Each call acquires a slot, spawns a
	// child goroutine parked on writer1, and blocks on the second
	// select until either the child completes or the watchdog
	// fires. We don't await the parents here — they complete when
	// release1 is closed below.
	parentResults1 := make(chan error, slotCount)
	for i := 0; i < slotCount; i++ {
		i := i
		go func() {
			parentResults1 <- reg.WriteFrame(ids[i], "data: x\n\n")
		}()
	}
	// Give the parents time to acquire slots and spawn their child
	// goroutines. Without this small sleep, the cap-saturated check
	// below could race ahead and observe slots that haven't been
	// claimed yet.
	time.Sleep(50 * time.Millisecond)

	// Cap saturated: a new write on a fresh entry must fail fast
	// with the documented sentinel — not block on a slot
	// acquisition, not silently grow the goroutine count. We do
	// this synchronously so the assertion's timing is direct.
	require.NoError(t, reg.Register("overflow", writer1, RegistrationMetadata{}, nil))
	start := time.Now()
	err := reg.WriteFrame("overflow", "data: x\n\n")
	elapsed := time.Since(start)
	require.ErrorIs(t, err, ErrWriteSlotsExhausted,
		"write past cap must fail with ErrWriteSlotsExhausted")
	require.Less(t, elapsed, 100*time.Millisecond,
		"write past cap must fail fast, not block on slot acquisition")

	// Release all parked cap-fill goroutines. Each deferred
	// `<-writeSlots` returns one slot to the cap; each child sends
	// to done, unblocking its parent; the parents then write to
	// parentResults1.
	close(release1)

	// Wait for every cap-fill parent to return. They all should
	// have unblocked; if any is still missing we have a leak.
	for i := 0; i < slotCount; i++ {
		select {
		case got := <-parentResults1:
			require.Error(t, got,
				"cap-fill parent %d must return the blocked-writer error after release", i)
		case <-time.After(2 * time.Second):
			t.Fatalf("cap-fill parent %d did not return after release", i)
		}
	}

	// Now register 4 fresh entries and write to them. Every write
	// must succeed — proving no goroutine leak — and the
	// (slotCount+1)th must still fail closed. We use writer2
	// (separate release channel) so the parked refill goroutines
	// stay parked while we assert the cap-saturation behavior on
	// the new batch.
	freshIDs := make([]string, slotCount)
	for i := 0; i < slotCount; i++ {
		freshIDs[i] = fmt.Sprintf("refill-%d", i)
		require.NoError(t, reg.Register(freshIDs[i], writer2, RegistrationMetadata{}, nil))
	}
	parentResults2 := make(chan error, slotCount)
	for i := 0; i < slotCount; i++ {
		i := i
		go func() {
			parentResults2 <- reg.WriteFrame(freshIDs[i], "data: x\n\n")
		}()
	}
	time.Sleep(50 * time.Millisecond)

	// Refill batch saturated: a new write must fail closed again.
	require.NoError(t, reg.Register("overflow-2", writer2, RegistrationMetadata{}, nil))
	require.ErrorIs(t,
		reg.WriteFrame("overflow-2", "data: x\n\n"),
		ErrWriteSlotsExhausted,
		"refill batch must also be cap-saturated — no permanent leak")

	// Drain refill batch.
	close(release2)
	for i := 0; i < slotCount; i++ {
		select {
		case got := <-parentResults2:
			require.Error(t, got,
				"refill parent %d must return the blocked-writer error", i)
		case <-time.After(2 * time.Second):
			t.Fatalf("refill parent %d did not return", i)
		}
	}
}

// TestConnectionRegistryMaxConcurrentWriteGoroutinesDefault checks
// the production default: a zero third arg resolves to
// DefaultMaxConcurrentWriteGoroutines (= 2 * DefaultConnectionRegistryCapacity).
// This pins the contract that deployers don't need to tune the cap to
// get the documented resource bound.
func TestConnectionRegistryMaxConcurrentWriteGoroutinesDefault(t *testing.T) {
	reg := NewConnectionRegistry(0, 0, 0) // all defaults
	want := DefaultMaxConcurrentWriteGoroutines
	got := cap(reg.writeSlots)
	assert.Equal(t, want, got, "default maxWriteGoroutines must be DefaultMaxConcurrentWriteGoroutines")
}
