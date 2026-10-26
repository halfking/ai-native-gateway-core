package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"golang.org/x/sync/errgroup"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// tryTenantStatsFromDailyRollup serves GET tenants/{code}/stats from
// stats_usage_daily when S4 stop-write is on.
//
// Why: after stop-write, logsTable session legs scan ~1e5 parent rows for
// default/7d; by-model / by-application still blow the 10s errgroup budget
// even without the twin anti-join (2026-10-18 245 journal). The daily rollup
// (same source as /api/admin/stats/*) answers provider_model aggregates in
// tens of milliseconds. Application dimension is not rolled up here ⇒
// by_application returns [].
//
// Returns false when the caller should fall through to the raw union path
// (write still enabled, or rollup relation missing).
func (h *Handler) tryTenantStatsFromDailyRollup(w http.ResponseWriter, ctx context.Context, code string, days int) bool {
	if settings.RequestLogsWriteEnabled() {
		return false
	}

	type tenantStats struct {
		Days          int                    `json:"days"`
		TotalRequests int64                  `json:"total_requests"`
		TotalTokens   int64                  `json:"total_tokens"`
		TotalCredits  int64                  `json:"total_credits"`
		TotalCost     float64                `json:"total_cost_usd"`
		UniqueKeys    int                    `json:"unique_keys"`
		UniqueModels  int                    `json:"unique_models"`
		UniqueApps    int                    `json:"unique_apps"`
		InputTokens   int64                  `json:"input_tokens"`
		OutputTokens  int64                  `json:"output_tokens"`
		CacheRead     int64                  `json:"cache_read_tokens"`
		CacheWrite    int64                  `json:"cache_write_tokens"`
		AvgLatencyMs  float64                `json:"avg_latency_ms"`
		ByModel       []tenantModelBreakdown `json:"by_model"`
		ByApplication []tenantAppBreakdown   `json:"by_application"`
		Daily         []tenantDailyStat      `json:"daily"`
	}
	var s tenantStats
	s.Days = days
	s.ByApplication = []tenantAppBreakdown{}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		var latencyCount, latencySum int64
		err := h.db.QueryRow(gctx, `
			SELECT COALESCE(SUM(request_count), 0)::bigint,
			       COALESCE(SUM(total_tokens), 0)::bigint,
			       COALESCE(SUM(credits_charged), 0)::bigint,
			       COALESCE(SUM(cost_usd), 0)::float8,
			       COALESCE(SUM(prompt_tokens), 0)::bigint,
			       COALESCE(SUM(completion_tokens), 0)::bigint,
			       COALESCE(SUM(cache_read_tokens), 0)::bigint,
			       COALESCE(SUM(cache_write_tokens), 0)::bigint,
			       COALESCE(SUM(latency_count), 0)::bigint,
			       COALESCE(SUM(latency_sum_ms), 0)::bigint
			FROM stats_usage_daily
			WHERE tenant_id = $1
			  AND dimension_type = 'provider_model'
			  AND day_utc >= ((timezone('Asia/Shanghai', now()))::date - ($2::int - 1))
		`, code, days).Scan(
			&s.TotalRequests, &s.TotalTokens, &s.TotalCredits, &s.TotalCost,
			&s.InputTokens, &s.OutputTokens, &s.CacheRead, &s.CacheWrite,
			&latencyCount, &latencySum)
		if err != nil {
			return statsStageError{"rollup totals", err}
		}
		if latencyCount > 0 {
			s.AvgLatencyMs = float64(latencySum) / float64(latencyCount)
		}
		return nil
	})

	g.Go(func() error {
		rows, err := h.db.Query(gctx, `
			SELECT COALESCE(NULLIF(TRIM(raw_model_name), ''), '<unknown>') AS model,
			       COALESCE(SUM(request_count), 0)::bigint,
			       COALESCE(SUM(total_tokens), 0)::bigint,
			       COALESCE(SUM(credits_charged), 0)::bigint,
			       COALESCE(SUM(cost_usd), 0)::float8
			FROM stats_usage_daily
			WHERE tenant_id = $1
			  AND dimension_type = 'provider_model'
			  AND day_utc >= ((timezone('Asia/Shanghai', now()))::date - ($2::int - 1))
			GROUP BY 1
			ORDER BY SUM(request_count) DESC, SUM(cost_usd) DESC
			LIMIT 20
		`, code, days)
		if err != nil {
			return statsStageError{"rollup by-model", err}
		}
		defer rows.Close()
		for rows.Next() {
			var m tenantModelBreakdown
			if scanErr := rows.Scan(&m.Model, &m.Requests, &m.Tokens, &m.Credits, &m.Cost); scanErr != nil {
				warnRowSkip("tenants.stats.rollup.byModel", scanErr)
				continue
			}
			s.ByModel = append(s.ByModel, m)
		}
		if rerr := rows.Err(); rerr != nil {
			return statsStageError{"rollup by-model iteration", rerr}
		}
		s.UniqueModels = len(s.ByModel)
		return nil
	})

	g.Go(func() error {
		rows, err := h.db.Query(gctx, `
			WITH days AS (
				SELECT generate_series(
					date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') - (($2::int - 1) * INTERVAL '1 day'),
					date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai'),
					INTERVAL '1 day')::date AS d
			), agg AS (
				SELECT day_utc AS d,
				       COALESCE(SUM(request_count), 0)::bigint AS requests,
				       COALESCE(SUM(success_count), 0)::bigint AS success,
				       COALESCE(SUM(failure_count), 0)::bigint AS errors,
				       COALESCE(SUM(total_tokens), 0)::bigint AS tokens,
				       COALESCE(SUM(credits_charged), 0)::bigint AS credits,
				       COALESCE(SUM(cost_usd), 0)::float8 AS cost
				FROM stats_usage_daily
				WHERE tenant_id = $1
				  AND dimension_type = 'provider_model'
				  AND day_utc >= ((timezone('Asia/Shanghai', now()))::date - ($2::int - 1))
				GROUP BY 1
			)
			SELECT to_char(days.d, 'YYYY-MM-DD'),
			       COALESCE(agg.requests, 0), COALESCE(agg.success, 0), COALESCE(agg.errors, 0),
			       COALESCE(agg.tokens, 0), COALESCE(agg.credits, 0), COALESCE(agg.cost, 0)
			FROM days LEFT JOIN agg ON agg.d = days.d
			ORDER BY days.d
		`, code, days)
		if err != nil {
			return statsStageError{"rollup daily", err}
		}
		for rows.Next() {
			var d tenantDailyStat
			if scanErr := rows.Scan(&d.Date, &d.Requests, &d.Success, &d.Errors, &d.Tokens, &d.Credits, &d.Cost); scanErr != nil {
				rows.Close()
				return statsStageError{"rollup daily scan", scanErr}
			}
			s.Daily = append(s.Daily, d)
		}
		if rerr := rows.Err(); rerr != nil {
			rows.Close()
			return statsStageError{"rollup daily iteration", rerr}
		}
		rows.Close()
		return nil
	})

	if err := g.Wait(); err != nil {
		var se statsStageError
		if errors.As(err, &se) {
			if isMissingStatsRelation(se.err) {
				slog.Warn("tenant stats: daily rollup missing; falling back to raw union",
					"tenant", code, "stage", se.stage, "err", se.err)
				return false
			}
			writeTenantStatsError(w, ctx, code, se.stage, se.err)
			return true
		}
		writeTenantStatsError(w, ctx, code, "rollup pipeline", err)
		return true
	}
	if s.ByModel == nil {
		s.ByModel = []tenantModelBreakdown{}
	}
	if s.Daily == nil {
		s.Daily = []tenantDailyStat{}
	}
	writeJSON(w, http.StatusOK, s)
	return true
}
