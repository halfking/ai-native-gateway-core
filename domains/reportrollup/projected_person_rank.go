package reportrollup

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
)

const personRankFacts = `FROM public.request_metrics_projection
	WHERE source_fact_id IS NOT NULL AND traffic_class='business'
	  AND occurred_at >= ($1::date::timestamp AT TIME ZONE '` + dailyHistoryZone + `')
	  AND occurred_at < (($1::date+1)::timestamp AT TIME ZONE '` + dailyHistoryZone + `')`

// RebuildDailyHistoryDetails publishes tenant model cells and exact person
// counts for one closed day. The caller holds the daily-state row lock.
func RebuildDailyHistoryDetails(ctx context.Context, tx *sql.Tx, day string) error {
	if err := rebuildPersonRankPacks(ctx, tx, day); err != nil {
		return err
	}
	if err := rebuildDailyHistoryCells(ctx, tx, day, historyTenantModel); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO `+historyDetailDaysTable+` (day) VALUES ($1::date)`, day)
	return err
}

func rebuildPersonRankPacks(ctx context.Context, tx *sql.Tx, day string) error {
	if _, err := tx.ExecContext(ctx, `WITH people AS (
		SELECT DISTINCT tenant_id, `+projectedPersonKey+` AS person `+personRankFacts+`
	)
	INSERT INTO public.request_metrics_person_rank_codes (tenant_id, person)
	SELECT tenant_id, person FROM people p WHERE NOT EXISTS (
		SELECT 1 FROM public.request_metrics_person_rank_codes c
		WHERE (c.tenant_id,c.person)=(p.tenant_id,p.person)
	) ORDER BY tenant_id, person ON CONFLICT (tenant_id, person) DO NOTHING`, day); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM public.request_metrics_person_rank_packs WHERE report_date=$1::date`, day); err != nil {
		return err
	}
	// Read codes in the next statement to include a code inserted by a
	// concurrent publisher that won an ON CONFLICT race for another day.
	_, err := tx.ExecContext(ctx, `WITH activity AS (
		SELECT tenant_id, `+projectedPersonKey+` AS person, count(*) AS requests `+personRankFacts+` GROUP BY 1,2
	)
	INSERT INTO public.request_metrics_person_rank_packs (report_date, tenant_id, counts)
	SELECT $1::date, a.tenant_id, CASE WHEN count(*)=count(c.code)
		THEN string_agg(int8send(c.code)||int8send(a.requests), ''::bytea ORDER BY c.code) END
	FROM activity a LEFT JOIN public.request_metrics_person_rank_codes c USING (tenant_id, person)
	GROUP BY a.tenant_id`, day)
	return err
}

type rankedPerson struct {
	tenant   string
	person   string
	requests int64
}

// readPackedPersonKeys uses one consistent snapshot. An unpublished day sends
// the caller to the exact indexed projection for the entire ranking window.
func readPackedPersonKeys(ctx context.Context, q Querier, coverage ProjectionCoverage, number int32) (personPageKeys, bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, historyDetailDaysTable).Scan(&exists); err != nil || !exists {
		return personPageKeys{}, false, err
	}
	var complete bool
	if err := q.QueryRow(ctx, `SELECT count(*)=(`+historyLastDay+`-`+historyFirstDay+`)
		FROM `+historyDetailDaysTable+` WHERE `+historyDayBounds, coverage.From, coverage.Through).Scan(&complete); err != nil || !complete {
		return personPageKeys{}, false, err
	}
	counts, err := readPersonRankCounts(ctx, q, coverage)
	if err != nil {
		return personPageKeys{}, true, err
	}
	keys, err := rankPersonKeys(ctx, q, counts, number)
	return keys, true, err
}

func readPersonRankCounts(ctx context.Context, q Querier, coverage ProjectionCoverage) (map[int64]int64, error) {
	rows, err := q.Query(ctx, `SELECT counts FROM public.request_metrics_person_rank_packs
		WHERE report_date >= `+historyFirstDay+` AND report_date < `+historyLastDay, coverage.From, coverage.Through)
	if err != nil {
		return nil, fmt.Errorf("read daily person rank counts: %w", err)
	}
	defer rows.Close()
	counts := make(map[int64]int64)
	for rows.Next() {
		// Consume the packet before Next invalidates the driver's buffer.
		var packed pgtype.DriverBytes
		if err := rows.Scan(&packed); err != nil {
			return nil, err
		}
		if len(packed)%16 != 0 {
			return nil, fmt.Errorf("invalid packed person rank length %d", len(packed))
		}
		for len(packed) > 0 {
			code := int64(binary.BigEndian.Uint64(packed))
			requests := int64(binary.BigEndian.Uint64(packed[8:]))
			if code <= 0 || requests <= 0 || counts[code] > math.MaxInt64-requests {
				return nil, fmt.Errorf("invalid packed person ranking for code %d", code)
			}
			counts[code] += requests
			packed = packed[16:]
		}
	}
	return counts, rows.Err()
}

func rankPersonKeys(ctx context.Context, q Querier, counts map[int64]int64, number int32) (personPageKeys, error) {
	total, size := len(counts), projectedPersonPageSize
	keys := personPageKeys{page: PersonPage{Number: 1, Size: size, Total: int64(total)}}
	if total == 0 {
		return keys, nil
	}
	page := min(int(number), 1+(total-1)/size)
	keys.page.Number = int32(page)
	offset := (page - 1) * size
	end := min(offset+size, total)
	ordered := make([]int64, 0, total)
	for _, requests := range counts {
		ordered = append(ordered, requests)
	}
	slices.Sort(ordered)
	// Resolve identities only for counts intersecting this page. Include
	// the whole tie at either edge before ordering by tenant and person.
	low, high := ordered[total-end], ordered[total-offset-1]
	firstInPage := offset - (total - sort.Search(total, func(i int) bool { return ordered[i] > high }))
	codes := make([]int64, 0, end-offset)
	for code, requests := range counts {
		if requests >= low && requests <= high {
			codes = append(codes, code)
		}
	}
	rows, err := q.Query(ctx, `SELECT code, tenant_id, person FROM public.request_metrics_person_rank_codes WHERE code=ANY($1::bigint[])`, codes)
	if err != nil {
		return personPageKeys{}, fmt.Errorf("resolve ranked people: %w", err)
	}
	defer rows.Close()
	people := make([]rankedPerson, 0, len(codes))
	for rows.Next() {
		var code int64
		var person rankedPerson
		if err := rows.Scan(&code, &person.tenant, &person.person); err != nil {
			return personPageKeys{}, err
		}
		person.requests = counts[code]
		people = append(people, person)
	}
	if err := rows.Err(); err != nil {
		return personPageKeys{}, err
	}
	if len(people) != len(codes) {
		return personPageKeys{}, fmt.Errorf("ranked people are missing from the dictionary")
	}
	slices.SortFunc(people, func(a, b rankedPerson) int {
		if c := cmp.Compare(b.requests, a.requests); c != 0 {
			return c
		}
		if c := cmp.Compare(a.tenant, b.tenant); c != 0 {
			return c
		}
		return cmp.Compare(a.person, b.person)
	})
	for _, person := range people[firstInPage : firstInPage+end-offset] {
		keys.tenants = append(keys.tenants, person.tenant)
		keys.people = append(keys.people, person.person)
	}
	return keys, nil
}
