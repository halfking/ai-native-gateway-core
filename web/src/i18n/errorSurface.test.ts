// errorSurface.test.ts — 用户可见错误文案的**唯一出口**（surfaceError）的行为契约
//
// ## 挡住的是什么
//
// 2026-10-27（第 16 增量）之前，全仓 311 处错误文案走的是这个形状：
//
//     error.value = e instanceof Error ? e.message : t('some.localized.fallback')
//
// 三元把 `.message` 放在**第一分支** ⇒ 兜底只在「抛出的不是 Error 实例」时才触发
// ⇒ 本地化兜底**结构性失效** ⇒ 用户实际看到的是后端下发的英文技术串
// （实测：admin 侧 1852 处硬编码错误文案，去重 631 条，其中仅 33 条中文）。
//
// 出口 `surfaceError()` 之后，错误文案的结构固定为：
//
//     `主文案`（点名主体 + 恢复动作） + `（原因：原始 detail）`
//
// ★ 主文案永远来自**当前语种**的语言包；原始 detail **只作次要原因**。
//
// ## 这份测试钉住的是「契约」，不是「实现」
//
//   ① 主文案来自当前语种（**逐语种**断言，期望值从语言包读，**不手抄**）
//   ② 原始 detail 只作次要原因，永不作主文案
//   ③ 噪声（该状态码自己的 statusText / 纯状态码 / 内部错误码）不占位
//   ④ 后端**真实**的理由（哪怕字面像 statusText）必须保留
//   ⑤ 缺键时**不出键路径**（vue-i18n 缺键会返回键路径本身，而本仓 missingWarn:false）
//
// ## 踩过的坑（记下来，因为症状很像「产品没修好」）
//
//   ★ `i18n.global.t(key)` **无参调用会把 `{reason}` 当命名插值吃掉**
//     ⇒ 「（原因：{reason}）」会渲染成「（原因：）」⇒ 出口必须走 `rawMessage()`。
//     用例 ⑧ 就是在钉这一条（断言渲染结果里**真的出现了** detail 原文）。
//   ★ 测试夹具必须**逐语种**：第一版把同一个中文值喂 8 个语种 ⇒ 非中文语种的
//     词表不认识中文 ⇒ 门/断言自己造出一堆假违规 ⇒ 期望值变成恒非空
//     （断言恒真比恒假更隐蔽）。本文件因此直接 import 8 份真实语言包。
//
// ## 反向对照（负控）
//
// 用例 ⑨：主文案取不到时（缺键）⇒ 落回**状态层**，且结果里**没有键路径**。
// 用例 ⑩：`withReason: false` ⇒ 只剩主文案，原因不占位。
//
// 均已实跑并断言变异确实发生（改出口实现 ⇒ 对应用例红）。

import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { i18n } from './index'
import { surfaceError, isInformativeReason } from './errorSurface'

import zhCN from '../locales/zh-CN'
import enUS from '../locales/en-US'
import zhTW from '../locales/zh-TW'
import jaJP from '../locales/ja-JP'
import deDE from '../locales/de-DE'
import esES from '../locales/es-ES'
import frFR from '../locales/fr-FR'
import arSA from '../locales/ar-SA'

/** 语言包目录名 → **i18n 实例内的语种码**（src/i18n.ts 的映射：en-US→en、ja-JP→ja …）。 */
const LOCALES = [
  { code: 'zh-CN', msgs: zhCN },
  { code: 'en', msgs: enUS },
  { code: 'zh-TW', msgs: zhTW },
  { code: 'ja', msgs: jaJP },
  { code: 'de', msgs: deDE },
  { code: 'es', msgs: esES },
  { code: 'fr', msgs: frFR },
  { code: 'ar', msgs: arSA },
] as const

/** 从语言包里取一个点分键的原文（期望值**读**出来，不手抄）。 */
function msgOf(messages: Record<string, unknown>, key: string): string {
  let cur: unknown = messages
  for (const seg of key.split('.')) {
    if (!cur || typeof cur !== 'object') return ''
    cur = (cur as Record<string, unknown>)[seg]
  }
  return typeof cur === 'string' ? cur : ''
}

const originalLocale = (i18n.global.locale as unknown as { value: string }).value

beforeAll(() => {
  for (const { code, msgs } of LOCALES) {
    i18n.global.setLocaleMessage(code, msgs as never)
  }
})

afterAll(() => {
  ;(i18n.global.locale as unknown as { value: string }).value = originalLocale
})

function setLocale(code: string) {
  ;(i18n.global.locale as unknown as { value: string }).value = code
}

describe('surfaceError：用户可见错误文案的唯一出口', () => {
  // ① 逐语种：主文案 = 当前语种语言包里的主体键；原因附在后缀里
  it('① 8 语种逐个：输出 = 该语种的主文案 + 该语种的原因后缀（含 detail 原文）', () => {
    const detail = 'upstream connect error'
    for (const { code, msgs } of LOCALES) {
      setLocale(code)
      const primary = msgOf(msgs as unknown as Record<string, unknown>, 'auditLog.loadFailed')
      const suffixTpl = msgOf(msgs as unknown as Record<string, unknown>, 'common.feedback.reasonSuffix')
      expect(primary, `${code} 缺 auditLog.loadFailed`).not.toBe('')
      expect(suffixTpl, `${code} 缺 common.feedback.reasonSuffix`).toContain('{reason}')
      const out = surfaceError({ detail }, { key: 'auditLog.loadFailed' })
      expect(out, `${code} 输出`).toBe(primary + suffixTpl.replace('{reason}', detail))
    }
  })

  // ② 原始 detail 永不作主文案
  it('② 原始 detail 只作次要原因：主文案位置不出现裸技术串', () => {
    setLocale('zh-CN')
    // ★ 用**人类可读**的技术串（后端 writeError 的实际形状）。
    //   不能用 `ECONNRESET` 这类 SCREAMING_SNAKE —— 那是**内部码**，
    //   按 impeccable「Do not expose internal codes」本就不该上屏（判据见用例 ⑤）。
    const out = surfaceError({ detail: 'upstream connect error' }, { key: 'auditLog.loadFailed' })
    expect(out.startsWith('审计日志列表没能加载出来')).toBe(true)
    expect(out).toContain('upstream connect error')
    // 主体文案在前、原因在后 ⇒ 原因不能顶在前面
    expect(out.indexOf('审计日志列表没能加载出来')).toBeLessThan(out.indexOf('upstream connect error'))
  })

  // ③ 噪声：该状态码**自己**的 statusText ⇒ 不占位
  it('③ HTTP statusText 是内部措辞，不占位（404 + "Not Found"）', () => {
    setLocale('zh-CN')
    const primary = msgOf(zhCN as unknown as Record<string, unknown>, 'errors.byStatus.404')
    const out = surfaceError({ status: 404, detail: 'Not Found' })
    expect(out).toBe(primary)
    expect(out).not.toContain('原因')
  })

  // ④ 反向：字面像 statusText、但**不是**该状态码自己的 ⇒ 是真实理由，必须留
  it('④ 后端真实理由必须保留（500 + "not found" ≠ 500 的 statusText）', () => {
    setLocale('zh-CN')
    const out = surfaceError({ status: 500, detail: 'not found' })
    expect(out).toContain('not found')
  })

  // ⑤ 纯状态码 / 内部错误码 ⇒ 不占位
  it('⑤ 纯状态码与 SCREAMING_SNAKE 内部码不占位', () => {
    expect(isInformativeReason('500')).toBe(false)
    expect(isInformativeReason('PROVIDER_TIMEOUT')).toBe(false)
    expect(isInformativeReason('')).toBe(false)
    setLocale('zh-CN')
    const primary = msgOf(zhCN as unknown as Record<string, unknown>, 'errors.byStatus.500')
    expect(surfaceError({ status: 500, detail: 'PROVIDER_TIMEOUT' })).toBe(primary)
  })

  // ⑥ 过长原因 ⇒ 截断
  it('⑥ 超长原因被截断（200 字符上限）', () => {
    setLocale('zh-CN')
    const long = 'x'.repeat(500)
    const out = surfaceError({ detail: long })
    expect(out).toContain('…')
    expect(out.length).toBeLessThan(400)
  })

  // ⑦ 状态层回落：没有主体键 ⇒ 按状态码取 errors.byStatus.*
  it('⑦ 无主体键 ⇒ 落回状态层（errors.byStatus.*）', () => {
    setLocale('zh-CN')
    const primary = msgOf(zhCN as unknown as Record<string, unknown>, 'errors.byStatus.403')
    const out = surfaceError({ status: 403, detail: 'tenant not permitted' })
    expect(out.startsWith(primary)).toBe(true)
  })

  // ⑧ 占位符不被吃掉（无参 t() 的坑）
  it('⑧ 原因后缀的 {reason} 占位符不被 vue-i18n 吃掉（detail 真的上屏）', () => {
    setLocale('zh-CN')
    const out = surfaceError({ detail: 'MARKER-9f3a' }, { key: 'auditLog.loadFailed' })
    expect(out).toContain('MARKER-9f3a')
    expect(out).not.toContain('（原因：）')
  })

  // ⑨ 负控：缺键 ⇒ 不出键路径
  it('⑨ 缺键时不显示键路径（落回状态层）', () => {
    setLocale('zh-CN')
    const out = surfaceError({ status: 500, detail: 'boom' }, { key: 'definitely.not.a.real.key' })
    expect(out).not.toContain('definitely.not.a.real.key')
    expect(out).not.toContain('not.a.real')
    expect(out.length).toBeGreaterThan(0)
  })

  // ⑩ 负控：显式不要原因 ⇒ 只有主文案
  it('⑩ withReason:false ⇒ 只剩主文案', () => {
    setLocale('zh-CN')
    const out = surfaceError({ detail: 'boom' }, { key: 'auditLog.loadFailed', withReason: false })
    expect(out).toBe(msgOf(zhCN as unknown as Record<string, unknown>, 'auditLog.loadFailed'))
  })

  // ⑪ 网络层失败 ⇒ errors.network
  it('⑪ 网络层失败落回 errors.network', () => {
    setLocale('zh-CN')
    const primary = msgOf(zhCN as unknown as Record<string, unknown>, 'errors.network')
    const out = surfaceError(new TypeError('Failed to fetch'))
    expect(out.startsWith(primary)).toBe(true)
  })
})
