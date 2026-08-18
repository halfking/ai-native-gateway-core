package migration

import (
	"context"
	"fmt"
	"os"
	"strings"
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

func authorizationForLedger(t *testing.T, ledger *Ledger) *CleanupAuthorization {
	t.Helper()
	items, err := ledger.LoadAll()
	if err != nil {
		t.Fatalf("load cleanup ledger: %v", err)
	}
	return cleanupAuthorization(time.Now().UTC(), items...)
}

func cleanupAuthorization(now time.Time, items ...Item) *CleanupAuthorization {
	return &CleanupAuthorization{
		Metadata: Metadata{
			Owner:             "cleanup-test-owner",
			LedgerID:          "cleanup-test-ledger",
			Mode:              ModeDual,
			CutoverEpoch:      1,
			StartedAt:         now.Add(-2 * time.Hour),
			UpdatedAt:         now.Add(-time.Hour),
			PreflightChecksum: "cleanup-test-checksum",
			RollbackDeadline:  now.Add(-time.Second),
			Checkpoint:        CheckpointCleanup,
		},
		Run: CleanupRun{
			Owner:             "cleanup-test-owner",
			LedgerID:          "cleanup-test-ledger",
			Mode:              ModeDual,
			PreflightChecksum: "cleanup-test-checksum",
			RollbackDeadline:  now.Add(-time.Second),
			Checkpoint:        CheckpointCleanup,
		},
		Items: items,
	}
}

func TestCleanupDeadlineAuthorizationGate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	fields := legacyHashFieldsAsStrings()
	item := seedCleanedItem(t, ledger, rdb, legacyNodeA, canonicalA, fields)

	tests := []struct {
		name    string
		mutate  func(*CleanupAuthorization)
		wantErr string
	}{
		{
			name: "deadline not reached",
			mutate: func(auth *CleanupAuthorization) {
				auth.Metadata.RollbackDeadline = now.Add(time.Second)
				auth.Run.RollbackDeadline = now.Add(time.Second)
			},
			wantErr: "rollback deadline has not elapsed",
		},
		{
			name: "deadline missing",
			mutate: func(auth *CleanupAuthorization) {
				auth.Metadata.RollbackDeadline = time.Time{}
				auth.Run.RollbackDeadline = time.Time{}
			},
			wantErr: "rollback deadline is required",
		},
		{
			name: "metadata identity mismatch",
			mutate: func(auth *CleanupAuthorization) {
				auth.Metadata.Owner = "different-owner"
			},
			wantErr: "owner mismatch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := cleanupAuthorization(now, item)
			tt.mutate(auth)
			cleanup := &Cleanup{
				Prefix: migPrefix,
				Ledger: ledger,
				RDB:    rdb,
				Opts: CleanupOptions{
					Mode:          ModeDual,
					Now:           func() time.Time { return now },
					Authorization: auth,
				},
			}
			if _, err := cleanup.Cleanup(ctx); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("cleanup error = %v, want %q", err, tt.wantErr)
			}
			exists, err := rdb.Exists(ctx, legacyNodeA).Result()
			if err != nil {
				t.Fatalf("check source: %v", err)
			}
			if exists != 1 {
				t.Fatal("deadline gate deleted source")
			}
		})
	}
}

func TestCleanupUnthrottledPersistsCleanedResult(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	item := seedCleanedItem(t, ledger, rdb, legacyNodeA, canonicalA, legacyHashFieldsAsStrings())
	var marked []Item
	_, err := (&Cleanup{
		Prefix: migPrefix,
		Ledger: ledger,
		RDB:    rdb,
		Opts: CleanupOptions{
			Mode:          ModeDual,
			Now:           func() time.Time { return now },
			Authorization: cleanupAuthorization(now, item),
			MarkCleaned: func(got Item) error {
				marked = append(marked, got)
				return nil
			},
		},
	}).Cleanup(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(marked) != 1 || marked[0].Status != StatusCleaned {
		t.Fatalf("marked = %+v, want one cleaned item", marked)
	}
}

func TestCleanupDeadlineAuthorizationAllowsExpiredDeadline(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	item := seedCleanedItem(t, ledger, rdb, legacyNodeA, canonicalA, legacyHashFieldsAsStrings())

	results, err := (&Cleanup{
		Prefix: migPrefix,
		Ledger: ledger,
		RDB:    rdb,
		Opts: CleanupOptions{
			Mode:          ModeDual,
			Now:           func() time.Time { return now },
			Authorization: cleanupAuthorization(now, item),
		},
	}).Cleanup(ctx)
	if err != nil {
		t.Fatalf("cleanup with expired deadline: %v", err)
	}
	if len(results) != 1 || results[0].Status != CleanupStatusDeleted {
		t.Fatalf("cleanup results = %+v, want one deleted result", results)
	}
}

func realCleanupRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_URL")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 500 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("TEST_REDIS_URL (%s) unreachable, skipping real Redis cleanup test: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestDeleteHashIfUnchangedPreservesMutationOnRealRedis(t *testing.T) {
	ctx := context.Background()
	rdb := realCleanupRedis(t)
	key := fmt.Sprintf("ursm:test:cleanup-cas:%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = rdb.Del(ctx, key).Err() })
	original := map[string]string{"generation": "1", "value": "before"}
	if err := rdb.HSet(ctx, key, original).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	snapshot, err := rdb.HGetAll(ctx, key).Result()
	if err != nil {
		t.Fatalf("snapshot source: %v", err)
	}
	if err := rdb.HSet(ctx, key, map[string]string{"generation": "2", "value": "after"}).Err(); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	outcome, err := deleteHashIfUnchanged(ctx, rdb, key, snapshot)
	if err != nil {
		t.Fatalf("compare-delete: %v", err)
	}
	if outcome != compareDeleteChanged {
		t.Fatalf("compare-delete result = %q, want changed", outcome)
	}
	live, err := rdb.HGetAll(ctx, key).Result()
	if err != nil || live["generation"] != "2" || live["value"] != "after" {
		t.Fatalf("mutated source was not preserved: fields=%v err=%v", live, err)
	}
	if err := rdb.ScriptFlush(ctx).Err(); err != nil {
		t.Fatalf("script flush: %v", err)
	}
	outcome, err = deleteHashIfUnchanged(ctx, rdb, key, live)
	if err != nil {
		t.Fatalf("compare-delete after script flush: %v", err)
	}
	if outcome != compareDeleteDeleted {
		t.Fatalf("compare-delete after script flush = %q, want deleted", outcome)
	}
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
		Opts: CleanupOptions{Mode: ModeDual, Authorization: authorizationForLedger(t, ledger)}}
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

// TestCleanupRollbackPreservesEvidence proves --force-rollback cannot bypass
// the durable mode/checkpoint/deadline authorization gate.
func TestCleanupRollbackPreservesEvidence(t *testing.T) {
	ctx := context.Background()
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	source := migPrefix + "node:a:7:b:8:c"
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	fields := legacyHashFieldsAsStrings()
	item := seedCleanedItem(t, ledger, rdb, source, canonical, fields)
	now := time.Now().UTC()
	auth := cleanupAuthorization(now, item)
	auth.Metadata.Mode = ModeLegacy
	auth.Run.Mode = ModeLegacy

	cl := &Cleanup{Prefix: migPrefix, Ledger: ledger, RDB: rdb,
		Opts: CleanupOptions{
			Mode:          ModeLegacy,
			ForceRollback: true,
			Now:           func() time.Time { return now },
			Authorization: auth,
		}}
	if _, err := cl.Cleanup(ctx); err == nil {
		t.Fatal("cleanup must refuse legacy mode even with --force-rollback")
	}
	exists, _ := rdb.Exists(ctx, source).Result()
	if exists != 1 {
		t.Fatal("source key must be preserved on rollback")
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
		Opts: CleanupOptions{Mode: ModeDual, RateLimitPerSec: 2, Authorization: authorizationForLedger(t, ledger)}}
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
		Opts: CleanupOptions{Mode: ModeDual, Authorization: authorizationForLedger(t, ledger)}}
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

// TestCleanupPreservesCanonicalPresentAfterHistoricalStatusPollution ensures
// a canonical key is safe even if an earlier copy pass wrote StatusCopied.
func TestCleanupPreservesCanonicalPresentAfterHistoricalStatusPollution(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	fields := legacyHashFieldsAsStrings()
	item := Item{
		SourceKey:      canonicalA,
		KeyType:        "hash",
		CanonicalKey:   canonicalA,
		SchemaSource:   "k2",
		Classification: ClassificationCanonicalPresent,
		Status:         StatusCopied,
		FieldChecksum:  fieldChecksum(fields),
		Generation:     1,
		ScanRunID:      "historical",
	}
	if err := ledger.Append(item); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := rdb.HSet(context.Background(), canonicalA, fields).Err(); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	results, err := (&Cleanup{
		Prefix: migPrefix,
		Ledger: ledger,
		RDB:    rdb,
		Opts:   CleanupOptions{Mode: ModeDual, Authorization: authorizationForLedger(t, ledger)},
	}).Cleanup(context.Background())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(results) != 1 || results[0].Status != CleanupStatusPreserved {
		t.Fatalf("cleanup results = %+v, want one preserved canonical result", results)
	}
	if results[0].Item.Status != StatusCopied {
		t.Fatalf("canonical ledger status = %q, want copied retained for audit", results[0].Item.Status)
	}
	exists, err := rdb.Exists(context.Background(), canonicalA).Result()
	if err != nil {
		t.Fatalf("check canonical: %v", err)
	}
	if exists != 1 {
		t.Fatal("cleanup deleted canonical key")
	}
}

func TestEntryCleanerRequiresAuthorization(t *testing.T) {
	ctx := context.Background()
	_, rdb := newFixtureRedis(t)
	fields := legacyHashFieldsAsStrings()
	if err := rdb.HSet(ctx, legacyNodeA, fields).Err(); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	entry := EntryRecord{
		SourceKey: legacyNodeA, TargetKey: canonicalA, Class: ClassificationMigratable,
		FieldChecksum: checksumFields(fields),
	}
	result, err := NewEntryCleaner(rdb).DeleteExact(ctx, entry)
	if err != nil {
		t.Fatalf("delete exact: %v", err)
	}
	if result.Status != CleanerSkippedIneligible {
		t.Fatalf("status = %q, want skipped ineligible", result.Status)
	}
	if exists, _ := rdb.Exists(ctx, legacyNodeA).Result(); exists != 1 {
		t.Fatal("unauthorized entry cleaner deleted source")
	}

	auth := cleanupAuthorization(time.Now().UTC(), Item{
		SourceKey: legacyNodeA, CanonicalKey: canonicalA, Classification: ClassificationMigratable,
		FieldChecksum: checksumFields(fields), Status: StatusCopied,
	})
	result, err = NewEntryCleaner(rdb, auth).DeleteExact(ctx, entry)
	if err != nil {
		t.Fatalf("authorized delete exact: %v", err)
	}
	if result.Status != CleanerDeleted {
		t.Fatalf("authorized status = %q, want deleted", result.Status)
	}
}

func TestEntryCleanerPreservesMigratableSelfTarget(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	fields := legacyHashFieldsAsStrings()
	if err := rdb.HSet(context.Background(), canonicalA, fields).Err(); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	result, err := NewEntryCleaner(rdb).DeleteExact(context.Background(), EntryRecord{
		SourceKey:     canonicalA,
		TargetKey:     canonicalA,
		Class:         ClassificationMigratable,
		FieldChecksum: checksumFields(fields),
	})
	if err != nil {
		t.Fatalf("delete exact: %v", err)
	}
	if result.Status != CleanerSkippedIneligible {
		t.Fatalf("status = %q, want skipped ineligible", result.Status)
	}
	exists, _ := rdb.Exists(context.Background(), canonicalA).Result()
	if exists != 1 {
		t.Fatal("entry cleaner deleted canonical key")
	}
}

func TestEntryCleanerRejectsMigratableEmptyTarget(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	fields := legacyHashFieldsAsStrings()
	if err := rdb.HSet(context.Background(), legacyNodeA, fields).Err(); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	result, err := NewEntryCleaner(rdb).DeleteExact(context.Background(), EntryRecord{
		SourceKey:     legacyNodeA,
		Class:         ClassificationMigratable,
		FieldChecksum: checksumFields(fields),
	})
	if err != nil {
		t.Fatalf("delete exact: %v", err)
	}
	if result.Status != CleanerSkippedIneligible {
		t.Fatalf("status = %q, want skipped ineligible", result.Status)
	}
	exists, _ := rdb.Exists(context.Background(), legacyNodeA).Result()
	if exists != 1 {
		t.Fatal("entry cleaner deleted source with empty target")
	}
}
