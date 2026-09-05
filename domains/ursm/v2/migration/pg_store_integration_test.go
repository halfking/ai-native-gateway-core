//go:build integration

package migration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

func TestPGStoreOpenRunAfter080And081(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("migration_test"),
		postgres.WithUsername("migration_test"),
		postgres.WithPassword("migration_test"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	pool := waitForMigrationTestPool(t, ctx, dsn)
	defer pool.Close()

	execMigrationScript(t, ctx, dsn, migrationScriptPath(t, "080-ursm-key-migration-ledger.sql"))
	assertRollbackDeadlineColumn(t, ctx, pool, false)
	execMigrationScript(t, ctx, dsn, migrationScriptPath(t, "081-ursm-key-migration-ledger-add-rollback-deadline.sql"))
	execMigrationScript(t, ctx, dsn, migrationScriptPath(t, "082-ursm-key-migration-state-machine.sql"))
	execMigrationScript(t, ctx, dsn, migrationScriptPath(t, "082-ursm-key-migration-state-machine.sql"))
	execMigrationScript(t, ctx, dsn, migrationScriptPath(t, "083-ursm-key-migration-add-dual-checkpoint.sql"))
	execMigrationScript(t, ctx, dsn, migrationScriptPath(t, "083-ursm-key-migration-add-dual-checkpoint.sql"))
	assertRollbackDeadlineColumn(t, ctx, pool, true)

	deadline := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	ledgerID := "ursm-test-081"
	pgStore := NewPGStore(pool)
	run := RunRecord{
		LedgerID:          ledgerID,
		Owner:             "migration-test-owner",
		KeySchemaMode:     store.KeySchemaModeDual,
		PreflightChecksum: "checksum",
		Checkpoint:        CheckpointCopy,
		CutoverEpoch:      7,
		RollbackDeadline:  deadline.Format(time.RFC3339Nano),
		Total:             3,
		Migratable:        1,
		CanonicalPresent:  1,
		Excluded:          1,
	}
	if err := pgStore.OpenRun(ctx, run); err != nil {
		t.Fatalf("open run after 081: %v", err)
	}
	changedRun := run
	changedRun.Owner = "different-owner"
	if err := pgStore.OpenRun(ctx, changedRun); err == nil {
		t.Fatal("reopening ledger_id with a different owner must fail closed")
	}

	var gotDeadline time.Time
	if err := pool.QueryRow(ctx, `SELECT rollback_deadline FROM ursm_key_migration_runs WHERE ledger_id = $1`, ledgerID).Scan(&gotDeadline); err != nil {
		t.Fatalf("read rollback deadline: %v", err)
	}
	if !gotDeadline.Equal(deadline) {
		t.Fatalf("rollback deadline = %s, want %s", gotDeadline, deadline)
	}

	entries := []EntryRecord{
		{
			SourceKey: legacyNodeA, TargetKey: canonicalA,
			Class: ClassificationMigratable, Schema: store.KeySchemaLegacy, Type: "hash",
			PTTLMillis: -1, Generation: 1, FieldChecksum: "legacy-checksum",
			Tuple: &store.ParsedNodeKey{TenantID: "a", CredentialID: 7, RawModel: "b:8:c"},
		},
		{
			SourceKey: canonicalA, TargetKey: canonicalA,
			Class: ClassificationCanonicalPresent, Schema: store.KeySchemaK2, Type: "hash",
			PTTLMillis: -1, Generation: 1, FieldChecksum: "canonical-checksum",
		},
		{
			SourceKey: migPrefix + "binding:excluded", Class: ClassificationExcludedNonAuthoritative,
			Type: "hash", PTTLMillis: -1, FieldChecksum: "excluded-checksum",
		},
	}
	if err := pgStore.UpsertEntries(ctx, ledgerID, entries); err != nil {
		t.Fatalf("upsert entries: %v", err)
	}
	var count int
	var tenant string
	if err := pool.QueryRow(ctx, `SELECT count(*), max(tuple_tenant) FROM ursm_key_migration_entries WHERE ledger_id = $1`, ledgerID).Scan(&count, &tenant); err != nil {
		t.Fatalf("read entries: %v", err)
	}
	if count != len(entries) || tenant != "a" {
		t.Fatalf("entries = count:%d tenant:%q, want count:%d tenant:a", count, tenant, len(entries))
	}

	pttl := int64(-1)
	outcome := CopyOutcome{
		LedgerID: ledgerID, Owner: run.Owner, Epoch: run.CutoverEpoch,
		SourceKey: legacyNodeA, TargetKey: canonicalA, Generation: 1,
		FieldChecksum: "legacy-checksum", State: StatusCopied, CopiedPTTLMs: &pttl,
	}
	if err := pgStore.PersistCopyOutcome(ctx, outcome); err != nil {
		t.Fatalf("persist durable copy outcome: %v", err)
	}
	if err := pgStore.PersistCopyOutcome(ctx, outcome); err != nil {
		t.Fatalf("replay durable copy outcome: %v", err)
	}
	stale := outcome
	stale.Epoch++
	if err := pgStore.PersistCopyOutcome(ctx, stale); err == nil {
		t.Fatal("stale copy epoch must be fenced")
	}

	evidence := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	advance := func(from, to Checkpoint, epoch int64, mode Mode) {
		t.Helper()
		if err := pgStore.advanceRun(ctx, PromotionRequest{
			LedgerID: ledgerID, Owner: run.Owner, ExpectedCheckpoint: from, ExpectedEpoch: epoch,
			Checkpoint: to, Mode: mode, Actor: run.Owner, ApprovedBy: "migration-test-reviewer",
			EvidenceSHA256: evidence, EvidenceRef: "test://evidence/" + string(to),
		}, deadline); err != nil {
			t.Fatalf("advance %s to %s: %v", from, to, err)
		}
	}
	advance(CheckpointCopy, CheckpointCoverage, 7, ModeDual)
	advance(CheckpointCoverage, CheckpointDual, 8, ModeDual)
	advance(CheckpointDual, CheckpointObserve, 9, ModeDual)
	advance(CheckpointObserve, CheckpointCleanup, 10, ModeCanonical)
	loadedRun, err := pgStore.LoadRun(ctx, ledgerID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if loadedRun.Checkpoint != CheckpointCleanup || loadedRun.CutoverEpoch != 11 || !loadedRun.RollbackDeadlineTime.Equal(deadline) {
		t.Fatalf("loaded run = %+v, want cleanup checkpoint, epoch 10, and deadline %s", loadedRun, deadline)
	}
	loadedEntries, err := pgStore.LoadEntries(ctx, ledgerID)
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(loadedEntries) != len(entries) || loadedEntries[0].FieldChecksum == "" {
		t.Fatalf("loaded entries = %+v, expected persisted checksum rows", loadedEntries)
	}
	var copied LoadedEntry
	for _, entry := range loadedEntries {
		if entry.SourceKey == legacyNodeA {
			copied = entry
		}
	}
	if copied.State != StatusCopied || copied.Tuple == nil || copied.Tuple.TenantID != "a" {
		t.Fatalf("copied entry = %+v, want copied row with tuple", copied)
	}
	if err := pgStore.ClaimEntryForCleanup(ctx, ledgerID, copied.SourceKey, copied.FieldChecksum); err != nil {
		t.Fatalf("claim cleanup: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ursm_key_migration_entries SET cleanup_claimed_at = $1 WHERE ledger_id = $2 AND source_key = $3`, deadline, ledgerID, copied.SourceKey); err != nil {
		t.Fatalf("set fenced claim clock: %v", err)
	}
	if err := pgStore.RecoverStaleCleanupClaim(ctx, ledgerID, copied.SourceKey, run.Owner, 11, time.Hour, deadline); err == nil {
		t.Fatal("unexpired cleanup claim must not be recovered")
	}
	if _, err := pool.Exec(ctx, `UPDATE ursm_key_migration_entries SET cleanup_claimed_at = $1 WHERE ledger_id = $2 AND source_key = $3`, deadline.Add(-2*time.Hour), ledgerID, copied.SourceKey); err != nil {
		t.Fatalf("age fenced claim: %v", err)
	}
	if err := pgStore.RecoverStaleCleanupClaim(ctx, ledgerID, copied.SourceKey, run.Owner, 11, time.Hour, deadline); err != nil {
		t.Fatalf("recover stale claim: %v", err)
	}
	if err := pgStore.ClaimEntryForCleanup(ctx, ledgerID, copied.SourceKey, copied.FieldChecksum); err != nil {
		t.Fatalf("reclaim cleanup after stale recovery: %v", err)
	}
	if err := pgStore.MarkEntryCleaned(ctx, ledgerID, copied.SourceKey, copied.FieldChecksum); err != nil {
		t.Fatalf("mark cleaned: %v", err)
	}
	if err := pgStore.MarkEntryCleaned(ctx, ledgerID, copied.SourceKey, copied.FieldChecksum); err == nil {
		t.Fatal("second cleanup state transition must fail closed")
	}
}

func migrationScriptPath(t *testing.T, filename string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "..", "sql", "migrations", filename)
}

func execMigrationScript(t *testing.T, ctx context.Context, dsn, path string) {
	t.Helper()
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse migration dsn: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect migration script: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, string(script)); err != nil {
		t.Fatalf("apply migration %s: %v", path, err)
	}
}

func assertRollbackDeadlineColumn(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var exists bool
	var dataType, nullable string
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'ursm_key_migration_runs'
			  AND column_name = 'rollback_deadline'
		), COALESCE((
			SELECT data_type FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'ursm_key_migration_runs'
			  AND column_name = 'rollback_deadline'
		), ''), COALESCE((
			SELECT is_nullable FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'ursm_key_migration_runs'
			  AND column_name = 'rollback_deadline'
		), '')`).Scan(&exists, &dataType, &nullable)
	if err != nil {
		t.Fatalf("inspect rollback_deadline: %v", err)
	}
	if exists != want {
		t.Fatalf("rollback_deadline exists = %t, want %t", exists, want)
	}
	if want && (dataType != "timestamp with time zone" || nullable != "YES") {
		t.Fatalf("rollback_deadline = type:%q nullable:%q, want timestamptz nullable", dataType, nullable)
	}
}

func waitForMigrationTestPool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	var pool *pgxpool.Pool
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			if err == nil {
				return pool
			}
			pool.Close()
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("connect migration postgres: %v", err)
	return nil
}
