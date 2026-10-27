package maas

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MonthlyBill is one tenant period settlement row.
type MonthlyBill struct {
	ID            int64      `json:"id"`
	TenantID      string     `json:"tenant_id"`
	PeriodYM      string     `json:"period_ym"`
	PeriodStart   time.Time  `json:"period_start"`
	PeriodEnd     time.Time  `json:"period_end"`
	TotalCredits  int64      `json:"total_credits"`
	TotalRequests int64      `json:"total_requests"`
	AmountCents   int64      `json:"amount_cents"`
	Currency      string     `json:"currency"`
	Status        string     `json:"status"`
	IssuedAt      *time.Time `json:"issued_at,omitempty"`
	Note          string     `json:"note"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Created       bool       `json:"created,omitempty"`
}

// PeriodBounds returns [start, end) for a calendar month in loc (default UTC).
func PeriodBounds(periodYM string, loc *time.Location) (start, end time.Time, err error) {
	if loc == nil {
		loc = time.UTC
	}
	periodYM = strings.TrimSpace(periodYM)
	t, err := time.ParseInLocation("2006-01", periodYM, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid period_ym %q: want YYYY-MM", periodYM)
	}
	start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	end = start.AddDate(0, 1, 0)
	return start, end, nil
}

// PreviousPeriodYM returns the calendar month before now as YYYY-MM.
func PreviousPeriodYM(now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	n := now.In(loc)
	prev := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -1, 0)
	return prev.Format("2006-01")
}

// AmountCentsFromCredits converts credit total using cents_per_credit.
func AmountCentsFromCredits(totalCredits int64, centsPerCredit float64) int64 {
	if totalCredits <= 0 || centsPerCredit <= 0 {
		return 0
	}
	return int64(math.Round(float64(totalCredits) * centsPerCredit))
}

type periodConsumption struct {
	Credits  int64
	Requests int64
}

func (s *Service) sumPeriodConsumption(ctx context.Context, tenantID string, start, end time.Time) (periodConsumption, error) {
	var out periodConsumption
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint,
		       COALESCE(SUM(request_count), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= $2
		   AND bucket_start < $3
	`, tenantID, start, end).Scan(&out.Credits, &out.Requests)
	return out, err
}

// IssueMonthlyBill upserts one tenant bill for periodYM (idempotent).
func (s *Service) IssueMonthlyBill(ctx context.Context, tenantID, periodYM string) (MonthlyBill, error) {
	if !s.Enabled() {
		return MonthlyBill{}, errors.New("maas service disabled")
	}
	if tenantID == "" || tenantID == "default" {
		return MonthlyBill{}, fmt.Errorf("tenant_id required")
	}
	start, end, err := PeriodBounds(periodYM, time.UTC)
	if err != nil {
		return MonthlyBill{}, err
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return MonthlyBill{}, err
	}
	cons, err := s.sumPeriodConsumption(ctx, tenantID, start, end)
	if err != nil {
		return MonthlyBill{}, fmt.Errorf("sum period consumption: %w", err)
	}
	amount := AmountCentsFromCredits(cons.Credits, settings.CentsPerCredit)
	now := time.Now().UTC()

	var bill MonthlyBill
	var created bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO maas_monthly_bills (
			tenant_id, period_ym, period_start, period_end,
			total_credits, total_requests, amount_cents, currency,
			status, issued_at, note, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			'issued', $9, '', $9, $9
		)
		ON CONFLICT (tenant_id, period_ym) DO UPDATE
		   SET total_credits  = CASE
		           WHEN maas_monthly_bills.status IN ('paid', 'void') THEN maas_monthly_bills.total_credits
		           ELSE EXCLUDED.total_credits END,
		       total_requests = CASE
		           WHEN maas_monthly_bills.status IN ('paid', 'void') THEN maas_monthly_bills.total_requests
		           ELSE EXCLUDED.total_requests END,
		       amount_cents   = CASE
		           WHEN maas_monthly_bills.status IN ('paid', 'void') THEN maas_monthly_bills.amount_cents
		           ELSE EXCLUDED.amount_cents END,
		       currency       = CASE
		           WHEN maas_monthly_bills.status IN ('paid', 'void') THEN maas_monthly_bills.currency
		           ELSE EXCLUDED.currency END,
		       status         = CASE
		           WHEN maas_monthly_bills.status IN ('paid', 'void') THEN maas_monthly_bills.status
		           ELSE 'issued' END,
		       issued_at      = COALESCE(maas_monthly_bills.issued_at, EXCLUDED.issued_at),
		       updated_at     = CASE
		           WHEN maas_monthly_bills.status IN ('paid', 'void') THEN maas_monthly_bills.updated_at
		           ELSE EXCLUDED.updated_at END
		RETURNING id, tenant_id, period_ym, period_start, period_end,
		          total_credits, total_requests, amount_cents, currency,
		          status, issued_at, note, created_at, updated_at,
		          (xmax = 0) AS created
	`, tenantID, periodYM, start, end,
		cons.Credits, cons.Requests, amount, settings.CurrencyDisplay, now,
	).Scan(
		&bill.ID, &bill.TenantID, &bill.PeriodYM, &bill.PeriodStart, &bill.PeriodEnd,
		&bill.TotalCredits, &bill.TotalRequests, &bill.AmountCents, &bill.Currency,
		&bill.Status, &bill.IssuedAt, &bill.Note, &bill.CreatedAt, &bill.UpdatedAt,
		&created,
	)
	if err != nil {
		return MonthlyBill{}, err
	}
	bill.Created = created
	return bill, nil
}

// IssuePreviousMonthForAllTenants issues previous calendar month for tenants with buckets.
func (s *Service) IssuePreviousMonthForAllTenants(ctx context.Context, now time.Time) (issued, skipped int, err error) {
	if !s.Enabled() {
		return 0, 0, nil
	}
	periodYM := PreviousPeriodYM(now, time.UTC)
	start, end, err := PeriodBounds(periodYM, time.UTC)
	if err != nil {
		return 0, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT tenant_id
		  FROM maas_credit_consumption_buckets
		 WHERE bucket_start >= $1 AND bucket_start < $2
		   AND tenant_id NOT IN ('', 'default')
	`, start, end)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var tid string
		if err := rows.Scan(&tid); err != nil {
			return issued, skipped, err
		}
		tenants = append(tenants, tid)
	}
	if err := rows.Err(); err != nil {
		return issued, skipped, err
	}
	for _, tid := range tenants {
		bill, err := s.IssueMonthlyBill(ctx, tid, periodYM)
		if err != nil {
			return issued, skipped, fmt.Errorf("issue %s/%s: %w", tid, periodYM, err)
		}
		if bill.Created {
			issued++
		} else {
			skipped++
		}
	}
	return issued, skipped, nil
}

// ListMonthlyBills returns bills for a tenant (newest first).
func (s *Service) ListMonthlyBills(ctx context.Context, tenantID string, limit int) ([]MonthlyBill, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}
	if limit <= 0 || limit > 120 {
		limit = 24
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, period_ym, period_start, period_end,
		       total_credits, total_requests, amount_cents, currency,
		       status, issued_at, note, created_at, updated_at
		  FROM maas_monthly_bills
		 WHERE tenant_id = $1
		 ORDER BY period_ym DESC
		 LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonthlyBill
	for rows.Next() {
		var b MonthlyBill
		if err := rows.Scan(
			&b.ID, &b.TenantID, &b.PeriodYM, &b.PeriodStart, &b.PeriodEnd,
			&b.TotalCredits, &b.TotalRequests, &b.AmountCents, &b.Currency,
			&b.Status, &b.IssuedAt, &b.Note, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return jsonSlice(out), rows.Err()
}

// GetMonthlyBill loads one bill by id (tenant-scoped).
func (s *Service) GetMonthlyBill(ctx context.Context, tenantID string, billID int64) (MonthlyBill, error) {
	var b MonthlyBill
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, period_ym, period_start, period_end,
		       total_credits, total_requests, amount_cents, currency,
		       status, issued_at, note, created_at, updated_at
		  FROM maas_monthly_bills
		 WHERE id = $1 AND tenant_id = $2
	`, billID, tenantID).Scan(
		&b.ID, &b.TenantID, &b.PeriodYM, &b.PeriodStart, &b.PeriodEnd,
		&b.TotalCredits, &b.TotalRequests, &b.AmountCents, &b.Currency,
		&b.Status, &b.IssuedAt, &b.Note, &b.CreatedAt, &b.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MonthlyBill{}, fmt.Errorf("bill not found")
	}
	return b, err
}

// FormatBillsCSV renders a CSV export (header + rows).
func FormatBillsCSV(bills []MonthlyBill) string {
	var b strings.Builder
	b.WriteString("period_ym,total_credits,total_requests,amount_cents,currency,status,issued_at\n")
	for _, bill := range bills {
		issued := ""
		if bill.IssuedAt != nil {
			issued = bill.IssuedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "%s,%d,%d,%d,%s,%s,%s\n",
			bill.PeriodYM, bill.TotalCredits, bill.TotalRequests,
			bill.AmountCents, bill.Currency, bill.Status, issued)
	}
	return b.String()
}
