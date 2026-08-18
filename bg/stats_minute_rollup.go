package bg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/stats"
	"github.com/kaixuan/llm-gateway-go/maas"
)

const (
	statsRollupInterval  = 1 * time.Minute
	statsRetentionDays   = 90
	statsBackfillDays    = 30
	statsRollupBatchSize = 5000
)

// StatsMinuteRollup compensates the in-memory accumulator by scanning request_logs_with_current_month.
type StatsMinuteRollup struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// 2026-07-27 concurrency fix: Stop() used to block on <-w.done even
	// when Start() had never run, which hangs the shutdown path forever.
	// started/stopOnce follow bg/pending_sweeper.go.
	stopOnce sync.Once
	started  atomic.Bool
}

func NewStatsMinuteRollup(db *pgxpool.Pool) *StatsMinuteRollup {
	return &StatsMinuteRollup{db: db, done: make(chan struct{})}
}

func (w *StatsMinuteRollup) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		// Already started; a second run() would double-close w.done.
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(cctx)
	slog.Info("stats minute rollup started", "interval", statsRollupInterval.String())
}

// Stop cancels the loop and waits for it. Safe on a never-Started
// worker (no-op) and safe to call twice.
func (w *StatsMinuteRollup) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if !w.started.Load() {
		// run() never launched, so w.done is never closed.
		return
	}
	<-w.done
}

func (w *StatsMinuteRollup) run(ctx context.Context) {
	defer close(w.done)
	w.backfillIfNeeded(ctx)
	ticker := time.NewTicker(statsRollupInterval)
	defer ticker.Stop()
	lastCleanup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.rollup(ctx)
			if time.Since(lastCleanup) > 24*time.Hour {
				if err := stats.CleanupOldMinuteStats(ctx, w.db, statsRetentionDays); err != nil {
					slog.Warn("stats minute cleanup failed", "error", err)
				}
				lastCleanup = time.Now()
			}
		}
	}
}

func (w *StatsMinuteRollup) backfillIfNeeded(ctx context.Context) {
	if w.db == nil {
		return
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var lastTS *time.Time
	_ = w.db.QueryRow(timeoutCtx, `SELECT last_ts FROM request_stats_rollup_cursor WHERE id = 1`).Scan(&lastTS)
	if lastTS != nil {
		return
	}
	slog.Info("stats minute rollup: backfilling recent window", "days", statsBackfillDays)
	cutoff := time.Now().UTC().Add(-statsBackfillDays * 24 * time.Hour)
	if err := w.rollupWindow(timeoutCtx, cutoff, time.Now().UTC()); err != nil {
		slog.Warn("stats minute backfill failed", "error", err)
	}
}

func (w *StatsMinuteRollup) rollup(ctx context.Context) {
	if w.db == nil {
		return
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var lastTS time.Time
	err := w.db.QueryRow(timeoutCtx, `
		SELECT COALESCE(last_ts, now() - INTERVAL '1 hour')
		FROM request_stats_rollup_cursor WHERE id = 1
	`).Scan(&lastTS)
	if err != nil {
		slog.Warn("stats minute rollup cursor read failed", "error", err)
		return
	}
	now := time.Now().UTC()
	if err := w.rollupWindow(timeoutCtx, lastTS, now); err != nil {
		slog.Warn("stats minute rollup failed", "error", err)
		return
	}
	// Advancing the cursor only after all projections succeed makes a retry
	// converge instead of permanently skipping rows.
	_, _ = w.db.Exec(timeoutCtx, `
		UPDATE request_stats_rollup_cursor
		   SET last_ts = $1, updated_at = now()
		 WHERE id = 1
	`, now)
}

func (w *StatsMinuteRollup) rollupWindow(ctx context.Context, since, until time.Time) error {
	creditsExpr := maas.RequestLogCreditsSQL("r", true)
	if err := w.rollupMain(ctx, since, until, creditsExpr); err != nil {
		return err
	}
	return w.rollupDims(ctx, since, until, creditsExpr)
}

func (w *StatsMinuteRollup) rollupVirtualIP(ctx context.Context, since, until time.Time) error {
	creditsExpr := maas.RequestLogCreditsSQL("r", true)
	_, err := w.db.Exec(ctx, `
		INSERT INTO request_stats_dim_minute (
			bucket, tenant_id, dim_type, dim_key,
			requests, success_count, failure_count,
			total_tokens, credits_charged, cost_usd
		)
		SELECT
			date_trunc('minute', r.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
			COALESCE(NULLIF(r.tenant_id, ''), 'default'),
			'virtual_ip',
			COALESCE(NULLIF(r.virtual_ip, ''), '__unknown__'),
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE r.request_status = 'success')::bigint,
			COUNT(*) FILTER (WHERE r.request_status = 'failure')::bigint,
			COALESCE(SUM(COALESCE(r.prompt_tokens,0)+COALESCE(r.completion_tokens,0)), 0)::bigint,
			COALESCE(SUM(`+creditsExpr+`), 0)::bigint,
			COALESCE(SUM(r.cost_usd), 0)
		FROM request_logs_with_current_month r
		WHERE r.request_status IN ('success', 'failure', 'rate_limited')
		  AND r.ts > $1 AND r.ts <= $2
		GROUP BY 1, 2, 4
		ON CONFLICT (bucket, tenant_id, dim_type, dim_key) DO UPDATE SET
			requests = EXCLUDED.requests,
			success_count = EXCLUDED.success_count,
			failure_count = EXCLUDED.failure_count,
			total_tokens = EXCLUDED.total_tokens,
			credits_charged = EXCLUDED.credits_charged,
			cost_usd = EXCLUDED.cost_usd
	`, since, until)
	return err
}

func (w *StatsMinuteRollup) rollupMain(ctx context.Context, since, until time.Time, creditsExpr string) error {
	_, err := w.db.Exec(ctx, `
		INSERT INTO request_stats_minute (
			bucket, tenant_id, provider_id, canonical_id,
			requests, success_count, failure_count,
			prompt_tokens, completion_tokens, total_tokens,
			credits_charged, cost_usd, latency_ms_sum
		)
		SELECT
			date_trunc('minute', r.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
			COALESCE(NULLIF(r.tenant_id, ''), 'default'),
			COALESCE(r.provider_id, 0),
			COALESCE(r.canonical_id, 0),
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE r.request_status = 'success')::bigint,
			COUNT(*) FILTER (WHERE r.request_status = 'failure')::bigint,
			COALESCE(SUM(r.prompt_tokens), 0)::bigint,
			COALESCE(SUM(r.completion_tokens), 0)::bigint,
			COALESCE(SUM(COALESCE(r.prompt_tokens,0)+COALESCE(r.completion_tokens,0)), 0)::bigint,
			COALESCE(SUM(`+creditsExpr+`), 0)::bigint,
			COALESCE(SUM(r.cost_usd), 0),
			COALESCE(SUM(r.latency_ms), 0)::bigint
		FROM request_logs_with_current_month r
		WHERE r.request_status IN ('success', 'failure', 'rate_limited')
		  AND r.ts > $1 AND r.ts <= $2
		GROUP BY 1, 2, 3, 4
		ON CONFLICT (bucket, tenant_id, provider_id, canonical_id) DO UPDATE SET
			requests = EXCLUDED.requests,
			success_count = EXCLUDED.success_count,
			failure_count = EXCLUDED.failure_count,
			prompt_tokens = EXCLUDED.prompt_tokens,
			completion_tokens = EXCLUDED.completion_tokens,
			total_tokens = EXCLUDED.total_tokens,
			credits_charged = EXCLUDED.credits_charged,
			cost_usd = EXCLUDED.cost_usd,
			latency_ms_sum = EXCLUDED.latency_ms_sum
	`, since, until)
	return err
}

func (w *StatsMinuteRollup) rollupDims(ctx context.Context, since, until time.Time, creditsExpr string) error {
	dimQueries := []struct {
		dimType string
		dimKey  string
	}{
		{"client_profile", `COALESCE(NULLIF(r.client_profile, ''), '__unknown__')`},
		{"virtual_ip", `COALESCE(NULLIF(r.virtual_ip, ''), '__unknown__')`},
		{"identity_hash", `COALESCE(NULLIF(r.identity_hash, ''), '__unknown__')`},
		{"model", `COALESCE(NULLIF(r.outbound_model, ''), NULLIF(r.client_model, ''), '__unknown__')`},
		{"tenant", `COALESCE(NULLIF(r.tenant_id, ''), 'default')`},
		{"provider", `COALESCE(r.provider_id, 0)::text`},
		{"error_kind", `COALESCE(NULLIF(r.error_kind, ''), '__unknown__')`},
	}
	for _, dq := range dimQueries {
		_, err := w.db.Exec(ctx, `
			INSERT INTO request_stats_dim_minute (
				bucket, tenant_id, dim_type, dim_key,
				requests, success_count, failure_count,
				total_tokens, credits_charged, cost_usd
			)
			SELECT
				date_trunc('minute', r.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
				COALESCE(NULLIF(r.tenant_id, ''), 'default'),
				$3,
				`+dq.dimKey+`,
				COUNT(*)::bigint,
				COUNT(*) FILTER (WHERE r.success)::bigint,
				COUNT(*) FILTER (WHERE NOT r.success)::bigint,
				COALESCE(SUM(COALESCE(r.prompt_tokens,0)+COALESCE(r.completion_tokens,0)), 0)::bigint,
				COALESCE(SUM(`+creditsExpr+`), 0)::bigint,
				COALESCE(SUM(r.cost_usd), 0)
			FROM request_logs_with_current_month r
			WHERE r.request_status IN ('success', 'failure', 'rate_limited')
			  AND r.ts > $1 AND r.ts <= $2
			  AND ($3 <> 'error_kind' OR r.request_status = 'failure')
			GROUP BY 1, 2, 4
			ON CONFLICT (bucket, tenant_id, dim_type, dim_key) DO UPDATE SET
				requests = EXCLUDED.requests,
				success_count = EXCLUDED.success_count,
				failure_count = EXCLUDED.failure_count,
				total_tokens = EXCLUDED.total_tokens,
				credits_charged = EXCLUDED.credits_charged,
				cost_usd = EXCLUDED.cost_usd
		`, since, until, dq.dimType)
		if err != nil {
			return err
		}
	}
	_, err := w.db.Exec(ctx, `
		INSERT INTO request_stats_error_drill_minute (
			bucket, tenant_id, error_kind, model_name, provider_id, client_profile, requests
		)
		SELECT
			date_trunc('minute', r.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
			COALESCE(NULLIF(r.tenant_id, ''), 'default'),
			COALESCE(NULLIF(r.error_kind, ''), '__unknown__'),
			COALESCE(NULLIF(r.outbound_model, ''), NULLIF(r.client_model, ''), ''),
			COALESCE(r.provider_id, 0),
			COALESCE(NULLIF(r.client_profile, ''), ''),
			COUNT(*)::bigint
		FROM request_logs_with_current_month r
		WHERE r.request_status = 'failure'
		  AND r.ts > $1 AND r.ts <= $2
		GROUP BY 1, 2, 3, 4, 5, 6
		ON CONFLICT (bucket, tenant_id, error_kind, model_name, provider_id, client_profile) DO UPDATE SET
			requests = EXCLUDED.requests
	`, since, until)
	return err
}
