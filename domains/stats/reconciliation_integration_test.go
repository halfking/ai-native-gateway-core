//go:build integration

package stats

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestReconciliation_PostgreSQL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Start PostgreSQL container
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

	// Apply schema migrations
	for _, name := range []string{
		"../../sql/migrations/startup/536_stats_analytics_foundation.sql",
		"../../sql/migrations/startup/537_usage_facts.sql",
		"../../sql/migrations/startup/539_stats_reconciliation_tenant.sql",
	} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Insert test data
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Insert usage_facts (source of truth)
	_, err = conn.Exec(ctx, `
		INSERT INTO usage_facts 
			(event_id, request_id, occurred_at, tenant_id, traffic_class, status,
			 provider_id, canonical_id, raw_model_name, 
			 prompt_tokens, completion_tokens, total_tokens, cost_amount, credits_charged)
		VALUES 
			('evt1', 'req1', $1, 'tenant1', 'business', 'success', 1, 10, 'gpt-4', 100, 50, 150, 0.01, 150),
			('evt2', 'req2', $1, 'tenant1', 'business', 'success', 1, 10, 'gpt-4', 200, 100, 300, 0.02, 300),
			('evt3', 'req3', $1, 'tenant1', 'business', 'error', 1, 10, 'gpt-4', 50, 0, 50, 0.005, 50)
	`, today.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	// Insert stats_usage_daily with intentional small discrepancy (within auto-repair threshold)
	// Facts: 3 requests, 2 success, 1 failure, 350 prompt, 150 completion, 500 total, 0.035 cost, 500 credits
	// Projection: slightly off by ~1% to trigger auto-repair
	_, err = conn.Exec(ctx, `
		INSERT INTO stats_usage_daily
			(day_utc, tenant_id, provider_id, canonical_id, raw_model_name, traffic_class,
			 dimension_type, dimension_key,
			 request_count, success_count, failure_count,
			 prompt_tokens, completion_tokens, total_tokens, cost_usd, credits_charged)
		VALUES 
			($1, 'tenant1', 1, 10, 'gpt-4', 'business', 'provider_model', 'provider:1:model:10',
			 3, 2, 1,
			 345, 148, 493, 0.0345, 495)
	`, today)
	if err != nil {
		t.Fatal(err)
	}

	// Run reconciliation
	worker := NewReconciliationWorker(pool, time.Hour)
	if worker == nil {
		t.Fatal("worker is nil")
	}

	err = worker.ReconcilePeriod(ctx, today, today.Add(24*time.Hour), "test")
	if err != nil {
		t.Fatal(err)
	}

	// Verify reconciliation run was created
	var runID, status string
	var eventsSeen, rowsCompared, diffCount int64
	err = conn.QueryRow(ctx, `
		SELECT run_id, status, events_seen, rows_compared, diff_count 
		FROM stats_reconciliation_runs 
		ORDER BY started_at DESC LIMIT 1
	`).Scan(&runID, &status, &eventsSeen, &rowsCompared, &diffCount)
	if err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Errorf("expected status=completed, got %s", status)
	}
	if eventsSeen != 3 {
		t.Errorf("expected events_seen=3, got %d", eventsSeen)
	}
	// diff_count represents unresolved diffs; all our diffs are auto-repairable, so it can be 0
	if rowsCompared <= 0 {
		t.Error("should have compared projection rows")
	}

	// Verify diffs were recorded (total diffs including auto-repaired)
	var diffCountActual int64
	err = conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM stats_reconciliation_diffs WHERE run_id = $1
	`, runID).Scan(&diffCountActual)
	if err != nil {
		t.Fatal(err)
	}
	if diffCountActual <= 0 {
		t.Error("should have recorded diffs")
	}

	// Verify some diffs are auto-repairable
	var autoRepaired int64
	err = conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM stats_reconciliation_diffs 
		WHERE run_id = $1 AND resolution = 'auto_repaired'
	`, runID).Scan(&autoRepaired)
	if err != nil {
		t.Fatal(err)
	}
	if autoRepaired <= 0 {
		t.Error("small diffs should be auto-repairable")
	}

	// Verify tenant_id is recorded in diffs for isolation
	var tenantID string
	err = conn.QueryRow(ctx, `
		SELECT DISTINCT tenant_id FROM stats_reconciliation_diffs WHERE run_id = $1
	`, runID).Scan(&tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if tenantID != "tenant1" {
		t.Errorf("expected tenant1, got %s", tenantID)
	}
}

func TestReconciliation_MissingProjection(t *testing.T) {
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

	// Apply schema migrations
	for _, name := range []string{
		"../../sql/migrations/startup/536_stats_analytics_foundation.sql",
		"../../sql/migrations/startup/537_usage_facts.sql",
		"../../sql/migrations/startup/539_stats_reconciliation_tenant.sql",
	} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Insert usage_facts but NO projection - this should be detected
	_, err = conn.Exec(ctx, `
		INSERT INTO usage_facts 
			(event_id, request_id, occurred_at, tenant_id, traffic_class, status,
			 provider_id, canonical_id, raw_model_name, 
			 prompt_tokens, completion_tokens, total_tokens, cost_amount, credits_charged)
		VALUES 
			('evt_missing', 'req_missing', $1, 'tenant2', 'business', 'success', 
			 2, 20, 'claude-3', 100, 50, 150, 0.01, 150),
			('evt_outside', 'req_outside', $2, 'tenant2', 'business', 'success',
			 2, 20, 'claude-3', 300, 100, 400, 0.02, 400)
	`, today.Add(12*time.Hour), today.Add(36*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `
		INSERT INTO stats_event_dedup (event_id, occurred_at)
		VALUES ('evt_missing', $1), ('evt_outside', $2)
	`, today.Add(12*time.Hour), today.Add(36*time.Hour)); err != nil {
		t.Fatal(err)
	}

	worker := NewReconciliationWorker(pool, time.Hour)
	err = worker.ReconcilePeriod(ctx, today, today.Add(24*time.Hour), "missing_test")
	if err != nil {
		t.Fatal(err)
	}

	// Verify missing projection was detected
	var diffCount int64
	err = conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM stats_reconciliation_diffs 
		WHERE tenant_id = 'tenant2' AND projected_value = 0 AND source_value > 0
	`).Scan(&diffCount)
	if err != nil {
		t.Fatal(err)
	}
	if diffCount <= 0 {
		t.Error("missing projection should be detected")
	}

	// Verify it's auto-repairable (rebuild will add it)
	var autoRepaired int64
	err = conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM stats_reconciliation_diffs 
		WHERE tenant_id = 'tenant2' AND resolution = 'auto_repaired'
	`).Scan(&autoRepaired)
	if err != nil {
		t.Fatal(err)
	}
	if autoRepaired <= 0 {
		t.Error("missing projection should be auto-repairable")
	}

	var dailyRequests, dailyTokens int64
	err = conn.QueryRow(ctx, `
		SELECT request_count, total_tokens
		FROM stats_usage_daily
		WHERE day_utc = $1 AND tenant_id = 'tenant2'
		  AND dimension_type = 'provider_model' AND dimension_key = 'claude-3'
	`, today).Scan(&dailyRequests, &dailyTokens)
	if err != nil {
		t.Fatalf("rebuilt daily projection missing: %v", err)
	}
	if dailyRequests != 1 || dailyTokens != 150 {
		t.Errorf("rebuilt daily projection=(requests=%d,tokens=%d), want (1,150)", dailyRequests, dailyTokens)
	}

	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	var monthlyRequests, monthlyTokens int64
	err = conn.QueryRow(ctx, `
		SELECT request_count, total_tokens
		FROM stats_usage_monthly
		WHERE month_start = $1 AND tenant_id = 'tenant2'
		  AND dimension_type = 'provider_model' AND dimension_key = 'claude-3'
	`, monthStart).Scan(&monthlyRequests, &monthlyTokens)
	if err != nil {
		t.Fatalf("rebuilt monthly projection missing: %v", err)
	}
	if monthlyRequests != 1 || monthlyTokens != 150 {
		t.Errorf("rebuilt monthly projection=(requests=%d,tokens=%d), want (1,150)", monthlyRequests, monthlyTokens)
	}
}
