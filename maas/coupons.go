package maas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Coupon is a redeemable credit voucher.
type Coupon struct {
	ID               int64      `json:"id"`
	Code             string     `json:"code"`
	CreditsAmount    int64      `json:"credits_amount"`
	MaxRedemptions   int        `json:"max_redemptions"`
	RedemptionCount  int        `json:"redemption_count"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Status           string     `json:"status"`
	Note             string     `json:"note"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// CouponRedemption records one successful activation.
type CouponRedemption struct {
	ID             int64     `json:"id"`
	CouponID       int64     `json:"coupon_id"`
	TenantID       string    `json:"tenant_id"`
	CreditsGranted int64     `json:"credits_granted"`
	CreatedAt      time.Time `json:"created_at"`
	Code           string    `json:"code,omitempty"`
}

// CreateCouponRequest is the admin create payload.
type CreateCouponRequest struct {
	Code           string     `json:"code"`
	CreditsAmount  int64      `json:"credits_amount"`
	MaxRedemptions int        `json:"max_redemptions"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Note           string     `json:"note"`
	CreatedBy      string     `json:"created_by"`
}

func normalizeCouponCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func generateCouponCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("CPN-%s", strings.ToUpper(hex.EncodeToString(b))), nil
}

// CreateCoupon inserts an active coupon. Empty code auto-generates CPN-XXXXXXXX.
func (s *Service) CreateCoupon(ctx context.Context, req CreateCouponRequest) (Coupon, error) {
	if !s.Enabled() {
		return Coupon{}, errors.New("maas service disabled")
	}
	if req.CreditsAmount <= 0 {
		return Coupon{}, fmt.Errorf("credits_amount must be positive")
	}
	if req.MaxRedemptions <= 0 {
		req.MaxRedemptions = 1
	}
	code := normalizeCouponCode(req.Code)
	if code == "" {
		var err error
		code, err = generateCouponCode()
		if err != nil {
			return Coupon{}, err
		}
	}
	var c Coupon
	err := s.pool.QueryRow(ctx, `
		INSERT INTO maas_coupons (
			code, credits_amount, max_redemptions, expires_at, status, note, created_by
		) VALUES ($1, $2, $3, $4, 'active', $5, $6)
		RETURNING id, code, credits_amount, max_redemptions, redemption_count,
		          expires_at, status, note, created_by, created_at, updated_at
	`, code, req.CreditsAmount, req.MaxRedemptions, req.ExpiresAt, req.Note, req.CreatedBy).Scan(
		&c.ID, &c.Code, &c.CreditsAmount, &c.MaxRedemptions, &c.RedemptionCount,
		&c.ExpiresAt, &c.Status, &c.Note, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
	)
	return c, err
}

// ListCoupons returns recent coupons (admin).
func (s *Service) ListCoupons(ctx context.Context, limit int) ([]Coupon, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, code, credits_amount, max_redemptions, redemption_count,
		       expires_at, status, note, created_by, created_at, updated_at
		  FROM maas_coupons
		 ORDER BY created_at DESC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Coupon
	for rows.Next() {
		var c Coupon
		if err := rows.Scan(
			&c.ID, &c.Code, &c.CreditsAmount, &c.MaxRedemptions, &c.RedemptionCount,
			&c.ExpiresAt, &c.Status, &c.Note, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return jsonSlice(out), rows.Err()
}

// ListCouponRedemptions returns activations for a tenant.
func (s *Service) ListCouponRedemptions(ctx context.Context, tenantID string, limit int) ([]CouponRedemption, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.coupon_id, r.tenant_id, r.credits_granted, r.created_at, c.code
		  FROM maas_coupon_redemptions r
		  JOIN maas_coupons c ON c.id = r.coupon_id
		 WHERE r.tenant_id = $1
		 ORDER BY r.created_at DESC
		 LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CouponRedemption
	for rows.Next() {
		var r CouponRedemption
		if err := rows.Scan(&r.ID, &r.CouponID, &r.TenantID, &r.CreditsGranted, &r.CreatedAt, &r.Code); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return jsonSlice(out), rows.Err()
}

// ActivateCoupon redeems a coupon into the tenant granted_balance exactly once
// per (coupon, tenant). Concurrent activations for the same pair: only one commits.
func (s *Service) ActivateCoupon(ctx context.Context, tenantID, code string) (CouponRedemption, error) {
	if !s.Enabled() {
		return CouponRedemption{}, errors.New("maas service disabled")
	}
	if tenantID == "" || tenantID == "default" {
		return CouponRedemption{}, fmt.Errorf("tenant_id required")
	}
	code = normalizeCouponCode(code)
	if code == "" {
		return CouponRedemption{}, fmt.Errorf("coupon code required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CouponRedemption{}, err
	}
	//nolint:errcheck
	defer tx.Rollback(ctx)

	var c Coupon
	err = tx.QueryRow(ctx, `
		SELECT id, code, credits_amount, max_redemptions, redemption_count,
		       expires_at, status
		  FROM maas_coupons
		 WHERE code = $1
		 FOR UPDATE
	`, code).Scan(
		&c.ID, &c.Code, &c.CreditsAmount, &c.MaxRedemptions, &c.RedemptionCount,
		&c.ExpiresAt, &c.Status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CouponRedemption{}, fmt.Errorf("coupon not found")
	}
	if err != nil {
		return CouponRedemption{}, err
	}
	if c.Status != "active" {
		return CouponRedemption{}, fmt.Errorf("coupon is %s", c.Status)
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(time.Now().UTC()) {
		_, _ = tx.Exec(ctx, `UPDATE maas_coupons SET status = 'expired', updated_at = now() WHERE id = $1`, c.ID)
		return CouponRedemption{}, fmt.Errorf("coupon expired")
	}
	if c.RedemptionCount >= c.MaxRedemptions {
		_, _ = tx.Exec(ctx, `UPDATE maas_coupons SET status = 'exhausted', updated_at = now() WHERE id = $1`, c.ID)
		return CouponRedemption{}, fmt.Errorf("coupon exhausted")
	}

	// Already redeemed by this tenant?
	var existingID int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM maas_coupon_redemptions WHERE coupon_id = $1 AND tenant_id = $2
	`, c.ID, tenantID).Scan(&existingID)
	if err == nil {
		return CouponRedemption{}, fmt.Errorf("coupon already activated for this tenant")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CouponRedemption{}, err
	}

	if err := s.ensureWallet(ctx, tx, tenantID); err != nil {
		return CouponRedemption{}, err
	}

	var balance int64
	err = tx.QueryRow(ctx, `
		UPDATE tenant_credit_wallets
		   SET granted_balance = granted_balance + $2::bigint,
		       balance_credits = granted_balance + purchased_balance + $2::bigint,
		       updated_at = now()
		 WHERE tenant_id = $1
		RETURNING balance_credits
	`, tenantID, c.CreditsAmount).Scan(&balance)
	if err != nil {
		return CouponRedemption{}, err
	}
	note := fmt.Sprintf("coupon %s", c.Code)
	if err := s.writeLedgerWithBalance(ctx, tx, tenantID, "adjust", c.CreditsAmount, balance, string(PoolGranted), "coupon", fmt.Sprintf("%d", c.ID), note); err != nil {
		return CouponRedemption{}, err
	}

	var red CouponRedemption
	err = tx.QueryRow(ctx, `
		INSERT INTO maas_coupon_redemptions (coupon_id, tenant_id, credits_granted)
		VALUES ($1, $2, $3)
		RETURNING id, coupon_id, tenant_id, credits_granted, created_at
	`, c.ID, tenantID, c.CreditsAmount).Scan(
		&red.ID, &red.CouponID, &red.TenantID, &red.CreditsGranted, &red.CreatedAt,
	)
	if err != nil {
		return CouponRedemption{}, err
	}
	red.Code = c.Code

	newCount := c.RedemptionCount + 1
	newStatus := "active"
	if newCount >= c.MaxRedemptions {
		newStatus = "exhausted"
	}
	_, err = tx.Exec(ctx, `
		UPDATE maas_coupons
		   SET redemption_count = $2, status = $3, updated_at = now()
		 WHERE id = $1
	`, c.ID, newCount, newStatus)
	if err != nil {
		return CouponRedemption{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CouponRedemption{}, err
	}
	return red, nil
}
