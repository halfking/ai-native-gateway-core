//go:build integration

// migration_694_behavior_integration_test.go — R15 #4: behavioral fixture for
// the UTC-session month-boundary trap that 694 (13 ensure functions) and 687
// (08:00+08 bound pollution) closed. The static contract tests catch textual
// drift; this test catches semantic regressions that only a real planner can
// expose (DECLARE-initializer evaluation order, session-TZ-dependent bound
// casting, columnar access-method availability).
//
// Run:
//
//	go test -tags integration ./sql/migrations/startup -run TestMigration694UTCBoundary -count=1
//
// With TEST_PG_URL pointing at a citus_columnar-capable instance (e.g. the
// local llm-gateway-pg) the full 694 file plus 699 (supplier_errors) and the
// next-month columnar trio run; on the vanilla postgres:16-alpine container
// the three USING-columnar definitions are filtered (no columnar AM exists)
// and the ten heap functions are exercised. All DDL runs inside one
// transaction that is rolled back, so TEST_PG_URL instances are left clean.
package startup

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// boundaryInstantUTC is 2027-04-01 02:00 Asia/Shanghai = 2027-03-31 18:00 UTC.
// Feeding it to a pinned ensure under a UTC session must create the Shanghai
// April partition; a pre-694 body would derive March (the function's entire
// reason to exist). April 2027 is also guaranteed absent on any real
// instance, so the create path (not the IF EXISTS skip) always executes.
const boundaryInstantUTC = "2027-03-31 18:00:00+00"

// shanghaiMidnightAprilLower / Upper are the expected RANGE bounds of the
// 2027_04 partition. Under SET LOCAL TIME ZONE 'Asia/Shanghai' the bound
// constants are created from Shanghai-midnight literals and pg_get_expr
// renders them with their explicit +08 offset — regardless of the caller's
// session zone. A 687-class bound would read 08:00:00 instead of 00:00:00.
const (
	aprilLowerUTC = "FROM ('2027-04-01 00:00:00+08')"
	aprilUpperUTC = "TO ('2027-05-01 00:00:00+08')"
)

func TestMigration694UTCBoundaryBehavior(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, cleanup := partitionBehaviorContainer(t, ctx)
	defer cleanup()

	var hasColumnar bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_am WHERE amname = 'columnar')`).Scan(&hasColumnar); err != nil {
		t.Fatalf("probe columnar access method: %v", err)
	}

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

	// ── fixture schema ──────────────────────────────────────────────
	exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`)
	// Minimal ledger shape for the migrations' idempotent bookkeeping; on a
	// TEST_PG_URL instance the real table already exists and this is a no-op.
	exec(`CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY,
		description text
	)`)

	// Minimal partitioned parents with the real partition keys (verified on
	// the authoritative instance). The ensure functions only ever create
	// partitions; they never touch other columns. On a TEST_PG_URL instance
	// the real tables win (IF NOT EXISTS) and the calls exercise them.
	parents := []string{
		`CREATE TABLE IF NOT EXISTS public.request_logs (ts timestamptz NOT NULL, search_text text, client_model text) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS public.request_logs_bodies (ts timestamptz NOT NULL) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS public.request_wal (created_at timestamptz NOT NULL) PARTITION BY RANGE (created_at)`,
		`CREATE TABLE IF NOT EXISTS public.candidate_failure_logs (ts timestamptz NOT NULL) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS public.credential_model_index (bucket timestamptz NOT NULL) PARTITION BY RANGE (bucket)`,
		`CREATE TABLE IF NOT EXISTS public.usage_ledger (ts timestamptz NOT NULL) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS public.routing_decision_log (ts timestamptz NOT NULL) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS public.tool_usage_stats (created_at timestamptz NOT NULL) PARTITION BY RANGE (created_at)`,
		`CREATE TABLE IF NOT EXISTS public.credit_ledger (created_at timestamptz NOT NULL) PARTITION BY RANGE (created_at)`,
		`CREATE TABLE IF NOT EXISTS public.request_logs_archive (ts timestamptz NOT NULL) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS public.credential_model_index_archive (bucket timestamptz NOT NULL) PARTITION BY RANGE (bucket)`,
		`CREATE TABLE IF NOT EXISTS public.routing_decision_log_archive (ts timestamptz NOT NULL) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS public.supplier_errors (occurred_at timestamptz NOT NULL) PARTITION BY RANGE (occurred_at)`,
	}
	for _, ddl := range parents {
		exec(ddl)
	}

	// ── apply 694 (and 699 where columnar exists) ───────────────────
	up694 := stripTxControl(t, "694_partition_ensure_timezone.sql")
	if !hasColumnar {
		up694 = filterColumnarDefs(t, up694)
	}
	exec(up694)
	if hasColumnar {
		exec(stripTxControl(t, "699_supplier_errors_ensure_timezone_pin.sql"))
	}

	// Snapshot the partitions that already exist so assertions can tell
	// partitions this test created from pre-existing instance state. A
	// shared instance may legitimately carry legacy (pre-687) bounds; the
	// test hard-fails only on partitions it created itself.
	preExisting := map[string]bool{}
	snapRows, err := tx.Query(ctx, `
		SELECT c.relname::text
		FROM pg_inherits i
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_class c ON c.oid = i.inhrelid
		WHERE p.relname IN (
			'request_logs', 'request_logs_bodies', 'request_wal',
			'candidate_failure_logs', 'credential_model_index', 'usage_ledger',
			'routing_decision_log', 'tool_usage_stats', 'credit_ledger',
			'request_logs_archive', 'credential_model_index_archive',
			'routing_decision_log_archive', 'supplier_errors')`)
	if err != nil {
		t.Fatalf("snapshot existing partitions: %v", err)
	}
	for snapRows.Next() {
		var name string
		if err := snapRows.Scan(&name); err != nil {
			t.Fatalf("scan snapshot: %v", err)
		}
		preExisting[name] = true
	}
	if err := snapRows.Err(); err != nil {
		t.Fatalf("iterate snapshot: %v", err)
	}
	snapRows.Close()

	// ── the trap: UTC session, boundary instant ─────────────────────
	exec(`SET TIME ZONE 'UTC'`)

	heapCases := []struct{ fn, wantPartition string }{
		{"ensure_candidate_failure_logs_partition", "candidate_failure_logs_2027_04"},
		{"ensure_credential_model_index_partition", "credential_model_index_2027_04"},
		{"ensure_request_logs_bodies_partition", "request_logs_bodies_2027_04"},
		{"ensure_request_logs_partition", "request_logs_2027_04"},
		{"ensure_request_wal_partition", "request_wal_2027_04"},
		{"ensure_routing_decision_log_partition", "routing_decision_log_2027_04"},
		{"ensure_usage_ledger_partition", "usage_ledger_2027_04"},
		{"ensure_tool_usage_stats_partition", "tool_usage_stats_2027_04"},
		{"ensure_credit_ledger_partition", "credit_ledger_2027_04"},
	}
	if hasColumnar {
		heapCases = append(heapCases,
			struct{ fn, wantPartition string }{"ensure_supplier_errors_partition", "supplier_errors_2027_04"})
	}
	for _, tc := range heapCases {
		if _, err := tx.Exec(ctx, "SELECT "+tc.fn+"($1)", boundaryInstantUTC); err != nil {
			t.Errorf("%s failed under UTC session: %v", tc.fn, err)
			continue
		}
		if preExisting[tc.wantPartition] {
			t.Logf("partition %s pre-existed; skipping create-path assertion", tc.wantPartition)
			continue
		}
		assertPartitionBound(t, tx, ctx, tc.wantPartition)
	}

	// 687 regression sweep over everything now visible. Partitions this
	// test created must carry midnight-Shanghai bounds — hard fail. Others
	// are pre-existing instance state (687 not yet applied there): report
	// loudly but do not fail, remediation is a deployment action.
	var legacy []string
	boundRows, err := tx.Query(ctx, `
		SELECT c.relname::text, pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits i
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_class c ON c.oid = i.inhrelid
		WHERE p.relname IN (
			'request_logs', 'request_logs_bodies', 'request_wal',
			'candidate_failure_logs', 'credential_model_index', 'usage_ledger',
			'routing_decision_log', 'tool_usage_stats', 'credit_ledger',
			'request_logs_archive', 'credential_model_index_archive',
			'routing_decision_log_archive', 'supplier_errors')`)
	if err != nil {
		t.Fatalf("sweep partition bounds: %v", err)
	}
	defer boundRows.Close()
	for boundRows.Next() {
		var part, bound string
		if err := boundRows.Scan(&part, &bound); err != nil {
			t.Fatalf("scan bound: %v", err)
		}
		if !strings.Contains(bound, " 08:00:00") {
			continue
		}
		if preExisting[part] {
			legacy = append(legacy, part)
			continue
		}
		t.Errorf("687 regression: partition %s created with an 08:00 bound: %s", part, bound)
	}
	if err := boundRows.Err(); err != nil {
		t.Fatalf("iterate bounds: %v", err)
	}
	if len(legacy) > 0 {
		t.Logf("WARNING: instance carries %d pre-existing 08:00+08-bounded partitions (migration 687 not applied on this instance — apply before 2026-10-01): %s",
			len(legacy), strings.Join(legacy, ", "))
	}

	// Next-month functions derive their month from now(); under the UTC
	// session the pin must still land Shanghai-midnight bounds for the
	// Shanghai now+1month. Columnar trio only where the access method
	// exists. Pre-existing partitions are reported, not failed.
	if hasColumnar {
		nextMonth := time.Now().In(shanghaiTestZone).AddDate(0, 1, 0)
		wantSuffix := nextMonth.Format("2006_01")
		// The functions date_trunc the derived month, so the expected lower
		// bound is the month's first day, not now+1month's day.
		nextMonthStart := time.Date(nextMonth.Year(), nextMonth.Month(), 1, 0, 0, 0, 0, shanghaiTestZone)
		wantLower := nextMonthStart.Format("2006-01-02") + " 00:00:00+08"

		for _, fn := range []string{
			"ensure_next_month_archive_partition",
			"ensure_next_month_cmi_archive_partition",
			"ensure_next_month_routing_archive_partition",
		} {
			if _, err := tx.Exec(ctx, "SELECT " + fn + "()"); err != nil {
				t.Errorf("%s failed under UTC session: %v", fn, err)
			}
		}
		for _, parent := range []string{
			"request_logs_archive",
			"credential_model_index_archive",
			"routing_decision_log_archive",
		} {
			part := parent + "_" + wantSuffix
			if preExisting[part] {
				continue
			}
			bound := partitionBound(t, tx, ctx, part)
			if !strings.Contains(bound, "FROM ('"+wantLower+"')") {
				t.Errorf("partition %s: bound %s does not start at Shanghai midnight %s", part, bound, wantLower)
			}
		}

		// Idempotency: a second supplier_errors call in the same session
		// must be a clean no-op (IF EXISTS branch + columnar enforce).
		if _, err := tx.Exec(ctx, `SELECT ensure_supplier_errors_partition($1)`, boundaryInstantUTC); err != nil {
			t.Errorf("second ensure_supplier_errors_partition call failed: %v", err)
		}
	}
}

// assertPartitionBound asserts the named partition exists and its RANGE
// bounds are exactly the Shanghai midnights for 2027_04, rendered under the
// current (UTC) session.
func assertPartitionBound(t *testing.T, tx pgx.Tx, ctx context.Context, part string) {
	t.Helper()
	bound := partitionBound(t, tx, ctx, part)
	if !strings.Contains(bound, aprilLowerUTC) || !strings.Contains(bound, aprilUpperUTC) {
		t.Errorf("partition %s: bound %q, want %s .. %s (Shanghai midnight)", part, bound, aprilLowerUTC, aprilUpperUTC)
	}
}

func partitionBound(t *testing.T, tx pgx.Tx, ctx context.Context, part string) string {
	t.Helper()
	var bound string
	err := tx.QueryRow(ctx, `
		SELECT pg_get_expr(c.relpartbound, c.oid)
		FROM pg_class c
		WHERE c.relname = $1 AND c.relispartition`, part).Scan(&bound)
	if err != nil {
		t.Fatalf("inspect partition %s: %v", part, err)
	}
	return bound
}

// shanghaiTestZone mirrors bg.partitionTZ: fixed +08 (no DST, no tzdata
// dependency in the test binary).
var shanghaiTestZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// stripTxControl removes the file-level BEGIN;/COMMIT; so the migration can
// run inside the fixture's own transaction (which is rolled back). Nested
// COMMIT would otherwise escape and persist DDL on TEST_PG_URL instances.
func stripTxControl(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	sql := string(body)
	sql = regexp.MustCompile(`(?m)^BEGIN;\s*$`).ReplaceAllString(sql, "")
	sql = regexp.MustCompile(`(?m)^COMMIT;\s*$`).ReplaceAllString(sql, "")
	return sql
}

// filterColumnarDefs removes 694 function definitions whose bodies require
// the citus_columnar access method, for vanilla-PG runs.
func filterColumnarDefs(t *testing.T, sql string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION public\.ensure_\w+\(.*?\$\$;`)
	filtered := re.ReplaceAllStringFunc(sql, func(m string) string {
		if strings.Contains(m, "USING columnar") {
			return ""
		}
		return m
	})
	if strings.Contains(filtered, "USING columnar") {
		t.Fatal("columnar filter left USING columnar definitions behind")
	}
	return filtered
}

func partitionBehaviorContainer(t *testing.T, ctx context.Context) (*pgx.Conn, func()) {
	t.Helper()
	if dsn := os.Getenv("TEST_PG_URL"); dsn != "" {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse TEST_PG_URL: %v", err)
		}
		// Multi-statement migration files require the simple protocol.
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect TEST_PG_URL: %v", err)
		}
		return conn, func() { _ = conn.Close(context.Background()) }
	}
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("partition_tz"),
		postgres.WithUsername("partition_tz"),
		postgres.WithPassword("partition_tz"),
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
