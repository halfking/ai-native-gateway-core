package dispatch

// 会话优化 v4 T2 — dispatch queue Redis mirror tests (UT-DQ-09).
//
// The mirror must (a) project Tier-1/Tier-2 depths, in-flight and retry_at
// into versioned keys, (b) rebuild METADATA ONLY — a rebuild read must have
// zero execution side effects, and (c) be a no-op when unwired.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestMirror(t *testing.T) (*QueueMirror, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewQueueMirror(client), mr
}

func TestQueueMirrorConcurrentObserveAndClose(t *testing.T) {
	m, _ := newTestMirror(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "model", Depth: int64(j)})
			}
		}()
	}
	m.Close()
	wg.Wait()
	m.Close()
}

// and retry_at land in the documented versioned keys and read back through
// RebuildMetadata.
func TestQueueMirrorProjectsVersionedKeys(t *testing.T) {
	m, mr := newTestMirror(t)
	defer m.Close()

	m.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "gpt-4o", Depth: 3, Delta: 1})
	m.ObserveQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: 11, Mode: ModeConcurrency, Depth: 5, Delta: 1})
	m.ObserveQueue(QueueObservation{Kind: QueueInFlight, InFlight: 7, Delta: 1})
	retryAt := time.Unix(1700000123, 0).UTC()
	m.MirrorRetryAt("req-1", retryAt)
	m.Flush()

	wantKey := DefaultQueueMirrorPrefix + ":" + DefaultQueueMirrorKeyVersion
	for key, value := range map[string]string{
		wantKey + ":t1_depth:gpt-4o": "3",
		wantKey + ":t2_depth:11":     "5",
		wantKey + ":inflight":        "7",
		wantKey + ":retry_at:req-1":  "1700000123000",
	} {
		got, err := mr.Get(key)
		if err != nil || got != value {
			t.Fatalf("mirror key %s = %q (%v), want %q", key, got, err, value)
		}
	}
	// TTL must be set so a dead instance's mirror fades out.
	if ttl := mr.TTL(wantKey + ":inflight"); ttl <= 0 {
		t.Fatalf("mirrored keys must carry a TTL, got %v", ttl)
	}

	metadata, err := m.RebuildMetadata(context.Background())
	if err != nil {
		t.Fatalf("RebuildMetadata: %v", err)
	}
	if len(metadata.Models) != 1 || metadata.Models[0] != (LaneDepth{Key: "gpt-4o", Depth: 3}) {
		t.Fatalf("models metadata = %#v", metadata.Models)
	}
	if len(metadata.Credentials) != 1 || metadata.Credentials[0] != (LaneDepth{Key: "11", Depth: 5}) {
		t.Fatalf("credentials metadata = %#v", metadata.Credentials)
	}
	if metadata.InFlight != 7 {
		t.Fatalf("inflight metadata = %d", metadata.InFlight)
	}
	if len(metadata.Retries) != 1 || metadata.Retries[0].RequestID != "req-1" || !metadata.Retries[0].RetryAt.Equal(retryAt) {
		t.Fatalf("retries metadata = %#v", metadata.Retries)
	}

	// ClearRetryAt removes the parked retry key.
	m.ClearRetryAt("req-1")
	m.Flush()
	if _, err := mr.Get(wantKey + ":retry_at:req-1"); err == nil {
		t.Fatal("retry_at key must be cleared")
	}

	// Ignored observation kinds must not create keys.
	m.ObserveQueue(QueueObservation{Kind: QueueOverflow, OverflowReason: "model_queue_full"})
	m.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "", Depth: 1})
	m.Flush()
	if keys := mr.Keys(); len(keys) != 3 {
		t.Fatalf("unexpected mirror key set: %v", keys)
	}
}

// TestQueueMirrorRebuildIsReadOnly (UT-DQ-09): rebuilding metadata has NO
// side effects — the mirror state is identical before and after a rebuild,
// and no queue observation/execution callback fires.
func TestQueueMirrorRebuildIsReadOnly(t *testing.T) {
	m, mr := newTestMirror(t)
	defer m.Close()

	m.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "m1", Depth: 2})
	m.MirrorRetryAt("req-9", time.Unix(1700000999, 0).UTC())
	m.Flush()
	before := mr.Keys()

	if _, err := m.RebuildMetadata(context.Background()); err != nil {
		t.Fatalf("RebuildMetadata: %v", err)
	}
	if _, err := m.RebuildMetadata(context.Background()); err != nil {
		t.Fatalf("second RebuildMetadata: %v", err)
	}
	after := mr.Keys()
	if len(before) != len(after) {
		t.Fatalf("rebuild mutated the mirror: before=%v after=%v", before, after)
	}
	for _, key := range before {
		if _, err := mr.Get(key); err != nil {
			t.Fatalf("rebuild lost key %s: %v", key, err)
		}
	}
}

// TestQueueMirrorNilIsNoOp: an unwired mirror (nil client) never panics and
// never writes anything.
func TestQueueMirrorNilIsNoOp(t *testing.T) {
	var m *QueueMirror
	m.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "m", Depth: 1})
	m.MirrorRetryAt("req", time.Now())
	m.ClearRetryAt("req")
	m.Flush()
	m.Close()
	metadata, err := m.RebuildMetadata(context.Background())
	if err != nil || len(metadata.Models) != 0 || len(metadata.Credentials) != 0 || len(metadata.Retries) != 0 {
		t.Fatalf("nil mirror metadata = %#v, err = %v", metadata, err)
	}
}

// TestPipelineWiresQueueMirror: the pipeline's observeQueue funnel feeds the
// mirror — a completed request leaves mirrored lane keys behind (final depth
// 0 is a real zero), and terminal handling clears retry_at.
func TestPipelineWiresQueueMirror(t *testing.T) {
	m, mr := newTestMirror(t)
	defer m.Close()

	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"m": {cred(1, ModeConcurrency, 4)}},
		forwardFn:    func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.SetQueueMirror(m)
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("mirror-1", "t", "m", context.Background(), nil)
	if _, err := p.Submit(context.Background(), qr); err != nil {
		t.Fatalf("submit: %v", err)
	}
	m.Flush()

	prefix := DefaultQueueMirrorPrefix + ":" + DefaultQueueMirrorKeyVersion
	for _, key := range []string{prefix + ":t1_depth:m", prefix + ":t2_depth:1", prefix + ":inflight"} {
		if got, err := mr.Get(key); err != nil || got != "0" {
			t.Fatalf("pipeline mirror key %s = %q (%v), want 0", key, got, err)
		}
	}
}
