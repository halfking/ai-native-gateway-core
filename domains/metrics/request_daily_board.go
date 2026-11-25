package metrics

import (
	"context"
	"database/sql"
	"fmt"
)

const boardProviderMeasures = `max(nullif(provider_code,'')) AS provider_code,
	max(nullif(provider_name,'')) AS provider_name, count(*) AS request_count,
	count(*) FILTER (WHERE status='success') AS success_count,
	coalesce(sum(prompt_tokens),0) AS prompt_tokens, coalesce(sum(completion_tokens),0) AS completion_tokens,
	coalesce(sum(total_tokens),0) AS total_tokens, coalesce(sum(credits_charged),0) AS credits,
	coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0) AS total_cost_usd`

const boardTrendMeasures = `count(*) FILTER (WHERE status='success') AS success,
	sum(latency_ms) AS latency_sum, count(latency_ms) AS latency_count`

const boardMeasureSums = `sum(requests)::bigint AS requests,
	sum(tokens) AS tokens, sum(credits) AS credits, sum(cost_usd) AS cost_usd`

const rebuildBoardPiesSQL = `WITH inputs AS MATERIALIZED (
			SELECT tenant_id, traffic_class, status, total_tokens, credits_charged, cost_amount, cost_currency,
				` + boardModel + ` AS model, ` + boardPieKeys + `
			FROM public.request_metrics_projection WHERE ` + requestDailyBounds + `
		), groups AS (
			SELECT coalesce(tenant_id,'') AS tenant_id, grouping(tenant_id)=1 AS all_tenants,
				traffic_class, ` + boardPieGrouping + `, ` + boardMeasures + `
			FROM inputs GROUP BY traffic_class, GROUPING SETS ((tenant_id), ()), GROUPING SETS (` + boardPieSets + `)
		)
		INSERT INTO public.request_metrics_board_pies (day, tenant_id, all_tenants, traffic_class, dimension, key, requests, tokens, credits, cost_usd)
		SELECT $1::date, tenant_id, all_tenants, traffic_class, dimension, key, requests, tokens, credits, cost_usd
		FROM groups WHERE key IS NOT NULL`

// RebuildRequestDailyBoard publishes the day only after all widgets are ready.
// The caller holds the summary day's lock.
func RebuildRequestDailyBoard(ctx context.Context, tx *sql.Tx, day string) error {
	statements := []string{
		`WITH pies AS (DELETE FROM public.request_metrics_board_pies WHERE day=$1::date),
		      providers AS (DELETE FROM public.request_metrics_board_providers WHERE day=$1::date),
		      keys AS (DELETE FROM public.request_metrics_board_keys WHERE day=$1::date)
		 DELETE FROM public.request_metrics_board_series WHERE day=$1::date`,
		rebuildBoardPiesSQL,
		`INSERT INTO public.request_metrics_board_providers (
			day, tenant_id, all_tenants, traffic_class, provider_id, provider_code, provider_name, request_count,
			success_count, prompt_tokens, completion_tokens, total_tokens, credits, total_cost_usd)
		SELECT $1::date, coalesce(tenant_id,''), grouping(tenant_id)=1, traffic_class, provider_id, ` + boardProviderMeasures + `
		FROM public.request_metrics_projection
		WHERE ` + requestDailyBounds + ` AND provider_id IS NOT NULL
		GROUP BY traffic_class, provider_id, GROUPING SETS ((tenant_id), ())`,
		`INSERT INTO public.request_metrics_board_keys (day, tenant_id, all_tenants, traffic_class, word, mask)
		SELECT $1::date, coalesce(tenant_id,''), grouping(tenant_id)=1,
			traffic_class, api_key_id >> 6, bit_or(1::bigint << (api_key_id & 63)::int)
		FROM public.request_metrics_projection
		WHERE ` + requestDailyBounds + ` AND api_key_id IS NOT NULL
		GROUP BY traffic_class, api_key_id >> 6, GROUPING SETS ((tenant_id), ())`,
		`WITH inputs AS (
			SELECT tenant_id, traffic_class, minutes, ` + boardBucket("occurred_at", "minutes") + ` AS bucket,
				` + boardModel + ` AS model, total_tokens, credits_charged, cost_amount, cost_currency, status, latency_ms
			FROM public.request_metrics_projection CROSS JOIN (VALUES (5),(15),(60),(360)) sizes(minutes)
			WHERE ` + requestDailyBounds + `
		)
		INSERT INTO public.request_metrics_board_series (
			day, tenant_id, all_tenants, traffic_class, minutes, bucket, is_total, model,
			requests, tokens, credits, cost_usd, success, latency_sum, latency_count)
		SELECT $1::date, coalesce(tenant_id,''), grouping(tenant_id)=1,
			traffic_class, minutes, bucket, grouping(model)=1, coalesce(model,''),
			` + boardMeasures + `, ` + boardTrendMeasures + `
		FROM inputs GROUP BY traffic_class, minutes, bucket, GROUPING SETS ((tenant_id), ()), GROUPING SETS ((model), ())
		HAVING grouping(tenant_id)=0 OR minutes>=15`,
		`INSERT INTO public.request_metrics_board_days (day) VALUES ($1::date)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, day); err != nil {
			return err
		}
	}
	return nil
}

func readRequestBoardDaily(ctx context.Context, db requestStatsDB, query BoardQuery) (RequestBoard, error) {
	clauses, args := buildFilterClauses(query.MetricsFilter, "", "occurred_at", kindTimestamp)
	scope := requestDailyScopeSQL(clauses[2:])
	dailyScope := scope + " AND NOT all_tenants"
	if query.TenantID == "" {
		dailyScope = scope + " AND all_tenants"
	}
	seriesScope := dailyScope
	if query.BucketMinutes < 15 {
		seriesScope = scope + " AND NOT all_tenants"
	}
	args = append(args, query.BucketMinutes)
	minutes := fmt.Sprintf("$%d::int", len(args))
	bucket := boardBucket("occurred_at", minutes)
	querySQL := `WITH projection AS (` + boardProjectionState + `),
clean_days AS MATERIALIZED (
	SELECT day FROM public.request_metrics_daily_state
	JOIN public.request_metrics_board_days USING (day)
	WHERE NOT dirty AND ` + requestCleanDayBounds + `
	  AND (SELECT backfill_complete FROM projection)
),
` + requestUncoveredRanges + `,
bounded AS MATERIALIZED (
	SELECT p.* FROM coverage CROSS JOIN LATERAL unnest(missing) AS gaps(span)
	CROSS JOIN LATERAL (
		SELECT occurred_at, tenant_id, api_key_id, session_id, provider_id, provider_code, provider_name,
			outbound_model, client_model, status, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
			cache_write_tokens, credits_charged, image_tokens, audio_tokens, video_tokens, cost_amount, cost_currency,
			usage_source, cache_input_tokens, latency_ms, ttft_ms, ` + boardModel + ` AS model,
			` + bucket + ` AS bucket, ` + boardPieKeys + `
		FROM public.request_metrics_projection
		WHERE source_fact_id IS NOT NULL AND occurred_at >= $1 AND occurred_at < $2
		  AND occurred_at >= lower(span) AND occurred_at < upper(span)` + scope + ` OFFSET 0
	) p
	WHERE (SELECT backfill_complete FROM projection)
),
pie_inputs AS (
	SELECT dimension, key, requests, tokens, credits, cost_usd
	FROM public.request_metrics_board_pies JOIN clean_days USING (day) WHERE true` + dailyScope + `
	UNION ALL
	SELECT ` + boardPieGrouping + `, ` + boardMeasures + `
	FROM bounded GROUP BY GROUPING SETS (` + boardPieSets + `)
),
pie_groups AS MATERIALIZED (
	SELECT dimension, key, ` + boardMeasureSums + `
	FROM pie_inputs WHERE key IS NOT NULL GROUP BY dimension, key
),
provider_inputs AS (
	SELECT provider_id, provider_code, provider_name, request_count, success_count,
		prompt_tokens, completion_tokens, total_tokens, credits, total_cost_usd
	FROM public.request_metrics_board_providers JOIN clean_days USING (day) WHERE true` + dailyScope + `
	UNION ALL
	SELECT provider_id, ` + boardProviderMeasures + ` FROM bounded WHERE provider_id IS NOT NULL
	GROUP BY provider_id
),
providers AS (
	SELECT provider_id, coalesce(max(provider_code), provider_id::text) AS provider_code,
		coalesce(max(provider_name), max(provider_code), provider_id::text) AS provider_name,
		sum(request_count)::bigint AS request_count, sum(prompt_tokens) AS prompt_tokens,
		sum(completion_tokens) AS completion_tokens, sum(total_tokens) AS total_tokens,
		sum(credits) AS credits, sum(total_cost_usd) AS total_cost_usd,
		sum(success_count)::float8 / sum(request_count) AS success_rate
	FROM provider_inputs GROUP BY provider_id
),
key_inputs AS (
	SELECT word, mask
	FROM public.request_metrics_board_keys JOIN clean_days USING (day) WHERE true` + dailyScope + `
	UNION ALL
	SELECT api_key_id >> 6, bit_or(1::bigint << (api_key_id & 63)::int)
	FROM bounded WHERE api_key_id IS NOT NULL GROUP BY api_key_id >> 6
),
summary AS (
	SELECT measured.*, unique_models AS active_models,
		(SELECT coalesce(sum(bit_count(mask::bit(64))),0) FROM
			(SELECT bit_or(mask) AS mask FROM key_inputs GROUP BY word) words) AS active_api_keys,
		(SELECT count(*) FROM providers) AS providers
	FROM (` + requestSummaryFromDailySQL(scope, "raw AS (SELECT * FROM bounded)",
		"(SELECT count(*) FROM pie_groups WHERE dimension='models' AND key<>'__unknown__')") + `) measured
),
` + boardRankedPies + `,
top_models AS MATERIALIZED (
	SELECT key FROM pie_groups WHERE dimension='models' ORDER BY requests DESC, key LIMIT 6
),
trend_inputs AS (
	SELECT bucket, is_total, model, requests, tokens, credits, cost_usd, success, latency_sum, latency_count
	FROM public.request_metrics_board_series JOIN clean_days USING (day)
	WHERE minutes=` + minutes + seriesScope + ` AND is_total
	UNION ALL
	SELECT bucket, is_total, model, requests, tokens, credits, cost_usd, success, latency_sum, latency_count
	FROM top_models t JOIN public.request_metrics_board_series s ON s.model=t.key
	JOIN clean_days USING (day)
	WHERE minutes=` + minutes + seriesScope + ` AND NOT is_total AND model <> '__others__'
	UNION ALL
	SELECT bucket, grouping(model)=1, coalesce(model,''), ` + boardMeasures + `, ` + boardTrendMeasures + `
	FROM bounded GROUP BY bucket, GROUPING SETS ((model), ())
	HAVING grouping(model)=1 OR (model <> '__others__' AND model IN (SELECT key FROM top_models))
),
` + boardDailyBucketCounts(query, minutes) + `,
trends AS (
	SELECT bucket, requests, tokens, credits, cost_usd, success,
		coalesce(latency_sum::float8/nullif(latency_count,0),0) AS avg_latency_ms
	FROM bucket_counts WHERE is_total
),
top_buckets AS (
	SELECT bucket, ` + boardMeasureSums + ` FROM bucket_counts
	WHERE NOT is_total GROUP BY bucket
),
chart_points AS (
	SELECT bucket, model AS series, requests, tokens, credits, cost_usd FROM bucket_counts WHERE NOT is_total
	UNION ALL
	SELECT b.bucket, '__others__', b.requests-coalesce(t.requests,0), b.tokens-coalesce(t.tokens,0),
		b.credits-coalesce(t.credits,0), b.cost_usd-coalesce(t.cost_usd,0)
	FROM bucket_counts b LEFT JOIN top_buckets t USING (bucket)
	WHERE b.is_total AND b.requests>coalesce(t.requests,0)
)
` + boardResultSQL
	return scanRequestBoard(ctx, db, querySQL, args, true)
}

func boardDailyBucketCounts(query BoardQuery, minutes string) string {
	const grouped = `SELECT bucket, is_total, model, ` + boardMeasureSums + `,
		sum(success) AS success, sum(latency_sum) AS latency_sum, sum(latency_count) AS latency_count
	FROM trend_inputs`
	const groupBy = ` GROUP BY bucket, is_total, model`

	if (query.TenantID == "" && query.BucketMinutes < 15) || (query.IncludeProbes && query.TrafficClass == nil) {
		return `bucket_counts AS MATERIALIZED (` + grouped + groupBy + `)`
	}

	// A single tenant scope and traffic class each supply one row per model and
	// bucket. If published days start and end on bucket boundaries, their rows
	// cannot overlap each other or the grouped rows from uncovered timestamps.
	return `bucket_layout AS MATERIALIZED (
		SELECT NOT EXISTS (
			SELECT FROM clean_days
			CROSS JOIN LATERAL (VALUES (day::timestamp AT TIME ZONE ` + requestDailyZone + `),
				((day+1)::timestamp AT TIME ZONE ` + requestDailyZone + `)) AS edges(boundary)
			WHERE ` + boardBucket("boundary", minutes) + ` <> boundary
		) AS disjoint
	), bucket_counts AS MATERIALIZED (
		SELECT bucket, is_total, model, requests, tokens, credits, cost_usd,
			success, latency_sum, latency_count
		FROM trend_inputs WHERE (SELECT disjoint FROM bucket_layout)
		UNION ALL
		` + grouped + ` WHERE NOT (SELECT disjoint FROM bucket_layout)` + groupBy + `
	)`
}
