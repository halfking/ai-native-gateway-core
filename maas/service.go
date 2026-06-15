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
}

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
			remaining -= use
		}
	}

	var balance int64
	if remaining > 0 {
		err = tx.QueryRow(ctx, `
			UPDATE tenant_credit_wallets
			   SET balance_credits = balance_credits - $2, updated_at = now()
			 WHERE tenant_id = $1 AND balance_credits >= $2
		 RETURNING balance_credits
		`, tenantID, remaining).Scan(&balance)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, &InsufficientCreditsError{TenantID: tenantID, Required: amount, Available: quota}
			}
			return 0, err
		}
	} else {
		err = tx.QueryRow(ctx, `
			SELECT balance_credits FROM tenant_credit_wallets WHERE tenant_id = $1
		`, tenantID).Scan(&balance)
		if err != nil {
			return 0, err
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO credit_ledger (tenant_id, entry_type, amount, balance_after, ref_type, ref_id, note)
		VALUES ($1, 'consume', $2, $3, 'request', $4, '')
	`, tenantID, -amount, balance, requestID)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return amount, nil
}

func (s *Service) availableCredits(ctx context.Context, tenantID string) (int64, error) {
	var balance int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(balance_credits, 0)
		FROM tenant_credit_wallets WHERE tenant_id = $1
	`, tenantID).Scan(&balance)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var quota int64
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(quota_remaining, 0)
		FROM tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active' AND period_end > now()
		ORDER BY period_end DESC LIMIT 1
	`, tenantID).Scan(&quota)
	return balance + quota, nil
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
		SELECT cents_per_credit::float8, base_credits_per_1m, currency_display
		FROM maas_settings WHERE id = 1
	`).Scan(&st.CentsPerCredit, &st.BaseCreditsPer1M, &st.CurrencyDisplay)
	return st, err
}

// UpdateSettings writes global conversion settings (super_admin).
func (s *Service) UpdateSettings(ctx context.Context, st Settings) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE maas_settings SET
			cents_per_credit = $1,
			base_credits_per_1m = $2,
			currency_display = $3,
			updated_at = now()
		WHERE id = 1
	`, st.CentsPerCredit, st.BaseCreditsPer1M, st.CurrencyDisplay)
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

// WalletView is the tenant-facing balance summary.
type WalletView struct {
	TenantID        string `json:"tenant_id"`
	BalanceCredits  int64  `json:"balance_credits"`
	QuotaRemaining  int64  `json:"quota_remaining"`
	TotalAvailable  int64  `json:"total_available"`
}

func (s *Service) GetWallet(ctx context.Context, tenantID string) (WalletView, error) {
	w := WalletView{TenantID: tenantID}
	_ = s.ensureWalletDirect(ctx, tenantID)
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(balance_credits, 0) FROM tenant_credit_wallets WHERE tenant_id = $1
	`, tenantID).Scan(&w.BalanceCredits)
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(quota_remaining, 0)
		FROM tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active' AND period_end > now()
		ORDER BY period_end DESC LIMIT 1
	`, tenantID).Scan(&w.QuotaRemaining)
	w.TotalAvailable = w.BalanceCredits + w.QuotaRemaining
	return w, nil
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
		SELECT id, entry_type, amount, balance_after, ref_type, ref_id, note, created_at
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
		if err := rows.Scan(&e.ID, &e.EntryType, &e.Amount, &e.BalanceAfter, &e.RefType, &e.RefID, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AdjustCredits adds credits to a tenant wallet (super_admin manual top-up).
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
	var balance int64
	err = tx.QueryRow(ctx, `
		UPDATE tenant_credit_wallets
		   SET balance_credits = balance_credits + $2, updated_at = now()
		 WHERE tenant_id = $1
		RETURNING balance_credits
	`, tenantID, amount).Scan(&balance)
	if err != nil {
		return err
	}
	entryType := "adjust"
	if amount > 0 {
		entryType = "topup"
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO credit_ledger (tenant_id, entry_type, amount, balance_after, ref_type, note)
		VALUES ($1, $2, $3, $4, 'manual', $5)
	`, tenantID, entryType, amount, balance, note)
	if err != nil {
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
