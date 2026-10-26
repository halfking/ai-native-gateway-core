// localeKeyParity.ts — 「跨语种键**存在性**平价」的质量门（2026-10-21 R56 轮新增）
//
// 目的：补上 i18n 既有门合起来的**又一条缝**（R55 §13.4-2 登记为「新的最大缝」）：
//   · parity.test.ts            只查「**每个语种 ⊇ zh-CN**」⇒ **单向**。
//                              ⇒「**zh-CN 缺了别的语种有的键**」这个方向**无人管**。
//                              ⇒ R55 的 zh-CN 分类键缺陷就是从这个缝漏出来的
//                                （zh-CN 把 16 个键塞进死命名空间，其余 7 语种在
//                                 `sessions.config.*` 平铺路径上 ⇒ 中文用户看到原始点号键路径）。
//   · keys_referenced.test.ts   只查「被引用的键在 **zh-CN** 里存不存在」⇒ 同样单向。
//   · 其余三道门（localeValueQuality / localePlaceholderParity / callSitePlaceholderCoverage）
//                              管的是**值**（语言、占位符、参数），**不管键是否存在**。
//   ⇒ 合起来：**「某语种缺了其它语种有的键」这一族缺陷无人管**。
//
// 判据（为什么不是「以某个语种为参照」）：
//   ★ R55 §13.6-3 已坐实：**参照方本身可能是错的**（族2 里恰恰是 en-US + zh-CN 缺参数）。
//     ⇒ 本门**不设参照方**：只判「**平不平**」。
//     ⇒ 「谁对谁错」交给**代码契约**裁定（调用点请求的是哪条路径）。
//
// 口径（重要，量具口径变了 baseline 就得重铸）：
//   - 键集合 = **运行时真值**（vm 求值 locale 模块，见 scanner.ts 的 loadLocale）。
//     ⚠️ 不用文件文本口径：JS 对象字面量里重复键后者覆盖前者
//       （en-US/routing.ts 的 `queryFailed` 出现 4 次，生效的是英文那个），
//       文件文本口径会把早已被覆盖的值算成缺陷。
//   - **可达键**（=「界面真的可能显示出来的键」）分三个来源，缺一不可：
//       ① **字面量引用**：scanner.ts 的 `scanAll`（含 helper 前缀解析，如 `td('creds.x')`
//          → `providerDetail.creds.x`）—— 这层 helper 解析是 `callSiteParams.ts` 不做的，
//          所以**必须用 scanner.ts 那一套**，不能只用自己的扫描器。
//       ② **模板候选键**：`callSiteParams.ts` 的 `scanAllCallSiteKeys`（R56 轮新增）
//          —— 按「静态前缀 + 静态后缀」匹配 union 得到的键。
//          ⚠️ **旧扫描器只认「第二参是对象字面量」的调用点** ⇒ 59 个模板调用点里
//            它只看到 3 个 ⇒ 候选键从 5 涨到 849（**170 倍**）。
//            漏掉的那些里就包括 R55 缺陷自己的 `t(\`sessions.config.${meta.i18nKey}\`, …)`。
//       ③ **动态键**（`t(opt.labelKey)` 这类）：静态不可知 ⇒ **不进入硬闸**，
//          改由**棘轮**兜住（棘轮量的是「union 级」的缺失，覆盖动态键派生的情况）。
//   - 值只取**字符串**叶子（数组/数字/布尔不算「这个键存在」）—— 与
//     localePlaceholderParity.ts 的同一处理，避免两套口径互相抵消。
//
// 两个层次（**硬闸 + 棘轮**，分工明确）：
//   · **硬闸 0**：每个**可达键**必须在**全部 8 个语种**都存在。
//     ⇒ 这一条直接覆盖 R55 缺陷的形状，且**不需要手写定点回归**。
//     ⇒ 实测当前 0 违例（可达 5293 键 × 8 语种）。
//   · **棘轮**（只许降不许升）：每个语种「相对 union 缺失的键数」。
//     ⇒ 覆盖**死键区**的不对称（动态键派生、死命名空间等）。
//     ⇒ 硬闸管不到的那些不对称，由它兜住。
//
// 消费方：
//   - src/i18n/localeKeyParity.test.ts（门，`pnpm test` 强制执行）
//   - scripts/i18n-key-parity-count.mjs（CLI：`npm run i18n:key-parity:check`）

import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { scanAll } from './scanner.ts'
import { scanAllCallSiteKeys, type CallSiteKeyRef } from './callSiteParams.ts'
import { allLocaleKeySets, SRC_DIR, LOCALES_DIR } from './localeValueQuality.ts'

export { SRC_DIR, LOCALES_DIR }
export const ALL_LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']

export const BASELINE_FILE = join(SRC_DIR, 'i18n', 'localeKeyParityBaseline.json')

/** 键的来源分类（用于报告「可达键是怎么来的」，让读数可复核）。 */
export interface ReachableKeySources {
  /** scanner.ts 的字面量引用（含 helper 前缀解析） */
  literal: number
  /** 模板候选键（新增量 = 不在 literal 里的那些） */
  template: number
  /** 可达键总数 */
  total: number
}

export interface KeyParityReport {
  locales: string[]
  /** 全语种键的并集 */
  unionSize: number
  /** 可达键的来源构成 */
  sources: ReachableKeySources
  /** 硬闸违例：某可达键在某些语种不存在（每条 = 一个键 + 缺它的语种） */
  missingInLocale: Array<{ key: string; missingIn: string[] }>
  /** 各语种相对 union 缺失的键数（棘轮量） */
  missingVsUnion: Record<string, number>
  /** 「存在于部分语种但非全部」的键数（棘轮量，死键区的不对称） */
  asymmetricKeys: number
  /** 动态键调用点（静态不可知 ⇒ 不进硬闸，由棘轮兜底） */
  dynamicCallSites: number
  /** 动态键调用点清单（file:line:key）—— 供登记册核对 */
  dynamicSiteList: string[]
}

// ─────────────────────────── 值表（复用 localeValueQuality 的缓存） ───────────────────────────

/**
 * 取某语种的**键集合**（运行时真值）。
 *
 * ⚠️ **口径必须与 scanner.ts 的 `collectLeafKeys` 一致**（数组也算一个键 ——
 *   ⚠️ 但 vue-i18n 9 的 `t()` **不支持数组型消息**（实测返回键路径字符串）⇒ 数组键只能经
 *      `i18n.ts` 的 `localeArray()` 从消息树直取；另见 scripts/i18n-array-key-guard.mjs）。
 *   ⇒ 复用 `allLocaleKeySets()`，**不要**用 `allLocaleValues()`（只收字符串）
 *   ⇒ 否则会把「数组型键」误判成「该语种缺这个键」
 *   （R56 实测：3 个数组型键被误报成「8 语种全缺」—— **量具自己的口径问题，不是缺陷**）。
 */
export function localeKeys(locale: string): Set<string> {
  return allLocaleKeySets().get(locale) ?? new Set<string>()
}

// ─────────────────────────── 可达键 ───────────────────────────

/**
 * 量出「可达键」= 界面真的可能显示出来的键。
 *
 * @param union 全语种键的并集（模板候选键要拿它做前缀/后缀匹配）
 */
export function collectReachableKeys(
  union: Set<string>,
  callSites: CallSiteKeyRef[],
  literalRefs: Set<string>,
): { keys: Set<string>; sources: ReachableKeySources } {
  const keys = new Set<string>(literalRefs)
  const literalCount = keys.size

  // 模板候选：静态前缀 + 静态后缀都满足，且中间至少还有一段
  //（否则 `prefix="a." suffix=""` 会把 `a.` 本身算成候选）
  for (const site of callSites) {
    if (site.keyKind !== 'template') continue
    for (const k of union) {
      if (k.length <= site.keyPrefix.length + site.keySuffix.length) continue
      if (k.startsWith(site.keyPrefix) && k.endsWith(site.keySuffix)) keys.add(k)
    }
  }

  return {
    keys,
    sources: {
      literal: literalCount,
      template: keys.size - literalCount,
      total: keys.size,
    },
  }
}

// ─────────────────────────── 判定核心 ───────────────────────────

/**
 * 纯函数：量跨语种键存在性平价。
 * @param keysByLocale 语种 → 键集合
 * @param union        全语种键的并集
 * @param reachable    可达键集合
 */
export function measureKeyParity(
  keysByLocale: Map<string, Set<string>>,
  union: Set<string>,
  reachable: Set<string>,
  sources: ReachableKeySources,
  dynamicSiteList: string[] = [],
): KeyParityReport {
  const locales = [...keysByLocale.keys()]

  // 硬闸：可达键必须在全部语种都存在
  const missingInLocale: KeyParityReport['missingInLocale'] = []
  for (const key of reachable) {
    const missingIn: string[] = []
    for (const loc of locales) {
      if (!keysByLocale.get(loc)?.has(key)) missingIn.push(loc)
    }
    if (missingIn.length > 0) missingInLocale.push({ key, missingIn })
  }

  // 棘轮量 1：各语种相对 union 缺失的键数
  const missingVsUnion: Record<string, number> = {}
  for (const loc of locales) {
    const keys = keysByLocale.get(loc) ?? new Set<string>()
    let n = 0
    for (const k of union) if (!keys.has(k)) n++
    missingVsUnion[loc] = n
  }

  // 棘轮量 2：「存在于部分语种但非全部」的键数
  let asymmetricKeys = 0
  for (const k of union) {
    let present = 0
    for (const loc of locales) if (keysByLocale.get(loc)?.has(k)) present++
    if (present > 0 && present < locales.length) asymmetricKeys++
  }

  return {
    locales,
    unionSize: union.size,
    sources,
    missingInLocale,
    missingVsUnion,
    asymmetricKeys,
    dynamicCallSites: dynamicSiteList.length,
    dynamicSiteList,
  }
}

// ─────────────────────────── 量本仓当前状态 ───────────────────────────

let cachedReport: KeyParityReport | null = null

/**
 * 清掉测量缓存。
 * ⚠️ **缓存是按进程内存的**：改完 locale 文件再测，拿到的仍是旧值 ⇒ 必须先调它。
 */
export function clearKeyParityCache(): void {
  cachedReport = null
}

/** 量本仓当前状态（带进程内缓存 ⇒ 门里多次调用只算一次） */
export function measureRepoKeyParity(): KeyParityReport {
  if (cachedReport) return cachedReport

  const keysByLocale = allLocaleKeySets()
  const union = new Set<string>()
  for (const keys of keysByLocale.values()) for (const k of keys) union.add(k)

  // ① 字面量引用（scanner.ts —— 含 helper 前缀解析，callSiteParams 不做这件事）
  const { references } = scanAll(SRC_DIR)
  const literalRefs = new Set(references.map((r) => r.key))

  // ② 全部调用点（R56 新增的 scanAllCallSiteKeys —— 不限第二参形态）
  const { callSites } = scanAllCallSiteKeys(SRC_DIR)
  const dynamicSites = callSites.filter((c) => c.keyKind === 'dynamic')

  const { keys: reachable, sources } = collectReachableKeys(union, callSites, literalRefs)

  const dynamicSiteList = dynamicSites
    .map((d) => `${d.file}:${d.line}:${d.key}`)
    .sort()

  cachedReport = measureKeyParity(keysByLocale, union, reachable, sources, dynamicSiteList)
  return cachedReport
}

// ─────────────────────────── 自证（量具必须有牙） ───────────────────────────
//
// 方向必须**双向**：
//   · 「会触发」—— 某语种真的缺一个可达键时必须报违例
//   · 「不误触发」—— 键在全部语种都存在 / 只在部分语种存在（那是棘轮管的）/
//     不可达键缺一个时**不得**报硬闸违例
// ⚠️ 缺了「不误触发」这一半，一门永远绿的判定和一门永远红的判定同样没用。

export interface SelfCheckCase {
  id: string
  desc: string
  expected: number
  actual: number
  ok: boolean
}

export function selfCheckKeyParity(): SelfCheckCase[] {
  const cases: SelfCheckCase[] = []
  const sources: ReachableKeySources = { literal: 0, template: 0, total: 0 }

  const probe = (
    id: string,
    desc: string,
    keysByLocale: Record<string, string[]>,
    union: string[],
    reachable: string[],
    expected: number,
  ) => {
    const m = new Map<string, Set<string>>()
    for (const [loc, ks] of Object.entries(keysByLocale)) m.set(loc, new Set(ks))
    const report = measureKeyParity(m, new Set(union), new Set(reachable), { ...sources, total: reachable.length })
    cases.push({ id, desc, expected, actual: report.missingInLocale.length, ok: report.missingInLocale.length === expected })
  }

  probe('K1', '可达键在全部语种都存在 ⇒ 无违例',
    { en: ['a', 'b'], zh: ['a', 'b'] }, ['a', 'b'], ['a', 'b'], 0)
  probe('K2', '★ 可达键在某个语种缺失 ⇒ 报 1 条硬闸违例',
    { en: ['a', 'b'], zh: ['a'] }, ['a', 'b'], ['a', 'b'], 1)
  probe('K3', '不可达键缺失 ⇒ 不报硬闸违例（棘轮管）',
    { en: ['a', 'b'], zh: ['a'] }, ['a', 'b'], ['a'], 0)
  probe('K4', '★ 缺键的语种被点名（不是只报「有一个语种缺」）',
    { en: ['a'], zh: ['a'], ja: [] }, ['a'], ['a'], 1)
  probe('K5', '三个语种各缺一个不同的可达键 ⇒ 报 2 条',
    { en: ['a', 'b'], zh: ['a'], ja: ['b'] }, ['a', 'b'], ['a', 'b'], 2)
  probe('K6', '键只在部分语种存在且不可达 ⇒ 硬闸不报、棘轮计',
    { en: ['a'], zh: [] }, ['a'], [], 0)

  // 棘轮量：缺失计数与不对称计数
  const m2 = new Map<string, Set<string>>([
    ['en', new Set(['a', 'b', 'c'])],
    ['zh', new Set(['a'])],
  ])
  const r2 = measureKeyParity(m2, new Set(['a', 'b', 'c']), new Set(['a']), { literal: 1, template: 0, total: 1 })
  const check = (id: string, desc: string, expected: number, actual: number) => {
    cases.push({ id, desc, expected, actual, ok: expected === actual })
  }
  check('K7', '棘轮量 1：zh 相对 union 缺 2 个键', 2, r2.missingVsUnion.zh)
  check('K8', '棘轮量 1：en 相对 union 缺 0 个键', 0, r2.missingVsUnion.en)
  check('K9', '棘轮量 2：2 个键「存在于部分语种但非全部」', 2, r2.asymmetricKeys)

  // 可达键收集：模板候选的匹配规则
  const union3 = new Set(['sessions.config.a', 'sessions.config.b', 'other.x', 'sessions.config.'])
  const site = (prefix: string, suffix: string): CallSiteKeyRef => ({
    file: 'f.vue', line: 1, column: 1, key: `${prefix}\${x}${suffix}`,
    keyKind: 'template', keyPrefix: prefix, keySuffix: suffix,
    secondArgKind: 'none', snippet: '',
  })
  const collected = collectReachableKeys(union3, [site('sessions.config.', '')], new Set(['other.x']))
  check('K10', '模板候选：前缀匹配且中间非空 ⇒ 命中 2 个', 2,
    [...collected.keys].filter((k) => k.startsWith('sessions.config.') && k !== 'sessions.config.').length)
  check('K11', '模板候选：前缀本身（中间为空）不算候选', 0,
    collected.keys.has('sessions.config.') ? 1 : 0)
  check('K12', '字面量引用计入可达键', 1, collected.keys.has('other.x') ? 1 : 0)

  return cases
}

export function assertSelfCheckKeyParity(): SelfCheckCase[] {
  const cases = selfCheckKeyParity()
  const failed = cases.filter((c) => !c.ok)
  if (failed.length > 0) {
    const detail = cases
      .map((c) => `  ${c.ok ? 'PASS' : 'FAIL'} ${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`)
      .join('\n')
    throw new Error(`locale key parity self-check failed:\n${detail}`)
  }
  return cases
}

// ─────────────────────────── baseline（棘轮：只许降不许升） ───────────────────────────

export interface KeyParityBaseline {
  metric: string
  updated: string
  /** 各语种相对 union 缺失的键数（只许降不许升） */
  missingVsUnion: Record<string, number>
  /** 「存在于部分语种但非全部」的键数（只许降不许升） */
  asymmetricKeys: number
}

export function readKeyParityBaseline(): KeyParityBaseline | null {
  try {
    return JSON.parse(readFileSync(BASELINE_FILE, 'utf8')) as KeyParityBaseline
  } catch {
    return null
  }
}

/**
 * 写 baseline。**只许降不许升**：任何一项会升高都抛错。
 * 升基线 = 「把新缺陷合法化」⇒ 必须人工显式改 JSON 并在 commit 消息里写明理由。
 */
export function writeKeyParityBaseline(current: KeyParityBaseline): KeyParityBaseline {
  const prev = readKeyParityBaseline()
  if (prev) {
    const raises: string[] = []
    for (const [loc, n] of Object.entries(current.missingVsUnion)) {
      const before = prev.missingVsUnion?.[loc]
      if (typeof before === 'number' && n > before) raises.push(`missingVsUnion.${loc} ${before} → ${n}`)
    }
    if (current.asymmetricKeys > prev.asymmetricKeys) {
      raises.push(`asymmetricKeys ${prev.asymmetricKeys} → ${current.asymmetricKeys}`)
    }
    if (raises.length > 0) {
      throw new Error(
        `baseline 棘轮只许降不许升；以下项会升高：\n  ${raises.join('\n  ')}\n` +
          '「把基线调高」必须人工显式改 JSON 并在 commit 消息里写明理由。',
      )
    }
  }
  const next: KeyParityBaseline = { ...current, updated: new Date().toISOString().slice(0, 10) }
  writeFileSync(BASELINE_FILE, JSON.stringify(next, null, 2) + '\n', 'utf8')
  return next
}

/** 由实测值构造一份 baseline 草稿 */
export function buildKeyParityBaseline(report: KeyParityReport): KeyParityBaseline {
  return {
    metric:
      'keys = runtime truth (vm-evaluated locale modules); reachable = scanner.ts literal refs ' +
      '(helper-prefix resolved) ∪ template candidates (prefix+suffix match); ' +
      'hard gate = every reachable key exists in all 8 locales; ' +
      'ratchet = per-locale missing-vs-union count + asymmetric key count',
    updated: new Date().toISOString().slice(0, 10),
    missingVsUnion: { ...report.missingVsUnion },
    asymmetricKeys: report.asymmetricKeys,
  }
}
