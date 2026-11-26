package maas

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarkBillPaid 测试标记账单为已付款
func TestMarkBillPaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 创建测试账单
	billID := createTestBill(t, pool, "test-tenant-1", "2026-11", "issued")

	// 执行：标记为已付款
	payment := BillPaymentInfo{
		PaymentDate:        time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
		PaymentMethod:      "wire_transfer",
		PaymentVoucher:     "TX123456789",
		PaymentAmountCents: 50000,
		Notes:              "Test payment",
		ActionBy:           "admin@test.com",
	}

	err := svc.MarkBillPaid(ctx, billID, payment)

	// 验证
	require.NoError(t, err)

	// 查询账单确认状态已更新
	var status string
	var paymentDate time.Time
	var paymentMethod, paymentVoucher string
	var paymentAmount int64

	err = pool.QueryRow(ctx, `
		SELECT status, payment_date, payment_method, payment_voucher, payment_amount_cents
		FROM maas_monthly_bills
		WHERE id = $1
	`, billID).Scan(&status, &paymentDate, &paymentMethod, &paymentVoucher, &paymentAmount)

	require.NoError(t, err)
	assert.Equal(t, "paid", status)
	assert.Equal(t, payment.PaymentDate.Unix(), paymentDate.Unix())
	assert.Equal(t, payment.PaymentMethod, paymentMethod)
	assert.Equal(t, payment.PaymentVoucher, paymentVoucher)
	assert.Equal(t, payment.PaymentAmountCents, paymentAmount)

	// 验证审计日志已创建
	var auditCount int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM maas_bill_status_audit
		WHERE bill_id = $1 AND action = 'mark_paid'
	`, billID).Scan(&auditCount)
	require.NoError(t, err)
	assert.Greater(t, auditCount, 0, "audit log should be created")
}

// TestMarkBillPaid_AlreadyPaid 测试重复标记已付款
func TestMarkBillPaid_AlreadyPaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 创建已付款的账单
	billID := createTestBill(t, pool, "test-tenant-2", "2026-11", "paid")

	// 执行：尝试再次标记为已付款
	payment := BillPaymentInfo{
		PaymentDate:        time.Now(),
		PaymentMethod:      "alipay",
		PaymentVoucher:     "TX987654321",
		PaymentAmountCents: 50000,
		ActionBy:           "admin@test.com",
	}

	err := svc.MarkBillPaid(ctx, billID, payment)

	// 验证：应该返回错误
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already paid")
}

// TestVoidBill 测试作废账单
func TestVoidBill(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 创建测试账单
	billID := createTestBill(t, pool, "test-tenant-3", "2026-11", "issued")

	// 执行：作废账单
	voidInfo := BillVoidInfo{
		Reason:   "duplicate",
		Notes:    "Duplicate billing, void this one",
		ActionBy: "admin@test.com",
	}

	err := svc.VoidBill(ctx, billID, voidInfo)

	// 验证
	require.NoError(t, err)

	// 查询账单确认状态已更新
	var status, voidReason string
	var voidedAt time.Time

	err = pool.QueryRow(ctx, `
		SELECT status, voided_at, void_reason
		FROM maas_monthly_bills
		WHERE id = $1
	`, billID).Scan(&status, &voidedAt, &voidReason)

	require.NoError(t, err)
	assert.Equal(t, "void", status)
	assert.False(t, voidedAt.IsZero())
	assert.Equal(t, voidInfo.Reason, voidReason)

	// 验证审计日志已创建
	var auditCount int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM maas_bill_status_audit
		WHERE bill_id = $1 AND action = 'void'
	`, billID).Scan(&auditCount)
	require.NoError(t, err)
	assert.Greater(t, auditCount, 0, "audit log should be created")
}

// TestVoidBill_CannotVoidPaid 测试无法作废已付款账单
func TestVoidBill_CannotVoidPaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 创建已付款的账单
	billID := createTestBill(t, pool, "test-tenant-4", "2026-11", "paid")

	// 执行：尝试作废已付款账单
	voidInfo := BillVoidInfo{
		Reason:   "error",
		Notes:    "Should not be able to void",
		ActionBy: "admin@test.com",
	}

	err := svc.VoidBill(ctx, billID, voidInfo)

	// 验证：应该返回错误
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be voided")
}

// TestBatchMarkBillsPaid 测试批量标记付款
func TestBatchMarkBillsPaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 创建3个测试账单
	billID1 := createTestBill(t, pool, "test-tenant-5", "2026-10", "issued")
	billID2 := createTestBill(t, pool, "test-tenant-5", "2026-11", "issued")
	billID3 := createTestBill(t, pool, "test-tenant-5", "2026-12", "issued")

	// 执行：批量标记
	payments := []struct {
		BillID             int64
		PaymentDate        time.Time
		PaymentVoucher     string
		PaymentAmountCents int64
	}{
		{billID1, time.Now(), "BATCH001", 50000},
		{billID2, time.Now(), "BATCH002", 60000},
		{billID3, time.Now(), "BATCH003", 70000},
	}

	succeeded, failed, err := svc.BatchMarkBillsPaid(ctx, payments, "wire_transfer", "admin@test.com")

	// 验证
	require.NoError(t, err)
	assert.Equal(t, 3, succeeded)
	assert.Equal(t, 0, failed)

	// 验证所有账单状态都已更新
	var paidCount int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM maas_monthly_bills
		WHERE id IN ($1, $2, $3) AND status = 'paid'
	`, billID1, billID2, billID3).Scan(&paidCount)
	require.NoError(t, err)
	assert.Equal(t, 3, paidCount)
}

// TestListBills 测试账单列表查询
func TestListBills(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 创建多个测试账单
	tenantID := "test-tenant-6"
	createTestBill(t, pool, tenantID, "2026-10", "issued")
	createTestBill(t, pool, tenantID, "2026-11", "paid")
	createTestBill(t, pool, tenantID, "2026-12", "void")

	// 测试1：查询所有账单
	bills, total, err := svc.ListBills(ctx, ListBillsFilter{
		TenantID: tenantID,
		Limit:    10,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, bills, 3)

	// 测试2：只查询issued状态
	bills, total, err = svc.ListBills(ctx, ListBillsFilter{
		TenantID: tenantID,
		Status:   "issued",
		Limit:    10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, bills, 1)
	assert.Equal(t, "issued", bills[0].Status)

	// 测试3：分页查询
	bills, total, err = svc.ListBills(ctx, ListBillsFilter{
		TenantID: tenantID,
		Limit:    1,
		Offset:   0,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, bills, 1)
}

// TestGetBillDetails 测试获取账单详情
func TestGetBillDetails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	// 创建测试账单
	billID := createTestBill(t, pool, "test-tenant-7", "2026-11", "issued")

	// 标记为已付款（产生审计日志）
	_ = svc.MarkBillPaid(ctx, billID, BillPaymentInfo{
		PaymentDate:        time.Now(),
		PaymentMethod:      "alipay",
		PaymentVoucher:     "TX111222333",
		PaymentAmountCents: 50000,
		ActionBy:           "admin@test.com",
	})

	// 获取详情
	bill, auditLogs, err := svc.GetBillDetails(ctx, billID)

	// 验证
	require.NoError(t, err)
	require.NotNil(t, bill)
	assert.Equal(t, billID, bill.ID)
	assert.Equal(t, "paid", bill.Status)

	// 验证审计日志
	assert.Greater(t, len(auditLogs), 0, "should have audit logs")
	assert.Equal(t, "mark_paid", auditLogs[0].Action)
}

// TestListBills_Overdue 测试逾期账单查询
func TestListBills_Overdue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	tenantID := "test-tenant-8"

	// 创建逾期账单（period_end在35天前）
	overdueBillID := createOverdueBill(t, pool, tenantID, "2026-10", 35)

	// 创建正常账单
	createTestBill(t, pool, tenantID, "2026-11", "issued")

	// 查询逾期账单
	bills, total, err := svc.ListBills(ctx, ListBillsFilter{
		TenantID: tenantID,
		Overdue:  true,
		Limit:    10,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, total, "should only return overdue bills")
	assert.Len(t, bills, 1)
	assert.Equal(t, overdueBillID, bills[0].ID)
}

// TestParameterValidation 测试参数验证
func TestParameterValidation(t *testing.T) {
	ctx := context.Background()
	pool := setupTestDB(t)
	svc := &Service{pool: pool}

	t.Run("MarkBillPaid_MissingPaymentDate", func(t *testing.T) {
		err := svc.MarkBillPaid(ctx, 1, BillPaymentInfo{
			PaymentMethod:      "alipay",
			PaymentAmountCents: 50000,
			ActionBy:           "admin",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "payment_date required")
	})

	t.Run("MarkBillPaid_InvalidAmount", func(t *testing.T) {
		err := svc.MarkBillPaid(ctx, 1, BillPaymentInfo{
			PaymentDate:        time.Now(),
			PaymentMethod:      "alipay",
			PaymentAmountCents: -100,
			ActionBy:           "admin",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must be positive")
	})

	t.Run("VoidBill_MissingReason", func(t *testing.T) {
		err := svc.VoidBill(ctx, 1, BillVoidInfo{
			ActionBy: "admin",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "void_reason required")
	})
}

// ─────────────────────────────────────────────────────────
// Test Helpers
// ─────────────────────────────────────────────────────────

// createTestBill 创建测试账单
func createTestBill(t *testing.T, pool *pgxpool.Pool, tenantID, periodYM, status string) int64 {
	t.Helper()

	ctx := context.Background()

	// 解析period
	periodStart, _ := time.Parse("2006-01", periodYM)
	periodEnd := periodStart.AddDate(0, 1, 0)

	var billID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO maas_monthly_bills 
		(tenant_id, period_ym, period_start, period_end, total_credits, 
		 total_requests, amount_cents, currency, status, issued_at)
		VALUES ($1, $2, $3, $4, 100000, 500, 50000, 'CNY', $5, NOW())
		RETURNING id
	`, tenantID, periodYM, periodStart, periodEnd, status).Scan(&billID)

	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM maas_bill_status_audit WHERE bill_id = $1
		`, billID)
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM maas_monthly_bills WHERE id = $1
		`, billID)
	})

	return billID
}

// createOverdueBill 创建逾期账单
func createOverdueBill(t *testing.T, pool *pgxpool.Pool, tenantID, periodYM string, daysAgo int) int64 {
	t.Helper()

	ctx := context.Background()

	// period_end设置为daysAgo天前
	periodEnd := time.Now().AddDate(0, 0, -daysAgo)
	periodStart := periodEnd.AddDate(0, -1, 0)

	var billID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO maas_monthly_bills 
		(tenant_id, period_ym, period_start, period_end, total_credits, 
		 total_requests, amount_cents, currency, status, issued_at)
		VALUES ($1, $2, $3, $4, 100000, 500, 50000, 'CNY', 'issued', $4)
		RETURNING id
	`, tenantID, periodYM, periodStart, periodEnd).Scan(&billID)

	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM maas_monthly_bills WHERE id = $1
		`, billID)
	})

	return billID
}
