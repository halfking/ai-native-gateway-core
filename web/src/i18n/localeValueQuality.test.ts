// localeValueQuality.test.ts — 「键在、值没翻」的门（2026-10-21 轮新增）
//
// 补的是 i18n 三条既有门合起来的缝（见 localeValueQuality.ts 头注释）：
//   parity（只比键集合）/ keys_referenced（只查键存在）/ 硬编码中文棘轮（排除 locales）
//   ⇒ 「键在、但值是英文原文」三类缺陷一条都拦不住。
//
// 三条门：
//   ① en-US 语种的值不得含汉字（硬闸，基线 0）—— 英文界面显示中文是用户可见缺陷
//   ② 每个语种「值仍等于 en-US」的键数 ≤ baseline（棘轮，只许降不许升）
//   ③ 非中日语种里含汉字的值数 ≤ baseline（棘轮）
//   ④ 每个语种「值仍等于**源语言 zh-CN**」的键数 ≤ baseline（棘轮）
//      —— ③ 把 ja-JP / zh-TW 整体豁免（「日文/繁中用汉字是正常的」），
//         ⇒ 那两片「整段留了简体原文」的盲区只有 ④ 看得见。
//
// ⚠️ 本门**不是**「全仓 i18n 已达标」的声明：它只覆盖**被源码引用**的键
//   （动态拼出来的键扫不到），且「值与 en-US 逐字相同」里含合法同值
//   （产品名、'OK'、'HTTP 500' 等）⇒ 存量数字是**上界**，不是缺陷精确条数。
//   棘轮方向（不增）对这些上界依然成立。

import { describe, expect, it } from 'vitest'
import {
  ALL_LOCALES,
  CJK_NATIVE_LOCALES,
  REFERENCE_LOCALE,
  SOURCE_LOCALE,
  assertSelfCheckCounting,
  buildBaseline,
  measureAll,
  readBaseline,
} from './localeValueQuality'

// ⚠️ 超时给到 120s：门要扫完 `src/**`（6000+ 引用）并加载 8 个语种的 locale 模块
//   （vm 求值），实测单次 20~40s。measureAll 已带缓存 ⇒ 整个文件只量一次。
//   （不缓存 + 默认 20s 超时 = 门自己变成计时敏感门，机器一忙就假红。）
describe('i18n 值质量（键在、值没翻）', { timeout: 120_000 }, () => {
  it('★ 计数器自证（量具有牙：会触发 + 不误触发）', () => {
    const cases = assertSelfCheckCounting()
    expect(cases.length).toBeGreaterThanOrEqual(15)
    expect(cases.filter((c) => !c.ok)).toEqual([])
  })

  it('★ en-US 语种的值不得含汉字（英文界面显示中文 = 用户可见缺陷）', () => {
    const { byLocale } = measureAll()
    const en = byLocale.get(REFERENCE_LOCALE)!
    expect(
      en.cjkSamples,
      `en-US 有 ${en.cjkInValue} 个被引用的值仍含汉字（英文用户会看到中文）：\n` +
        en.cjkSamples.map((k) => `  ${k}`).join('\n'),
    ).toEqual([])
    expect(en.cjkInValue).toBe(0)
  })

  it('每个语种「值仍等于 en-US」的键数不得高于 baseline（棘轮）', () => {
    const baseline = readBaseline()
    expect(baseline, 'localeValueBaseline.json 缺失').not.toBeNull()
    const { byLocale } = measureAll()
    const over: string[] = []
    for (const loc of ALL_LOCALES) {
      if (loc === REFERENCE_LOCALE) continue
      const limit = baseline!.untranslated?.[loc]
      if (typeof limit !== 'number') {
        over.push(`${loc}: baseline 里没有这一项（新增语种必须先登记基线）`)
        continue
      }
      const actual = byLocale.get(loc)!.untranslated
      if (actual > limit) {
        over.push(
          `${loc}: ${actual} > baseline ${limit}（新增 ${actual - limit} 条「值=英文原文」）\n` +
            byLocale.get(loc)!.untranslatedSamples.map((k) => `      ${k}`).join('\n'),
        )
      }
    }
    expect(over, `「值=英文原文」的条数上升了：\n${over.join('\n')}`).toEqual([])
  })

  it('非中日语种里含汉字的值数不得高于 baseline（棘轮）', () => {
    const baseline = readBaseline()
    expect(baseline, 'localeValueBaseline.json 缺失').not.toBeNull()
    const { byLocale } = measureAll()
    const over: string[] = []
    for (const loc of ALL_LOCALES) {
      if (loc === REFERENCE_LOCALE || CJK_NATIVE_LOCALES.has(loc)) continue
      const limit = baseline!.cjkInNonCjkLocale?.[loc]
      if (typeof limit !== 'number') {
        over.push(`${loc}: baseline 里没有这一项（新增语种必须先登记基线）`)
        continue
      }
      const q = byLocale.get(loc)!
      if (q.cjkInValue > limit) {
        over.push(`${loc}: ${q.cjkInValue} > baseline ${limit}\n` + q.cjkSamples.map((k) => `      ${k}`).join('\n'))
      }
    }
    expect(over, `非中日语种里出现汉字：\n${over.join('\n')}`).toEqual([])
  })

  it('每个语种「值仍等于源语言 zh-CN」的键数不得高于 baseline（棘轮；ja-JP/zh-TW 靠它才可见）', () => {
    const baseline = readBaseline()
    expect(baseline, 'localeValueBaseline.json 缺失').not.toBeNull()
    const { byLocaleAllKeys } = measureAll()
    const over: string[] = []
    for (const loc of ALL_LOCALES) {
      // 源语言自己与自己逐字相同是恒等式 ⇒ 无意义，不计。
      if (loc === SOURCE_LOCALE) continue
      const limit = baseline!.sameAsSource?.[loc]
      if (typeof limit !== 'number') {
        over.push(`${loc}: baseline 里没有这一项（新增语种必须先登记基线）`)
        continue
      }
      const q = byLocaleAllKeys.get(loc)!
      if (q.sameAsSource > limit) {
        over.push(
          `${loc}: ${q.sameAsSource} > baseline ${limit}（新增 ${q.sameAsSource - limit} 条「还留着源语言原文」）\n` +
            q.sameAsSourceSamples.map((k) => `      ${k}`).join('\n'),
        )
      }
    }
    expect(over, `「还留着源语言原文」的条数上升了：\n${over.join('\n')}`).toEqual([])
  })

  it('★ 非中日语种里含汉字的值数（全量键口径）不得高于 baseline —— 治「动态键」盲区', () => {
    // ⚠ 这条与上面的「引用集口径」是**两回事**：
    //   引用集口径对 `navLabel(item.labelKey, …)` 这类**运行时拼键**完全失效
    //   （引用集里没有 ⇒ 读数 0 ⇒ 而界面上仍在显示中文；实测每个语种 182 条）。
    //   ⇒ 必须有一条按**全部叶子键**计数的口径，否则这个盲区会一直绿着。
    const baseline = readBaseline()
    expect(baseline, 'localeValueBaseline.json 缺失').not.toBeNull()
    const { byLocaleAllKeys } = measureAll()
    const over: string[] = []
    for (const loc of ALL_LOCALES) {
      if (CJK_NATIVE_LOCALES.has(loc)) continue
      const limit = baseline!.cjkInNonCjkLocaleAllKeys?.[loc]
      if (typeof limit !== 'number') {
        over.push(`${loc}: baseline 里没有这一项（新增语种必须先登记基线）`)
        continue
      }
      const q = byLocaleAllKeys.get(loc)!
      if (q.cjkInValue > limit) {
        over.push(
          `${loc}: ${q.cjkInValue} > baseline ${limit}（新增 ${q.cjkInValue - limit} 条含汉字的值）\n` +
            q.cjkSamples.map((k) => `      ${k}`).join('\n'),
        )
      }
    }
    expect(over, `非中日语种里出现汉字（全量键口径）：\n${over.join('\n')}`).toEqual([])
  })

  it('baseline 非空且覆盖全部语种（防空过）', () => {
    const baseline = readBaseline()
    expect(baseline).not.toBeNull()
    expect(Object.keys(baseline!.untranslated).length).toBe(ALL_LOCALES.length - 1)
    for (const loc of ALL_LOCALES) {
      if (loc === REFERENCE_LOCALE) continue
      expect(typeof baseline!.untranslated[loc], `${loc} 未登记 untranslated 基线`).toBe('number')
    }
    expect(baseline!.enUSCjk).toBe(0)
    // ★ 指标 4 的基线必须覆盖**除源语言外**的全部语种
    //   （ja-JP / zh-TW 的「整段留了简体原文」全靠这一项才看得见 ⇒ 不能漏登记）
    expect(Object.keys(baseline!.sameAsSource ?? {}).length).toBe(ALL_LOCALES.length - 1)
    for (const loc of ALL_LOCALES) {
      if (loc === SOURCE_LOCALE) continue
      expect(typeof baseline!.sameAsSource?.[loc], `${loc} 未登记 sameAsSource 基线`).toBe('number')
    }
    // ★ 指标 5 的基线必须覆盖**全部非 CJK 语种**（含 en-US）
    const nonCjk = ALL_LOCALES.filter((l) => !CJK_NATIVE_LOCALES.has(l))
    expect(Object.keys(baseline!.cjkInNonCjkLocaleAllKeys ?? {}).length).toBe(nonCjk.length)
    for (const loc of nonCjk) {
      expect(
        typeof baseline!.cjkInNonCjkLocaleAllKeys?.[loc],
        `${loc} 未登记 cjkInNonCjkLocaleAllKeys 基线`,
      ).toBe('number')
    }
  })

  it('实测值与 baseline 的差值只允许为负（棘轮只许降）', () => {
    // 这条不是重复上一条：它把「基线是**当前**上界」这件事也钉住，
    // 避免有人手改 JSON 把基线调高（那会让门失去意义）。
    const baseline = readBaseline()!
    const { byLocale } = measureAll()
    const raised: string[] = []
    for (const loc of ALL_LOCALES) {
      if (loc === REFERENCE_LOCALE) continue
      const limit = baseline.untranslated?.[loc]
      if (typeof limit === 'number' && byLocale.get(loc)!.untranslated > limit) {
        raised.push(`${loc}: 实测 ${byLocale.get(loc)!.untranslated} > 基线 ${limit}`)
      }
    }
    expect(raised, `基线被调高或出现新缺陷：\n${raised.join('\n')}`).toEqual([])
  })

  it('buildBaseline 能从实测值构造出与当前基线同形的草稿（CLI 出口自证）', () => {
    const { byLocale, byLocaleAllKeys } = measureAll()
    const draft = buildBaseline(byLocale, byLocaleAllKeys)
    expect(Object.keys(draft.untranslated).sort()).toEqual(
      ALL_LOCALES.filter((l) => l !== REFERENCE_LOCALE).sort(),
    )
    expect(draft.enUSCjk).toBe(0)
    // ★ 指标 4：源语言自身不登记，其余语种全登记
    expect(Object.keys(draft.sameAsSource).sort()).toEqual(
      ALL_LOCALES.filter((l) => l !== SOURCE_LOCALE).sort(),
    )
    // ★ 指标 5：全部非 CJK 语种（含 en-US）登记
    expect(Object.keys(draft.cjkInNonCjkLocaleAllKeys).sort()).toEqual(
      ALL_LOCALES.filter((l) => !CJK_NATIVE_LOCALES.has(l)).sort(),
    )
  })
})
