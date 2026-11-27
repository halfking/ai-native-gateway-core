package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type ProjectedAccountActivity struct {
	Ready        bool
	UpdatedAt    *time.Time
	PendingSince *time.Time
	Payload      json.RawMessage
}

const accountActivityMeasures = `
	count(*) AS requests,
	coalesce(sum(total_tokens),0) AS tokens,
	coalesce(sum(credits_charged),0) AS credits,
	coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0) AS cost_usd,
	count(*) FILTER (WHERE cost_amount <> 0 AND cost_currency IS DISTINCT FROM 'USD') AS cost_rows_outside_usd`

const accountUsageReadSQL = `
WITH projection AS (` + boardProjectionState + `)
SELECT backfill_complete, updated_at, pending_since,
	CASE WHEN backfill_complete THEN (
		SELECT coalesce(jsonb_agg(to_jsonb(accounts) ORDER BY requests DESC, tenant_id, username),'[]'::jsonb)
		FROM (
			SELECT m.tenant_id, m.owner_user AS username, count(*) AS requests,
				coalesce(sum(m.total_tokens),0) AS tokens, coalesce(sum(m.credits_charged),0) AS credits,
				max(m.occurred_at) AS last_active_at
			FROM public.request_metrics_projection m
			WHERE m.source_fact_id IS NOT NULL AND m.occurred_at >= $1 AND m.occurred_at < $2
			  AND ($3 = '' OR m.tenant_id=$3) AND ($4::boolean OR m.traffic_class='business')
			  AND EXISTS (SELECT 1 FROM public.users u WHERE u.tenant_id=m.tenant_id AND u.username=m.owner_user)
			GROUP BY m.tenant_id, m.owner_user
		) accounts
	) ELSE '[]'::jsonb END
FROM projection`

// ReadProjectedAccounts summarizes usage for registered users without merging identical names across tenants.
func ReadProjectedAccounts(ctx context.Context, db requestStatsDB, filter MetricsFilter) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	err := db.QueryRow(ctx, accountUsageReadSQL, filter.StartTime, filter.EndTime, filter.TenantID, filter.IncludeProbes).
		Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}

const accountActivityReadSQL = `
WITH projection AS (` + boardProjectionState + `),
bounded AS MATERIALIZED (
	SELECT occurred_at, request_id, ` + requestModelKey + ` AS model, application_code, application_id,
		api_key_id, api_key_prefix, latency_ms, ttft_ms, total_tokens, credits_charged, status, cost_amount, cost_currency,
		prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens
	FROM public.request_metrics_projection
	WHERE source_fact_id IS NOT NULL AND tenant_id=$3 AND ($4='' OR owner_user=$4) AND occurred_at >= $1 AND occurred_at < $2
	  AND ($6::boolean OR traffic_class='business') AND (SELECT backfill_complete FROM projection)
), kpi AS (
	SELECT ` + accountActivityMeasures + `,
		count(DISTINCT api_key_id) AS unique_keys, count(DISTINCT nullif(model,'__unknown__')) AS unique_models,
		count(DISTINCT application_id) AS unique_apps, coalesce(sum(prompt_tokens),0) AS input_tokens,
		coalesce(sum(completion_tokens),0) AS output_tokens, coalesce(sum(cached_tokens),0) AS cache_read_tokens,
		coalesce(sum(cache_write_tokens),0) AS cache_write_tokens, coalesce(avg(latency_ms),0) AS avg_latency_ms,
		count(*) FILTER (WHERE status <> 'success') AS errors,
		coalesce(count(*) FILTER (WHERE status <> 'success')::float8/nullif(count(*),0),0) AS error_rate,
		percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE latency_ms>0)::bigint AS latency_p95_ms
	FROM bounded
), daily_counts AS (
	SELECT (occurred_at AT TIME ZONE $5)::date AS date, ` + accountActivityMeasures + `,
		count(*) FILTER (WHERE status='success') AS success,
		count(*) FILTER (WHERE status<>'success') AS errors
	FROM bounded GROUP BY 1
), daily AS (
	SELECT days.day::date AS date, coalesce(d.requests,0) AS requests, coalesce(d.tokens,0) AS tokens,
		coalesce(d.credits,0) AS credits, coalesce(d.cost_usd,0) AS cost_usd,
		coalesce(d.success,0) AS success, coalesce(d.errors,0) AS errors
	FROM generate_series(($1::timestamptz AT TIME ZONE $5)::date,
		(($2::timestamptz - interval '1 microsecond') AT TIME ZONE $5)::date, interval '1 day') AS days(day)
	LEFT JOIN daily_counts d ON d.date=days.day::date
), buckets AS (
	SELECT d.kind, d.id, max(d.name) AS name, ` + accountActivityMeasures + `
	FROM bounded
	CROSS JOIN LATERAL (VALUES
		('models', NULL::bigint, model, model),
		('apps', NULL::bigint, coalesce(nullif(application_code,''), application_id::text, '__unknown__'), coalesce(nullif(application_code,''), application_id::text, '__unknown__')),
		('keys', api_key_id, api_key_id::text, nullif(api_key_prefix,''))
	) d(kind,id,bucket_key,name)
	WHERE d.bucket_key IS NOT NULL AND ($4<>'' OR d.kind <> 'keys')
	GROUP BY d.kind, d.id, d.bucket_key
), top_buckets AS (
	SELECT kind, jsonb_agg(to_jsonb(ranked) - 'kind' - 'rank' ORDER BY rank) AS rows
	FROM (
		SELECT kind, id, coalesce(name,id::text) AS name, requests, tokens, credits, cost_usd, cost_rows_outside_usd,
			row_number() OVER (PARTITION BY kind ORDER BY CASE WHEN $4='' THEN credits END DESC,
				CASE WHEN $4='' THEN cost_usd END DESC, requests DESC, name, id) AS rank FROM buckets
	) ranked WHERE rank<=CASE WHEN $4='' THEN 20 ELSE 5 END GROUP BY kind
), recent AS (
	SELECT request_id, occurred_at AS ts, model, ttft_ms AS first_chunk_ms, latency_ms AS total_ms, credits_charged AS credits, status
	FROM bounded WHERE $4<>'' ORDER BY occurred_at DESC, request_id LIMIT 10
)
SELECT backfill_complete, updated_at, pending_since,
	CASE WHEN backfill_complete THEN jsonb_build_object(
		'kpi', to_jsonb(kpi),
		'daily', coalesce((SELECT jsonb_agg(to_jsonb(daily) ORDER BY date) FROM daily),'[]'::jsonb),
		'top_models', coalesce((SELECT rows FROM top_buckets WHERE kind='models'),'[]'::jsonb),
		'top_apps', coalesce((SELECT rows FROM top_buckets WHERE kind='apps'),'[]'::jsonb),
		'top_keys', coalesce((SELECT rows FROM top_buckets WHERE kind='keys'),'[]'::jsonb),
		'recent_requests', coalesce((SELECT jsonb_agg(to_jsonb(recent) ORDER BY ts DESC, request_id) FROM recent),'[]'::jsonb),
		'cost_rows_outside_usd', kpi.cost_rows_outside_usd
	) ELSE '{}'::jsonb END
FROM projection CROSS JOIN kpi`

// ReadProjectedAccountActivity uses one bounded request snapshot for every account widget.
func ReadProjectedAccountActivity(ctx context.Context, db requestStatsDB, filter MetricsFilter, username, timezone string) (ProjectedAccountActivity, error) {
	if username == "" {
		return ProjectedAccountActivity{}, errors.New("account activity requires a username")
	}
	return readProjectedActivity(ctx, db, filter, username, timezone)
}

func ReadProjectedTenantActivity(ctx context.Context, db requestStatsDB, filter MetricsFilter, timezone string) (ProjectedAccountActivity, error) {
	return readProjectedActivity(ctx, db, filter, "", timezone)
}

func readProjectedActivity(ctx context.Context, db requestStatsDB, filter MetricsFilter, username, timezone string) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	if filter.TenantID == "" {
		return result, errors.New("activity requires a tenant")
	}
	err := db.QueryRow(ctx, accountActivityReadSQL, filter.StartTime, filter.EndTime, filter.TenantID, username, timezone, filter.IncludeProbes).
		Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}
