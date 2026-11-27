package reportrollup

import (
	"context"
	"fmt"
	"time"
)

const projectedPersonPageSize = 50

// PersonPage describes the slice of the request-ranked person distribution.
type PersonPage struct {
	Number int32 `json:"number"`
	Size   int   `json:"page_size"`
	Total  int64 `json:"total"`
}

// BuildProjectedPersonPage reads the internal distribution and one page of
// exact person rows. The caller supplies a single repeatable-read snapshot.
func BuildProjectedPersonPage(ctx context.Context, q Querier, start, end time.Time, filter GrainFilter, coverage ProjectionCoverage, page int32) (*GrainReport, error) {
	if page < 1 {
		return nil, fmt.Errorf("person page must be positive")
	}
	// Generic plans can repeatedly scan a tenant window for individual page
	// keys. Plan against this page's pairs and date bounds in this transaction.
	if _, err := q.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan"); err != nil {
		return nil, fmt.Errorf("plan projected person page: %w", err)
	}
	report, err := buildGrainReport(ctx, q, start, end, ViewInternal, filter, Names{}, false, &coverage, projectedSummaryPersonPage)
	if err != nil {
		return nil, err
	}
	rows, paging, err := readProjectedPersonPage(ctx, q, coverage, filter, page)
	if err != nil {
		return nil, err
	}
	report.Persons, report.PersonPage = rows, &paging
	return report, nil
}

const projectedPersonOrder = `requests DESC, tenant_id COLLATE "C", person COLLATE "C"`

type personPageKeys struct {
	page    PersonPage
	tenants []string
	people  []string
}

func readProjectedPersonPage(ctx context.Context, q Querier, coverage ProjectionCoverage, filter GrainFilter, number int32) ([]PersonRow, PersonPage, error) {
	args := []any{coverage.From, coverage.Through, coverage.Timezone, true}
	where, args := projectedGrainWhere(filter, args)
	var keys personPageKeys
	var packed bool
	var err error
	if eligibleDailyHistory(filter, coverage, projectedSummaryPersonPage) {
		keys, packed, err = readPackedPersonKeys(ctx, q, coverage, number)
		if err != nil {
			return nil, keys.page, err
		}
	}
	if !packed {
		keys, err = readRawPersonKeys(ctx, q, where, args, number)
		if err != nil {
			return nil, keys.page, err
		}
	}
	if len(keys.people) == 0 {
		return nil, keys.page, nil
	}
	// Both arrays come from the same ordered page. Retain the pair throughout
	// the join so a person shared by two tenants never crosses report scopes.
	tenantArg, personArg := len(args)+1, len(args)+2
	where += fmt.Sprintf(` AND (tenant_id, person) IN (
		SELECT tenant_id, person FROM unnest($%d::text[], $%d::text[]) AS page(tenant_id, person)
	)`, tenantArg, personArg)
	grouped := make(map[string]*grainAccumulator, len(keys.people))
	err = readProjectedGroups(ctx, q, projectedGrainSQL(where, []string{projectedPersonGroup}, false), append(args, keys.tenants, keys.people),
		func(_ groupKind, row grainScanRow) { getAccStr(grouped, personKey(row.tenant, row.person)).add(row) })
	if err != nil {
		return nil, keys.page, err
	}
	rows := make([]PersonRow, 0, len(keys.people))
	for i, name := range keys.people {
		tenant := keys.tenants[i]
		group := grouped[personKey(tenant, name)]
		if group == nil {
			return nil, keys.page, fmt.Errorf("ranked person measures are absent from the report snapshot")
		}
		rows = append(rows, group.personRow(tenant, name))
	}
	return rows, keys.page, nil
}

func readRawPersonKeys(ctx context.Context, q Querier, where string, args []any, number int32) (personPageKeys, error) {
	limitArg, numberArg := len(args)+1, len(args)+2
	rankSQL := projectedFactsSQL(where, "tenant_id, person", false) + fmt.Sprintf(`,
	counts AS MATERIALIZED (
		SELECT tenant_id, person, count(*) AS requests FROM facts GROUP BY tenant_id, person
	), bounds AS (
		SELECT count(*) AS total,
			least($%[2]d::bigint, greatest(1, (count(*)+$%[1]d::bigint-1)/$%[1]d::bigint)) AS number
		FROM counts
	), page AS (
		SELECT tenant_id, person, requests FROM counts ORDER BY `+projectedPersonOrder+`
		LIMIT $%[1]d OFFSET (SELECT (number-1)*$%[1]d FROM bounds)
	)
	SELECT total, number, keys.tenants, keys.people FROM bounds CROSS JOIN (
		SELECT coalesce(array_agg(tenant_id ORDER BY `+projectedPersonOrder+`), '{}') AS tenants,
			coalesce(array_agg(person ORDER BY `+projectedPersonOrder+`), '{}') AS people FROM page
	) keys`, limitArg, numberArg)
	keys := personPageKeys{page: PersonPage{Size: projectedPersonPageSize}}
	if err := q.QueryRow(ctx, rankSQL, append(args, keys.page.Size, number)...).
		Scan(&keys.page.Total, &keys.page.Number, &keys.tenants, &keys.people); err != nil {
		return personPageKeys{}, fmt.Errorf("rank projected report people: %w", err)
	}
	return keys, nil
}
