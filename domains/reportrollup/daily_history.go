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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
)

const (
	dailyHistoryZone        = "Asia/Shanghai"
	historyDaysTable        = "public.request_metrics_history_days"
	historyCellsTable       = "public.request_metrics_daily_history"
	tenantHistoryDaysTable  = "public.request_metrics_tenant_history_days"
	tenantHistoryCellTable  = "public.request_metrics_daily_tenant_history"
	historyDetailDaysTable  = "public.request_metrics_history_detail_days"
	tenantModelHistoryTable = "public.request_metrics_daily_tenant_model_history"
	historyPageDaysTable    = "public.request_metrics_history_page_days"
	historyPageCellsTable   = "public.request_metrics_daily_history_page_cells"
)

type historyKind uint8

const (
	historyTotal historyKind = iota
	historyModel
	historyProvider
	historyTenant
	historyTenantModel
	historyPageDimensions
)

const historyFacts = `status, ` + projectedErrorKind + ` AS kind, ` + projectedModelKey + ` AS model,
	provider_id, tenant_id, credential_id, api_key_id, ` + projectedPersonKey + ` AS person,
	prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens, cache_input_tokens,
	total_tokens, credits_charged, latency_ms, cost_amount, cost_currency`

const historyKeys = `report_date, business_only, dimension, group_key`
const historyMeasures = `requests, successes, input_tokens, output_tokens, cache_read, cache_write,
	usd_cents, credits, tokens, cache_covered_read, cache_input, cache_coverage, usd_coverage, latency_samples`

func historyKeyColumns(kind historyKind) string {
	if kind == historyTenantModel || kind == historyPageDimensions {
		return historyKeys + ", tenant_id"
	}
	return historyKeys
}

func historyCellsSQL(kind historyKind) string {
	keys := `report_date, business_only, 'total'::text AS dimension, ''::text AS group_key`
	groupBy := `report_date, business_only`
	switch kind {
	case historyModel:
		keys = `report_date, business_only,
			CASE WHEN GROUPING(model)=0 THEN 'model' ELSE 'total' END AS dimension,
			CASE WHEN GROUPING(model)=0 THEN model ELSE '' END AS group_key`
		groupBy += `, GROUPING SETS ((), (model))`
	case historyProvider:
		keys = `report_date, business_only,
			CASE WHEN GROUPING(model)=0 THEN 'model' WHEN GROUPING(provider_id)=0 THEN 'provider' ELSE 'total' END AS dimension,
			CASE WHEN GROUPING(model)=0 THEN model WHEN GROUPING(provider_id)=0 THEN coalesce(provider_id,-1)::text ELSE '' END AS group_key`
		groupBy += `, GROUPING SETS ((), (model), (provider_id))`
	case historyTenant:
		keys = `report_date, business_only, 'tenant'::text AS dimension, tenant_id AS group_key`
		groupBy += `, tenant_id`
	case historyTenantModel:
		keys = `report_date, business_only, 'model'::text AS dimension, model AS group_key, tenant_id`
		groupBy += `, tenant_id, model`
	case historyPageDimensions:
		keys = `report_date, business_only,
			CASE WHEN GROUPING(person)=0 THEN 'person' WHEN GROUPING(tenant_id)=0 THEN 'tenant'
			     WHEN GROUPING(credential_id)=0 THEN 'credential' WHEN GROUPING(api_key_id)=0 THEN 'apikey'
			     WHEN GROUPING(provider_id)=0 THEN 'provider' ELSE 'model' END AS dimension,
			CASE WHEN GROUPING(person)=0 THEN person WHEN GROUPING(tenant_id)=0 THEN tenant_id
			     WHEN GROUPING(credential_id)=0 THEN coalesce(credential_id,-1)::text WHEN GROUPING(api_key_id)=0 THEN coalesce(api_key_id,-1)::text
			     WHEN GROUPING(provider_id)=0 THEN coalesce(provider_id,-1)::text ELSE model END AS group_key,
			CASE WHEN GROUPING(person)=0 THEN tenant_id ELSE '' END AS tenant_id`
		groupBy += `, GROUPING SETS ((tenant_id, person), (tenant_id), (credential_id), (api_key_id), (provider_id), (model))`
	}
	keyColumns := historyKeyColumns(kind)
	var rankAggregation, rankColumn string
	if kind == historyPageDimensions {
		rankAggregation = `, ` + projectedLatencyRanks + ` FILTER (WHERE ` + projectedLatencySample + `) AS latency_ranks`
		rankColumn = `, latency_ranks`
	}
	return `, measures AS (
		SELECT ` + keys + `, ` + projectedAdditiveMeasures + `,
			coalesce(string_agg(int8send(latency_ms), ''::bytea ORDER BY latency_ms)
				FILTER (WHERE ` + projectedLatencySample + `), ''::bytea) AS latency_samples` + rankAggregation + `
		FROM facts GROUP BY ` + groupBy + `
	), error_counts AS (
		SELECT ` + keys + `, kind, count(*) AS requests
		FROM facts WHERE status<>'success' GROUP BY ` + groupBy + `, kind
	), errors AS (
		SELECT ` + keyColumns + `, jsonb_object_agg(kind, requests) AS counts
		FROM error_counts GROUP BY ` + keyColumns + `
	), raw_cells AS (
		SELECT ` + keyColumns + `, ` + historyMeasures + `, coalesce(counts,'{}') AS errors` + rankColumn + `
		FROM measures LEFT JOIN errors USING (` + keyColumns + `)
	)`
}

// RebuildDailyHistory writes all cells before publishing the day. The caller
// holds the shared daily-state row lock for the duration of the transaction.
func RebuildDailyHistory(ctx context.Context, tx *sql.Tx, day string) error {
	return rebuildDailyHistory(ctx, tx, day, historyProvider)
}

// RebuildDailyTenantHistory publishes business tenant cells under the same
// day lock, independently of the cells an older gateway can rebuild.
func RebuildDailyTenantHistory(ctx context.Context, tx *sql.Tx, day string) error {
	return rebuildDailyHistory(ctx, tx, day, historyTenant)
}

// RebuildDailyPageHistory publishes each single-day dimension with ranks
// computed by the same PostgreSQL aggregate as the live report.
func RebuildDailyPageHistory(ctx context.Context, tx *sql.Tx, day string) error {
	return rebuildDailyHistory(ctx, tx, day, historyPageDimensions)
}

func historyTables(kind historyKind) (days, cells string) {
	switch kind {
	case historyTenant:
		return tenantHistoryDaysTable, tenantHistoryCellTable
	case historyTenantModel:
		return historyDetailDaysTable, tenantModelHistoryTable
	case historyPageDimensions:
		return historyPageDaysTable, historyPageCellsTable
	default:
		return historyDaysTable, historyCellsTable
	}
}

func rebuildDailyHistory(ctx context.Context, tx *sql.Tx, day string, kind historyKind) error {
	if err := rebuildDailyHistoryCells(ctx, tx, day, kind); err != nil {
		return err
	}
	days, _ := historyTables(kind)
	_, err := tx.ExecContext(ctx, `INSERT INTO `+days+` (day) VALUES ($1::date)`, day)
	return err
}

func rebuildDailyHistoryCells(ctx context.Context, tx *sql.Tx, day string, kind historyKind) error {
	_, cells := historyTables(kind)
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+cells+` WHERE report_date=$1::date`, day); err != nil {
		return err
	}
	traffic := `(false), (true)`
	if kind == historyTenant || kind == historyTenantModel {
		traffic = `(true)`
	}
	columns := historyKeyColumns(kind) + `, ` + historyMeasures + `, errors`
	if kind == historyPageDimensions {
		columns += `, latency_ranks`
	}
	_, err := tx.ExecContext(ctx, `WITH facts AS NOT MATERIALIZED (
		SELECT $1::date AS report_date, scope.business_only, `+historyFacts+`
		FROM public.request_metrics_projection CROSS JOIN (VALUES `+traffic+`) scope(business_only)
		WHERE source_fact_id IS NOT NULL AND (NOT scope.business_only OR traffic_class='business')
		  AND occurred_at >= ($1::date::timestamp AT TIME ZONE '`+dailyHistoryZone+`')
		  AND occurred_at < (($1::date+1)::timestamp AT TIME ZONE '`+dailyHistoryZone+`')
	)`+historyCellsSQL(kind)+`
	INSERT INTO `+cells+` (`+columns+`)
	SELECT * FROM raw_cells`, day)
	return err
}

func eligibleDailyHistory(filter GrainFilter, coverage ProjectionCoverage, scope ProjectedSummaryScope) bool {
	if !filter.empty() || coverage.Timezone != dailyHistoryZone || !coverage.Through.After(coverage.From) ||
		(scope != ProjectedSummaryTotals && !scope.includesDistribution()) {
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

const historyFactsSelect = `SELECT (p.occurred_at AT TIME ZONE '` + dailyHistoryZone + `')::date AS report_date,
	$3::boolean AS business_only, ` + historyFacts

const historyFactsFilter = `p.source_fact_id IS NOT NULL AND (NOT $3::boolean OR p.traffic_class='business')`

func dailyHistorySQL(fullyPublished, contiguousGap bool, kind historyKind, tenantScoped bool) string {
	days, cells := historyTables(kind)
	selection := ""
	switch kind {
	case historyTotal:
		selection = " AND dimension='total'"
	case historyModel:
		selection = " AND dimension<>'provider'"
	}
	factsFilter := historyFactsFilter
	gapFrom, gapThrough := "$4", "$5"
	if tenantScoped {
		if kind == historyTenantModel {
			selection += " AND tenant_id=$4"
		} else {
			selection += " AND group_key=$4"
		}
		factsFilter += " AND p.tenant_id=$4"
		gapFrom, gapThrough = "$5", "$6"
	}
	keyColumns := historyKeyColumns(kind)
	published := `SELECT ` + keyColumns + `, ` + historyMeasures + `, errors FROM ` + cells + `
		WHERE report_date >= ` + historyFirstDay + ` AND report_date < ` + historyLastDay + ` AND business_only=$3` + selection
	var input string
	switch {
	case fullyPublished:
		input = `WITH history AS (` + published + `)`
	case contiguousGap:
		// Concrete bounds let the planner estimate the raw branch from the
		// occurrence index. A range-valued join estimates many more raw rows.
		input = `WITH facts AS NOT MATERIALIZED (` + historyFactsSelect + `
		FROM public.request_metrics_projection p WHERE ` + factsFilter + `
		  AND p.occurred_at >= (` + gapFrom + `::date::timestamp AT TIME ZONE '` + dailyHistoryZone + `')
		  AND p.occurred_at < (` + gapThrough + `::date::timestamp AT TIME ZONE '` + dailyHistoryZone + `')
	)` + historyCellsSQL(kind) + `, history AS (` + published + `
		AND (report_date < ` + gapFrom + `::date OR report_date >= ` + gapThrough + `::date)
		UNION ALL SELECT * FROM raw_cells
	)`
	default:
		input = `WITH clean_days AS MATERIALIZED (
		SELECT day FROM ` + days + ` WHERE ` + historyDayBounds + `
	), gaps AS (
		SELECT tstzmultirange(tstzrange($1,$2,'[)')) - coalesce(range_agg(
			tstzrange(day::timestamp AT TIME ZONE '` + dailyHistoryZone + `',
			          (day+1)::timestamp AT TIME ZONE '` + dailyHistoryZone + `', '[)')), '{}') AS missing
		FROM clean_days
	), facts AS NOT MATERIALIZED (` + historyFactsSelect + `
		FROM gaps CROSS JOIN LATERAL unnest(missing) AS r(span)
		JOIN public.request_metrics_projection p ON p.occurred_at >= lower(span) AND p.occurred_at < upper(span)
		WHERE ` + factsFilter + `
	)` + historyCellsSQL(kind) + `, history AS (
		SELECT ` + keyColumns + `, ` + historyMeasures + `, errors
		FROM ` + cells + ` JOIN clean_days ON day=report_date
		WHERE business_only=$3` + selection + `
		UNION ALL SELECT * FROM raw_cells
	)`
	}
	return input + ` SELECT report_date, dimension, group_key, ` + historyMeasures + `, errors FROM history`
}

type dailyHistoryGroup struct {
	kind    groupKind
	row     grainScanRow
	cost    decimal.Decimal
	runs    []latencyRun
	samples int
}

type dailyHistoryCell struct {
	row       grainScanRow
	dimension string
	key       string
	amount    pgtype.Numeric
	samples   latencyRun
	errors    map[string]int64
}

func scanDailyHistoryCell(rows pgx.Rows, extra ...any) (dailyHistoryCell, error) {
	var c dailyHistoryCell
	c.row.projection = &projectedMeasures{}
	c.row.currency = "USD"
	var encoded []byte
	r, p := &c.row, c.row.projection
	columns := []any{&r.date, &c.dimension, &c.key, &r.requestCount, &r.successCount, &r.inputTokens, &r.outputTokens,
		&r.cacheRead, &r.cacheWrite, &c.amount, &r.credits, &p.tokens, &p.cacheRead, &p.cacheInput, &p.cacheMeasured,
		&p.usdMeasured, &encoded, &c.errors}
	if err := rows.Scan(append(columns, extra...)...); err != nil {
		return c, fmt.Errorf("scan daily request history: %w", err)
	}
	if !c.amount.Valid || c.amount.Int == nil {
		return c, fmt.Errorf("invalid daily history amount for %s %q", c.dimension, c.key)
	}
	if len(encoded)%8 != 0 {
		return c, fmt.Errorf("invalid daily latency samples for %s %q: %d bytes", c.dimension, c.key, len(encoded))
	}
	c.samples = latencyRun(encoded)
	p.latencyMeasured = int64(c.samples.size())
	return c, nil
}

// latencyRun holds the sorted int8send values returned by PostgreSQL. Keep
// one copy of the bytes and decode only values inspected during rank selection.
type latencyRun []byte

func (r latencyRun) size() int { return len(r) / 8 }

func (r latencyRun) at(index int) int64 {
	return int64(binary.BigEndian.Uint64(r[index*8:]))
}

// runDailyHistory returns false before emitting rows if the optional schema or
// published days are absent. Scope, coverage and all reads share the caller's
// snapshot so dirty intervals can be combined with published ones exactly once.
func runDailyHistory(ctx context.Context, q Querier, view View, filter GrainFilter, coverage ProjectionCoverage, scope ProjectedSummaryScope, sink func(groupKind, grainScanRow)) (bool, error) {
	kind := historyTotal
	if scope.includesDistribution() {
		kind = historyModel
		if projectedProviderDistribution(view) {
			kind = historyProvider
		}
	}
	return readDailyHistory(ctx, q, coverage, kind, projectedBusinessOnly(view, filter), "", sink)
}

func readDailyHistory(ctx context.Context, q Querier, coverage ProjectionCoverage, kind historyKind, businessOnly bool, tenantID string, sink func(groupKind, grainScanRow)) (bool, error) {
	days, _ := historyTables(kind)
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, days).Scan(&exists); err != nil || !exists {
		return false, err
	}
	var published int
	var missing int
	var firstMissing, lastMissing sql.NullTime
	if err := q.QueryRow(ctx, `SELECT count(h.day), count(*)-count(h.day),
		min(d.day::date) FILTER (WHERE h.day IS NULL), max(d.day::date) FILTER (WHERE h.day IS NULL)
		FROM generate_series(`+historyFirstDay+`::timestamp, (`+historyLastDay+`-1)::timestamp, interval '1 day') AS d(day)
		LEFT JOIN `+days+` h ON h.day=d.day::date`,
		coverage.From, coverage.Through).Scan(&published, &missing, &firstMissing, &lastMissing); err != nil || published == 0 {
		return false, err
	}
	contiguousGap := missing > 0 && firstMissing.Time.AddDate(0, 0, missing-1).Equal(lastMissing.Time)
	args := []any{coverage.From, coverage.Through, businessOnly}
	if tenantID != "" {
		args = append(args, tenantID)
	}
	if contiguousGap {
		args = append(args, firstMissing.Time, lastMissing.Time.AddDate(0, 0, 1))
	}
	rows, err := q.Query(ctx, dailyHistorySQL(missing == 0, contiguousGap, kind, tenantID != ""), args...)
	if err != nil {
		return true, fmt.Errorf("read daily request history: %w", err)
	}
	defer rows.Close()
	groups := make(map[[2]string]*dailyHistoryGroup)
	tenantTotals := kind == historyTenant && tenantID != ""
	for rows.Next() {
		cell, err := scanDailyHistoryCell(rows)
		if err != nil {
			return true, err
		}
		row, dimension, key := cell.row, cell.dimension, cell.key
		amount, samples, errors := cell.amount, cell.samples, cell.errors
		groupKey := [2]string{dimension, key}
		group := groups[groupKey]
		if group == nil {
			group = &dailyHistoryGroup{row: grainScanRow{gDate: true}}
			switch dimension {
			case "total":
				group.kind = gkTotal
			case "provider":
				providerID, err := strconv.ParseInt(key, 10, 64)
				if err != nil {
					return true, fmt.Errorf("read daily history provider: %w", err)
				}
				group.kind, group.row.providerID = gkProvider, &providerID
			case "model":
				group.kind, group.row.model = gkModel, key
			case "tenant":
				group.kind, group.row.tenant = gkTenant, key
			default:
				return true, fmt.Errorf("unknown daily history dimension %q", dimension)
			}
			groups[groupKey] = group
		}
		if amount.Int.Sign() != 0 {
			group.cost = group.cost.Add(decimal.NewFromBigInt(amount.Int, amount.Exp))
		}
		if samples.size() > 0 {
			group.runs = append(group.runs, samples)
			group.samples += samples.size()
		}
		if group.kind == gkTotal || tenantTotals {
			value, err := amount.Float64Value()
			if err != nil {
				return true, fmt.Errorf("read daily history amount: %w", err)
			}
			day := row
			day.costCents = value.Float64
			setHistoryRanks(&day, []latencyRun{samples}, samples.size())
			emitHistoryRow(sink, gkDay, day, errors)
		}
		row.gDate, row.providerID, row.model, row.tenant = true, group.row.providerID, group.row.model, group.row.tenant
		emitHistoryRow(sink, group.kind, row, errors)
		if tenantTotals {
			emitHistoryRow(sink, gkTotal, row, errors)
		}
	}
	if err := rows.Err(); err != nil {
		return true, err
	}
	_, hasTotal := groups[[2]string{"total", ""}]
	if (kind != historyTenant && kind != historyTenantModel && !hasTotal) || (tenantTotals && len(groups) == 0) {
		sink(gkTotal, grainScanRow{gDate: true, projection: &projectedMeasures{}})
	}
	for _, group := range groups {
		group.row.costCents = group.cost.InexactFloat64()
		setHistoryRanks(&group.row, group.runs, group.samples)
		sink(group.kind, group.row)
		if tenantTotals {
			sink(gkTotal, group.row)
		}
	}
	return true, nil
}

func emitHistoryRow(sink func(groupKind, grainScanRow), group groupKind, row grainScanRow, errors map[string]int64) {
	sink(group, row)
	for kind, count := range errors {
		sink(group, grainScanRow{gDate: row.gDate, date: row.date, providerID: row.providerID, model: row.model, tenant: row.tenant, kind: kind, errKindCount: count})
	}
}

// setHistoryRanks matches PostgreSQL percentile_cont across the union of the
// sorted samples. Binary counting selects exact ranks without resorting all rows.
func setHistoryRanks(row *grainScanRow, runs []latencyRun, size int) {
	if size == 0 {
		return
	}
	minimum, maximum := int64(math.MaxInt64), int64(0)
	for _, run := range runs {
		minimum = min(minimum, run.at(0))
		maximum = max(maximum, run.at(run.size()-1))
	}
	upperBound := func(run latencyRun, value int64) int {
		return sort.Search(run.size(), func(i int) bool { return run.at(i) > value })
	}
	at := func(rank int) int64 {
		if len(runs) == 1 {
			return runs[0].at(rank)
		}
		first, last := minimum, maximum
		for first < last {
			middle := first + (last-first)/2
			var count int
			for _, run := range runs {
				count += upperBound(run, middle)
			}
			if count > rank {
				last = middle
			} else {
				first = middle + 1
			}
		}
		return first
	}
	percentile := func(p float64) float64 {
		rank := p * float64(size-1)
		left, right := int(math.Floor(rank)), int(math.Ceil(rank))
		lower := at(left)
		value := float64(lower)
		if left == right {
			return value
		}
		if len(runs) == 1 {
			return value + (rank-float64(left))*(float64(runs[0].at(right))-value)
		}
		// The upper rank is adjacent. It either repeats the lower sample or
		// equals the smallest sample strictly larger than it across the runs.
		count, next := 0, maximum
		for _, run := range runs {
			i := upperBound(run, lower)
			count += i
			if i < run.size() {
				next = min(next, run.at(i))
			}
		}
		if count > right {
			return value
		}
		return value + (rank-float64(left))*(float64(next)-value)
	}
	row.p50w, row.p95w, row.w = percentile(0.5), percentile(0.95), 1
}
