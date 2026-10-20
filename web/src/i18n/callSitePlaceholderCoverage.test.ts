// callSitePlaceholderCoverage.test.ts — 「调用点传的参数有没有被任何语种显示出来」的门
//   （2026-10-21 审计轮新增）
//
// 补的是 i18n 既有门合起来的又一条盲区（见 callSitePlaceholderCoverage.ts 头注释）：
//   parity（只比键集合）/ keys_referenced（只查键存在）/ 硬编码中文棘轮（排除 locales）
//   / localeValueQuality（只管值的语言）/ localePlaceholderParity（只管**跨语种平不平**）
//   ⇒ 「**所有语种一致、但都缺**调用点要的那个占位符」一条都拦不住。
//
// ⚠️ 本门**不重复**平价门的职责：
//   平价门管「有的语种显示、有的不显示」；本门管「**所有语种都不显示**」。
//   两门互补，任缺一条都会漏掉一族缺陷。

import { describe, expect, it } from 'vitest'
import { relative } from 'node:path'
import { ALL_LOCALES, WEB_ROOT, allLocaleValues } from './localeValueQuality'
import { placeholderSet } from './localePlaceholderParity'
import {
  UNRESOLVED_REGISTRY,
  assertSelfCheckCoverage,
  measureRepoCoverage,
} from './callSitePlaceholderCoverage'

// ⚠️ 超时给到 120s：门要加载 8 个语种的 locale 模块（vm 求值），
//   实测单次 20~40s（高负载机器上更久）。
describe('i18n 调用点参数 → 占位符覆盖（所有语种都不显示 = 死参数）', { timeout: 120_000 }, () => {
  it('★ 量具自证（双向：会触发 + 不误触发）', () => {
    const cases = assertSelfCheckCoverage()
    // 提取器 6 条 + 判定 10 条 + 解析层 14 条 = 30 条以上
    expect(cases.length).toBeGreaterThanOrEqual(24)
    expect(cases.filter((c) => !c.ok)).toEqual([])
  })

  it('★ 全仓不得存在「调用点传了却没有任何语种显示」的参数（硬闸 0）', () => {
    const report = measureRepoCoverage()
    const detail = report.deadParams
      .map(
        (d) =>
          `  ${d.callSite.key}  @ ${relative(WEB_ROOT, d.callSite.file)}:${d.callSite.line}\n` +
          `      死参数: ${d.deadParams.join(', ')}\n` +
          `      候选消息: ${d.candidateKeys.slice(0, 5).join(', ')}${d.candidateKeys.length > 5 ? ' …' : ''}\n` +
          `      候选里出现过的占位符: ${d.availableParams.join(', ') || '(无)'}`,
      )
      .join('\n')
    expect(
      report.deadParams,
      `调用点传了却没有任何语种显示的参数（${report.deadParams.length} 处）：\n${detail}`,
    ).toEqual([])
    expect(report.deadParams.length).toBe(0)
  })

  it('★ 非空下限：门必须真量到了足够多的调用点与参数（防空过假绿）', () => {
    const report = measureRepoCoverage()
    // 调用点总数（第二个参数是对象字面量）：实测 337 ⇒ 下限 300
    expect(report.callSites, '调用点数量异常少 ⇒ 扫描器可能没扫到东西').toBeGreaterThanOrEqual(300)
    // 带具名参数的调用点：实测 336
    expect(report.sitesWithParams).toBeGreaterThanOrEqual(300)
    // 具名参数总数：实测 444
    expect(report.paramCount, '参数总数异常少 ⇒ 属性名可能没解析出来').toBeGreaterThanOrEqual(400)
    // 去重后的参数名：实测 101
    expect(report.distinctParamNames).toBeGreaterThanOrEqual(90)
    // 涉及的键数：实测 302
    // ⚠️ 这个数会比「调用点数」大，因为一个模板键会展开成多个候选消息键。
    expect(report.distinctKeys).toBeGreaterThanOrEqual(250)
    // 语种数：8
    expect(allLocaleValues().size).toBe(ALL_LOCALES.length)
    // ★ 键形态分布：literal 必须是绝大多数（否则解析器退化了）
    expect(report.byKeyKind.literal, 'literal 键占比异常低 ⇒ 模板/变量键解析有偏').toBeGreaterThanOrEqual(300)
  })

  it('★ 未决登记册与实测未决集合双向严格相等（新增未决 / 过期登记都要红）', () => {
    const report = measureRepoCoverage()
    const measured = new Set(
      report.unresolved.map((u) => `${relative(WEB_ROOT, u.callSite.file)}:${u.callSite.line}`),
    )
    const registered = new Set(UNRESOLVED_REGISTRY.map((r) => `${r.file}:${r.line}`))

    const missing = [...measured].filter((k) => !registered.has(k))
    const stale = [...registered].filter((k) => !measured.has(k))
    expect(
      missing,
      `实测未决但未登记（新增未决调用点，必须人工解出候选键并登记）：\n${missing.join('\n')}`,
    ).toEqual([])
    expect(stale, `登记册里有、实测已不未决（登记过期，必须删掉）：\n${stale.join('\n')}`).toEqual([])
    expect(measured.size).toBe(registered.size)
    // 每条登记都必须有可排查的理由（空理由 ⇒ 登记无效）
    for (const r of UNRESOLVED_REGISTRY) {
      expect(r.why, `登记 ${r.file}:${r.line} 缺少理由`).not.toBe('')
      expect(r.keys.length, `登记 ${r.file}:${r.line} 缺少候选键`).toBeGreaterThan(0)
      expect(r.params.length, `登记 ${r.file}:${r.line} 缺少期望占位符`).toBeGreaterThan(0)
    }
  })

  it('★ 登记册自证：每条登记的候选键在全部 8 语种下都带上了期望占位符', () => {
    const values = allLocaleValues()
    const bad: string[] = []
    for (const r of UNRESOLVED_REGISTRY) {
      for (const key of r.keys) {
        for (const loc of ALL_LOCALES) {
          const v = values.get(loc)?.[key]
          if (typeof v !== 'string') {
            bad.push(`${r.file}:${r.line} → ${loc} 的 ${key} 不存在或不是字符串`)
            continue
          }
          const ph = placeholderSet(v)
          for (const p of r.params) {
            if (!ph.has(p)) bad.push(`${r.file}:${r.line} → ${loc} 的 ${key} 缺占位符 {${p}}`)
          }
        }
      }
    }
    expect(bad, `登记的候选键与期望占位符不一致：\n${bad.join('\n')}`).toEqual([])
  })

  it('★ 定点回归：族2 rowResult.running 的调用点 {op} 在 8 语种都被显示', () => {
    const report = measureRepoCoverage()
    const key = 'dataLifecycle.storageOverview.rowResult.running'
    const hit = report.deadParams.filter((d) => d.callSite.key === key)
    expect(hit, `族2 回归（调用点传了 {op} 却没有语种显示）：\n${JSON.stringify(hit, null, 2)}`).toEqual([])
    // 且该键必须真的被某个调用点覆盖到（防空过：键换了名就没人管了）
    const values = allLocaleValues()
    for (const loc of ALL_LOCALES) {
      const ph = [...placeholderSet(values.get(loc)?.[key])].sort().join(',')
      expect(ph, `${loc} 的 ${key} 占位符应为 op`).toBe('op')
    }
  })

  it('★ 定点回归：zh-CN 的 sessions.config.promptInjectionCategory* 17 键在 8 语种齐备', () => {
    // 2026-10-21 实测缺陷：zh-CN 只把 1 个分类键放在调用点请求的路径上，
    //   其余 16 个被塞进零消费者的 `sessions.promptInjectionCategories.*` 死命名空间
    //   ⇒ 中文用户看到的是原始点号键路径。
    //   ★ 2026-10-21（R57 轮）后续：那个**死命名空间已按属主拍板删除**
    //     （1112 条死翻译，8 语种）⇒ 而本用例钉的 `sessions.config.promptInjectionCategory*`
    //     是 R55 轮补到**调用点请求路径**上的那份（R55 `bd7efecc5`）⇒ **不受删除影响**，
    //     仍须在 8 语种齐备 ⇒ 本用例继续有效。
    const values = allLocaleValues()
    const CATS = [
      'RoleHijack', 'InstructionOverride', 'InstructionLeak', 'Jailbreak',
      'EncodingBypass', 'InjectionMarker', 'MultiTurnAttack', 'ResourceExhaustion',
      'DataExfiltration', 'SocialEngineering', 'PromptLeaking', 'PayloadSmuggling',
      'UnicodeObfuscation', 'ContextManipulation', 'ToolAbuse', 'Legacy', 'Unknown',
    ]
    const bad: string[] = []
    for (const c of CATS) {
      const key = `sessions.config.promptInjectionCategory${c}`
      for (const loc of ALL_LOCALES) {
        const v = values.get(loc)?.[key]
        if (typeof v !== 'string' || v.trim() === '') bad.push(`${loc} 缺 ${key}`)
      }
    }
    expect(bad, `分类标签键缺失（调用点请求 sessions.config.${'${meta.i18nKey}'}）：\n${bad.join('\n')}`).toEqual([])
  })

  it('★ 定点回归：不得有调用点把 default 当插值参数传（vue-i18n v9 语义）', () => {
    // vue-i18n v9：t(key, {…}) 的整个对象 = options.named（具名插值表）
    //   ⇒ `default` 只是插值名，**不产生回退**；v9 的回退是字符串第二参或第三参。
    //   本仓全仓没有任何消息使用 `{default}` ⇒ 传它必然是死参数。
    const report = measureRepoCoverage()
    const offenders = report.deadParams.filter((d) => d.deadParams.includes('default'))
    expect(offenders, `default 被当插值参数传：\n${JSON.stringify(offenders, null, 2)}`).toEqual([])
  })
})
