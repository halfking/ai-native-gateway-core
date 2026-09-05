package dbdegradation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// helper: build a BackupRecord for tests.
func mkRec(key, payload string) BackupRecord {
	return BackupRecord{
		Type:      "request_log",
		Timestamp: time.Now().UTC(),
		RecordKey: key,
		Payload:   json.RawMessage(payload),
	}
}

func TestRingBuffer_FIFO(t *testing.T) {
	rb := NewRingBuffer(10)

	for i := 0; i < 5; i++ {
		_ = rb.WriteRequestLog(context.Background(), string(rune('a'+i)), mkRec(string(rune('a'+i)), `"v"`))
	}

	got := rb.Dump()
	if len(got) != 5 {
		t.Fatalf("Dump len = %d, want 5", len(got))
	}
	for i, rec := range got {
		want := string(rune('a' + i))
		if rec.RecordKey != want {
			t.Errorf("Dump[%d].RecordKey = %q, want %q", i, rec.RecordKey, want)
		}
	}
}

func TestRingBuffer_Cap_OverwriteOldest(t *testing.T) {
	rb := NewRingBuffer(3)

	// Fill: a, b, c
	for _, k := range []string{"a", "b", "c"} {
		_ = rb.WriteRequestLog(context.Background(), k, mkRec(k, `"v"`))
	}
	// Overwrite: d, e → buffer holds [c, d, e], dropped=2
	for _, k := range []string{"d", "e"} {
		_ = rb.WriteRequestLog(context.Background(), k, mkRec(k, `"v"`))
	}

	got := rb.Dump()
	if len(got) != 3 {
		t.Fatalf("Dump len = %d, want 3", len(got))
	}
	wantKeys := []string{"c", "d", "e"}
	for i, rec := range got {
		if rec.RecordKey != wantKeys[i] {
			t.Errorf("Dump[%d].RecordKey = %q, want %q", i, rec.RecordKey, wantKeys[i])
		}
	}

	stats := rb.Stats()
	if stats.Dropped != 2 {
		t.Errorf("Stats.Dropped = %d, want 2", stats.Dropped)
	}
	if stats.Writes != 5 {
		t.Errorf("Stats.Writes = %d, want 5", stats.Writes)
	}
	if stats.Size != 3 {
		t.Errorf("Stats.Size = %d, want 3", stats.Size)
	}
	if stats.Capacity != 3 {
		t.Errorf("Stats.Capacity = %d, want 3", stats.Capacity)
	}
}

func TestRingBuffer_BackupWriterInterface(t *testing.T) {
	var _ BackupWriter = (*RingBuffer)(nil) // compile-time check

	rb := NewRingBuffer(5)
	ctx := context.Background()

	if err := rb.WriteRequestLog(ctx, "k1", mkRec("k1", `"p1"`)); err != nil {
		t.Fatalf("WriteRequestLog: %v", err)
	}
	if err := rb.WriteRequestWAL(ctx, "k2", mkRec("k2", `"p2"`)); err != nil {
		t.Fatalf("WriteRequestWAL: %v", err)
	}

	got := rb.Dump()
	if len(got) != 2 {
		t.Fatalf("Dump len = %d, want 2", len(got))
	}
	// Verify payload type was correctly attached via WriteGeneric.
	if got[0].Type != "request_log" {
		t.Errorf("got[0].Type = %q, want request_log", got[0].Type)
	}
	if got[1].Type != "request_wal" {
		t.Errorf("got[1].Type = %q, want request_wal", got[1].Type)
	}
}

func TestRingBuffer_Concurrent_NoRace(t *testing.T) {
	rb := NewRingBuffer(100)
	var wg sync.WaitGroup
	var writes uint64

	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				key := string(rune('A'+gid)) + "_" + string(rune('0'+i%10))
				_ = rb.WriteRequestLog(context.Background(), key, mkRec(key, `"v"`))
				atomic.AddUint64(&writes, 1)
			}
		}(g)
	}

	// Concurrent dumps
	stop := make(chan struct{})
	dumpDone := make(chan struct{})
	go func() {
		defer close(dumpDone)
		for {
			select {
			case <-stop:
				return
			default:
				_ = rb.Dump()
				_ = rb.Stats()
			}
		}
	}()

	wg.Wait()
	close(stop)
	<-dumpDone

	// 10 goroutines × 50 writes = 500 total writes
	if atomic.LoadUint64(&writes) != 500 {
		t.Errorf("writes = %d, want 500", writes)
	}
	stats := rb.Stats()
	if stats.Writes != 500 {
		t.Errorf("Stats.Writes = %d, want 500", stats.Writes)
	}
	if stats.Size > 100 {
		t.Errorf("Stats.Size = %d, exceeds cap 100", stats.Size)
	}
}

func TestRingBuffer_Clear(t *testing.T) {
	rb := NewRingBuffer(5)
	for _, k := range []string{"a", "b", "c"} {
		_ = rb.WriteRequestLog(context.Background(), k, mkRec(k, `"v"`))
	}

	if got := rb.Dump(); len(got) != 3 {
		t.Fatalf("pre-clear Dump len = %d, want 3", len(got))
	}

	rb.Clear()

	if got := rb.Dump(); len(got) != 0 {
		t.Errorf("post-clear Dump len = %d, want 0", len(got))
	}
	stats := rb.Stats()
	if stats.Size != 0 {
		t.Errorf("post-clear Stats.Size = %d, want 0", stats.Size)
	}
	if stats.Capacity != 5 {
		t.Errorf("post-clear Stats.Capacity = %d, want 5", stats.Capacity)
	}
	// dropped counter is monotonic (records the cumulative cost, useful for alerts)
	if stats.Dropped != 0 {
		t.Errorf("post-clear Stats.Dropped = %d, want 0 (no overwrites occurred)", stats.Dropped)
	}
}

func TestRingBuffer_Replay_Success(t *testing.T) {
	rb := NewRingBuffer(5)
	for _, k := range []string{"a", "b", "c"} {
		_ = rb.WriteRequestLog(context.Background(), k, mkRec(k, `"v"`))
	}

	var replayed []string
	var mu sync.Mutex
	replayFn := func(ctx context.Context, rec BackupRecord) error {
		mu.Lock()
		replayed = append(replayed, rec.RecordKey)
		mu.Unlock()
		return nil
	}

	replayed2, failed, err := rb.Replay(context.Background(), 0, replayFn)
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
	if replayed2 != 3 {
		t.Errorf("replayed = %d, want 3", replayed2)
	}
	if len(replayed) != 3 {
		t.Errorf("replayed records = %d, want 3", len(replayed))
	}
	// Buffer cleared after replay
	if rb.Stats().Size != 0 {
		t.Errorf("after replay Stats.Size = %d, want 0", rb.Stats().Size)
	}
}

func TestRingBuffer_Replay_Failure_Requeued(t *testing.T) {
	rb := NewRingBuffer(5)
	for _, k := range []string{"a", "b", "c"} {
		_ = rb.WriteRequestLog(context.Background(), k, mkRec(k, `"v"`))
	}

	replayFn := func(ctx context.Context, rec BackupRecord) error {
		if rec.RecordKey == "b" {
			return errors.New("simulated failure")
		}
		return nil
	}

	replayed, failed, err := rb.Replay(context.Background(), 0, replayFn)
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if replayed != 2 {
		t.Errorf("replayed = %d, want 2", replayed)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	// Failed entries are requeued (WAL semantics): "b" is still buffered.
	if rb.Stats().Size != 1 {
		t.Errorf("after failed replay Stats.Size = %d, want 1 (requeued failure)", rb.Stats().Size)
	}
	// Requeue does not loop: replay is only operator-triggered, so the
	// retried record appears exactly once.
	first := rb.Dump()
	if len(first) != 1 || first[0].RecordKey != "b" {
		t.Errorf("Dump = %+v, want single requeued record b", first)
	}
}

func TestRingBuffer_Replay_PartialKeepsSurvivors(t *testing.T) {
	// Regression: partial replay previously read past the valid region and
	// overwrote the un-replayed tail with garbage.
	rb := NewRingBuffer(8)
	keys := []string{"a", "b", "c", "d", "e"}
	for _, k := range keys {
		_ = rb.WriteRequestLog(context.Background(), k, mkRec(k, `"v"`))
	}
	replayFn := func(ctx context.Context, rec BackupRecord) error { return nil }
	if _, _, err := rb.Replay(context.Background(), 2, replayFn); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	dumped := rb.Dump()
	if len(dumped) != 3 {
		t.Fatalf("Dump length = %d, want 3", len(dumped))
	}
	for i, want := range []string{"c", "d", "e"} {
		if dumped[i].RecordKey != want {
			t.Fatalf("Dump[%d].RecordKey = %q, want %q (partial replay corrupted tail)", i, dumped[i].RecordKey, want)
		}
	}
}

func TestRingBuffer_Replay_Limit(t *testing.T) {
	rb := NewRingBuffer(10)
	for i := 0; i < 10; i++ {
		k := string(rune('a' + i))
		_ = rb.WriteRequestLog(context.Background(), k, mkRec(k, `"v"`))
	}

	replayFn := func(ctx context.Context, rec BackupRecord) error { return nil }
	replayed, failed, err := rb.Replay(context.Background(), 3, replayFn)
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if replayed != 3 {
		t.Errorf("replayed = %d, want 3 (limited)", replayed)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
	// Remaining 7 entries still in buffer
	if rb.Stats().Size != 7 {
		t.Errorf("after limited replay Stats.Size = %d, want 7", rb.Stats().Size)
	}
}

func TestRingBuffer_Replay_Empty(t *testing.T) {
	rb := NewRingBuffer(5)
	called := 0
	replayFn := func(ctx context.Context, rec BackupRecord) error {
		called++
		return nil
	}
	replayed, failed, err := rb.Replay(context.Background(), 0, replayFn)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if replayed != 0 || failed != 0 {
		t.Errorf("replayed=%d failed=%d, want 0/0", replayed, failed)
	}
	if called != 0 {
		t.Errorf("replayFn called %d times, want 0", called)
	}
}

func TestRingBuffer_ZeroOrNegativeCap(t *testing.T) {
	rb := NewRingBuffer(0)
	if rb.Stats().Capacity != 1 {
		t.Errorf("NewRingBuffer(0).Capacity = %d, want 1 (sane minimum)", rb.Stats().Capacity)
	}
	if err := rb.WriteRequestLog(context.Background(), "x", mkRec("x", `"v"`)); err != nil {
		t.Errorf("WriteRequestLog on min-cap buffer: %v", err)
	}
	if rb.Stats().Size != 1 {
		t.Errorf("after write Stats.Size = %d, want 1", rb.Stats().Size)
	}
}
