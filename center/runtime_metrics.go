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

// RuntimeMetricPoint is a single telemetry sample for admin detail views.
type RuntimeMetricPoint struct {
	Timestamp          time.Time `json:"timestamp"`
	CPUUsagePct        float64   `json:"cpu_usage_pct"`
	MemUsedMB          int64     `json:"mem_used_mb"`
	MemTotalMB         int64     `json:"mem_total_mb"`
	DiskUsedGB         int64     `json:"disk_used_gb"`
	DiskTotalGB        int64     `json:"disk_total_gb"`
	DBSizeMB           int64     `json:"db_size_mb"`
	UptimeSecs         int64     `json:"uptime_secs"`
	CurrentConcurrency int       `json:"current_concurrency"`
	Last5MinTPS        float64   `json:"last_5min_tps"`
	Last5MinP50Ms      float64   `json:"last_5min_p50_ms"`
	Last5MinP99Ms      float64   `json:"last_5min_p99_ms"`
	Last5MinSuccessPct float64   `json:"last_5min_success_pct"`
	TenantCount        int       `json:"tenant_count"`
}

// ListRuntimeMetricsForInstance returns recent runtime_metrics samples (newest first).
func (s *PgxStore) ListRuntimeMetricsForInstance(ctx context.Context, instanceID string, since time.Time, limit int) ([]RuntimeMetricPoint, error) {
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	rows, err := s.db.Query(ctx, `
		SELECT timestamp,
		       COALESCE(cpu_usage_pct, 0),
		       COALESCE(mem_used_mb, 0),
		       COALESCE(mem_total_mb, 0),
		       COALESCE(disk_used_gb, 0),
		       COALESCE(disk_total_gb, 0),
		       COALESCE(db_size_mb, 0),
		       COALESCE(uptime_secs, 0),
		       COALESCE(current_concurrency, 0),
		       COALESCE(last_5min_tps, 0),
		       COALESCE(last_5min_p50_ms, 0),
		       COALESCE(last_5min_p99_ms, 0),
		       COALESCE(last_5min_success_pct, 0),
		       COALESCE(tenant_count, 0)
		FROM runtime_metrics
		WHERE instance_id = $1 AND timestamp >= $2
		ORDER BY timestamp DESC
		LIMIT $3
	`, instanceID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RuntimeMetricPoint
	for rows.Next() {
		var p RuntimeMetricPoint
		if err := rows.Scan(
			&p.Timestamp, &p.CPUUsagePct, &p.MemUsedMB, &p.MemTotalMB,
			&p.DiskUsedGB, &p.DiskTotalGB, &p.DBSizeMB, &p.UptimeSecs,
			&p.CurrentConcurrency, &p.Last5MinTPS, &p.Last5MinP50Ms, &p.Last5MinP99Ms,
			&p.Last5MinSuccessPct, &p.TenantCount,
		); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	if items == nil {
		items = []RuntimeMetricPoint{}
	}
	return items, rows.Err()
}

// ListRuntimeAlertsForInstance returns open/recent alerts for one instance.
func (s *PgxStore) ListRuntimeAlertsForInstance(ctx context.Context, instanceID string, limit int) ([]RuntimeAlertEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, rule_key, instance_id, severity, title, message, status,
		       COALESCE(metric_value, 0), detected_at,
		       acked_at, COALESCE(acked_by, ''), resolved_at, COALESCE(resolved_by, ''),
		       suppressed_until
		FROM runtime_alerts
		WHERE instance_id = $1
		  AND status IN ('triggered', 'acknowledged', 'suppressed')
		ORDER BY detected_at DESC
		LIMIT $2
	`, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RuntimeAlertEvent
	for rows.Next() {
		var e RuntimeAlertEvent
		if err := rows.Scan(
			&e.ID, &e.RuleKey, &e.InstanceID, &e.Severity, &e.Title, &e.Message, &e.Status,
			&e.MetricValue, &e.DetectedAt,
			&e.AckedAt, &e.AckedBy, &e.ResolvedAt, &e.ResolvedBy, &e.SuppressedUntil,
		); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	if items == nil {
		items = []RuntimeAlertEvent{}
	}
	return items, rows.Err()
}
