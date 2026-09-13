#!/usr/bin/env node
// responsive-audit.mjs — @media / @container 断点白名单审计 CLI（方案 §4.2，2026-09-13）
//
// 用法：
//   node scripts/responsive-audit.mjs                          # 人类可读报告（默认 WARN 不失败）
//   node scripts/responsive-audit.mjs --strict                 # 存在白名单外断点则退出 1
//   node scripts/responsive-audit.mjs --strict --allow-legacy  # 白名单外存量降级为 WARN，退出 0
//   node scripts/responsive-audit.mjs --json                   # 输出 JSON 给 CI / 工具消费
//   node scripts/responsive-audit.mjs --src=<dir>              # 自定义源码目录（默认 web/src）
//
// 断点白名单唯一事实源是 src/config/breakpoints.ts 的 MEDIA_QUERY_WHITELIST；
// 本脚本优先直接 import（Node strip-types），失败时按正则从源文件提取作为兜底。
// 只审计宽度类条件（min-width / max-width / width 的 px 值），@media 与
// @container（容器查询）同一白名单与分级；prefers-color-scheme / hover /
// print 等非宽度特性不计入。
// 注意：Step 1 只接入脚本不收紧（存量碎片值多，--allow-legacy 放行），Step 8 收敛。

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const __dirname = new URL('.', import.meta.url).pathname
const ROOT = resolve(__dirname, '..')

function parseArgs(argv) {
  const opts = { src: join(ROOT, 'src'), json: false, strict: false, allowLegacy: false, help: false }
  for (const a of argv) {
    if (a === '--json') opts.json = true
    else if (a === '--strict') opts.strict = true
    else if (a === '--allow-legacy') opts.allowLegacy = true
    else if (a === '--help' || a === '-h') opts.help = true
    else if (a.startsWith('--src=')) opts.src = resolve(a.slice(6))
  }
  return opts
}

function printHelp() {
  console.log(`
responsive-audit — @media/@container 断点白名单审计（白名单源：src/config/breakpoints.ts）

USAGE
  node scripts/responsive-audit.mjs [options]

OPTIONS
  --strict         存在白名单外断点时退出码 1（默认仅 WARN）
  --allow-legacy   白名单外存量降级为 WARN（与 --strict 连用时不失败）
  --json           输出 JSON 报告
  --src=<dir>      自定义源码目录（默认 <repo>/web/src）
  -h, --help       显示本帮助
`)
}

/** 白名单加载：优先 import TS 单一事实源，失败时正则兜底提取。 */
async function loadWhitelist(srcDir) {
  try {
    const mod = await import(pathToFileURL(join(srcDir, 'config', 'breakpoints.ts')).href)
    if (Array.isArray(mod.MEDIA_QUERY_WHITELIST)) return [...mod.MEDIA_QUERY_WHITELIST]
  } catch {
    /* strip-types 不可用时走兜底 */
  }
  const raw = readFileSync(join(srcDir, 'config', 'breakpoints.ts'), 'utf8')
  const m = raw.match(/MEDIA_QUERY_WHITELIST[^=]*=\s*\[([^\]]*)\]/)
  if (!m) throw new Error('无法从 breakpoints.ts 提取 MEDIA_QUERY_WHITELIST')
  return m[1].split(',').map((s) => parseInt(s.trim(), 10)).filter((n) => Number.isFinite(n))
}

function listFiles(dir, exts, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) listFiles(p, exts, out)
    else if (exts.some((e) => name.endsWith(e))) out.push(p)
  }
  return out
}

/** 提取一个文件中所有 @media / @container 条件的宽度断点值（含行号）。
 *  @container 可带容器名前缀（如 `@container card (min-width: 400px)`），
 *  prelude 整体捕获后按同一宽度特性正则取值。 */
function extractWidthHits(content) {
  const hits = []
  const preludeRe = /@(?:media|container)([^{]*){/g
  let m
  while ((m = preludeRe.exec(content)) !== null) {
    const prelude = m[1]
    const line = content.slice(0, m.index).split('\n').length
    const valueRe = /(?:min-width|max-width|width)\s*:\s*(\d+)px/g
    let v
    while ((v = valueRe.exec(prelude)) !== null) hits.push({ value: parseInt(v[1], 10), line })
  }
  return hits
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  if (opts.help) { printHelp(); process.exit(0) }

  const whitelist = await loadWhitelist(opts.src)
  const whitelistSet = new Set(whitelist)
  const files = listFiles(opts.src, ['.vue', '.css'])

  const perFile = []
  const valueCounts = new Map()
  let mediaQueryCount = 0

  for (const file of files) {
    const content = readFileSync(file, 'utf8')
    const hits = extractWidthHits(content)
    if (hits.length === 0) continue
    mediaQueryCount += hits.length
    const legacy = hits.filter((h) => !whitelistSet.has(h.value))
    for (const h of hits) valueCounts.set(h.value, (valueCounts.get(h.value) ?? 0) + 1)
    if (legacy.length > 0) {
      perFile.push({ file: relative(ROOT, file), legacy })
    }
  }

  const legacyTotal = perFile.reduce((n, f) => n + f.legacy.length, 0)
  const widthValues = [...valueCounts.entries()]
    .map(([value, count]) => ({ value, count, whitelisted: whitelistSet.has(value) }))
    .sort((a, b) => b.count - a.count)

  // pass 判定：未开 strict 恒通过；开 strict 后 legacy 需为 0，或显式 allow-legacy 放行存量
  const pass = !opts.strict || legacyTotal === 0 || opts.allowLegacy

  if (opts.json) {
    console.log(JSON.stringify({
      whitelist, filesScanned: files.length, mediaQueries: mediaQueryCount,
      widthValues, legacyFiles: perFile, legacyTotal,
      strict: opts.strict, allowLegacy: opts.allowLegacy, pass,
    }, null, 2))
  } else {
    console.log(`responsive-audit — 断点白名单 ${JSON.stringify(whitelist)}`)
    console.log(`扫描 ${files.length} 个文件（.vue/.css），命中 ${mediaQueryCount} 处宽度断点（@media/@container）\n`)
    console.log('断点值分布（次数降序）：')
    for (const { value, count, whitelisted } of widthValues) {
      console.log(`  ${String(value).padStart(5)}px  x${String(count).padEnd(4)} ${whitelisted ? '✓ 白名单' : '✗ 白名单外（存量）'}`)
    }
    if (perFile.length > 0) {
      console.log(`\n白名单外断点明细（${legacyTotal} 处，${perFile.length} 个文件）：`)
      for (const { file, legacy } of perFile) {
        const summary = legacy.map((h) => `${h.value}px@L${h.line}`).join(', ')
        console.log(`  ${file}: ${summary}`)
      }
    }
    console.log('')
    if (legacyTotal > 0 && opts.strict && !opts.allowLegacy) {
      console.log(`STRICT FAIL — ${legacyTotal} 处白名单外断点（--allow-legacy 可放行存量）`)
    } else if (legacyTotal > 0) {
      console.log(`WARN — ${legacyTotal} 处白名单外存量断点放行（Step 8 收敛；后续可去 --allow-legacy 收紧）`)
    } else {
      console.log('PASS — 所有 @media/@container 宽度断点均在白名单内')
    }
  }
  process.exitCode = pass ? 0 : 1
}

main().catch((err) => { console.error(err); process.exitCode = 1 })
