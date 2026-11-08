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
		{Key: "sales.quote.approval_rate_alert_pct", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1), Max: floatPtr(100), Default: 40, DangerLevel: Safe, HotReload: true, Description: "审批触发率告警线（%）", DescriptionLong: "DealHub 突破率自校准：进入人审的报价占已提交报价的比例超过该线 ⇒ 看板告警（阈值定错的信号，不是门）。消费方 = GET /api/admin/sales/dashboard（2026-11-08 看板轮接线，不再是占位键）。", Unit: "%"},
		{Key: "sales.quote.default_valid_days", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1), Max: floatPtr(365), Default: 14, DangerLevel: Safe, HotReload: true, Description: "报价默认有效期（天）", DescriptionLong: "创建报价未显式指定 valid_until 时的默认有效期天数（风暴档 §8-7 建议 14 天）。Expired 是读取时派生态，本值只决定写入快照的 valid_until。", Unit: "天"},
		{Key: "sales.quote.cost_drift_alert_pct", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(0), Max: floatPtr(1000), Default: 10, DangerLevel: Safe, HotReload: true, Description: "成本漂移提醒线（%）", DescriptionLong: "活报价单（draft/pending/approved/sent）的版本行成本快照相对供给视图当前价的最大漂移超过该值 ⇒ 清单标「成本已漂移，建议重报」。提醒不自动撤（风暴档 §3.4：MVP 形态 = 提醒）。0 = 关闭漂移标记。", Unit: "%"},
		{Key: "sales.quote.tier_coef_aggressive", Type: TypeFloat, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1.01), Max: floatPtr(100), Default: 1.3, DangerLevel: Safe, HotReload: true, Description: "激进档报价系数（成本 ×）", DescriptionLong: "三档一键报价（风暴档 §8-5）：激进档零售价 = 成本 × 该系数（P2 市场位势同步器落地前的固定加成起步值）。仅前端快填用，服务端不校验零售价与系数的关系（销售可改价，红线在审批）。", Unit: "×"},
		{Key: "sales.quote.tier_coef_standard", Type: TypeFloat, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1.01), Max: floatPtr(100), Default: 1.6, DangerLevel: Safe, HotReload: true, Description: "标准档报价系数（成本 ×）", DescriptionLong: "三档一键报价（风暴档 §8-5）：标准档零售价 = 成本 × 该系数。仅前端快填用，服务端不校验零售价与系数的关系。", Unit: "×"},
		{Key: "sales.quote.tier_coef_conservative", Type: TypeFloat, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1.01), Max: floatPtr(100), Default: 2.0, DangerLevel: Safe, HotReload: true, Description: "保守档报价系数（成本 ×）", DescriptionLong: "三档一键报价（风暴档 §8-5）：保守档零售价 = 成本 × 该系数。仅前端快填用，服务端不校验零售价与系数的关系。", Unit: "×"},
		{Key: "sales.public.rate_limit_per_min", Type: TypeInt, Scope: ScopePlatform, Category: CategorySales, Min: floatPtr(1), Max: floatPtr(600), Default: 30, DangerLevel: Safe, HotReload: true, Description: "公开报价页每分钟限速（次/IP）", DescriptionLong: "/api/public/quote/{token} 匿名面的 per-IP 固定窗限速（2026-11-08 审计 P2-2：此前注释声称限速但实现为零）。超限返回 429。", Unit: "次/分"},
	}
}
