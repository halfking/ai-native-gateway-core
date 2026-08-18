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
	execMigrationScript(t, ctx, dsn, migrationScriptPath(t, "081-ursm-key-migration-ledger-add-rollback-deadline.sql"))
	assertRollbackDeadlineColumn(t, ctx, pool, true)

	deadline := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	ledgerID := "ursm-test-081"
	pgStore := NewPGStore(pool)
	if err := pgStore.OpenRun(ctx, RunRecord{
		LedgerID:          ledgerID,
		Owner:             "migration-test-owner",
		KeySchemaMode:     store.KeySchemaModeDual,
		PreflightChecksum: "checksum",
		Checkpoint:        CheckpointPreflight,
		RollbackDeadline:  deadline.Format(time.RFC3339Nano),
		Total:             3,
		Migratable:        1,
		CanonicalPresent:  1,
		Excluded:          1,
	}); err != nil {
		t.Fatalf("open run after 081: %v", err)
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
