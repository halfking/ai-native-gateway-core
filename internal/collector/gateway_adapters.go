package collector

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/licensing"
)

// PgTelemetryPrefReader checks runtime telemetry opt-in from preferences table.
type PgTelemetryPrefReader struct {
	Store licensing.RuntimeTelemetryPreferenceStore
	Hash  func() (string, error)
}

func (r *PgTelemetryPrefReader) Enabled(ctx context.Context) (bool, error) {
	if r.Store == nil || r.Hash == nil {
		return false, nil
	}
	hash, err := r.Hash()
	if err != nil || strings.TrimSpace(hash) == "" {
		return false, err
	}
	pref, err := r.Store.GetRuntimeTelemetryPreference(ctx, hash)
	if err != nil {
		return false, err
	}
	return pref != nil && pref.Enabled, nil
}

// PgTrafficReader aggregates request metrics without reading bodies.
type PgTrafficReader struct {
	Pool *pgxpool.Pool
}

func (r *PgTrafficReader) Snapshot(ctx context.Context) (TrafficSnapshot, error) {
	if r.Pool == nil {
		return TrafficSnapshot{}, nil
	}
	var snap TrafficSnapshot
	err := r.Pool.QueryRow(ctx, `
		SELECT
			COALESCE(COUNT(*), 0)::float8 / 300.0 AS tps,
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms), 0) AS p50,
			COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms), 0) AS p99,
			COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 0) AS success_pct,
			COALESCE(COUNT(DISTINCT tenant_id), 0) AS tenant_count
		FROM request_logs_with_current_month
		WHERE ts >= NOW() - INTERVAL '5 minutes'
	`).Scan(&snap.Last5MinTPS, &snap.Last5MinP50Ms, &snap.Last5MinP99Ms, &snap.Last5MinSuccessPct, &snap.TenantCount)
	if err != nil {
		return TrafficSnapshot{}, nil
	}

	rows, err := r.Pool.Query(ctx, `
		SELECT COALESCE(NULLIF(outbound_model, ''), client_model) AS model_name, COUNT(*)::bigint
		FROM request_logs_with_current_month
		WHERE ts >= NOW() - INTERVAL '5 minutes'
		  AND COALESCE(NULLIF(outbound_model, ''), client_model, '') <> ''
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 20
	`)
	if err == nil {
		defer rows.Close()
		snap.ModelUsage = map[string]int64{}
		for rows.Next() {
			var model string
			var count int64
			if scanErr := rows.Scan(&model, &count); scanErr == nil {
				snap.ModelUsage[model] = count
			}
		}
	}

	var inFlight int
	_ = r.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_logs_with_current_month
		WHERE ts >= NOW() - INTERVAL '30 seconds'
	`).Scan(&inFlight)
	snap.CurrentConcurrency = inFlight
	return snap, nil
}

// PgDBSizeReader returns PostgreSQL database size in megabytes.
type PgDBSizeReader struct {
	Pool *pgxpool.Pool
}

func (r *PgDBSizeReader) DatabaseSizeMB(ctx context.Context) (int64, error) {
	if r.Pool == nil {
		return 0, nil
	}
	var bytes int64
	err := r.Pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&bytes)
	if err != nil {
		return 0, err
	}
	return bytes / 1024 / 1024, nil
}

// PgLicenseInfoReader exposes non-sensitive license metadata for telemetry payloads.
type PgLicenseInfoReader struct {
	Store licensing.Store
	Hash  func() (string, error)
}

func (r *PgLicenseInfoReader) LicenseType(ctx context.Context) (string, error) {
	license, err := r.lookupLicense(ctx)
	if err != nil || license == nil {
		return "", err
	}
	return license.SubscriptionTier, nil
}

func (r *PgLicenseInfoReader) ExpiresInDays(ctx context.Context) (int, error) {
	license, err := r.lookupLicense(ctx)
	if err != nil || license == nil || license.ExpiresAt.IsZero() {
		return 0, err
	}
	days := int(time.Until(license.ExpiresAt).Hours() / 24)
	if days < 0 {
		return 0, nil
	}
	return days, nil
}

func (r *PgLicenseInfoReader) lookupLicense(ctx context.Context) (*licensing.License, error) {
	if r.Store == nil || r.Hash == nil {
		return nil, nil
	}
	hash, err := r.Hash()
	if err != nil {
		return nil, err
	}
	return r.Store.GetLicenseByHardwareHash(ctx, hash)
}

// ReadCredentialFile reads and trims a credential file from disk.
func ReadCredentialFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
