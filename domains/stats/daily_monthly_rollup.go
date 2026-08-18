package stats

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultRollupInterval = time.Hour
	rollupLookback        = 48 * time.Hour
)

// DailyMonthlyRollup rebuilds open daily/monthly projections from usage_facts.
// Rebuild semantics make retries and late usage corrections converge without
// additive double counting.
type DailyMonthlyRollup struct {
	db       *pgxpool.Pool
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
	started  atomic.Bool
	stopOnce sync.Once
}

func NewDailyMonthlyRollup(db *pgxpool.Pool, interval time.Duration) *DailyMonthlyRollup {
	if interval <= 0 {
		interval = defaultRollupInterval
	}
	return &DailyMonthlyRollup{db: db, interval: interval, done: make(chan struct{})}
}

func (r *DailyMonthlyRollup) Start(ctx context.Context) {
	if r == nil || r.db == nil || !r.started.CompareAndSwap(false, true) {
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.run(cctx)
	slog.Info("stats daily/monthly rollup started", "interval", r.interval.String())
}

func (r *DailyMonthlyRollup) Stop() {
	if r == nil || !r.started.Load() {
		return
	}
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
	})
	<-r.done
}

func (r *DailyMonthlyRollup) run(ctx context.Context) {
	defer close(r.done)
	// Populate a first snapshot shortly after startup, then refresh on a
	// bounded interval. A failure is retried on the next tick.
	r.refreshRecent(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshRecent(ctx)
		}
	}
}

func (r *DailyMonthlyRollup) refreshRecent(ctx context.Context) {
	now := time.Now().UTC()
	refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := r.Refresh(refreshCtx, now.Add(-rollupLookback), now); err != nil {
		slog.Warn("stats daily/monthly rollup failed", "error", err)
	}
}

// Refresh rebuilds daily buckets intersecting [since, until), then rebuilds
// the corresponding open monthly buckets from those daily rows.
func (r *DailyMonthlyRollup) Refresh(ctx context.Context, since, until time.Time) error {
	if r == nil || r.db == nil {
		return nil
	}
	if until.Before(since) || until.Equal(since) {
		return nil
	}
	since = since.UTC()
	until = until.UTC()
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin stats rollup: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		DELETE FROM stats_usage_daily
		WHERE day_utc >= ($1 AT TIME ZONE 'UTC')::date
		  AND day_utc < (($2 - INTERVAL '1 microsecond') AT TIME ZONE 'UTC')::date + 1`, since, until); err != nil {
		return fmt.Errorf("clear daily buckets: %w", err)
	}
	if _, err := tx.Exec(ctx, dailyInsertSQL, since, until); err != nil {
		return fmt.Errorf("rebuild daily buckets: %w", err)
	}

	monthStart := time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(until.Year(), until.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	if _, err := tx.Exec(ctx, `
		DELETE FROM stats_usage_monthly m
		WHERE m.month_start >= $1::date AND m.month_start < $2::date
	  AND NOT EXISTS (
			SELECT 1 FROM stats_usage_monthly closed
			WHERE closed.month_start = m.month_start AND closed.status = 'closed'
	  )`, monthStart, monthEnd); err != nil {
		return fmt.Errorf("clear open monthly buckets: %w", err)
	}
	if _, err := tx.Exec(ctx, monthlyInsertSQL, monthStart, monthEnd); err != nil {
		return fmt.Errorf("rebuild monthly buckets: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit stats rollup: %w", err)
	}
	return nil
}

const dailyInsertSQL = `
WITH latest AS (
	SELECT DISTINCT ON (event_id)
		*
	FROM usage_facts
	WHERE occurred_at >= $1 AND occurred_at < $2
	ORDER BY event_id, revision DESC, finalized_at DESC
)
INSERT INTO stats_usage_daily (
	day_utc, tenant_id, provider_id, credential_id, canonical_id, raw_model_name,
	dimension_type, dimension_key, traffic_class, request_count, success_count,
	failure_count, timeout_count, rate_limited_count, attempt_count, retry_count,
	probe_count, switch_count, prompt_tokens, completion_tokens, cache_read_tokens,
	cache_write_tokens, reasoning_tokens, image_tokens, audio_tokens, video_tokens,
	provider_tokens, total_tokens, credits_charged, cost_usd, latency_count,
	latency_sum_ms, ttft_count, ttft_sum_ms, source_event_count,
	source_max_occurred_at, updated_at
)
SELECT
	(occurred_at AT TIME ZONE 'UTC')::date,
	COALESCE(tenant_id, 'default'), COALESCE(provider_id, 0), COALESCE(credential_id, 0),
	COALESCE(canonical_id, 0), COALESCE(raw_model_name, ''), 'provider_model',
	COALESCE(raw_model_name, ''), COALESCE(traffic_class, 'unknown'),
	COUNT(*), COUNT(*) FILTER (WHERE status = 'success'),
	COUNT(*) FILTER (WHERE status = 'failure'), COUNT(*) FILTER (WHERE status = 'timeout'),
	COUNT(*) FILTER (WHERE status = 'rate_limited'), COUNT(*), 0,
	COUNT(*) FILTER (WHERE traffic_class <> 'business'), 0,
	SUM(prompt_tokens), SUM(completion_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
	SUM(reasoning_tokens), SUM(image_tokens), SUM(audio_tokens), SUM(video_tokens),
	SUM(provider_tokens), SUM(total_tokens), SUM(credits_charged), SUM(cost_amount),
	COUNT(*) FILTER (WHERE latency_ms > 0), SUM(latency_ms) FILTER (WHERE latency_ms > 0),
	COUNT(*) FILTER (WHERE ttft_ms > 0), SUM(ttft_ms) FILTER (WHERE ttft_ms > 0), COUNT(*),
	MAX(occurred_at), now()
FROM latest
GROUP BY 1,2,3,4,5,6,8,9
ON CONFLICT (day_utc, tenant_id, provider_id, credential_id, canonical_id, raw_model_name, dimension_type, dimension_key, traffic_class)
DO UPDATE SET
	request_count = EXCLUDED.request_count, success_count = EXCLUDED.success_count,
	failure_count = EXCLUDED.failure_count, timeout_count = EXCLUDED.timeout_count,
	rate_limited_count = EXCLUDED.rate_limited_count, attempt_count = EXCLUDED.attempt_count,
	retry_count = EXCLUDED.retry_count, probe_count = EXCLUDED.probe_count,
	switch_count = EXCLUDED.switch_count, prompt_tokens = EXCLUDED.prompt_tokens,
	completion_tokens = EXCLUDED.completion_tokens, cache_read_tokens = EXCLUDED.cache_read_tokens,
	cache_write_tokens = EXCLUDED.cache_write_tokens, reasoning_tokens = EXCLUDED.reasoning_tokens,
	image_tokens = EXCLUDED.image_tokens, audio_tokens = EXCLUDED.audio_tokens,
	video_tokens = EXCLUDED.video_tokens, provider_tokens = EXCLUDED.provider_tokens,
	total_tokens = EXCLUDED.total_tokens, credits_charged = EXCLUDED.credits_charged,
	cost_usd = EXCLUDED.cost_usd, latency_count = EXCLUDED.latency_count,
	latency_sum_ms = EXCLUDED.latency_sum_ms, ttft_count = EXCLUDED.ttft_count,
	ttft_sum_ms = EXCLUDED.ttft_sum_ms, source_event_count = EXCLUDED.source_event_count,
	source_max_occurred_at = EXCLUDED.source_max_occurred_at, updated_at = now()`

const monthlyInsertSQL = `
INSERT INTO stats_usage_monthly (
	month_start, tenant_id, provider_id, credential_id, canonical_id, raw_model_name,
	dimension_type, dimension_key, traffic_class, request_count, success_count,
	failure_count, timeout_count, rate_limited_count, attempt_count, retry_count,
	probe_count, switch_count, prompt_tokens, completion_tokens, cache_read_tokens,
	cache_write_tokens, reasoning_tokens, image_tokens, audio_tokens, video_tokens,
	provider_tokens, total_tokens, credits_charged, cost_usd, latency_count,
	latency_sum_ms, ttft_count, ttft_sum_ms, source_day_count, source_high_watermark,
	updated_at
)
SELECT
	date_trunc('month', day_utc)::date, tenant_id, provider_id, credential_id, canonical_id,
	raw_model_name, dimension_type, dimension_key, traffic_class,
	SUM(request_count), SUM(success_count), SUM(failure_count), SUM(timeout_count),
	SUM(rate_limited_count), SUM(attempt_count), SUM(retry_count), SUM(probe_count),
	SUM(switch_count), SUM(prompt_tokens), SUM(completion_tokens), SUM(cache_read_tokens),
	SUM(cache_write_tokens), SUM(reasoning_tokens), SUM(image_tokens), SUM(audio_tokens),
	SUM(video_tokens), SUM(provider_tokens), SUM(total_tokens), SUM(credits_charged), SUM(cost_usd),
	SUM(latency_count), SUM(latency_sum_ms), SUM(ttft_count), SUM(ttft_sum_ms), COUNT(DISTINCT day_utc),
	MAX(source_max_occurred_at), now()
FROM stats_usage_daily
WHERE day_utc >= $1::date AND day_utc < $2::date
GROUP BY 1,2,3,4,5,6,7,8,9
ON CONFLICT (month_start, tenant_id, provider_id, credential_id, canonical_id, raw_model_name, dimension_type, dimension_key, traffic_class)
DO UPDATE SET
	request_count = EXCLUDED.request_count, success_count = EXCLUDED.success_count,
	failure_count = EXCLUDED.failure_count, timeout_count = EXCLUDED.timeout_count,
	rate_limited_count = EXCLUDED.rate_limited_count, attempt_count = EXCLUDED.attempt_count,
	retry_count = EXCLUDED.retry_count, probe_count = EXCLUDED.probe_count,
	switch_count = EXCLUDED.switch_count, prompt_tokens = EXCLUDED.prompt_tokens,
	completion_tokens = EXCLUDED.completion_tokens, cache_read_tokens = EXCLUDED.cache_read_tokens,
	cache_write_tokens = EXCLUDED.cache_write_tokens, reasoning_tokens = EXCLUDED.reasoning_tokens,
	image_tokens = EXCLUDED.image_tokens, audio_tokens = EXCLUDED.audio_tokens,
	video_tokens = EXCLUDED.video_tokens, provider_tokens = EXCLUDED.provider_tokens,
	total_tokens = EXCLUDED.total_tokens, credits_charged = EXCLUDED.credits_charged,
	cost_usd = EXCLUDED.cost_usd, latency_count = EXCLUDED.latency_count,
	latency_sum_ms = EXCLUDED.latency_sum_ms, ttft_count = EXCLUDED.ttft_count,
	ttft_sum_ms = EXCLUDED.ttft_sum_ms, source_day_count = EXCLUDED.source_day_count,
	source_high_watermark = EXCLUDED.source_high_watermark, updated_at = now()
WHERE stats_usage_monthly.status <> 'closed'`
