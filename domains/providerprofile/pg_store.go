package providerprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGMetricsStore PostgreSQL指标存储实现
type PGMetricsStore struct {
	db *pgxpool.Pool
}

// NewPGMetricsStore 创建PostgreSQL指标存储
func NewPGMetricsStore(db *pgxpool.Pool) *PGMetricsStore {
	return &PGMetricsStore{db: db}
}

// SaveSnapshot 保存采集快照
func (s *PGMetricsStore) SaveSnapshot(ctx context.Context, snapshot *MetricSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("save snapshot: snapshot is nil")
	}

	// Defensively guard all pointer fields so a nil sub-struct never causes a panic.
	var p50, p95, p99 int
	if snapshot.NetworkMetrics != nil {
		p50, p95, p99 = snapshot.NetworkMetrics.P50, snapshot.NetworkMetrics.P95, snapshot.NetworkMetrics.P99
	}
	var totalReq, successReq, avgTTFT, avgDur int
	if snapshot.AvailabilityMetrics != nil {
		totalReq, successReq = snapshot.AvailabilityMetrics.TotalRequests, snapshot.AvailabilityMetrics.SuccessRequests
		avgTTFT, avgDur = snapshot.AvailabilityMetrics.AvgTTFTMs, snapshot.AvailabilityMetrics.AvgDurationMs
	}
	var errCount int
	var errorTypesJSON []byte
	var err error
	if snapshot.StabilityMetrics != nil {
		errCount = snapshot.StabilityMetrics.ErrorCount
		errTypes := map[string]interface{}{}
		for k, v := range snapshot.StabilityMetrics.ErrorTypes {
			errTypes[k] = v
		}
		if snapshot.RateLimitMetrics != nil {
			errTypes["_provider_profile_rate_limit"] = snapshot.RateLimitMetrics
		}
		if snapshot.AvailabilityWindow != nil {
			errTypes["_provider_profile_availability_window"] = snapshot.AvailabilityWindow
		}
		errorTypesJSON, err = marshalJSON(errTypes)
		if err != nil {
			return fmt.Errorf("marshal error types: %w", err)
		}
	} else {
		errTypes := map[string]interface{}{}
		if snapshot.RateLimitMetrics != nil {
			errTypes["_provider_profile_rate_limit"] = snapshot.RateLimitMetrics
		}
		if snapshot.AvailabilityWindow != nil {
			errTypes["_provider_profile_availability_window"] = snapshot.AvailabilityWindow
		}
		if len(errTypes) == 0 {
			errTypes["_provider_profile_empty"] = true
		}
		errorTypesJSON, err = marshalJSON(errTypes)
		if err != nil {
			return fmt.Errorf("marshal error types: %w", err)
		}
	}
	var totalModels, availModels int
	if snapshot.ScaleMetrics != nil {
		totalModels, availModels = snapshot.ScaleMetrics.TotalModels, snapshot.ScaleMetrics.AvailableModels
	}

	query := `
		INSERT INTO provider_profile_metrics (
			credential_id, provider_id, metric_time, time_slot,
			network_latency_p50, network_latency_p95, network_latency_p99,
			availability_total_requests, availability_success_requests,
			availability_ttft_avg_ms, availability_duration_avg_ms,
			stability_error_count, stability_error_types,
			scale_total_models, scale_available_models
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::text::jsonb, $14, $15)
	`

	_, err = s.db.Exec(ctx, query,
		snapshot.CredentialID,
		snapshot.ProviderID,
		snapshot.MetricTime,
		snapshot.TimeSlot,
		p50, p95, p99,
		totalReq, successReq,
		avgTTFT, avgDur,
		errCount,
		string(errorTypesJSON),
		totalModels, availModels,
	)

	if err != nil {
		return fmt.Errorf("insert metrics snapshot: %w", err)
	}

	return nil
}

// GetSnapshotsByDateRange 获取指定时间范围的快照
func (s *PGMetricsStore) GetSnapshotsByDateRange(ctx context.Context, credentialID int64, start, end time.Time) ([]*MetricSnapshot, error) {
	query := `
		SELECT
			credential_id, provider_id, metric_time, time_slot,
			network_latency_p50, network_latency_p95, network_latency_p99,
			availability_total_requests, availability_success_requests,
			availability_ttft_avg_ms, availability_duration_avg_ms,
			stability_error_count, stability_error_types,
			scale_total_models, scale_available_models
		FROM provider_profile_metrics
		WHERE credential_id = $1
		  AND metric_time >= $2
		  AND metric_time < $3
		ORDER BY metric_time ASC
	`

	rows, err := s.db.Query(ctx, query, credentialID, start, end)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []*MetricSnapshot
	for rows.Next() {
		var snapshot MetricSnapshot
		var timeSlotStr string
		var errorTypesJSON []byte

		snapshot.NetworkMetrics = &NetworkMetrics{}
		snapshot.AvailabilityMetrics = &AvailabilityMetrics{}
		snapshot.StabilityMetrics = &StabilityMetrics{}
		snapshot.ScaleMetrics = &ScaleMetrics{}

		err := rows.Scan(
			&snapshot.CredentialID,
			&snapshot.ProviderID,
			&snapshot.MetricTime,
			&timeSlotStr,
			&snapshot.NetworkMetrics.P50,
			&snapshot.NetworkMetrics.P95,
			&snapshot.NetworkMetrics.P99,
			&snapshot.AvailabilityMetrics.TotalRequests,
			&snapshot.AvailabilityMetrics.SuccessRequests,
			&snapshot.AvailabilityMetrics.AvgTTFTMs,
			&snapshot.AvailabilityMetrics.AvgDurationMs,
			&snapshot.StabilityMetrics.ErrorCount,
			&errorTypesJSON,
			&snapshot.ScaleMetrics.TotalModels,
			&snapshot.ScaleMetrics.AvailableModels,
		)
		if err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}

		snapshot.TimeSlot = TimeSlot(timeSlotStr)

		if len(errorTypesJSON) > 0 {
			var persisted map[string]json.RawMessage
			if err := json.Unmarshal(errorTypesJSON, &persisted); err != nil {
				return nil, fmt.Errorf("unmarshal error types: %w", err)
			}
			snapshot.StabilityMetrics.ErrorTypes = make(map[string]int)
			for errorType, raw := range persisted {
				switch errorType {
				case "_provider_profile_rate_limit":
					var signal RateLimitMetrics
					if err := json.Unmarshal(raw, &signal); err != nil {
						return nil, fmt.Errorf("unmarshal rate limit metrics: %w", err)
					}
					snapshot.RateLimitMetrics = &signal
				case "_provider_profile_availability_window":
					var signal AvailabilityWindow
					if err := json.Unmarshal(raw, &signal); err != nil {
						return nil, fmt.Errorf("unmarshal availability window: %w", err)
					}
					snapshot.AvailabilityWindow = &signal
				case "_provider_profile_empty":
					continue
				default:
					var count int
					if err := json.Unmarshal(raw, &count); err != nil {
						return nil, fmt.Errorf("unmarshal error type %q: %w", errorType, err)
					}
					snapshot.StabilityMetrics.ErrorTypes[errorType] = count
				}
			}
		}

		snapshots = append(snapshots, &snapshot)
	}

	return snapshots, rows.Err()
}

// CleanupOldMetrics 清理7天前的数据
func (s *PGMetricsStore) CleanupOldMetrics(ctx context.Context) (int64, error) {
	query := `
		DELETE FROM provider_profile_metrics
		WHERE created_at < NOW() - INTERVAL '7 days'
	`

	result, err := s.db.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("cleanup old metrics: %w", err)
	}

	return result.RowsAffected(), nil
}
