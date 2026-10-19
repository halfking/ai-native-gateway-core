// localeValueQuality.ts — 「键在、值没翻」的质量计数器 + baseline 棘轮
//
// 目的：补上 i18n 三条既有门合起来的缝（2026-10-21 轮实测）：
//   · parity.test.ts        只比**键集合** ⇒ 英文占位照样「平」
//   · keys_referenced.test.ts 只查**键是否存在** ⇒ 同上
//   · hardcoded_cjk 棘轮     **排除 src/locales/** ⇒ 翻译成英文既不涨中文计数也不被它管
//   ⇒ 三条门合起来拦不住「键在、但值是英文原文」。
//
// 本模块量三件事（口径见下方「口径」）：
//   1. enUS_cjk        —— en-US 语种里**含汉字的值**。这是**硬闸（基线 0）**：
//                         英文界面显示中文是用户可见缺陷，不该被「存量」掩盖。
//   2. untranslated    —— 某语种的值与 en-US **逐字相同**的键数（=「没翻」）。
//   3. cjkInNonCjkLocale —— 非中日语种里**含汉字的值**（阿拉伯/德/西/法语种里出现汉字）。
//   2 与 3 都是**棘轮**：baseline 记当前值，只许降不许升。
//
// 口径（重要，量具口径变了 baseline 就得重铸）：
//   - 只统计**被源码引用**的键（scanAll 的引用集）。未被引用的键不会显示在屏幕上，
//     不构成用户可见缺陷；把它们计进来会让「存量噪声」淹没增量信号。
//     ⚠ 代价：动态拼出来的键（`t('x.' + name)`）扫不到 ⇒ 该形态**不被本门覆盖**。
//   - 语种值取**运行时真值**（用 vm 跑 locale 模块 ⇒ JS 对象字面量语义：
//     重复键后者覆盖前者）。⚠ **不要用文件文本统计**：en-US/routing.ts 里
//     `queryFailed` 出现 4 次，运行时生效的是英文那个（文件文本口径会把
//     早已被覆盖的中文算成缺陷 —— 实测文件行数 588 vs 运行时 389）。
//   - CJK = /[一-鿿㐀-䶿]/（汉字，含扩展 A）。ja-JP 排除：日文用汉字是正常的。
//
// 消费方：
//   - src/i18n/localeValueQuality.test.ts（pnpm test 强制执行）
//   - scripts/i18n-locale-value-count.mjs（CLI：npm run i18n:locale-value:check）
//
// 棘轮纪律：**只许降不许升**。writeBaseline 在任何一项会升高时直接抛错，
// 不提供 --force —— 「把基线调高」必须是人工显式改 JSON 的动作，且要在 commit
// 消息里写明理由（与 hardcoded_cjk 的纪律一致：CLI 是写 baseline 的出口，
// 一个数错目标的量具正是从这儿把错的数字固化进 baseline 的）。

import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
// ⚠️ 显式带 `.ts` 扩展：CLI 用 `node --experimental-strip-types` 跑本文件，
//   Node 的 ESM 解析**不认**无扩展名的相对导入（vitest 认）⇒ 不带扩展名会让
//   `npm run i18n:locale-value:check` 直接 ERR_MODULE_NOT_FOUND。
import { loadLocale, scanAll } from './scanner.ts'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const WEB_ROOT = join(__dirname, '..', '..')
export const SRC_DIR = join(WEB_ROOT, 'src')
export const LOCALES_DIR = join(SRC_DIR, 'locales')
export const BASELINE_FILE = join(WEB_ROOT, 'src', 'i18n', 'localeValueBaseline.json')

export const REFERENCE_LOCALE = 'en-US'
export const ALL_LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']
/** 这三个语种本身就用汉字（zh）或假名/汉字（ja）⇒ 「值含汉字」不是它们的缺陷。 */
export const CJK_NATIVE_LOCALES = new Set(['zh-CN', 'zh-TW', 'ja-JP'])

const CJK_RE = /[一-鿿㐀-䶿]/

export interface LocaleValues {
  [fullKey: string]: string
}

export interface LocaleQuality {
  locale: string
  /** 被引用且本语种存在的键数 */
  referenced: number
  /** 值与 en-US 逐字相同（= 没翻） */
  untranslated: number
  /** 值里含汉字 */
  cjkInValue: number
  /** 抽样（用于报错时定位） */
  untranslatedSamples: string[]
  cjkSamples: string[]
}

// ─────────────────────────── 值抽取 ───────────────────────────

function collectLeafValues(obj: unknown, prefix = '', out: LocaleValues = {}): LocaleValues {
  if (obj === null || typeof obj !== 'object') return out
  for (const [k, v] of Object.entries(obj as Record<string, unknown>)) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v !== null && typeof v === 'object' && !Array.isArray(v)) {
      collectLeafValues(v, key, out)
    } else if (typeof v === 'string') {
      out[key] = v
    }
  }
  return out
}

/** 取某语种**运行时真值**的展平表（full key = 模块名 + 内部路径）。 */
export function localeValues(locale: string, localesDir: string = LOCALES_DIR): LocaleValues {
  return collectLeafValues(loadLocale(locale, localesDir))
}

// ─────────────────────────── 全语种值表（带缓存） ───────────────────────────
//
// ⚠️ 为什么要这一层：加载 8 个语种的全部 locale 模块（vm 求值）**实测单次 20~40s**。
//   现有两道门（localeValueQuality 的质量计数、localePlaceholderParity 的占位符平价）
//   都需要「全部语种的运行时真值」⇒ 若各自加载一次 ⇒ 同一进程里加载 16 次
//   ⇒ 门自己变成「计时敏感门」（默认 20s 超时必红，机器一忙就假红）。
//   ⇒ **缓存挂在「值表」这一层**（不是挂在各自的指标上）⇒ 两道门共享同一次加载。
//
// ⚠️ 缓存是**按进程内存**的：测完之后若改了 locale 文件再测，拿到的仍是旧值。
//   用途：① 测试里改了 locale 文件要复测时必须先调 clearMeasureCache()；
//        ② CLI 每次是新进程，不受影响。
let cachedValues: Map<string, LocaleValues> | null = null

/** 取全部语种的运行时真值展平表（带进程内缓存）。 */
export function allLocaleValues(
  localesDir: string = LOCALES_DIR,
): Map<string, LocaleValues> {
  if (cachedValues && localesDir === LOCALES_DIR) return cachedValues
  const m = new Map<string, LocaleValues>()
  for (const loc of ALL_LOCALES) m.set(loc, localeValues(loc, localesDir))
  if (localesDir === LOCALES_DIR) cachedValues = m
  return m
}

// ─────────────────────────── 计数（纯函数，可被自证直接驱动） ───────────────────────────

/**
 * 纯函数：量一个语种的三个指标。
 * @param values      本语种运行时真值表
 * @param refValues   参照语种（en-US）运行时真值表
 * @param referenced  被源码引用的键集合
 */
export function measure(
  values: LocaleValues,
  refValues: LocaleValues,
  referenced: Set<string>,
  locale: string,
): LocaleQuality {
  const q: LocaleQuality = {
    locale,
    referenced: 0,
    untranslated: 0,
    cjkInValue: 0,
    untranslatedSamples: [],
    cjkSamples: [],
  }
  for (const key of referenced) {
    const v = values[key]
    if (typeof v !== 'string') continue
    q.referenced++
    const ref = refValues[key]
    if (typeof ref === 'string' && v === ref) {
      q.untranslated++
      if (q.untranslatedSamples.length < 8) q.untranslatedSamples.push(key)
    }
    if (CJK_RE.test(v)) {
      q.cjkInValue++
      if (q.cjkSamples.length < 8) q.cjkSamples.push(key)
    }
  }
  return q
}

/** 一次量全部语种。
 *
 *  ⚠️ **必须带缓存**：量一次要扫完 `src/**`（6000+ 引用）并加载 8 个语种的
 *  全部 locale 模块（vm 求值）—— 实测单次 20~40s。门里有 4 条断言都要用这份结果；
 *  不缓存 ⇒ 同一份数据量 4 次 ⇒ 门自己变成「计时敏感门」（默认 20s 超时必红，
 *  机器一忙就假红）。⇒ 缓存后只量一次，后续断言复用。
 */
let cachedAll: { byLocale: Map<string, LocaleQuality>; referencedCount: number } | null = null

/**
 * 清掉测量缓存。
 *
 * ⚠️ **缓存是按进程内存的**：测完之后若**改了 locale 文件**再测，拿到的仍是旧值。
 * 用途：① 测试里改了 locale 文件要复测时必须先调它；
 *      ② CLI 每次是新进程，不受影响。
 * ⇒ **别把「缓存」当成「文件变了会自动重算」**：本仓已有一条同族教训
 *   （判据脚本自身读到半写文件 ⇒ 瞬时红）。
 */
export function clearMeasureCache(): void {
  cachedAll = null
  cachedValues = null
}

export function measureAll(
  localesDir: string = LOCALES_DIR,
  srcDir: string = SRC_DIR,
): { byLocale: Map<string, LocaleQuality>; referencedCount: number } {
  if (cachedAll && localesDir === LOCALES_DIR && srcDir === SRC_DIR) return cachedAll
  const { references } = scanAll(srcDir)
  const referenced = new Set(references.map((r) => r.key))
  const valuesByLocale = allLocaleValues(localesDir)
  const refValues = valuesByLocale.get(REFERENCE_LOCALE) ?? {}
  const byLocale = new Map<string, LocaleQuality>()
  for (const loc of ALL_LOCALES) {
    const values = valuesByLocale.get(loc) ?? {}
    byLocale.set(loc, measure(values, refValues, referenced, loc))
  }
  const result = { byLocale, referencedCount: referenced.size }
  if (localesDir === LOCALES_DIR && srcDir === SRC_DIR) cachedAll = result
  return result
}

// ─────────────────────────── 自证（量具必须有牙） ───────────────────────────
//
// 这组自证的唯一价值是「每次跑门禁都执行」。写成只被调用一次、或只把结果算出来
// 却不断言，等于没有 —— 一个不触发的检查不报任何错。
// 方向必须**双向**：
//   · 「会触发」—— 值仍是英文原文 / 值含汉字，必须被计到
//   · 「不误触发」—— 已本地化的值、**未被引用**的键，必须不被计到

export interface SelfCheckCase {
  id: string
  desc: string
  expected: number
  actual: number
  ok: boolean
}

export function selfCheckCounting(): SelfCheckCase[] {
  const cases: SelfCheckCase[] = []
  const ref: LocaleValues = { 'm.a': 'Hello', 'm.b': '查询失败', 'm.c': 'Only in ref', 'm.d': 'Hello' }
  const probe = (
    id: string,
    desc: string,
    values: LocaleValues,
    referenced: string[],
    pick: (q: LocaleQuality) => number,
    expected: number,
  ) => {
    const q = measure(values, ref, new Set(referenced), 'probe')
    const actual = pick(q)
    cases.push({ id, desc, expected, actual, ok: actual === expected })
  }

  probe('L1', '与 en-US 逐字相同 ⇒ 计为「没翻」', { 'm.a': 'Hello' }, ['m.a'], (q) => q.untranslated, 1)
  probe('L2', '已本地化 ⇒ 不计为「没翻」', { 'm.a': 'مرحبا' }, ['m.a'], (q) => q.untranslated, 0)
  probe('L3', '值含汉字 ⇒ 计为 cjkInValue', { 'm.b': '查询失败' }, ['m.b'], (q) => q.cjkInValue, 1)
  probe('L4', '纯英文值 ⇒ 不计为 cjkInValue', { 'm.a': 'Hello' }, ['m.a'], (q) => q.cjkInValue, 0)
  probe('L5', '未被引用的键不参与计数', { 'm.a': 'Hello', 'm.c': 'Hello' }, ['m.a'], (q) => q.untranslated, 1)
  probe('L6', '本语种缺失的键不参与计数', {}, ['m.a'], (q) => q.referenced, 0)
  probe('L7', '参照语种没有该键 ⇒ 不算「逐字相同」', { 'm.x': 'Hello' }, ['m.x'], (q) => q.untranslated, 0)
  probe('L8', '同一值出现在多个键上 ⇒ 按键计数（不按去重后的值计数）', { 'm.a': 'Hello', 'm.d': 'Hello' }, ['m.a', 'm.d'], (q) => q.untranslated, 2)
  return cases
}

export function assertSelfCheckCounting(): SelfCheckCase[] {
  const cases = selfCheckCounting()
  const failed = cases.filter((c) => !c.ok)
  if (failed.length > 0) {
    const detail = cases
      .map((c) => `  ${c.ok ? 'PASS' : 'FAIL'} ${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`)
      .join('\n')
    throw new Error(`locale value quality counter self-check failed:\n${detail}`)
  }
  return cases
}

// ─────────────────────────── baseline（棘轮：只许降不许升） ───────────────────────────

export interface Baseline {
  metric: string
  updated: string
  /** en-US 语种含汉字的值数（硬闸，目标 0） */
  enUSCjk: number
  /** 每个语种「值仍等于 en-US」的键数 */
  untranslated: Record<string, number>
  /** 非中日语种里含汉字的值数 */
  cjkInNonCjkLocale: Record<string, number>
}

export function readBaseline(): Baseline | null {
  try {
    return JSON.parse(readFileSync(BASELINE_FILE, 'utf8')) as Baseline
  } catch {
    return null
  }
}

/**
 * 写 baseline。**只许降不许升**：任何一项会升高都抛错。
 * 降基线是「把已修好的部分从门槛里划掉」，升基线是「把新缺陷合法化」——
 * 后者必须人工显式改 JSON，并在 commit 消息里写明理由。
 */
export function writeBaseline(current: Baseline): Baseline {
  const prev = readBaseline()
  if (prev) {
    const raises: string[] = []
    if (current.enUSCjk > prev.enUSCjk) {
      raises.push(`enUSCjk ${prev.enUSCjk} → ${current.enUSCjk}`)
    }
    for (const [loc, n] of Object.entries(current.untranslated)) {
      const before = prev.untranslated?.[loc]
      if (typeof before === 'number' && n > before) raises.push(`untranslated.${loc} ${before} → ${n}`)
    }
    for (const [loc, n] of Object.entries(current.cjkInNonCjkLocale)) {
      const before = prev.cjkInNonCjkLocale?.[loc]
      if (typeof before === 'number' && n > before) raises.push(`cjkInNonCjkLocale.${loc} ${before} → ${n}`)
    }
    if (raises.length > 0) {
      throw new Error(
        `baseline 棘轮只许降不许升；以下项会升高：\n  ${raises.join('\n  ')}\n` +
          '「把基线调高」必须人工显式改 JSON 并在 commit 消息里写明理由。',
      )
    }
  }
  const next: Baseline = { ...current, updated: new Date().toISOString().slice(0, 10) }
  writeFileSync(BASELINE_FILE, JSON.stringify(next, null, 2) + '\n', 'utf8')
  return next
}

/** 由实测值构造一份 baseline 草稿（供 CLI --update-baseline 使用）。 */
export function buildBaseline(byLocale: Map<string, LocaleQuality>): Baseline {
  const untranslated: Record<string, number> = {}
  const cjkInNonCjkLocale: Record<string, number> = {}
  let enUSCjk = 0
  for (const loc of ALL_LOCALES) {
    const q = byLocale.get(loc)
    if (!q) continue
    if (loc === REFERENCE_LOCALE) {
      enUSCjk = q.cjkInValue
      continue
    }
    untranslated[loc] = q.untranslated
    if (!CJK_NATIVE_LOCALES.has(loc)) cjkInNonCjkLocale[loc] = q.cjkInValue
  }
  return {
    metric:
      'referenced keys only; values = runtime truth (vm-evaluated locale modules); ' +
      'untranslated = value byte-identical to en-US; cjk = /[\\u4e00-\\u9fff\\u3400-\\u4dbf]/',
    updated: new Date().toISOString().slice(0, 10),
    enUSCjk,
    untranslated,
    cjkInNonCjkLocale,
  }
}
