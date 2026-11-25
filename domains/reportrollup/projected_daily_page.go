package reportrollup

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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
	if !eligibleDailyHistory(GrainFilter{}, coverage, ProjectedSummaryTotals) || coverage.Through.Sub(coverage.From).Hours() != 24 {
		return false, nil
	}
	businessOnly := projectedBusinessOnly(view, filter)
	args := []any{coverage.From, businessOnly, string(group), page.PageSize, max(1, number)}
	restriction, args, remaining := publishedDailyRestriction(filter, group, args)
	tenant := remaining.TenantID
	remaining.TenantID = ""
	if !remaining.empty() {
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
	if tenant != "" {
		// A cross-dimension tenant constraint can use complete cells when the
		// tenant accounts for all traffic in the requested day and view.
		var tenants, matching int64
		if err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE group_key=$3)
			FROM `+historyPageCellsTable+` WHERE report_date=`+historyFirstDay+` AND business_only=$2 AND dimension='tenant'`,
			coverage.From, businessOnly, tenant).Scan(&tenants, &matching); err != nil {
			return false, err
		}
		if matching == 0 {
			return true, nil
		}
		if matching != tenants {
			return false, nil
		}
	}

	// Resolve the page from dimension keys alone. Wide latency sample buffers
	// are read only for its rows, even when the selected day has many groups.
	where := `report_date=` + historyFirstDay + ` AND business_only=$2 AND dimension=$3` + restriction
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
	rows, err := q.Query(ctx, query, args...)
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
		acc, err := cell.publishedAccumulator(ranks)
		if err != nil {
			return true, err
		}
		key := cell.key
		if group == GrainGroupPerson {
			key = personKey(tenantID, key)
		}
		page.Rows = append(page.Rows, grain.resultRow(cell.row.date.Format("2006-01-02"), key, &acc))
	}
	return true, rows.Err()
}

// publishedDailyRestriction consumes filters represented by the cell's key.
// The remaining filters must still be satisfied before using the cells.
func publishedDailyRestriction(filter GrainFilter, group GrainGroup, args []any) (string, []any, GrainFilter) {
	var where strings.Builder
	arg := func(value any) int {
		args = append(args, value)
		return len(args)
	}
	textKey := func(column string, key *string) {
		if *key != "" {
			fmt.Fprintf(&where, " AND %s=$%d", column, arg(*key))
			*key = ""
		}
	}
	idKey := func(key **int64) {
		// The unassigned filter selects SQL NULL; verify that from the facts.
		if *key != nil && **key != UnassignedID {
			text := strconv.FormatInt(**key, 10)
			textKey("group_key", &text)
			*key = nil
		}
	}
	switch group {
	case GrainGroupProvider:
		idKey(&filter.ProviderID)
	case GrainGroupCredential:
		idKey(&filter.CredentialID)
	case GrainGroupAPIKey:
		idKey(&filter.APIKeyID)
	case GrainGroupTenant:
		textKey("group_key", &filter.TenantID)
	case GrainGroupPerson:
		textKey("tenant_id", &filter.TenantID)
		textKey("group_key", &filter.Person)
	case GrainGroupModel:
		if len(filter.ModelNames) > 0 {
			fmt.Fprintf(&where, " AND group_key=ANY($%d)", arg(filter.ModelNames))
			filter.Model, filter.ModelNames = "", nil
		}
	}
	return where.String(), args, filter
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
