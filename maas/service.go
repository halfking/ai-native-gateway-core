package maas

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service handles tenant credit wallets, plans, and consumption.
type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	if pool == nil {
		return nil
	}
	return &Service{pool: pool}
}

func (s *Service) Enabled() bool {
	return s != nil && s.pool != nil
}

// Settings holds global MaaS conversion knobs.
type Settings struct {
	CentsPerCredit    float64 `json:"cents_per_credit"`
	BaseCreditsPer1M  int64   `json:"base_credits_per_1m"`
	CurrencyDisplay   string  `json:"currency_display"`
	AlipayAccount     string  `json:"alipay_account"`
	WechatMchID       string  `json:"wechat_mch_id"`
	StubAlipayQRURL   string  `json:"stub_alipay_qr_url"`
	StubWechatQRURL   string  `json:"stub_wechat_qr_url"`
}

// CreditPool identifies which balance pool was affected.
type CreditPool string

const (
	PoolSubscription CreditPool = "subscription_quota"
	PoolGranted      CreditPool = "granted"
	PoolPurchased    CreditPool = "purchased"
)

// PreCheckCredits blocks new requests when a non-default tenant has no credits.
func (s *Service) PreCheckCredits(ctx context.Context, tenantID string) error {
	if !s.Enabled() || tenantID == "" || tenantID == "default" {
		return nil
	}
	avail, err := s.availableCredits(ctx, tenantID)
	if err != nil {
		return err
	}
	if avail <= 0 {
		return &InsufficientCreditsError{TenantID: tenantID, Required: 1, Available: 0}
	}
	return nil
}

// ChargeRequest deducts credits after a successful upstream call.
func (s *Service) ChargeRequest(ctx context.Context, tenantID, requestID, canonicalName string, promptTokens, completionTokens int) (int64, error) {
	if !s.Enabled() || tenantID == "" || tenantID == "default" {
		return 0, nil
	}
	if promptTokens <= 0 && completionTokens <= 0 {
		return 0, nil
	}

	settings, err := s.GetSettings(ctx)
	if err != nil {
		return 0, err
	}
	rateIn, rateOut, err := s.modelRates(ctx, canonicalName, settings.BaseCreditsPer1M)
	if err != nil {
		return 0, err
	}
	amount := CalcCredits(promptTokens, completionTokens, rateIn, rateOut, settings.BaseCreditsPer1M)
	if amount <= 0 {
		return 0, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if err := s.ensureWallet(ctx, tx, tenantID); err != nil {
		return 0, err
	}

	remaining := amount
	subID, quota, err := s.activeSubscriptionQuota(ctx, tx, tenantID)
	if err != nil {
		return 0, err
	}
	if subID > 0 && quota > 0 {
		use := min64(remaining, quota)
		if use > 0 {
			_, err = tx.Exec(ctx, `
				UPDATE tenant_subscriptions
				   SET quota_remaining = quota_remaining - $2, updated_at = now()
				 WHERE id = $1 AND quota_remaining >= $2
			`, subID, use)
			if err != nil {
				return 0, err
			}
			if err := s.writeLedger(ctx, tx, tenantID, "consume", -use, string(PoolSubscription), "request", requestID, ""); err != nil {
				return 0, err
			}
			remaining -= use
		}
	}

	var granted, purchased int64
	err = tx.QueryRow(ctx, `
		SELECT granted_balance, purchased_balance FROM tenant_credit_wallets WHERE tenant_id = $1 FOR UPDATE
	`, tenantID).Scan(&granted, &purchased)
	if err != nil {
		return 0, err
	}

	if remaining > 0 {
		useGranted := min64(remaining, granted)
		if useGranted > 0 {
			granted -= useGranted
			remaining -= useGranted
			if err := s.writeLedger(ctx, tx, tenantID, "consume", -useGranted, string(PoolGranted), "request", requestID, ""); err != nil {
				return 0, err
			}
		}
	}
	if remaining > 0 {
		if purchased < remaining {
			return 0, &InsufficientCreditsError{TenantID: tenantID, Required: amount, Available: quota + granted + purchased}
		}
		purchased -= remaining
		if err := s.writeLedger(ctx, tx, tenantID, "consume", -remaining, string(PoolPurchased), "request", requestID, ""); err != nil {
			return 0, err
		}
		remaining = 0
	}

	var balance int64
	err = tx.QueryRow(ctx, `
		UPDATE tenant_credit_wallets
		   SET granted_balance = $2,
		       purchased_balance = $3,
		       balance_credits = $2 + $3,
		       updated_at = now()
		 WHERE tenant_id = $1
		RETURNING balance_credits
	`, tenantID, granted, purchased).Scan(&balance)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return amount, nil
}

func (s *Service) availableCredits(ctx context.Context, tenantID string) (int64, error) {
	w, err := s.GetWallet(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	return w.TotalAvailable, nil
}

func (s *Service) activeSubscriptionQuota(ctx context.Context, tx pgx.Tx, tenantID string) (subID int, quota int64, err error) {
	err = tx.QueryRow(ctx, `
		SELECT id, quota_remaining
		FROM tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active' AND period_end > now()
		ORDER BY period_end DESC LIMIT 1
		FOR UPDATE
	`, tenantID).Scan(&subID, &quota)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	return subID, quota, err
}

func (s *Service) ensureWallet(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tenant_credit_wallets (tenant_id) VALUES ($1)
		ON CONFLICT (tenant_id) DO NOTHING
	`, tenantID)
	return err
}

func (s *Service) modelRates(ctx context.Context, canonicalName string, base int64) (in, out int64, err error) {
	if canonicalName == "" {
		return 0, 0, nil
	}
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(mcr.credits_per_1m_in, 0), COALESCE(mcr.credits_per_1m_out, 0)
		FROM models_canonical mc
		LEFT JOIN model_credit_rates mcr ON mcr.canonical_id = mc.id
		WHERE mc.canonical_name = $1 AND COALESCE(mc.status, 'active') = 'active'
		LIMIT 1
	`, canonicalName).Scan(&in, &out)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	return in, out, err
}

// GetSettings returns the singleton maas_settings row.
func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.pool.QueryRow(ctx, `
		SELECT cents_per_credit::float8, base_credits_per_1m, currency_display,
		       COALESCE(alipay_account, ''), COALESCE(wechat_mch_id, ''),
		       COALESCE(stub_alipay_qr_url, ''), COALESCE(stub_wechat_qr_url, '')
		FROM maas_settings WHERE id = 1
	`).Scan(&st.CentsPerCredit, &st.BaseCreditsPer1M, &st.CurrencyDisplay,
		&st.AlipayAccount, &st.WechatMchID, &st.StubAlipayQRURL, &st.StubWechatQRURL)
	return st, err
}

// UpdateSettings writes global conversion settings (super_admin).
func (s *Service) UpdateSettings(ctx context.Context, st Settings) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE maas_settings SET
			cents_per_credit = $1,
			base_credits_per_1m = $2,
			currency_display = $3,
			alipay_account = $4,
			wechat_mch_id = $5,
			stub_alipay_qr_url = $6,
			stub_wechat_qr_url = $7,
			updated_at = now()
		WHERE id = 1
	`, st.CentsPerCredit, st.BaseCreditsPer1M, st.CurrencyDisplay,
		st.AlipayAccount, st.WechatMchID, st.StubAlipayQRURL, st.StubWechatQRURL)
	return err
}

// Plan is a subscription tier exposed to tenants.
type Plan struct {
	ID              int    `json:"id"`
	Code            string `json:"code"`
	Tier            string `json:"tier"`
	Name            string `json:"name"`
	PriceCents      int    `json:"price_cents"`
	MonthlyCredits  int64  `json:"monthly_credits"`
	Enabled         bool   `json:"enabled"`
	SortOrder       int    `json:"sort_order"`
}

// TopupPackage is a one-time credit bundle.
type TopupPackage struct {
	ID            int    `json:"id"`
	Code          string `json:"code"`
	Tier          string `json:"tier"`
	Name          string `json:"name"`
	PriceCents    int    `json:"price_cents"`
	CreditsAmount int64  `json:"credits_amount"`
	Enabled       bool   `json:"enabled"`
	SortOrder     int    `json:"sort_order"`
}

func (s *Service) ListPlans(ctx context.Context, enabledOnly bool) ([]Plan, error) {
	q := `SELECT id, code, tier, name, price_cents, monthly_credits, enabled, sort_order
	      FROM subscription_plans`
	if enabledOnly {
		q += ` WHERE enabled = TRUE`
	}
	q += ` ORDER BY sort_order, id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Code, &p.Tier, &p.Name, &p.PriceCents, &p.MonthlyCredits, &p.Enabled, &p.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) ListTopupPackages(ctx context.Context, enabledOnly bool) ([]TopupPackage, error) {
	q := `SELECT id, code, tier, name, price_cents, credits_amount, enabled, sort_order
	      FROM topup_packages`
	if enabledOnly {
		q += ` WHERE enabled = TRUE`
	}
	q += ` ORDER BY sort_order, id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopupPackage
	for rows.Next() {
		var p TopupPackage
		if err := rows.Scan(&p.ID, &p.Code, &p.Tier, &p.Name, &p.PriceCents, &p.CreditsAmount, &p.Enabled, &p.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// WalletView is the tenant-facing balance summary (three pools).
type WalletView struct {
	TenantID         string     `json:"tenant_id"`
	QuotaRemaining   int64      `json:"quota_remaining"`
	GrantedBalance   int64      `json:"granted_balance"`
	PurchasedBalance int64      `json:"purchased_balance"`
	BalanceCredits   int64      `json:"balance_credits"`
	TotalAvailable   int64      `json:"total_available"`
	Subscription     *SubscriptionView `json:"subscription,omitempty"`
}

// SubscriptionView is the active subscription summary.
type SubscriptionView struct {
	PlanID      int       `json:"plan_id"`
	PlanName    string    `json:"plan_name"`
	Status      string    `json:"status"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
}

// AccountView combines wallet, subscription, and recent ledger for account center.
type AccountView struct {
	Wallet       WalletView    `json:"wallet"`
	RecentLedger []LedgerEntry `json:"recent_ledger"`
	RecentOrders []BillingOrder `json:"recent_orders"`
}

func (s *Service) GetWallet(ctx context.Context, tenantID string) (WalletView, error) {
	w := WalletView{TenantID: tenantID}
	_ = s.ensureWalletDirect(ctx, tenantID)
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(granted_balance, 0), COALESCE(purchased_balance, 0), COALESCE(balance_credits, 0)
		FROM tenant_credit_wallets WHERE tenant_id = $1
	`, tenantID).Scan(&w.GrantedBalance, &w.PurchasedBalance, &w.BalanceCredits)
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(quota_remaining, 0)
		FROM tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active' AND period_end > now()
		ORDER BY period_end DESC LIMIT 1
	`, tenantID).Scan(&w.QuotaRemaining)

	var sub SubscriptionView
	var planName string
	err := s.pool.QueryRow(ctx, `
		SELECT ts.plan_id, sp.name, ts.status, ts.period_start, ts.period_end
		FROM tenant_subscriptions ts
		JOIN subscription_plans sp ON sp.id = ts.plan_id
		WHERE ts.tenant_id = $1 AND ts.status = 'active' AND ts.period_end > now()
		ORDER BY ts.period_end DESC LIMIT 1
	`, tenantID).Scan(&sub.PlanID, &planName, &sub.Status, &sub.PeriodStart, &sub.PeriodEnd)
	if err == nil {
		sub.PlanName = planName
		w.Subscription = &sub
	}

	w.TotalAvailable = w.QuotaRemaining + w.GrantedBalance + w.PurchasedBalance
	if w.BalanceCredits == 0 {
		w.BalanceCredits = w.GrantedBalance + w.PurchasedBalance
	}
	return w, nil
}

// GetAccount returns wallet + recent ledger + orders for tenant account center.
func (s *Service) GetAccount(ctx context.Context, tenantID string) (AccountView, error) {
	wallet, err := s.GetWallet(ctx, tenantID)
	if err != nil {
		return AccountView{}, err
	}
	ledger, err := s.ListLedger(ctx, tenantID, 10)
	if err != nil {
		return AccountView{}, err
	}
	orders, err := s.ListOrders(ctx, tenantID, 5)
	if err != nil {
		return AccountView{}, err
	}
	return AccountView{Wallet: wallet, RecentLedger: ledger, RecentOrders: orders}, nil
}

func (s *Service) ensureWalletDirect(ctx context.Context, tenantID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tenant_credit_wallets (tenant_id) VALUES ($1)
		ON CONFLICT (tenant_id) DO NOTHING
	`, tenantID)
	return err
}

// ModelRateRow is a public model listing entry with credit pricing.
type ModelRateRow struct {
	CanonicalName   string `json:"canonical_name"`
	DisplayName     string `json:"display_name"`
	CreditsPer1MIn  int64  `json:"credits_per_1m_in"`
	CreditsPer1MOut int64  `json:"credits_per_1m_out"`
}

func (s *Service) ListPublicModels(ctx context.Context) ([]ModelRateRow, error) {
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	base := settings.BaseCreditsPer1M
	rows, err := s.pool.Query(ctx, `
		SELECT mc.canonical_name,
		       COALESCE(NULLIF(TRIM(mc.display_name), ''), mc.canonical_name),
		       COALESCE(NULLIF(mcr.credits_per_1m_in, 0), $1),
		       COALESCE(NULLIF(mcr.credits_per_1m_out, 0), $1)
		FROM models_canonical mc
		LEFT JOIN model_credit_rates mcr ON mcr.canonical_id = mc.id
		WHERE COALESCE(mc.status, 'active') = 'active'
		ORDER BY mc.canonical_name
	`, base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelRateRow
	for rows.Next() {
		var r ModelRateRow
		if err := rows.Scan(&r.CanonicalName, &r.DisplayName, &r.CreditsPer1MIn, &r.CreditsPer1MOut); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LedgerEntry is one credit ledger row.
type LedgerEntry struct {
	ID           int64     `json:"id"`
	EntryType    string    `json:"entry_type"`
	Amount       int64     `json:"amount"`
	BalanceAfter int64     `json:"balance_after"`
	Pool         *string   `json:"pool"`
	RefType      *string   `json:"ref_type"`
	RefID        *string   `json:"ref_id"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Service) ListLedger(ctx context.Context, tenantID string, limit int) ([]LedgerEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, entry_type, amount, balance_after, pool, ref_type, ref_id, note, created_at
		FROM credit_ledger
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.EntryType, &e.Amount, &e.BalanceAfter, &e.Pool, &e.RefType, &e.RefID, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) writeLedger(ctx context.Context, tx pgx.Tx, tenantID, entryType string, amount int64, pool, refType, refID, note string) error {
	var balance int64
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(granted_balance, 0) + COALESCE(purchased_balance, 0)
		FROM tenant_credit_wallets WHERE tenant_id = $1
	`, tenantID).Scan(&balance)
	return s.writeLedgerWithBalance(ctx, tx, tenantID, entryType, amount, balance, pool, refType, refID, note)
}

func (s *Service) writeLedgerWithBalance(ctx context.Context, tx pgx.Tx, tenantID, entryType string, amount, balanceAfter int64, pool, refType, refID, note string) error {
	var refTypeVal, refIDVal *string
	if refType != "" {
		refTypeVal = &refType
	}
	if refID != "" {
		refIDVal = &refID
	}
	var poolVal *string
	if pool != "" {
		poolVal = &pool
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO credit_ledger (tenant_id, entry_type, amount, balance_after, pool, ref_type, ref_id, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, tenantID, entryType, amount, balanceAfter, poolVal, refTypeVal, refIDVal, note)
	return err
}

// AdjustCredits adds credits to purchased_balance (super_admin manual top-up).
func (s *Service) AdjustCredits(ctx context.Context, tenantID string, amount int64, note string) error {
	if amount == 0 {
		return fmt.Errorf("adjust amount must be non-zero")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.ensureWallet(ctx, tx, tenantID); err != nil {
		return err
	}
	entryType := "adjust"
	pool := string(PoolPurchased)
	if amount > 0 {
		entryType = "topup"
	}
	var balance int64
	if amount > 0 {
		err = tx.QueryRow(ctx, `
			UPDATE tenant_credit_wallets
			   SET purchased_balance = purchased_balance + $2,
			       balance_credits = granted_balance + purchased_balance + $2,
			       updated_at = now()
			 WHERE tenant_id = $1
			RETURNING balance_credits
		`, tenantID, amount).Scan(&balance)
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE tenant_credit_wallets
			   SET purchased_balance = purchased_balance + $2,
			       balance_credits = granted_balance + purchased_balance + $2,
			       updated_at = now()
			 WHERE tenant_id = $1 AND purchased_balance + $2 >= 0
			RETURNING balance_credits
		`, tenantID, amount).Scan(&balance)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insufficient purchased balance")
		}
		return err
	}
	if err := s.writeLedgerWithBalance(ctx, tx, tenantID, entryType, amount, balance, pool, "manual", "", note); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
