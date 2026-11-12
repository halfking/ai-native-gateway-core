// quoteQuickFill.ts — 三档快填的法定币→credits 换算（2026-11-12 复审审计
// P2-2 收口：此前把法定币价直接写进 credits 轴，差 ~4 个数量级）。
//
// 换算链 = 后端 precomputeLines 收入算术的**精确逆**（sales/quote.go）：
//   revenue_credits × cents_per_credit / 100 = revenue_cny
//   revenue_usd = revenue_cny / fx
// ⇒ credits = cost_usd × coef × fx × 100 / cents_per_credit
// ⇒ coef 倍加成的毛利率恰为 1 − 1/coef（快填意图与红线判定同构）。
//
// fp 舍入：先 round 到 1e-6 再向上取整到分位（P3-4：3.0×1.6 直接 ceil 的
// 浮点表示误差会得多进一分，恒向客户不利方向偏）。

export interface TierQuickFillHints {
  fxRate: number
  centsPerCredit: number
}

/** 服务端 fallback（R72f：与 settings spec 默认值逐字对齐）。 */
export const QUICK_FILL_FALLBACK: TierQuickFillHints = { fxRate: 7.2, centsPerCredit: 0.01 }

export function tierQuickFillCredits(
  cost: number,
  currency: string,
  coef: number,
  hints: TierQuickFillHints,
): number {
  const costUsd = currency === 'CNY' ? cost / hints.fxRate : cost
  const credits = (costUsd * coef * hints.fxRate * 100) / hints.centsPerCredit
  return Math.ceil(Math.round(credits * 1e6) / 1e6)
}
