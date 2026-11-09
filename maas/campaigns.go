package maas

// W3-T3 / W6-T4（2026-11-04 运营管理与价格计费体系，总体设计 §2.4-3/§2.5）：
// 活动目录的租户可见面 + 兑现。红线：活动额度兑现必须双写 ——
// promotion_redemptions 一行 + credit_ledger.campaign_id 一行（granted 池），
// 活动成本 = SUM(credit_ledger.amount) WHERE campaign_id 非空，与
// redemptions.credits_granted 双向对账。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Campaign is the tenant-visible shape of promotion_campaigns (937) —
// active rows only, budget internals withheld.
type Campaign struct {
	ID                      int64          `json:"id"`
	Code                    string         `json:"code"`
	Name                    string         `json:"name"`
	Description             string         `json:"description"`
	BenefitType             string         `json:"benefit_type"`
	BenefitValue            map[string]any `json:"benefit_value"`
	StartsAt                *time.Time     `json:"starts_at,omitempty"`
	EndsAt                  *time.Time     `json:"ends_at,omitempty"`
	MaxRedemptionsPerTenant int            `json:"max_redemptions_per_tenant"`
}

// ListActiveCampaigns returns currently-redeemable campaigns (status=active
// and inside the start/end window when set). Tenant read path filters in API.
func (s *Service) ListActiveCampaigns(ctx context.Context) ([]Campaign, error) {
	out := []Campaign{}
	if !s.Enabled() {
		return out, fmt.Errorf("maas service not enabled")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, code, name, description, benefit_type, benefit_value,
		       starts_at, ends_at, max_redemptions_per_tenant
		FROM promotion_campaigns
		WHERE status = 'active'
		  AND (starts_at IS NULL OR starts_at <= now())
		  AND (ends_at IS NULL OR ends_at >= now())
		ORDER BY id DESC
		LIMIT 100
	`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Campaign
		var benefit []byte
		if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.Description, &c.BenefitType,
			&benefit, &c.StartsAt, &c.EndsAt, &c.MaxRedemptionsPerTenant); err != nil {
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

// campaignDetailJSON 构造写入 credit_ledger.detail 的 JSON。
//
// ★ 必须走 json.Marshal，不能手拼字符串：手拼
//
//	`{"campaign_code":"`+code+`"}` 时，code 里只要有一个 `"` 或 `\`
//	就产出**非法 JSON** ⇒ 整条 INSERT 被驱动拒（jsonb 解析失败）⇒
//	事务回滚 ⇒ 兑现整体失败。code 是管理员可写的活动编码 ⇒ 不是
//	不可达分支。（手拼不是 SQL 注入——参数是绑定的——但它是数据转换
//	缺陷。）
func campaignDetailJSON(code string) ([]byte, error) {
	return json.Marshal(map[string]string{"campaign_code": code})
}

// CampaignRedemption is the result of redeeming one campaign.
type CampaignRedemption struct {
	RedemptionID   int64  `json:"redemption_id"`
	CampaignID     int64  `json:"campaign_id"`
	CampaignCode   string `json:"campaign_code"`
	TenantID       string `json:"tenant_id"`
	OwnerUser      string `json:"owner_user"`
	CreditsGranted int64  `json:"credits_granted"`
}

// RedeemCampaign grants a campaign's credit benefit once per tenant
// (max_redemptions_per_tenant enforced), inside one transaction with the
// ledger double-write (campaign_id carried into credit_ledger).
func (s *Service) RedeemCampaign(ctx context.Context, code, tenantID, ownerUser string) (CampaignRedemption, error) {
	out := CampaignRedemption{}
	if !s.Enabled() {
		return out, fmt.Errorf("maas service not enabled")
	}
	if code == "" || tenantID == "" {
		return out, fmt.Errorf("code and tenant required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort rollback

	var campID int64
	var campName, benefitType string
	var benefit []byte
	var maxPerTenant int
	var totalBudget, grantedCredits int64
	err = tx.QueryRow(ctx, `
		SELECT id, name, benefit_type, benefit_value, max_redemptions_per_tenant,
		       total_budget_credits, granted_credits
		FROM promotion_campaigns
		WHERE code = $1 AND status = 'active'
		  AND (starts_at IS NULL OR starts_at <= now())
		  AND (ends_at IS NULL OR ends_at >= now())
		FOR UPDATE
	`, code).Scan(&campID, &campName, &benefitType, &benefit, &maxPerTenant, &totalBudget, &grantedCredits)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("campaign not active: %s", code)
	}
	if err != nil {
		return out, err
	}

	// 2026-11-09（运营审计轮项4）：benefit_type 三态实现 ——
	//   credits/gift ⇒ 即时发积分（gift 仅文案/来源语义不同，台账 note 标注）；
	//   discount    ⇒ 记一次「未使用折扣」，下一张订单结账时抵扣
	//                 （promotion_redemptions.order_id 此前零消费，正是挂单位）。
	var credits int64
	var discountPercent int
	var benefitKind string
	switch benefitType {
	case "credits":
		benefitKind = "credits"
		var vv map[string]float64
		_ = json.Unmarshal(benefit, &vv)
		credits = int64(vv["credits"])
	case "gift":
		benefitKind = "gift"
		var vv map[string]float64
		_ = json.Unmarshal(benefit, &vv)
		credits = int64(vv["credits"])
	case "discount":
		benefitKind = "discount"
		var vv map[string]float64
		_ = json.Unmarshal(benefit, &vv)
		discountPercent = int(vv["percent"])
		if discountPercent <= 0 || discountPercent >= 100 {
			return out, fmt.Errorf("campaign %s carries invalid discount percent", code)
		}
	default:
		return out, fmt.Errorf("campaign %s carries unknown benefit type %s", code, benefitType)
	}
	if benefitKind != "discount" && credits <= 0 {
		return out, fmt.Errorf("campaign %s carries no credit benefit", code)
	}
	if benefitKind != "discount" && (totalBudget < 0 || grantedCredits < 0) {
		return out, fmt.Errorf("campaign %s carries invalid budget accounting", code)
	}
	if benefitKind != "discount" && totalBudget > 0 && credits > totalBudget-grantedCredits {
		return out, fmt.Errorf("campaign %s budget exhausted", code)
	}

	// 每租户兑现次数上限。
	var redeemed int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM promotion_redemptions WHERE campaign_id = $1 AND tenant_id = $2
	`, campID, tenantID).Scan(&redeemed); err != nil {
		return out, err
	}
	if maxPerTenant > 0 && redeemed >= maxPerTenant {
		return out, fmt.Errorf("campaign %s already redeemed for this tenant", code)
	}

	var redID int64
	if benefitKind == "discount" {
		// 折扣兑现：不发积分、不动钱包；只记 redemption（order_id 保持 NULL
		// ⇒ 下一张订单结账时消费，见 consumeDiscountRedemption）。
		if err := tx.QueryRow(ctx, `
			INSERT INTO promotion_redemptions (campaign_id, tenant_id, owner_user, benefit, credits_granted)
			VALUES ($1, $2, $3, $4, 0)
			RETURNING id
		`, campID, tenantID, ownerUser, benefit).Scan(&redID); err != nil {
			return out, err
		}
		if err := tx.Commit(ctx); err != nil {
			return out, err
		}
		return CampaignRedemption{
			RedemptionID: redID, CampaignID: campID, CampaignCode: code,
			TenantID: tenantID, OwnerUser: ownerUser, CreditsGranted: 0,
		}, nil
	}

	if err := s.ensureWallet(ctx, tx, tenantID); err != nil {
		return out, err
	}
	// 钱包入账（granted 池）。
	var balance int64
	if err := tx.QueryRow(ctx, `
		UPDATE tenant_credit_wallets
		   SET granted_balance = granted_balance + $2::bigint,
		       balance_credits = granted_balance + purchased_balance + $2::bigint,
		       updated_at = now()
		 WHERE tenant_id = $1
		RETURNING balance_credits
	`, tenantID, credits).Scan(&balance); err != nil {
		return out, err
	}
	// ★ 双写：ledger 带 campaign_id（活动成本口径的另一半）。
	detail, err := campaignDetailJSON(code)
	if err != nil {
		return out, err
	}
	if benefitKind == "gift" {
		detail, err = json.Marshal(map[string]string{"campaign_code": code, "benefit": "gift"})
		if err != nil {
			return out, err
		}
	}
	ledgerNote := fmt.Sprintf("campaign %s", campName)
	if benefitKind == "gift" {
		ledgerNote = fmt.Sprintf("campaign %s (gift)", campName)
	}
	if err := s.writeLedgerWithBalanceDetail(ctx, tx, tenantID, "adjust", credits, balance,
		string(PoolGranted), "campaign", fmt.Sprintf("%d", campID),
		ledgerNote, ownerUser, campID,
		detail); err != nil {
		return out, err
	}
	var redID2 int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO promotion_redemptions (campaign_id, tenant_id, owner_user, benefit, credits_granted)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, campID, tenantID, ownerUser, benefit, credits).Scan(&redID2); err != nil {
		return out, err
	}
	redID = redID2
	if _, err := tx.Exec(ctx, `
		UPDATE promotion_campaigns SET granted_credits = granted_credits + $2, updated_at = now() WHERE id = $1
	`, campID, credits); err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	return CampaignRedemption{
		RedemptionID: redID, CampaignID: campID, CampaignCode: code,
		TenantID: tenantID, OwnerUser: ownerUser, CreditsGranted: credits,
	}, nil
}
