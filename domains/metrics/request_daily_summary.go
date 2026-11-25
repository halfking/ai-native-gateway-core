package metrics

import (
	"context"
	"database/sql"
	"strings"
)

const requestDailyZone = "'Asia/Shanghai'"

const requestDailyBounds = `occurred_at >= ($1::date::timestamp AT TIME ZONE ` + requestDailyZone + `)
	AND occurred_at < (($1::date+1)::timestamp AT TIME ZONE ` + requestDailyZone + `)
	AND source_fact_id IS NOT NULL`

const requestStatsDistributionState = `sum(latency_ms) AS latency_sum, count(latency_ms) AS latency_count,
	sum(ttft_ms) AS ttft_sum, count(ttft_ms) AS ttft_count,
	min(occurred_at) AS first_request_at, max(occurred_at) AS last_request_at`

var requestStatsStateColumns = func() string {
	names := make([]string, 0, len(requestStatsCounters))
	for _, counter := range requestStatsCounters {
		names = append(names, counter.name)
	}
	return strings.Join(names, ", ") + `, latency_sum, latency_count, ttft_sum, ttft_count, first_request_at, last_request_at`
}()

// RebuildRequestDailySummary replaces the measures and membership for a day.
// The caller holds that day's invalidation row lock and publishes the clean
// marker in the same transaction.
func RebuildRequestDailySummary(ctx context.Context, tx *sql.Tx, day string) error {
	statements := []string{
		`WITH removed AS (DELETE FROM public.request_metrics_daily_session_masks WHERE day=$1::date)
		DELETE FROM public.request_metrics_daily_summary WHERE day=$1::date`,
		`INSERT INTO public.request_metrics_daily_summary (day, tenant_id, traffic_class, models, ` + requestStatsStateColumns + `)
		SELECT $1::date, tenant_id, traffic_class,
			coalesce(array_agg(DISTINCT model) FILTER (WHERE model <> '__unknown__'), '{}'),
			` + requestStatsRawCounters + `, ` + requestStatsDistributionState + `
		FROM (SELECT *, ` + requestModelKey + ` AS model FROM public.request_metrics_projection
		      WHERE ` + requestDailyBounds + `) r
		GROUP BY tenant_id, traffic_class`,
		`INSERT INTO public.request_metrics_daily_session_codes (tenant_id, session_id)
		SELECT DISTINCT r.tenant_id, r.session_id
		FROM public.request_metrics_projection r
		WHERE ` + requestDailyBounds + ` AND r.session_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM public.request_metrics_daily_session_codes s
		    WHERE (s.tenant_id,s.session_id)=(r.tenant_id,r.session_id))
		ORDER BY r.tenant_id, r.session_id ON CONFLICT (tenant_id,session_id) DO NOTHING`,
		`INSERT INTO public.request_metrics_daily_session_masks (day, tenant_id, traffic_class, word, mask)
		SELECT $1::date, r.tenant_id, r.traffic_class, s.code/64, bit_or(1::bigint << (s.code%64)::int)
		FROM public.request_metrics_projection r
		JOIN public.request_metrics_daily_session_codes s USING (tenant_id,session_id)
		WHERE ` + requestDailyBounds + `
		GROUP BY r.tenant_id, r.traffic_class, s.code/64`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, day); err != nil {
			return err
		}
	}
	return nil
}

func canUseRequestDailySummary(q RequestStatsQuery) bool {
	f := q.MetricsFilter
	start, end := f.effectiveWindow()
	return canUseRequestDailyScope(f) && !start.IsZero() && end.After(start) &&
		q.Credential == nil && q.Canonical == nil && q.APIKey == nil && q.Application == nil &&
		q.Model == "" && len(q.Models) == 0 && q.ClientModel == "" && q.EndUser == "" &&
		q.Owner == "" && q.KeyPrefix == "" && q.AppCode == "" && q.TaskType == ""
}

func canUseRequestDailyScope(f MetricsFilter) bool {
	return f.ProviderID == nil && f.SessionID == nil && f.PayerID == nil && f.TeamID == nil &&
		f.GatewayNodeID == nil && f.Status == nil
}

var requestStatsMergedCounters = requestStatsCounterSQL(true)

const requestCleanDayBounds = `day >= ($1::timestamptz AT TIME ZONE ` + requestDailyZone + `)::date
	AND day < ($2::timestamptz AT TIME ZONE ` + requestDailyZone + `)::date
	AND (day::timestamp AT TIME ZONE ` + requestDailyZone + `) >= $1`

const requestUncoveredRanges = `coverage AS (
	SELECT tstzmultirange(tstzrange($1,$2,'[)')) - coalesce(range_agg(
		tstzrange(day::timestamp AT TIME ZONE ` + requestDailyZone + `,
		          (day+1)::timestamp AT TIME ZONE ` + requestDailyZone + `, '[)')), '{}') AS missing
	FROM clean_days
)`

func requestCleanDaysSQL() string {
	return `WITH clean_days AS MATERIALIZED (
		SELECT day FROM public.request_metrics_daily_state WHERE NOT dirty AND ` + requestCleanDayBounds + `
	)`
}

func requestDailySummaryItemsSQL(scope []string, detail string) string {
	return requestCleanDaysSQL() + `
	SELECT CASE WHEN EXISTS (SELECT 1 FROM clean_days)
		THEN (SELECT jsonb_agg(to_jsonb(items)) FROM (` + requestDailySummarySQL(scope, true) + `) items)
		ELSE (` + detail + `) END`
}

// Keep the fully published window out of the planner's raw scan branch.
func requestCoveredSummaryItemsSQL(scope []string) string {
	const startDay = "($1::timestamptz AT TIME ZONE " + requestDailyZone + ")::date"
	const endDay = "($2::timestamptz AT TIME ZONE " + requestDailyZone + ")::date"
	return requestCleanDaysSQL() + `
	SELECT CASE WHEN (` + startDay + `::timestamp AT TIME ZONE ` + requestDailyZone + `)=$1
	             AND (` + endDay + `::timestamp AT TIME ZONE ` + requestDailyZone + `)=$2
	             AND (SELECT count(*) FROM clean_days)=` + endDay + `-` + startDay + `
		THEN (SELECT jsonb_agg(to_jsonb(items)) FROM (` + requestDailySummarySQL(scope, false) + `) items)
		END`
}

func requestDailySummarySQL(scope []string, includeDetail bool) string {
	filter := requestDailyScopeSQL(scope)
	detail := "raw AS (SELECT * FROM public.request_metrics_projection WHERE false)"
	if includeDetail {
		detail = requestUncoveredRanges + `, raw AS MATERIALIZED (
		SELECT r.* FROM coverage CROSS JOIN LATERAL unnest(missing) AS gaps(span)
		CROSS JOIN LATERAL (
			SELECT occurred_at, tenant_id, session_id, outbound_model, client_model,
				status, prompt_tokens, completion_tokens, total_tokens, cached_tokens, cache_write_tokens,
				image_tokens, audio_tokens, video_tokens, credits_charged, cost_amount, cost_currency,
				usage_source, cache_input_tokens, latency_ms, ttft_ms
			FROM public.request_metrics_projection
			WHERE occurred_at >= $1 AND occurred_at < $2
			  AND occurred_at >= lower(span) AND occurred_at < upper(span)
			  AND source_fact_id IS NOT NULL` + filter + ` OFFSET 0
		) r
	)`
	}
	return requestSummaryFromDailySQL(filter, detail, `(SELECT count(*) FROM (SELECT model FROM models WHERE model <> '__unknown__' GROUP BY model) unique_models)`)
}

func requestDailyScopeSQL(scope []string) string {
	if len(scope) == 0 {
		return ""
	}
	return " AND " + strings.Join(scope, " AND ")
}

func requestSummaryFromDailySQL(filter, detail, uniqueModels string) string {
	return `WITH ` + detail + `, daily AS MATERIALIZED (
		SELECT s.* FROM public.request_metrics_daily_summary s JOIN clean_days USING(day) WHERE true` + filter + `
	), measures AS (
		SELECT ` + requestStatsStateColumns + ` FROM daily
		UNION ALL
		SELECT ` + requestStatsRawCounters + `, ` + requestStatsDistributionState + ` FROM raw
	), models AS (
		SELECT unnest(models) AS model FROM daily
		UNION ALL SELECT ` + requestModelKey + ` FROM raw
	), raw_sessions AS MATERIALIZED (
		SELECT DISTINCT tenant_id, session_id FROM raw WHERE session_id IS NOT NULL
	), raw_codes AS MATERIALIZED (
		SELECT c.code FROM raw_sessions r
		LEFT JOIN public.request_metrics_daily_session_codes c USING (tenant_id,session_id)
	), words AS (
		SELECT word, mask FROM public.request_metrics_daily_session_masks JOIN clean_days USING(day) WHERE true` + filter + `
		UNION ALL
		SELECT code/64, bit_or(1::bigint << (code%64)::int)
		FROM raw_codes WHERE code IS NOT NULL GROUP BY code/64
	)
	SELECT ` + requestStatsMergedCounters + `,
		` + uniqueModels + ` AS unique_models,
		(SELECT coalesce(sum(bit_count(mask::bit(64))),0) FROM
		   (SELECT bit_or(mask) AS mask FROM words GROUP BY word) unique_words)
		   + (SELECT count(*) FROM raw_codes WHERE code IS NULL) AS active_sessions,
		coalesce(sum(latency_sum)/nullif(sum(latency_count),0),0) AS avg_latency_ms,
		coalesce(sum(ttft_sum)/nullif(sum(ttft_count),0),0) AS avg_ttft_ms,
		coalesce(sum(success_count)::float8/nullif(sum(total_requests),0),0) AS success_rate,
		min(first_request_at) AS first_request_at, max(last_request_at) AS last_request_at
	FROM measures`
}
