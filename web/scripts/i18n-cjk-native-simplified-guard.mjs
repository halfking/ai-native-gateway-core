#!/usr/bin/env node
// i18n-cjk-native-simplified-guard.mjs — CJK 原生语种（ja-JP / zh-TW）简体残留门（2026-10-27）
//
// 用法：
//   node scripts/i18n-cjk-native-simplified-guard.mjs              # 对比 baseline，超限则 exit 1
//   node scripts/i18n-cjk-native-simplified-guard.mjs --json       # 输出 JSON
//   node scripts/i18n-cjk-native-simplified-guard.mjs --selftest   # 自证（违规→红 / 干净→绿）
//   node scripts/i18n-cjk-native-simplified-guard.mjs --update-baseline
//
// ★ 它治的是哪个盲区：
//   仓内已有 `localeValueQuality.ts` 的两个指标都量不到「CJK 原生语种里的简体残留」——
//     · `cjkInNonCjkLocaleAllKeys` 把 ja-JP / zh-TW **整体豁免**（CJK_NATIVE_LOCALES）
//       （理由：日文/繁中用汉字是正常的）
//     · `sameAsSource` 只判「与 zh-CN **逐字相同**」⇒ **部分**残留（值已改过、但还留着简体字）
//       两条都看不见。
//   ⇒ 现实后果（2026-10-27 实测）：`freePool.signup` / `freePool.env` 在 zh-TW 与 ja-JP 里
//     装的是 **acquisition.* 的值**（"注册 Key" / "环境变量"，简体原文），8 个语种里只有这两个
//     语种没本地化 ⇒ 界面在繁中/日文下显示简体中文。
//
// ★ 判据（对象化、可移植）：
//   简体专用字 ≈ 「**Big5 转不出来的汉字**」（繁体有、简体/日文新字体形没有）。
//   ★ 字表是**入仓的数据资产**（scripts/cjk-native-simplified-chars.json），
//     由 scripts/gen-cjk-native-simplified-chars.mjs 一次性推导 ——
//     门本身不依赖 iconv ⇒ Windows / CI 都能跑。
//   ★ ja-JP 额外豁免「合法日文新字体」子集（実/処/録/続/検/総…）：
//     那些字在日文里是**正字**，不是简体残留。
//
// ★ 两个指标：
//   · referenced（被源码引用 ⇒ 用户可见）—— **硬闸 0**：界面上不许出现简体原文
//   · unreferenced（未被引用 ⇒ 潜伏）—— **棘轮**：只许降不许升（存量是「共有死键」等历史包袱）
//
// ★ 口径自陈：
//   引用集 = ① scanner.ts 的字面量 t/tc/$t/i18n.t/te/tm/localeArray + helper 展开
//           ② **属性承载键**（labelKey / titleKey / hintKey，含运行时前缀解析）
//           ③ **运行时拼键前缀下的键**（`t(\`prefix.${x}\`)` ⇒ 落在 prefix. 下的都算用户可见）
//   「全量键」= 该语种全部叶子键（**不只被引用的**）—— 这正是原盲区所在。
// ★★ 误报方向：**宁可漏报不误报**，但 ja-JP 侧是「宁可误报不漏报」——
//   因为豁免表 japaneseOk 现在是**人工审定的固定数据**（不再随「已翻译值」自动增长），
//   ⇒ 将来若真译出用到「点」这类「既是简体简形、也是日文常用字」的地方，会**误报**。
//   ⇒ 那时需要人工往 japaneseOk 里加那个字（并说明理由），而不是让豁免表自动放宽 ——
//   ★ 自动放宽会把自己的缺陷一并豁免（2026-10-27 实测：授权/联系/请求/一键/运维平台/编排 被洗白 ⇒ 门报「0」是假的）。

import { fileURLToPath, pathToFileURL } from 'node:url'
import { dirname, join, resolve } from 'node:path'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const TARGET_LOCALES = ['ja-JP', 'zh-TW']
const SOURCE = 'zh-CN'
const DATA_FILE = join(ROOT, 'scripts', 'cjk-native-simplified-chars.json')
const BASELINE_FILE = join(ROOT, 'scripts', 'i18n-cjk-native-simplified-baseline.json')

// ★ 与仓内 localeValueQuality.ts:89 的 CJK_RE 逐字一致（**必须转义写法**：
//   手打字面量区间极易写歪一个码位 ⇒ 区间跨过代理对区 ⇒ emoji/韩文被误判成汉字）。
const CJK_RE = /[\u4e00-\u9fff\u3400-\u4dbf]/

export function loadCharTables() {
  if (!existsSync(DATA_FILE)) {
    throw new Error(`字表缺失：${DATA_FILE}\n生成：node scripts/gen-cjk-native-simplified-chars.mjs`)
  }
  const d = JSON.parse(readFileSync(DATA_FILE, 'utf8'))
  return {
    notBig5: new Set([...d.notBig5]),
    japaneseOk: new Set([...d.japaneseOk]),
    updated: d.updated,
    provenance: d.provenance,
  }
}

/**
 * 量一个值里的「简体专用字」。
 * @param {string} value
 * @param {{locale: string, notBig5: Set<string>, japaneseOk: Set<string>}} opts
 * @returns {string[]} 命中的字符（去重、保序）
 */
export function simplifiedCharsOf(value, opts) {
  if (typeof value !== 'string') return []
  const out = []
  for (const ch of value) {
    if (!CJK_RE.test(ch)) continue
    if (!opts.notBig5.has(ch)) continue
    if (opts.locale === 'ja-JP' && opts.japaneseOk.has(ch)) continue
    if (!out.includes(ch)) out.push(ch)
  }
  return out
}

/**
 * 量一个语种的全部叶子键。
 * @param {Map<string,string|null>} values  扁平化的叶子键 → 值
 * @param {Set<string>} refKeys             引用集（字面量 + 属性承载键 + helper 展开）
 * @param {{locale: string, notBig5: Set<string>, japaneseOk: Set<string>}} opts
 * @param {string[]} dynPrefixes            **运行时拼键的静态前缀**（如 `dataLifecycle.hotPartition.hotTableNames.`）
 *
 * ★★ 为什么要 dynPrefixes（2026-10-27 实测的根因）：
 *   引用集只含**字面量**调用 ⇒ `t(\`dataLifecycle.hotPartition.hotTableNames.${x}\`)`
 *   这类**运行时拼键**下的键**全部落进「未引用」桶** ⇒ 而它们**是用户可见的**
 *   （HotPartitionManager.vue 直接渲染）⇒ 门把用户可见的简体原文判成「潜伏存量」⇒ **硬闸形同虚设**。
 *   ⇒ 实测：这一族在 zh-TW 有 27 条 / ja-JP 有 39 条用户可见简体残留，门却报「用户可见残留 = 0」。
 *   ⇒ 故：落在任一动态前缀下的键**一律算 referenced**（宁可严，不可漏）。
 */
export function scanValues(values, refKeys, opts, dynPrefixes = []) {
  const referenced = []
  const unreferenced = []
  for (const [key, value] of values) {
    if (typeof value !== 'string') continue
    const chars = simplifiedCharsOf(value, opts)
    if (!chars.length) continue
    const rec = { key, value: value.length > 160 ? value.slice(0, 160) + '…' : value, chars: chars.join('') }
    const isDyn = dynPrefixes.some((p) => key.startsWith(p))
    if (refKeys.has(key) || isDyn) referenced.push(rec)
    else unreferenced.push(rec)
  }
  return { referenced, unreferenced }
}

// ───────────────────────── 自证 ─────────────────────────
function selftest() {
  // ★ 合成字表：把「要抓的简体字」与「要豁免的合法日文新字体」显式写死，
  //   保证夹具的期望值可**手工推演**，不依赖仓内数据文件。
  const shinjitai = '実処録続継検総価値働効単発号圧対学国専属当応悪戦数断旧権歴毎気済温満状独産画窓経緑縦覧観訳説読譲転込遅釈録鍵関険隠雑頼駆両乗側剰区参営囲図増壊声売変奨届帯帰広廃従徴戻払択担拠拡携撃残着蔵装維編脱'
  const simplified = '这载败册实对错时发认动户间签态应无点总计务译验证仓库储径输处录续宽视稳择环汇览变轮连达长单从旧条归练组细终经结绝统绿红约级类紧简筛笔竖称积种础码盖监电现热烧满滞滤滥浅测济浏温溯沟欢桥档样树栏权术杂显击携换损举拨拦拥拟护扫扩执'
  const notBig5 = new Set([...shinjitai, ...simplified])
  const japaneseOk = new Set([...shinjitai])
  const cases = []
  const probe = (id, desc, value, locale, expected) => {
    const got = simplifiedCharsOf(value, { locale, notBig5, japaneseOk })
    cases.push({ id, desc, expected, actual: got.join(''), ok: got.join('') === expected })
  }

  // ① 简体原文 ⇒ 必须抓到
  probe('S1', '简体「加载会话列表失败」⇒ 抓到「载」「败」', '加载会话列表失败', 'zh-TW', '载败')
  probe('S2', '简体「注册 Key」⇒ 只抓「册」（注是 Big5 里也有的常用字，不算简体专用）', '注册 Key', 'zh-TW', '册')
  probe('S3', 'ja-JP 值里的简体「实践」⇒ 抓到「实」（践 不是简体专用字）', '实践', 'ja-JP', '实')
  // ② 繁体 ⇒ 不得抓到
  probe('S4', '繁体「載入工作階段列表失敗」⇒ 不抓', '載入工作階段列表失敗', 'zh-TW', '')
  probe('S5', '繁体「註冊」⇒ 不抓', '註冊', 'zh-TW', '')
  probe('S6', 'ja-JP 正常日文「実装」⇒ 不抓（実 是合法日文）', '実装', 'ja-JP', '')
  // ③ 合法日文新字体 ⇒ 不得抓到
  probe('S7', '日文「登録」⇒ 不抓', '登録', 'ja-JP', '')
  probe('S8', '日文「継続」⇒ 不抓', '継続', 'ja-JP', '')
  probe('S9', '日文「確認」⇒ 不抓', '確認', 'ja-JP', '')
  // ④ 非汉字 ⇒ 一律不抓
  probe('S10', '纯 ASCII ⇒ 不抓', 'OAuth / API Key / 429', 'zh-TW', '')
  probe('S11', 'emoji + 汉字（繁体）⇒ 只看汉字，不抓', '📖 統計', 'zh-TW', '')
  probe('S12', '韩文 ⇒ 不抓（不是汉字，也不是简体）', '문서는 제공', 'zh-TW', '')
  probe('S13', '假名 ⇒ 不抓', 'サンプル', 'ja-JP', '')
  // ⑤ 占位符不影响判定
  probe('S14', '带占位符的简体值 ⇒ 照样抓到', '加载失败：{msg}', 'zh-TW', '载败')

  // ⑥ 扫描器层：referenced / unreferenced 分流
  const values = new Map([
    ['a.visible', '加载失败'],
    ['b.hidden', '加载失败'],
    ['c.clean', '載入失敗'],
    ['d.ascii', 'OK'],
  ])
  const scan = scanValues(values, new Set(['a.visible']), { locale: 'zh-TW', notBig5, japaneseOk })
  const scanOk =
    scan.referenced.length === 1 && scan.referenced[0].key === 'a.visible' &&
    scan.unreferenced.length === 1 && scan.unreferenced[0].key === 'b.hidden'
  cases.push({ id: 'S15', desc: 'referenced / unreferenced 分流正确', expected: 'ref=1 unref=1', actual: `ref=${scan.referenced.length} unref=${scan.unreferenced.length}`, ok: scanOk })

  // ★★ 运行时拼键前缀下的键 ⇒ **必须算 referenced**
  //   背景（2026-10-27 实测）：`t(\`dataLifecycle.hotPartition.hotTableNames.${x}\`)`
  //   这类调用点里的键**不在字面量引用集** ⇒ 若不算 referenced，用户可见的简体原文
  //   会被判成「潜伏存量」⇒ **硬闸形同虚设**（实测漏检 39 条）。
  const dynScan = scanValues(
    new Map([
      ['dataLifecycle.hotPartition.hotTableNames.requestLogs', '加载失败'],
      ['other.hidden', '加载失败'],
    ]),
    new Set(),
    { locale: 'zh-TW', notBig5, japaneseOk },
    ['dataLifecycle.hotPartition.hotTableNames.'],
  )
  const dynOk =
    dynScan.referenced.length === 1 &&
    dynScan.referenced[0].key === 'dataLifecycle.hotPartition.hotTableNames.requestLogs' &&
    dynScan.unreferenced.length === 1 &&
    dynScan.unreferenced[0].key === 'other.hidden'
  cases.push({
    id: 'S15b',
    desc: '运行时拼键前缀下的键算 referenced（用户可见，不许漏检）',
    expected: 'ref=1 unref=1',
    actual: `ref=${dynScan.referenced.length} unref=${dynScan.unreferenced.length}`,
    ok: dynOk,
  })

  // ★★ 反向：前缀不匹配时**不得**误判为 referenced（否则潜伏存量会被悄悄洗白）
  const dynNeg = scanValues(
    new Map([['other.hidden', '加载失败']]),
    new Set(),
    { locale: 'zh-TW', notBig5, japaneseOk },
    ['dataLifecycle.hotPartition.hotTableNames.'],
  )
  const dynNegOk = dynNeg.referenced.length === 0 && dynNeg.unreferenced.length === 1
  cases.push({
    id: 'S15c',
    desc: '前缀不匹配的键仍算 unreferenced（不许把潜伏存量洗白）',
    expected: 'ref=0 unref=1',
    actual: `ref=${dynNeg.referenced.length} unref=${dynNeg.unreferenced.length}`,
    ok: dynNegOk,
  })

  // ⑦ 字表自检：notBig5 不得含 ASCII；japaneseOk 必须是 notBig5 的子集
  const tablesOk = [...notBig5].every((c) => c.codePointAt(0) > 0x2fff) &&
    [...japaneseOk].every((c) => notBig5.has(c))
  cases.push({ id: 'S16', desc: '字表结构合法（notBig5 无 ASCII；japaneseOk ⊆ notBig5）', expected: 'ok', actual: tablesOk ? 'ok' : 'bad', ok: tablesOk })

  // ⑧ ★ 真实字表自检（从仓内数据文件读）：CJK_RE 不得命中 emoji / 韩文 / 假名
  let realTables = null
  try {
    realTables = loadCharTables()
  } catch { /* 数据文件缺失时跳过，交给 CLI 报 */ }
  if (realTables) {
    const falsePositives = ['📖', '🟡', '🔒', '한', 'あ', 'A', '1'].filter((c) => CJK_RE.test(c))
    cases.push({
      id: 'S17',
      desc: 'CJK_RE 不误判 emoji / 韩文 / 假名 / ASCII（字面量区间写歪 ⇒ 跨过代理对区）',
      expected: '',
      actual: falsePositives.join(''),
      ok: falsePositives.length === 0,
    })
    const cjkInNotBig5 = [...realTables.notBig5].filter((c) => !CJK_RE.test(c))
    cases.push({
      id: 'S18',
      desc: '字表里的每个字都是汉字',
      expected: '',
      actual: cjkInNotBig5.join(''),
      ok: cjkInNotBig5.length === 0,
    })
    // ⑨ 用**真实字表**跑一遍本轮修掉的真实缺陷 ⇒ 证明门能抓到它
    const rOpts = { locale: 'zh-TW', notBig5: realTables.notBig5, japaneseOk: realTables.japaneseOk }
    const rGot = simplifiedCharsOf('注册 Key', rOpts).join('')
    cases.push({
      id: 'S19',
      desc: '真实字表：本轮修掉的 freePool 缺陷原文「注册 Key」⇒ 抓到「册」',
      expected: '册',
      actual: rGot,
      ok: rGot === '册',
    })
    const jOpts = { locale: 'ja-JP', notBig5: realTables.notBig5, japaneseOk: realTables.japaneseOk }
    const jGot = simplifiedCharsOf('実装', jOpts).join('')
    cases.push({
      id: 'S20',
      desc: '真实字表：日文「実装」⇒ 不抓（実 在 japaneseOk 豁免表里）',
      expected: '',
      actual: jGot,
      ok: jGot === '',
    })
  }

  return cases
}

// ───────────────────────── 主流程 ─────────────────────────
async function loadScanner() {
  const p = pathToFileURL(join(ROOT, 'src', 'i18n', 'scanner.ts')).href
  return await import(p)
}

/**
 * 取「运行时拼键」的静态前缀集合（如 `dataLifecycle.hotPartition.hotTableNames.`）。
 * ★ 来自 callSiteParams.ts 的 template 形态调用点 —— 它与 scanner.ts 的字面量引用是**两套**，
 *   前者只认 `t('字面量')`，后者认 `` t(`前缀.${x}`) `` 的静态前缀部分。
 */
async function loadDynamicPrefixes(srcDir) {
  const p = pathToFileURL(join(ROOT, 'src', 'i18n', 'callSiteParams.ts')).href
  const mod = await import(p)
  const { callSites } = mod.scanAllCallSiteKeys(srcDir)
  const out = new Set()
  for (const r of callSites) {
    if (r.keyKind === 'template' && r.keyPrefix) out.add(r.keyPrefix)
  }
  return [...out]
}

function flattenLeaves(obj, prefix, out) {
  for (const [k, v] of Object.entries(obj || {})) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) flattenLeaves(v, key, out)
    else out.set(key, typeof v === 'string' ? v : null)
  }
  return out
}

function readBaseline() {
  if (!existsSync(BASELINE_FILE)) return null
  try { return JSON.parse(readFileSync(BASELINE_FILE, 'utf8')) } catch { return null }
}

function writeBaseline(data) {
  writeFileSync(BASELINE_FILE, JSON.stringify(data, null, 2) + '\n')
}

async function main() {
  const argv = process.argv.slice(2)

  if (argv.includes('--selftest')) {
    const cases = selftest()
    for (const c of cases) {
      const mark = c.ok ? '✅' : '❌'
      process.stdout.write(`  ${mark} ${c.id} ${c.desc}${c.ok ? '' : ` — expected=${JSON.stringify(c.expected)} actual=${JSON.stringify(c.actual)}`}\n`)
    }
    const bad = cases.filter((c) => !c.ok)
    process.stdout.write(`\n${cases.length - bad.length}/${cases.length} 通过\n`)
    process.exit(bad.length ? 1 : 0)
  }

  const tables = loadCharTables()
  const scanner = await loadScanner()
  const { loadLocale, scanAll } = scanner
  const localesDir = join(ROOT, 'src', 'locales')
  const refKeys = new Set(scanAll(join(ROOT, 'src')).references.map((r) => r.key))
  // ★ 运行时拼键的静态前缀：落在这些前缀下的键**是用户可见的**，不能算「潜伏」
  const dynPrefixes = await loadDynamicPrefixes(join(ROOT, 'src'))

  const report = { at: new Date().toISOString().slice(0, 10), tablesUpdated: tables.updated, locales: {} }
  for (const loc of TARGET_LOCALES) {
    const values = flattenLeaves(loadLocale(loc, localesDir), '', new Map())
    const opts = { locale: loc, notBig5: tables.notBig5, japaneseOk: tables.japaneseOk }
    const s = scanValues(values, refKeys, opts, dynPrefixes)
    report.locales[loc] = {
      totalKeys: values.size,
      referenced: s.referenced.length,
      unreferenced: s.unreferenced.length,
      referencedSamples: s.referenced.slice(0, 20),
      unreferencedSamples: s.unreferenced.slice(0, 20),
    }
  }

  if (argv.includes('--json')) {
    process.stdout.write(JSON.stringify(report, null, 2) + '\n')
    return
  }

  if (argv.includes('--update-baseline')) {
    const data = {
      updated: report.at,
      note: 'referenced = 硬闸（须为 0）；unreferenced = 棘轮（只许降）。由 scripts/i18n-cjk-native-simplified-guard.mjs --update-baseline 写入',
      referenced: Object.fromEntries(TARGET_LOCALES.map((l) => [l, report.locales[l].referenced])),
      unreferenced: Object.fromEntries(TARGET_LOCALES.map((l) => [l, report.locales[l].unreferenced])),
    }
    writeBaseline(data)
    process.stdout.write(JSON.stringify(data) + '\n')
    return
  }

  const baseline = readBaseline()
  const lines = [
    'CJK 原生语种（ja-JP / zh-TW）简体残留',
    `口径：全量键；引用集 = 字面量 + 属性承载键 + 运行时拼键前缀；简体专用字 = Big5 转不出的汉字（字表 ${tables.updated}）；ja-JP 豁免合法日文新字体`,
  ]
  let failed = false
  for (const loc of TARGET_LOCALES) {
    const r = report.locales[loc]
    const bRef = baseline?.referenced?.[loc]
    const bUnref = baseline?.unreferenced?.[loc]
    lines.push(`  ${loc}: 用户可见残留=${r.referenced} (硬闸 0${bRef !== undefined ? `, baseline ${bRef}` : ''})｜未引用残留=${r.unreferenced}${bUnref !== undefined ? ` (baseline ${bUnref})` : ''}`)
    if (r.referenced > 0) {
      failed = true
      lines.push(`    ❌ 用户可见的简体残留（界面上会出现简体原文）：`)
      for (const s of r.referencedSamples) lines.push(`       ${s.key} = ${JSON.stringify(s.value)}  [${s.chars}]`)
    }
    if (typeof bUnref === 'number' && r.unreferenced > bUnref) {
      failed = true
      lines.push(`    ❌ 未引用残留上升 ${bUnref} → ${r.unreferenced}（棘轮被打破）`)
    }
  }
  if (!baseline) {
    failed = true
    lines.push('❌ baseline 文件缺失：scripts/i18n-cjk-native-simplified-baseline.json')
    lines.push('   生成：node scripts/i18n-cjk-native-simplified-guard.mjs --update-baseline')
  }
  process.stdout.write(lines.join('\n') + '\n')
  if (failed) {
    process.stderr.write('\n❌ 简体残留门未通过\n')
    process.exit(1)
  }
  process.stdout.write('\n✅ 通过\n')
}

// ★ 只有「被直接执行」时才跑门 —— 本模块导出纯函数供别的工具 import 复用。
//   （少了这条：import 本模块就会连带跑一遍门 ⇒ 导入方的输出被门的输出盖住。）
const isDirectRun = (() => {
  try {
    return process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])
  } catch {
    return false
  }
})()

if (isDirectRun) {
  main().catch((e) => {
    console.error('i18n-cjk-native-simplified-guard failed:', e)
    process.exit(1)
  })
}
