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
    // ★ 2026-10-27（第 13 增量 · 语言包死键清理轮）：含占位符的键数实测由 **703** 降到 **467**
    //   （删掉的 3315 个死键里有 **236 个**带占位符）⇒ 原下限 500 已高于实测 ⇒ 门红。
    //   ⇒ 下限按新事实下调到 **400**：仍能挡住「提取器坏了、几乎没提取到占位符」这条假绿线。
    expect(report.keysWithPlaceholder, '几乎没提取到占位符 ⇒ 提取器可能坏了').toBeGreaterThanOrEqual(400)
  })

  it('★ 定点回归：同形状活键（2 个占位符 × 8 语种）', () => {
    // ★ 2026-10-21（R57 轮）：原「族1」钉的是 `customer.wizard.messages.deviceLimitBody`，
    //   而那个键住在**死命名空间** `customer.wizard.*` 里（三口径实测零消费者）
    //   ⇒ 属主拍板删除该命名空间后，这个键随之消失 ⇒ 原定点回归**失去了对象**
    //   （8 语种全空 ⇒ 门红）。
    //   ⇒ 换成一个**同形状（2 个占位符）且真被引用**的活键，保证这个形状仍被覆盖 ——
    //     否则「同形状的下一个键族」来了照样漏（LESSONS 2026-10-21
    //     「补一个能覆盖旧形状的门，比给每个旧形状手写定点回归更值」）。
    const values = allLocaleValues()
    const key = 'agentRegistryView.pager.pageInfo'
    const bad: string[] = []
    for (const loc of ALL_LOCALES) {
      const got = [...placeholderSet(values.get(loc)?.[key])].sort().join(',')
      if (got !== 'page,total') bad.push(`${loc}: 占位符=[${got}]，期望=[page,total]`)
    }
    expect(bad, `同形状活键回归（占位符被吞）：\n${bad.join('\n')}`).toEqual([])
  })

  it('★ 三个死命名空间已删除（钉住这次处置，防止被加回来）', () => {
    // 属主 2026-10-21 拍板：只删这三个点名命名空间，其余死键留给后续轮。
    // ⇒ 这里钉住「它们不在了」：否则有人重跑
    //   web/scripts/sync-sessions-locale-leaks.mjs 之类的脚本、或从别处拷回词条，
    //   就会把 1112 条死翻译悄悄加回来，而没有任何门会响。
    const values = allLocaleValues()
    const deadPrefixes = [
      'customer.wizard.',
      'sessions.promptInjectionCategories.',
      'sessions.audit.promptInjectionCategories.',
    ]
    const still: string[] = []
    for (const loc of ALL_LOCALES) {
      const v = values.get(loc) ?? {}
      for (const k of Object.keys(v)) {
        if (deadPrefixes.some((p) => k.startsWith(p))) still.push(`${loc}:${k}`)
      }
    }
    expect(still, `死命名空间被加回来了（${still.length} 条）：\n${still.slice(0, 10).join('\n')}`).toEqual([])
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
