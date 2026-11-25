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
		totals := cell.finalize()
		row := DailyGroupRow{Date: date, Key: k, Totals: totals, ErrorBreakdown: cell.breakdown(), QualityScore: ProviderQualityScore(totals)}
		if !grain.numeric {
			row.Name = k
		}
		page.Rows = append(page.Rows, row)
	}
	return page, nil
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
