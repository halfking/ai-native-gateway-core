//go:build integration

package startup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func openStatsMigrationConn(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		container, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("stats_test"),
			postgres.WithUsername("stats_test"),
			postgres.WithPassword("stats_test"),
		)
		if err != nil {
			t.Fatalf("start postgres container: %v", err)
		}
		t.Cleanup(func() {
			terminateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(terminateCtx); err != nil {
				t.Errorf("terminate postgres container: %v", err)
			}
		})
		dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatalf("container connection string: %v", err)
		}
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	var conn *pgx.Conn
	for attempt := 0; attempt < 30; attempt++ {
		conn, err = pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestStatsMigrations536537539Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	conn := openStatsMigrationConn(t, ctx)

	schema := fmt.Sprintf("stats_migration_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema); err != nil {
		t.Fatal(err)
	}

	execMigration := func(name string) {
		t.Helper()
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	relationExists := func(name string) bool {
		t.Helper()
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+"."+name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}
	indexExists := func(name string) bool {
		t.Helper()
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_indexes WHERE schemaname = $1 AND indexname = $2)`, schema, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}

	execMigration("536_stats_analytics_foundation.sql")
	for _, table := range []string{
		"stats_event_dedup", "stats_event_inbox", "stats_event_inbox_default", "stats_usage_daily",
		"stats_usage_monthly", "stats_adjustments", "stats_reconciliation_runs", "stats_reconciliation_diffs",
	} {
		if !relationExists(table) {
			t.Errorf("536 did not create %s", table)
		}
	}
	if relationExists("usage_facts") {
		t.Fatal("536 must not create usage_facts; that belongs to 537")
	}
	var partitioned bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_partitioned_table p
		JOIN pg_class c ON c.oid = p.partrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = 'stats_event_inbox')`, schema).Scan(&partitioned); err != nil {
		t.Fatal(err)
	}
	if !partitioned {
		t.Fatal("stats_event_inbox is not a range-partitioned table")
	}
	for _, index := range []string{"idx_stats_event_inbox_pending", "idx_stats_event_inbox_request", "idx_stats_event_inbox_scope"} {
		if !indexExists(index) {
			t.Errorf("536 missing index %s", index)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO stats_event_inbox (event_id, occurred_at, request_id, event_type, status)
		VALUES ('inbox-default', now(), 'request-default', 'request_succeeded', 'success')`); err != nil {
		t.Fatal(err)
	}
	var routedToDefault bool
	if err := conn.QueryRow(ctx, `SELECT tableoid = 'stats_event_inbox_default'::regclass
		FROM stats_event_inbox WHERE event_id = 'inbox-default'`).Scan(&routedToDefault); err != nil {
		t.Fatal(err)
	}
	if !routedToDefault {
		t.Fatal("inbox row did not route to default partition")
	}
	execMigration("536_stats_analytics_foundation.sql")

	execMigration("537_usage_facts.sql")
	if !relationExists("usage_facts") || !relationExists("usage_facts_default") {
		t.Fatal("537 did not create usage facts parent/default partition")
	}
	for _, index := range []string{"idx_usage_facts_request", "idx_usage_facts_tenant_time", "idx_usage_facts_provider_time", "idx_usage_facts_model_time"} {
		if !indexExists(index) {
			t.Errorf("537 missing index %s", index)
		}
	}
	occurred := time.Now().UTC().Truncate(time.Microsecond)
	args := []any{"fact-event", "fact-request", 1, occurred, "default", "business", "success"}
	insertFact := `INSERT INTO usage_facts (event_id, request_id, revision, occurred_at, tenant_id, traffic_class, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`
	if _, err := conn.Exec(ctx, insertFact, args...); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Exec(ctx, insertFact, args...)
	if err == nil {
		t.Fatal("expected duplicate usage fact unique violation")
	}
	if !isPgErrorCode(err, "23505") {
		t.Fatalf("expected 23505 for duplicate usage fact, got %v", err)
	}
	if _, err := conn.Exec(ctx, insertFact, "fact-event", "fact-request", 2, occurred, "default", "business", "success"); err != nil {
		t.Fatalf("higher revision should be accepted: %v", err)
	}
	execMigration("537_usage_facts.sql")

	if _, err := conn.Exec(ctx, `INSERT INTO stats_reconciliation_runs (run_id, period_start, period_end)
		VALUES ('tenant-backfill', now() - interval '1 hour', now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO stats_reconciliation_diffs (run_id, dimension_type, dimension_key, metric)
		VALUES ('tenant-backfill', 'tenant', 'default', 'request_count')`); err != nil {
		t.Fatal(err)
	}
	execMigration("539_stats_reconciliation_tenant.sql")
	var tenant string
	if err := conn.QueryRow(ctx, `SELECT tenant_id FROM stats_reconciliation_diffs WHERE run_id = 'tenant-backfill'`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if tenant != "default" {
		t.Fatalf("539 tenant backfill = %q, want default", tenant)
	}
	if !indexExists("idx_stats_reconciliation_diffs_tenant") {
		t.Fatal("539 did not create tenant index")
	}
	execMigration("539_stats_reconciliation_tenant.sql")

	execMigration("540_stats_event_inbox_consumer.sql")
	for _, index := range []string{"idx_stats_event_inbox_claimable", "idx_stats_event_inbox_dead_letter"} {
		if !indexExists(index) {
			t.Errorf("540 missing index %s", index)
		}
	}
	var status string
	if err := conn.QueryRow(ctx, `SELECT processing_status FROM stats_event_inbox WHERE event_id = 'inbox-default'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("540 backfill status = %q, want pending", status)
	}
	_, err = conn.Exec(ctx, `UPDATE stats_event_inbox SET processing_status = 'invalid' WHERE event_id = 'inbox-default'`)
	if err == nil || !isPgErrorCode(err, "23514") {
		t.Fatalf("expected processing status check violation 23514, got %v", err)
	}
	execMigration("540_stats_event_inbox_consumer.sql")
}

func TestMigration539RequiresFoundation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	conn := openStatsMigrationConn(t, ctx)
	schema := fmt.Sprintf("stats_539_missing_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema+"; SET search_path TO "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE") })
	body, err := os.ReadFile("539_stats_reconciliation_tenant.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(body)); err == nil {
		t.Fatal("539 must fail without stats_reconciliation_diffs because its index target is absent")
	}
}

func isPgErrorCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
