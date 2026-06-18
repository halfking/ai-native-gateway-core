package maas

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UsageModelRow is per-model credit consumption for a tenant window.
type UsageModelRow struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
}

// UsageTrendRow is daily credit + request totals.
type UsageTrendRow struct {
	Date     string  `json:"date"`
	Requests int64   `json:"requests"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
}

// UsageSummary aggregates tenant MaaS consumption from request_logs.
type UsageSummary struct {
	Days          int               `json:"days"`
	TenantID      string            `json:"tenant_id"`
	TotalRequests int64             `json:"total_requests"`
	TotalCredits  int64             `json:"total_credits"`
	TotalCostUSD  float64           `json:"total_cost_usd,omitempty"`
	ByModel       []UsageModelRow   `json:"by_model"`
	Trend         []UsageTrendRow   `json:"trend"`
}

// ClampUsageDays bounds the days query parameter for usage summary.
func ClampUsageDays(days int) int {
	if days < 1 {
		return 1
	}
	if days > 90 {
		return 90
	}
	return days
}

// ClampUsageLimit bounds the per-model top-N limit.
func ClampUsageLimit(limit int) int {
	if limit < 1 {
		return 10
	}
	if limit > 50 {
		return 50
	}
	return limit
}

// QueryUsageSummary reads credits_charged + request counts for one tenant (tenant-facing; no upstream cost).
func (s *Service) QueryUsageSummary(ctx context.Context, tenantID string, days, limit int) (UsageSummary, error) {
	return s.queryUsageSummary(ctx, tenantID, days, limit, false)
}

// QueryUsageSummaryWithCost includes upstream cost_usd for platform admin views.
func (s *Service) QueryUsageSummaryWithCost(ctx context.Context, tenantID string, days, limit int) (UsageSummary, error) {
	return s.queryUsageSummary(ctx, tenantID, days, limit, true)
}

func (s *Service) queryUsageSummary(ctx context.Context, tenantID string, days, limit int, includeCost bool) (UsageSummary, error) {
	if !s.Enabled() {
		return UsageSummary{}, fmt.Errorf("maas service not enabled")
	}
	if tenantID == "" {
		return UsageSummary{}, fmt.Errorf("tenant_id required")
	}
	days = ClampUsageDays(days)
	limit = ClampUsageLimit(limit)

	out := UsageSummary{
		Days:     days,
		TenantID: tenantID,
		ByModel:  []UsageModelRow{},
		Trend:    []UsageTrendRow{},
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if includeCost {
		if err := s.pool.QueryRow(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(COALESCE(credits_charged, 0)), 0),
			       COALESCE(SUM(COALESCE(cost_usd, 0)), 0)::float8
			FROM request_logs
			WHERE tenant_id = $1
			  AND ts >= now() - ($2 * INTERVAL '1 day')
		`, tenantID, days).Scan(&out.TotalRequests, &out.TotalCredits, &out.TotalCostUSD); err != nil {
			return UsageSummary{}, err
		}
	} else if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(COALESCE(credits_charged, 0)), 0)
		FROM request_logs
		WHERE tenant_id = $1
		  AND ts >= now() - ($2 * INTERVAL '1 day')
	`, tenantID, days).Scan(&out.TotalRequests, &out.TotalCredits); err != nil {
		return UsageSummary{}, err
	}

	modelSQL := `
		SELECT COALESCE(NULLIF(TRIM(outbound_model), ''), NULLIF(TRIM(client_model), ''), 'unknown') AS model,
		       COUNT(*)::bigint,
		       COALESCE(SUM(COALESCE(credits_charged, 0)), 0)::bigint`
	if includeCost {
		modelSQL += `,
		       COALESCE(SUM(COALESCE(cost_usd, 0)), 0)::float8`
	}
	modelSQL += `
		FROM request_logs
		WHERE tenant_id = $1
		  AND ts >= now() - ($2 * INTERVAL '1 day')
		GROUP BY 1
		ORDER BY COUNT(*) DESC, SUM(COALESCE(credits_charged, 0)) DESC
		LIMIT $3`

	rows, err := s.pool.Query(ctx, modelSQL, tenantID, days, limit)
	if err != nil {
		return UsageSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row UsageModelRow
		if includeCost {
			if err := rows.Scan(&row.Model, &row.Requests, &row.Credits, &row.CostUSD); err != nil {
				return UsageSummary{}, err
			}
		} else if err := rows.Scan(&row.Model, &row.Requests, &row.Credits); err != nil {
			return UsageSummary{}, err
		}
		out.ByModel = append(out.ByModel, row)
	}
	if err := rows.Err(); err != nil {
		return UsageSummary{}, err
	}

	trendSQL := `
		SELECT TO_CHAR(DATE(ts AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
		       COUNT(*)::bigint,
		       COALESCE(SUM(COALESCE(credits_charged, 0)), 0)::bigint`
	if includeCost {
		trendSQL += `,
		       COALESCE(SUM(COALESCE(cost_usd, 0)), 0)::float8`
	}
	trendSQL += `
		FROM request_logs
		WHERE tenant_id = $1
		  AND ts >= now() - ($2 * INTERVAL '1 day')
		GROUP BY DATE(ts AT TIME ZONE 'UTC')
		ORDER BY DATE(ts AT TIME ZONE 'UTC')`

	trendRows, err := s.pool.Query(ctx, trendSQL, tenantID, days)
	if err != nil {
		return UsageSummary{}, err
	}
	defer trendRows.Close()
	for trendRows.Next() {
		var row UsageTrendRow
		if includeCost {
			if err := trendRows.Scan(&row.Date, &row.Requests, &row.Credits, &row.CostUSD); err != nil {
				return UsageSummary{}, err
			}
		} else if err := trendRows.Scan(&row.Date, &row.Requests, &row.Credits); err != nil {
			return UsageSummary{}, err
		}
		out.Trend = append(out.Trend, row)
	}
	out.ByModel = jsonSlice(out.ByModel)
	out.Trend = jsonSlice(out.Trend)
	return out, trendRows.Err()
}

// EnabledPool exposes pool for tests that need direct DB setup.
func (s *Service) EnabledPool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}
