#!/usr/bin/env node
// i18n-empty-state-guard.mjs — 「空状态必须能区分状态种类」门
// （2026-10-27 第 14 增量 · 空状态 5 分轮）
//
// 用法：
//   node scripts/i18n-empty-state-guard.mjs            # 检查；违规 ⇒ exit 1
//   node scripts/i18n-empty-state-guard.mjs --json     # 输出 JSON 报告
//   node scripts/i18n-empty-state-guard.mjs --selftest # 判据自证
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 为什么要有这道门（**这道门测的是 impeccable 要的东西，不是「措辞通顺」**）
//
//   impeccable `skill/reference/clarify.md` 原文：
//     "An empty state distinguishes first use, no results, filters,
//      permissions, and failure. Explain the state and provide the next
//      useful action."
//
//   ⇒ 硬要求是**「区分」**：一句「暂时没有内容可显示」把 5 种状态压成 1 种，
//     即使它「读起来通顺」也是**不合格**的空状态。
//
//   ★ 本仓**已有的**语言包侧门（`src/i18n/copyClarityLocale.ts` 的 L3）测的是
//     **另一个东西**：它只判「值整句等于通用空状态短语」。
//     ⇒ 一句「暂时没有内容可显示」**通不过** L3（不是通用短语），
//       但它**恰恰是**本轮要治的病 —— 于是那道门是**绿的**，而界面在混同状态。
//     ★ ⇒ 「门绿 ≠ 门有牙」：**测的东西不是要的 ⇒ 门绿了也说明不了问题**。
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 判据（5 条，全部可证伪）
//
//   ① 退役键不许回来：4 个通用空态键（`common.feedback.noData` /
//      `dashboard.board.empty` / `hyper.list.empty` / `dashboard.noData`）
//      的**源码引用数必须为 0**，且**在 8 个语言包里都不存在**。
//      ⇒ 棘轮：一旦有人为了省事复用通用键，本门立刻红。
//
//   ② 登记键 8 语种齐备（与 parity 门同事实，但本门自己再断言一次 ——
//      「同一事实两处独立断言」比「一处断言」更能挡住量具漂移）。
//
//   ③ 键名必须带**状态种类标记**（`empty<Kind>`；`common.empty.<kind>` 亦可）
//      ⇒ 「可区分」这件事**进结构**，不靠写文案时的自觉。
//      ★ 这是本门与 L3 的**分工**：L3 管措辞，本门管结构。
//
//   ④ 登记键的值**不得是通用空状态短语**（复用 `LOCALE_GENERIC_EMPTY`，
//      ★ 不在本文件里重写这张表 —— 「同一口径只能有一份实现」）。
//
//   ⑤ 同语种内登记键的值**必须互不相同** ⇒ 不同页面不许说同一句话
//      （impeccable：「Say each idea once」的反面：不许把 N 种状态讲成 1 句）。
//
//   ⑥ 登记键的**源码引用数必须 ≥ 1** ⇒ 登记了却没人用的键就是死键
//      （第 13 增量刚清掉 3315 个死键；登记簿不该成为死键的新家）。
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 口径来源（复用优先，但**引用集这一项必须自己定义**）
//   · 语言包键集合 / 值表 ⇒ `src/i18n/localeValueQuality.ts`
//   · 通用空状态短语表 ⇒ `src/i18n/copyClarityLocale.ts` 的 `LOCALE_GENERIC_EMPTY`
//   · 源码引用集 ⇒ **本文件自己的 `collectCallSiteRefKeys()`**（只认真取键调用）
//     ★ **不用**死键门的 `collectStaticRefKeys()`：它刻意保守（所有点分字面量都算引用）
//       ⇒ 在死键方向是优点，在「这个键被用了吗」这个问题上是错的（注释/断言会被算成引用）。
//       ★ 详见 `collectCallSiteRefKeys()` 的注释与 LESSONS §424 形态七。

import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(fileURLToPath(import.meta.url), '../..')
const REGISTRY_FILE = join(ROOT, 'scripts', 'i18n-empty-state-registry.json')
const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']

function readRegistry(file = REGISTRY_FILE) {
  return JSON.parse(readFileSync(file, 'utf8'))
}

/** 键名末段是否带状态种类标记（大小写不敏感）。 */
export function hasKindMarker(key, kinds) {
  const last = key.split('.').pop() || ''
  const lower = last.toLowerCase()
  return kinds.some((k) => lower.includes(String(k).toLowerCase()))
}

/**
 * ★★ 只数「**真取键调用**」的引用集。
 *
 * ★ 为什么不用死键门的 `collectStaticRefKeys()`：
 *   它刻意**保守**（把「所有点分键路径形态的字面量」都算成引用 ⇒ 少删键）——
 *   在**死键**方向这是优点；★ 但在「这个键**被用**了吗」这个问题上它是错的：
 *   **注释里提到一个键名、测试里断言一个键名，都会被算成「在用」**。
 *   ⇒ 实测（本轮）：注释里写 `dashboard.board.empty`（讲旧版行为）、
 *     测试里写 `expect(used.has('hyper.list.empty')).toBe(false)`（断言它**不该**出现）
 *     —— 两者都被算成「引用」⇒ 门误报 3 处。
 *   ⇒ 本门只问「这个键**被当成键取用**了吗」⇒ 只认**取键调用形态**。
 *   ★ 这不是「放宽判据」，是**把判据对准被问的那件事**（见 LESSONS §424 形态七）。
 */
const CALL_SITE_RE = /\b(?:\$?t|tc|te|tm|TE|TM|TC|localeArray|i18n\.t)(?:<[^>]*>)?\s*\(\s*(['"`])([A-Za-z][A-Za-z0-9_.\-]*)\1/g

/** 纯函数：从一段源码里取出「真取键调用」的键集（★ 自证直接驱动它）。 */
export function collectCallSiteRefKeysFrom(src) {
  const keys = new Set()
  CALL_SITE_RE.lastIndex = 0
  let m
  while ((m = CALL_SITE_RE.exec(src)) !== null) keys.add(m[2])
  return keys
}

export function collectCallSiteRefKeys() {
  const keys = new Set()
  const walk = (dir) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name)
      if (e.isDirectory()) {
        if (e.name === 'locales' || e.name === 'node_modules') continue
        walk(p)
        continue
      }
      if (!/\.(vue|ts|tsx|js|mjs)$/.test(e.name)) continue
      const src = readFileSync(p, 'utf8')
      for (const k of collectCallSiteRefKeysFrom(src)) keys.add(k)
    }
  }
  walk(join(ROOT, 'src'))
  return keys
}

/**
 * 核心判据（**纯函数**：给它事实源，它给违规清单 ⇒ 自证可以喂假事实）。
 * @param {object} facts
 * @param {Set<string>} facts.refKeys        源码引用键集
 * @param {Map<string, Set<string>>} facts.perLocaleKeys  各语种键集合
 * @param {Map<string, object>} facts.perLocaleValues      各语种值表（fullKey → 值）
 * @param {object} facts.registry           登记簿
 * @param {object<string, Set<string>>} facts.genericEmpty  各语种通用空状态短语表
 */
export function checkEmptyStateFacts(facts) {
  const { refKeys, perLocaleKeys, perLocaleValues, registry, genericEmpty } = facts
  const violations = []
  const kinds = registry.kindVocabulary || []
  const add = (kind, detail, extra = {}) => violations.push({ kind, detail, ...extra })

  // ① 退役键：源码 0 引用 + 8 语种都不存在
  for (const dead of registry.retiredKeys || []) {
    if (refKeys.has(dead)) {
      add('retired-key-referenced', `退役的通用空态键「${dead}」又出现在源码里（引用集命中）`, { key: dead })
    }
    for (const loc of LOCALES) {
      if (perLocaleKeys.get(loc)?.has(dead)) {
        add('retired-key-present', `退役的通用空态键「${dead}」仍在 ${loc} 语言包里`, { key: dead, locale: loc })
      }
    }
  }

  // ②③④⑤⑥ 登记键
  const seenByLocale = new Map(LOCALES.map((l) => [l, new Map()]))
  for (const entry of registry.keys || []) {
    const key = entry.key
    const kind = entry.kind
    if (!kinds.includes(kind)) {
      add('unknown-kind', `登记键「${key}」的 kind「${kind}」不在状态种类词表里`, { key })
    }
    // ③ 键名带 kind 标记
    if (!hasKindMarker(key, kinds)) {
      add('missing-kind-marker', `登记键「${key}」的键名末段没有状态种类标记（应含 ${kinds.join('/')}）`, { key })
    }
    // ⑥ 至少被引用一次（登记簿不是死键的新家）
    if (!refKeys.has(key)) {
      add('registered-key-unreferenced', `登记键「${key}」源码 0 引用 ⇒ 它是死键（要么接上引用，要么从登记簿撤掉）`, { key })
    }
    for (const loc of LOCALES) {
      // ② 8 语种齐备
      if (!perLocaleKeys.get(loc)?.has(key)) {
        add('missing-in-locale', `登记键「${key}」在 ${loc} 语言包里不存在`, { key, locale: loc })
        continue
      }
      const values = perLocaleValues.get(loc) || {}
      const v = values[key]
      // ④ 不得是通用空状态短语
      const gen = genericEmpty[loc] || new Set()
      if (typeof v === 'string' && gen.has(v.replace(/\s+/g, ' ').trim())) {
        add('generic-empty-phrase', `登记键「${key}」在 ${loc} 的值是通用空状态短语（${v}）⇒ 没点名缺的是什么`, { key, locale: loc, value: v })
      }
      // ⑤ 同语种内值互不相同
      const norm = typeof v === 'string' ? v.replace(/\s+/g, ' ').trim() : String(v)
      const bucket = seenByLocale.get(loc)
      if (bucket.has(norm)) {
        add('duplicate-value', `${loc} 语言包里「${bucket.get(norm)}」与「${key}」值完全相同（${norm}）⇒ 不同页面不许说同一句话`, { key, locale: loc, other: bucket.get(norm) })
      } else {
        bucket.set(norm, key)
      }
    }
  }

  return violations
}

/** 载入事实源（★ 主流程与自证用同一个 loader ⇒ 同源）。 */
export async function loadFacts() {
  const quality = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'localeValueQuality.ts')).href)
  const clarity = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'copyClarityLocale.ts')).href)
  const registry = readRegistry()
  const refKeys = collectCallSiteRefKeys()
  const perLocaleKeys = quality.allLocaleKeySets(join(ROOT, 'src', 'locales'))
  const perLocaleValues = quality.allLocaleValues(join(ROOT, 'src', 'locales'))
  return { refKeys, perLocaleKeys, perLocaleValues, registry, genericEmpty: clarity.LOCALE_GENERIC_EMPTY }
}

async function selftest() {
  const cases = []
  const check = (id, desc, expected, actual) => {
    cases.push({ id, desc, expected, actual, ok: JSON.stringify(expected) === JSON.stringify(actual) })
  }

  // ── 纯函数层的自证（喂假事实 ⇒ 不依赖仓库状态）──
  const reg = {
    kindVocabulary: ['FirstUse', 'NoResult', 'Filtered', 'Failed', 'NoPermission'],
    retiredKeys: ['old.generic'],
    keys: [
      { key: 'ns.a.emptyNoResult', kind: 'NoResult' },
      { key: 'ns.b.emptyFiltered', kind: 'Filtered' },
    ],
  }
  /**
   * 造一份**8 语种齐全**的干净事实源。
   * ★ 教训（第 11 增量 §418）：夹具本身若有缺口，期望值就会写成「一堆不相干的违规」
   *   ⇒ 断言变成「恒非空」，看不出门到底在测什么。
   *   ⇒ 这里让每个语种都带上两个登记键，且**不放**退役键。
   */
  const mkFacts = (over = {}) => {
    const perLocaleKeys = new Map()
    const perLocaleValues = new Map()
    for (const loc of LOCALES) {
      perLocaleKeys.set(loc, new Set(['ns.a.emptyNoResult', 'ns.b.emptyFiltered']))
      perLocaleValues.set(loc, {
        'ns.a.emptyNoResult': `所选时段内没有供应商数据。（${loc}）`,
        'ns.b.emptyFiltered': `当前筛选条件下没有会话记录。（${loc}）`,
      })
    }
    return {
      registry: reg,
      genericEmpty: { 'zh-CN': new Set(['暂无数据']) },
      refKeys: new Set(['ns.a.emptyNoResult', 'ns.b.emptyFiltered']),
      perLocaleKeys,
      perLocaleValues,
      ...over,
    }
  }
  const kindsOf = (vs) => vs.map((v) => v.kind).sort()
  const only = (vs, kind) => vs.filter((v) => v.kind === kind)

  const clean = checkEmptyStateFacts(mkFacts())
  check('E1', '干净事实源（8 语种齐备 + 无退役键）⇒ 零违规', [], clean)

  // E2 退役键回到源码 ⇒ 判红
  const v2 = checkEmptyStateFacts(mkFacts({ refKeys: new Set(['ns.a.emptyNoResult', 'ns.b.emptyFiltered', 'old.generic']) }))
  check('E2', '退役的通用空态键又出现在源码 ⇒ 判红', ['retired-key-referenced'], kindsOf(only(v2, 'retired-key-referenced')))

  // E3 退役键回到语言包 ⇒ 判红
  const ks = new Map(mkFacts().perLocaleKeys)
  ks.set('zh-CN', new Set([...ks.get('zh-CN'), 'old.generic']))
  const v3 = checkEmptyStateFacts(mkFacts({ perLocaleKeys: ks }))
  check('E3', '退役键仍在语言包里 ⇒ 判红', ['retired-key-present'], kindsOf(only(v3, 'retired-key-present')))

  // E4 键名缺 kind 标记 ⇒ 判红
  const v4 = checkEmptyStateFacts(mkFacts({ registry: { ...reg, keys: [{ key: 'ns.a.empty', kind: 'NoResult' }] } }))
  check('E4', '键名末段没有状态种类标记 ⇒ 判红', ['missing-kind-marker'], kindsOf(only(v4, 'missing-kind-marker')))

  // E5 值是通用空状态短语 ⇒ 判红
  const pv = new Map(mkFacts().perLocaleValues)
  pv.set('zh-CN', { ...pv.get('zh-CN'), 'ns.a.emptyNoResult': '暂无数据' })
  const v5 = checkEmptyStateFacts(mkFacts({ perLocaleValues: pv }))
  check('E5', '值整句等于通用空状态短语 ⇒ 判红', ['generic-empty-phrase'], kindsOf(only(v5, 'generic-empty-phrase')))

  // E6 同语种内两个键值相同 ⇒ 判红
  const pv2 = new Map(mkFacts().perLocaleValues)
  pv2.set('zh-CN', { ...pv2.get('zh-CN'), 'ns.b.emptyFiltered': '所选时段内没有供应商数据。（zh-CN）' })
  const v6 = checkEmptyStateFacts(mkFacts({ perLocaleValues: pv2 }))
  check('E6', '同语种内两个登记键值完全相同 ⇒ 判红', ['duplicate-value'], kindsOf(only(v6, 'duplicate-value')))

  // E7 登记键 0 引用 ⇒ 判红（登记簿不许变成死键的新家）
  const v7 = checkEmptyStateFacts(mkFacts({ refKeys: new Set(['ns.a.emptyNoResult']) }))
  check('E7', '登记键源码 0 引用 ⇒ 判红', ['registered-key-unreferenced'], kindsOf(only(v7, 'registered-key-unreferenced')))

  // E8 缺语种 ⇒ 判红
  const ks8 = new Map(mkFacts().perLocaleKeys)
  ks8.delete('ja-JP')
  const v8 = checkEmptyStateFacts(mkFacts({ perLocaleKeys: ks8 }))
  check('E8', '登记键缺语种 ⇒ 判红', true, v8.some((x) => x.kind === 'missing-in-locale'))

  // E9 kind 标记识别器本身
  check('E9', 'kind 标记识别：emptyNoResultProviders ⇒ true', true, hasKindMarker('ns.emptyNoResultProviders', reg.kindVocabulary))
  check('E9b', 'kind 标记识别：ns.empty（无标记）⇒ false', false, hasKindMarker('ns.empty', reg.kindVocabulary))
  check('E9c', 'kind 标记识别：common.empty.noResult ⇒ true', true, hasKindMarker('common.empty.noResult', reg.kindVocabulary))

  // E9d ★★ 「注释 / 断言里的键名不算引用」（LESSONS §424 形态七）
  //   本轮的实测：注释里写 `dashboard.board.empty`（讲旧版行为）、
  //   测试里写 `expect(used.has('hyper.list.empty')).toBe(false)`（断言它**不该**出现）
  //   —— 都被死键门那个保守引用集算成「在用」⇒ 门误报 3 处。
  const cs = collectCallSiteRefKeysFrom(
    "// 旧版用 dashboard.board.empty（讲历史行为）\nconst a = t('ns.a.emptyNoResult')\n" +
    "expect(used.has('ns.b.emptyFiltered')).toBe(false)\n"
  )
  check('E9d', '注释里的键名不进引用集', false, cs.has('dashboard.board.empty'))
  check('E9e', '真取键调用进引用集', true, cs.has('ns.a.emptyNoResult'))
  check('E9f', '测试里的断言字符串不进引用集（它不是取键调用）', false, cs.has('ns.b.emptyFiltered'))

  // ── 事实源层的自证（★ 必须真跑仓库事实，否则就是「门绿了但没跑」）──
  const facts = await loadFacts()
  const v10 = checkEmptyStateFacts(facts)
  check('E10', '★ 真仓事实源下本门零违规（登记簿与语言包/源码一致）', [], v10)

  // E11 ★ 登记簿的每个 kind 都必须在词表里；且已落地的 kind 至少覆盖 NoResult
  const kindsUsed = new Set((facts.registry.keys || []).map((k) => k.kind))
  check('E11', '登记簿用到的 kind 全在词表内', true, [...kindsUsed].every((k) => facts.registry.kindVocabulary.includes(k)))
  check('E11b', '登记簿至少用到 NoResult（无结果是最基本的一类）', true, kindsUsed.has('NoResult'))

  // E12 ★ 退役键在真仓里确实 0 引用且 8 语种都不存在
  check('E12', '★ 真仓：4 个退役键源码 0 引用', true,
    (facts.registry.retiredKeys || []).every((k) => !facts.refKeys.has(k)))
  check('E12b', '★ 真仓：4 个退役键 8 语种都不存在', true,
    (facts.registry.retiredKeys || []).every((k) => LOCALES.every((l) => !facts.perLocaleKeys.get(l)?.has(k))))

  let bad = 0
  console.log('══ 判据自证 ══')
  for (const c of cases) {
    if (!c.ok) bad++
    console.log(`  ${c.ok ? '✔' : '✘'} ${c.id} ${c.desc}（期望 ${JSON.stringify(c.expected)}，实测 ${JSON.stringify(c.actual)}）`)
  }
  console.log(`\n══ 自证 ${cases.length - bad}/${cases.length} ══`)
  if (bad) process.exit(1)
}

async function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    await selftest()
    return
  }
  const facts = await loadFacts()
  const violations = checkEmptyStateFacts(facts)
  const kindsUsed = {}
  for (const k of facts.registry.keys || []) kindsUsed[k.kind] = (kindsUsed[k.kind] || 0) + 1

  if (argv.includes('--json')) {
    console.log(JSON.stringify({
      registryKeys: (facts.registry.keys || []).length,
      retiredKeys: facts.registry.retiredKeys || [],
      kindsUsed,
      kindsUnused: (facts.registry.kindVocabulary || []).filter((k) => !kindsUsed[k]),
      violations,
    }, null, 2))
    if (violations.length) process.exit(1)
    return
  }

  console.log('=== 空状态 5 分门 ===')
  console.log('登记键: ' + (facts.registry.keys || []).length + ' 个；状态种类: ' +
    Object.entries(kindsUsed).map(([k, v]) => `${k}×${v}`).join(' '))
  const unused = (facts.registry.kindVocabulary || []).filter((k) => !kindsUsed[k])
  if (unused.length) console.log('（本轮未用到、但词表保留的种类: ' + unused.join(', ') + '）')
  console.log('')
  if (violations.length === 0) {
    console.log('✅ 空状态门 PASS（登记键 8 语种齐备 / kind 标记在位 / 值互不相同 / 通用键已退役）')
    return
  }
  console.log(`❌ 空状态门：${violations.length} 项违规`)
  const byKind = new Map()
  for (const v of violations) {
    if (!byKind.has(v.kind)) byKind.set(v.kind, [])
    byKind.get(v.kind).push(v)
  }
  for (const [k, list] of byKind) {
    console.log(`  [${k}] ${list.length} 项`)
    for (const v of list.slice(0, 40)) console.log('    - ' + v.detail)
    if (list.length > 40) console.log(`    … 共 ${list.length} 项`)
  }
  process.exit(1)
}

const isDirectRun = !!process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href
if (isDirectRun) {
  main().catch((e) => {
    console.error('i18n-empty-state-guard failed:', e && e.stack ? e.stack : e)
    process.exit(2)
  })
}
