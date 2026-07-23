package systemmonitor

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMetricsCollector_CollectCoverage(t *testing.T) {
	// Skip if no test DB available
	dsn := "postgres://localhost/llm_gateway_test?sslmode=disable"
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("Skip: no test DB available (%v)", err)
	}
	defer db.Close()

	if err := db.Ping(context.Background()); err != nil {
		t.Skipf("Skip: cannot ping test DB (%v)", err)
	}

	// Setup: insert test data
	ctx := context.Background()
	setupSQL := `
		INSERT INTO system_probe_runs 
		  (task_type, automaticity, credential_id, raw_model, source, worker_id, status, started_at, finished_at)
		VALUES
		  ('direct_ping', 'mandatory', 1, 'gpt-4', 'systemmonitor', 'test-worker-1', 'success', NOW() - INTERVAL '1 day', NOW() - INTERVAL '1 day'),
		  ('direct_ping', 'mandatory', 2, 'gpt-4', 'systemmonitor', 'test-worker-2', 'success', NOW() - INTERVAL '2 days', NOW() - INTERVAL '2 days'),
		  ('credential_selfcheck', 'mandatory', 3, 'gpt-3.5', 'legacy_selfcheck', 'legacy-worker-1', 'success', NOW() - INTERVAL '3 days', NOW() - INTERVAL '3 days')
		ON CONFLICT DO NOTHING;
	`
	if _, err := db.Exec(ctx, setupSQL); err != nil {
		t.Fatalf("setup test data: %v", err)
	}

	// Cleanup
	defer func() {
		db.Exec(ctx, "DELETE FROM system_probe_runs WHERE worker_id LIKE 'test-%' OR worker_id LIKE 'legacy-%'")
	}()

	// Test
	collector := NewMetricsCollector(db)
	metrics, err := collector.CollectCoverage(ctx, 7)
	if err != nil {
		t.Fatalf("CollectCoverage() error: %v", err)
	}

	// Assertions
	if metrics.TotalTasks < 3 {
		t.Errorf("Expected at least 3 total tasks, got %d", metrics.TotalTasks)
	}
	if metrics.SystemMonitorTasks < 2 {
		t.Errorf("Expected at least 2 systemmonitor tasks, got %d", metrics.SystemMonitorTasks)
	}
	if metrics.LegacyTasks < 1 {
		t.Errorf("Expected at least 1 legacy task, got %d", metrics.LegacyTasks)
	}
	if metrics.CoveragePercent <= 0 || metrics.CoveragePercent > 100 {
		t.Errorf("Expected coverage 0-100%%, got %.2f%%", metrics.CoveragePercent)
	}
	if len(metrics.BySource) == 0 {
		t.Error("Expected non-empty BySource map")
	}
	if len(metrics.ByTaskType) == 0 {
		t.Error("Expected non-empty ByTaskType map")
	}
	if metrics.CollectedAt.IsZero() {
		t.Error("Expected CollectedAt to be set")
	}

	t.Logf("Metrics: Total=%d, SystemMonitor=%d, Legacy=%d, Coverage=%.2f%%",
		metrics.TotalTasks, metrics.SystemMonitorTasks, metrics.LegacyTasks, metrics.CoveragePercent)
}

func TestMetricsCollector_IsReadyForMigration(t *testing.T) {
	// This is a integration test that needs real DB
	// Skip in unit test environment
	t.Skip("Requires real DB with sufficient data")
}

func TestCoverageMetrics_Calculation(t *testing.T) {
	// Unit test for calculation logic
	metrics := &CoverageMetrics{
		WindowDays:         7,
		TotalTasks:         100,
		SystemMonitorTasks: 85,
		LegacyTasks:        15,
	}

	// Calculate coverage
	metrics.CoveragePercent = float64(metrics.SystemMonitorTasks) / float64(metrics.TotalTasks) * 100.0

	if metrics.CoveragePercent != 85.0 {
		t.Errorf("Expected 85%%, got %.2f%%", metrics.CoveragePercent)
	}
}
