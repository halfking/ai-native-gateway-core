#!/usr/bin/env node
// maestro/tools/slow-pages.mjs — 从 Maestro 的运行产物里筛出「打开 > N 秒」的页面。
//
// ★★ 2026-10-28（R176）重写的理由：
//   旧版从 **CLI 文本**里正则抠 `ms__<slug> = <ms>` —— 实跑证明**抠不到**：
//   Maestro 不会把 evalScript 的赋值结果打进 CLI 输出（只有 "Run ${...}... COMPLETED"）。
//   ⇒ 旧版的 `--log` 路径是**假通道**（永远读不到数）。
//   真正的机器可读通道是 Maestro 调试产物里的 **commands.json**：
//   每条命令都带 `metadata.duration`（毫秒）⇒
//     **页面打开耗时 = openLink 的 duration + 其后 extendedWaitUntil 的 duration**
//     （后者就是「等该页关键文案出现」花掉的墙钟）。
//   实测对拍（2026-10-28）：/forbidden 的 openLink=813ms + extendedWaitUntil=9275ms
//   = 10088ms，与 flow 里 output.ms__forbidden 实测的 10463ms 同量级 ⇒ 通道可信。
//
// 用法：
//   node maestro/tools/slow-pages.mjs --artifacts maestro/reports/<时间戳>
//   node maestro/tools/slow-pages.mjs --artifacts maestro/reports/<时间戳> --threshold 2000
//   node maestro/tools/slow-pages.mjs --junit maestro/reports/junit.xml   # 备用路径
//
// 输出：一张按耗时倒序的表 + 超过阈值的清单。
// 退出码：0 = 没有超过阈值的页面；1 = 有页面超过阈值（可当 CI 卡口）；2 = 用法错误。

import fs from 'node:fs'
import path from 'node:path'

function parseArgs(argv) {
  const opts = { artifacts: null, junit: null, threshold: 2000 }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--help' || a === '-h') opts.help = true
    else if (a.startsWith('--artifacts=')) opts.artifacts = a.slice(12)
    else if (a === '--artifacts') opts.artifacts = argv[++i]
    else if (a.startsWith('--junit=')) opts.junit = a.slice(8)
    else if (a === '--junit') opts.junit = argv[++i]
    else if (a.startsWith('--threshold=')) opts.threshold = Number(a.slice(12))
    else if (a === '--threshold') opts.threshold = Number(argv[++i])
  }
  return opts
}

const opts = parseArgs(process.argv.slice(2))
if (opts.help || (!opts.artifacts && !opts.junit)) {
  console.log(`maestro/tools/slow-pages.mjs — 筛出打开耗时 > 阈值 的页面

  --artifacts=<dir>  Maestro 调试产物目录（内含 commands.json；可多层级）
  --junit=<file>     备用：JUnit 报告（含 slow-check:<route> 断言）
  --threshold=<ms>   阈值，默认 2000
`)
  process.exit(2)
}

/** slug -> { ms, route, flow } */
const rows = new Map()
const put = (slug, ms, route, flow) => {
  const prev = rows.get(slug)
  // 同一路由可能被多个 flow 覆盖（重定向/别名）⇒ 取最慢的一次，定位最坏情况。
  if (prev && prev.ms >= ms) return
  rows.set(slug, { slug, ms, route, flow })
}

// ── 1) commands.json：openLink + 其后的可见性等待 ───────────────────────────
if (opts.artifacts) {
  const root = path.resolve(opts.artifacts)
  if (!fs.existsSync(root)) {
    console.error(`slow-pages: 产物目录不存在：${root}`)
    process.exit(2)
  }
  const files = []
  const walk = (dir) => {
    let entries = []
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true })
    } catch {
      return
    }
    for (const e of entries) {
      const p = path.join(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.name === 'commands.json') files.push(p)
    }
  }
  walk(root)

  for (const file of files) {
    let cmds = []
    try {
      cmds = JSON.parse(fs.readFileSync(file, 'utf8'))
    } catch {
      continue
    }
    if (!Array.isArray(cmds)) continue
    const flow = path.basename(path.dirname(file))

    // 状态机：记住最近一次 openLink；其后第一条「可见性等待」的 duration 加总。
    let pending = null // { route, linkMs }
    for (const c of cmds) {
      const cmd = c.command || {}
      const meta = c.metadata || {}
      const dur = Number(meta.duration || 0)
      const key = Object.keys(cmd).find((k) => k.endsWith('Command'))
      const ev = meta.evaluatedCommand || cmd
      const evKey = Object.keys(ev).find((k) => k.endsWith('Command'))
      const body = ev[evKey] || {}

      if (key === 'openLinkCommand') {
        const link = String(body.link || '')
        pending = { route: routeOf(link), linkMs: dur }
        continue
      }
      if (key === 'assertConditionCommand') {
        const cond = body.condition || {}
        const visible = cond.visible
        // 只认「等可见」的断言（extendedWaitUntil）；notVisible / scriptCondition 不算。
        if (visible && pending) {
          const slug = slugOf(pending.route)
          put(slug, pending.linkMs + dur, pending.route, flow)
          pending = null
          continue
        }
        if (!visible) continue
      }
      if (key === 'evalScriptCommand' && pending) {
        // 没等到可见性断言就换页 ⇒ 该页没有可计时的关键文案。
        pending = null
      }
    }
  }
}

function routeOf(link) {
  if (!link) return '(unknown)'
  const m = link.match(/^https?:\/\/[^/]+(\/[^?#]*)/)
  let r = m ? m[1] : link
  if (!r.startsWith('/')) r = '/' + r
  return r
}
function slugOf(route) {
  return (route.replace(/^\//, '') || 'root').replace(/[:*]/g, '_')
}

// ── 2) JUnit 备用路径 ───────────────────────────────────────────────────────
if (opts.junit) {
  if (!fs.existsSync(opts.junit)) {
    console.error(`slow-pages: JUnit 报告不存在：${opts.junit}`)
    process.exit(2)
  }
  const xml = fs.readFileSync(opts.junit, 'utf8')
  const cases = xml.match(/<testcase\b[\s\S]*?<\/testcase>/g) || []
  for (const c of cases) {
    const routeM = c.match(/slow-check:([^\s"'<&]+)/)
    if (!routeM) continue
    if (!/<failure\b/.test(c)) continue
    const route = routeM[1]
    put(slugOf(route), -1, route, 'junit')
  }
}

if (rows.size === 0) {
  console.log('slow-pages: 没读到任何计时数据。')
  console.log('  提示：用 --artifacts 指向 maestro/reports/<时间戳> 目录（内含 commands.json）。')
  process.exit(0)
}

const all = [...rows.values()].sort((a, b) => b.ms - a.ms)
const slow = all.filter((r) => r.ms > opts.threshold || r.ms < 0)

console.log('')
console.log(`打开耗时（阈值 ${opts.threshold}ms；口径 = openLink + 可见性等待）`)
console.log('─'.repeat(78))
for (const r of all) {
  const flag = r.ms < 0 ? ' ??  ' : r.ms > opts.threshold ? ' SLOW' : '      '
  console.log(`${flag} ${String(r.ms).padStart(7)} ms  ${(r.route || r.slug).padEnd(48)} ${r.flow || ''}`)
}
console.log('─'.repeat(78))
console.log(`共 ${all.length} 个页面，其中 ${slow.length} 个超过 ${opts.threshold}ms`)
if (slow.length) {
  console.log('')
  console.log('超过阈值的页面：')
  for (const r of slow) console.log(`  · ${r.route || r.slug}  ${r.ms}ms`)
}
process.exit(slow.length ? 1 : 0)
