//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestEnsureNodeProbeTriggerKindSchemaUpgradesLegacyChecks(t *testing.T) {
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
		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool := waitForMigration538Pool(t, ctx, dsn)
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		CREATE TABLE node_probe_runs (
			trigger_kind text NOT NULL,
			CONSTRAINT node_probe_runs_trigger_kind_check CHECK (
				trigger_kind IN ('request_failure', 'manual', 'credential_recovery', 'sync_request')
			)
		);
		CREATE TABLE credential_probe_queue (
			source text NOT NULL,
			CONSTRAINT credential_probe_queue_source_check CHECK (
				source IN ('request_failure', 'periodic', 'external_async', 'admin', 'integrity_probe_planner')
			)
		);`)
	if err != nil {
		t.Fatalf("create legacy checks: %v", err)
	}

	db := &DB{pool: pool}
	if err := db.ensureNodeProbeTriggerKindSchema(ctx); err != nil {
		t.Fatalf("upgrade checks: %v", err)
	}
	if err := db.ensureNodeProbeTriggerKindSchema(ctx); err != nil {
		t.Fatalf("repeat upgrade checks: %v", err)
	}

	for _, triggerKind := range []string{"periodic", "admin", "integrity_probe_planner", "selfcheck", "external_async"} {
		if _, err := pool.Exec(ctx, `INSERT INTO node_probe_runs (trigger_kind) VALUES ($1)`, triggerKind); err != nil {
			t.Errorf("insert trigger_kind %q: %v", triggerKind, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO credential_probe_queue (source) VALUES ('selfcheck')`); err != nil {
		t.Fatalf("insert queue selfcheck source: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_probe_runs (trigger_kind) VALUES ('invalid')`); err == nil {
		t.Fatal("invalid trigger_kind unexpectedly accepted")
	}
}

func waitForMigration538Pool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	for attempt := 0; attempt < 30; attempt++ {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool
			}
			pool.Close()
		}
		time.Sleep(time.Second)
	}
	t.Fatal("connect migration 538 postgres container")
	return nil
}
