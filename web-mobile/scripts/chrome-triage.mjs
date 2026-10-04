#!/usr/bin/env node
/**
 * chrome-triage.mjs —— 用本地 Chrome（移动视口）对线上 /m/ 做**快速分诊**。
 *
 * ⚠️ 它**不是**验收工具。定位：
 *   · mobile-audit.mjs（模拟器 WebView）= 权威取证，交付证据用它；
 *   · 本脚本 = 分诊，车开得快，用来在等模拟器冷启动时先把显示问题找出来。
 *
 * ⚠️ 两者的差异必须写清楚，否则会拿分诊结果冒充验收结论：
 *   | 维度 | 本脚本 | 模拟器 |
 *   |---|---|---|
 *   | 视口 | 固定 393×852 @3x | 真机 1080×2400 @420dpi |
 *   | safe-area | env() 恒为 0（无刘海） | 取决于 SystemBars 注入 |
 *   | 桥 | 无 window.Capacitor | 同（缺陷 ②，远程 origin 不注入） |
 *   ⇒ **safe-area 相关的判定在分诊里恒为「无问题」，那不是通过，是没测到。**
 *     凡是 kind=safe-area 的结论，只能由模拟器那一轮给。
 *
 * 检测器与模拟器共用 scripts/_layout-audit.mjs（单源，见该文件头）。
 *
 * 用法：node scripts/chrome-triage.mjs --pass <密码> [--theme both] [--out <dir>]
 */
import { spawn, execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, existsSync, rmSync } from 'node:fs'
import { setTimeout as sleep } from 'node:timers/promises'
import os from 'node:os'
import path from 'node:path'
import { layoutAudit } from './_layout-audit.mjs'
import { waitForSettle } from './_layout-audit.mjs'
import { driveLogin } from './_login-driver.mjs'

const argv = process.argv.slice(2)
const arg = (n, d) => { const i = argv.indexOf('--' + n); return i >= 0 && argv[i + 1] ? argv[i + 1] : d }

const ORIGIN = arg('origin', 'https://llmgateway.internal.example.com')
const BASE = arg('base', '/m')
const OUT = arg('out', path.resolve('reports/chrome-triage'))
const THEME = arg('theme', 'both')
const USER = arg('user', 'admin')
const PASS = arg('pass', '')
const ROUTES = arg('routes', '/login,/,/nodes,/models,/keys,/alerts,/usage')
  .split(',').map((s) => s.trim()).filter(Boolean)
// Pixel 7 CSS 视口。用它是因为它是当前 UI 规范 15 的验收机型之一。
const VW = Number(arg('vw', 393))
const VH = Number(arg('vh', 852))
const DPR = Number(arg('dpr', 3))
const PORT = Number(arg('port', 9444))

if (!PASS) { console.error('✗ 需要 --pass <密码>'); process.exit(2) }

const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const PROFILE = path.join(os.tmpdir(), 'llmgw-triage-profile')
if (existsSync(PROFILE)) rmSync(PROFILE, { recursive: true, force: true })

class CDP {
  constructor(ws) { this.ws = ws; this.id = 0; this.pending = new Map(); this.handlers = [] }
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
      } else if (m.method) c.handlers.forEach((h) => h(m))
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
  on(f) { this.handlers.push(f) }
  close() { try { this.ws.close() } catch { /* noop */ } }
}

const AUDIT_SRC = layoutAudit.toString()
const SETTLE_SRC = waitForSettle.toString()

async function main() {
  console.log(`▸ 启动 Chrome（headless, ${VW}×${VH} @${DPR}x, 移动 UA）`)
  const chrome = spawn(CHROME, [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${PROFILE}`,
    `--window-size=${VW},${VH}`,
    '--hide-scrollbars',
    '--no-first-run', '--no-default-browser-check',
    '--disable-background-timer-throttling',
    '--disable-renderer-backgrounding',
    '--force-device-scale-factor=1',
    'about:blank',
  ], { stdio: 'ignore' })

  const cleanup = () => { try { chrome.kill('SIGKILL') } catch { /* noop */ } }
  process.on('exit', cleanup)

  // 等 devtools 端口就绪
  let ver = null
  for (let i = 0; i < 40; i++) {
    try { ver = await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json(); break } catch { await sleep(500) }
  }
  if (!ver) { console.error('✗ Chrome devtools 端口未就绪'); process.exit(2) }
  console.log('▸ ' + ver.Browser)

  const t = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json()
  const cdp = await CDP.connect(t.webSocketDebuggerUrl)

  await cdp.send('Page.enable'); await cdp.send('Runtime.enable')
  await cdp.send('Log.enable'); await cdp.send('Network.enable')
  // ★ 关键：没有这一行，页面拿到的是桌面视口，所有移动端缺陷都不会出现。
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: VW, height: VH, deviceScaleFactor: DPR, mobile: true,
  })
  await cdp.send('Emulation.setUserAgentOverride', {
    userAgent: 'Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36',
    platform: 'Linux armv8l',
  })

  const consoleErrors = []
  let current = '(boot)'
  cdp.on((m) => {
    if (m.method === 'Runtime.exceptionThrown') {
      consoleErrors.push({ route: current, type: 'exception',
        text: m.params.exceptionDetails?.exception?.description || m.params.exceptionDetails?.text })
    } else if (m.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(m.params.type)) {
      consoleErrors.push({ route: current, type: m.params.type,
        text: (m.params.args || []).map((a) => a.value ?? a.description ?? a.type).join(' ').slice(0, 300) })
    } else if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') {
      consoleErrors.push({ route: current, type: 'log',
        text: String(m.params.entry.text).slice(0, 300) + ' @ ' + (m.params.entry.url || '') })
    }
  })

  const nav = async (url, wait = 3000) => {
    await cdp.send('Page.navigate', { url })
    await sleep(wait)
  }

  // ── 登录（与模拟器取证共用同一个驱动，见 _login-driver.mjs）────
  const evalJs = async (expression, wantValue = false) => {
    const r = await cdp.send('Runtime.evaluate', { returnByValue: wantValue, expression })
    return wantValue ? r.result.value : true
  }
  const loginRes = await driveLogin(cdp, { origin: ORIGIN, base: BASE, user: USER, pass: PASS, nav, evalJs })
  const loggedIn = loginRes.ok
  console.log((loggedIn ? '✓' : '✗') + ' 登录后路由 = ' + loginRes.route + (loginRes.why ? '  —— ' + loginRes.why : ''))

  if (!existsSync(OUT)) mkdirSync(OUT, { recursive: true })
  const shots = path.join(OUT, 'shots')
  if (existsSync(shots)) rmSync(shots, { recursive: true, force: true })
  mkdirSync(shots, { recursive: true })

  const report = {
    vehicle: 'chrome-headless-mobile-viewport',
    disclaimer: '分诊用，非验收证据；safe-area 类结论必须由模拟器那一轮给',
    viewport: { VW, VH, DPR }, origin: ORIGIN + BASE, loggedIn, pages: [],
  }

  for (const theme of (THEME === 'both' ? ['light', 'dark'] : [THEME])) {
    await cdp.send('Runtime.evaluate', {
      expression: `try{localStorage.setItem('llmgw_mobile_theme',${JSON.stringify(theme)})}catch(e){}
                   document.documentElement.classList.toggle('dark', ${theme === 'dark'})`,
    })
    for (const route of ROUTES) {
      current = route
      await nav(ORIGIN + BASE + route, 3000)
      // 稳定等待后再采样（理由同 _layout-audit.mjs：/nodes 要 15s 才出错误态）
      const settle = await cdp.send('Runtime.evaluate', {
        returnByValue: true, awaitPromise: true, expression: `(${SETTLE_SRC})(26000)`,
      }).catch(() => null)
      const settleInfo = settle?.result?.value || { settled: false, state: 'probe-failed' }
      const a = await cdp.send('Runtime.evaluate', { returnByValue: true, expression: `(${AUDIT_SRC})()` })
      const audit = a.result.value || { issues: [], error: a.exceptionDetails?.text }
      const shot = await cdp.send('Page.captureScreenshot', { format: 'png' }).catch(() => null)
      const file = path.join(shots, `${theme}${route.replace(/\//g, '_') || '_root'}.png`)
      if (shot?.data) writeFileSync(file, Buffer.from(shot.data, 'base64'))
      const errs = consoleErrors.filter((e) => e.route === route)
      report.pages.push({ theme, route, url: ORIGIN + BASE + route, screenshot: file, settle: settleInfo, audit, consoleErrors: errs })

      const bad = audit.issues?.length || 0
      console.log(`  ${bad || errs.length ? '✗' : '✓'} [${theme}] ${route.padEnd(8)} ${String(bad).padStart(2)} 显示问题  ${errs.length} 控制台  settle=${settleInfo.state}@${Math.round((settleInfo.waitedMs||0)/1000)}s`)
      for (const i of (audit.issues || [])) {
        console.log(`      · ${i.sev} ${i.kind}: ${i.detail}`)
        for (const o of (i.offenders || i.items || []).slice(0, 4)) {
          console.log(`          ${o.sel || o.a || ''} ${o.w ? o.w + 'x' + o.h : ''} ${o.ratio ? 'ratio=' + o.ratio : ''} ${o.text ? '"' + o.text + '"' : ''}`.trim())
        }
      }
      for (const e of errs.slice(0, 3)) console.log(`      · console.${e.type}: ${e.text}`)
    }
  }

  cdp.close(); cleanup()
  const total = report.pages.reduce((n, p) => n + (p.audit?.issues?.length || 0) + p.consoleErrors.length, 0)
  report.summary = { pages: report.pages.length, issues: total }
  writeFileSync(path.join(OUT, 'report.json'), JSON.stringify(report, null, 2))
  console.log(`\n▸ 报告 ${path.join(OUT, 'report.json')}`)
  console.log(`▸ ${report.pages.length} 个页面态 / ${total} 个问题`)
}

main().catch((e) => { console.error('✗ ' + (e.stack || e.message)); process.exit(2) })
