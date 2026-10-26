#!/usr/bin/env node
// maestro/tools/slow-pages.mjs — 从 Maestro 的运行产物里筛出「打开 > 2 秒」的页面。
//
// 用法：
//   node maestro/tools/slow-pages.mjs --log maestro/reports/run.log
//   node maestro/tools/slow-pages.mjs --junit maestro/reports/junit.xml
//   node maestro/tools/slow-pages.mjs --log maestro/reports/run.log --threshold 2000
//
// 数据来源（两种都支持，按可用性择一）：
//   1) --log   Maestro 的 CLI 输出。每个页面在 flow 里被写成
//              output.ms__<slug> = <毫秒>；本脚本用正则把它们抠出来。
//   2) --junit Maestro 的 JUnit 报告。每个页面的 2s 软阈值断言带
//              label="slow-check:<route>"；失败（=超过阈值）的 testcase 名字里
//              就能读出是哪条路由。
//
// 输出：一张按耗时倒序的表 + 超过阈值的条目清单。退出码：
//   0 = 没有超过阈值的页面；1 = 有页面超过阈值（便于 CI 卡口）；2 = 用法错误。

import fs from 'node:fs'
import path from 'node:path'

function parseArgs(argv) {
  const opts = { log: null, junit: null, threshold: 2000 }
  for (const a of argv) {
    if (a === '--help' || a === '-h') opts.help = true
    else if (a.startsWith('--log=')) opts.log = a.slice(6)
    else if (a.startsWith('--junit=')) opts.junit = a.slice(8)
    else if (a.startsWith('--threshold=')) opts.threshold = Number(a.slice(12))
    else if (a === '--log') opts.log = argv[++argv.indexOf(a)]
  }
  return opts
}

const opts = parseArgs(process.argv.slice(2))
if (opts.help || (!opts.log && !opts.junit)) {
  console.log(`maestro/tools/slow-pages.mjs — 筛出打开耗时 > 阈值 的页面

  --log=<file>      Maestro CLI 输出（含 output.ms__<slug> = <ms>）
  --junit=<file>    Maestro JUnit 报告（含 slow-check:<route> 断言）
  --threshold=<ms>  阈值，默认 2000
`)
  process.exit(2)
}

const rows = new Map() // slug -> { ms, route, source }
const put = (slug, ms, route, source) => {
  const prev = rows.get(slug)
  if (prev && prev.ms >= ms) return
  rows.set(slug, { slug, ms, route, source })
}

// ── 1) CLI 日志：output.ms__<slug> = <ms> ───────────────────────────────────
if (opts.log) {
  if (!fs.existsSync(opts.log)) {
    console.error(`slow-pages: 日志不存在：${opts.log}`)
    process.exit(2)
  }
  const text = fs.readFileSync(opts.log, 'utf8')
  // 形如： ms__admin_settings = 1234   /  "ms__admin_settings" = 1234
  const re = /ms__([A-Za-z0-9_]+)\s*["']?\s*[:=]\s*(-?\d+)/g
  let m
  while ((m = re.exec(text)) !== null) {
    put(m[1], Number(m[2]), null, 'log')
  }
}

// ── 2) JUnit：失败的 testcase 里带 slow-check:<route> ───────────────────────
if (opts.junit) {
  if (!fs.existsSync(opts.junit)) {
    console.error(`slow-pages: JUnit 报告不存在：${opts.junit}`)
    process.exit(2)
  }
  const xml = fs.readFileSync(opts.junit, 'utf8')
  // 逐个 <testcase …>…</testcase> 块；块内出现 <failure> 即判定失败
  const cases = xml.match(/<testcase\b[\s\S]*?<\/testcase>/g) || []
  for (const c of cases) {
    const routeM = c.match(/slow-check:([^\s"'<&]+)/)
    if (!routeM) continue
    const failed = /<failure\b/.test(c)
    if (!failed) continue
    const msM = c.match(/ms__([A-Za-z0-9_]+)\s*["']?\s*[:=]\s*(-?\d+)/)
    const route = routeM[1]
    const slug = route.replace(/^\//, '').replace(/[/:*]/g, '_') || 'root'
    put(slug, msM ? Number(msM[2]) : -1, route, 'junit')
  }
}

if (rows.size === 0) {
  console.log('slow-pages: 没读到任何计时数据。')
  console.log('  提示：先跑 `maestro test --format junit --output maestro/reports/junit.xml …` 并保留 CLI 输出（tee 到 .log）。')
  process.exit(0)
}

const all = [...rows.values()].sort((a, b) => b.ms - a.ms)
const slow = all.filter(r => r.ms > opts.threshold || r.ms < 0)

console.log('')
console.log(`打开耗时（阈值 ${opts.threshold}ms）`)
console.log('─'.repeat(72))
for (const r of all) {
  const flag = r.ms < 0 ? ' ??  ' : r.ms > opts.threshold ? ' SLOW' : '      '
  console.log(`${flag} ${String(r.ms).padStart(7)} ms  ${(r.route || r.slug).padEnd(46)} ${r.slug}`)
}
console.log('─'.repeat(72))
console.log(`共 ${all.length} 个页面，其中 ${slow.length} 个超过 ${opts.threshold}ms`)
if (slow.length) {
  console.log('')
  console.log('超过阈值的页面：')
  for (const r of slow) console.log(`  · ${r.route || r.slug}  ${r.ms}ms`)
}
process.exit(slow.length ? 1 : 0)
