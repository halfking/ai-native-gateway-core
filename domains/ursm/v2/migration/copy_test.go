package migration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func preflightOne(t *testing.T, rdb *redis.Client, key string) Entry {
	t.Helper()
	r, err := NewPreflight(rdb, "p:").Scan(context.Background())
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	for _, e := range r.Entries {
		if e.SourceKey == key {
			return e
		}
	}
	t.Fatalf("preflight has no entry for %q", key)
	return Entry{}
}

func TestCopyPreservesRemainingPTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	source := "p:node:tenant-a:7:model-a"
	if err := rdb.HSet(ctx, source, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := rdb.PExpire(ctx, source, 10*time.Second).Err(); err != nil {
		t.Fatalf("pexpire: %v", err)
	}
	e := preflightOne(t, rdb, source)
	mr.FastForward(2 * time.Second)
	result, err := NewCopier(rdb, "p:").CopyHash(ctx, e)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if result.Status != CopyApplied {
		t.Fatalf("copy status=%s", result.Status)
	}
	sourceTTL, err := rdb.PTTL(ctx, source).Result()
	if err != nil {
		t.Fatalf("source pttl: %v", err)
	}
	targetTTL, err := rdb.PTTL(ctx, e.TargetKey).Result()
	if err != nil {
		t.Fatalf("target pttl: %v", err)
	}
	if targetTTL <= 0 || targetTTL > sourceTTL {
		t.Fatalf("target PTTL=%v source PTTL=%v: target must not outlive source", targetTTL, sourceTTL)
	}
}

func TestCopyPreservesPersistentSource(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	source := "p:node:tenant-a:7:model-a"
	if err := rdb.HSet(ctx, source, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := preflightOne(t, rdb, source)
	if _, err := NewCopier(rdb, "p:").CopyHash(ctx, e); err != nil {
		t.Fatalf("copy: %v", err)
	}
	ttl, err := rdb.PTTL(ctx, e.TargetKey).Result()
	if err != nil || ttl != -1*time.Nanosecond {
		t.Fatalf("target PTTL=%v err=%v, want persistent -1", ttl, err)
	}
}

func TestCopySkipsExpiredSource(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	e := Entry{SourceKey: "p:node:tenant-a:7:model-a", TargetKey: "p:node:k2:dGVuYW50LWE:7:bW9kZWwtYQ", Class: ClassMigratable, Type: "hash", Generation: 1, FieldChecksum: "unused"}
	result, err := NewCopier(rdb, "p:").CopyHash(ctx, e)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if result.Status != CopySkippedExpired {
		t.Fatalf("status=%s, want skipped_expired", result.Status)
	}
	if mr.Exists(e.TargetKey) {
		t.Fatal("expired source must not create target")
	}
}

func TestCopyFencesGenerationAndIsIdempotent(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	source := "p:node:tenant-a:7:model-a"
	if err := rdb.HSet(ctx, source, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := preflightOne(t, rdb, source)
	// The source changed after preflight: generation fence must stop copy.
	if err := rdb.HSet(ctx, source, "generation", "2").Err(); err != nil {
		t.Fatalf("advance source generation: %v", err)
	}
	if _, err := NewCopier(rdb, "p:").CopyHash(ctx, e); !errors.Is(err, ErrGenerationChanged) {
		t.Fatalf("copy err=%v, want ErrGenerationChanged", err)
	}
	if mr.Exists(e.TargetKey) {
		t.Fatal("generation mismatch must not create target")
	}
	// Fresh inventory copies once; repeating the exact ledger row is a no-op.
	e = preflightOne(t, rdb, source)
	first, err := NewCopier(rdb, "p:").CopyHash(ctx, e)
	if err != nil || first.Status != CopyApplied {
		t.Fatalf("first copy=%+v err=%v", first, err)
	}
	second, err := NewCopier(rdb, "p:").CopyHash(ctx, e)
	if err != nil || second.Status != CopyAlreadyApplied {
		t.Fatalf("second copy=%+v err=%v", second, err)
	}
}

func TestCopyNeverOverwritesExistingCanonicalTarget(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	source := "p:node:tenant-a:7:model-a"
	if err := rdb.HSet(ctx, source, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("source: %v", err)
	}
	e := preflightOne(t, rdb, source)
	if err := rdb.HSet(ctx, e.TargetKey, "generation", "99", "available", "0").Err(); err != nil {
		t.Fatalf("target: %v", err)
	}
	if _, err := NewCopier(rdb, "p:").CopyHash(ctx, e); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("copy err=%v, want ErrTargetConflict", err)
	}
	if got := mr.HGet(e.TargetKey, "generation"); got != "99" {
		t.Fatalf("target generation=%q: copy must not overwrite live canonical state", got)
	}
}
