package migration

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Preflight is read-only: SCAN + TYPE/HGETALL/ZRANGE/PTTL inspection. These
// miniredis tests prove deterministic inventory classification/checksums,
// not production Redis SCAN/TTL behavior.

func TestPreflightScanProducesDeterministicChecksum(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	legacy := "p:node:tenant-a:7:model-a"
	if err := rdb.HSet(ctx, legacy, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("legacy seed: %v", err)
	}
	k2, err := store.K2NodeKeyForTenant("p:", "tenant-b", 8, "m:x")
	if err != nil {
		t.Fatalf("k2 key: %v", err)
	}
	if err := rdb.HSet(ctx, k2, "generation", "2", "available", "0").Err(); err != nil {
		t.Fatalf("k2 seed: %v", err)
	}
	// Historical ambiguous form: it remains in inventory and makes the
	// report NO-GO; it is never assigned a guessed target.
	if err := rdb.HSet(ctx, "p:node:a:7:b:8:c", "generation", "1").Err(); err != nil {
		t.Fatalf("ambiguous seed: %v", err)
	}

	p := NewPreflight(rdb, "p:")
	first, err := p.Scan(ctx)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	second, err := p.Scan(ctx)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if first.Checksum == "" || first.Checksum != second.Checksum {
		t.Fatalf("checksum first=%q second=%q: same keyspace must have deterministic inventory checksum", first.Checksum, second.Checksum)
	}
	if first.Migratable != 1 || first.CanonicalPresent != 1 || first.Ambiguous != 1 || first.Go() {
		t.Fatalf("report = %+v, want migratable=1 canonical=1 ambiguous=1 NO-GO", first)
	}
	if len(first.Entries) != 3 {
		t.Fatalf("entry count=%d, want 3", len(first.Entries))
	}
	for _, e := range first.Entries {
		if e.Class == ClassAmbiguous && e.TargetKey != "" {
			t.Fatalf("ambiguous entry must not have a target key: %+v", e)
		}
	}
}

func TestPreflightCapturesHashChecksumAndPTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	key := "p:node:tenant-a:7:model-a"
	if err := rdb.HSet(ctx, key, "z", "last", "a", "first", "generation", "7").Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := rdb.PExpire(ctx, key, 10_000_000_000).Err(); err != nil {
		t.Fatalf("pexpire: %v", err)
	}
	r, err := NewPreflight(rdb, "p:").Scan(ctx)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(r.Entries) != 1 {
		t.Fatalf("entries=%d", len(r.Entries))
	}
	e := r.Entries[0]
	if e.Type != "hash" || e.FieldChecksum == "" || e.Generation != 7 || e.PTTLMillis <= 0 {
		t.Fatalf("entry=%+v", e)
	}
	// The checksum depends on sorted field name/value pairs, not Redis's
	// unordered HGETALL response. Re-seeding equivalent fields in a fresh
	// server yields the same exact checksum.
	mr2 := miniredis.RunT(t)
	rdb2 := redis.NewClient(&redis.Options{Addr: mr2.Addr()})
	if err := rdb2.HSet(ctx, key, "generation", "7", "a", "first", "z", "last").Err(); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	r2, err := NewPreflight(rdb2, "p:").Scan(ctx)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if r2.Entries[0].FieldChecksum != e.FieldChecksum {
		t.Fatalf("checksum order dependence: %q != %q", r2.Entries[0].FieldChecksum, e.FieldChecksum)
	}
}
