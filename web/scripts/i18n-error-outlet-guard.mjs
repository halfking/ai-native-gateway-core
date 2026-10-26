#!/usr/bin/env node
// i18n-error-outlet-guard.mjs — 「用户可见错误文案必须经过单一出口」门
// （2026-10-27 第 16 增量 · 错误出口轮）
//
// 用法：
//   node scripts/i18n-error-outlet-guard.mjs            # 检查；违规 ⇒ exit 1
//   node scripts/i18n-error-outlet-guard.mjs --json     # 输出 JSON 报告
//   node scripts/i18n-error-outlet-guard.mjs --selftest # 判据自证
//   node scripts/i18n-error-outlet-guard.mjs --list     # 逐条列出豁免与出口键
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 这道门测的是 impeccable 要的东西
//
//   impeccable `skill/reference/clarify.md`：
//     "An actionable error answers: 1. what failed; 2. why, when known and useful;
//      3. how to recover or what alternative remains."
//     "Do not expose internal codes as the primary message."
//
// ★ 本仓的病（实测，不是推演）：
//   全仓 196 处 `error.value = e instanceof Error ? e.message : <兜底>`。
//   三元把 `.message` 放在**第一分支** ⇒ 兜底只在「抛出的不是 Error 实例」时触发
//   ⇒ **本地化兜底结构性失效** ⇒ 用户看到的是后端下发的英文技术串
//      （实测：admin 侧 1852 处硬编码错误文案，去重 631 条，仅 33 条中文）。
//   ⇒ 第 15 增量改写的 143 个语言包键，在这条主路径上**根本没上过屏**。
//
// ★ 本轮把「错误文案的唯一出口」定成 `src/i18n/errorSurface.ts` 的 `surfaceError()`：
//   主文案 = 主体专属键（点名主体 + 恢复动作）｜原因 = 原始 detail（次要）
//   ⇒ 这道门守住「不许再绕过出口」。
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 判据（4 条，全部可证伪）
//
//   ① O1 裸用：源码里不得再出现「`<x> instanceof Error ? <x>.message : …`」
//      且该表达式**自己就是**用户可见文案。
//      ⇒ 登记在豁免表里的「原因槽 / 拼接原因」形态除外（豁免必须自证仍然成立）。
//
//   ② O2 出口键齐备：`surfaceError(..., { key: 'X' })` 的 X 必须在 **8 语种**都存在。
//      ★ 这条防的是「接了出口，但键名写错/只写了一个语种」⇒ 运行时静默显示键路径。
//
//   ③ O3 状态层合规：`errors.byStatus.*` / `errors.network` / `errors.unknown`
//      （没有主体专属键时的兜底层）必须**含恢复动作**，且不是裸失败短语。
//      ⇒ 复用 L1 门（`i18n-error-clarity-guard.mjs`）的**同一把尺子**
//         （hasRecoveryAction / isBareFailurePhrase）—— 同一口径只能有一份实现。
//
//   ④ O4 原因后缀：`common.feedback.reasonSuffix` 必须 8 语种齐备且保留 `{reason}`。

import { readFileSync } from 'node:fs'
import { join, resolve, relative } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(fileURLToPath(import.meta.url), '../..')
const REGISTRY_FILE = join(ROOT, 'scripts', 'i18n-error-outlet-registry.json')
const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']

/** 状态层的键（没有主体专属键时的兜底层）。 */
export const STATUS_TIER_KEYS = [
  'errors.byStatus.400', 'errors.byStatus.401', 'errors.byStatus.403', 'errors.byStatus.404',
  'errors.byStatus.409', 'errors.byStatus.429', 'errors.byStatus.500', 'errors.byStatus.502',
  'errors.byStatus.503', 'errors.byStatus.504', 'errors.network', 'errors.unknown',
]
/** 原因后缀键（出口用来把原始 detail 挂在主文案后面）。 */
export const REASON_SUFFIX_KEY = 'common.feedback.reasonSuffix'

function readRegistry(file = REGISTRY_FILE) {
  return JSON.parse(readFileSync(file, 'utf8'))
}

// ───────────────────────── 原始形状扫描 ─────────────────────────

/**
 * 找出源码里全部「`<x> instanceof Error ? <x>.message : <fb>`」的站点。
 *
 * ★ 为什么要自己扫而不用一行正则：
 *   ① 兜底表达式里带括号（`emit('error', e instanceof Error ? e.message : t('x'))`）
 *      ⇒ 行级正则会把收尾的 `)` 一起吞进去。
 *   ② 形状可能跨行（`const message = (e) =>\n  e instanceof Error ? …`）。
 */
export function findRawMessageSites(src) {
  const sites = []
  const re = /([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)\s+instanceof\s+Error\s*\?/g
  let m
  while ((m = re.exec(src)) !== null) {
    const path = m[1]
    const start = m.index
    let i = re.lastIndex
    while (i < src.length && /\s/.test(src[i])) i++
    const trueBranch = path + '.message'
    if (src.slice(i, i + trueBranch.length) !== trueBranch) continue
    i += trueBranch.length
    while (i < src.length && /\s/.test(src[i])) i++
    if (src[i] !== ':') continue
    i++
    while (i < src.length && /\s/.test(src[i])) i++
    const fbStart = i
    // 兜底表达式的结束：同行内的 `,` `;` 或闭合括号
    let j = fbStart
    let depth = 0
    while (j < src.length) {
      const ch = src[j]
      if (ch === '\n') break
      if (ch === "'" || ch === '"' || ch === '`') {
        const q = ch
        j++
        while (j < src.length) {
          if (src[j] === '\\') { j += 2; continue }
          if (src[j] === q) { j++; break }
          j++
        }
        continue
      }
      if (ch === '(' || ch === '[' || ch === '{') { depth++; j++; continue }
      if (ch === ')' || ch === ']' || ch === '}') { if (depth === 0) break; depth--; j++; continue }
      if (depth === 0 && (ch === ',' || ch === ';')) break
      j++
    }
    const line = src.slice(0, start).split('\n').length
    // 该站点所在的一行（豁免自证要在同一行里找到本地化主文案的键）
    const lineStart = src.lastIndexOf('\n', start) + 1
    const lineEnd = src.indexOf('\n', start)
    // ★ 豁免自证的上下文窗口 = **站点所在行 + 之前 400 字符**。
    //   第一版只看「同一行」⇒ 漏掉「消费键在上一行」的写法
    //   （`t('…WithReason', {\n  reason: (e instanceof Error ? e.message : '') || …`）
    //   ⇒ 那 4 处一律判「豁免失效」⇒ 假红。
    const contextText = src.slice(Math.max(0, lineStart - 400), lineEnd < 0 ? src.length : lineEnd)
    sites.push({
      start,
      line,
      path,
      fallback: src.slice(fbStart, j).trim(),
      lineText: src.slice(lineStart, lineEnd < 0 ? src.length : lineEnd),
      contextText,
    })
    re.lastIndex = j
  }
  return sites
}

// ───────────────────────── 判据（纯函数） ─────────────────────────

/**
 * @param {object} facts
 * @param {Map<string,string[]>} facts.rawSites   文件 → 原始形状站点（带 lineText）
 * @param {Map<string,string[]>} facts.outletKeys 出口键 → 调用点
 * @param {Map<string,Map<string,string>>} facts.perLocaleValues
 * @param {Map<string,Set<string>>} facts.perLocaleKeys
 * @param {object} facts.registry
 * @param {object} deps  { hasRecoveryAction, isBareFailurePhrase }
 */
export function checkErrorOutlet(facts, deps) {
  const { rawSites, outletKeys, perLocaleValues, perLocaleKeys, registry } = facts
  const { hasRecoveryAction, isBareFailurePhrase } = deps
  const violations = []

  const fileExempt = new Map((registry.fileExemptions || []).map((e) => [e.file, e]))
  const exemptions = registry.exemptions || []

  // ① O1：不得裸用原始 message（豁免必须自证仍然成立）
  for (const [file, sites] of rawSites) {
    if (fileExempt.has(file)) continue
    for (const s of sites) {
      const hit = exemptions.find((e) => e.file === file && (s.contextText ?? s.lineText ?? '').includes(e.key))
      if (hit) continue
      violations.push({
        rule: 'O1-raw-message',
        key: `${file}:${s.line}`,
        reason: '原始 error.message 直接成为用户可见文案（未被任何本地化主文案承载）',
        value: s.fallback.slice(0, 80),
        sites: [s.fallback.slice(0, 60)],
      })
    }
  }
  // ①b 豁免失效：登记了但站点没了 ⇒ 空豁免（不许留空豁免藏违规）
  for (const e of exemptions) {
    const sites = rawSites.get(e.file) || []
    const still = sites.some((s) => (s.contextText ?? s.lineText ?? '').includes(e.key))
    if (!still) {
      violations.push({
        rule: 'O1-exemption-stale',
        key: `${e.file} · ${e.key}`,
        reason: '豁免项已失效（对应站点不存在，或该键已不在站点所在行）⇒ 空豁免必须删掉',
      })
    }
  }

  // ② O2：出口键 8 语种齐备
  for (const [key, sites] of outletKeys) {
    for (const locale of LOCALES) {
      if (perLocaleKeys.get(locale)?.has(key)) continue
      violations.push({
        rule: 'O2-outlet-key-missing',
        key,
        locale,
        reason: '错误出口引用的键在该语种不存在 ⇒ 运行时静默显示键路径',
        sites,
      })
    }
  }

  // ③ O3：状态层必须含恢复动作（8 语种逐语种判）
  for (const key of STATUS_TIER_KEYS) {
    for (const locale of LOCALES) {
      const value = perLocaleValues.get(locale)?.[key]
      if (value === undefined) {
        violations.push({
          rule: 'O3-status-tier-missing',
          key,
          locale,
          reason: '状态层键在该语种不存在',
        })
        continue
      }
      if (!hasRecoveryAction(value, locale)) {
        violations.push({
          rule: 'O3-status-tier-no-recovery',
          key,
          locale,
          reason: '状态层文案没有恢复动作（用户不知道下一步怎么办）',
          value,
        })
      }
      if (isBareFailurePhrase(value, locale)) {
        violations.push({
          rule: 'O3-status-tier-bare',
          key,
          locale,
          reason: '状态层文案是裸失败短语（既不点名主体也不给下一步）',
          value,
        })
      }
    }
  }

  // ④ O4：原因后缀键 8 语种齐备 + 保留 {reason}
  for (const locale of LOCALES) {
    const value = perLocaleValues.get(locale)?.[REASON_SUFFIX_KEY]
    if (value === undefined) {
      violations.push({
        rule: 'O4-reason-suffix-missing',
        key: REASON_SUFFIX_KEY,
        locale,
        reason: '原因后缀键在该语种不存在 ⇒ 原始 detail 无法作为次要原因上屏',
      })
      continue
    }
    if (!value.includes('{reason}')) {
      violations.push({
        rule: 'O4-reason-suffix-no-placeholder',
        key: REASON_SUFFIX_KEY,
        locale,
        reason: '原因后缀缺少 {reason} 占位符',
        value,
      })
    }
  }

  return violations
}

// ───────────────────────── 事实源 ─────────────────────────

export async function loadFacts() {
  const quality = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'localeValueQuality.ts')).href)
  const positions = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'errorPositions.ts')).href)
  const clarity = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'copyClarity.ts')).href)
  const registry = readRegistry()
  const perLocaleKeys = quality.allLocaleKeySets(join(ROOT, 'src', 'locales'))
  const perLocaleValues = quality.allLocaleValues(join(ROOT, 'src', 'locales'))
  const files = clarity.uiFiles()

  // 原始形状站点：逐文件扫（门自己的实现文件由登记簿的 fileExemptions 排除）
  const rawSites = new Map()
  for (const file of files) {
    const src = readFileSync(file, 'utf8')
    // ★ 路径口径 = **相对 web/ 仓根**（`src/…`）⇒ 与登记簿 fileExemptions/exemptions 的写法一致。
    //   （第一版用「相对 src/」⇒ 登记簿里写 `src/…` 而门读出 `…` ⇒ 豁免一条都匹配不上。）
    const rel = relative(ROOT, file)
    const sites = findRawMessageSites(src)
    if (!sites.length) continue
    rawSites.set(rel, sites)
  }

  const outletKeys = positions.allOutletKeys(files)
  return { rawSites, outletKeys, perLocaleKeys, perLocaleValues, registry }
}

// ───────────────────────── 自证 ─────────────────────────

async function selftest() {
  const clarity = await import(pathToFileURL(join(ROOT, 'scripts', 'i18n-error-clarity-guard.mjs')).href)
  const cases = []
  const check = (id, desc, expected, actual) => {
    cases.push({ id, desc, expected, actual, ok: JSON.stringify(expected) === JSON.stringify(actual) })
  }
  const rules = (violations) => violations.map((v) => v.rule)

  // 造一份干净事实源。
  //
  // ★ 夹具必须**逐语种**（第 15 增量 §427 的老教训）：
  //   第一版把**同一个中文值**喂 8 个语种 ⇒ 非中文语种的词表不认识中文
  //   ⇒ 「没有恢复动作」被判 84 条假违规 ⇒ 夹具自己有缺口时，期望值会写成
  //   「一堆不相干的违规」⇒ 断言变成恒非空（**断言恒真比恒假更隐蔽**）。
  const TIER_VALUES = {
    'zh-CN': '服务器没能完成这个操作，请稍后重试',
    'zh-TW': '伺服器沒能完成這個操作，請稍後重試',
    'ja-JP': 'サーバーがこの操作を完了できませんでした。しばらくしてから再試行してください。',
    'en-US': 'The server could not complete this operation. Please try again shortly.',
    'de-DE': 'Der Server konnte die Aktion nicht abschließen. Bitte in Kürze erneut versuchen.',
    'es-ES': 'El servidor no pudo completar esta operación. Vuelva a intentarlo en breve.',
    'fr-FR': "Le serveur n'a pas pu effectuer cette opération. Veuillez réessayer dans un instant.",
    'ar-SA': 'لم يتمكّن الخادم من إتمام هذه العملية. أعد المحاولة بعد قليل.',
  }
  const SUFFIX_VALUES = {
    'zh-CN': '（原因：{reason}）',
    'zh-TW': '（原因：{reason}）',
    'ja-JP': '（理由：{reason}）',
    'en-US': '(reason: {reason})',
    'de-DE': '(Grund: {reason})',
    'es-ES': '(motivo: {reason})',
    'fr-FR': '(raison : {reason})',
    'ar-SA': '(السبب: {reason})',
  }
  const keysFor = (extra) => {
    const m = new Map()
    for (const locale of LOCALES) {
      const set = new Set([...STATUS_TIER_KEYS, REASON_SUFFIX_KEY, ...Object.keys(extra || {})])
      m.set(locale, set)
    }
    return m
  }
  const valuesFor = (extra) => {
    const m = new Map()
    for (const locale of LOCALES) {
      const table = {}
      for (const key of STATUS_TIER_KEYS) table[key] = TIER_VALUES[locale]
      table[REASON_SUFFIX_KEY] = SUFFIX_VALUES[locale]
      Object.assign(table, extra?.[locale] || {})
      m.set(locale, table)
    }
    return m
  }
  const registry = {
    fileExemptions: [{ file: 'src/ignored.ts', reason: '夹具' }],
    exemptions: [],
  }
  const deps = {
    hasRecoveryAction: clarity.hasRecoveryAction,
    isBareFailurePhrase: clarity.isBareFailurePhrase,
  }

  // S1：干净事实源 ⇒ 无违规
  {
    const facts = {
      rawSites: new Map(),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: valuesFor(),
      registry,
    }
    check('S1', '干净事实源 ⇒ 0 违规', [], rules(checkErrorOutlet(facts, deps)))
  }

  // S2：出现裸用原始 message ⇒ O1
  {
    const facts = {
      rawSites: new Map([['src/a.vue', [{ line: 10, lineText: "error.value = e instanceof Error ? e.message : 'x'", contextText: "error.value = e instanceof Error ? e.message : 'x'", fallback: "'x'" }]]]),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: valuesFor(),
      registry,
    }
    check('S2', '裸用原始 message ⇒ O1-raw-message', ['O1-raw-message'], rules(checkErrorOutlet(facts, deps)))
  }

  // S3：豁免且键在同一行 ⇒ 不报 O1
  {
    const facts = {
      rawSites: new Map([['src/a.vue', [{ line: 10, lineText: "t('k', { reason: e instanceof Error ? e.message : '' })", contextText: "t('k', { reason: e instanceof Error ? e.message : '' })", fallback: "''" }]]]),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: valuesFor(),
      registry: { ...registry, exemptions: [{ file: 'src/a.vue', key: 'k', category: 'reason-slot', reason: 'x' }] },
    }
    check('S3', '登记豁免且键在行内 ⇒ 不报 O1', [], rules(checkErrorOutlet(facts, deps)))
  }

  // S4：豁免失效（站点没了）⇒ O1-exemption-stale
  {
    const facts = {
      rawSites: new Map(),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: valuesFor(),
      registry: { ...registry, exemptions: [{ file: 'src/a.vue', key: 'k', category: 'reason-slot', reason: 'x' }] },
    }
    check('S4', '豁免项已无对应站点 ⇒ O1-exemption-stale', ['O1-exemption-stale'], rules(checkErrorOutlet(facts, deps)))
  }

  // S5：豁免键不在行内 ⇒ O1（豁免不能用来藏违规）
  {
    const facts = {
      rawSites: new Map([['src/a.vue', [{ line: 10, lineText: "error.value = e instanceof Error ? e.message : 'x'", contextText: "error.value = e instanceof Error ? e.message : 'x'", fallback: "'x'" }]]]),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: valuesFor(),
      registry: { ...registry, exemptions: [{ file: 'src/a.vue', key: 'not-in-line', category: 'reason-slot', reason: 'x' }] },
    }
    check('S5', '豁免键不在站点行内 ⇒ O1-raw-message + 豁免失效', ['O1-raw-message', 'O1-exemption-stale'], rules(checkErrorOutlet(facts, deps)))
  }

  // S6：文件级豁免 ⇒ 整个文件跳过
  {
    const facts = {
      rawSites: new Map([['src/ignored.ts', [{ line: 1, lineText: 'e instanceof Error ? e.message : x', contextText: 'e instanceof Error ? e.message : x', fallback: 'x' }]]]),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: valuesFor(),
      registry,
    }
    check('S6', '文件级豁免 ⇒ 不报 O1', [], rules(checkErrorOutlet(facts, deps)))
  }

  // S7：出口键在某语种缺 ⇒ O2（只缺一个语种 ⇒ 1 条）
  {
    const keys = keysFor({ 'some.key': 1 })
    keys.get('ja-JP').delete('some.key')
    const facts = {
      rawSites: new Map(),
      outletKeys: new Map([['some.key', ['src/a.vue:1']]]),
      perLocaleKeys: keys,
      perLocaleValues: valuesFor({ 'some.key': 1 }),
      registry,
    }
    check('S7', '出口键在某语种缺失 ⇒ O2-outlet-key-missing', ['O2-outlet-key-missing'], rules(checkErrorOutlet(facts, deps)))
  }

  // S8：状态层无恢复动作 ⇒ O3（没有恢复动作 ⇒ 同时也是裸失败短语 ⇒ 两条）
  {
    const values = valuesFor()
    values.get('zh-CN')['errors.byStatus.500'] = '服务器内部错误'
    const facts = {
      rawSites: new Map(),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: values,
      registry,
    }
    check('S8', '状态层没有恢复动作 ⇒ O3-status-tier-no-recovery + bare', ['O3-status-tier-no-recovery', 'O3-status-tier-bare'], rules(checkErrorOutlet(facts, deps)))
  }

  // S9：原因后缀缺占位符 ⇒ O4
  {
    const values = valuesFor()
    values.get('zh-CN')[REASON_SUFFIX_KEY] = '（原因）'
    const facts = {
      rawSites: new Map(),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: values,
      registry,
    }
    check('S9', '原因后缀缺 {reason} ⇒ O4-reason-suffix-no-placeholder', ['O4-reason-suffix-no-placeholder'], rules(checkErrorOutlet(facts, deps)))
  }

  // S10：原因后缀键缺失 ⇒ O4
  {
    const values = valuesFor()
    delete values.get('de-DE')[REASON_SUFFIX_KEY]
    const facts = {
      rawSites: new Map(),
      outletKeys: new Map(),
      perLocaleKeys: keysFor(),
      perLocaleValues: values,
      registry,
    }
    check('S10', '原因后缀键缺失 ⇒ O4-reason-suffix-missing', ['O4-reason-suffix-missing'], rules(checkErrorOutlet(facts, deps)))
  }

  // S11：扫描器本身 —— 三元真分支不是 .message ⇒ 不算站点
  {
    const src = "const m = e instanceof Error ? e : new Error(String(e))"
    check('S11', '归一化写法（?: e : new Error）不是站点', 0, findRawMessageSites(src).length)
  }

  // S12：扫描器本身 —— 真分支是 .message ⇒ 是站点，且兜底不吞掉收尾括号
  {
    const src = "emit('error', e instanceof Error ? e.message : t('x'))"
    const sites = findRawMessageSites(src)
    check('S12', '站点识别 + 兜底边界正确', 1, sites.length)
    check('S12b', '兜底 = t(\'x\')（不吞收尾括号）', "t('x')", sites[0]?.fallback)
  }

  let failed = 0
  for (const c of cases) {
    if (!c.ok) {
      failed++
      console.log(`❌ ${c.id} ${c.desc}\n   期望 ${JSON.stringify(c.expected)}\n   实得 ${JSON.stringify(c.actual)}`)
    }
  }
  console.log(`\n自证 ${cases.length - failed}/${cases.length}` + (failed ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(failed ? 2 : 0)
}

// ───────────────────────── CLI ─────────────────────────

const isSelftest = process.argv.includes('--selftest')
const isJson = process.argv.includes('--json')
const isList = process.argv.includes('--list')

// ★ main-guard：被别的门 import 时只提供判据，不跑 CLI（也不 process.exit）。
const isMain = (() => {
  const entry = process.argv[1]
  if (!entry) return false
  try {
    return pathToFileURL(resolve(entry)).href === import.meta.url
  } catch {
    return false
  }
})()

if (!isMain) {
  // 仅导出判据。
} else if (isSelftest) {
  await selftest()
} else {
  const clarity = await import(pathToFileURL(join(ROOT, 'scripts', 'i18n-error-clarity-guard.mjs')).href)
  const facts = await loadFacts()
  const violations = checkErrorOutlet(facts, {
    hasRecoveryAction: clarity.hasRecoveryAction,
    isBareFailurePhrase: clarity.isBareFailurePhrase,
  })
  const rawCount = [...facts.rawSites.values()].reduce((s, v) => s + v.length, 0)
  if (isList) {
    console.log('--- 出口键 ---')
    for (const [key, sites] of facts.outletKeys) console.log(`${key}\t${sites.join(', ')}`)
    console.log('--- 原始形状站点 ---')
    for (const [file, sites] of facts.rawSites) {
      for (const s of sites) console.log(`${file}:${s.line}\t${s.fallback.slice(0, 60)}`)
    }
    console.log('--- 豁免 ---')
    for (const e of facts.registry.exemptions || []) console.log(`[${e.category}] ${e.file} · ${e.key}`)
    process.exit(0)
  }
  if (isJson) {
    console.log(JSON.stringify({ violations, count: violations.length }, null, 2))
  } else {
    console.log('口径：用户可见错误文案必须经过 src/i18n/errorSurface.ts 的 surfaceError()；'
      + '主文案 = 本地化键（点名主体 + 恢复动作），原始 detail 只能作次要原因')
    console.log(`出口键：${facts.outletKeys.size}；原始形状站点：${rawCount}；豁免：${(facts.registry.exemptions || []).length}`)
    if (violations.length === 0) {
      console.log('\n✅ 错误文案都从单一出口出来 —— PASS')
    } else {
      console.log(`\n❌ ${violations.length} 处违规：`)
      for (const v of violations) {
        console.log(`  [${v.rule}] ${v.key}${v.locale ? ' @' + v.locale : ''} — ${v.reason}`)
        if (v.value) console.log(`      「${v.value}」`)
        if (v.sites) console.log(`      调用点：${v.sites.join(', ')}`)
      }
    }
  }
  process.exit(violations.length ? 1 : 0)
}
