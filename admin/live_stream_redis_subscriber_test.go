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
	if err := hub.store.Record(ctx, req); err != nil {
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
	if err := store.Record(ctx, req); err != nil {
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
