package startup

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Migration 711 (hosted-task-delegation-design §6.1) creates the hosted task
// delegation projection trio: hosted_tasks / hosted_task_events /
// hosted_task_callbacks. This file pins the SQL contract statically; the
// DB-backed matrix (fresh up/down/up + RLS negative rows) runs only when
// TEST_PG_URL points at a disposable database — without it the integration
// portion skips and must not be reported as PASS (acceptance matrix D).
func TestMigration711HostedTasksContract(t *testing.T) {
	up, err := os.ReadFile("711_hosted_tasks.sql")
	if err != nil {
		t.Fatalf("read migration failed: %v", err)
	}
	down, err := os.ReadFile("711_hosted_tasks.down.sql")
	if err != nil {
		t.Fatalf("read down migration failed: %v", err)
	}
	for name, sql := range map[string]string{"up": string(up), "down": string(down)} {
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Errorf("711 %s migration must be transactional", name)
		}
	}
	upText := string(up)

	// Idempotent re-apply discipline: IF NOT EXISTS on every CREATE TABLE.
	for _, table := range []string{"hosted_tasks", "hosted_task_events", "hosted_task_callbacks"} {
		if !strings.Contains(upText, "CREATE TABLE IF NOT EXISTS "+table+" ") {
			t.Errorf("711 must create %s idempotently (IF NOT EXISTS)", table)
		}
	}

	// State machine (§4.2): all nine statuses in the CHECK.
	for _, status := range []string{
		"delegated", "dispatching", "running", "completing",
		"completed", "failed", "needs_review", "cancelled", "expired",
	} {
		if !strings.Contains(upText, "'"+status+"'") {
			t.Errorf("711 status CHECK missing %q", status)
		}
	}

	// Event taxonomy (§4.3) pinned in the events CHECK.
	for _, ev := range []string{
		"accepted", "dispatch_degraded", "running", "progress", "cancel_requested",
		"completed", "failed", "expired", "cancelled", "callback_delivered", "callback_dlq",
	} {
		if !strings.Contains(upText, "'"+ev+"'") {
			t.Errorf("711 event_type CHECK missing %q", ev)
		}
	}

	// Idempotency + CAS + terminal-sticky contract columns.
	for _, needle := range []string{
		"UNIQUE (tenant_id, idempotency_key)",
		"CONSTRAINT hosted_tasks_terminal_complete",
		"CONSTRAINT hosted_tasks_status_check",
		"PRIMARY KEY (task_id, seq)",
		"REFERENCES hosted_tasks(id) ON DELETE CASCADE",
		"sse_cursor",
		"dispatch_key",
		"result_version",
	} {
		if !strings.Contains(upText, needle) {
			t.Errorf("711 missing contract element %q", needle)
		}
	}

	// RLS: enable + tenant isolation + bypass on all three tables (§6.1:
	// bypass 仅 worker 角色). Six policies total.
	if got := strings.Count(upText, "ENABLE ROW LEVEL SECURITY"); got != 3 {
		t.Errorf("711 must ENABLE RLS on exactly 3 tables, got %d", got)
	}
	for _, table := range []string{"hosted_tasks", "hosted_task_events", "hosted_task_callbacks"} {
		if !strings.Contains(upText, "CREATE POLICY "+table+"_tenant_isolation ON "+table) {
			t.Errorf("711 missing tenant isolation policy for %s", table)
		}
		if !strings.Contains(upText, "CREATE POLICY "+table+"_super_admin_bypass ON "+table) {
			t.Errorf("711 missing bypass policy for %s", table)
		}
	}

	// Down must drop in reverse dependency order (children first).
	downText := string(down)
	if strings.Index(downText, "DROP TABLE IF EXISTS hosted_task_callbacks") >
		strings.Index(downText, "DROP TABLE IF EXISTS hosted_tasks") {
		t.Error("711 down must drop child tables before hosted_tasks")
	}
}

// TestMigration711FreshUpDownUpAndRLS runs the acceptance-matrix-D fixture
// against a live PostgreSQL: fresh up → down → up, then a NOSUPERUSER /
// NOBYPASSRLS tenant probe proving tenant A cannot see tenant B rows.
// Convention (migration_532): TEST_PG_URL must point at a disposable DB;
// unset env → skip (never a fake PASS).
func TestMigration711FreshUpDownUpAndRLS(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; migration 711 integration test requires a live PostgreSQL (convention: tests/integration gating)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol // multi-statement files
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect TEST_PG_URL: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	upSQL, err := os.ReadFile("711_hosted_tasks.sql")
	if err != nil {
		t.Fatalf("read up: %v", err)
	}
	downSQL, err := os.ReadFile("711_hosted_tasks.down.sql")
	if err != nil {
		t.Fatalf("read down: %v", err)
	}

	apply := func(label, sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("apply %s failed: %v", label, err)
		}
	}
	apply("up#1", string(upSQL))
	apply("down", string(downSQL))
	apply("up#2 (fresh up/down/up)", string(upSQL))

	// POST_CONDITION: constraints + policies exist after re-apply.
	var policies int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_policies
		WHERE tablename IN ('hosted_tasks','hosted_task_events','hosted_task_callbacks')
	`).Scan(&policies); err != nil || policies != 6 {
		t.Fatalf("expected 6 RLS policies after fresh up/down/up, got %d (err=%v)", policies, err)
	}

	// RLS negative matrix: unprivileged tenant-scoped session cannot read
	// another tenant's rows. The apply/test connection is usually the table
	// owner (RLS exempt), so self-provision a NOSUPERUSER+NOBYPASSRLS probe
	// role; if that is impossible the matrix skips (never fake-passes).
	var rolSuper, rolBypass bool
	if err := conn.QueryRow(ctx, `
		SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user
	`).Scan(&rolSuper, &rolBypass); err != nil {
		t.Fatalf("resolve current role: %v", err)
	}
	probeDSN := dsn
	if rolSuper || rolBypass {
		if _, err := conn.Exec(ctx, `
			DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ht711_probe') THEN
					CREATE ROLE ht711_probe LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'ht711_probe';
				END IF;
			END $$;
			GRANT USAGE ON SCHEMA public TO ht711_probe;
			GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ht711_probe;
		`); err != nil {
			t.Skipf("cannot provision NOSUPERUSER+NOBYPASSRLS probe role: %v", err)
		}
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("reparse dsn: %v", err)
		}
		u.User = url.UserPassword("ht711_probe", "ht711_probe")
		probeDSN = u.String()
	}

	probeCfg, err := pgx.ParseConfig(probeDSN)
	if err != nil {
		t.Fatalf("parse probe dsn: %v", err)
	}
	probeCfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	probe, err := pgx.ConnectConfig(ctx, probeCfg)
	if err != nil {
		t.Fatalf("connect probe: %v", err)
	}
	defer func() { _ = probe.Close(context.Background()) }()
	if pSuper, pBypass := rolSuper && probeDSN == dsn, rolBypass && probeDSN == dsn; pSuper || pBypass {
		t.Skipf("probe role %s is SUPERUSER/BYPASSRLS; RLS negative matrix cannot run", probeCfg.User)
	}

	// Fixture: one row owned by tenant-A, inserted via the owner connection.
	apply("rls fixture", `
		INSERT INTO hosted_tasks (id, tenant_id, goal, deadline_at, idempotency_key)
		VALUES ('ht_rls_test', 'tenant-A', 'goal-A', NOW() + INTERVAL '1 hour', 'idem-A')
		ON CONFLICT DO NOTHING`)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DELETE FROM hosted_tasks WHERE id = 'ht_rls_test'")
	})

	setTenant := func(tenant string) {
		t.Helper()
		if _, err := probe.Exec(ctx, "SET app.current_tenant = '"+tenant+"'"); err != nil {
			t.Fatalf("set tenant GUC: %v", err)
		}
	}

	// Tenant A sees its own row.
	setTenant("tenant-A")
	var n int
	if err := probe.QueryRow(ctx, `SELECT count(*) FROM hosted_tasks WHERE id='ht_rls_test'`).Scan(&n); err != nil {
		t.Fatalf("tenant-A select: %v", err)
	}
	if n != 1 {
		t.Fatalf("tenant-A expected 1 row, got %d", n)
	}

	// Tenant B sees nothing (统一 404 语义的库层基础).
	setTenant("tenant-B")
	if err := probe.QueryRow(ctx, `SELECT count(*) FROM hosted_tasks WHERE id='ht_rls_test'`).Scan(&n); err != nil {
		t.Fatalf("tenant-B select: %v", err)
	}
	if n != 0 {
		t.Fatalf("tenant-B must not see tenant-A rows, got %d", n)
	}

	// No GUC → no rows (fail-closed).
	if _, err := probe.Exec(ctx, "RESET app.current_tenant"); err == nil {
		if err := probe.QueryRow(ctx, `SELECT count(*) FROM hosted_tasks WHERE id='ht_rls_test'`).Scan(&n); err == nil && n != 0 {
			t.Fatalf("unset GUC must not leak tenant-A rows, got %d", n)
		}
	}
}
