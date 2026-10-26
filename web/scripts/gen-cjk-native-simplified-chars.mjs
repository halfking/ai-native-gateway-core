#!/usr/bin/env node
// gen-cjk-native-simplified-chars.mjs — 生成「简体专用字」字表（2026-10-27）
//
// 用法：
//   node scripts/gen-cjk-native-simplified-chars.mjs           # 重新生成 scripts/data/cjk-native-simplified-chars.json
//   node scripts/gen-cjk-native-simplified-chars.mjs --dry-run # 只打印统计，不落盘
//
// ★ 为什么需要它：
//   门（i18n-cjk-native-simplified-guard.mjs）要判「这个汉字是不是简体」，
//   而仓内没有任何 S/T 转换依赖（本机也没有 opencc / opencc-js）。
//   ⇒ 这里用 **Big5** 当判据：**Big5 转不出来的汉字 = 繁体有、简体/日文新字体形没有**。
//   ★ 这个判据对 **zh-TW** 是精确的（繁体字都能转）；
//     对 **ja-JP** 会连**合法日文新字体**一起命中（実/処/録/続/検/総…），
//     所以要再减去 japaneseOk 子集。
//
// ★ 依赖：**iconv**（macOS / Linux 自带；Windows 需另行提供）。
//   Windows 上跑不动本脚本 ⇒ 直接用仓内已生成的数据文件即可（门本身不依赖 iconv）。
//
// ★ 字表的「覆盖边界」（必须写进审计档，否则会被当成万能判据）：
//   本表只覆盖**当前语料里出现过的汉字**。将来若新增一个本表没有的简体字，
//   门**抓不到**它 ⇒ 需要重跑本脚本扩表（审计档已登记这条为「已知局限」）。

import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { dirname, join, resolve } from 'node:path'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const LOCALES_DIR = join(ROOT, 'src', 'locales')
const OUT_FILE = join(ROOT, 'scripts', 'cjk-native-simplified-chars.json')

// ★ 与仓内 localeValueQuality.ts:89 逐字一致（**必须转义写法**：
//   手打字面量区间极易写歪一个码位 ⇒ 区间跨过代理对区 ⇒ emoji/韩文被误判成汉字）。
const CJK_RE = /[\u4e00-\u9fff\u3400-\u4dbf]/

// ★★★ 「Big5 收录了简体字形」的**人工审定正名单**（2026-10-27 实测新增）
//
//   ★ 为什么需要它：
//     「Big5 转不出来 = 简体专用字」这个判据**不完备** ——
//     **Big5 自己收录了一小批简体字形**（实测：异 / 据 / 与 / 余 / 卖 都能被 iconv 转成 Big5 码位），
//     ⇒ 它们永远进不了 iconv 推导出的 notBig5 ⇒ 门**抓不到**它们 ⇒ 漏检。
//     实测证据：`iconv -f UTF-8 -t BIG-5` 对「异」返回 c9dd（Big5 码位存的是繁体「異」），
//     ⇒ 「能转」只说明「该码位有对应字」，**不代表**这个字形在繁体里合法。
//   ★ 与 japaneseOk 的关系：
//     「与」「余」在**日文**里是正字（付与 / 余分に使う）⇒ 它们会因
//     「出现在 ja-JP 已本地化值里」而**自动**进 japaneseOk ⇒ ja-JP 不误报；
//     而 zh-TW（无豁免）会照抓 ⇒ 正好符合事实。
//   ★ 纪律：这份清单是**人工审定**的，不是数据推导的
//     （★ 通用式：「数据驱动的豁免表会把该数据里的缺陷一并豁免」；此处是它的镜像 ——
//      ★ iconv 推导出的「负名单」会把**它自己的盲区**一并放过 ⇒ 必须人工补正名单）。
//   ★ 已知**未穷尽**：用「zh-CN 字频 ≫ zh-TW 字频」反查，实测还有约 185 个候选字
//     未能自动区分「简繁同形的合法用字」与「Big5 收录的简体字形」
//     ⇒ 已登记为下一轮工程（需人工逐字审定的完整正名单）。
const BIG5_TRAPS = ['异', '据', '与', '余', '卖']

function big5Ok(ch) {
  try {
    execFileSync('iconv', ['-f', 'UTF-8', '-t', 'BIG-5'], { input: ch, stdio: ['pipe', 'pipe', 'pipe'] })
    return true
  } catch {
    return false
  }
}

function flattenLeaves(obj, prefix, out) {
  for (const [k, v] of Object.entries(obj || {})) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) flattenLeaves(v, key, out)
    else out.set(key, typeof v === 'string' ? v : null)
  }
  return out
}

async function main() {
  const dryRun = process.argv.includes('--dry-run')

  // ① 语料：8 个语种语言包的全部 .ts（跳过 index.ts —— 那是 barrel，会重复计数）
  const corpus = new Set()
  for (const loc of readdirSync(LOCALES_DIR)) {
    const dir = join(LOCALES_DIR, loc)
    if (!statSync(dir).isDirectory()) continue
    for (const f of readdirSync(dir).filter((x) => x.endsWith('.ts') && x !== 'index.ts')) {
      const src = readFileSync(join(dir, f), 'utf8')
      for (const ch of src) if (CJK_RE.test(ch)) corpus.add(ch)
    }
  }

  // ② notBig5 = Big5 转不出来的汉字  ∪  **人工审定的「Big5 收录了简体字形」正名单**
  const notBig5 = [...new Set([...corpus].filter((c) => !big5Ok(c)).concat(BIG5_TRAPS))].sort()
  const notBig5Set = new Set(notBig5)

  // ③ japaneseOk = 其中在 ja-JP「已本地化值」里出现过的（合法日文新字体）
  const scanner = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'scanner.ts')).href)
  const { loadLocale } = scanner
  const srcFlat = flattenLeaves(loadLocale('zh-CN', LOCALES_DIR), '', new Map())
  const jaFlat = flattenLeaves(loadLocale('ja-JP', LOCALES_DIR), '', new Map())
  // ★★ 关键修正：★ 「出现在已本地化值里」**不足以**证明该字是合法日文 ——
  //   **漏进来的简体字会把自己洗白**（实测：ja-JP 的「已翻译值」里藏着 授权 / 联系 / 请求 / 一键 /
  //   运维平台 / …编辑 这些简体片段 ⇒ 若把它们豁免 ⇒ 门永远看不见它们）。
  // ⇒ 故：★ japaneseOk = （出现在已本地化值里的字）**减去**一份**人工审定的「这些不是日文」清单**。
  const NOT_JAPANESE = '权维编联请键运'   // 简体形；日文对应形是 権/維/編/連/請/鍵/運（本表里已有）
  const notJapanese = new Set([...NOT_JAPANESE])
  const japaneseOk = new Set()
  for (const [k, v] of jaFlat) {
    const s = srcFlat.get(k)
    if (typeof v !== 'string' || typeof s !== 'string' || v === s) continue
    for (const ch of v) if (notBig5Set.has(ch) && !notJapanese.has(ch)) japaneseOk.add(ch)
  }

  const data = {
    provenance:
      'notBig5 = 全语料(8 语种语言包)出现、但 iconv(UTF-8→BIG-5) 转不出的汉字（繁体有、简体/日文新字体形没有）' +
      ' ∪ 人工审定的「Big5 收录了简体字形」正名单（' + BIG5_TRAPS.join('') + '：iconv 推导的盲区）；' +
      'japaneseOk = notBig5 中在 ja-JP「已本地化值」里出现过的子集（合法日文新字体，如 実/処/録/続/検/総）。',
    generator: 'node scripts/gen-cjk-native-simplified-chars.mjs（需 iconv；Windows 上用仓内已生成的数据文件）',
    knownLimits:
      '本表只覆盖当前语料出现过的汉字；新增简体字需重跑本脚本扩表（门本身不依赖 iconv，可移植）。' +
      'japaneseOk 里的每个字都应经得起「它真是日文正字吗」的复核 —— 生成器会打印本次新增的字供人眼核对。',
    notJapanese: NOT_JAPANESE,
    updated: new Date().toISOString().slice(0, 10),
    notBig5: notBig5.join(''),
    japaneseOk: [...japaneseOk].sort().join(''),
  }

  process.stdout.write(
    `语料汉字 ${corpus.size} 个｜notBig5 ${data.notBig5.length}｜japaneseOk ${data.japaneseOk.length}\n`
  )
  process.stdout.write(`人工审定「不是日文正字」清单: ${NOT_JAPANESE}（已从 japaneseOk 中排除）\n`)
  process.stdout.write(`japaneseOk 全量（供人眼核对）: ${data.japaneseOk}\n`)
  if (dryRun) {
    process.stdout.write('--dry-run：不落盘\n')
    return
  }
  writeFileSync(OUT_FILE, JSON.stringify(data, null, 2) + '\n')
  process.stdout.write(`→ ${OUT_FILE}\n`)
}

main().catch((e) => {
  console.error('gen-cjk-native-simplified-chars failed:', e)
  process.exit(1)
})
