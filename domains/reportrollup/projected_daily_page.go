package reportrollup

import (
	"context"
	"fmt"
	"strconv"
)

const projectedDailyPageSize = 50

type ProjectedDailyPage struct {
	Rows     []DailyGroupRow `json:"rows"`
	Number   int             `json:"number"`
	PageSize int             `json:"page_size"`
	Total    int             `json:"total"`
}

type projectedDailyGroup struct {
	columns string
	first   string
	second  string
	key     func(grainScanRow) string
	numeric bool
}

var projectedDailyGroups = map[GrainGroup]projectedDailyGroup{
	GrainGroupModel: {columns: "raw_model_name", first: "raw_model_name", second: "''::text",
		key: func(r grainScanRow) string { return r.model }},
	GrainGroupProvider: {columns: "provider_id", first: "coalesce(provider_id::text,'-1')", second: "''::text", numeric: true,
		key: func(r grainScanRow) string { return strconv.FormatInt(dimKey(r.providerID), 10) }},
	GrainGroupCredential: {columns: "credential_id", first: "coalesce(credential_id::text,'-1')", second: "''::text", numeric: true,
		key: func(r grainScanRow) string { return strconv.FormatInt(dimKey(r.credentialID), 10) }},
	GrainGroupTenant: {columns: "tenant_id", first: "tenant_id", second: "''::text",
		key: func(r grainScanRow) string { return r.tenant }},
	GrainGroupPerson: {columns: "tenant_id, person", first: "tenant_id", second: "person",
		key: func(r grainScanRow) string { return personKey(r.tenant, r.person) }},
	GrainGroupAPIKey: {columns: "api_key_id", first: "coalesce(api_key_id::text,'-1')", second: "''::text", numeric: true,
		key: func(r grainScanRow) string { return strconv.FormatInt(dimKey(r.apiKeyID), 10) }},
}

// BuildProjectedDailyPage reads one dimension of one completed calendar day.
// The day and all results are covered by the caller's projection snapshot.
func BuildProjectedDailyPage(ctx context.Context, q Querier, view View, filter GrainFilter, coverage ProjectionCoverage, group GrainGroup, number int) (ProjectedDailyPage, error) {
	page := ProjectedDailyPage{Rows: []DailyGroupRow{}, Number: 1, PageSize: projectedDailyPageSize}
	if !coverage.From.Before(coverage.Through) {
		return page, nil
	}

	grain, ok := projectedDailyGroups[group]
	if !ok {
		return page, fmt.Errorf("unknown daily detail group %q", group)
	}
	if used, err := readPublishedDailyPage(ctx, q, view, filter, coverage, group, grain, number, &page); err != nil || used {
		return page, err
	}
	args := []any{coverage.From, coverage.Through, coverage.Timezone, projectedBusinessOnly(view, filter)}
	where, args := projectedGrainWhere(filter, args)
	first, second, err := readProjectedDailyKeys(ctx, q, grain, where, args, number, &page)
	if err != nil || len(first) == 0 {
		return page, err
	}
	if page.Total > page.PageSize {
		where += fmt.Sprintf(` AND (%s, %s) IN (SELECT * FROM unnest($%d::text[], $%d::text[]))`,
			grain.first, grain.second, len(args)+1, len(args)+2)
		args = append(args, first, second)
	}
	columns := "report_date, " + grain.columns + ", status, kind, " + projectedMeasureInputs
	statement := projectedFactsSQL(where, columns, true) +
		projectedGrainAggregates([]string{"(report_date, " + grain.columns + ")"}, false)
	rows := make(map[string]*grainAccumulator, len(first))
	var date string
	if err := readProjectedGroups(ctx, q, statement, args,
		func(_ groupKind, row grainScanRow) {
			date = row.date.Format("2006-01-02")
			getAccStr(rows, grain.key(row)).add(row)
		}); err != nil {
		return page, err
	}
	for i, k := range first {
		if group == GrainGroupPerson {
			k = personKey(k, second[i])
		}
		cell := rows[k]
		if cell == nil {
			return page, fmt.Errorf("daily page measures are absent from the report snapshot")
		}
		page.Rows = append(page.Rows, grain.resultRow(date, k, cell))
	}
	return page, nil
}

func (grain projectedDailyGroup) resultRow(date, key string, cell *grainAccumulator) DailyGroupRow {
	totals := cell.finalize()
	row := DailyGroupRow{Date: date, Key: key, Totals: totals, ErrorBreakdown: cell.breakdown(), QualityScore: ProviderQualityScore(totals)}
	if !grain.numeric {
		row.Name = key
	}
	return row
}

func readPublishedDailyPage(ctx context.Context, q Querier, view View, filter GrainFilter, coverage ProjectionCoverage,
	group GrainGroup, grain projectedDailyGroup, number int, page *ProjectedDailyPage) (bool, error) {
	if !eligibleDailyHistory(filter, coverage, ProjectedSummaryTotals) || coverage.Through.Sub(coverage.From).Hours() != 24 {
		return false, nil
	}
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, historyPageDaysTable).Scan(&exists); err != nil || !exists {
		return false, err
	}
	var published bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+historyPageDaysTable+` WHERE day=`+historyFirstDay+`)`, coverage.From).Scan(&published); err != nil || !published {
		return false, err
	}

	// Resolve the page from dimension keys alone. Wide latency sample buffers
	// are read only for its rows, even when the selected day has many groups.
	where := `report_date=` + historyFirstDay + ` AND business_only=$2 AND dimension=$3`
	query := `WITH keys AS MATERIALIZED (SELECT group_key, tenant_id FROM ` + historyPageCellsTable + ` WHERE ` + where + `),
	bounds AS (
		SELECT count(*) AS total, least($5::bigint, greatest(1, (count(*)+$4::bigint-1)/$4::bigint)) AS number FROM keys
	), page AS (
		SELECT group_key, tenant_id FROM keys ORDER BY tenant_id COLLATE "C", group_key COLLATE "C"
		LIMIT $4 OFFSET (SELECT (number-1)*$4 FROM bounds)
	)
	SELECT report_date, dimension, group_key, ` + historyMeasures + `, errors, tenant_id, b.total, b.number, latency_ranks
	FROM (SELECT * FROM ` + historyPageCellsTable + ` WHERE ` + where + `) c JOIN page p USING (tenant_id, group_key) CROSS JOIN bounds b
	ORDER BY tenant_id COLLATE "C", group_key COLLATE "C"`
	rows, err := q.Query(ctx, query, coverage.From, projectedBusinessOnly(view, filter), string(group), page.PageSize, max(1, number))
	if err != nil {
		return true, fmt.Errorf("read published daily detail: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tenantID string
		var ranks []float64
		cell, err := scanDailyHistoryCell(rows, &tenantID, &page.Total, &page.Number, &ranks)
		if err != nil {
			return true, err
		}
		amount, err := cell.amount.Float64Value()
		if err != nil {
			return true, fmt.Errorf("read daily detail amount: %w", err)
		}
		cell.row.costCents = amount.Float64
		if cell.samples.size() > 0 {
			if len(ranks) != 2 {
				return true, fmt.Errorf("published daily detail lacks latency ranks")
			}
			cell.row.p50w, cell.row.p95w, cell.row.w = ranks[0], ranks[1], 1
		}
		acc := &grainAccumulator{}
		acc.add(cell.row)
		for kind, count := range cell.errors {
			acc.add(grainScanRow{kind: kind, errKindCount: count})
		}
		key := cell.key
		if group == GrainGroupPerson {
			key = personKey(tenantID, key)
		}
		page.Rows = append(page.Rows, grain.resultRow(cell.row.date.Format("2006-01-02"), key, acc))
	}
	return true, rows.Err()
}

func readProjectedDailyKeys(ctx context.Context, q Querier, grain projectedDailyGroup, where string, args []any, number int, page *ProjectedDailyPage) ([]string, []string, error) {
	// Match the full report's byte order and keep tenant/person keys as a pair.
	columns := grain.first + ` COLLATE "C" AS first, ` + grain.second + ` COLLATE "C" AS second`
	query := projectedFactsSQL(where, columns, false) + fmt.Sprintf(`,
	keys AS MATERIALIZED (SELECT DISTINCT first, second FROM facts),
	bounds AS (
		SELECT count(*) AS total,
		       least($%[2]d::bigint, greatest(1, (count(*)+$%[1]d::bigint-1)/$%[1]d::bigint)) AS number
		FROM keys
	), page AS (
		SELECT first, second FROM keys ORDER BY first, second
		LIMIT $%[1]d OFFSET (SELECT (number-1)*$%[1]d FROM bounds)
	)
	SELECT total, number,
		(SELECT ARRAY(SELECT first FROM page ORDER BY first, second)),
		(SELECT ARRAY(SELECT second FROM page ORDER BY first, second))
	FROM bounds`, len(args)+1, len(args)+2)
	var first, second []string
	if err := q.QueryRow(ctx, query, append(args, page.PageSize, max(1, number))...).Scan(&page.Total, &page.Number, &first, &second); err != nil {
		return nil, nil, fmt.Errorf("read projected daily detail keys: %w", err)
	}
	return first, second, nil
}
