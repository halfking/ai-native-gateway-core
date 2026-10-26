package bg

import (
	"context"
	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

func TestAvailabilityRejectsOlderObservationAndClearsRetry(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewModelAvailabilityCache(client, time.Hour)
	ctx := context.Background()
	now := time.Now().UTC()
	retry := now.Add(time.Hour)
	fields := modelAvailabilityFields(42, "minimax-m3", "broken_confirmed", false, "timeout", 0, 3, &retry, "probe")
	fields["updated_at"] = now.Add(-time.Minute).Format(time.RFC3339Nano)
	if err := cache.Set(ctx, 42, "minimax-m3", fields); err != nil {
		t.Fatal(err)
	}
	fresh := modelAvailabilityFields(42, "minimax-m3", "healthy_confirmed", true, "", 1, 0, nil, "probe")
	fresh["updated_at"] = now.Format(time.RFC3339Nano)
	if err := cache.Set(ctx, 42, "minimax-m3", fresh); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, 42, "minimax-m3", fields); err != nil {
		t.Fatal(err)
	}
	got, err := NewModelAvailabilityReader(client).Read(ctx, 42, "minimax-m3")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Available || got.ConsecutiveFailures != 0 || got.NextRetryAt != nil {
		t.Fatalf("stale result replaced recovery or retained retry: %+v", got)
	}
}
