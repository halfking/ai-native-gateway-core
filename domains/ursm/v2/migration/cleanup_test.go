package migration

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCleanupDeletesOnlyExactMatchingLedgerKey(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	eligible := "p:node:tenant-a:7:model-a"
	other := "p:node:tenant-b:8:model-b"
	if err := rdb.HSet(ctx, eligible, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("eligible seed: %v", err)
	}
	if err := rdb.HSet(ctx, other, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("other seed: %v", err)
	}
	e := preflightOne(t, rdb, eligible)
	result, err := NewCleaner(rdb).DeleteExact(ctx, e)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.Status != CleanupDeleted || mr.Exists(eligible) || !mr.Exists(other) {
		t.Fatalf("cleanup result=%+v eligible=%v other=%v", result, mr.Exists(eligible), mr.Exists(other))
	}
}

func TestCleanupSkipsChangedOrIneligibleLedgerKey(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	key := "p:node:tenant-a:7:model-a"
	if err := rdb.HSet(ctx, key, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := preflightOne(t, rdb, key)
	if err := rdb.HSet(ctx, key, "available", "0").Err(); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	result, err := NewCleaner(rdb).DeleteExact(ctx, e)
	if err != nil {
		t.Fatalf("cleanup changed: %v", err)
	}
	if result.Status != CleanupSkippedChecksum || !mr.Exists(key) {
		t.Fatalf("changed cleanup=%+v exists=%v", result, mr.Exists(key))
	}
	e.Class = ClassAmbiguous
	result, err = NewCleaner(rdb).DeleteExact(ctx, e)
	if err != nil {
		t.Fatalf("cleanup ineligible: %v", err)
	}
	if result.Status != CleanupSkippedIneligible || !mr.Exists(key) {
		t.Fatalf("ineligible cleanup=%+v exists=%v", result, mr.Exists(key))
	}
}

func TestCleanupIsPausableAndResumable(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	keys := []string{"p:node:tenant-a:7:model-a", "p:node:tenant-b:8:model-b"}
	entries := make([]Entry, 0, len(keys))
	for _, key := range keys {
		if err := rdb.HSet(ctx, key, "generation", "1", "available", "1").Err(); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
		entries = append(entries, preflightOne(t, rdb, key))
	}
	cleaner := NewCleaner(rdb)
	first, err := cleaner.DeleteBatch(ctx, entries, 1)
	if err != nil || first.Deleted != 1 || first.Next != 1 {
		t.Fatalf("first batch=%+v err=%v", first, err)
	}
	second, err := cleaner.DeleteBatch(ctx, entries[first.Next:], 1)
	if err != nil || second.Deleted != 1 || mr.Exists(keys[0]) || mr.Exists(keys[1]) {
		t.Fatalf("second batch=%+v err=%v exists=%v/%v", second, err, mr.Exists(keys[0]), mr.Exists(keys[1]))
	}
}
