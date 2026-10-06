#!/usr/bin/env node
/**
 * layout-audit-selftest.mjs —— 给 scripts/_layout-audit.mjs 装一把**正控**。
 *
 * ⚠️ 为什么必须有这个文件：判据是 398 行检测逻辑、跑在真实浏览器里，
 *   而「判据返回 issues: []」与「页面真的没问题」**长得一模一样**。
 *   没有夹具，这把尺子永远无法被证伪 —— 尺子坏了也没人知道。
 *
 * ⚠️ 断言范围（刻意收窄）：只断言**我逐行读过、能从源码推出结论**的判据：
 *   ① h-overflow（`scrollWidth > innerWidth + 1` 触发 + offender 过滤）
 *   ② tap-target（`MIN_TAP = 44`，`w<44 || h<44`）
 *   其余 kind（low-contrast / covered-by-fixed / invisible-text / near-blank …）
 *   阈值与启发式没逐行读完，**不写断言** —— 夹具没踩中它们阈值时，
 *   失败的是我的夹具不是尺子，那种红是噪声。
 *
 * ★★ 视口怎么定（踩了三个坑才定下来，勿改）：
 *   ① `Emulation.setDeviceMetricsOverride` 在本机 headless 下**经常不生效**
 *      （请求 320/1024，实得 1280/1753）；
 *   ② `--window-size=320` 也不行 —— 本机 Chrome **最小窗口宽 500 CSS px**，
 *      旧 headless 与 --headless=new 都一样；
 *   ③ 而且**超宽内容会把 headless 窗口本身撑大**，内宽跟着变。
 *   ⇒ 正解：外层页面固定 1400px（永不被内容撑开），把夹具放进**同源 iframe**，
 *     用 iframe 的宽度当视口，并 CDP `createIsolatedWorld` 把判据注入 iframe
 *     自己的 realm（`contentWindow.eval` 跨 realm 不可用）。
 *     实测请求 320/360/1024 ⇒ 实得 320/360/1024，分毫不差。
 *   每次都回读 `innerWidth` 比对，不等就 exit 2 —— 绝不在错的视口上判「尺子」。
 *
 * ★ 三套场景缺一不可（第一版只有「全量」，结果被变异 M1 穿了）：
 *   全量     —— 大溢出 + 小溢出 + 诱饵 + 小热区
 *   小溢出   —— 只有 ~40px 的溢出 ⇒ 卡「容差被从 +1 放宽到 +200」那种变异
 *   干净页   —— 无缺陷无诱饵     ⇒ 卡「判据对什么都乱报」
 *
 * 用法：node scripts/layout-audit-selftest.mjs
 * 退出码：0 = 尺子有牙；1 = 断言没过（逐条打印为什么）；2 = 环境没生效。
 */
import { spawn } from 'node:child_process'
import { createServer } from 'node:http'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'
import { layoutAudit } from './_layout-audit.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURE = readFileSync(join(HERE, 'fixtures', 'layout-audit-fixture.html'), 'utf8')
const CHROME = process.env.CHROME_BIN ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const AUDIT_SRC = layoutAudit.toString()
const HOST_W = 1400

const CASES = [
  { label: '@320 全量', q: '/fixture', w: 320 },
  { label: '@1024 全量', q: '/fixture', w: 1024 },
  { label: '@320 仅小溢出', q: '/fixture?plants=small', w: 320 },
  { label: '@320 干净页', q: '/fixture?plants=none', w: 320 },
]

const HOST_PAGE = `<!doctype html><meta charset="utf-8">
<body style="margin:0">
<iframe id="f" src="/fixture" style="width:${HOST_W}px;height:820px;border:0"></iframe>
<script>
  // 换 src 而不是 location.reload() —— 换 src 会重建执行上下文，
  // 外层正好按新 frameId 重新 createIsolatedWorld。
  window.setFixture = (w, q) => { const f = document.getElementById('f'); f.style.width = w + 'px'; f.src = q }
</script>`

const server = createServer((req, res) => {
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
  res.end(req.url.startsWith('/host') ? HOST_PAGE : FIXTURE)
})
await new Promise((r) => server.listen(0, '127.0.0.1', r))
const WEB_PORT = server.address().port

class CDP {
  constructor(w) { this.w = w; this.i = 0; this.p = new Map() }
  static async connect(u) {
    const w = new WebSocket(u)
    await new Promise((res, rej) => { w.onopen = res; w.onerror = () => rej(new Error('ws 连接失败')) })
    const c = new CDP(w)
    w.onmessage = (e) => {
      const m = JSON.parse(e.data)
      if (m.id && c.p.has(m.id)) { c.p.get(m.id)(m); c.p.delete(m.id) }
    }
    return c
  }
  send(method, params = {}) {
    const id = ++this.i
    return new Promise((res, rej) => {
      this.p.set(id, res)
      this.w.send(JSON.stringify({ id, method, params }))
      setTimeout(() => rej(new Error(`${method} 超时`)), 20000)
    })
  }
}

const PORT = 9700 + Math.floor(Math.random() * 250)
const chrome = spawn(
  CHROME,
  ['--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
   `--window-size=${HOST_W + 40},900`, `--remote-debugging-port=${PORT}`,
   `--user-data-dir=/tmp/wm-selftest-${PORT}`, 'about:blank'],
  { stdio: 'ignore' },
)
const stop = () => { try { chrome.kill('SIGKILL') } catch {} ; server.close() }
process.on('exit', stop)

let page = null
for (let i = 0; i < 40; i++) {
  try {
    const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json()
    page = list.find((t) => t.type === 'page')
    if (page) break
  } catch {}
  await sleep(300)
}
if (!page) { console.error('✗ 环境没生效：Chrome 调试口没起来'); stop(); process.exit(2) }

const cdp = await CDP.connect(page.webSocketDebuggerUrl)
await cdp.send('Page.enable')
await cdp.send('Runtime.enable')
await cdp.send('Page.navigate', { url: `http://127.0.0.1:${WEB_PORT}/host` })
await sleep(1000)

/** 在 iframe 自己的 realm 里跑判据（不能靠 contentWindow.eval —— 跨 realm 不可用）。 */
async function runInFrame() {
  const tree = await cdp.send('Page.getFrameTree')
  const frame = tree.result?.frameTree?.childFrames?.find((f) => (f.frame.url || '').includes('/fixture'))
  if (!frame) throw new Error('找不到 iframe 的 frame')
  const world = await cdp.send('Page.createIsolatedWorld', {
    frameId: frame.frame.id, worldName: 'layout-audit-selftest', grantUniveralAccess: true,
  })
  const ctx = world.result?.executionContextId
  if (ctx === undefined) throw new Error('createIsolatedWorld 没返回 executionContextId')
  const vwRes = await cdp.send('Runtime.evaluate', { expression: 'innerWidth', contextId: ctx, returnByValue: true })
  const auditRes = await cdp.send('Runtime.evaluate', {
    expression: `(${AUDIT_SRC})()`, contextId: ctx, returnByValue: true,
  })
  return { vw: vwRes.result?.result?.value, report: auditRes.result?.result?.value,
           raw: JSON.stringify(auditRes.result).slice(0, 300) }
}

const results = []
for (const c of CASES) {
  await cdp.send('Runtime.evaluate', { expression: `setFixture(${c.w}, ${JSON.stringify(c.q)})` })
  await sleep(900)
  let got
  try { got = await runInFrame() } catch (e) {
    console.error(`✗ 环境没生效（不是判据的问题）：${c.label} ${e.message}`); stop(); process.exit(2)
  }
  if (got.vw !== c.w) {
    console.error(`✗ 环境没生效（不是判据的问题）：${c.label} 请求 ${c.w}，iframe 实得 ${got.vw}`); stop(); process.exit(2)
  }
  if (!got.report) { console.error(`✗ 判据在 ${c.label} 没返回结果 ——`, got.raw); stop(); process.exit(1) }
  results.push({ ...c, report: got.report })
}
stop()

// ---- 断言 ----
const fails = []
const has = (arr, id) => (arr ?? []).some((o) => (o.sel || '').includes(id))
const kinds = (r) => r.report.issues.map((i) => i.kind).sort().join(',') || '（无）'

const full320 = results[0], full1024 = results[1], small = results[2], clean = results[3]

// ① 全量 @320：两条溢出 + 小热区都抓到；诱饵一个都不许误报
{
  const t = full320.label, hOv = full320.report.issues.find((i) => i.kind === 'h-overflow')
  const tap = full320.report.issues.find((i) => i.kind === 'tap-target')
  if (!hOv) fails.push(`${t} 漏报 h-overflow：夹具种了三块溢出块，不可能不溢出`)
  else {
    const ids = hOv.offenders.map((o) => o.sel).join(' , ')
    if (!has(hOv.offenders, '#plant-halways')) fails.push(`${t} offenders 缺 #plant-halways（1600px）。实际：${ids}`)
    if (!has(hOv.offenders, '#plant-hfixed')) fails.push(`${t} offenders 缺 #plant-hfixed（480px > 320）。实际：${ids}`)
    if (!has(hOv.offenders, '#plant-hsmall')) fails.push(`${t} offenders 缺 #plant-hsmall（小幅溢出）。实际：${ids}`)
    if (has(hOv.offenders, '#decoy-flex') || has(hOv.offenders, '#decoy-pct')) {
      fails.push(`${t} 误报诱饵（省略号截断 + overflow:hidden 实测安全）：${ids}`)
    }
  }
  if (!tap) fails.push(`${t} 漏报 tap-target：夹具有 24×24 按钮 < MIN_TAP 44`)
  else {
    const ids = tap.items.map((o) => o.sel).join(' , ')
    if (!has(tap.items, '#plant-tap-small')) fails.push(`${t} tap-target items 缺 #plant-tap-small。实际：${ids}`)
    if (has(tap.items, '#decoy-tap-ok')) fails.push(`${t} tap-target 误报 #decoy-tap-ok（48×48 ≥ 44 是安全的）。实际：${ids}`)
  }
}

// ② 全量 @1024：480px 那块装得下 ⇒ 不该再出现在 offenders（offender 过滤的精度）
{
  const t = full1024.label, hOv = full1024.report.issues.find((i) => i.kind === 'h-overflow')
  if (!hOv) fails.push(`${t} 漏报 h-overflow：1600px 那块任何视口都溢出`)
  else {
    const ids = hOv.offenders.map((o) => o.sel).join(' , ')
    if (!has(hOv.offenders, '#plant-halways')) fails.push(`${t} offenders 缺 #plant-halways。实际：${ids}`)
    if (has(hOv.offenders, '#plant-hfixed')) {
      fails.push(`${t} offenders 里仍有 #plant-hfixed —— wrap 上限 720，480px 装得下，说明 offender 过滤过宽`)
    }
  }
}

// ③ 仅小溢出：卡「容差被放宽」那种变异（大溢出那两条会掩盖它）
{
  const t = small.label, hOv = small.report.issues.find((i) => i.kind === 'h-overflow')
  if (!hOv) {
    fails.push(`${t} 漏报 h-overflow：页面上唯一的缺陷是 ~40px 的小幅溢出，容差稍一放宽就会漏`)
  } else if (!has(hOv.offenders, '#plant-hsmall')) {
    fails.push(`${t} offenders 缺 #plant-hsmall。实际：${hOv.offenders.map((o) => o.sel).join(' , ')}`)
  }
}

// ④ 干净页：一张什么都没有的页面，判据不许报 h-overflow
{
  const t = clean.label, hOv = clean.report.issues.find((i) => i.kind === 'h-overflow')
  if (hOv) {
    fails.push(`${t} 干净页被判出 h-overflow（尺子对什么都乱报）：${hOv.offenders.map((o) => o.sel).join(' , ')}`)
  }
}

for (const r of results) console.log(`判据在 ${r.label.padEnd(14)} 返回：${kinds(r)}`)
if (fails.length) {
  console.error(`\n✗ 自检失败 ${fails.length} 条：`)
  for (const f of fails) console.error('  · ' + f)
  process.exit(1)
}
console.log('\n✓ 自检通过：种下的缺陷全部被抓到（含小幅溢出）、诱饵与干净页都没被误报 —— 尺子有牙')