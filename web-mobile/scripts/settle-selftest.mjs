#!/usr/bin/env node
/**
 * settle-selftest.mjs —— 给 waitForSettle 自己搭前提。
 *
 * 为什么要它：waitForSettle 决定「这一页到底检没检」。判据本身错了，
 * 上层就会把「没测到」读成「没问题」—— 上一轮就是这么丢掉 6 个页面态的
 * （Home / Usage / 登录 三页各 × 明暗，都渲染了内容却被判成永不 settle）。
 *
 * ⚠️ 判据自证的常见陷阱：只测「该过的过了」。所以下面**同时**有该过和不该过的：
 *   · static-card  该 settle（已渲染完、但不是列表 —— 就是 Home/Usage 的形状）
 *   · slow-stream  **不该** settle（每 400ms 增长；防「稳定」判据把数据流吞成终态）
 *   · blank        **不该** settle（否则会把白屏吞掉，near-blank 就再也报不出来）
 *   · skeleton     **不该** settle（永久骨架屏）
 *   · list-rows    该 settle（老路径别被我改坏）
 *   · error-view   该 settle（错误态）
 *
 * 用法：node scripts/settle-selftest.mjs
 */
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, rmSync, existsSync } from 'node:fs'
import { setTimeout as sleep } from 'node:timers/promises'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { waitForSettle } from './_layout-audit.mjs'

const PORT_HTTP = 9455
const PORT_CDP = 9456
const SETTLE_SRC = waitForSettle.toString()
const CAP = 5000

const LOREM = '本页用于验证终态判定：内容已渲染完成且不再变化，因此应当被判为稳定终态。'

const FIXTURES = {
  // ① 已渲染完、但不是列表：没有骨架、没有 .state-view、没有列表行。
  //    这正是 HomeView（.data-card）/ UsageView（.table）/ LoginView（form）的形状。
  'static-card': {
    want: { settled: true, state: 'stable' },
    html: `<body><main class="page"><h1>68,059</h1><p>${LOREM}</p>
      <div class="data-card"><div class="card-row">68,059 请求</div>
      <div class="card-row">764,300,408 tokens</div>
      <div class="card-row">$251.12</div></div></main></body>`,
  },
  // ② 内容还在缓慢增长 → 绝不能判终态（否则会把「还在加载」读成「已加载完」）
  'slow-stream': {
    want: { settled: false },
    html: `<body><main><p id="sink">start</p></main>
      <script>let n=0; setInterval(()=>{document.getElementById('sink').textContent =
        'chunk-'+(n++)+'-'+'x'.repeat(n);},400);<\/script></body>`,
  },
  // ③ 空页 → 绝不能判终态（要留给 ⑨ near-blank 去报）
  blank: { want: { settled: false }, html: `<body><div>err</div></body>` },
  // ④ 永久骨架屏 → 绝不能判终态
  skeleton: {
    want: { settled: false, state: 'initialLoading' },
    html: `<body><div class="state-view" aria-busy="true">
      <div class="state-view__row"><div class="skeleton state-view__line"></div></div>
      <div class="state-view__row"><div class="skeleton state-view__line"></div></div></div></body>`,
  },
  // ⑤ 老路径：列表行 → 仍然要判 content（别把已修好的东西改坏）
  'list-rows': {
    want: { settled: true, state: 'content' },
    html: `<body><div class="card-list"><div class="hyper-list__row">a</div>
      <div class="hyper-list__row">b</div><div class="hyper-list__row">c</div></div></body>`,
  },
  // ⑥ 错误态 → 仍然要判终态
  'error-view': {
    want: { settled: true },
    html: `<body><div class="state-view state-view--center">
      <p class="state-view__text">加载失败</p>
      <p class="state-view__hint">Server temporarily unavailable</p></div></body>`,
  },
}

class CDP {
  constructor(ws) { this.ws = ws; this.id = 0; this.pending = new Map() }
  static async connect(wsUrl) {
    const ws = new WebSocket(wsUrl)
    await new Promise((res, rej) => {
      ws.addEventListener('open', res, { once: true })
      ws.addEventListener('error', () => rej(new Error('ws 连接失败')), { once: true })
    })
    const c = new CDP(ws)
    ws.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data)
      if (m.id && c.pending.has(m.id)) {
        const { res, rej } = c.pending.get(m.id); c.pending.delete(m.id)
        m.error ? rej(new Error(m.error.message)) : res(m.result)
      }
    })
    return c
  }
  send(method, params = {}) {
    const id = ++this.id
    return new Promise((res, rej) => {
      this.pending.set(id, { res, rej })
      this.ws.send(JSON.stringify({ id, method, params }))
      setTimeout(() => { if (this.pending.has(id)) { this.pending.delete(id); rej(new Error(method + ' 超时')) } }, 30000)
    })
  }
  close() { try { this.ws.close() } catch { /* noop */ } }
}

const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const PROFILE = path.join(os.tmpdir(), 'llmgw-settle-selftest-profile')

const server = http.createServer((req, res) => {
  const name = (req.url || '/').replace(/^\//, '') || 'static-card'
  const f = FIXTURES[name]
  if (!f) { res.writeHead(404); return res.end('no fixture') }
  res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
  res.end(f.html)
})

async function main() {
  if (!existsSync(CHROME)) { console.error('✗ 找不到 Chrome：' + CHROME); process.exit(2) }
  if (existsSync(PROFILE)) rmSync(PROFILE, { recursive: true, force: true })

  await new Promise((r) => server.listen(PORT_HTTP, '127.0.0.1', r))
  const chrome = spawn(CHROME, [
    '--headless=new', `--remote-debugging-port=${PORT_CDP}`, `--user-data-dir=${PROFILE}`,
    '--no-first-run', '--no-default-browser-check', 'about:blank',
  ], { stdio: 'ignore' })
  const cleanup = () => { try { chrome.kill('SIGKILL') } catch { /* noop */ } try { server.close() } catch { /* noop */ } }
  process.on('exit', cleanup)

  let ver = null
  for (let i = 0; i < 40; i++) {
    try { ver = await (await fetch(`http://127.0.0.1:${PORT_CDP}/json/version`)).json(); break } catch { await sleep(500) }
  }
  if (!ver) { console.error('✗ Chrome devtools 端口未就绪'); process.exit(2) }

  const t = await (await fetch(`http://127.0.0.1:${PORT_CDP}/json/new?about:blank`, { method: 'PUT' })).json()
  const cdp = await CDP.connect(t.webSocketDebuggerUrl)
  await cdp.send('Page.enable'); await cdp.send('Runtime.enable')

  const results = []
  for (const [name, f] of Object.entries(FIXTURES)) {
    await cdp.send('Page.navigate', { url: `http://127.0.0.1:${PORT_HTTP}/${name}` })
    await sleep(600) // 让 slow-stream 至少长两轮
    const r = await cdp.send('Runtime.evaluate', {
      returnByValue: true, awaitPromise: true, expression: `(${SETTLE_SRC})(${CAP})`,
    })
    const got = r.result?.value || { settled: null, state: 'probe-failed' }
    const problems = []
    if (got.settled !== f.want.settled) problems.push(`settled=${got.settled} 期望 ${f.want.settled}`)
    if (f.want.state && got.state !== f.want.state) problems.push(`state="${got.state}" 期望 "${f.want.state}"`)
    results.push({ name, want: f.want, got, problems })
  }
  cdp.close(); cleanup()

  console.log('')
  let fail = 0
  for (const r of results) {
    const ok = r.problems.length === 0
    if (!ok) fail++
    console.log(`  ${ok ? '✓' : '✗'} ${r.name.padEnd(12)} settled=${String(r.got.settled).padEnd(5)} state="${r.got.state}" waited=${r.got.waitedMs}ms${ok ? '' : '   ← ' + r.problems.join('; ')}`)
  }
  console.log(`\n▸ ${results.length} 个前提 / ${results.length - fail} 通过 / ${fail} 失败`)
  process.exit(fail ? 1 : 0)
}

main().catch((e) => { console.error(e); process.exit(2) })
