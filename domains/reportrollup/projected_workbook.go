package reportrollup

import (
	"context"
	"fmt"
	"time"
)

// BuildProjectedWorkbookReport reads precisely the dimensions included by
// the workbook. Coverage, aggregates, and details use the caller's snapshot.
func BuildProjectedWorkbookReport(ctx context.Context, q Querier, start, end time.Time, view View, filter GrainFilter,
	names Names, daily bool, coverage ProjectionCoverage) (*GrainReport, error) {
	published := false
	if daily && eligibleDailyHistory(filter, coverage, ProjectedSummaryTotals) {
		var err error
		published, err = publishedWorkbookDays(ctx, q, coverage, projectedBusinessOnly(view, filter))
		if err != nil {
			return nil, err
		}
	}
	report, err := buildGrainReport(ctx, q, start, end, view, filter, names, daily && !published, &coverage, projectedWorkbook)
	if err != nil || !published {
		return report, err
	}
	if err := loadWorkbookDays(ctx, q, report, coverage, names, projectedBusinessOnly(view, filter)); err != nil {
		return nil, err
	}
	return report, nil
}

func publishedWorkbookDays(ctx context.Context, q Querier, coverage ProjectionCoverage, businessOnly bool) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, historyPageDaysTable).Scan(&exists); err != nil || !exists {
		return false, err
	}
	var complete bool
	// A day without published cells can still be covered if the same
	// snapshot verifies there are no matching facts on that day.
	err := q.QueryRow(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM generate_series(`+historyFirstDay+`::timestamp, (`+historyLastDay+`-1)::timestamp, interval '1 day') d(day)
		WHERE NOT EXISTS (SELECT 1 FROM `+historyPageDaysTable+` h WHERE h.day=d.day::date)
		  AND EXISTS (SELECT 1 FROM public.request_metrics_projection p
			WHERE p.source_fact_id IS NOT NULL AND (NOT $3::boolean OR p.traffic_class='business')
			  AND p.occurred_at >= (d.day AT TIME ZONE '`+dailyHistoryZone+`')
			  AND p.occurred_at < ((d.day+interval '1 day') AT TIME ZONE '`+dailyHistoryZone+`'))
	)`, coverage.From, coverage.Through, businessOnly).Scan(&complete)
	return complete, err
}

func loadWorkbookDays(ctx context.Context, q Querier, report *GrainReport, coverage ProjectionCoverage, names Names, businessOnly bool) error {
	rows, err := q.Query(ctx, `SELECT report_date, dimension, group_key, `+historyMeasures+`, errors, tenant_id, latency_ranks
		FROM `+historyPageCellsTable+` JOIN `+historyPageDaysTable+` h ON h.day=report_date
		WHERE report_date >= `+historyFirstDay+` AND report_date < `+historyLastDay+` AND business_only=$3
		ORDER BY report_date, dimension, tenant_id, group_key`, coverage.From, coverage.Through, businessOnly)
	if err != nil {
		return fmt.Errorf("read workbook details: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tenant string
		var ranks []float64
		cell, err := scanDailyHistoryCell(rows, &tenant, &ranks)
		if err != nil {
			return err
		}
		acc, err := cell.publishedAccumulator(ranks)
		if err != nil {
			return err
		}
		date := cell.row.date.Format(time.DateOnly)
		totals := acc.finalize()
		detail := DailyGroupRow{Date: date, Key: cell.key, Totals: totals, ErrorBreakdown: acc.breakdown(), QualityScore: ProviderQualityScore(totals)}
		switch GrainGroup(cell.dimension) {
		case GrainGroupProvider:
			detail.Name = dimDisplayName(names.Providers, detail.Key)
			report.DailyProviders = append(report.DailyProviders, detail)
		case GrainGroupCredential:
			detail.Name = dimDisplayName(names.Credentials, detail.Key)
			report.DailyCredentials = append(report.DailyCredentials, detail)
		case GrainGroupTenant:
			detail.Name = detail.Key
			report.DailyTenants = append(report.DailyTenants, detail)
		case GrainGroupPerson:
			detail.Key = personKey(tenant, detail.Key)
			detail.Name = detail.Key
			report.DailyPersons = append(report.DailyPersons, detail)
		case GrainGroupAPIKey:
			detail.Name = dimDisplayName(names.APIKeys, detail.Key)
			report.DailyAPIKeys = append(report.DailyAPIKeys, detail)
		case GrainGroupModel:
			report.DailyModels = append(report.DailyModels, DailyModelRow{Date: date, RawModelName: detail.Key,
				Totals: detail.Totals, ErrorBreakdown: detail.ErrorBreakdown, QualityScore: detail.QualityScore})
		default:
			return fmt.Errorf("unknown workbook detail dimension %q", cell.dimension)
		}
	}
	return rows.Err()
}
