// localeKeyParity.test.ts — 「跨语种键存在性平价」的门（2026-10-21，R56 轮）
//
// 补的是 R55 §13.4-2 登记的「新的最大缝」：
//   parity.test.ts 只查「每个语种 ⊇ zh-CN」⇒ **单向**
//   ⇒「**某语种缺了别的语种有的键**」无人管
//   ⇒ R55 的 zh-CN 分类键缺陷正是从这个缝漏出来的。
//
// 本门与既有门的分工（**不重复**）：
//   · parity.test.ts        单向：每个语种 ⊇ zh-CN（键**集合**层面）
//   · keys_referenced.test.ts 单向：被引用的键在 zh-CN 里存在
//   · localeValueQuality    值的**语言**对不对
//   · localePlaceholderParity 值的**占位符**平不平
//   · callSitePlaceholderCoverage 调用点的**参数**有没有被显示
//   · **本门**                 键的**存在性**跨语种**平不平**（双向，不设参照方）
//
// 判据纪律沿用本仓既有约定：
//   ① 自证必须**双向**（会触发 + 不误触发各半）—— 否则恒绿门与恒红门同样没用。
//   ② 硬闸必须配**非空下限**—— 否则「一个键都没量」也是绿的（假绿）。
//   ③ 棘轮只许降不许升 —— 升基线必须人工显式改 JSON 并在 commit 消息写明理由。
//   ④ 阴性结论（0 违例）要当缺陷一样复核 —— 先怀疑量具坏了。
//      R56 实测：门首跑就报 3 条违例，追下去发现是**量具口径问题**
//      （键集合用了只收字符串的口径，而引用扫描把数组当键）⇒ 修量具，不修判据。

import { describe, expect, it } from 'vitest'
import {
  measureRepoKeyParity,
  measureKeyParity,
  collectReachableKeys,
  selfCheckKeyParity,
  readKeyParityBaseline,
  ALL_LOCALES,
  type ReachableKeySources,
} from './localeKeyParity'
import { scanAllCallSiteKeys, scanAllCallSiteParams } from './callSiteParams'
import { SRC_DIR } from './localeValueQuality'

describe('i18n 跨语种键存在性平价（localeKeyParity）', () => {
  it('判定层自证 12 条双向全过（量具必须有牙）', () => {
    const cases = selfCheckKeyParity()
    const failed = cases.filter((c) => !c.ok)
    expect(
      failed.map((c) => `${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`),
      '判定层自证失败',
    ).toEqual([])
    expect(cases.length).toBeGreaterThanOrEqual(12)
  })

  it('★ 硬闸：每个可达键必须在全部 8 个语种都存在（双向，不设参照方）', () => {
    const report = measureRepoKeyParity()
    const detail = report.missingInLocale
      .map((v) => `  ${v.key} 缺: ${v.missingIn.join(', ')}`)
      .join('\n')
    expect(
      report.missingInLocale.map((v) => `${v.key} missing in ${v.missingIn.join(',')}`),
      `跨语种键存在性不平等（${report.missingInLocale.length} 条）：\n${detail}`,
    ).toEqual([])
  })

  it('★ 非空下限：量到的键数必须够多（否则「什么都没量」也是绿的）', () => {
    const report = measureRepoKeyParity()
    expect(report.locales.length, '语种数应为 8').toBe(8)
    // ★ 2026-10-27（第 13 增量 · 语言包死键清理轮）：union 键数实测由 9468 降到 **6153**
    //   （删掉了 3315 个「8 语种共有 + 全仓 0 引用」的叶子键）⇒ 原下限 9000 已高于实测 ⇒ 门红。
    //   ⇒ 下限按新事实下调到 **5000**：仍远高于「加载器坏了、只比了几个键」那条假绿线。
    expect(report.unionSize, 'union 键数').toBeGreaterThan(5000)
    expect(report.sources.total, '可达键数').toBeGreaterThan(5000)
    expect(report.sources.literal, '字面量引用键数').toBeGreaterThan(4000)
    // ★ 模板候选必须**真的**有量 —— 若为 0，说明模板键的收集机制坏了
    //   （R56 实测：旧扫描器只看到 3 个模板调用点 ⇒ 候选 5 个；新扫描器 59 个 ⇒ 候选 455+）
    expect(report.sources.template, '模板候选键数').toBeGreaterThan(200)
    expect(report.dynamicCallSites, '动态键调用点数（不可静态解析，如实计量）').toBeGreaterThan(0)
  })

  it('棘轮：各语种「相对 union 缺失的键数」不得超过 baseline（只许降不许升）', () => {
    const report = measureRepoKeyParity()
    const baseline = readKeyParityBaseline()
    expect(baseline, 'baseline 文件 src/i18n/localeKeyParityBaseline.json 缺失或非法').not.toBeNull()
    const offenders: string[] = []
    for (const loc of report.locales) {
      const limit = baseline!.missingVsUnion?.[loc]
      const cur = report.missingVsUnion[loc]
      if (typeof limit !== 'number') {
        offenders.push(`${loc} 不在 baseline 里`)
        continue
      }
      if (cur > limit) offenders.push(`${loc}: ${cur} > baseline ${limit}`)
    }
    expect(offenders, `棘轮被突破：\n${offenders.join('\n')}`).toEqual([])
  })

  it('棘轮：「存在于部分语种但非全部」的键数不得超过 baseline', () => {
    const report = measureRepoKeyParity()
    const baseline = readKeyParityBaseline()
    expect(baseline, 'baseline 缺失').not.toBeNull()
    expect(
      report.asymmetricKeys,
      `不对称键数 ${report.asymmetricKeys} > baseline ${baseline!.asymmetricKeys}`,
    ).toBeLessThanOrEqual(baseline!.asymmetricKeys)
  })

  it('★ 定点回归：R55 的 zh-CN 分类键缺陷形状（zh-CN 缺、其余语种有）必须被硬闸抓住', () => {
    // 这一条是**机制**的回归（不是手写的「这 17 个键要在」）⇒
    // 证明 R56 门**不需要**像 R55 那样为每个键族手写定点回归。
    //
    // 构造：三个语种有 `ns.cat`，一个语种没有，且 `ns.cat` 落在可达键集合里
    //   ⇒ 硬闸必须报 1 条违例，并点名缺失的语种。
    const sources: ReachableKeySources = { literal: 1, template: 0, total: 1 }
    const keysByLocale = new Map<string, Set<string>>([
      ['en-US', new Set(['ns.cat'])],
      ['fr-FR', new Set(['ns.cat'])],
      ['ja-JP', new Set(['ns.cat'])],
      ['zh-CN', new Set<string>()], // ← zh-CN 缺（R55 缺陷的形状）
    ])
    const report = measureKeyParity(
      keysByLocale,
      new Set(['ns.cat']),
      new Set(['ns.cat']),
      sources,
    )
    expect(report.missingInLocale.length, '硬闸必须报 1 条').toBe(1)
    expect(report.missingInLocale[0].key).toBe('ns.cat')
    expect(report.missingInLocale[0].missingIn).toEqual(['zh-CN'])
  })

  it('★ 定点回归：反向形状（zh-CN 有、别的语种缺）同样被硬闸抓住', () => {
    // 方向必须**双向** —— R55 的缺陷是「zh-CN 缺」，但反向同样是用户可见缺陷
    //   （那个语种的用户看到原始点号键路径）。
    const sources: ReachableKeySources = { literal: 1, template: 0, total: 1 }
    const keysByLocale = new Map<string, Set<string>>([
      ['en-US', new Set<string>()], // ← en-US 缺
      ['zh-CN', new Set(['ns.cat'])],
    ])
    const report = measureKeyParity(
      keysByLocale,
      new Set(['ns.cat']),
      new Set(['ns.cat']),
      sources,
    )
    expect(report.missingInLocale.length, '硬闸必须报 1 条').toBe(1)
    expect(report.missingInLocale[0].missingIn).toEqual(['en-US'])
  })

  it('★ 定点回归：模板派生键也进硬闸（R55 缺陷自己就是模板键）', () => {
    // R55 的缺陷键族来自 `t(\`sessions.config.${meta.i18nKey}\`, …)` ——
    //   第二参**不是**对象字面量 ⇒ R55 的扫描器扫不到 ⇒ 机制是瞎的。
    // ⇒ 本条断言：模板候选键**确实**进入可达键集合 ⇒ 会进硬闸。
    const { callSites } = scanAllCallSiteKeys(SRC_DIR)
    const templateSites = callSites.filter((c) => c.keyKind === 'template')
    expect(templateSites.length, '模板调用点数').toBeGreaterThan(20)

    const union = new Set(['sessions.config.catA', 'sessions.config.catB', 'other.x'])
    const collected = collectReachableKeys(
      union,
      [
        {
          file: 'f.vue', line: 1, column: 1, key: 'sessions.config.${k}',
          keyKind: 'template', keyPrefix: 'sessions.config.', keySuffix: '',
          secondArgKind: 'none', snippet: '',
        },
      ],
      new Set(['other.x']),
    )
    expect(collected.keys.has('sessions.config.catA'), '模板候选 A 必须进可达集').toBe(true)
    expect(collected.keys.has('sessions.config.catB'), '模板候选 B 必须进可达集').toBe(true)
    expect(collected.sources.template, '模板候选增量').toBe(2)
  })

  it('★ 新扫描器是旧扫描器的真超集（旧扫描器扫到的，新扫描器必须都扫到）', () => {
    // 差集对照（R55 §13.6-4 的纪律）：「只有旧版找到的 = 0」⇒ 新版没漏扫。
    // 这是**两套量具**并存的必要条件 —— 否则新门会用一套更窄的引用集。
    const { callSites: keys } = scanAllCallSiteKeys(SRC_DIR)
    const newSig = new Set(keys.map((k) => `${k.file}:${k.line}:${k.column}`))
    // 旧扫描器（只认对象字面量第二参）—— 用同一模块的旧函数复算
    const { callSites: old } = scanAllCallSiteParams(SRC_DIR)
    const onlyOld = old.filter((o) => !newSig.has(`${o.file}:${o.line}:${o.column}`))
    expect(
      onlyOld.map((o) => `${o.file}:${o.line}`),
      '旧扫描器扫到而新扫描器没扫到的调用点（应为 0 ⇒ 新扫描器是超集）',
    ).toEqual([])
    expect(keys.length, '新扫描器应扫到多于旧扫描器的调用点').toBeGreaterThan(old.length)
  })

  it('语种集合固定为 8 个（防止新增语种后本门静默不覆盖）', () => {
    expect(ALL_LOCALES).toEqual(['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW'])
  })
})
