package bg

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestBackfill(cache *ModelAvailabilityCache, reader *ModelAvailabilityReader) *AvailabilityCacheBackfill {
	return &AvailabilityCacheBackfill{
		cache:     cache,
		reader:    reader,
		batchSize: 200,
		lookback:  time.Hour,
		interval:  time.Minute,
		done:      make(chan struct{}),
	}
}

// TestAvailabilityCacheBackfillFieldGeneration pins the Redis hash
// payload shape produced by the backfill writer. This is the contract
// the admin /api/admin/probe/cache-state endpoint and the routing
// MaybeExitSuspicious hook both consume, so changes here have ripple
// effects across both admin and hot-path code.
func TestAvailabilityCacheBackfillFieldGeneration(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := NewModelAvailabilityCache(client, time.Hour)
	reader := NewModelAvailabilityReader(client)
	w := newTestBackfill(cache, reader)

	ctx := context.Background()
	nextRetry := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	if err := w.cache.Set(ctx, 42, "minimax-m3", ModelAvailabilityFields(
		42, "minimax-m3", "healthy_confirmed", true, "ok", 3, 0,
		&nextRetry, "backfill",
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
	if got["source"] != "backfill" {
		t.Fatalf("source = %q, want backfill", got["source"])
	}
	if got["credential_id"] != "42" {
		t.Fatalf("credential_id = %q, want 42", got["credential_id"])
	}
	if got["raw_model_name"] != "minimax-m3" {
		t.Fatalf("raw_model_name = %q, want minimax-m3", got["raw_model_name"])
	}
}

// TestAvailabilityCacheBackfillSkipsFreshEntries verifies that the
// shouldRefresh guard refuses to overwrite a Redis entry that was
// updated within the lookback window.
func TestAvailabilityCacheBackfillSkipsFreshEntries(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := NewModelAvailabilityCache(client, time.Hour)
	reader := NewModelAvailabilityReader(client)
	w := newTestBackfill(cache, reader)

	// Seed a fresh entry with the current timestamp.
	ctx := context.Background()
	nextRetry := time.Now().Add(2 * time.Minute)
	if err := cache.Set(ctx, 42, "minimax-m3", ModelAvailabilityFields(
		42, "minimax-m3", "healthy_confirmed", true, "ok", 3, 0,
		&nextRetry, "model_probe",
	)); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// shouldRefresh must report false for an entry we just wrote.
	snap, err := reader.Read(ctx, 42, "minimax-m3")
	if err != nil || snap == nil {
		t.Fatalf("Read: snap=%v err=%v", snap, err)
	}
	if shouldRefresh(snap.UpdatedAt, w.lookback) {
		t.Fatalf("shouldRefresh returned true for fresh entry (updated_at=%v)", snap.UpdatedAt)
	}
}

// TestAvailabilityCacheBackfillShouldRefresh pins the staleness boundary.
// An entry whose UpdatedAt is older than the lookback window must be
// treated as stale so a backfill can refresh it.
func TestAvailabilityCacheBackfillShouldRefresh(t *testing.T) {
	staleUpdatedAt := time.Now().Add(-3 * time.Hour)
	freshUpdatedAt := time.Now().Add(-5 * time.Minute)

	lookback := time.Hour
	if !shouldRefresh(&staleUpdatedAt, lookback) {
		t.Fatal("expected stale entry to refresh")
	}
	if shouldRefresh(&freshUpdatedAt, lookback) {
		t.Fatal("expected fresh entry NOT to refresh")
	}
	if !shouldRefresh(nil, lookback) {
		t.Fatal("expected nil UpdatedAt to refresh")
	}
}

// TestIsUnavailableState covers the state→available mapping that the
// backfill writer uses to populate the available flag in Redis.
func TestIsUnavailableState(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{"healthy_confirmed", false},
		{"healthy", false},
		{"recovering", false},
		{"unknown", false},
		{"broken_confirmed", true},
		{"failing", true},
		{"unreachable", true},
		{"", false},
	}
	for _, c := range cases {
		if got := isUnavailable(c.state); got != c.want {
			t.Fatalf("isUnavailable(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}
