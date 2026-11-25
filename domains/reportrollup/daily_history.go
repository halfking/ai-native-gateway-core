package reportrollup

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

const dailyHistoryZone = "Asia/Shanghai"

const historyFacts = `status, ` + projectedErrorKind + ` AS kind, ` + projectedModelKey + ` AS model,
	provider_id, prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens, cache_input_tokens,
	total_tokens, credits_charged, latency_ms, cost_amount, cost_currency`

const historyKeys = `report_date, business_only, dimension, group_key`
const historyMeasures = `requests, successes, input_tokens, output_tokens, cache_read, cache_write,
	usd_cents, credits, tokens, cache_covered_read, cache_input, cache_coverage, usd_coverage, latency_samples`

func historyCellsSQL(distribution string) string {
	return `, cells AS MATERIALIZED (
		SELECT f.*, d.dimension, d.group_key FROM facts f
		CROSS JOIN LATERAL (
			SELECT 'total'::text, ''::text
			UNION ALL SELECT 'provider', coalesce(provider_id,-1)::text WHERE ` + distribution + `
			UNION ALL SELECT 'model', model WHERE ` + distribution + `
		) AS d(dimension, group_key)
	), measures AS (
		SELECT ` + historyKeys + `, ` + projectedAdditiveMeasures + `,
			coalesce(string_agg(int8send(latency_ms), ''::bytea ORDER BY latency_ms)
				FILTER (WHERE ` + projectedLatencySample + `), ''::bytea) AS latency_samples
		FROM cells GROUP BY ` + historyKeys + `
	), error_counts AS (
		SELECT ` + historyKeys + `, kind, count(*) AS requests
		FROM cells WHERE status<>'success' GROUP BY ` + historyKeys + `, kind
	), errors AS (
		SELECT ` + historyKeys + `, jsonb_object_agg(kind, requests) AS counts
		FROM error_counts GROUP BY ` + historyKeys + `
	), raw_cells AS (
		SELECT ` + historyKeys + `, ` + historyMeasures + `, coalesce(counts,'{}') AS errors
		FROM measures LEFT JOIN errors USING (` + historyKeys + `)
	)`
}

// RebuildDailyHistory writes all cells before publishing the day. The caller
// holds the shared daily-state row lock for the duration of the transaction.
func RebuildDailyHistory(ctx context.Context, tx *sql.Tx, day string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM public.request_metrics_daily_history WHERE report_date=$1::date`, day); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `WITH facts AS (
		SELECT $1::date AS report_date, scope.business_only, `+historyFacts+`
		FROM public.request_metrics_projection CROSS JOIN (VALUES (false), (true)) scope(business_only)
		WHERE source_fact_id IS NOT NULL AND (NOT scope.business_only OR traffic_class='business')
		  AND occurred_at >= ($1::date::timestamp AT TIME ZONE '`+dailyHistoryZone+`')
		  AND occurred_at < (($1::date+1)::timestamp AT TIME ZONE '`+dailyHistoryZone+`')
	)`+historyCellsSQL("true")+`
	INSERT INTO public.request_metrics_daily_history (`+historyKeys+`, `+historyMeasures+`, errors)
	SELECT * FROM raw_cells`, day)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO public.request_metrics_history_days (day) VALUES ($1::date)`, day)
	return err
}

func eligibleDailyHistory(view View, filter GrainFilter, coverage ProjectionCoverage, scope ProjectedSummaryScope) bool {
	if !filter.empty() || coverage.Timezone != dailyHistoryZone || !coverage.Through.After(coverage.From) ||
		(scope != ProjectedSummaryTotals && !(scope == ProjectedSummaryDistribution && (view == ViewProvider || view == ViewCredential))) {
		return false
	}
	loc, err := time.LoadLocation(dailyHistoryZone)
	if err != nil {
		return false
	}
	midnight := func(bound time.Time) bool {
		hour, minute, second := bound.In(loc).Clock()
		return hour == 0 && minute == 0 && second == 0 && bound.Nanosecond() == 0
	}
	return midnight(coverage.From) && midnight(coverage.Through)
}

const historyFirstDay = `($1::timestamptz AT TIME ZONE '` + dailyHistoryZone + `')::date`
const historyLastDay = `($2::timestamptz AT TIME ZONE '` + dailyHistoryZone + `')::date`
const historyDayBounds = `day >= ` + historyFirstDay + ` AND day < ` + historyLastDay

func dailyHistorySQL(fullyPublished bool) string {
	input := `WITH history AS (
		SELECT ` + historyKeys + `, ` + historyMeasures + `, errors
		FROM public.request_metrics_daily_history
		WHERE report_date >= ` + historyFirstDay + ` AND report_date < ` + historyLastDay + `
		  AND business_only=$3 AND ($4::boolean OR dimension='total')
	)`
	if !fullyPublished {
		input = `WITH clean_days AS MATERIALIZED (
		SELECT day FROM public.request_metrics_history_days WHERE ` + historyDayBounds + `
	), gaps AS (
		SELECT tstzmultirange(tstzrange($1,$2,'[)')) - coalesce(range_agg(
			tstzrange(day::timestamp AT TIME ZONE '` + dailyHistoryZone + `',
			          (day+1)::timestamp AT TIME ZONE '` + dailyHistoryZone + `', '[)')), '{}') AS missing
		FROM clean_days
	), facts AS (
		SELECT (p.occurred_at AT TIME ZONE '` + dailyHistoryZone + `')::date AS report_date,
			$3::boolean AS business_only, ` + historyFacts + `
		FROM gaps CROSS JOIN LATERAL unnest(missing) AS r(span)
		JOIN public.request_metrics_projection p ON p.occurred_at >= lower(span) AND p.occurred_at < upper(span)
		WHERE p.source_fact_id IS NOT NULL AND (NOT $3::boolean OR p.traffic_class='business')
	)` + historyCellsSQL("$4::boolean") + `, history AS (
		SELECT ` + historyKeys + `, ` + historyMeasures + `, errors
		FROM public.request_metrics_daily_history JOIN clean_days ON day=report_date
		WHERE business_only=$3 AND ($4::boolean OR dimension='total')
		UNION ALL SELECT * FROM raw_cells
	)`
	}
	return input + `, distribution_errors AS (
		SELECT dimension, group_key, kind, sum(count::bigint)::bigint AS requests
		FROM history CROSS JOIN LATERAL jsonb_each_text(errors) AS e(kind, count)
		WHERE dimension<>'total' GROUP BY dimension, group_key, kind
	), error_breakdowns AS (
		SELECT dimension, group_key, jsonb_object_agg(kind, requests) AS counts
		FROM distribution_errors GROUP BY dimension, group_key
	), distributions AS (
		SELECT dimension, group_key, sum(requests)::bigint AS requests, sum(successes)::bigint AS successes,
			sum(input_tokens)::bigint AS input_tokens, sum(output_tokens)::bigint AS output_tokens,
			sum(cache_read)::bigint AS cache_read, sum(cache_write)::bigint AS cache_write,
			sum(usd_cents) AS usd_cents, sum(credits)::bigint AS credits, sum(tokens)::bigint AS tokens,
			sum(cache_covered_read)::bigint AS cache_covered_read, sum(cache_input)::bigint AS cache_input,
			sum(cache_coverage)::bigint AS cache_coverage, sum(usd_coverage)::bigint AS usd_coverage,
			coalesce(array_agg(latency_samples) FILTER (WHERE octet_length(latency_samples)>0), '{}') AS latency_runs
		FROM history WHERE dimension<>'total' GROUP BY dimension, group_key
	)
	SELECT report_date, dimension, group_key, requests, successes, input_tokens, output_tokens, cache_read, cache_write,
		usd_cents, credits, tokens, cache_covered_read, cache_input, cache_coverage, usd_coverage,
		ARRAY[latency_samples], errors, sum(usd_cents) OVER () AS interval_usd_cents
	FROM history WHERE dimension='total'
	UNION ALL
	SELECT NULL::date, dimension, group_key, requests, successes, input_tokens, output_tokens, cache_read, cache_write,
		usd_cents, credits, tokens, cache_covered_read, cache_input, cache_coverage, usd_coverage,
		latency_runs, coalesce(counts,'{}'), 0::numeric
	FROM distributions LEFT JOIN error_breakdowns USING (dimension,group_key)`
}

// runDailyHistory returns false before emitting rows if the optional schema or
// published days are absent. Scope, coverage and all reads share the caller's
// snapshot so dirty intervals can be combined with published ones exactly once.
func runDailyHistory(ctx context.Context, q Querier, view View, filter GrainFilter, coverage ProjectionCoverage, scope ProjectedSummaryScope, sink func(groupKind, grainScanRow)) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('public.request_metrics_history_days') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return false, err
	}
	var published int
	var fullyPublished bool
	if err := q.QueryRow(ctx, `SELECT count(*), count(*)=(`+historyLastDay+`-`+historyFirstDay+`)
		FROM public.request_metrics_history_days WHERE `+historyDayBounds,
		coverage.From, coverage.Through).Scan(&published, &fullyPublished); err != nil || published == 0 {
		return false, err
	}
	rows, err := q.Query(ctx, dailyHistorySQL(fullyPublished), coverage.From, coverage.Through, projectedBusinessOnly(view, filter), scope == ProjectedSummaryDistribution)
	if err != nil {
		return true, fmt.Errorf("read daily request history: %w", err)
	}
	defer rows.Close()
	var totalRuns [][]int64
	var totalSamples int
	totalRow := grainScanRow{gDate: true}
	seenTotal := false
	for rows.Next() {
		var r grainScanRow
		r.projection = &projectedMeasures{}
		r.currency = "USD"
		var date sql.NullTime
		var dimension, key string
		var encodedRuns [][]byte
		var errors map[string]int64
		var intervalUSD float64
		p := r.projection
		if err := rows.Scan(&date, &dimension, &key, &r.requestCount, &r.successCount, &r.inputTokens, &r.outputTokens,
			&r.cacheRead, &r.cacheWrite, &r.costCents, &r.credits, &p.tokens, &p.cacheRead, &p.cacheInput, &p.cacheMeasured,
			&p.usdMeasured, &encodedRuns, &errors, &intervalUSD); err != nil {
			return true, fmt.Errorf("scan daily request history: %w", err)
		}
		var size int
		runs := make([][]int64, 0, len(encodedRuns))
		for _, encoded := range encodedRuns {
			if len(encoded) == 0 {
				continue
			}
			samples := make([]int64, len(encoded)/8)
			if _, err := binary.Decode(encoded, binary.BigEndian, samples); err != nil {
				return true, fmt.Errorf("decode daily latency samples: %w", err)
			}
			runs = append(runs, samples)
			size += len(samples)
		}
		p.latencyMeasured = int64(size)
		switch dimension {
		case "total":
			seenTotal = true
			totalRow.costCents = intervalUSD
			totalRuns = append(totalRuns, runs...)
			totalSamples += size
			r.date = date.Time
			day := r
			setHistoryRanks(&day, runs, size)
			emitHistoryRow(sink, gkDay, day, errors)
			r.gDate, r.costCents = true, 0
			emitHistoryRow(sink, gkTotal, r, errors)
		case "provider":
			providerID, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				return true, fmt.Errorf("read daily history provider: %w", err)
			}
			r.gDate, r.providerID = true, &providerID
			setHistoryRanks(&r, runs, size)
			emitHistoryRow(sink, gkProvider, r, errors)
		case "model":
			r.gDate, r.model = true, key
			setHistoryRanks(&r, runs, size)
			emitHistoryRow(sink, gkModel, r, errors)
		default:
			return true, fmt.Errorf("unknown daily history dimension %q", dimension)
		}
	}
	if err := rows.Err(); err != nil {
		return true, err
	}
	if !seenTotal {
		totalRow.projection = &projectedMeasures{}
	}
	setHistoryRanks(&totalRow, totalRuns, totalSamples)
	sink(gkTotal, totalRow)
	return true, nil
}

func emitHistoryRow(sink func(groupKind, grainScanRow), group groupKind, row grainScanRow, errors map[string]int64) {
	sink(group, row)
	for kind, count := range errors {
		sink(group, grainScanRow{gDate: row.gDate, date: row.date, providerID: row.providerID, model: row.model, kind: kind, errKindCount: count})
	}
}

// setHistoryRanks matches PostgreSQL percentile_cont across the union of the
// sorted samples. Binary counting selects exact ranks without resorting all rows.
func setHistoryRanks(row *grainScanRow, runs [][]int64, size int) {
	if size == 0 {
		return
	}
	minimum, maximum := int64(math.MaxInt64), int64(0)
	for _, run := range runs {
		minimum = min(minimum, run[0])
		maximum = max(maximum, run[len(run)-1])
	}
	at := func(rank int) float64 {
		if len(runs) == 1 {
			return float64(runs[0][rank])
		}
		first, last := minimum, maximum
		for first < last {
			middle := first + (last-first)/2
			var count int
			for _, run := range runs {
				count += sort.Search(len(run), func(i int) bool { return run[i] > middle })
			}
			if count > rank {
				last = middle
			} else {
				first = middle + 1
			}
		}
		return float64(first)
	}
	percentile := func(p float64) float64 {
		rank := p * float64(size-1)
		left, right := int(math.Floor(rank)), int(math.Ceil(rank))
		value := at(left)
		if left == right {
			return value
		}
		return value + (rank-float64(left))*(at(right)-value)
	}
	row.p50w, row.p95w, row.w = percentile(0.5), percentile(0.95), 1
}
