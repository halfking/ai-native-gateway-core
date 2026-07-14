package center

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/collector"
)

func (s *PgxStore) InsertRuntimeMetrics(ctx context.Context, instanceID string, licenseID *int64, metrics collector.RuntimeMetrics) error {
	modelUsage, err := json.Marshal(metrics.ModelUsage)
	if err != nil {
		return err
	}
	if metrics.ModelUsage == nil {
		modelUsage = []byte("{}")
	}
	ts := metrics.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO runtime_metrics (
			instance_id, license_id, timestamp,
			cpu_usage_pct, mem_used_mb, mem_total_mb, disk_used_gb, disk_total_gb, db_size_mb, uptime_secs,
			current_concurrency, last_5min_tps, last_5min_p50_ms, last_5min_p99_ms, last_5min_success_pct,
			model_usage, tenant_count
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16::jsonb, $17
		)
	`, instanceID, licenseID, ts,
		metrics.CPUUsagePct, metrics.MemUsedMB, metrics.MemTotalMB, metrics.DiskUsedGB, metrics.DiskTotalGB, metrics.DBSizeMB, metrics.UptimeSecs,
		metrics.CurrentConcurrency, metrics.Last5MinTPS, metrics.Last5MinP50Ms, metrics.Last5MinP99Ms, metrics.Last5MinSuccessPct,
		modelUsage, metrics.TenantCount,
	)
	return err
}

func (s *PgxStore) GetInstanceHardwareHash(ctx context.Context, instanceID string) (string, error) {
	var hardwareHash string
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(hardware_hash, '')
		FROM gateway_instances
		WHERE instance_id = $1
	`, instanceID).Scan(&hardwareHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return hardwareHash, err
}

func (s *PgxStore) LookupTelemetryPreference(ctx context.Context, hardwareHash string) (licenseID int64, enabled bool, found bool, err error) {
	if hardwareHash == "" {
		return 0, false, false, nil
	}
	err = s.db.QueryRow(ctx, `
		SELECT license_id, enabled
		FROM runtime_telemetry_preferences
		WHERE hardware_hash = $1
	`, hardwareHash).Scan(&licenseID, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	return licenseID, enabled, true, nil
}

func (s *PgxStore) DeleteRuntimeMetricsForHardwareHash(ctx context.Context, hardwareHash string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM runtime_metrics
		WHERE instance_id IN (
			SELECT instance_id FROM gateway_instances WHERE hardware_hash = $1
		)
	`, hardwareHash)
	return err
}
