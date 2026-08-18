package migration

import (
	"context"
	"testing"
)

// TestPreflightRescanCopyAndCleanupPreservesCanonical exercises the operator
// resume sequence that previously promoted a canonical ledger row to copied
// and deleted it during cleanup.
func TestPreflightRescanCopyAndCleanupPreservesCanonical(t *testing.T) {
	mr, rdb := newFixtureRedis(t)
	writeLegacyNode(t, mr, rdb, legacyNodeA)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	preflight := &PreflightRunner{
		Prefix:  migPrefix,
		Ledger:  ledger,
		Scanner: &RedisScanner{RDB: rdb},
	}

	if _, err := preflight.Preflight(context.Background()); err != nil {
		t.Fatalf("first preflight: %v", err)
	}
	copyPhase := &Copy{Prefix: migPrefix, Ledger: ledger, RDB: rdb}
	if _, err := copyPhase.Copy(context.Background()); err != nil {
		t.Fatalf("first copy: %v", err)
	}
	if _, err := copyPhase.Copy(context.Background()); err != nil {
		t.Fatalf("idempotent copy: %v", err)
	}
	if _, err := preflight.Preflight(context.Background()); err != nil {
		t.Fatalf("rescan preflight: %v", err)
	}

	copyResults, err := copyPhase.Copy(context.Background())
	if err != nil {
		t.Fatalf("copy after rescan: %v", err)
	}
	canonicalSkipped := false
	for _, result := range copyResults {
		if result.Item.SourceKey == canonicalA {
			canonicalSkipped = result.Status == CopyStatusSkipped && result.Item.Status == StatusClassified
		}
	}
	if !canonicalSkipped {
		t.Fatalf("canonical rescan row was promoted or not skipped: %+v", copyResults)
	}

	cleanupResults, err := (&Cleanup{
		Prefix: migPrefix,
		Ledger: ledger,
		RDB:    rdb,
		Opts:   CleanupOptions{Mode: ModeDual},
	}).Cleanup(context.Background())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, result := range cleanupResults {
		if result.Item.SourceKey == canonicalA && result.Status == CleanupStatusDeleted {
			t.Fatalf("cleanup deleted canonical key: %+v", result)
		}
	}

	legacyExists, err := rdb.Exists(context.Background(), legacyNodeA).Result()
	if err != nil {
		t.Fatalf("check legacy source: %v", err)
	}
	if legacyExists != 0 {
		t.Fatal("cleanup did not delete legacy source")
	}
	canonicalFields, err := rdb.HGetAll(context.Background(), canonicalA).Result()
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if fieldChecksum(canonicalFields) != fieldChecksum(legacyHashFieldsAsStrings()) {
		t.Fatal("canonical fields changed during resume cleanup")
	}
}
