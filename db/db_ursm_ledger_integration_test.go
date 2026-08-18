//go:build integration

package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestEnsureUrsmKeyMigrationLedgerSchemaUpgrades080(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("db_test"),
		postgres.WithUsername("db_test"),
		postgres.WithPassword("db_test"),
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
	pool := waitForLedgerEnsurePool(t, ctx, dsn)
	defer pool.Close()
	apply080LedgerMigration(t, ctx, dsn)

	var before bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'ursm_key_migration_runs'
		  AND column_name = 'rollback_deadline')`).Scan(&before); err != nil {
		t.Fatalf("check 080 schema: %v", err)
	}
	if before {
		t.Fatal("080 baseline unexpectedly contains rollback_deadline")
	}

	if err := (&DB{pool: pool}).ensureUrsmKeyMigrationLedgerSchema(ctx); err != nil {
		t.Fatalf("ensure ledger schema: %v", err)
	}
	if err := (&DB{pool: pool}).ensureUrsmKeyMigrationLedgerSchema(ctx); err != nil {
		t.Fatalf("repeat ensure ledger schema: %v", err)
	}

	var dataType, nullable string
	if err := pool.QueryRow(ctx, `
		SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'ursm_key_migration_runs'
		  AND column_name = 'rollback_deadline'`).Scan(&dataType, &nullable); err != nil {
		t.Fatalf("inspect upgraded column: %v", err)
	}
	if dataType != "timestamp with time zone" || nullable != "YES" {
		t.Fatalf("rollback_deadline = type:%q nullable:%q, want timestamptz nullable", dataType, nullable)
	}
}

func apply080LedgerMigration(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "080-ursm-key-migration-ledger.sql"))
	if err != nil {
		t.Fatalf("read 080 migration: %v", err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect migration script: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, string(script)); err != nil {
		t.Fatalf("apply 080: %v", err)
	}
}

func waitForLedgerEnsurePool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
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
	t.Fatalf("connect postgres: %v", err)
	return nil
}
