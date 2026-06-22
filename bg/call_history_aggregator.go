// Package bg provides background workers for credential health management.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
	"github.com/redis/go-redis/v9"
)

// CallHistoryAggregator periodically aggregates Redis sliding window data
// into PG credential_model_call_history table (1-minute windows).
type CallHistoryAggregator struct {
	redis    *redis.Client
	db       credentialhealth.DBQuerier
	recorder *credentialhealth.Recorder
	interval time.Duration // default 1 minute
	stopCh   chan struct{}
}

// NewCallHistoryAggregator creates an aggregator worker.
func NewCallHistoryAggregator(
	redis *redis.Client,
	db credentialhealth.DBQuerier,
	interval time.Duration,
) *CallHistoryAggregator {
	if interval == 0 {
		interval = 1 * time.Minute
	}
	
	recorder := credentialhealth.NewRecorder(redis, 2*time.Hour, 100)
	
	return &CallHistoryAggregator{
		redis:    redis,
		db:       db,
		recorder: recorder,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the aggregation loop.
func (a *CallHistoryAggregator) Start(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	slog.Info("call_history_aggregator started", "interval", a.interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("call_history_aggregator stopping")
			return
		case <-a.stopCh:
			slog.Info("call_history_aggregator stopped")
			return
		case <-ticker.C:
			if err := a.aggregate(ctx); err != nil {
				slog.Error("call_history_aggregator failed", "error", err)
			}
		}
	}
}

// Stop gracefully stops the worker.
func (a *CallHistoryAggregator) Stop() {
	close(a.stopCh)
}

// aggregate reads all Redis sliding windows and writes aggregated stats to PG.
func (a *CallHistoryAggregator) aggregate(ctx context.Context) error {
	// Scan for all callhist keys in Redis
	keys, err := a.redis.Keys(ctx, "llmgw:callhist:*").Result()
	if err != nil {
		return err
	}

	slog.Debug("call_history_aggregator: scanning keys", "count", len(keys))

	// For simplicity, this worker aggregates recent data (last 5 minutes)
	// A production version would track "last aggregated timestamp" per key
	// to avoid re-processing. For now, we aggregate recent entries only.
	
	windowStart := time.Now().Truncate(1 * time.Minute)
	since := windowStart.Add(-5 * time.Minute)

	batch := make([]aggregateRow, 0, len(keys))
	
	for _, key := range keys {
		// Parse key: llmgw:callhist:{credentialID}:{model}
		var credID int
		var model string
		if _, err := fmt.Sscanf(key, "llmgw:callhist:%d:%s", &credID, &model); err != nil {
			slog.Warn("call_history_aggregator: invalid key format", "key", key)
			continue
		}

		entries, err := a.recorder.GetRecent(ctx, credID, model, since)
		if err != nil || len(entries) == 0 {
			continue
		}

		row := aggregateRow{
			CredentialID: credID,
			RawModel:     model,
			WindowStart:  windowStart,
		}

		for _, e := range entries {
			row.TotalCalls++
			if e.Success {
				row.SuccessCalls++
				row.LatencySum += e.LatencyMs
			} else {
				row.FailedCalls++
				switch e.ErrorKind {
				case "rate_limit":
					row.ErrorRateLimitCount++
				case "quota":
					row.ErrorQuotaCount++
				case "concurrent":
					row.ErrorConcurrentCount++
				case "network":
					row.ErrorNetworkCount++
				case "auth":
					row.ErrorAuthCount++
				default:
					row.ErrorOtherCount++
				}
			}
		}

		if row.SuccessCalls > 0 {
			row.AvgLatencyMs = float64(row.LatencySum) / float64(row.SuccessCalls)
		}

		batch = append(batch, row)
	}

	if len(batch) == 0 {
		return nil
	}

	// Batch INSERT to PG
	return a.insertBatch(ctx, batch)
}

type aggregateRow struct {
	CredentialID          int
	RawModel              string
	WindowStart           time.Time
	TotalCalls            int
	SuccessCalls          int
	FailedCalls           int
	AvgLatencyMs          float64
	LatencySum            int // temp, not persisted
	ErrorRateLimitCount   int
	ErrorQuotaCount       int
	ErrorConcurrentCount  int
	ErrorNetworkCount     int
	ErrorAuthCount        int
	ErrorOtherCount       int
}

// insertBatch inserts aggregated rows into PG (ON CONFLICT DO UPDATE).
func (a *CallHistoryAggregator) insertBatch(ctx context.Context, rows []aggregateRow) error {
	// For simplicity, insert one-by-one with ON CONFLICT
	// Production would use pgx.CopyFrom or multi-row INSERT
	for _, r := range rows {
		_, err := a.db.Exec(ctx, `
			INSERT INTO credential_model_call_history (
				credential_id, raw_model, window_start,
				total_calls, success_calls, failed_calls,
				avg_latency_ms,
				error_rate_limit_count, error_quota_count, error_concurrent_count,
				error_network_count, error_auth_count, error_other_count
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (credential_id, raw_model, window_start) DO UPDATE SET
				total_calls = credential_model_call_history.total_calls + EXCLUDED.total_calls,
				success_calls = credential_model_call_history.success_calls + EXCLUDED.success_calls,
				failed_calls = credential_model_call_history.failed_calls + EXCLUDED.failed_calls,
				avg_latency_ms = (credential_model_call_history.avg_latency_ms + EXCLUDED.avg_latency_ms) / 2.0
		`, r.CredentialID, r.RawModel, r.WindowStart,
			r.TotalCalls, r.SuccessCalls, r.FailedCalls,
			r.AvgLatencyMs,
			r.ErrorRateLimitCount, r.ErrorQuotaCount, r.ErrorConcurrentCount,
			r.ErrorNetworkCount, r.ErrorAuthCount, r.ErrorOtherCount)
		if err != nil {
			slog.Error("call_history_aggregator: insert failed", "error", err)
			// Continue with next row
		}
	}

	slog.Info("call_history_aggregator: aggregated", "rows", len(rows))
	return nil
}
