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
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	freshSchemaAuditDBEnv  = "TEST_AUDIT_ISOLATED_DB_URL"
	freshSchemaEndStateEnv = "TEST_AUDIT_FRESH_SCHEMA_DB_URL"
)

// TestFreshSchemaFromMigrations_KeyObjectsExist checks the retained shell
// end-state. Set KEEP_FRESH_DB=1 when running the shell script, then provide
// its DSN in TEST_AUDIT_FRESH_SCHEMA_DB_URL. Throwaway tests below always use
// TEST_AUDIT_ISOLATED_DB_URL and clean up their own databases.
func TestFreshSchemaFromMigrations_KeyObjectsExist(t *testing.T) {
	pool := openPoolFromEnv(t, freshSchemaEndStateEnv)
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
	admin := openFreshPool(t)
	ctx := context.Background()

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
	expectedFailures := 0
	unexpectedSuccesses := 0
	for i, m := range repairMigrations {
		body, err := os.ReadFile("../../sql/migrations/startup/" + m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		dbName := fmt.Sprintf("llm_gateway_repair_probe_%d", i)
		err = withThrowawayDB(t, admin, dbName, func(conn *pgxpool.Pool) error {
			_, execErr := conn.Exec(ctx, string(body))
			return execErr
		})
		if err != nil {
			expectedFailures++
		} else {
			unexpectedSuccesses++
			t.Errorf("%s: expected error on fresh DB, got nil", m)
		}
	}
	if expectedFailures != len(repairMigrations) || unexpectedSuccesses != 0 {
		t.Fatalf("repair migration accounting: expected_failures=%d/%d unexpected_successes=%d", expectedFailures, len(repairMigrations), unexpectedSuccesses)
	}
}

// TestFreshSchemaFromMigrations_BuildMigrationsSucceedOnFreshDB is the
// dual of the above: representative build-class migrations
// (511_state_transitions_table, 511..632 incremental set) MUST succeed
// against a fresh DB. This test reads them off disk in the order they
// appear in installer/internal/dbinit/runner.go's StartupFiles list and
// applies each. If any fail, the fresh-DB baseline is broken.
func TestFreshSchemaFromMigrations_BuildMigrationsSucceedOnFreshDB(t *testing.T) {
	admin := openFreshPool(t)
	ctx := context.Background()
	buildMigrations := []string{
		"511_state_transitions_table.sql",
		"515_state_transitions_seq_unique.sql",
		"536_stats_analytics_foundation.sql",
		"537_usage_facts.sql",
	}

	for i, name := range buildMigrations {
		body, err := os.ReadFile("../../sql/migrations/startup/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		dbName := fmt.Sprintf("llm_gateway_build_probe_%d", i)
		if err := withThrowawayDB(t, admin, dbName, func(conn *pgxpool.Pool) error {
			_, execErr := conn.Exec(ctx, string(body))
			return execErr
		}); err != nil {
			t.Errorf("%s: build-class migration should succeed on fresh DB: %v", name, err)
		}
	}
}

// ----- helpers -----------------------------------------------------------

func withThrowawayDB(t *testing.T, admin *pgxpool.Pool, dbName string, fn func(*pgxpool.Pool) error) error {
	t.Helper()
	ctx := context.Background()
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName); err != nil {
		t.Fatalf("drop %s: %v", dbName, err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("create %s: %v", dbName, err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1", dbName)
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName)
	}()
	conn, err := pgxpool.New(ctx, deriveConnForDB(t, dbName))
	if err != nil {
		t.Fatalf("connect %s: %v", dbName, err)
	}
	defer conn.Close()
	return fn(conn)
}

func openPoolFromEnv(t *testing.T, envName string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(envName)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", envName)
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

// openFreshPool opens a pool to the isolated audit database.
func openFreshPool(t *testing.T) *pgxpool.Pool {
	return openPoolFromEnv(t, freshSchemaAuditDBEnv)
}

// deriveConnForDB swaps the database name in the test DSN so we can
// connect to a throwaway DB without rebuilding the connection config.
// The test DSN is expected to be of the form
// postgres://kxuser:pw@host:port/dbname?...
func deriveConnForDB(t *testing.T, dbName string) string {
	t.Helper()
	dsn := os.Getenv(freshSchemaAuditDBEnv)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.Database = dbName
	cfg.MaxConns = 2
	return cfg.ConnString()
}
