package maas

// marketing_admin_test.go — 2026-11-09（运营审计轮项4）判据族。
//
// ① checkEffectiveWindow 纯函数三态；② 下单/兑现两条链的 SQL 形态钉
// （934 商业列此前零消费 —— 形态漂移即红）；③ discount 下单消费链的
// 事务形状（锁 redemption → 建单 → 回填 order_id 必须同事务）。

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCheckEffectiveWindow(t *testing.T) {
	now := time.Now()
	future := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)

	if err := checkEffectiveWindow(nil, nil, "plan"); err != nil {
		t.Fatalf("no window must pass: %v", err)
	}
	if err := checkEffectiveWindow(&future, nil, "plan"); err == nil {
		t.Fatalf("not-yet-on-sale must be rejected")
	}
	if err := checkEffectiveWindow(&past, nil, "plan"); err != nil {
		t.Fatalf("already-on-sale must pass: %v", err)
	}
	if err := checkEffectiveWindow(nil, &past, "package"); err == nil {
		t.Fatalf("sales-ended must be rejected")
	}
	if err := checkEffectiveWindow(nil, &future, "package"); err != nil {
		t.Fatalf("still-on-sale must pass: %v", err)
	}
}

// TestCreateOrderCarriesCommerceColumns 钉 CreateOrder 的下单校验形状：
// 生效窗两列 + 加油包 bonus/first_time_only 必须在 SELECT 里，且
// first_time_only 有 paid 订单守卫（934 三列此前零消费）。
func TestCreateOrderCarriesCommerceColumns(t *testing.T) {
	src, err := os.ReadFile("orders.go")
	if err != nil {
		t.Fatalf("read orders.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"SELECT name, price_cents, monthly_credits, enabled, effective_from, effective_to",
		"SELECT name, price_cents, credits_amount, enabled,",
		"COALESCE(bonus_credits, 0)",
		"COALESCE(first_time_only, FALSE)",
		"checkEffectiveWindow(effFrom, effTo, \"plan\")",
		"checkEffectiveWindow(effFrom, effTo, \"package\")",
		"package is limited to one purchase per tenant",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("orders.go missing %q", want)
		}
	}
	// 确认时补发 bonus：单独一条 ledger（ref_type=order_bonus）。
	if !strings.Contains(body, `"order_bonus"`) {
		t.Fatalf("ConfirmOrder must grant bonus credits as a separate order_bonus ledger entry")
	}
	if !strings.Contains(body, "SELECT COALESCE(bonus_credits, 0) FROM topup_packages WHERE id = $1") {
		t.Fatalf("bonus must be re-read at confirm time (package definition may change between create and confirm)")
	}
}

// TestCampaignBenefitKindsImplemented 钉 gift/discount 两类 benefit 的实现
// 形态（937 闭集里此前 Go 拒收）。
func TestCampaignBenefitKindsImplemented(t *testing.T) {
	src, err := os.ReadFile("campaigns.go")
	if err != nil {
		t.Fatalf("read campaigns.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		`case "gift":`,
		`case "discount":`,
		"discountPercent",
		"campaign %s carries invalid discount percent",
		"campaign %s (gift)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("campaigns.go missing %q", want)
		}
	}
	// discount 兑现必须零积分（credits_granted=0，order_id 留 NULL 由下单消费）。
	if !strings.Contains(body, "VALUES ($1, $2, $3, $4, 0)") {
		t.Fatalf("discount redemption must record zero credits")
	}
}

// TestOrderDiscountConsumptionAtomic 钉折扣消费的事务形状：
// 锁 redemption → 建单 → 回填 order_id 必须在同一事务（CreateOrder 的
// INSERT 必须走 tx 而不是 pool），且 QR 在折后金额上生成。
func TestOrderDiscountConsumptionAtomic(t *testing.T) {
	src, err := os.ReadFile("orders.go")
	if err != nil {
		t.Fatalf("read orders.go: %v", err)
	}
	body := string(src)
	aidx := strings.Index(body, "func (s *Service) CreateOrder")
	if aidx < 0 {
		t.Fatalf("CreateOrder not found")
	}
	fn := body[aidx : aidx+strings.Index(body[aidx+1:], "\nfunc ")+1]
	if !strings.Contains(fn, "applyTenantOrderDiscount") {
		t.Fatalf("CreateOrder must consult pending discount redemptions")
	}
	insertIdx := strings.Index(fn, "INSERT INTO billing_orders")
	beginIdx := strings.Index(fn, "s.pool.Begin(ctx)")
	if insertIdx < 0 || beginIdx < 0 || beginIdx > insertIdx {
		t.Fatalf("order INSERT must run inside the discount transaction (Begin before INSERT)")
	}
	if !strings.Contains(fn, "UPDATE promotion_redemptions SET order_id = $2") {
		t.Fatalf("discount redemption must be stamped with the order id in the same tx")
	}
	if strings.Index(fn, "GenerateQR(orderNo, amountCents, channel)") < strings.Index(fn, "applyTenantOrderDiscount") {
		t.Fatalf("QR must be generated AFTER the discount is applied (amount reduced)")
	}
}

// TestMarketingAdminValidationShapes 钉管理面校验（benefit_value 形状与
// status 闭集）。
func TestMarketingAdminValidationShapes(t *testing.T) {
	src, err := os.ReadFile("marketing_admin.go")
	if err != nil {
		t.Fatalf("read marketing_admin.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"benefit_value.percent must be 1..99",
		"benefit_value.credits must be a positive number",
		`case "draft", "active", "paused", "ended":`,
		"status must be active or disabled",
		"FOR UPDATE OF r",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marketing_admin.go missing %q", want)
		}
	}
}
