#!/usr/bin/env node
/**
 * mobile-audit.mjs —— 模拟器上的**逐页显示取证**。
 *
 * 为什么用 CDP 而不是截图肉眼扫：
 *   截图只能证明「页面长这样」，证明不了「哪一行 CSS 超了视口 3px」。
 *   而显示问题里最贵的一类恰恰是**肉眼很难发现、但一遇就毁版式**的：
 *   横向溢出、点不到的小热区、与背景同色的文字、被固定栏盖住的内容。
 *   这些都能在页面里量出来 ⇒ 用 Runtime.evaluate 取数，用 Page.captureScreenshot 存档。
 *
 * 驱动方式：adb forward 到 WebView 的 devtools socket，再用 Chrome DevTools 协议。
 *   - 不用 uiautomator：WebView 内的 DOM 默认不进无障碍树，dump 出来只有一个空节点。
 *   - 不用 adb input 盲点：坐标点击无法复现，也无法判定「点到没有」。
 *
 * 用法：
 *   node scripts/mobile-audit.mjs --serial emulator-5556
 *   node scripts/mobile-audit.mjs --serial emulator-5556 --theme both
 *   node scripts/mobile-audit.mjs --serial emulator-5556 --routes /,/nodes --out /tmp/audit-x
 *
 * 退出码：0 = 跑完（**不代表无问题**，看报告）；2 = 环境/连接失败。
 */
import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, rmSync, existsSync } from 'node:fs'
import { setTimeout as sleep } from 'node:timers/promises'
import path from 'node:path'

// ───────────────────────────── 参数 ─────────────────────────────
const argv = process.argv.slice(2)
const arg = (name, dflt) => {
  const i = argv.indexOf('--' + name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : dflt
}
const has = (name) => argv.includes('--' + name)

const SERIAL = arg('serial', 'emulator-5556')
const ORIGIN = arg('origin', 'https://llmgateway.internal.example.com')
const BASE = arg('base', '/m')
const OUT = arg('out', path.resolve('reports/mobile-audit'))
const THEME = arg('theme', 'dark') // light | dark | both
const USER = arg('user', 'admin')
const PASS = arg('pass', '')
const ROUTES = arg('routes', '/login,/,/nodes,/models,/keys,/alerts,/usage')
  .split(',').map((s) => s.trim()).filter(Boolean)
const KEEP = has('keep')
const SETTLE_MS = Number(arg('settle', 2600))

// ───────────────────────────── adb ─────────────────────────────
function adb(...args) {
  return execFileSync('adb', ['-s', SERIAL, ...args], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  }).replace(/\r/g, '')
}
function adbTry(...args) {
  try { return adb(...args) } catch { return '' }
}
const sh = (s) => execFileSync('bash', ['-lc', s], { encoding: 'utf8' }).replace(/\r/g, '')

function fail(msg) {
  console.error('✗ ' + msg)
  process.exit(2)
}

// ───────────────────── WebView devtools 连接 ─────────────────────
const FWD_PORT = 9333

async function webviewSocket() {
  // 只认本 App 的 WebView：按包名过滤，避免连到别的 app / 系统 WebView
  // ⚠️ pidof 必须**在设备里**跑（adb shell），宿主的 pidof 查的是宿主进程。
  const pids = adbTry('shell', 'pidof', 'com.kaixuan.llmgw').trim().split(/\s+/).filter(Boolean)
  if (!pids.length || !pids[0]) fail('com.kaixuan.llmgw 未运行（先 adb -s <serial> shell am start -n com.kaixuan.llmgw/.MainActivity）')
  for (const pid of pids) {
    const s = `webview_devtools_remote_${pid}`
    // 探测 socket 是否真的在监听：建连成功才算数（存在文件 ≠ 有服务）
    adbTry('forward', `--remove`, `tcp:${FWD_PORT}`)
    adb('forward', `tcp:${FWD_PORT}`, `localabstract:${s}`)
    try {
      const r = await fetch(`http://127.0.0.1:${FWD_PORT}/json/version`)
      if (r.ok) return { pid, socket: s }
    } catch { /* 换下一个 pid */ }
  }
  fail(`找不到可用的 WebView devtools socket（pid=${pids.join(',')}）——WebView 调试需要 debuggable 构建`)
}

async function pageTarget() {
  const list = await (await fetch(`http://127.0.0.1:${FWD_PORT}/json/list`)).json()
  const pages = list.filter((t) => t.type === 'page' && t.webSocketDebuggerUrl)
  if (!pages.length) fail('devtools 无 page target（WebView 可能还没起任何页面）')
  // 优先业务 origin 的那个；引导页（http://localhost）只在它还没交接时存在
  const biz = pages.filter((p) => p.url.startsWith(ORIGIN))
  if (biz.length) return biz[0]
  // ⚠️ 2026-10-04：此前这里是 `biz[0] || pages[0]`。设备上可能还有**别的应用的
  // WebView**（实测克隆 AVD 里有一个 192.168.31.34:4199 的仪表盘应用），
  // fallback 会静默接管到它，于是「登录失败」「页面无数据」全是**量错了对象**，
  // 而报告看上去一切正常。⇒ 宁可失败，也不审计别人的页面。
  const others = pages.map((p) => p.url).join(', ')
  fail(`devtools 里没有 ${ORIGIN} 的 page target（现有：${others}）。\n` +
       `  设备上可能有其它应用的 WebView；本工具只审计本 App 的业务页，不做 fallback。`)
}

// ───────────────────── 极简 CDP 客户端 ─────────────────────
class CDP {
  constructor(ws) { this.ws = ws; this.id = 0; this.pending = new Map(); this.handlers = [] }
  static async connect(wsUrl) {
    const ws = new WebSocket(wsUrl)
    await new Promise((res, rej) => {
      ws.addEventListener('open', res, { once: true })
      ws.addEventListener('error', () => rej(new Error('WebSocket 连接失败')), { once: true })
    })
    const c = new CDP(ws)
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data)
      if (msg.id && c.pending.has(msg.id)) {
        const { res, rej } = c.pending.get(msg.id)
        c.pending.delete(msg.id)
        msg.error ? rej(new Error(msg.error.message)) : res(msg.result)
      } else if (msg.method) {
        c.handlers.forEach((h) => h(msg))
      }
    })
    return c
  }
  send(method, params = {}) {
    const id = ++this.id
    return new Promise((res, rej) => {
      this.pending.set(id, { res, rej })
      this.ws.send(JSON.stringify({ id, method, params }))
      setTimeout(() => {
        if (this.pending.has(id)) { this.pending.delete(id); rej(new Error(method + ' 超时')) }
      }, 30000)
    })
  }
  on(fn) { this.handlers.push(fn) }
  close() { try { this.ws.close() } catch { /* noop */ } }
}

// ───────────────────── 页面内审计脚本 ─────────────────────
// 检测器本体在 _layout-audit.mjs（与 Chrome 分诊器共用同一份，见那里的单源理由）。
// 这里只做「注入」：函数被 .toString() 后送进页面执行，所以不能带 ESM 语法。
import { layoutAudit } from './_layout-audit.mjs'
import { waitForSettle } from './_layout-audit.mjs'
const AUDIT_SRC = layoutAudit.toString()
const SETTLE_SRC = waitForSettle.toString()

// ───────────────────── 登录 ─────────────────────
// 驱动实现在 _login-driver.mjs（与 Chrome 分诊共用，见那里的「为什么必须单源」）。
import { driveLogin } from './_login-driver.mjs'

async function login(cdp) {
  const evalJs = async (expression, wantValue = false) => {
    const r = await cdp.send('Runtime.evaluate', { returnByValue: wantValue, expression })
    return wantValue ? r.result.value : true
  }
  const res = await driveLogin(cdp, {
    origin: ORIGIN, base: BASE, user: USER, pass: PASS,
    nav: async (url, ms) => { await cdp.send('Page.navigate', { url }); await sleep(ms) },
    evalJs,
  })
  console.log((res.ok ? '✓' : '✗') + ' 登录后路由 = ' + res.route + (res.why ? '  —— ' + res.why : ''))
  return res.ok
}

// ───────────────────── 主流程 ─────────────────────
async function main() {
  if (!PASS) fail('需要 --pass <密码>（不落盘、不进 git）')
  console.log('▸ 目标设备 ' + SERIAL + '  origin ' + ORIGIN + BASE)
  const { pid, socket } = webviewSocket()
  console.log('▸ WebView devtools ' + socket + ' (pid ' + pid + ') → 127.0.0.1:' + FWD_PORT)

  let target
  for (let i = 0; i < 20; i++) {
    try { target = await pageTarget(); break } catch (e) { await sleep(1000) }
  }
  if (!target) fail('devtools 无 page target')
  console.log('▸ 接管页面 ' + target.url)

  const cdp = await CDP.connect(target.webSocketDebuggerUrl)
  const consoleErrors = []
  cdp.on((m) => {
    if (m.method === 'Runtime.exceptionThrown') {
      consoleErrors.push({ route: current, type: 'exception',
        text: m.params.exceptionDetails?.exception?.description || m.params.exceptionDetails?.text })
    } else if (m.method === 'Runtime.consoleAPICalled' && (m.params.type === 'error' || m.params.type === 'warning')) {
      consoleErrors.push({ route: current, type: m.params.type,
        text: (m.params.args || []).map((a) => a.value ?? a.description ?? a.type).join(' ').slice(0, 300) })
    } else if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') {
      consoleErrors.push({ route: current, type: 'log', text: String(m.params.entry.text).slice(0, 300) + ' @ ' + (m.params.entry.url || '') })
    }
  })
  let current = '(boot)'

  await cdp.send('Page.enable'); await cdp.send('Runtime.enable')
  await cdp.send('Log.enable'); await cdp.send('Network.enable')
  // 移动端：务必关掉 desktop 视口，否则页面拿到 980px 布局宽度，一切「移动端问题」都会消失
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: 0, height: 0, deviceScaleFactor: 0, mobile: false,
  }).catch(() => {})

  if (!existsSync(OUT)) mkdirSync(OUT, { recursive: true })
  const shots = path.join(OUT, 'shots')
  if (!KEEP && existsSync(shots)) rmSync(shots, { recursive: true, force: true })
  mkdirSync(shots, { recursive: true })

  // 登录（/login 页也要取证，所以先登录再回头截它）
  const loggedIn = await login(cdp)

  const themes = THEME === 'both' ? ['light', 'dark'] : [THEME]
  const report = { startedAt: new Date().toISOString(), serial: SERIAL, origin: ORIGIN + BASE,
    emulator: {}, loggedIn, themes, pages: [] }

  // 模拟器形态：设备显示问题的判据之一（截图要与这些一致）
  const emu = {}
  for (const [k, c] of Object.entries({
    sdk: 'ro.build.version.sdk', release: 'ro.build.version.release',
    model: 'ro.product.model', size: 'ro.boot.hardware.size',
  })) emu[k] = (adbTry('shell', 'getprop', c) || '').trim()
  emu.screen = (adbTry('shell', 'wm', 'size') || '').trim()
  emu.density = (adbTry('shell', 'wm', 'density') || '').trim()
  report.emulator = emu
  console.log('▸ 模拟器 ' + emu.model + ' API ' + emu.sdk + ' ' + emu.screen + ' ' + emu.density)

  for (const theme of themes) {
    await cdp.send('Runtime.evaluate', {
      expression: `try{localStorage.setItem('llmgw_mobile_theme',${JSON.stringify(theme)});localStorage.setItem('llmgw_theme',${JSON.stringify(theme)})}catch(e){}
                   document.documentElement.classList.toggle('dark', ${theme === 'dark'})`,
    })
    for (const route of ROUTES) {
      current = route
      const url = ORIGIN + BASE + route
      const t0 = Date.now()
      await cdp.send('Page.navigate', { url })
      await sleep(SETTLE_MS)
      // 稳定等待：必须等到骨架屏撤掉 / 终态视图出现再采样。
      // 否则像 /nodes（服务端 15s 查询超时）会在 t+4s 被拍成「白屏」，
      // 而它其实在 t+18s 会正常进错误态 —— 那是假阳性，见 _layout-audit.mjs 注释。
      const settle = await cdp.send('Runtime.evaluate', {
        returnByValue: true, awaitPromise: true,
        expression: `(${SETTLE_SRC})(26000)`,
      }).catch(() => null)
      const settleInfo = settle?.result?.value || { settled: false, state: 'probe-failed' }

      const audit = await cdp.send('Runtime.evaluate', {
        returnByValue: true, awaitPromise: false, expression: `(${AUDIT_SRC})()`,
      })
      const a = audit.result.value || { issues: [], error: audit.exceptionDetails?.text }

      const shot = await cdp.send('Page.captureScreenshot', { format: 'png' }).catch(() => null)
      const file = path.join(shots, `${theme}${route.replace(/\//g, '_') || '_root'}.png`)
      if (shot?.data) writeFileSync(file, Buffer.from(shot.data, 'base64'))

      const errs = consoleErrors.filter((e) => e.route === route)
      const page = { theme, route, url, ms: Date.now() - t0, screenshot: shot?.data ? file : null,
        settle: settleInfo, audit: a, consoleErrors: errs }
      report.pages.push(page)

      const bad = a.issues?.length || 0
      const cerr = errs.length
      console.log(`  ${bad || cerr ? '✗' : '✓'} [${theme}] ${route.padEnd(8)} ${String(bad).padStart(2)} 显示问题  ${cerr} 控制台  settle=${settleInfo.state}@${Math.round((settleInfo.waitedMs||0)/1000)}s  共${page.ms}ms`)
      for (const i of (a.issues || [])) console.log(`      · ${i.sev} ${i.kind}: ${i.detail}`)
      for (const e of errs.slice(0, 3)) console.log(`      · console.${e.type}: ${e.text}`)
    }
  }

  cdp.close()
  adbTry('forward', '--remove', `tcp:${FWD_PORT}`)

  report.finishedAt = new Date().toISOString()
  writeFileSync(path.join(OUT, 'report.json'), JSON.stringify(report, null, 2))
  const total = report.pages.reduce((n, p) => n + (p.audit?.issues?.length || 0) + p.consoleErrors.length, 0)
  report.summary = { pages: report.pages.length, issues: total }
  writeFileSync(path.join(OUT, 'report.json'), JSON.stringify(report, null, 2))
  console.log(`\n▸ 报告 ${path.join(OUT, 'report.json')}`)
  console.log(`▸ 截图 ${shots}`)
  console.log(`▸ 合计 ${report.pages.length} 个页面态 / ${total} 个问题（显示 + 控制台）`)
  if (!loggedIn) console.log('⚠ 登录未成功，鉴权页之后的页面可能只是登录重定向 —— 报告里逐页看 url 字段')
}

main().catch((e) => { console.error('✗ ' + (e.stack || e.message)); process.exit(2) })
