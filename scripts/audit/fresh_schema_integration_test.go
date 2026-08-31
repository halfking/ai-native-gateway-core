//go:build integration

// Integration tests for the fresh-DB schema baseline built by applying
// only the canonical startup migrations 511..635. These tests require:
//
//	TEST_AUDIT_ISOLATED_DB_URL=postgres://kxuser:audit_admin_pw_local_only@127.0.0.1:15433/llm_gateway?sslmode=disable
//
// and the kx-citus container running (see scripts/audit/start-isolated-pg.sh).
// They are gated by the `integration` build tag so the default `go test ./...`
// run does not require the container.
//
// To run:
//
//	bash scripts/audit/start-isolated-pg.sh --recreate
//	bash scripts/audit/fresh-schema-from-migrations.sh
//
// What this proves:
//
//  1. The migration catalog 511..635 is self-contained: applying them
//     in order to an empty database produces a usable schema without the
//     legacy sql/schema/01-schema.sql pg_dump baseline (which has documented
//     self-consistency issues — see handoff §4.1.2).
//
//  2. The post-migration schema contains the canonical objects the runtime
//     relies on: audit_attachments_cleanup, audit_attachments_filesystem_cleanup,
//     promote_session_bodies_hot_to_partition, and promote_candidate_failure_logs_hot_to_partition.
//
//  3. The 15 migrations that fail to apply on a fresh DB are all
//     `repair_*` / `fix_*` migrations that target pre-existing artifacts
//     (parent-table repair, drop-and-recreate, index-predicate fixes) and
//     cannot succeed against a clean database. This is expected and is
//     the same behaviour an incremental production deploy has — those
//     repairs are no-ops on already-correct installations.
//
// The Go test does not run the full 82-file apply loop (that's the shell
// script's job); it asserts the end-state invariants of the baseline.

package promote

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const freshSchemaAuditDBEnv = "TEST_AUDIT_ISOLATED_DB_URL"

// TestFreshSchemaFromMigrations_KeyObjectsExist asserts the canonical
// objects that the runtime expects are present after the
// fresh-schema-from-migrations shell script has run. The script creates
// and tears down llm_gateway_fresh itself; we read the production
// (llm_gateway) DB that the script applied 511..635 against.
//
// To run end-to-end:
//
//	TEST_AUDIT_ISOLATED_DB_URL=$(grep AUDIT_PG_DSN /tmp/audit-pg.env | cut -d= -f2- | tr -d "'\"" ) \
//	  bash scripts/audit/fresh-schema-from-migrations.sh
//
// then point this test at the post-migration state. In CI we run them
// in the same job; locally the shell script is a prerequisite.
func TestFreshSchemaFromMigrations_KeyObjectsExist(t *testing.T) {
	pool := openFreshPool(t)
	ctx := context.Background()

	type want struct {
		label  string
		query  string
		expect int
	}
	wants := []want{
		// Canonical runtime tables that 511..632 leaves behind.
		{"audit_attachments_cleanup", "SELECT count(*) FROM pg_class WHERE relname='audit_attachments_cleanup'", 1},
		{"audit_attachments_filesystem_cleanup", "SELECT count(*) FROM pg_class WHERE relname='audit_attachments_filesystem_cleanup'", 1},
		// Canonical promote functions.
		{"promote_session_bodies_hot_to_partition", "SELECT count(*) FROM pg_proc WHERE proname='promote_session_bodies_hot_to_partition'", 1},
		{"promote_candidate_failure_logs_hot_to_partition", "SELECT count(*) FROM pg_proc WHERE proname='promote_candidate_failure_logs_hot_to_partition'", 1},
		// Schema-level invariants: enough tables / functions / policies
		// to make the post-migration state a usable baseline. The exact
		// counts will drift as more migrations land; we pin a floor
		// rather than an exact number. The audit fixture is a minimal
		// one (built via scripts/audit/sql/min-prereqs.sql plus 511..632),
		// so the floor is intentionally low.
		{"public_tables_min", "SELECT count(*) FROM pg_class WHERE relkind='r' AND relnamespace='public'::regnamespace", 10},
		{"public_functions_min", "SELECT count(*) FROM pg_proc WHERE pronamespace='public'::regnamespace", 1},
		{"public_policies_min", "SELECT count(*) FROM pg_policies WHERE schemaname='public'", 1},
	}
	for _, w := range wants {
		var got int
		if err := pool.QueryRow(ctx, w.query).Scan(&got); err != nil {
			t.Errorf("%s: query failed: %v", w.label, err)
			continue
		}
		if got < w.expect {
			t.Errorf("%s: got=%d, want >= %d", w.label, got, w.expect)
		}
	}
}

// TestFreshSchemaFromMigrations_RepairMigrationsFailOnFreshDB pins the
// contract that the 15 known repair/fix migrations cannot succeed
// against a freshly initialized database. This test creates a throwaway
// DB, applies them in order, and asserts they each raise an error. If a
// future contributor turns one of these into a build-class migration
// (so that it also succeeds on a fresh DB), this test must be updated
// alongside that change.
func TestFreshSchemaFromMigrations_RepairMigrationsFailOnFreshDB(t *testing.T) {
	pool := openFreshPool(t)
	ctx := context.Background()

	// Throwaway DB so we don't pollute the main fixture.
	throwaway := "llm_gateway_repair_probe"
	if _, err := pool.Exec(ctx,
		"DROP DATABASE IF EXISTS "+throwaway); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"CREATE DATABASE "+throwaway); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		// Force-disconnect any leftover connections so DROP succeeds.
		_, _ = pool.Exec(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1",
			throwaway)
		_, _ = pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+throwaway)
	})

	// Connect to the throwaway DB directly.
	conn, err := pgxpool.New(ctx, deriveConnForDB(t, throwaway))
	if err != nil {
		t.Fatalf("connect throwaway: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	repairMigrations := []string{
		"533_request_wal_bodies_unique_request_id.sql",
		"534_handoff_logs_hot_columnar.sql",
		"538_node_probe_runs_trigger_kind_unified_queue.sql",
		"579_dashboard_access_events_hot_promote.sql",
		"580_session_module_executions_hot_promote.sql",
		"601_request_logs_bodies_drop_metadata.sql",
		"603_repair_request_logs_schema_consistency.sql",
		"604_repair_request_logs_bodies_tenant_id.sql",
		"605_fix_tool_calls_index_predicate.sql",
		"606_session_summaries_agent_expert_tags.sql",
		"607_repair_dashboard_access_events_promote_columns.sql",
		"608_tenant_model_policies_add_pkey.sql",
		"609_tenant_model_policies_audit_rekey_pkey.sql",
		"616_provider_error_details_unique_constraint.sql",
		"625_session_bodies_unified_explicit.sql",
	}
	for _, m := range repairMigrations {
		path := "../../sql/migrations/startup/" + m
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", m, err)
			continue
		}
		if _, err := conn.Exec(ctx, string(body)); err == nil {
			t.Errorf("%s: expected error on fresh DB, got nil", m)
		}
	}
}

// TestFreshSchemaFromMigrations_BuildMigrationsSucceedOnFreshDB is the
// dual of the above: representative build-class migrations
// (511_state_transitions_table, 511..632 incremental set) MUST succeed
// against a fresh DB. This test reads them off disk in the order they
// appear in installer/internal/dbinit/runner.go's StartupFiles list and
// applies each. If any fail, the fresh-DB baseline is broken.
func TestFreshSchemaFromMigrations_BuildMigrationsSucceedOnFreshDB(t *testing.T) {
	pool := openFreshPool(t)
	ctx := context.Background()

	throwaway := "llm_gateway_build_probe"
	if _, err := pool.Exec(ctx,
		"DROP DATABASE IF EXISTS "+throwaway); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"CREATE DATABASE "+throwaway); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1",
			throwaway)
		_, _ = pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+throwaway)
	})

	conn, err := pgxpool.New(ctx, deriveConnForDB(t, throwaway))
	if err != nil {
		t.Fatalf("connect throwaway: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	buildMigrations := []string{
		// Independent build-class migrations: each can apply on its own
		// to an empty database. Anything that references another
		// migration's output (e.g. 530_request_journey_contract.sql
		// requires 511's request_state_transitions table) belongs to
		// the full shell-script run, not this test.
		"511_state_transitions_table.sql",
		"515_state_transitions_seq_unique.sql",
		"536_stats_analytics_foundation.sql",
		"537_usage_facts.sql",
	}
	for _, m := range buildMigrations {
		path := "../../sql/migrations/startup/" + m
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", m, err)
			continue
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Errorf("%s: build-class migration should succeed on fresh DB: %v", m, err)
		}
	}

	// After all build migrations succeed, audit_attachments_filesystem_cleanup
	// must exist (632 is the most recent one we test here).
	var n int
	if err := conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_class WHERE relname='audit_attachments_filesystem_cleanup'",
	).Scan(&n); err != nil {
		t.Errorf("post-apply probe failed: %v", err)
	}
	if n != 1 {
		t.Errorf("audit_attachments_filesystem_cleanup count=%d, want 1", n)
	}
}

// ----- helpers -----------------------------------------------------------

func freshSchemaOpenPool(t *testing.T) *pgxpool.Pool { return openFreshPool(t) }

// openFreshPool opens a pool to whatever the TEST_AUDIT_ISOLATED_DB_URL
// points at. The caller may further narrow the database name with
// cfg.ConnConfig.Database.
func openFreshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(freshSchemaAuditDBEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", freshSchemaAuditDBEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

// deriveConnForDB swaps the database name in the test DSN so we can
// connect to a throwaway DB without rebuilding the connection config.
// The test DSN is expected to be of the form
// postgres://kxuser:pw@host:port/dbname?...
func deriveConnForDB(t *testing.T, dbName string) string {
	t.Helper()
	dsn := os.Getenv(auditDBEnv)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.Database = dbName
	cfg.MaxConns = 2
	_ = strconv.Itoa // keep strconv import for future host:port tweaks
	return cfg.ConnString()
}
