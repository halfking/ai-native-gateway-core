// localePlaceholderParity.test.ts — 「同一键、不同语种，占位符不一致」的门
//   （2026-10-21 批判式审计轮新增）
//
// 补的是 i18n 四条既有门合起来的缝（见 localePlaceholderParity.ts 头注释）：
//   parity（只比键集合）/ keys_referenced（只查键存在）/ 硬编码中文棘轮（排除 locales）
//   / localeValueQuality（只管值的语言）⇒ 占位符被某个语种吞掉，一条都拦不住。
//
// ⚠️ 本门**不是**「所有调用点参数都被正确显示」的声明：
//   已知盲区「所有语种一致但都缺调用点要的那个占位符」本门抓不到，
//   2026-10-21 实测本仓当前 0 实例（解法见 localePlaceholderParity.ts 头注释）。

import { describe, expect, it } from 'vitest'
import { ALL_LOCALES, allLocaleValues } from './localeValueQuality'
import {
  assertSelfCheckParity,
  measureRepoParity,
  placeholderSet,
} from './localePlaceholderParity'

// ⚠️ 超时给到 120s：门要加载 8 个语种的 locale 模块（vm 求值），实测单次 20~40s。
//   两道门共享 localeValueQuality 的值表缓存 ⇒ 同一进程里只加载一次。
describe('i18n 占位符平价（同一键、跨语种）', { timeout: 120_000 }, () => {
  it('★ 量具自证（双向：会触发 + 不误触发）', () => {
    const cases = assertSelfCheckParity()
    expect(cases.length).toBeGreaterThanOrEqual(12)
    expect(cases.filter((c) => !c.ok)).toEqual([])
  })

  it('★ 全仓不得存在「同键、占位符不一致」的语种（硬闸 0）', () => {
    const report = measureRepoParity()
    const detail = report.violations
      .map((v) => {
        const groups = v.groups.map((g) => `      [${g.signature}] ← ${g.locales.join(', ')}`).join('\n')
        return `  ${v.key}\n${groups}`
      })
      .join('\n')
    expect(report.violations, `占位符不一致的键（${report.violations.length} 个）：\n${detail}`).toEqual([])
    expect(report.violations.length).toBe(0)
  })

  it('★ 非空下限：门必须真比了足够多的键与占位符（防空过假绿）', () => {
    const report = measureRepoParity()
    // 语种数：8
    expect(report.locales.length).toBe(ALL_LOCALES.length)
    // 参与比较的键数：修复前实测 10041 ⇒ 下限取 5000（远低于实测，
    // 但足以挡住「加载器坏了、只比了几个键」这类假绿）
    expect(report.comparedKeys, '参与比较的键数异常少 ⇒ 量具可能没加载全').toBeGreaterThanOrEqual(5000)
    // 含占位符的键数：修复前实测各语种 672~721 ⇒ 下限取 500
    expect(report.keysWithPlaceholder, '几乎没提取到占位符 ⇒ 提取器可能坏了').toBeGreaterThanOrEqual(500)
  })

  it('★ 定点回归：族1 deviceLimitBody 的 8 个语种都带 {active}/{max}', () => {
    const values = allLocaleValues()
    const key = 'customer.wizard.messages.deviceLimitBody'
    const bad: string[] = []
    for (const loc of ALL_LOCALES) {
      const got = [...placeholderSet(values.get(loc)?.[key])].sort().join(',')
      if (got !== 'active,max') bad.push(`${loc}: 占位符=[${got}]，期望=[active,max]`)
    }
    expect(bad, `族1 回归（占位符被吞）：\n${bad.join('\n')}`).toEqual([])
  })

  it('★ 定点回归：族2 rowResult.running 的 8 个语种都带 {op}', () => {
    const values = allLocaleValues()
    const key = 'dataLifecycle.storageOverview.rowResult.running'
    const bad: string[] = []
    for (const loc of ALL_LOCALES) {
      const got = [...placeholderSet(values.get(loc)?.[key])].sort().join(',')
      if (got !== 'op') bad.push(`${loc}: 占位符=[${got}]，期望=[op]`)
    }
    expect(bad, `族2 回归（调用点传了 {op} 却没有语种显示）：\n${bad.join('\n')}`).toEqual([])
  })
})
