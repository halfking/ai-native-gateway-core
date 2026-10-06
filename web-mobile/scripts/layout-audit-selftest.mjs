#!/usr/bin/env node
/**
 * layout-audit-selftest.mjs —— 给 scripts/_layout-audit.mjs 装一把**正控**。
 *
 * ⚠️ 为什么必须有这个文件：判据是 398 行检测逻辑、跑在真实浏览器里，
 *   而「判据返回 issues: []」与「页面真的没问题」**长得一模一样**。
 *   没有夹具，这把尺子永远无法被证伪 —— 尺子坏了也没人知道。
 *
 * ✅ 已正控的 kind（8 / 9）：h-overflow、tap-target、invisible-text、
 *   low-contrast、broken-image、tap-overlap、covered-by-fixed、near-blank。
 *   每条都同时有「plant（必须报）」与「decoy（不许报）」两侧。
 * ⛔ 仍未正控：safe-area —— 它要先有 `--app-safe-bottom` 变量才有意义，
 *   那是**壳注入**的量（模拟器那一轮才测得到），在浏览器夹具里自造一个
 *   等于自己造契约。故意不写断言，不用假阳性换「覆盖率」。
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
 *   每次都回读 `innerWidth` 比对，不等就 exit 2 —— 绝不在错的视口上判「尺子」。
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

/** label / 夹具场景 / 视口宽。场景名对应 fixtures 页里的 ?plants=。 */
const CASES = [
  { label: '@320 全量', q: '/fixture', w: 320 },
  { label: '@1024 全量', q: '/fixture', w: 1024 },
  { label: '@320 仅小溢出', q: '/fixture?plants=small', w: 320 },
  { label: '@320 干净页', q: '/fixture?plants=none', w: 320 },
  { label: '@320 对比度', q: '/fixture?plants=contrast', w: 320 },
  { label: '@320 坏图', q: '/fixture?plants=broken', w: 320 },
  { label: '@320 重叠', q: '/fixture?plants=overlap', w: 320 },
  { label: '@320 底栏遮挡', q: '/fixture?plants=covered', w: 320 },
  { label: '@320 底栏已避让', q: '/fixture?plants=covered-ok', w: 320 },
  { label: '@320 近白屏', q: '/fixture?plants=blank', w: 320 },
  { label: '@320 底栏遮挡(滚动)', q: '/fixture?plants=covered-scroll', w: 320 },
  { label: '@320 底栏已避让(滚动)', q: '/fixture?plants=covered-scroll-ok', w: 320 },
]

const HOST_PAGE = `<!doctype html><meta charset="utf-8">
<body style="margin:0">
<iframe id="f" src="/fixture" style="width:${HOST_W}px;height:820px;border:0"></iframe>
<script>
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
  await sleep(800)
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
const kind = (r, k) => r.report.issues.find((i) => i.kind === k)
const kinds = (r) => r.report.issues.map((i) => i.kind).sort().join(',') || '（无）'
const R = Object.fromEntries(results.map((r) => [r.label, r]))

// ① h-overflow / tap-target（@320 全量）
{
  const r = R['@320 全量'], t = r.label
  const hOv = kind(r, 'h-overflow'), tap = kind(r, 'tap-target')
  if (!hOv) fails.push(`${t} 漏报 h-overflow：夹具种了三块溢出块`)
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

// ② h-overflow 的 offender 精度（@1024：480px 那块装得下，不该再出现）
{
  const r = R['@1024 全量'], t = r.label, hOv = kind(r, 'h-overflow')
  if (!hOv) fails.push(`${t} 漏报 h-overflow：1600px 那块任何视口都溢出`)
  else {
    const ids = hOv.offenders.map((o) => o.sel).join(' , ')
    if (!has(hOv.offenders, '#plant-halways')) fails.push(`${t} offenders 缺 #plant-halways。实际：${ids}`)
    if (has(hOv.offenders, '#plant-hfixed')) {
      fails.push(`${t} offenders 里仍有 #plant-hfixed —— wrap 上限 720，480px 装得下，说明 offender 过滤过宽`)
    }
  }
}

// ③ 仅小溢出：卡「容差被放宽」那种变异（大溢出会掩盖它）
{
  const r = R['@320 仅小溢出'], t = r.label, hOv = kind(r, 'h-overflow')
  if (!hOv) fails.push(`${t} 漏报 h-overflow：页面上唯一的缺陷是 ~40px 的小幅溢出，容差稍一放宽就会漏`)
  else if (!has(hOv.offenders, '#plant-hsmall')) {
    fails.push(`${t} offenders 缺 #plant-hsmall。实际：${hOv.offenders.map((o) => o.sel).join(' , ')}`)
  }
}

// ④ 干净页：一张什么都没有的页面，判据不许报任何一条
{
  const r = R['@320 干净页'], t = r.label
  for (const k of ['h-overflow', 'tap-target', 'invisible-text', 'low-contrast', 'near-blank', 'broken-image', 'tap-overlap', 'covered-by-fixed']) {
    if (kind(r, k)) fails.push(`${t} 干净页被判出 ${k}（尺子对什么都乱报）`)
  }
}

// ⑤ 对比度：同色字 → invisible-text；低对比 → low-contrast；高对比诱饵不许进
{
  const r = R['@320 对比度'], t = r.label
  const inv = kind(r, 'invisible-text'), low = kind(r, 'low-contrast')
  if (!inv) fails.push(`${t} 漏报 invisible-text：白底白字 ratio≈1.00 < 1.25`)
  else if (!has(inv.items, '#plant-invisible')) {
    fails.push(`${t} invisible-text items 缺 #plant-invisible。实际：${inv.items.map((o) => o.sel).join(' , ')}`)
  }
  if (!low) fails.push(`${t} 漏报 low-contrast：#999 on #fff ratio≈2.85 < 4.5`)
  else {
    if (!has(low.items, '#plant-lowc')) fails.push(`${t} low-contrast items 缺 #plant-lowc。实际：${low.items.map((o) => o.sel).join(' , ')}`)
    if (has(low.items, '#decoy-contrast')) fails.push(`${t} low-contrast 误报 #decoy-contrast（#333 on #fff ratio≈12.6 是安全的）`)
  }
}

// ⑥ 坏图
{
  const r = R['@320 坏图'], t = r.label, bk = kind(r, 'broken-image')
  if (!bk) fails.push(`${t} 漏报 broken-image：src 指向不存在的文件`)
  else {
    const ids = bk.items.map((o) => o.sel).join(' , ')
    // ⚠️ 判据的 path() 只拼 tag#id.class，不含属性 ⇒ 断言必须按 id 匹配，不能写 [alt=…]
    if (!has(bk.items, '#plant-broken-img')) fails.push(`${t} broken-image items 里没有那张坏图。实际：${ids}`)
    if (has(bk.items, '#decoy-good-img')) fails.push(`${t} broken-image 误报了 data URI 的好图。实际：${ids}`)
  }
}

// ⑦ 触控重叠
{
  const r = R['@320 重叠'], t = r.label, ov = kind(r, 'tap-overlap')
  if (!ov) fails.push(`${t} 漏报 tap-overlap：两个 120×120 按钮重叠 > 30%`)
  else {
    const ids = ov.items.map((o) => `${o.a}~${o.b}`).join(' , ')
    if (!ids.includes('#plant-ov-a') || !ids.includes('#plant-ov-b')) {
      fails.push(`${t} tap-overlap 没指向那两个重叠按钮。实际：${ids}`)
    }
    if (ids.includes('#decoy-ov-a') || ids.includes('#decoy-ov-b')) {
      fails.push(`${t} tap-overlap 误报了两个分开的诱饵按钮。实际：${ids}`)
    }
  }
}

// ⑧ 固定底栏遮挡：压住时报，padding 够时不报
{
  const r1 = R['@320 底栏遮挡'], r2 = R['@320 底栏已避让']
  if (!kind(r1, 'covered-by-fixed')) {
    fails.push(`${r1.label} 漏报 covered-by-fixed：main 末尾被 56px 固定底栏压住`)
  }
  if (kind(r2, 'covered-by-fixed')) {
    fails.push(`${r2.label} 误报 covered-by-fixed：main 已有 80px padding-bottom，末行点得到`)
  }
  // 同一条判据的**另一条分支**：main 是可滚动容器 → 走 slack 判定。
  // ★ 这两条必须成对：covered-by-fixed 的条件在两条分支里各出现一次
  //   （`padB < bottomBar.r.height - 2`），只覆盖一条的话改坏另一条不会被发现（M6 实测）。
  const r3 = R['@320 底栏遮挡(滚动)'], r4 = R['@320 底栏已避让(滚动)']
  if (!kind(r3, 'covered-by-fixed')) {
    fails.push(`${r3.label} 漏报 covered-by-fixed：main 是可滚动容器且末尾 slack < 底栏高`)
  }
  if (kind(r4, 'covered-by-fixed')) {
    fails.push(`${r4.label} 误报 covered-by-fixed：可滚动容器已有 80px padding-bottom`)
  }
}

// ⑨ 近白屏
{
  const r = R['@320 近白屏'], t = r.label
  if (!kind(r, 'near-blank')) {
    fails.push(`${t} 漏报 near-blank：整页仅 ${r.report.stats.domNodes} 个节点 < 门槛 20`)
  }
}

for (const r of results) console.log(`判据在 ${r.label.padEnd(16)} 返回：${kinds(r)}`)
if (fails.length) {
  console.error(`\n✗ 自检失败 ${fails.length} 条：`)
  for (const f of fails) console.error('  · ' + f)
  process.exit(1)
}
console.log('\n✓ 自检通过：8/9 条判据的 plant 全部被抓到、decoy 与干净页都没被误报 —— 尺子有牙')
console.log('  （safe-area 故意未正控：它依赖壳注入的 --app-safe-bottom，自造等于自造契约）')