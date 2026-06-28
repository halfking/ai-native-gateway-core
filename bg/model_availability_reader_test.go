package bg

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestModelAvailabilityReaderRead(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := NewModelAvailabilityCache(client, time.Hour)
	reader := NewModelAvailabilityReader(client)

	ctx := context.Background()
	nextRetryAt := time.Now().Add(3 * time.Minute).UTC().Truncate(time.Second)
	if err := cache.Set(ctx, 7, "glm-5.2", modelAvailabilityFields(
		7,
		"glm-5.2",
		"healthy_confirmed",
		true,
		"ok",
		2,
		0,
		&nextRetryAt,
		"model_probe",
	)); err != nil {
		t.Fatalf("cache.Set: %v", err)
	}

	snapshot, err := reader.Read(ctx, 7, "glm-5.2")
	if err != nil {
		t.Fatalf("reader.Read: %v", err)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if snapshot.State != "healthy_confirmed" {
		t.Fatalf("state = %q", snapshot.State)
	}
	if !snapshot.Available {
		t.Fatal("expected available=true")
	}
	if !snapshot.LoadedFromCache {
		t.Fatal("expected LoadedFromCache=true")
	}
}

func TestModelAvailabilityReaderReadByModel(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := NewModelAvailabilityCache(client, time.Hour)
	reader := NewModelAvailabilityReader(client)

	ctx := context.Background()
	if err := cache.Set(ctx, 1, "glm-5.2", modelAvailabilityFields(1, "glm-5.2", "healthy_confirmed", true, "ok", 3, 0, nil, "model_probe")); err != nil {
		t.Fatalf("cache.Set #1: %v", err)
	}
	if err := cache.Set(ctx, 2, "glm-5.2", modelAvailabilityFields(2, "glm-5.2", "broken_confirmed", false, "http_4xx", 0, 3, nil, "model_probe")); err != nil {
		t.Fatalf("cache.Set #2: %v", err)
	}

	rows, err := reader.ReadByModel(ctx, "glm-5.2")
	if err != nil {
		t.Fatalf("ReadByModel: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
}
