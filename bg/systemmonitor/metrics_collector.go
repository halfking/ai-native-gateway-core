package systemmonitor

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MetricsCollector collects coverage metrics from system_probe_runs table.
// It tracks the migration progress from legacy workers to SystemMonitor.
type MetricsCollector struct {
	db *pgxpool.Pool
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector(db *pgxpool.Pool) *MetricsCollector {
	return &MetricsCollector{db: db}
}

// CoverageMetrics represents the coverage statistics for SystemMonitor adoption.
type CoverageMetrics struct {
	WindowDays         int              `json:"window_days"`
	TotalTasks         int64            `json:"total_tasks"`
	SystemMonitorTasks int64            `json:"system_monitor_tasks"`
	LegacyTasks        int64            `json:"legacy_tasks"`
	CoveragePercent    float64          `json:"coverage_percent"`
	BySource           map[string]int64 `json:"by_source"`
	ByTaskType         map[string]int64 `json:"by_task_type"`
	CollectedAt        time.Time        `json:"collected_at"`
}

// CollectCoverage queries system_probe_runs and calculates coverage metrics.
// windowDays: number of days to look back (default 7).
func (mc *MetricsCollector) CollectCoverage(ctx context.Context, windowDays int) (*CoverageMetrics, error) {
	if mc == nil || mc.db == nil {
		return nil, fmt.Errorf("collect coverage failed: database pool is unavailable (component=metrics_collector)")
	}
	if windowDays <= 0 {
		windowDays = 7
	}

	metrics := &CoverageMetrics{
		WindowDays:  windowDays,
		BySource:    make(map[string]int64),
		ByTaskType:  make(map[string]int64),
		CollectedAt: time.Now(),
	}

	// Query 1: Total task count
	totalQuery := `
		SELECT COUNT(*) 
		FROM system_probe_runs 
		WHERE started_at > NOW() - INTERVAL '1 day' * $1
	`
	if err := mc.db.QueryRow(ctx, totalQuery, windowDays).Scan(&metrics.TotalTasks); err != nil {
		return nil, fmt.Errorf("query total tasks: %w", err)
	}

	// Query 2: Tasks by source
	sourceQuery := `
		SELECT source, COUNT(*) as count
		FROM system_probe_runs
		WHERE started_at > NOW() - INTERVAL '1 day' * $1
		GROUP BY source
		ORDER BY count DESC
	`
	rows, err := mc.db.Query(ctx, sourceQuery, windowDays)
	if err != nil {
		return nil, fmt.Errorf("query by source: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var source string
		var count int64
		if err := rows.Scan(&source, &count); err != nil {
			return nil, fmt.Errorf("scan source row: %w", err)
		}
		metrics.BySource[source] = count

		// Categorize: "systemmonitor" vs "legacy_*"
		if source == "systemmonitor" {
			metrics.SystemMonitorTasks += count
		} else if source == "legacy_selfcheck" || source == "legacy_asset_health" {
			metrics.LegacyTasks += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source rows: %w", err)
	}

	// Query 3: Tasks by task_type
	typeQuery := `
		SELECT task_type, COUNT(*) as count
		FROM system_probe_runs
		WHERE started_at > NOW() - INTERVAL '1 day' * $1
		GROUP BY task_type
		ORDER BY count DESC
	`
	rows, err = mc.db.Query(ctx, typeQuery, windowDays)
	if err != nil {
		return nil, fmt.Errorf("query by task_type: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var taskType string
		var count int64
		if err := rows.Scan(&taskType, &count); err != nil {
			return nil, fmt.Errorf("scan task_type row: %w", err)
		}
		metrics.ByTaskType[taskType] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task_type rows: %w", err)
	}

	// Calculate coverage percentage
	if metrics.TotalTasks > 0 {
		metrics.CoveragePercent = float64(metrics.SystemMonitorTasks) / float64(metrics.TotalTasks) * 100.0
	}

	return metrics, nil
}

// IsReadyForMigration checks if the coverage is sufficient to migrate away from legacy workers.
// Threshold: ≥ 80% coverage for 7 consecutive days (simplified: checks current 7-day window).
func (mc *MetricsCollector) IsReadyForMigration(ctx context.Context) (bool, string, error) {
	metrics, err := mc.CollectCoverage(ctx, 7)
	if err != nil {
		return false, "", fmt.Errorf("collect coverage: %w", err)
	}

	const threshold = 80.0
	if metrics.CoveragePercent >= threshold {
		return true, fmt.Sprintf("Coverage %.2f%% ≥ %.0f%% (SystemMonitor: %d, Legacy: %d, Total: %d)",
			metrics.CoveragePercent, threshold,
			metrics.SystemMonitorTasks, metrics.LegacyTasks, metrics.TotalTasks), nil
	}

	return false, fmt.Sprintf("Coverage %.2f%% < %.0f%% (SystemMonitor: %d, Legacy: %d, Total: %d). Need more time.",
		metrics.CoveragePercent, threshold,
		metrics.SystemMonitorTasks, metrics.LegacyTasks, metrics.TotalTasks), nil
}
