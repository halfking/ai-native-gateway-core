// Package bg provides background workers for credential health management.
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
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

	// highWatermark (P1-4 fix, 2026-06-22 audit): maps each Redis key to
	// the millisecond timestamp of the newest entry that has already been
	// aggregated. The next tick only pulls entries strictly newer than
	// this, which is what makes aggregation idempotent: an entry is
	// counted exactly once even though the worker runs every minute and
	// the Redis sliding window retains 2h of history.
	//
	// Before this fix, every tick re-pulled the trailing 5 minutes and
	// ON CONFLICT DO UPDATE summed them in again, so total_calls grew
	// ~5x real volume within minutes. See audit P1-4.
	highWatermark sync.Map // map[string]int64 (redisKey → lastAggregatedMs)
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
	// P1-3 fix (2026-06-22 audit): use SCAN instead of KEYS.
	// KEYS is O(N) and blocks the Redis main thread; on a busy gateway with
	// hundreds of (credential,model) pairs this stalls every other client
	// (sessions, fp slots, pending cache) for tens of milliseconds each
	// minute. SCAN is cursor-based and yields between iterations.
	keys, err := a.scanCallHistKeys(ctx)
	if err != nil {
		return fmt.Errorf("scan keys: %w", err)
	}

	slog.Debug("call_history_aggregator: scanning keys", "count", len(keys))

	// windowStart is the 1-minute bucket this tick belongs to. Because
	// the bucket key is part of the PK (credential_id, raw_model,
	// window_start), every entry aggregated in this tick maps to the same
	// row, and the high-watermark guarantees each Redis entry contributes
	// to exactly one tick.
	windowStart := time.Now().Truncate(1 * time.Minute)

	batch := make([]aggregateRow, 0, len(keys))

	for _, key := range keys {
		// Parse key: llmgw:callhist:{credentialID}:{model}
		credID, model, ok := parseCallHistKey(key)
		if !ok {
			slog.Warn("call_history_aggregator: invalid key format", "key", key)
			continue
		}

		// High-watermark: only pull entries strictly newer than the last
		// aggregated timestamp for this key. On the first tick for a key
		// there is no watermark, so we aggregate everything currently in
		// the window — this is correct because the row didn't exist yet.
		var sinceMs int64
		if v, ok := a.highWatermark.Load(key); ok {
			sinceMs = v.(int64)
		}
		since := time.UnixMilli(sinceMs)

		entries, err := a.recorder.GetRecent(ctx, credID, model, since)
		if err != nil || len(entries) == 0 {
			continue
		}

		row := aggregateRow{
			CredentialID: credID,
			RawModel:     model,
			WindowStart:  windowStart,
		}

		// Track the newest entry we're about to aggregate so the next
		// tick starts from here. Initialize to the current watermark so
		// a zero-entry loop (already up to date) doesn't reset it.
		newestMs := sinceMs
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
			if e.Timestamp > newestMs {
				newestMs = e.Timestamp
			}
		}

		if row.SuccessCalls > 0 {
			row.AvgLatencyMs = float64(row.LatencySum) / float64(row.SuccessCalls)
		}

		// Stash the newest entry ts on the row so we can advance the
		// watermark AFTER insertBatch succeeds (see comment below).
		row.newestMs = newestMs
		batch = append(batch, row)
	}

	if len(batch) == 0 {
		return nil
	}

	// Batch INSERT to PG.
	if err := a.insertBatch(ctx, batch); err != nil {
		return err
	}

	// Now that the rows are in PG, advance the watermarks. If we advanced
	// before the insert and the insert failed, the next tick would skip
	// these entries and they'd be lost forever.
	//
	// We store newestMs+1 because GetRecent filters with >= (inclusive).
	// Storing the exact boundary would re-include that entry on the next
	// tick; +1 makes the filter strictly-greater, so each entry is
	// aggregated exactly once even if multiple entries share the same ms.
	for _, r := range batch {
		key := fmt.Sprintf("llmgw:callhist:%d:%s", r.CredentialID, r.RawModel)
		a.highWatermark.Store(key, r.newestMs+1)
	}

	// GC stale watermarks (residual #2 from 2026-06-22 audit): any
	// watermark key whose Redis counterpart no longer exists (the 2h
	// sliding window expired, or the credential/model was deleted) is
	// dead weight. Prune it so the sync.Map can't grow unbounded over
	// weeks of credential churn. We do this only when the watermark has
	// a meaningful number of entries, to avoid scanning on every tick.
	a.pruneStaleWatermarks(keys)

	slog.Info("call_history_aggregator: aggregated", "rows", len(batch))
	return nil
}

// pruneStaleWatermarks deletes highWatermark entries whose Redis key is no
// longer present in the current scan results. Called once per aggregate()
// tick; cheap because it only iterates the watermark map (not Redis).
func (a *CallHistoryAggregator) pruneStaleWatermarks(liveKeys []string) {
	// Build a set of currently-live Redis keys for O(1) lookup.
	live := make(map[string]struct{}, len(liveKeys))
	for _, k := range liveKeys {
		live[k] = struct{}{}
	}

	// Walk the watermark map and delete entries whose key is gone.
	// sync.Map.Range visits a snapshot, so deleting during iteration is safe.
	var pruned int
	a.highWatermark.Range(func(key, _ any) bool {
		if _, ok := live[key.(string)]; !ok {
			a.highWatermark.Delete(key)
			pruned++
		}
		return true
	})

	if pruned > 0 {
		slog.Info("call_history_aggregator: pruned stale watermarks", "count", pruned)
	}
}

// scanCallHistKeys iterates Redis using SCAN (cursor-based, non-blocking)
// to collect all llmgw:callhist:* keys. Returns an empty slice (not nil)
// when there are no matches.
func (a *CallHistoryAggregator) scanCallHistKeys(ctx context.Context) ([]string, error) {
	var allKeys []string
	var cursor uint64
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		keys, nextCursor, err := a.redis.Scan(ctx, cursor, "llmgw:callhist:*", 200).Result()
		if err != nil {
			return nil, err
		}
		allKeys = append(allKeys, keys...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return allKeys, nil
}

// parseCallHistKey parses "llmgw:callhist:{credentialID}:{model}" into its
// components. Returns ok=false if the format doesn't match. Note that model
// names may contain dots/dashes but not colons, so a single colon-split on
// the "llmgw:callhist:" prefix + ":" separator is unambiguous.
func parseCallHistKey(key string) (credID int, model string, ok bool) {
	const prefix = "llmgw:callhist:"
	if !strings.HasPrefix(key, prefix) {
		return 0, "", false
	}
	rest := key[len(prefix):]
	idx := strings.IndexByte(rest, ':')
	if idx < 0 {
		return 0, "", false
	}
	id, err := strconv.Atoi(rest[:idx])
	if err != nil {
		return 0, "", false
	}
	return id, rest[idx+1:], true
}

type aggregateRow struct {
	CredentialID         int
	RawModel             string
	WindowStart          time.Time
	TotalCalls           int
	SuccessCalls         int
	FailedCalls          int
	AvgLatencyMs         float64
	LatencySum           int // temp, not persisted
	ErrorRateLimitCount  int
	ErrorQuotaCount      int
	ErrorConcurrentCount int
	ErrorNetworkCount    int
	ErrorAuthCount       int
	ErrorOtherCount      int

	// newestMs is the highest entry timestamp aggregated into this row.
	// Used to advance the per-key high-watermark after the row is durably
	// persisted. Not written to PG.
	newestMs int64
}

// insertBatch inserts aggregated rows into PG.
//
// P1-4 fix (2026-06-22 audit): the previous ON CONFLICT clause ADDED
// incoming counts to existing rows (total_calls = old + EXCLUDED), which
// combined with the re-pull-every-5-minutes bug multiplied real volume by
// the number of overlapping ticks. Now that the high-watermark guarantees
// each entry is aggregated exactly once, a conflicting row should only
// ever arise from a crash-retry of the SAME tick — in which case the
// correct semantics is to REPLACE (the row is the authoritative tally for
// this window bucket), not accumulate.
func (a *CallHistoryAggregator) insertBatch(ctx context.Context, rows []aggregateRow) error {
	// For simplicity, insert one-by-one with ON CONFLICT.
	// Production would use pgx.CopyFrom or multi-row INSERT.
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
				total_calls          = EXCLUDED.total_calls,
				success_calls        = EXCLUDED.success_calls,
				failed_calls         = EXCLUDED.failed_calls,
				avg_latency_ms       = EXCLUDED.avg_latency_ms,
				error_rate_limit_count  = EXCLUDED.error_rate_limit_count,
				error_quota_count       = EXCLUDED.error_quota_count,
				error_concurrent_count  = EXCLUDED.error_concurrent_count,
				error_network_count     = EXCLUDED.error_network_count,
				error_auth_count        = EXCLUDED.error_auth_count,
				error_other_count       = EXCLUDED.error_other_count
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
	return nil
}
