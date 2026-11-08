package maas

// marketing_admin.go — 2026-11-09（运营审计轮项4）：营销策略管理面。
//
// 既有状态（2026-11-09 取证）：plans/topup_packages 只有 GET 目录，
// 934 商业列（bonus_credits / effective_from/to / first_time_only /
// overage / auto_renew…）零消费；campaigns 只有目录+兑现+对账、
// discount/gift 两种 benefit 落库闭集里但 Go 拒收；coupons 只能创建
// 不能停用。本文件补齐管理面 CRUD 与状态机；兑现/下单侧的行为改动在
// campaigns.go（gift 兑现 / discount 挂单）与 orders.go（加油包 bonus、
// 生效窗、first_time_only）。
//
// 红线：
//   • 所有写路径 superAdmin（路由层闸，应用层不重复造轮）；
//   • 不新增迁移 —— 全部落在 934/937 已有的列上。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------- plans ----

// PlanUpsert is the admin write model for subscription_plans (934 columns
// included; zero-value columns are written as NULL/defaults).
type PlanUpsert struct {
	Code              string     `json:"code"`
	Tier              string     `json:"tier"`
	Name              string     `json:"name"`
	PriceCents        int        `json:"price_cents"`
	MonthlyCredits    int64      `json:"monthly_credits"`
	BillingPeriod     string     `json:"billing_period"`
	QuotaCredits      *int64     `json:"quota_credits"`
	OveragePriceCents *int       `json:"overage_price_cents"`
	OverageAllowed    bool       `json:"overage_allowed"`
	EffectiveFrom     *time.Time `json:"effective_from"`
	EffectiveTo       *time.Time `json:"effective_to"`
	AutoRenew         bool       `json:"auto_renew"`
	ConcurrencyLimit  *int       `json:"concurrency_limit"`
	SLA               string     `json:"sla"`
	Enabled           bool       `json:"enabled"`
	SortOrder         int        `json:"sort_order"`
}

// UpsertPlan creates or updates a subscription plan keyed by code.
func (s *Service) UpsertPlan(ctx context.Context, req PlanUpsert) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	if req.Code == "" || req.Name == "" {
		return errors.New("code and name required")
	}
	if req.PriceCents < 0 {
		return errors.New("price_cents must be >= 0")
	}
	if req.BillingPeriod == "" {
		req.BillingPeriod = "monthly"
	}
	if req.Tier == "" {
		req.Tier = "basic"
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO subscription_plans (
			code, tier, name, price_cents, monthly_credits,
			billing_period, quota_credits, overage_price_cents, overage_allowed,
			effective_from, effective_to, auto_renew, concurrency_limit, sla,
			enabled, sort_order
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (code) DO UPDATE SET
			tier = EXCLUDED.tier,
			name = EXCLUDED.name,
			price_cents = EXCLUDED.price_cents,
			monthly_credits = EXCLUDED.monthly_credits,
			billing_period = EXCLUDED.billing_period,
			quota_credits = EXCLUDED.quota_credits,
			overage_price_cents = EXCLUDED.overage_price_cents,
			overage_allowed = EXCLUDED.overage_allowed,
			effective_from = EXCLUDED.effective_from,
			effective_to = EXCLUDED.effective_to,
			auto_renew = EXCLUDED.auto_renew,
			concurrency_limit = EXCLUDED.concurrency_limit,
			sla = EXCLUDED.sla,
			enabled = EXCLUDED.enabled,
			sort_order = EXCLUDED.sort_order,
			updated_at = now()
	`, req.Code, req.Tier, req.Name, req.PriceCents, req.MonthlyCredits,
		req.BillingPeriod, req.QuotaCredits, req.OveragePriceCents, req.OverageAllowed,
		req.EffectiveFrom, req.EffectiveTo, req.AutoRenew, req.ConcurrencyLimit, req.SLA,
		req.Enabled, req.SortOrder)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("plan upsert wrote no rows")
	}
	return nil
}

// SetPlanEnabled flips one plan's availability (下架/上架)。
func (s *Service) SetPlanEnabled(ctx context.Context, id int, enabled bool) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE subscription_plans SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// --------------------------------------------------------------- topups ----

// TopupUpsert is the admin write model for topup_packages.
type TopupUpsert struct {
	Code          string     `json:"code"`
	Tier          string     `json:"tier"`
	Name          string     `json:"name"`
	PriceCents    int        `json:"price_cents"`
	CreditsAmount int64      `json:"credits_amount"`
	BonusCredits  int64      `json:"bonus_credits"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	FirstTimeOnly bool       `json:"first_time_only"`
	Enabled       bool       `json:"enabled"`
	SortOrder     int        `json:"sort_order"`
}

// UpsertTopupPackage creates or updates a topup (加油包) package keyed by code.
func (s *Service) UpsertTopupPackage(ctx context.Context, req TopupUpsert) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	if req.Code == "" || req.Name == "" {
		return errors.New("code and name required")
	}
	if req.PriceCents < 0 || req.CreditsAmount <= 0 {
		return errors.New("price_cents must be >= 0 and credits_amount must be > 0")
	}
	if req.BonusCredits < 0 {
		return errors.New("bonus_credits must be >= 0")
	}
	if req.Tier == "" {
		req.Tier = "small"
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO topup_packages (
			code, tier, name, price_cents, credits_amount, bonus_credits,
			effective_from, effective_to, first_time_only, enabled, sort_order
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (code) DO UPDATE SET
			tier = EXCLUDED.tier,
			name = EXCLUDED.name,
			price_cents = EXCLUDED.price_cents,
			credits_amount = EXCLUDED.credits_amount,
			bonus_credits = EXCLUDED.bonus_credits,
			effective_from = EXCLUDED.effective_from,
			effective_to = EXCLUDED.effective_to,
			first_time_only = EXCLUDED.first_time_only,
			enabled = EXCLUDED.enabled,
			sort_order = EXCLUDED.sort_order,
			updated_at = now()
	`, req.Code, req.Tier, req.Name, req.PriceCents, req.CreditsAmount, req.BonusCredits,
		req.EffectiveFrom, req.EffectiveTo, req.FirstTimeOnly, req.Enabled, req.SortOrder)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("topup package upsert wrote no rows")
	}
	return nil
}

// SetTopupPackageEnabled flips one package's availability.
func (s *Service) SetTopupPackageEnabled(ctx context.Context, id int, enabled bool) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE topup_packages SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ------------------------------------------------------------- coupons ----

// SetCouponStatus flips a coupon's lifecycle status (active/disabled). The
// CHECK allows disabled; ActivateCoupon already refuses non-active coupons.
func (s *Service) SetCouponStatus(ctx context.Context, id int, status string) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	if status != "active" && status != "disabled" {
		return errors.New("status must be active or disabled (use redemption/exhaustion for the rest)")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE maas_coupons SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ------------------------------------------------------------ campaigns ----

// AdminCampaign is one promotion campaign row (admin view — includes drafts).
type AdminCampaign struct {
	ID                      int64          `json:"id"`
	Code                    string         `json:"code"`
	Name                    string         `json:"name"`
	Description             string         `json:"description"`
	BenefitType             string         `json:"benefit_type"`
	BenefitValue            map[string]any `json:"benefit_value"`
	Status                  string         `json:"status"`
	StartsAt                *time.Time     `json:"starts_at"`
	EndsAt                  *time.Time     `json:"ends_at"`
	MaxRedemptionsPerTenant int            `json:"max_redemptions_per_tenant"`
	TotalBudgetCredits      int64          `json:"total_budget_credits"`
	GrantedCredits          int64          `json:"granted_credits"`
	CreatedAt               time.Time      `json:"created_at"`
}

// ListCampaignsAdmin returns every campaign (draft/paused/ended included).
func (s *Service) ListCampaignsAdmin(ctx context.Context) ([]AdminCampaign, error) {
	out := []AdminCampaign{}
	if !s.Enabled() {
		return out, errors.New("maas service not enabled")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, code, name, description, benefit_type, benefit_value,
		       status, starts_at, ends_at, max_redemptions_per_tenant,
		       total_budget_credits, granted_credits, created_at
		FROM promotion_campaigns
		ORDER BY id DESC
		LIMIT 200
	`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c AdminCampaign
		var benefit []byte
		if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.Description, &c.BenefitType,
			&benefit, &c.Status, &c.StartsAt, &c.EndsAt, &c.MaxRedemptionsPerTenant,
			&c.TotalBudgetCredits, &c.GrantedCredits, &c.CreatedAt); err != nil {
			return out, err
		}
		c.BenefitValue = map[string]any{}
		if len(benefit) > 0 {
			_ = json.Unmarshal(benefit, &c.BenefitValue)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CampaignUpsert is the admin write model for promotion_campaigns.
type CampaignUpsert struct {
	Code                    string         `json:"code"`
	Name                    string         `json:"name"`
	Description             string         `json:"description"`
	BenefitType             string         `json:"benefit_type"` // credits | discount | gift
	BenefitValue            map[string]any `json:"benefit_value"`
	StartsAt                *time.Time     `json:"starts_at"`
	EndsAt                  *time.Time     `json:"ends_at"`
	MaxRedemptionsPerTenant int            `json:"max_redemptions_per_tenant"`
	TotalBudgetCredits      int64          `json:"total_budget_credits"`
}

// CreateCampaign inserts a draft campaign. Benefit value shape per type:
//
//	credits/gift: {"credits": N}   discount: {"percent": 1..99}
func (s *Service) CreateCampaign(ctx context.Context, req CampaignUpsert) (AdminCampaign, error) {
	out := AdminCampaign{}
	if !s.Enabled() {
		return out, errors.New("maas service not enabled")
	}
	if req.Code == "" || req.Name == "" {
		return out, errors.New("code and name required")
	}
	switch req.BenefitType {
	case "credits", "gift":
		if v, ok := req.BenefitValue["credits"].(float64); !ok || v <= 0 {
			return out, errors.New("benefit_value.credits must be a positive number")
		}
	case "discount":
		v, ok := req.BenefitValue["percent"].(float64)
		if !ok || v <= 0 || v >= 100 {
			return out, errors.New("benefit_value.percent must be 1..99")
		}
	default:
		return out, fmt.Errorf("invalid benefit_type: %s", req.BenefitType)
	}
	if req.MaxRedemptionsPerTenant <= 0 {
		req.MaxRedemptionsPerTenant = 1
	}
	benefitJSON, err := json.Marshal(req.BenefitValue)
	if err != nil {
		return out, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO promotion_campaigns (
			code, name, description, benefit_type, benefit_value,
			starts_at, ends_at, max_redemptions_per_tenant, total_budget_credits, status
		) VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,'draft')
		RETURNING id
	`, req.Code, req.Name, req.Description, req.BenefitType, string(benefitJSON),
		req.StartsAt, req.EndsAt, req.MaxRedemptionsPerTenant, req.TotalBudgetCredits,
	).Scan(&out.ID)
	if err != nil {
		return out, err
	}
	out.Code = req.Code
	out.Name = req.Name
	out.BenefitType = req.BenefitType
	out.Status = "draft"
	return out, nil
}

// SetCampaignStatus moves a campaign through draft → active → paused/ended.
func (s *Service) SetCampaignStatus(ctx context.Context, id int64, status string) error {
	if !s.Enabled() {
		return errors.New("maas service not enabled")
	}
	switch status {
	case "draft", "active", "paused", "ended":
	default:
		return fmt.Errorf("invalid status: %s", status)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE promotion_campaigns SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// -------------------------------------------------- discount at order ----

// discountRedemption is one pending discount to apply at order time.
type discountRedemption struct {
	RedemptionID int64
	Code         string
	Percent      int
}

// consumeDiscountRedemption picks the tenant's newest unconsumed discount
// redemption (order_id IS NULL) whose campaign is still active and inside
// its window, and locks it FOR UPDATE. Caller must run the returned choices
// inside the SAME transaction that inserts the order and stamps
// order_id — otherwise a crash between the two writes loses the discount.
func (s *Service) consumeDiscountRedemption(ctx context.Context, tx pgx.Tx, tenantID string) (*discountRedemption, error) {
	var r discountRedemption
	err := tx.QueryRow(ctx, `
		SELECT r.id, c.code, (c.benefit_value->>'percent')::int
		FROM promotion_redemptions r
		JOIN promotion_campaigns c ON c.id = r.campaign_id
		WHERE r.tenant_id = $1
		  AND r.order_id IS NULL
		  AND c.benefit_type = 'discount'
		  AND c.status = 'active'
		  AND (c.starts_at IS NULL OR c.starts_at <= now())
		  AND (c.ends_at IS NULL OR c.ends_at >= now())
		ORDER BY r.created_at DESC
		LIMIT 1
		FOR UPDATE OF r
	`, tenantID).Scan(&r.RedemptionID, &r.Code, &r.Percent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// applyTenantOrderDiscount wraps CreateOrder's order INSERT with discount
// consumption: when a pending discount redemption exists, the order amount is
// reduced by the campaign percent. Returns the (possibly reduced) amount,
// a note fragment, and the locked redemption id — the CALLER must stamp
// promotion_redemptions.order_id with the new order id in the same tx
// (the 937 column previously had zero consumers).
func (s *Service) applyTenantOrderDiscount(ctx context.Context, tx pgx.Tx, tenantID string, amountCents int) (int, string, int64, error) {
	d, err := s.consumeDiscountRedemption(ctx, tx, tenantID)
	if err != nil || d == nil {
		return amountCents, "", 0, err
	}
	discounted := amountCents * (100 - d.Percent) / 100
	note := fmt.Sprintf("campaign discount %s (-%d%%)", d.Code, d.Percent)
	return discounted, note, d.RedemptionID, nil
}
