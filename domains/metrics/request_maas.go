package metrics

import (
	"context"
	"fmt"
	"time"
)

// MaaSQuery describes a request-attributed usage view within one tenant.
// Credits in these views are request measurements. Ledger postings have their own clock.
type MaaSQuery struct {
	Start, End          time.Time
	Tenant, Owner       string
	Timezone, Model     string
	Day                 string
	Limit, Offset       int
	IncludeUpstreamCost bool
	MatchModelAliases   bool
}

const maasBoundedSQL = `
WITH projection AS (` + boardProjectionState + `), bounded AS MATERIALIZED (
	SELECT occurred_at, fact_request_id AS request_id, ` + requestModelKey + ` AS model, raw_model_name, client_model, tenant_id,
		api_key_owner_user, provider_id, provider_name, credential_id, credential_label, canonical_id,
		prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens, credits_charged,
		CASE WHEN $5::boolean AND cost_currency='USD' THEN cost_amount END AS cost_usd,
		$5::boolean AND cost_amount<>0 AND cost_currency IS DISTINCT FROM 'USD' AS non_usd_cost,
		$5::boolean AND (cost_amount IS NULL OR cost_currency IS DISTINCT FROM 'USD') AS unknown_usd_cost,
		status, error_kind, latency_ms, client_cancelled
	FROM public.request_metrics
	WHERE tenant_id=$3 AND ($4='' OR api_key_owner_user=$4) AND occurred_at >= $1 AND occurred_at < $2
	  AND traffic_class='business' AND source_fact_id IS NOT NULL AND (SELECT backfill_complete FROM projection)
)
`

const maasSummarySQL = maasBoundedSQL + `,
totals AS (
	SELECT count(*) AS requests, coalesce(sum(credits_charged),0) AS credits,
		count(DISTINCT nullif(model,'__unknown__')) AS models,
		count(*) FILTER (WHERE status='success') AS success,
		avg(latency_ms) AS avg_latency_ms,
		CASE WHEN count(*)=0 THEN 0 ELSE sum(cost_usd) END AS usd,
		count(*) FILTER (WHERE non_usd_cost) AS non_usd, count(*) FILTER (WHERE unknown_usd_cost) AS unknown_usd
	FROM bounded
), models AS (
	SELECT model, count(*) AS requests, coalesce(sum(credits_charged),0) AS credits,
		CASE WHEN $5 THEN sum(cost_usd) END AS cost_usd
	FROM bounded GROUP BY model ORDER BY requests DESC, credits DESC, model LIMIT $7
), daily AS (
	SELECT (occurred_at AT TIME ZONE $6)::date AS date, count(*) AS requests,
		coalesce(sum(credits_charged),0) AS credits, CASE WHEN $5 THEN sum(cost_usd) END AS cost_usd
	FROM bounded GROUP BY 1
)
SELECT backfill_complete, updated_at, pending_since,
	CASE WHEN backfill_complete THEN jsonb_strip_nulls(jsonb_build_object(
		'total_requests', totals.requests, 'total_credits', totals.credits,
		'unique_models', totals.models, 'success_count', totals.success, 'avg_latency_ms', totals.avg_latency_ms,
		'total_cost_usd', CASE WHEN $5 THEN totals.usd END,
		'cost_rows_outside_usd', CASE WHEN $5 THEN totals.non_usd END,
		'cost_rows_without_usd_evidence', CASE WHEN $5 THEN totals.unknown_usd END,
		'by_model', coalesce((SELECT jsonb_agg(to_jsonb(models) ORDER BY requests DESC, credits DESC, model) FROM models),'[]'),
		'trend', coalesce((SELECT jsonb_agg(to_jsonb(daily) ORDER BY date) FROM daily),'[]')
	)) ELSE '{}'::jsonb END
FROM projection CROSS JOIN totals`

const maasConsumptionSQL = maasBoundedSQL + `,
detail AS (
	SELECT tenant_id, coalesce(api_key_owner_user,'') AS owner_user, provider_id,
		coalesce(max(provider_name),'') AS provider_name, credential_id, coalesce(max(credential_label),'') AS credential_label,
		canonical_id, model, count(*) AS requests,
		coalesce(sum(prompt_tokens),0) AS prompt_tokens, coalesce(sum(completion_tokens),0) AS completion_tokens,
		coalesce(sum(cached_tokens),0) AS cache_read_tokens, coalesce(sum(cache_write_tokens),0) AS cache_write_tokens,
		coalesce(sum(credits_charged),0) AS credits_charged,
		CASE WHEN $5 THEN sum(cost_usd) END AS upstream_cost_usd,
		CASE WHEN $5 THEN count(*) FILTER (WHERE non_usd_cost) END AS cost_rows_outside_usd,
		CASE WHEN $5 THEN count(*) FILTER (WHERE unknown_usd_cost) END AS cost_rows_without_usd_evidence,
		count(*) FILTER (WHERE credits_charged>0 AND client_cancelled) AS cancelled_billed_requests,
		count(*) FILTER (WHERE credits_charged>0 AND client_cancelled IS NULL) AS billed_without_cancellation_evidence
	FROM bounded GROUP BY tenant_id, api_key_owner_user, provider_id, credential_id, canonical_id, model
)
SELECT backfill_complete, updated_at, pending_since,
	CASE WHEN backfill_complete THEN coalesce((SELECT jsonb_agg(to_jsonb(detail)
		ORDER BY credits_charged DESC, requests DESC, owner_user, provider_id, credential_id, canonical_id, model) FROM detail),'[]')
	ELSE '[]'::jsonb END
FROM projection`

const maasRequestsSQL = maasBoundedSQL + `,
filtered AS MATERIALIZED (
	SELECT * FROM bounded
	WHERE ($6='' OR model=$6 OR ($9::boolean AND (raw_model_name=$6 OR client_model=$6)))
	  AND (nullif($10,'') IS NULL OR (occurred_at AT TIME ZONE $11)::date = nullif($10,'')::date)
), totals AS (
	SELECT count(*) AS requests, coalesce(sum(prompt_tokens),0) AS prompt_tokens,
		coalesce(sum(completion_tokens),0) AS completion_tokens, coalesce(sum(credits_charged),0) AS credits_charged,
		CASE WHEN $5 THEN sum(cost_usd) END AS cost_usd,
		CASE WHEN $5 THEN count(*) FILTER (WHERE non_usd_cost) END AS cost_rows_outside_usd
	FROM filtered
), page AS (
	SELECT request_id, occurred_at AS ts, model, client_model, model AS outbound_model,
		provider_id, coalesce(provider_name,'') AS provider_name,
		credential_id, coalesce(credential_label,'') AS credential_label,
		prompt_tokens, completion_tokens, cached_tokens AS cache_read_tokens,
		(status='success') AS success, status AS request_status, error_kind, credits_charged, cost_usd, latency_ms
	FROM filtered ORDER BY occurred_at DESC, request_id LIMIT $7 OFFSET $8
)
SELECT backfill_complete, updated_at, pending_since,
	CASE WHEN backfill_complete THEN jsonb_build_object('total', totals.requests, 'summary', to_jsonb(totals),
		'rows', coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY ts DESC, request_id) FROM page),'[]'))
	ELSE '{}'::jsonb END
FROM projection CROSS JOIN totals`

func ReadMaaSUsage(ctx context.Context, db requestStatsDB, query MaaSQuery, section string) (ProjectedAccountActivity, error) {
	var result ProjectedAccountActivity
	if query.Tenant == "" || !query.End.After(query.Start) {
		return result, fmt.Errorf("MaaS statistics require a tenant and a valid window")
	}
	args := []any{query.Start, query.End, query.Tenant, query.Owner, query.IncludeUpstreamCost}
	var sql string
	switch section {
	case "summary":
		sql = maasSummarySQL
		args = append(args, query.Timezone, query.Limit)
	case "detail":
		sql = maasConsumptionSQL
	case "requests":
		sql = maasRequestsSQL
		args = append(args, query.Model, query.Limit, query.Offset, query.MatchModelAliases, query.Day, query.Timezone)
	default:
		return result, fmt.Errorf("unsupported MaaS statistics section %q", section)
	}
	err := db.QueryRow(ctx, sql, args...).Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Payload)
	return result, err
}
