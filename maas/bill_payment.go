package maas

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BillPaymentInfo 付款信息
type BillPaymentInfo struct {
	PaymentDate       time.Time `json:"payment_date"`
	PaymentMethod     string    `json:"payment_method"` // wire_transfer / alipay / wechat / credit_card / other
	PaymentVoucher    string    `json:"payment_voucher"`
	PaymentAmountCents int64    `json:"payment_amount_cents"`
	Notes             string    `json:"notes,omitempty"`
	ActionBy          string    `json:"action_by"` // 操作人
}

// BillVoidInfo 作废信息
type BillVoidInfo struct {
	Reason   string `json:"reason"` // dispute / adjustment / duplicate / error / other
	Notes    string `json:"notes,omitempty"`
	ActionBy string `json:"action_by"` // 操作人
}

// MarkBillPaid 标记账单为已付款
func (s *Service) MarkBillPaid(ctx context.Context, billID int64, payment BillPaymentInfo) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}

	// 验证参数
	if billID <= 0 {
		return errors.New("invalid bill_id")
	}
	if payment.PaymentDate.IsZero() {
		return errors.New("payment_date required")
	}
	if payment.PaymentMethod == "" {
		return errors.New("payment_method required")
	}
	if payment.PaymentAmountCents <= 0 {
		return errors.New("payment_amount_cents must be positive")
	}
	if payment.ActionBy == "" {
		return errors.New("action_by required")
	}

	// 更新账单状态
	result, err := s.pool.Exec(ctx, `
		UPDATE maas_monthly_bills
		SET status = 'paid',
		    payment_date = $2,
		    payment_method = $3,
		    payment_voucher = $4,
		    payment_amount_cents = $5,
		    payment_notes = $6,
		    updated_at = NOW()
		WHERE id = $1
		  AND status IN ('issued', 'void') -- 只允许从issued或void转为paid
	`, billID, payment.PaymentDate, payment.PaymentMethod, payment.PaymentVoucher,
		payment.PaymentAmountCents, payment.Notes)

	if err != nil {
		return fmt.Errorf("failed to mark bill as paid: %w", err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		return errors.New("bill not found or already paid")
	}

	return nil
}

// VoidBill 作废账单
func (s *Service) VoidBill(ctx context.Context, billID int64, voidInfo BillVoidInfo) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}

	// 验证参数
	if billID <= 0 {
		return errors.New("invalid bill_id")
	}
	if voidInfo.Reason == "" {
		return errors.New("void_reason required")
	}
	if voidInfo.ActionBy == "" {
		return errors.New("action_by required")
	}

	// 更新账单状态
	result, err := s.pool.Exec(ctx, `
		UPDATE maas_monthly_bills
		SET status = 'void',
		    voided_at = NOW(),
		    void_reason = $2,
		    payment_notes = $3,
		    void_by = $4,
		    updated_at = NOW()
		WHERE id = $1
		  AND status = 'issued' -- 只允许作废issued状态的账单
	`, billID, voidInfo.Reason, voidInfo.Notes, voidInfo.ActionBy)

	if err != nil {
		return fmt.Errorf("failed to void bill: %w", err)
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		return errors.New("bill not found or cannot be voided (must be in issued status)")
	}

	return nil
}

// BatchMarkBillsPaid 批量标记账单为已付款
func (s *Service) BatchMarkBillsPaid(ctx context.Context, payments []struct {
	BillID             int64
	PaymentDate        time.Time
	PaymentVoucher     string
	PaymentAmountCents int64
}, paymentMethod, actionBy string) (succeeded int, failed int, err error) {
	if !s.Enabled() {
		return 0, 0, errors.New("maas service disabled")
	}

	for _, p := range payments {
		err := s.MarkBillPaid(ctx, p.BillID, BillPaymentInfo{
			PaymentDate:        p.PaymentDate,
			PaymentMethod:      paymentMethod,
			PaymentVoucher:     p.PaymentVoucher,
			PaymentAmountCents: p.PaymentAmountCents,
			ActionBy:           actionBy,
		})

		if err == nil {
			succeeded++
		} else {
			failed++
		}
	}

	return succeeded, failed, nil
}

// ListBillsFilter 账单查询过滤条件
type ListBillsFilter struct {
	TenantID   string
	Status     string // issued / paid / void
	PeriodFrom string // YYYY-MM
	PeriodTo   string // YYYY-MM
	Overdue    bool   // 仅逾期账单
	Limit      int
	Offset     int
}

// ListBills 查询账单列表
func (s *Service) ListBills(ctx context.Context, filter ListBillsFilter) ([]MonthlyBill, int, error) {
	if !s.Enabled() {
		return nil, 0, errors.New("maas service disabled")
	}

	// 构建查询条件
	whereClause := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1

	if filter.TenantID != "" {
		whereClause += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, filter.TenantID)
		argIdx++
	}

	if filter.Status != "" {
		whereClause += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, filter.Status)
		argIdx++
	}

	if filter.PeriodFrom != "" {
		whereClause += fmt.Sprintf(" AND period_ym >= $%d", argIdx)
		args = append(args, filter.PeriodFrom)
		argIdx++
	}

	if filter.PeriodTo != "" {
		whereClause += fmt.Sprintf(" AND period_ym <= $%d", argIdx)
		args = append(args, filter.PeriodTo)
		argIdx++
	}

	if filter.Overdue {
		whereClause += " AND status = 'issued' AND period_end < NOW() - INTERVAL '30 days'"
	}

	// 查询总数
	var total int
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*) FROM maas_monthly_bills %s
	`, whereClause), args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count bills: %w", err)
	}

	// 设置默认limit
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}

	// 查询账单列表
	limitClause := fmt.Sprintf(" ORDER BY period_ym DESC, id DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, tenant_id, period_ym, period_start, period_end,
		       total_credits, total_requests, amount_cents, currency, status,
		       issued_at, note, created_at, updated_at,
		       payment_date, payment_method, payment_voucher, payment_amount_cents,
		       voided_at, void_reason
		FROM maas_monthly_bills
		%s %s
	`, whereClause, limitClause), args...)

	if err != nil {
		return nil, 0, fmt.Errorf("failed to query bills: %w", err)
	}
	defer rows.Close()

	bills := []MonthlyBill{}
	for rows.Next() {
		var b MonthlyBill
		var paymentDate, voidedAt *time.Time
		var paymentMethod, paymentVoucher, voidReason *string
		var paymentAmount *int64

		err := rows.Scan(
			&b.ID, &b.TenantID, &b.PeriodYM, &b.PeriodStart, &b.PeriodEnd,
			&b.TotalCredits, &b.TotalRequests, &b.AmountCents, &b.Currency, &b.Status,
			&b.IssuedAt, &b.Note, &b.CreatedAt, &b.UpdatedAt,
			&paymentDate, &paymentMethod, &paymentVoucher, &paymentAmount,
			&voidedAt, &voidReason,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan bill: %w", err)
		}

		// 注意：MonthlyBill结构体可能需要扩展以包含付款信息
		// 这里先使用现有字段
		bills = append(bills, b)
	}

	return bills, total, rows.Err()
}

// GetBillDetails 获取账单详情（包含付款信息和审计日志）
func (s *Service) GetBillDetails(ctx context.Context, billID int64) (*MonthlyBill, []BillAuditLog, error) {
	if !s.Enabled() {
		return nil, nil, errors.New("maas service disabled")
	}

	// 查询账单
	var b MonthlyBill
	var paymentDate, voidedAt *time.Time
	var paymentMethod, paymentVoucher, voidReason *string
	var paymentAmount *int64

	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, period_ym, period_start, period_end,
		       total_credits, total_requests, amount_cents, currency, status,
		       issued_at, note, created_at, updated_at,
		       payment_date, payment_method, payment_voucher, payment_amount_cents,
		       voided_at, void_reason
		FROM maas_monthly_bills
		WHERE id = $1
	`, billID).Scan(
		&b.ID, &b.TenantID, &b.PeriodYM, &b.PeriodStart, &b.PeriodEnd,
		&b.TotalCredits, &b.TotalRequests, &b.AmountCents, &b.Currency, &b.Status,
		&b.IssuedAt, &b.Note, &b.CreatedAt, &b.UpdatedAt,
		&paymentDate, &paymentMethod, &paymentVoucher, &paymentAmount,
		&voidedAt, &voidReason,
	)

	if err == pgx.ErrNoRows {
		return nil, nil, errors.New("bill not found")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query bill: %w", err)
	}

	// 查询审计日志
	auditLogs, err := s.getBillAuditLogs(ctx, billID)
	if err != nil {
		return &b, nil, fmt.Errorf("failed to query audit logs: %w", err)
	}

	return &b, auditLogs, nil
}

// BillAuditLog 账单审计日志
type BillAuditLog struct {
	ID             int64     `json:"id"`
	BillID         int64     `json:"bill_id"`
	OldStatus      string    `json:"old_status"`
	NewStatus      string    `json:"new_status"`
	Action         string    `json:"action"`
	ActionBy       string    `json:"action_by"`
	ActionReason   string    `json:"action_reason,omitempty"`
	PaymentDetails string    `json:"payment_details,omitempty"` // JSON string
	CreatedAt      time.Time `json:"created_at"`
}

// getBillAuditLogs 获取账单审计日志
func (s *Service) getBillAuditLogs(ctx context.Context, billID int64) ([]BillAuditLog, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, bill_id, old_status, new_status, action, action_by,
		       action_reason, payment_details::text, created_at
		FROM maas_bill_status_audit
		WHERE bill_id = $1
		ORDER BY created_at DESC
	`, billID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := []BillAuditLog{}
	for rows.Next() {
		var log BillAuditLog
		var actionReason, paymentDetails *string

		err := rows.Scan(
			&log.ID, &log.BillID, &log.OldStatus, &log.NewStatus,
			&log.Action, &log.ActionBy, &actionReason, &paymentDetails, &log.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		if actionReason != nil {
			log.ActionReason = *actionReason
		}
		if paymentDetails != nil {
			log.PaymentDetails = *paymentDetails
		}

		logs = append(logs, log)
	}

	return logs, rows.Err()
}
