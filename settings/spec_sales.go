package settings

// spec_sales.go —— 报价系统 MVP 红线数值（2026-11-04 实施规划 W1-T2，
// 决议 B DealHub 三维阈值 + 待裁决默认值）。
//
// ★ R72f 原则：注册键的 Default 必须等于读方 fallback，保证「kv 无行时」
//
//	注册前后行为一致。读方 = sales/quote.go 的 evaluateRedLines /
//	defaultValidUntil（settings.GetPlatformInt）。
//
// ★ 数值全部是「待裁决建议值」（实施规划 §7-9/10/11 + 风暴档 §8-1/7），
//
//	运营改 settings_kv 即生效（HotReload），无需发版。
func SalesSpecs() []*Spec {
	return []*Spec{
		{Key: "sales.quote.margin_floor_pct", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(0), Max: floatPtr(100), Default: 30, DangerLevel: Warning, HotReload: true, Description: "报价毛利下限（%）", DescriptionLong: "DealHub 三维红线的毛利下限维：报价版本整体毛利率低于该值 ⇒ 触发人审（pending_approval）。0 = 关闭该维。突破率自校准：触发率过高（告警线见 sales.quote.approval_rate_alert_pct）说明该阈值定错。", Unit: "%"},
		{Key: "sales.quote.discount_depth_pct", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(0), Max: floatPtr(100), Default: 10, DangerLevel: Warning, HotReload: true, Description: "报价折扣深度触审线（%）", DescriptionLong: "DealHub 三维红线的折扣深度维：行折扣率（discount_pct）高于该值 ⇒ 触发人审。0 = 关闭该维（任意折扣都不审）。", Unit: "%"},
		{Key: "sales.quote.order_amount_usd", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(0), Max: floatPtr(1000000), Default: 500, DangerLevel: Safe, HotReload: true, Description: "单笔金额触审线（USD）", DescriptionLong: "DealHub 三维红线的单笔金额维：报价版本销售总额（USD 折算）不低于该值 ⇒ 触发人审。0 = 关闭该维。", Unit: "USD"},
		{Key: "sales.quote.approval_rate_alert_pct", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1), Max: floatPtr(100), Default: 40, DangerLevel: Safe, HotReload: true, Description: "审批触发率告警线（%）", DescriptionLong: "DealHub 突破率自校准：进入人审的报价占已提交报价的比例超过该线 ⇒ 看板告警（阈值定错的信号，不是门）。看板轮落地前该键仅登记占位。", Unit: "%"},
		{Key: "sales.quote.default_valid_days", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1), Max: floatPtr(365), Default: 14, DangerLevel: Safe, HotReload: true, Description: "报价默认有效期（天）", DescriptionLong: "创建报价未显式指定 valid_until 时的默认有效期天数（风暴档 §8-7 建议 14 天）。Expired 是读取时派生态，本值只决定写入快照的 valid_until。", Unit: "天"},
	}
}
