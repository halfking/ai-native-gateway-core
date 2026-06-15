package maas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// OrderType is subscribe or topup.
type OrderType string

const (
	OrderSubscribe OrderType = "subscribe"
	OrderTopup     OrderType = "topup"
)

// OrderStatus lifecycle for billing orders.
type OrderStatus string

const (
	OrderPending   OrderStatus = "pending"
	OrderPaid      OrderStatus = "paid"
	OrderCancelled OrderStatus = "cancelled"
	OrderExpired   OrderStatus = "expired"
)

// BillingOrder is a tenant purchase order.
type BillingOrder struct {
	ID             int64          `json:"id"`
	OrderNo        string         `json:"order_no"`
	TenantID       string         `json:"tenant_id"`
	OrderType      OrderType      `json:"order_type"`
	Status         OrderStatus    `json:"status"`
	AmountCents    int            `json:"amount_cents"`
	Credits        int64          `json:"credits"`
	PlanID         *int           `json:"plan_id,omitempty"`
	PackageID      *int           `json:"package_id,omitempty"`
	PaymentChannel PaymentChannel `json:"payment_channel"`
	QRPayload      string         `json:"qr_payload"`
	QRURL          string         `json:"qr_url"`
	PaidAt         *time.Time     `json:"paid_at,omitempty"`
	ExpiresAt      time.Time      `json:"expires_at"`
	Note           string         `json:"note"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	// Enriched for API responses
	PaymentHint string `json:"payment_hint,omitempty"`
	StubMode    bool   `json:"stub_mode,omitempty"`
	PlanName    string `json:"plan_name,omitempty"`
	PackageName string `json:"package_name,omitempty"`
}

// CreateOrderRequest is the tenant-facing order creation payload.
type CreateOrderRequest struct {
	Type           OrderType      `json:"type"`
	PlanID         *int           `json:"plan_id"`
	PackageID      *int           `json:"package_id"`
	PaymentChannel PaymentChannel `json:"payment_channel"`
}

func generateOrderNo() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("MO%s%s", time.Now().Format("20060102150405"), hex.EncodeToString(b)), nil
}

func (s *Service) paymentSettings(ctx context.Context) (PaymentSettings, error) {
	var ps PaymentSettings
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(alipay_account, ''), COALESCE(wechat_mch_id, ''),
		       COALESCE(stub_alipay_qr_url, ''), COALESCE(stub_wechat_qr_url, '')
		FROM maas_settings WHERE id = 1
	`).Scan(&ps.AlipayAccount, &ps.WechatMchID, &ps.StubAlipayQRURL, &ps.StubWechatQRURL)
	return ps, err
}

// CreateOrder creates a pending billing order with placeholder QR.
func (s *Service) CreateOrder(ctx context.Context, tenantID string, req CreateOrderRequest) (BillingOrder, error) {
	if !s.Enabled() {
		return BillingOrder{}, errors.New("maas service disabled")
	}
	if req.Type != OrderSubscribe && req.Type != OrderTopup {
		return BillingOrder{}, fmt.Errorf("invalid order type: %s", req.Type)
	}
	channel := req.PaymentChannel
	if channel == "" {
		channel = PaymentAlipay
	}
	if channel != PaymentAlipay && channel != PaymentWechat {
		return BillingOrder{}, fmt.Errorf("invalid payment channel: %s", channel)
	}

	var amountCents int
	var credits int64
	var planID, packageID *int
	var planName, packageName string

	switch req.Type {
	case OrderSubscribe:
		if req.PlanID == nil || *req.PlanID <= 0 {
			return BillingOrder{}, errors.New("plan_id required for subscribe")
		}
		var name string
		var enabled bool
		err := s.pool.QueryRow(ctx, `
			SELECT name, price_cents, monthly_credits, enabled
			FROM subscription_plans WHERE id = $1
		`, *req.PlanID).Scan(&name, &amountCents, &credits, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return BillingOrder{}, errors.New("plan not found")
		}
		if err != nil {
			return BillingOrder{}, err
		}
		if !enabled {
			return BillingOrder{}, errors.New("plan is not available")
		}
		planID = req.PlanID
		planName = name
	case OrderTopup:
		if req.PackageID == nil || *req.PackageID <= 0 {
			return BillingOrder{}, errors.New("package_id required for topup")
		}
		var name string
		var enabled bool
		err := s.pool.QueryRow(ctx, `
			SELECT name, price_cents, credits_amount, enabled
			FROM topup_packages WHERE id = $1
		`, *req.PackageID).Scan(&name, &amountCents, &credits, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return BillingOrder{}, errors.New("package not found")
		}
		if err != nil {
			return BillingOrder{}, err
		}
		if !enabled {
			return BillingOrder{}, errors.New("package is not available")
		}
		packageID = req.PackageID
		packageName = name
	}

	orderNo, err := generateOrderNo()
	if err != nil {
		return BillingOrder{}, err
	}

	ps, err := s.paymentSettings(ctx)
	if err != nil {
		return BillingOrder{}, err
	}
	provider := StubQRProvider{Settings: ps}
	qr := provider.GenerateQR(orderNo, amountCents, channel)
	expiresAt := time.Now().Add(30 * time.Minute)

	var o BillingOrder
	err = s.pool.QueryRow(ctx, `
		INSERT INTO billing_orders (
			order_no, tenant_id, order_type, status, amount_cents, credits,
			plan_id, package_id, payment_channel, qr_payload, qr_url, expires_at
		) VALUES ($1, $2, $3, 'pending', $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, order_no, tenant_id, order_type, status, amount_cents, credits,
		          plan_id, package_id, payment_channel, qr_payload, qr_url,
		          paid_at, expires_at, note, created_at, updated_at
	`, orderNo, tenantID, req.Type, amountCents, credits,
		planID, packageID, channel, qr.QRPayload, qr.QRURL, expiresAt,
	).Scan(
		&o.ID, &o.OrderNo, &o.TenantID, &o.OrderType, &o.Status, &o.AmountCents, &o.Credits,
		&planID, &packageID, &o.PaymentChannel, &o.QRPayload, &o.QRURL,
		&o.PaidAt, &o.ExpiresAt, &o.Note, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return BillingOrder{}, err
	}
	o.PlanID = planID
	o.PackageID = packageID
	o.PlanName = planName
	o.PackageName = packageName
	o.PaymentHint = qr.Hint
	o.StubMode = qr.StubMode
	return o, nil
}

// GetOrder returns one order by ID, optionally scoped to tenant.
func (s *Service) GetOrder(ctx context.Context, orderID int64, tenantID string) (BillingOrder, error) {
	o, err := s.scanOrder(ctx, `
		SELECT bo.id, bo.order_no, bo.tenant_id, bo.order_type, bo.status,
		       bo.amount_cents, bo.credits, bo.plan_id, bo.package_id,
		       bo.payment_channel, bo.qr_payload, bo.qr_url,
		       bo.paid_at, bo.expires_at, bo.note, bo.created_at, bo.updated_at,
		       COALESCE(sp.name, ''), COALESCE(tp.name, '')
		FROM billing_orders bo
		LEFT JOIN subscription_plans sp ON sp.id = bo.plan_id
		LEFT JOIN topup_packages tp ON tp.id = bo.package_id
		WHERE bo.id = $1
	`, orderID)
	if err != nil {
		return BillingOrder{}, err
	}
	if tenantID != "" && o.TenantID != tenantID {
		return BillingOrder{}, pgx.ErrNoRows
	}
	s.enrichOrderPaymentHint(ctx, &o)
	return o, nil
}

// ListOrders returns orders for a tenant or all orders when tenantID is empty.
func (s *Service) ListOrders(ctx context.Context, tenantID string, limit int) ([]BillingOrder, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var rows pgx.Rows
	var err error
	if tenantID != "" {
		rows, err = s.pool.Query(ctx, `
			SELECT bo.id, bo.order_no, bo.tenant_id, bo.order_type, bo.status,
			       bo.amount_cents, bo.credits, bo.plan_id, bo.package_id,
			       bo.payment_channel, bo.qr_payload, bo.qr_url,
			       bo.paid_at, bo.expires_at, bo.note, bo.created_at, bo.updated_at,
			       COALESCE(sp.name, ''), COALESCE(tp.name, '')
			FROM billing_orders bo
			LEFT JOIN subscription_plans sp ON sp.id = bo.plan_id
			LEFT JOIN topup_packages tp ON tp.id = bo.package_id
			WHERE bo.tenant_id = $1
			ORDER BY bo.created_at DESC
			LIMIT $2
		`, tenantID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT bo.id, bo.order_no, bo.tenant_id, bo.order_type, bo.status,
			       bo.amount_cents, bo.credits, bo.plan_id, bo.package_id,
			       bo.payment_channel, bo.qr_payload, bo.qr_url,
			       bo.paid_at, bo.expires_at, bo.note, bo.created_at, bo.updated_at,
			       COALESCE(sp.name, ''), COALESCE(tp.name, '')
			FROM billing_orders bo
			LEFT JOIN subscription_plans sp ON sp.id = bo.plan_id
			LEFT JOIN topup_packages tp ON tp.id = bo.package_id
			ORDER BY bo.created_at DESC
			LIMIT $1
		`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BillingOrder
	for rows.Next() {
		var o BillingOrder
		var planID, packageID *int
		var planName, packageName string
		err := rows.Scan(
			&o.ID, &o.OrderNo, &o.TenantID, &o.OrderType, &o.Status,
			&o.AmountCents, &o.Credits, &planID, &packageID,
			&o.PaymentChannel, &o.QRPayload, &o.QRURL,
			&o.PaidAt, &o.ExpiresAt, &o.Note, &o.CreatedAt, &o.UpdatedAt,
			&planName, &packageName,
		)
		if err != nil {
			return nil, err
		}
		o.PlanID = planID
		o.PackageID = packageID
		o.PlanName = planName
		o.PackageName = packageName
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Service) enrichOrderPaymentHint(ctx context.Context, o *BillingOrder) {
	ps, err := s.paymentSettings(ctx)
	if err != nil {
		return
	}
	qr := StubQRProvider{Settings: ps}.GenerateQR(o.OrderNo, o.AmountCents, o.PaymentChannel)
	o.PaymentHint = qr.Hint
	o.StubMode = qr.StubMode
}

func (s *Service) scanOrder(ctx context.Context, q string, args ...any) (BillingOrder, error) {
	row := s.pool.QueryRow(ctx, q, args...)
	return scanOrderFromRow(row)
}

type scannable interface {
	Scan(dest ...any) error
}

func scanOrderRow(rows scannable) (BillingOrder, error) {
	return scanOrderFromRow(rows)
}

func scanOrderFromRow(row scannable) (BillingOrder, error) {
	var o BillingOrder
	var planID, packageID *int
	var planName, packageName string
	err := row.Scan(
		&o.ID, &o.OrderNo, &o.TenantID, &o.OrderType, &o.Status,
		&o.AmountCents, &o.Credits, &planID, &packageID,
		&o.PaymentChannel, &o.QRPayload, &o.QRURL,
		&o.PaidAt, &o.ExpiresAt, &o.Note, &o.CreatedAt, &o.UpdatedAt,
		&planName, &packageName,
	)
	if err != nil {
		return BillingOrder{}, err
	}
	o.PlanID = planID
	o.PackageID = packageID
	o.PlanName = planName
	o.PackageName = packageName
	return o, nil
}

// ConfirmOrder marks an order paid and credits the tenant (admin manual confirm).
func (s *Service) ConfirmOrder(ctx context.Context, orderID int64, note string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var o BillingOrder
	var planID, packageID *int
	err = tx.QueryRow(ctx, `
		SELECT id, order_no, tenant_id, order_type, status, amount_cents, credits,
		       plan_id, package_id, payment_channel
		FROM billing_orders
		WHERE id = $1
		FOR UPDATE
	`, orderID).Scan(
		&o.ID, &o.OrderNo, &o.TenantID, &o.OrderType, &o.Status, &o.AmountCents, &o.Credits,
		&planID, &packageID, &o.PaymentChannel,
	)
	if err != nil {
		return err
	}
	if o.Status == OrderPaid {
		return nil
	}
	if o.Status != OrderPending {
		return fmt.Errorf("order status is %s, cannot confirm", o.Status)
	}
	if time.Now().After(o.ExpiresAt) {
		_, _ = tx.Exec(ctx, `UPDATE billing_orders SET status = 'expired', updated_at = now() WHERE id = $1`, orderID)
		return errors.New("order expired")
	}

	o.PlanID = planID
	o.PackageID = packageID

	if err := s.ensureWallet(ctx, tx, o.TenantID); err != nil {
		return err
	}

	switch o.OrderType {
	case OrderTopup:
		if err := s.creditPurchased(ctx, tx, o.TenantID, o.Credits, "order", o.OrderNo, "topup"); err != nil {
			return err
		}
	case OrderSubscribe:
		if o.PlanID == nil {
			return errors.New("subscribe order missing plan_id")
		}
		if err := s.activateSubscription(ctx, tx, o.TenantID, *o.PlanID, o.Credits); err != nil {
			return err
		}
		if err := s.writeLedger(ctx, tx, o.TenantID, "subscribe", o.Credits, "subscription_quota", "order", o.OrderNo, ""); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown order type: %s", o.OrderType)
	}

	_, err = tx.Exec(ctx, `
		UPDATE billing_orders
		   SET status = 'paid', paid_at = now(), note = $2, updated_at = now()
		 WHERE id = $1
	`, orderID, note)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) activateSubscription(ctx context.Context, tx pgx.Tx, tenantID string, planID int, monthlyCredits int64) error {
	now := time.Now()
	periodEnd := now.AddDate(0, 1, 0)
	_, err := tx.Exec(ctx, `
		UPDATE tenant_subscriptions
		   SET status = 'expired', updated_at = now()
		 WHERE tenant_id = $1 AND status = 'active'
	`, tenantID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO tenant_subscriptions (tenant_id, plan_id, status, period_start, period_end, quota_remaining)
		VALUES ($1, $2, 'active', $3, $4, $5)
	`, tenantID, planID, now, periodEnd, monthlyCredits)
	return err
}

func (s *Service) creditPurchased(ctx context.Context, tx pgx.Tx, tenantID string, amount int64, refType, refID, entryType string) error {
	var balance int64
	err := tx.QueryRow(ctx, `
		UPDATE tenant_credit_wallets
		   SET purchased_balance = purchased_balance + $2,
		       balance_credits = granted_balance + purchased_balance + $2,
		       updated_at = now()
		 WHERE tenant_id = $1
		RETURNING balance_credits
	`, tenantID, amount).Scan(&balance)
	if err != nil {
		return err
	}
	return s.writeLedgerWithBalance(ctx, tx, tenantID, entryType, amount, balance, "purchased", refType, refID, "")
}

// GrantCredits adds to granted_balance (admin credit grant).
func (s *Service) GrantCredits(ctx context.Context, tenantID string, amount int64, note string) error {
	if amount <= 0 {
		return fmt.Errorf("grant amount must be positive")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.ensureWallet(ctx, tx, tenantID); err != nil {
		return err
	}
	var balance int64
	err = tx.QueryRow(ctx, `
		UPDATE tenant_credit_wallets
		   SET granted_balance = granted_balance + $2,
		       balance_credits = granted_balance + purchased_balance + $2,
		       updated_at = now()
		 WHERE tenant_id = $1
		RETURNING balance_credits
	`, tenantID, amount).Scan(&balance)
	if err != nil {
		return err
	}
	if err := s.writeLedgerWithBalance(ctx, tx, tenantID, "adjust", amount, balance, "granted", "manual", "", note); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
