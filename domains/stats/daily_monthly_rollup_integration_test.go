//go:build integration

package stats

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	dto "github.com/prometheus/client_model/go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// readClosedSkippedCounter reads the current value of
// llm_gateway_stats_monthly_closed_skipped_total via the test accessor
// so we don't leak the internal symbol.
func readClosedSkippedCounter(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := metrics.StatsMonthlyClosedSkippedVec().Write(m); err != nil {
		t.Fatalf("StatsMonthlyClosedSkippedVec().Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

// setupRollupContainer starts a fresh postgres container, applies the
// 536 foundation schema (which is the only schema rollup requires),
// and returns a pool + raw conn for seeding.
func setupRollupContainer(t *testing.T, ctx context.Context) (*pgxpool.Pool, *pgx.Conn) {
	t.Helper()
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
	t.Cleanup(func() { _ = conn.Close(ctx) })

	body, err := os.ReadFile("../../sql/migrations/startup/536_stats_analytics_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply 536: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool, conn
}

// TestRollup_NoClosedRows asserts that when the target month range has
// no status='closed' rows in stats_usage_monthly, Refresh completes
// without surfacing the closed-skip counter (no warn, no metric
// increment). This is the happy path: a freshly-deployed database has
// no closed months.
func TestRollup_NoClosedRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, conn := setupRollupContainer(t, ctx)

	// Capture slog output so we can assert no warn was emitted.
	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	counterBefore := readClosedSkippedCounter(t)

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Insert one stats_usage_daily row for the current month.
	if _, err := conn.Exec(ctx, `
		INSERT INTO stats_usage_daily
			(day_utc, tenant_id, provider_id, canonical_id, raw_model_name,
			 dimension_type, dimension_key, traffic_class,
			 request_count, success_count, prompt_tokens, completion_tokens,
			 total_tokens, cost_usd, credits_charged)
		VALUES
			($1, 't1', 1, 10, 'gpt-4', 'provider_model', 'p:1:m:10',
			 'business', 3, 3, 100, 50, 150, 0.01, 150)
	`, today); err != nil {
		t.Fatal(err)
	}

	// Run Refresh over the current month.
	rollup := NewDailyMonthlyRollup(pool, time.Hour)
	if err := rollup.Refresh(ctx, today, today.Add(48*time.Hour)); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	counterAfter := readClosedSkippedCounter(t)
	if counterAfter != counterBefore {
		t.Fatalf("llm_gateway_stats_monthly_closed_skipped_total delta = %v, want 0 (no closed rows in target range)",
			counterAfter-counterBefore)
	}

	if strings.Contains(logBuf.String(), "stats monthly rollup suppressed by closed rows") {
		t.Fatalf("unexpected warn log emitted when no closed rows present:\n%s", logBuf.String())
	}

	// Sanity: monthly row was created.
	var monthlyRows int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM stats_usage_monthly`).Scan(&monthlyRows); err != nil {
		t.Fatal(err)
	}
	if monthlyRows <= 0 {
		t.Fatalf("expected at least one stats_usage_monthly row, got 0")
	}
}

// TestRollup_ClosedRowsAreSurfaced is the regression test for P2-3: a
// row in stats_usage_monthly that is already status='closed' must
// surface skipped_closed_count > 0, log a slog.Warn, and increment
// llm_gateway_stats_monthly_closed_skipped_total. Previously
// (pre-fix), the closed row would be silently ignored because
// `ON CONFLICT ... DO UPDATE ... WHERE status <> 'closed'` degrades
// to DO NOTHING when the WHERE clause is false.
//
// The test seeds a closed monthly row in the target month, runs
// Refresh (which produces daily rows for the same day/month), and
// asserts the CTE surfaces exactly one closed-row hit.
func TestRollup_ClosedRowsAreSurfaced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, conn := setupRollupContainer(t, ctx)

	// Capture slog output so we can assert the Warn was emitted with
	// the expected key.
	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	counterBefore := readClosedSkippedCounter(t)

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)

	// Seed a closed stats_usage_monthly row for the current month.
	// This row has the same primary key tuple as the rollup will
	// attempt to upsert, so the WHERE status<>'closed' clause will
	// silently degrade to DO NOTHING — exactly the bug we are
	// guarding against.
	if _, err := conn.Exec(ctx, `
		INSERT INTO stats_usage_monthly
			(month_start, tenant_id, provider_id, credential_id, canonical_id,
			 raw_model_name, dimension_type, dimension_key, traffic_class,
			 request_count, success_count, status, closed_at, updated_at)
		VALUES
			($1, 't1', 1, 0, 10, 'gpt-4', 'provider_model', 'p:1:m:10',
			 'business', 99, 99, 'closed', now(), now())
	`, monthStart); err != nil {
		t.Fatal(err)
	}

	// Seed a daily row that will trigger the upsert (same PK tuple).
	if _, err := conn.Exec(ctx, `
		INSERT INTO stats_usage_daily
			(day_utc, tenant_id, provider_id, canonical_id, raw_model_name,
			 dimension_type, dimension_key, traffic_class,
			 request_count, success_count, prompt_tokens, completion_tokens,
			 total_tokens, cost_usd, credits_charged)
		VALUES
			($1, 't1', 1, 10, 'gpt-4', 'provider_model', 'p:1:m:10',
			 'business', 3, 3, 100, 50, 150, 0.01, 150)
	`, today); err != nil {
		t.Fatal(err)
	}

	rollup := NewDailyMonthlyRollup(pool, time.Hour)
	if err := rollup.Refresh(ctx, today, today.Add(48*time.Hour)); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	counterAfter := readClosedSkippedCounter(t)
	if delta := counterAfter - counterBefore; delta != 1 {
		t.Fatalf("llm_gateway_stats_monthly_closed_skipped_total delta = %v, want +1 (one closed row in target range)",
			delta)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "stats monthly rollup suppressed by closed rows") {
		t.Fatalf("expected warn log line not found; got:\n%s", logs)
	}
	if !strings.Contains(logs, `"skipped_closed_count":1`) {
		t.Fatalf("expected warn log to include skipped_closed_count=1; got:\n%s", logs)
	}

	// Sanity: the closed row's original request_count was preserved
	// (i.e. we did NOT overwrite an immutable month). The metric
	// surfaces the suppression; it does not undo it.
	var preservedCount int64
	if err := conn.QueryRow(ctx, `
		SELECT request_count FROM stats_usage_monthly
		WHERE month_start = $1 AND tenant_id = 't1' AND status = 'closed'
	`, monthStart).Scan(&preservedCount); err != nil {
		t.Fatal(err)
	}
	if preservedCount != 99 {
		t.Fatalf("closed row was overwritten: request_count = %d, want 99 (immutable)", preservedCount)
	}

	// Indirect sanity check that the new metrics helper is wired:
	// calling RecordStatsMonthlyClosedSkipped(0) must be a no-op, and
	// a positive call increments by exactly that amount.
	beforeHelper := readClosedSkippedCounter(t)
	metrics.RecordStatsMonthlyClosedSkipped(0)
	if got := readClosedSkippedCounter(t); got != beforeHelper {
		t.Fatalf("RecordStatsMonthlyClosedSkipped(0) must be a no-op, but counter went %v -> %v",
			beforeHelper, got)
	}
	metrics.RecordStatsMonthlyClosedSkipped(2)
	if got := readClosedSkippedCounter(t); got != beforeHelper+2 {
		t.Fatalf("RecordStatsMonthlyClosedSkipped(2): counter = %v, want %v", got, beforeHelper+2)
	}
}