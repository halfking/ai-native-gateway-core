//go:build integration

// migration_705_behavior_integration_test.go — behavioral fixture for the
// request_logs promote 断链 that migration 705 closes: migration 337 DETACHed
// the monthly shells, the pg_class-only existence check in the 694 ensure
// never re-attached them, and promote INSERTs INTO the parent piled every row
// into request_logs_default (609 MB / 255k rows on the 2026-09-14 census).
//
// Covered here against a real planner:
//   * repair_request_logs_detached_partitions(): column-drifted shell is
//     synced + attached, that month's rows drain out of the default through
//     the parent, other months' rows stay in the default, the default is
//     re-attached, and a re-run is a 0-row no-op.
//   * ensure_request_logs_partition(): attached shell is a no-op; a legacy
//     detached shell is re-attached; the 473-class default-gap (rows already
//     in default for a month with no partition) self-heals on CREATE.
//   * UTC-session safety: all calls run under SET TIME ZONE 'UTC'; bounds
//     must still be Shanghai midnights (687 regression guard).
//
// Run:
//
//	go test -tags integration ./sql/migrations/startup -run TestMigration705 -count=1
//
// All DDL runs inside one transaction that is rolled back, so TEST_PG_URL
// instances are left clean.
package startup

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMigration705ReattachAndSelfHeal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, cleanup := partitionBehaviorContainer(t, ctx)
	defer cleanup()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	exec := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, sql)
		}
	}
	queryInt := func(sql string) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("query %q: %v", sql, err)
		}
		return n
	}

	// ── fixture: parent + default + drifted detached shell ────────────
	exec(`CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY,
		description text
	)`)
	exec(`CREATE TABLE public.request_logs (
		id bigint,
		ts timestamptz NOT NULL,
		search_text text,
		client_model text,
		is_terminal boolean
	) PARTITION BY RANGE (ts)`)
	// 705 also creates GIN trgm indexes on new partitions.
	exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`)
	exec(`CREATE TABLE public.request_logs_default (
		id bigint,
		ts timestamptz NOT NULL,
		search_text text,
		client_model text,
		is_terminal boolean
	)`)
	exec(`ALTER TABLE public.request_logs ATTACH PARTITION public.request_logs_default DEFAULT`)

	// The shell predates is_terminal (the 2026-09-14 census drift: shells
	// lack billed_despite_cancellation / request_depth / is_terminal).
	exec(`CREATE TABLE public.request_logs_2027_04 (
		id bigint,
		ts timestamptz NOT NULL,
		search_text text,
		client_model text
	)`)

	// The broken state: April rows sit in the default, the shell is an
	// empty standalone. March rows must survive the repair untouched.
	exec(`INSERT INTO public.request_logs (id, ts)
		SELECT g, '2027-04-05 12:00+08'::timestamptz FROM generate_series(1, 7) g`)
	exec(`INSERT INTO public.request_logs (id, ts)
		SELECT g, '2027-03-05 12:00+08'::timestamptz FROM generate_series(101, 103) g`)

	// ── apply 705 (single-txn safe: BEGIN/COMMIT and psql meta stripped) ──
	up705 := strip705TxControl(t, "705_request_logs_reattach_detached_partitions.sql")
	exec(up705)

	// ── assertions: repair ────────────────────────────────────────────
	// (the migration body itself ran the repair; assert the end state)
	if !partitionAttached(t, tx, ctx, "request_logs", "request_logs_2027_04") {
		t.Fatal("shell request_logs_2027_04 was not re-attached")
	}
	assertPartitionBound(t, tx, ctx, "request_logs_2027_04")
	if !partitionIsDefault(t, tx, ctx, "request_logs_default") {
		t.Fatal("request_logs_default was not re-attached as the DEFAULT partition")
	}

	if got := queryInt(`SELECT count(*) FROM public.request_logs_2027_04`); got != 7 {
		t.Errorf("April shell rows = %d, want 7 (drained from default)", got)
	}
	if got := queryInt(`SELECT count(*) FROM public.request_logs_default`); got != 3 {
		t.Errorf("default rows after repair = %d, want 3 (March rows stay)", got)
	}
	if got := queryInt(`SELECT count(*) FROM public.request_logs_2027_04 WHERE is_terminal IS NOT NULL`); got != 0 {
		t.Errorf("synced column should be NULL-filled for drained rows, got %d non-null", got)
	}
	// Routing works through the freshly attached partition.
	exec(`INSERT INTO public.request_logs (id, ts) VALUES (999, '2027-04-06 08:00+08'::timestamptz)`)
	if got := queryInt(`SELECT count(*) FROM public.request_logs_2027_04 WHERE id = 999`); got != 1 {
		t.Errorf("new April insert did not route into the attached partition (got %d)", got)
	}

	// Idempotency: a second repair run drains nothing and keeps topology.
	if _, err := tx.Exec(ctx, `SELECT public.repair_request_logs_detached_partitions()`); err != nil {
		t.Fatalf("second repair run failed: %v", err)
	}
	if got := queryInt(`SELECT count(*) FROM public.request_logs_default`); got != 3 {
		t.Errorf("default rows changed on idempotent re-run = %d, want 3", got)
	}

	// ── assertions: ensure ────────────────────────────────────────────
	// Already-attached month: no-op (must not error or duplicate).
	if _, err := tx.Exec(ctx,
		`SELECT public.ensure_request_logs_partition('2027-04-15 02:00+00'::timestamptz)`); err != nil {
		t.Errorf("ensure on attached month failed: %v", err)
	}

	// 473-class default-gap: May rows land in the default (no partition
	// exists), then ensure must self-heal — create the partition, drain the
	// month, re-attach the default.
	exec(`INSERT INTO public.request_logs (id, ts)
		SELECT g, '2027-05-05 12:00+08'::timestamptz FROM generate_series(1, 5) g`)
	if _, err := tx.Exec(ctx,
		`SELECT public.ensure_request_logs_partition('2027-05-15 02:00+00'::timestamptz)`); err != nil {
		t.Fatalf("ensure with default-gap month failed: %v", err)
	}
	if !partitionAttached(t, tx, ctx, "request_logs", "request_logs_2027_05") {
		t.Fatal("self-heal did not create request_logs_2027_05")
	}
	if got := queryInt(`SELECT count(*) FROM public.request_logs_2027_05`); got != 5 {
		t.Errorf("May partition rows = %d, want 5 (drained by self-heal)", got)
	}
	if got := queryInt(`SELECT count(*) FROM public.request_logs_default`); got != 3 {
		t.Errorf("default rows after self-heal = %d, want 3", got)
	}
	if !partitionIsDefault(t, tx, ctx, "request_logs_default") {
		t.Fatal("request_logs_default not re-attached after self-heal")
	}
}

// strip705TxControl strips BEGIN;/COMMIT; and the psql \set meta-command so
// the migration file can run inside the fixture transaction via pgx.
func strip705TxControl(t *testing.T, name string) string {
	t.Helper()
	body := stripTxControl(t, name)
	return regexp.MustCompile(`(?m)^\\set .*$`).ReplaceAllString(body, "")
}

func partitionAttached(t *testing.T, tx pgx.Tx, ctx context.Context, parent, part string) bool {
	t.Helper()
	var attached bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_inherits i
			JOIN pg_class p ON p.oid = i.inhparent
			JOIN pg_class c ON c.oid = i.inhrelid
			WHERE p.relname = $1 AND c.relname = $2
		)`, parent, part).Scan(&attached)
	if err != nil {
		t.Fatalf("check attach state of %s: %v", part, err)
	}
	return attached
}

func partitionIsDefault(t *testing.T, tx pgx.Tx, ctx context.Context, part string) bool {
	t.Helper()
	var bound string
	err := tx.QueryRow(ctx, `
		SELECT pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		WHERE c.relname = $1`, part).Scan(&bound)
	if err != nil {
		t.Fatalf("inspect bound of %s: %v", part, err)
	}
	return strings.Contains(bound, "DEFAULT")
}
