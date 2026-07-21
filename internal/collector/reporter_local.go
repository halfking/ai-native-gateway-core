package collector

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LocalDBReporter writes runtime metrics to the local runtime_metrics table
// (migration 433). Used alongside HTTPReporter to enable local diagnostics
// of load-vs-latency correlation without relying on Authority ingest.
type LocalDBReporter struct {
	Pool       *pgxpool.Pool
	InstanceID string
	LicenseID  *int64
}

// Report implements Reporter by inserting into runtime_metrics.
func (r *LocalDBReporter) Report(ctx context.Context, payload []byte) error {
	if r.Pool == nil {
		return nil // Graceful no-op when DB unavailable
	}

	var metrics RuntimeMetrics
	if err := json.Unmarshal(payload, &metrics); err != nil {
		return err
	}

	modelUsageJSON, err := json.Marshal(metrics.ModelUsage)
	if err != nil {
		return err
	}
	if metrics.ModelUsage == nil {
		modelUsageJSON = []byte("{}")
	}
	// 2026-07-22: 转为 string 以配合 ::text::jsonb cast，避免 pgx 二进制协议的 22P02 错误
	modelUsageStr := string(modelUsageJSON)

	_, err = r.Pool.Exec(ctx, `
		INSERT INTO runtime_metrics (
			instance_id, license_id, timestamp,
			cpu_usage_pct, mem_used_mb, mem_total_mb, disk_used_gb, disk_total_gb,
			db_size_mb, uptime_secs,
			current_concurrency, last_5min_tps, last_5min_p50_ms, last_5min_p99_ms,
			last_5min_success_pct, model_usage, tenant_count
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7, $8,
			$9, $10,
			$11, $12, $13, $14,
			$15, $16::text::jsonb, $17
		)
	`, metrics.InstanceID, r.LicenseID, metrics.Timestamp,
		metrics.CPUUsagePct, metrics.MemUsedMB, metrics.MemTotalMB, metrics.DiskUsedGB, metrics.DiskTotalGB,
		metrics.DBSizeMB, metrics.UptimeSecs,
		metrics.CurrentConcurrency, metrics.Last5MinTPS, metrics.Last5MinP50Ms, metrics.Last5MinP99Ms,
		metrics.Last5MinSuccessPct, modelUsageStr, metrics.TenantCount,
	)
	return err
}
