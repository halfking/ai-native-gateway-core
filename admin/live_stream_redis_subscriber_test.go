package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLiveStreamRedisNotify_DrivesBroadcast(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
		BroadcastQueueSize: 8,
		RedisClient:        rdb,
	})

	req := LiveRequest{
		RequestID:     "req-sub-1",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-test",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}
	ctx := context.Background()
	if err := hub.store.Record(ctx, req, ""); err != nil {
		t.Fatalf("record: %v", err)
	}

	payload, err := json.Marshal(liveStreamNotifyPayload{
		RequestID: req.RequestID,
		TenantID:  req.TenantID,
	})
	if err != nil {
		t.Fatalf("marshal notify: %v", err)
	}
	hub.handleRedisNotify(string(payload))

	select {
	case got := <-hub.broadcast:
		if got.RequestID != req.RequestID {
			t.Fatalf("expected request_id %q, got %q", req.RequestID, got.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for redis notify broadcast")
	}
}

// TestLiveStreamRedisNotify_SelfEchoSkipped (OBS-DV2 #4) — a notify carrying
// this hub's own instanceID must be ignored, otherwise the hub re-enqueues its
// own Publish() output and every child_request frame is duplicated. A notify
// with no instanceID (legacy/other writers) is still processed.
func TestLiveStreamRedisNotifySelfEchoSkipped(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
		BroadcastQueueSize: 8,
		RedisClient:        rdb,
	})
	if hub.instanceID == "" {
		t.Fatal("hub should have a generated instanceID")
	}

	req := LiveRequest{
		RequestID:     "req-self-echo",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "default",
		Model:         "gpt-test",
		ModelCategory: "openai",
		ProviderCode:  "openai",
		Status:        "success",
	}
	ctx := context.Background()
	if err := hub.store.Record(ctx, req, hub.instanceID); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Same-instance notify → must be skipped (no broadcast enqueued).
	selfPayload, err := json.Marshal(liveStreamNotifyPayload{
		RequestID:  req.RequestID,
		TenantID:   req.TenantID,
		InstanceID: hub.instanceID,
	})
	if err != nil {
		t.Fatalf("marshal self notify: %v", err)
	}
	hub.handleRedisNotify(string(selfPayload))
	select {
	case got := <-hub.broadcast:
		t.Fatalf("self-echo notify should be skipped, but got broadcast request_id=%q", got.RequestID)
	case <-time.After(200 * time.Millisecond):
	}

	// Legacy notify (no instanceID) → still processed.
	legacyPayload, err := json.Marshal(liveStreamNotifyPayload{
		RequestID: req.RequestID,
		TenantID:  req.TenantID,
	})
	if err != nil {
		t.Fatalf("marshal legacy notify: %v", err)
	}
	hub.handleRedisNotify(string(legacyPayload))
	select {
	case got := <-hub.broadcast:
		if got.RequestID != req.RequestID {
			t.Fatalf("expected request_id %q, got %q", req.RequestID, got.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for legacy notify broadcast")
	}
}

// TestLiveStreamSelfEcho_NoDuplicateAcrossPhases (OBS-DV2 #4 end-to-end) —
// simulates the real persistence path: a request is written twice (in_progress
// then terminal), each write emits a Redis notify stamped with THIS hub's
// instanceID. Because the notify is the hub's own echo, handleRedisNotify must
// drop it so the two persistence phases produce exactly two local broadcasts
// (the in_progress frame and the terminal frame), NOT four (two self-echoes on
// top). This is the exact scenario that produced duplicated child_request frames
// before instanceID was introduced.
func TestLiveStreamSelfEcho_NoDuplicateAcrossPhases(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
		BroadcastQueueSize: 16,
		RedisClient:        rdb,
	})

	mkReq := func(status string) LiveRequest {
		return LiveRequest{
			RequestID:     "req-phases",
			Ts:            time.Now().UTC().Format(time.RFC3339),
			TenantID:      "default",
			Model:         "gpt-test",
			ModelCategory: "openai",
			ProviderCode:  "openai",
			Status:        status,
		}
	}

	// Phase 1: in_progress. Publish() enqueues locally AND records to Redis
	// (stamped with this hub's instanceID). The Redis notify is our own echo,
	// so handleRedisNotify must drop it.
	hub.Publish(mkReq("in_progress"))
	hub.handleRedisNotify(mustNotify(t, "req-phases", "default", hub.instanceID))

	// Phase 2: terminal — same flow.
	hub.Publish(mkReq("success"))
	hub.handleRedisNotify(mustNotify(t, "req-phases", "default", hub.instanceID))

	// Drain the broadcast channel; collect distinct (status) frames.
	seen := map[string]bool{}
	timeout := time.After(1 * time.Second)
	for len(seen) < 2 {
		select {
		case got := <-hub.broadcast:
			seen[got.Status] = true
		case <-timeout:
			t.Fatalf("expected exactly 2 distinct lifecycle frames (in_progress, success), got %v", seen)
		}
	}
	// After the 2 expected frames, no further self-echo frame should arrive.
	select {
	case got := <-hub.broadcast:
		t.Fatalf("unexpected extra broadcast (self-echo duplicate): status=%q", got.Status)
	case <-time.After(150 * time.Millisecond):
	}
	if !seen["in_progress"] || !seen["success"] {
		t.Fatalf("missing lifecycle phase frames: %v", seen)
	}
}

func mustNotify(t *testing.T, requestID, tenantID, instanceID string) string {
	t.Helper()
	b, err := json.Marshal(liveStreamNotifyPayload{
		RequestID:  requestID,
		TenantID:   tenantID,
		InstanceID: instanceID,
	})
	if err != nil {
		t.Fatalf("marshal notify: %v", err)
	}
	return string(b)
}

func TestLiveStreamRedisStore_LoadRequest(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	store := NewLiveStreamRedisStore(rdb)
	ctx := context.Background()
	req := LiveRequest{
		RequestID:     "req-load-1",
		Ts:            time.Now().UTC().Format(time.RFC3339),
		TenantID:      "tenant-a",
		Model:         "model-x",
		ModelCategory: "vendor-x",
		ProviderCode:  "provider-x",
		Status:        "in_progress",
	}
	if err := store.Record(ctx, req, ""); err != nil {
		t.Fatalf("record: %v", err)
	}

	loaded, err := store.LoadRequest(ctx, req.TenantID, req.RequestID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.RequestID != req.RequestID || loaded.Model != req.Model {
		t.Fatalf("unexpected loaded request: %#v", loaded)
	}
}
