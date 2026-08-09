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
	var rateLimitHits, rateLimitTotal interface{}
	if snapshot.RateLimitMetrics != nil {
		rateLimitHits = snapshot.RateLimitMetrics.RateLimitHits
		rateLimitTotal = snapshot.RateLimitMetrics.TotalRequests
	}
	var concurrencyLimit, concurrencyLimitAuto, concurrencyEffLimit, downtimeBuckets, downtimeTotalBuckets, longestDowntimeRun interface{}
	var concurrencyCapped interface{}
	if snapshot.ConcurrencyCapacity != nil {
		concurrencyLimit = snapshot.ConcurrencyCapacity.ConcurrencyLimit
		concurrencyLimitAuto = snapshot.ConcurrencyCapacity.ConcurrencyLimitAuto
		concurrencyEffLimit = snapshot.ConcurrencyCapacity.EffLimit
		concurrencyCapped = snapshot.ConcurrencyCapacity.IsCapped
	}
	if snapshot.AvailabilityWindow != nil {
		downtimeBuckets = snapshot.AvailabilityWindow.DowntimeBuckets
		downtimeTotalBuckets = snapshot.AvailabilityWindow.TotalBuckets
		longestDowntimeRun = snapshot.AvailabilityWindow.LongestRun
	}
	var qualityMean, qualityStddev, qualityCV, qualityVolatile, qualitySampleN interface{}
	if snapshot.QualityStabilitySignal != nil {
		qualityMean = snapshot.QualityStabilitySignal.Mean
		qualityStddev = snapshot.QualityStabilitySignal.Stddev
		qualityCV = snapshot.QualityStabilitySignal.CV
		qualityVolatile = snapshot.QualityStabilitySignal.IsVolatile
		qualitySampleN = snapshot.QualityStabilitySignal.SampleN
	}

	query := `
		INSERT INTO provider_profile_metrics (
			credential_id, provider_id, metric_time, time_slot,
			network_latency_p50, network_latency_p95, network_latency_p99,
			availability_total_requests, availability_success_requests,
			availability_ttft_avg_ms, availability_duration_avg_ms,
			stability_error_count, stability_error_types,
			scale_total_models, scale_available_models,
			rate_limit_hits, rate_limit_total_requests,
			concurrency_limit, concurrency_limit_auto, concurrency_eff_limit, concurrency_is_capped,
			downtime_buckets, downtime_total_buckets, longest_downtime_run,
			quality_stability_mean, quality_stability_stddev, quality_stability_cv,
			quality_stability_is_volatile, quality_stability_sample_n
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::text::jsonb, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29)
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
		rateLimitHits, rateLimitTotal,
		concurrencyLimit, concurrencyLimitAuto, concurrencyEffLimit, concurrencyCapped,
		downtimeBuckets, downtimeTotalBuckets, longestDowntimeRun,
		qualityMean, qualityStddev, qualityCV, qualityVolatile, qualitySampleN,
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
				scale_total_models, scale_available_models,
				rate_limit_hits, rate_limit_total_requests,
				concurrency_limit, concurrency_limit_auto, concurrency_eff_limit, concurrency_is_capped,
				downtime_buckets, downtime_total_buckets, longest_downtime_run,
				quality_stability_mean, quality_stability_stddev, quality_stability_cv,
				quality_stability_is_volatile, quality_stability_sample_n

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
		var rateLimitHits, rateLimitTotal *int
		var concurrencyLimit, concurrencyLimitAuto, concurrencyEffLimit *int
		var concurrencyCapped *bool
		var downtimeBuckets, downtimeTotalBuckets, longestDowntimeRun *int
		var qualityMean, qualityStddev, qualityCV *float64
		var qualityVolatile *bool
		var qualitySampleN *int

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
			&rateLimitHits,
			&rateLimitTotal,
			&concurrencyLimit,
			&concurrencyLimitAuto,
			&concurrencyEffLimit,
			&concurrencyCapped,
			&downtimeBuckets,
			&downtimeTotalBuckets,
			&longestDowntimeRun,
			&qualityMean,
			&qualityStddev,
			&qualityCV,
			&qualityVolatile,
			&qualitySampleN,
		)

		if err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}

		snapshot.TimeSlot = TimeSlot(timeSlotStr)
		if rateLimitHits != nil || rateLimitTotal != nil {
			snapshot.RateLimitMetrics = &RateLimitMetrics{}
			if rateLimitHits != nil {
				snapshot.RateLimitMetrics.RateLimitHits = *rateLimitHits
			}
			if rateLimitTotal != nil {
				snapshot.RateLimitMetrics.TotalRequests = *rateLimitTotal
			}
			if snapshot.RateLimitMetrics.TotalRequests > 0 {
				snapshot.RateLimitMetrics.HitsRatio = float64(snapshot.RateLimitMetrics.RateLimitHits) / float64(snapshot.RateLimitMetrics.TotalRequests)
			}
		}
		if concurrencyEffLimit != nil {
			snapshot.ConcurrencyCapacity = &ConcurrencyCapacity{}
			if concurrencyLimit != nil {
				snapshot.ConcurrencyCapacity.ConcurrencyLimit = *concurrencyLimit
			}
			if concurrencyLimitAuto != nil {
				snapshot.ConcurrencyCapacity.ConcurrencyLimitAuto = *concurrencyLimitAuto
			}
			snapshot.ConcurrencyCapacity.EffLimit = *concurrencyEffLimit
			if concurrencyCapped != nil {
				snapshot.ConcurrencyCapacity.IsCapped = *concurrencyCapped
			}
		}
		if downtimeBuckets != nil || downtimeTotalBuckets != nil || longestDowntimeRun != nil {
			snapshot.AvailabilityWindow = &AvailabilityWindow{}
			if downtimeBuckets != nil {
				snapshot.AvailabilityWindow.DowntimeBuckets = *downtimeBuckets
			}
			if downtimeTotalBuckets != nil {
				snapshot.AvailabilityWindow.TotalBuckets = *downtimeTotalBuckets
			}
			if longestDowntimeRun != nil {
				snapshot.AvailabilityWindow.LongestRun = *longestDowntimeRun
			}
			if snapshot.AvailabilityWindow.TotalBuckets > 0 {
				snapshot.AvailabilityWindow.DowntimeRatio = float64(snapshot.AvailabilityWindow.DowntimeBuckets) / float64(snapshot.AvailabilityWindow.TotalBuckets)
			}
		}

		if qualityMean != nil || qualityStddev != nil || qualityCV != nil || qualityVolatile != nil || qualitySampleN != nil {
			snapshot.QualityStabilitySignal = &QualityStabilitySignal{}
			if qualityMean != nil {
				snapshot.QualityStabilitySignal.Mean = *qualityMean
			}
			if qualityStddev != nil {
				snapshot.QualityStabilitySignal.Stddev = *qualityStddev
			}
			if qualityCV != nil {
				snapshot.QualityStabilitySignal.CV = *qualityCV
			}
			if qualityVolatile != nil {
				snapshot.QualityStabilitySignal.IsVolatile = *qualityVolatile
			}
			if qualitySampleN != nil {
				snapshot.QualityStabilitySignal.SampleN = *qualitySampleN
			}
		}

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
