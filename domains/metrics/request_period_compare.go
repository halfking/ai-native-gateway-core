package metrics

import (
	"context"
	"time"
)

type RequestPeriodComparison struct {
	Ready        bool
	UpdatedAt    *time.Time
	PendingSince *time.Time
	Periods      []RequestPeriodTotals
}

type RequestPeriodTotals struct {
	Ordinal       int                `json:"ordinal"`
	Requests      int64              `json:"requests"`
	Tokens        int64              `json:"tokens"`
	Credits       float64            `json:"credits"`
	CostUSD       float64            `json:"cost_usd"`
	NonUSDRows    int64              `json:"cost_rows_outside_usd"`
	UniqueModels  int                `json:"unique_models"`
	ModelCostsUSD map[string]float64 `json:"model_costs_usd"`
}

const requestPeriodCompareSQL = `
WITH periods (ordinal, start_at, end_at) AS (
    VALUES (0, $1::timestamptz, $2::timestamptz), (1, $3::timestamptz, $4::timestamptz)
), projection AS (
    SELECT backfill_complete, updated_at,
           (SELECT enqueued_at FROM public.request_metrics_pending ORDER BY enqueued_at LIMIT 1) AS pending_since
    FROM public.request_metrics_projection_state WHERE singleton
)
SELECT backfill_complete, updated_at, pending_since,
    CASE WHEN backfill_complete THEN (
        SELECT jsonb_agg(to_jsonb(period_result) ORDER BY ordinal)
        FROM (
            SELECT p.ordinal, coalesce(sum(m.requests),0) AS requests,
                   coalesce(sum(m.tokens),0) AS tokens, coalesce(sum(m.credits),0) AS credits,
                   coalesce(sum(m.cost_usd),0) AS cost_usd, coalesce(sum(m.non_usd),0) AS cost_rows_outside_usd,
                   count(m.model) FILTER (WHERE m.model <> '__unknown__') AS unique_models,
                   coalesce(jsonb_object_agg(m.model, m.cost_usd) FILTER (WHERE m.model IS NOT NULL),'{}') AS model_costs_usd
            FROM periods p
            LEFT JOIN LATERAL (
                SELECT ` + requestModelKey + ` AS model,
                       count(*) AS requests, sum(total_tokens) AS tokens, sum(credits_charged) AS credits,
                       coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0) AS cost_usd,
                       count(*) FILTER (WHERE cost_amount <> 0 AND cost_currency IS DISTINCT FROM 'USD') AS non_usd
                FROM public.request_metrics
                WHERE source_fact_id IS NOT NULL AND occurred_at >= p.start_at AND occurred_at < p.end_at
                  AND ($5 = '' OR tenant_id = $5) AND ($6 = '' OR owner_user = $6)
                  AND ($7::boolean OR traffic_class = 'business')
                GROUP BY 1
            ) m ON true
            GROUP BY p.ordinal
        ) period_result
    ) ELSE '[]'::jsonb END
FROM projection`

// ReadRequestPeriodComparison aggregates both half-open periods in one database snapshot.
func ReadRequestPeriodComparison(ctx context.Context, db requestStatsDB, current MetricsFilter, previousStart, previousEnd time.Time, owner string) (RequestPeriodComparison, error) {
	var result RequestPeriodComparison
	err := db.QueryRow(ctx, requestPeriodCompareSQL, current.StartTime, current.EndTime, previousStart, previousEnd,
		current.TenantID, owner, current.IncludeProbes).Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Periods)
	return result, err
}
