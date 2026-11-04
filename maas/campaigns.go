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

	credits := int64(0)
	if benefitType == "credits" {
		var vv map[string]float64
		_ = json.Unmarshal(benefit, &vv)
		credits = int64(vv["credits"])
	}
	if credits <= 0 {
		return out, fmt.Errorf("campaign %s carries no credit benefit", code)
	}
	if totalBudget > 0 && grantedCredits+credits > totalBudget {
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
	if err := s.writeLedgerWithBalanceDetail(ctx, tx, tenantID, "adjust", credits, balance,
		string(PoolGranted), "campaign", fmt.Sprintf("%d", campID),
		fmt.Sprintf("campaign %s", campName), ownerUser, campID,
		detail); err != nil {
		return out, err
	}
	var redID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO promotion_redemptions (campaign_id, tenant_id, owner_user, benefit, credits_granted)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, campID, tenantID, ownerUser, benefit, credits).Scan(&redID); err != nil {
		return out, err
	}
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
