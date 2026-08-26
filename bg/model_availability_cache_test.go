package bg

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestModelAvailabilityCacheSet(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := NewModelAvailabilityCache(client, time.Hour)

	ctx := context.Background()
	nextRetryAt := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	if err := cache.Set(ctx, 42, "minimax-m3", modelAvailabilityFields(
		42,
		"minimax-m3",
		"healthy_confirmed",
		true,
		"ok",
		3,
		0,
		&nextRetryAt,
		"model_probe",
	)); err != nil {
		t.Fatalf("cache.Set: %v", err)
	}

	got, err := client.HGetAll(ctx, "llmgw:avail:42:minimax-m3").Result()
	if err != nil {
		t.Fatalf("HGetAll: %v", err)
	}
	if got["state"] != "healthy_confirmed" {
		t.Fatalf("state = %q, want healthy_confirmed", got["state"])
	}
	if got["available"] != "true" {
		t.Fatalf("available = %q, want true", got["available"])
	}
	if got["source"] != "model_probe" {
		t.Fatalf("source = %q, want model_probe", got["source"])
	}
	if ttl := mr.TTL("llmgw:avail:42:minimax-m3"); ttl <= 0 {
		t.Fatalf("ttl = %v, want > 0", ttl)
	}
}
