//go:build integration

package handoff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestMigration527_DurableGoalStateLifecycleIntegration is the audit
// follow-up for 75c714b67 row F: execute migration 527 (the startup mirror of
// db-migration 362) end-to-end against a real PostgreSQL container, verify the
// schema widens status to VARCHAR(32), adds the six goal_state / restore_*
// columns with the expected types and nullability, accepts the new lifecycle
// statuses, and that the matching down migration cleanly rolls everything back.
//
// The structural substring assertions in migration_362_test.go catch textual
// regressions in the SQL files; this test catches DDL-level regressions
// (wrong column types, broken CHECK constraints, bad partial-index predicate,
// failed down collapse) that substring matching cannot detect.
func TestMigration527_DurableGoalStateLifecycleIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	conn, cleanup := migration527Container(t, ctx)
	defer cleanup()

	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, sql)
		}
	}
	execFile := func(rel string) {
		t.Helper()
		path := filepath.Join(repoRoot(t), rel)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("exec %s: %v", path, err)
		}
	}

	exec(`CREATE TABLE IF NOT EXISTS handoff_logs (
		id SERIAL PRIMARY KEY,
		tenant_id VARCHAR(64) NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	execFile("sql/migrations/startup/517_handoff_pending_confirmations.sql")
	execFile("sql/migrations/startup/527_handoff_durable_goal_state.sql")

	wantColumns := []struct {
		name     string
		dataType string
		nullable bool
	}{
		{"goal_state", "jsonb", true},
		{"goal_state_version", "integer", true},
		{"restore_status", "character varying", true},
		{"restore_error", "character varying", true},
		{"restore_attempted_at", "timestamp with time zone", true},
		{"restored_at", "timestamp with time zone", true},
	}
	for _, want := range wantColumns {
		gotType, gotNullable, err := columnInfo(ctx, conn, "handoff_pending_confirmations", want.name)
		if err != nil {
			t.Fatalf("inspect column %s: %v", want.name, err)
		}
		if gotType != want.dataType {
			t.Errorf("column %s data_type = %q, want %q", want.name, gotType, want.dataType)
		}
		if gotNullable != want.nullable {
			t.Errorf("column %s is_nullable = %v, want %v", want.name, gotNullable, want.nullable)
		}
	}

	statusType, _, err := columnInfo(ctx, conn, "handoff_pending_confirmations", "status")
	if err != nil {
		t.Fatalf("inspect status column: %v", err)
	}
	if statusType != "character varying" {
		t.Errorf("status data_type = %q, want character varying", statusType)
	}
	var statusLength int
	if err := conn.QueryRow(ctx, `SELECT character_maximum_length
		FROM information_schema.columns
		WHERE table_name='handoff_pending_confirmations' AND column_name='status'`).Scan(&statusLength); err != nil {
		t.Fatalf("inspect status length: %v", err)
	}
	if statusLength != 32 {
		t.Errorf("status character_maximum_length = %d, want 32", statusLength)
	}

	var checkDef string
	if err := conn.QueryRow(ctx, `SELECT pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t ON c.conrelid = t.oid
		WHERE t.relname='handoff_pending_confirmations'
		  AND c.conname='handoff_pending_confirmations_status_check'`).Scan(&checkDef); err != nil {
		t.Fatalf("inspect CHECK constraint: %v", err)
	}
	for _, status := range []string{"pending", "confirmed", "accounting_confirmed", "restored", "manual_required", "expired"} {
		if !contains(checkDef, status) {
			t.Errorf("CHECK constraint missing status %q; got: %s", status, checkDef)
		}
	}

	var indexDef string
	if err := conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname='public' AND indexname='idx_handoff_pending_restore'`).Scan(&indexDef); err != nil {
		t.Fatalf("inspect partial index: %v", err)
	}
	if !contains(indexDef, "WHERE") || !contains(indexDef, "accounting_confirmed") || !contains(indexDef, "manual_required") {
		t.Errorf("partial index predicate missing accounting_confirmed/manual_required: %s", indexDef)
	}

	exec(`INSERT INTO handoff_logs (tenant_id) VALUES ('tenant-a') RETURNING id`)
	var logID int
	if err := conn.QueryRow(ctx, `SELECT id FROM handoff_logs WHERE tenant_id='tenant-a' ORDER BY id DESC LIMIT 1`).Scan(&logID); err != nil {
		t.Fatalf("lookup handoff_logs id: %v", err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO handoff_pending_confirmations (
		id, api_key_id, previous_session_id, token_hash, status, expires_at,
		trigger_mode, trigger_reason, tokens_at_trigger, messages_at_trigger,
		tokens_in_session, proposal_created_at, tenant_id, handoff_log_id,
		goal_state, restore_status
	) VALUES (
		gen_random_uuid(), 42, 'gw_old', repeat('a', 64), 'accounting_confirmed',
		NOW() + INTERVAL '1 hour', 'manual', 'skill:handoff', 100, 1, 100,
		NOW(), 'tenant-a', $1,
		'{"version":1,"source_session_id":"gw_old","tenant_id":"tenant-a","state":"active"}'::jsonb,
		'accounting_confirmed'
	)`, logID)
	if err != nil {
		t.Fatalf("insert accounting_confirmed row: %v", err)
	}

	_, err = conn.Exec(ctx, `INSERT INTO handoff_pending_confirmations (
		id, api_key_id, previous_session_id, token_hash, status, expires_at,
		trigger_mode, trigger_reason, tokens_at_trigger, messages_at_trigger,
		tokens_in_session, proposal_created_at, tenant_id
	) VALUES (
		gen_random_uuid(), 42, 'gw_invalid', repeat('b', 64), 'invalid_status',
		NOW() + INTERVAL '1 hour', 'manual', 'skill:handoff', 100, 1, 100,
		NOW(), 'tenant-a'
	)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("invalid status insert error = %v, want SQLSTATE 23514", err)
	}

	execFile("sql/migrations/startup/527_handoff_durable_goal_state.down.sql")

	var downStatus string
	if err := conn.QueryRow(ctx, `SELECT status FROM handoff_pending_confirmations
		WHERE previous_session_id='gw_old' AND tenant_id='tenant-a'`).Scan(&downStatus); err != nil {
		t.Fatalf("lookup status after down: %v", err)
	}
	if downStatus != "confirmed" {
		t.Errorf("status after down = %q, want 'confirmed' (new lifecycle must collapse)", downStatus)
	}

	for _, col := range []string{"goal_state", "goal_state_version", "restore_status", "restore_error", "restore_attempted_at", "restored_at"} {
		exists, err := columnExists(ctx, conn, "handoff_pending_confirmations", col)
		if err != nil {
			t.Fatalf("inspect %s after down: %v", col, err)
		}
		if exists {
			t.Errorf("column %s still present after down migration", col)
		}
	}

	var downStatusLength int
	if err := conn.QueryRow(ctx, `SELECT character_maximum_length
		FROM information_schema.columns
		WHERE table_name='handoff_pending_confirmations' AND column_name='status'`).Scan(&downStatusLength); err != nil {
		t.Fatalf("inspect status length after down: %v", err)
	}
	if downStatusLength != 16 {
		t.Errorf("status character_maximum_length after down = %d, want 16", downStatusLength)
	}

	var downCheckDef string
	if err := conn.QueryRow(ctx, `SELECT pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t ON c.conrelid = t.oid
		WHERE t.relname='handoff_pending_confirmations'
		  AND c.conname='handoff_pending_confirmations_status_check'`).Scan(&downCheckDef); err != nil {
		t.Fatalf("inspect CHECK after down: %v", err)
	}
	for _, status := range []string{"pending", "confirmed", "expired"} {
		if !contains(downCheckDef, status) {
			t.Errorf("down CHECK constraint missing status %q; got: %s", status, downCheckDef)
		}
	}
	if contains(downCheckDef, "accounting_confirmed") {
		t.Errorf("down CHECK still references accounting_confirmed: %s", downCheckDef)
	}

	var indexExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='idx_handoff_pending_restore'
	)`).Scan(&indexExists); err != nil {
		t.Fatalf("inspect partial index after down: %v", err)
	}
	if indexExists {
		t.Error("partial index idx_handoff_pending_restore still present after down migration")
	}

	execFile("sql/migrations/startup/527_handoff_durable_goal_state.sql")
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name='handoff_pending_confirmations' AND column_name='goal_state'
	)`).Scan(&indexExists); err != nil {
		t.Fatalf("inspect re-up columns: %v", err)
	}
	if !indexExists {
		t.Error("527 up is not re-entrant: goal_state column missing after second up")
	}
}

func migration527Container(t *testing.T, ctx context.Context) (*pgx.Conn, func()) {
	t.Helper()
	if dsn := os.Getenv("TEST_PG_URL"); dsn != "" {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connect TEST_PG_URL: %v", err)
		}
		return conn, func() { _ = conn.Close(context.Background()) }
	}
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("handoff_527"),
		postgres.WithUsername("handoff_527"),
		postgres.WithPassword("handoff_527"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
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
		t.Fatalf("connect postgres: %v", err)
	}
	cleanup := func() {
		_ = conn.Close(context.Background())
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}
	return conn, cleanup
}

func columnInfo(ctx context.Context, conn *pgx.Conn, table, column string) (string, bool, error) {
	var dataType string
	var nullable string
	if err := conn.QueryRow(ctx, `SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_name=$1 AND column_name=$2`, table, column).Scan(&dataType, &nullable); err != nil {
		return "", false, err
	}
	return dataType, nullable == "YES", nil
}

func columnExists(ctx context.Context, conn *pgx.Conn, table, column string) (bool, error) {
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name=$1 AND column_name=$2
	)`, table, column).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
