package maas

// W3-T1 / W6-T4（2026-11-04 运营管理与价格计费体系，总体设计 §2.4-3/§2.5）：
// 活动额度的**双向对账**执行器。
//
// ★ 这个文件为什么存在（12h 审计轮第 4 号发现）：
//
//	campaigns.go 的文件头把「双向对账」写成了一条**红线**：
//
//	  「活动额度兑现必须双写 —— promotion_redemptions 一行 +
//	   credit_ledger.campaign_id 一行（granted 池）；活动成本 =
//	   SUM(credit_ledger.amount) WHERE campaign_id 非空，与
//	   redemptions.credits_granted 双向对账。」
//
//	⇒ 写侧闭环成立（双写在**同一个事务**里，见 RedeemCampaign）。
//	⇒ **读侧在整个仓内不存在**：没有任何代码算出「活动成本」并与
//	   redemptions.credits_granted 对账 ⇒ 红线只落在注释里。
//
// ★ 而且红线里给的对账口径**读错了表**（这是本仓已修过一次的同型缺陷）：
//
//	写入目标是 credit_ledger_hot（独立表，不是 credit_ledger 的分区）；
//	credit_ledger 是**分区父表**，hot 行在被 promote_credit_ledger_hot_to_partition
//	提升之前**对父表不可见** ⇒ 按红线写 `SUM(credit_ledger.amount)` 会
//	**系统性漏计**提升滞后窗口内的活动额度。
//	⇒ 同型缺陷已在 consume 侧被修过一次（AF-3，2026-11-03 审计轮，
//	  见 reconcile.go 的注释）⇒ 本轮在 campaign 侧再复现一次。
//
// ⇒ 正解口径：**读视图 credit_ledger_with_current_month**（hot ∪ 分区，
//   带 campaign_id）—— 这正是 AF-3 修法所选的同一个视图。

import (
	"context"
	"fmt"
	"strings"
)

// CampaignReconciliation 是单个活动的「兑现台账 × 账本」对账快照。
type CampaignReconciliation struct {
	CampaignID   int64  `json:"campaign_id"`
	CampaignCode string `json:"campaign_code"`
	// RedemptionCount 是该活动的兑现笔数。
	RedemptionCount int64 `json:"redemption_count"`
	// RedemptionCredits = SUM(promotion_redemptions.credits_granted)。
	RedemptionCredits int64 `json:"redemption_credits"`
	// LedgerCredits = SUM(账本 amount WHERE campaign_id = 本活动)
	// —— **两臂并读**（hot ∪ 分区）⇒ 与提升滞后窗口无关。
	LedgerCredits int64 `json:"ledger_credits"`
	// GrantedCredits = promotion_campaigns.granted_credits（兑现时累加的计数）。
	GrantedCredits int64 `json:"granted_credits"`
	// Matched 判据：台账侧（redemptions）与账本侧（ledger）额度相等。
	Matched bool `json:"matched"`
}

// campaignReconcileSQL 是对账查询本体（导出成常量 ⇒ 可被测试直接钉住口径）。
//
// ★ ★ 口径护栏：账本侧读的是 **credit_ledger_with_current_month**
//
//	（hot ∪ 分区并读视图）——**不是** credit_ledger 父表。写入口是
//	credit_ledger_hot（独立表），父表在 promote 之前看不到 hot 行 ⇒
//	读父表会系统性漏计提升滞后窗口内的活动额度（AF-3 同型缺陷）。
const campaignReconcileSQL = `
		SELECT c.id,
		       c.code,
		       COALESCE(r.redeemed_count, 0)::bigint,
		       COALESCE(r.redemption_credits, 0)::bigint,
		       COALESCE(l.ledger_credits, 0)::bigint,
		       c.granted_credits
		  FROM promotion_campaigns c
		  LEFT JOIN (
		        SELECT campaign_id,
		               count(*)::bigint                AS redeemed_count,
		               COALESCE(SUM(credits_granted), 0)::bigint AS redemption_credits
		          FROM promotion_redemptions
		         GROUP BY campaign_id
		       ) r ON r.campaign_id = c.id
		  LEFT JOIN (
		        SELECT campaign_id,
		               COALESCE(SUM(amount), 0)::bigint AS ledger_credits
		          FROM credit_ledger_with_current_month
		         WHERE campaign_id IS NOT NULL
		         GROUP BY campaign_id
		       ) l ON l.campaign_id = c.id
		 WHERE c.status <> 'draft'`

// ReconcileCampaigns 对账活动额度。
//
// code 非空 ⇒ 只对账该活动；code 为空 ⇒ 对账全活动目录（有活动的）。
//
// ★ 对账读的是 **credit_ledger_with_current_month**（hot ∪ 分区并读视图），
//
//	不是 credit_ledger 父表 —— 否则提升滞后窗口内的行会漏计
//	（AF-3 同型缺陷，见文件头）。
func (s *Service) ReconcileCampaigns(ctx context.Context, code string) ([]CampaignReconciliation, error) {
	out := []CampaignReconciliation{}
	if !s.Enabled() {
		return out, fmt.Errorf("maas service not enabled")
	}
	code = strings.TrimSpace(code)

	args := []any{}
	filter := ""
	if code != "" {
		args = append(args, code)
		filter = " AND c.code = $1"
	}

	// 一条 SQL 同时取三侧 ⇒ 同一快照点，避免两侧读到不同时刻。
	//  ① 台账侧：SUM(promotion_redemptions.credits_granted) + 笔数
	//  ② 账本侧：SUM(两臂并读视图 amount) WHERE campaign_id = c.id
	//  ③ 计数侧：c.granted_credits
	//
	// ★ 左连接用 COALESCE 兜 0 ⇒ 无兑现记录的活动也在结果里（值为 0）。
	rows, err := s.pool.Query(ctx, campaignReconcileSQL+filter+`
		 ORDER BY c.id DESC`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var rec CampaignReconciliation
		if err := rows.Scan(&rec.CampaignID, &rec.CampaignCode,
			&rec.RedemptionCount, &rec.RedemptionCredits,
			&rec.LedgerCredits, &rec.GrantedCredits); err != nil {
			return out, err
		}
		rec.Matched = rec.RedemptionCredits == rec.LedgerCredits
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// CampaignsBalanced 判据（供测试与门使用）：全部活动对账通过。
//
// ★ 口径：只判「台账侧 == 账本侧」。granted_credits 是兑现时在活动行上
//
//	累加的计数，它与前两者的关系是「同一笔兑现的三个影子」，任一不等
//	都说明写侧漏了或重复了 ⇒ 三者全等才算平。
func CampaignsBalanced(recs []CampaignReconciliation) bool {
	for _, r := range recs {
		if !r.Matched {
			return false
		}
		if r.RedemptionCredits != r.GrantedCredits {
			return false
		}
	}
	return true
}
