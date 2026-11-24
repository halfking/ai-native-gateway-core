package maas

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/bucket/credit"
)

// BackfillCreditConsumptionBuckets fills missing hours with retained debit
// evidence. Existing buckets can contain historical inputs and stay available
// for reconciliation against the ledger.
func (s *Service) BackfillCreditConsumptionBuckets(ctx context.Context, lookbackDays int) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	return credit.BackfillPostedConsumption(ctx, s.pool, lookbackDays)
}

// QueryConsumedCreditsWindow sums the hourly bucket counter for a tenant
// over the last `days` days. Returns 0 when the bucket table is missing or
// empty so callers can degrade gracefully.
//
// Used by admin /api/usage/summary as the fast path for
// total_credits_charged (replaces SELECT SUM(credits_charged) FROM
// usage_ledger … which scans all rows in the window).
func (s *Service) QueryConsumedCreditsWindow(ctx context.Context, tenantID string, days int) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	if tenantID == "" {
		return 0, fmt.Errorf("tenant_id required")
	}
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var credits int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`, tenantID, days).Scan(&credits)
	if err != nil {
		return 0, fmt.Errorf("query consumed credits window: %w", err)
	}
	return credits, nil
}
