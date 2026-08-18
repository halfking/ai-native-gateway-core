package migration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// seedCleanedItem inserts an Item in the StatusCopied state plus writes the
// matching source key into Redis, returning the constructed Item.
func seedCleanedItem(t *testing.T, ledger *Ledger, rdb *redis.Client, source, canonical string, fields map[string]string) Item {
	t.Helper()
	it := Item{
		SourceKey:      source,
		KeyType:        "hash",
		CanonicalKey:   canonical,
		SchemaSource:   "legacy",
		Classification: ClassificationMigratable,
		Status:         StatusCopied,
		FieldChecksum:  fieldChecksum(fields),
		Generation:     1,
		ScanRunID:      "test",
	}
	if err := ledger.Append(it); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if err := rdb.HSet(context.Background(), canonical, fields).Err(); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
	return it
}

// TestCleanupDeletesOnlyLedgerKeys writes some legacy keys (in ledger) and
// some unrelated keys (NOT in ledger). Cleanup must delete only the former.
func TestCleanupDeletesOnlyLedgerKeys(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	const (
		source1 = migPrefix + "node:a:7:b:8:c"
		canon1  = migPrefix + "node:k2:YQ:7:Yjo4OmM"
		source2 = migPrefix + "node:b:8:c:d"
		canon2  = migPrefix + "node:k2:Yg:8:Y2M6ZA"
		other   = migPrefix + "node:other:1:m"
	)
	fields := legacyHashFieldsAsStrings()
	seedCleanedItem(t, ledger, rdb, source1, canon1, fields)
	seedCleanedItem(t, ledger, rdb, source2, canon2, fields)
	if err := rdb.HSet(context.Background(), other, fields).Err(); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	cl := &Cleanup{Prefix: migPrefix, Ledger: ledger, RDB: rdb,
		Opts: CleanupOptions{Mode: ModeDual}}
	results, err := cl.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 ledger rows", len(results))
	}
	deleted := 0
	for _, r := range results {
		if r.Status == CleanupStatusDeleted {
			deleted++
		}
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (results=%+v)", deleted, results)
	}
	// source1/source2 must be gone, canonical keys untouched, other key
	// must still exist.
	for _, k := range []string{source1, source2} {
		exists, _ := rdb.Exists(context.Background(), k).Result()
		if exists != 0 {
			t.Fatalf("expected %s deleted", k)
		}
	}
	for _, k := range []string{canon1, canon2, other} {
		exists, _ := rdb.Exists(context.Background(), k).Result()
		if exists != 1 {
			t.Fatalf("expected %s present", k)
		}
	}
}

// TestCleanupRollbackPreservesEvidence: cleanup in ModeLegacy must refuse
// without --force-rollback; the source key remains.
func TestCleanupRollbackPreservesEvidence(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	source := migPrefix + "node:a:7:b:8:c"
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	fields := legacyHashFieldsAsStrings()
	seedCleanedItem(t, ledger, rdb, source, canonical, fields)

	cl := &Cleanup{Prefix: migPrefix, Ledger: ledger, RDB: rdb,
		Opts: CleanupOptions{Mode: ModeLegacy}}
	if _, err := cl.Cleanup(context.Background()); err == nil {
		t.Fatal("cleanup must refuse in legacy mode without --force-rollback")
	}
	exists, _ := rdb.Exists(context.Background(), source).Result()
	if exists != 1 {
		t.Fatal("source key must be preserved on rollback")
	}
	// --force-rollback should allow the delete.
	cl.Opts.ForceRollback = true
	if _, err := cl.Cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup with force-rollback: %v", err)
	}
	exists, _ = rdb.Exists(context.Background(), source).Result()
	if exists != 0 {
		t.Fatal("source key must be deleted when force-rollback=true")
	}
}

// TestCleanupRateLimited: rate=2 key/s, cleanup 3 items, expect >=1.0s
// elapsed (allow scheduler slack of ±200ms). We seed exactly three rows so
// the math is trivial.
func TestCleanupRateLimited(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	fields := legacyHashFieldsAsStrings()
	for i := 0; i < 3; i++ {
		src := fmt.Sprintf("%snode:t:%d:7:m%d", migPrefix, i, i)
		can := fmt.Sprintf("%snode:k2:dA%c:7:bX%c", migPrefix, byte('A'+i), byte('0'+i))
		seedCleanedItem(t, ledger, rdb, src, can, fields)
	}
	cl := &Cleanup{Prefix: migPrefix, Ledger: ledger, RDB: rdb,
		Opts: CleanupOptions{Mode: ModeDual, RateLimitPerSec: 2}}
	start := time.Now()
	if _, err := cl.Cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("rate limit too aggressive: elapsed=%v, want >=900ms", elapsed)
	}
}

// TestCleanupChecksumMismatchRefuses: ledger records checksum A; live hash
// has checksum B (drifted). cleanup must refuse, leaving the key in place.
func TestCleanupChecksumMismatchRefuses(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	source := migPrefix + "node:a:7:b:8:c"
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	ledgerFields := legacyHashFieldsAsStrings()
	seedCleanedItem(t, ledger, rdb, source, canonical, ledgerFields)
	// Drift the live source fields after seeding.
	drifted := copyStringMap(ledgerFields)
	drifted["generation"] = "9"
	if err := rdb.HSet(context.Background(), source, drifted).Err(); err != nil {
		t.Fatalf("drift: %v", err)
	}
	cl := &Cleanup{Prefix: migPrefix, Ledger: ledger, RDB: rdb,
		Opts: CleanupOptions{Mode: ModeDual}}
	results, err := cl.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(results) == 0 || results[0].Status != CleanupStatusRefused {
		t.Fatalf("expected refused status, got %+v", results)
	}
	if results[0].Reason != "source checksum mismatch" {
		t.Fatalf("reason = %q", results[0].Reason)
	}
	exists, _ := rdb.Exists(context.Background(), source).Result()
	if exists != 1 {
		t.Fatal("refused cleanup must not delete the source")
	}
}
