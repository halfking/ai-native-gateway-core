package maas

import (
	"context"
	"fmt"
	"time"
)

// TripartiteReading is the three-way reconciliation snapshot for one period.
// A = consumption buckets · B = monthly bill · C = credit_ledger consume abs sum.
type TripartiteReading struct {
	TenantID       string `json:"tenant_id"`
	PeriodYM       string `json:"period_ym"`
	BucketCredits  int64  `json:"bucket_credits"`
	BillCredits    int64  `json:"bill_credits"`
	LedgerConsume  int64  `json:"ledger_consume_abs"`
	BucketsMatchBill bool `json:"buckets_match_bill"`
}

// ReconcileTripartite compares buckets vs bill vs ledger consume for a period.
// Ledger consume may lag or diverge when subscription quota covers usage
// (no wallet ledger row) — BucketsMatchBill is the hard gate for出账正确性.
func (s *Service) ReconcileTripartite(ctx context.Context, tenantID, periodYM string) (TripartiteReading, error) {
	if !s.Enabled() {
		return TripartiteReading{}, fmt.Errorf("maas service disabled")
	}
	start, end, err := PeriodBounds(periodYM, time.UTC)
	if err != nil {
		return TripartiteReading{}, err
	}
	cons, err := s.sumPeriodConsumption(ctx, tenantID, start, end)
	if err != nil {
		return TripartiteReading{}, err
	}
	var billCredits int64
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(total_credits, 0)
		  FROM maas_monthly_bills
		 WHERE tenant_id = $1 AND period_ym = $2
	`, tenantID, periodYM).Scan(&billCredits)
	if err != nil {
		billCredits = -1 // missing bill
	}
	var ledgerAbs int64
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(ABS(amount)), 0)::bigint
		  FROM credit_ledger
		 WHERE tenant_id = $1
		   AND entry_type = 'consume'
		   AND created_at >= $2 AND created_at < $3
	`, tenantID, start, end).Scan(&ledgerAbs)

	out := TripartiteReading{
		TenantID:         tenantID,
		PeriodYM:         periodYM,
		BucketCredits:    cons.Credits,
		BillCredits:      billCredits,
		LedgerConsume:    ledgerAbs,
		BucketsMatchBill: billCredits == cons.Credits,
	}
	return out, nil
}

// TripartiteOK is the pure predicate used by tests and gates.
func TripartiteOK(bucket, bill int64) bool {
	return bucket == bill
}
