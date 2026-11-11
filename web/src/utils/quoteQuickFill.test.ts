// quoteQuickFill.test.ts — 三档快填单位换算判据（2026-11-12 复审审计 P2-2/P3-4）。
import { describe, expect, it } from 'vitest'
import { QUICK_FILL_FALLBACK, tierQuickFillCredits } from './quoteQuickFill'

describe('tierQuickFillCredits', () => {
  it('USD 成本 × 系数换算到 credits 轴（fx 与 cents 的量纲链精确闭合）', () => {
    // 1.0 USD × 1.6 ⇒ 1.6 USD ⇒ ×7.2 = 11.52 CNY = 1152 分 ⇒ ÷0.01 = 115200 credits
    expect(tierQuickFillCredits(1.0, 'USD', 1.6, QUICK_FILL_FALLBACK)).toBe(115200)
    // coef 倍加成 ⇒ 毛利率恰为 1 − 1/coef（与红线判定同构）
    const c = 1.0
    const rate = tierQuickFillCredits(c, 'USD', 1.6, QUICK_FILL_FALLBACK)
    const margin = (rate - c * 1.6 * QUICK_FILL_FALLBACK.fxRate * 100 / QUICK_FILL_FALLBACK.centsPerCredit)
    expect(Math.abs(margin)).toBeLessThan(1e-6) // fp 比较用容差（额面值恒等）
  })
  it('CNY 成本走 c/fx→usd 链（fx 两端相消 ⇒ 只剩 cents）', () => {
    // 7.2 CNY = 1 USD × 1.6 ⇒ 115200 credits（与 1.0 USD × 1.6 同值）
    expect(tierQuickFillCredits(7.2, 'CNY', 1.6, QUICK_FILL_FALLBACK)).toBe(115200)
  })
  it('P3-4：fp 表示误差不得多进一分（3.0×1.6=4.799…⇒ 恰 4.80 对应 credits）', () => {
    // 3.0 USD × 1.6 = 4.8 USD ⇒ 4.8×7.2×100/0.01 = 345600（不是 345600.000…1 ceil 出的下一档）
    expect(tierQuickFillCredits(3.0, 'USD', 1.6, QUICK_FILL_FALLBACK)).toBe(345600)
    expect(tierQuickFillCredits(1.5, 'USD', 1.3, QUICK_FILL_FALLBACK)).toBe(140400) // 1.95 USD
    expect(tierQuickFillCredits(1.1, 'USD', 1.6, QUICK_FILL_FALLBACK)).toBe(126720) // 1.76 USD
  })
})
