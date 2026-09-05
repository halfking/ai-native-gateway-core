//go:build integration

package stats

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestReconciliationDiffIdentity_548(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = pgContainer.Terminate(cleanupCtx)
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(connStr)
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
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE schema_migrations (
			version text PRIMARY KEY,
			description text NOT NULL DEFAULT '',
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"../../sql/migrations/startup/536_stats_analytics_foundation.sql",
		"../../sql/migrations/startup/539_stats_reconciliation_tenant.sql",
		"../../sql/migrations/startup/546_stats_reconciliation_diffs_unique.sql",
		"../../sql/migrations/startup/548_stats_reconciliation_diffs_identity.sql",
	} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	if _, err := conn.Exec(ctx, `
		INSERT INTO stats_reconciliation_runs (run_id, period_start, period_end, scope)
		VALUES ('recon_identity', now(), now() + interval '1 day', 'test')`); err != nil {
		t.Fatal(err)
	}

	insert := `
		INSERT INTO stats_reconciliation_diffs
			(run_id, tenant_id, dimension_type, dimension_key, metric, source_value, projected_value, difference)
		VALUES ('recon_identity', $1, 'daily_rollup', $2, 'total_tokens', 100, 90, 10)`
	dayOne := "tenant:tenant-a:day:2026-08-20:provider:1:cred:0:model:10:gpt-4"
	dayTwo := "tenant:tenant-a:day:2026-08-21:provider:1:cred:0:model:10:gpt-4"
	tenantTwo := "tenant:tenant-b:day:2026-08-20:provider:1:cred:0:model:10:gpt-4"

	if _, err := conn.Exec(ctx, insert, "tenant-a", dayOne); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, insert, "tenant-a", dayTwo); err != nil {
		t.Fatalf("different UTC days must coexist: %v", err)
	}
	if _, err := conn.Exec(ctx, insert, "tenant-b", tenantTwo); err != nil {
		t.Fatalf("different tenants must coexist: %v", err)
	}
	if _, err := conn.Exec(ctx, insert, "tenant-a", dayOne); err == nil {
		t.Fatal("same tenant/day/dimension/metric must violate migration 548 unique identity")
	}
}
