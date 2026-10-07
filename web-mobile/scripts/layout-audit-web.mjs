#!/usr/bin/env node
/**
 * layout-audit-web.mjs —— 把 `_layout-audit.mjs` 那把判据，跑到**真实线上页面**上。
 *
 * 为什么需要它（这不是重复造轮子）：
 *   · `mobile-audit.mjs` / `interaction-audit.mjs` 都绑 `--serial`，必须占一台安卓设备；
 *     设备被占时，「不同屏幕适配」这一维就完全量不到。
 *   · 本脚本零设备依赖：真 Chrome + CDP + 视口回读，能在**提交后立刻**对
 *     「已上线构建」和「本地构建」各量一遍。判据本身**一行不改**，直接从
 *     `_layout-audit.mjs` import 后 `toString()` 注入，绝不复制第二份。
 *
 * ★ 三条自保（每条都对应一次真实假绿，缺一条就会把「没量到」读成「没问题」）：
 *
 *   1. **视口回读**：每组 `setDeviceMetricsOverride` 之后实测 `innerWidth/innerHeight`，
 *      不等于请求值就标 `invalid` 并**剔出分母**。
 *      `Emulation` 的失败是静默的，不等就绿 ⇒ 会变成「同一宽度量 9 遍报成 9 档都通过」。
 *      稳定生效的两个条件：每组 set 前先 `clearDeviceMetricsOverride`（否则沿用上一档）、
 *      `mobile:true` + `deviceScaleFactor` 一起给。
 *
 *   2. **内容有效性不能只看文字长度**：`#main-content` 文本 >20 字时，**错误页同样满足**。
 *      这里额外做错误特征匹配（抓正文首段 + 命中错误文案则判为 error-page），
 *      否则「48 组有内容」可能全是「48 个一样长的错误页」。
 *
 *   3. **先 waitForSettle 再采样**：`_layout-audit.mjs` 自己的注释记着一次真实假阳性 ——
 *      导航后 ~4s 抓的那张「整页骨架屏」是**本来就该**的骨架屏，
 *      差一个观察窗就是「永久骨架屏」这个完全不同的结论。
 *
 * 用法：
 *   LLM_GATEWAY_ADMIN_PASSWORD=… node scripts/layout-audit-web.mjs \
 *     --origin https://llmgateway.internal.example.com --tag prod
 *   node scripts/layout-audit-web.mjs --tag local --origin http://127.0.0.1:8782
 *
 * 退出码：0 = 跑完（**不代表无问题**，看报告）；2 = 环境/连接失败。
 * ⚠️ 口令只从环境取：不接受 argv（会进 ps 与 shell history），不写盘、不回显。
 */
import { spawn } from 'node:child_process'
import { mkdirSync, writeFileSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { layoutAudit, waitForSettle } from './_layout-audit.mjs'

// ───────────────────────────── 参数 ─────────────────────────────
const argv = process.argv.slice(2)
const arg = (n, d) => { const i = argv.indexOf('--' + n); return i >= 0 && argv[i + 1] ? argv[i + 1] : d }
const has = (n) => argv.includes('--' + n)

const ORIGIN = arg('origin', 'https://llmgateway.internal.example.com')
const TAG = arg('tag', 'run')
const USER = arg('user', 'admin')
const BASE = arg('base', '/m')
const OUT = arg('out', '/tmp')
const CHROME = process.env.CHROME_BIN || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const PORT = 9600 + Math.floor(Math.random() * 500)

const ROUTES = arg('routes', '/,/models,/keys,/nodes,/usage,/alerts')
  .split(',').filter(Boolean).map((r) => BASE + r)

// 宽×高。横屏三档是**形状**变化（矮视口下吸底栏与内容区争用关系不同），不是只加宽。
// ⚠️ 视口集合与 `docs/UI规范/15-真机验收与落地路线.md` §2.1 那张表**必须一致**，
//   两者由 `src/composables/viewport-matrix.spec.ts` 双向对账（少一档即红）。
//   568×320 是唯一「compact + 横屏 + 吸底栏」的档：横握手机的 CSS 宽度多在
//   568~932，而移动端 medium 从 600 起 ⇒ 少了它就量不到吸底栏（10 §4.6.61）。
const SIZES = arg('sizes', '320x800,360x800,390x844,412x915,568x320,600x800,740x360,840x673,768x1024,914x411,1024x600,1024x768,1194x834,1280x800,1440x900')
  .split(',').map((s) => { const [w, h] = s.split('x').map(Number); return { w, h } })

const SETTLE_MS = Number(arg('settle', '25000'))
// 横向 safe-area 注入（2026-10-08）：`--insets 30,30[,top,bottom]`
// 为什么需要它：headless 的 env(safe-area-inset-*) 恒 0，而**视口宽高变化改变不了
// inset 带的位置** ⇒ 横屏手势/刘海这一维在 12 档视口审计里结构性量不到。
// 注入的是壳真正写的那两个自定义属性（Capacitor SystemBars.injectSafeAreaCSS，
// 名字与写入位置都在 theme.css 注释里），只有**数值**是合成的；判据对数值无感。
// ⚠️ 注入后必须**回读** `--app-safe-*`：读不回来就把这组标作废，
//   否则「注不进去」和「量出来没问题」在报告里长得一模一样。
const INSET_RAW = arg('insets', '')
const INSETS = INSET_RAW ? INSET_RAW.split(',').map(Number) : null
// `--overlay`：每组额外采一次「抽屉打开」状态。抽屉是 `Teleport to="body"` 的
// fixed 浮层，默认永远关着 ⇒ 不专门驱动就量不到它。
const OVERLAY = has('overlay')
const INJECT_INSETS = INSETS ? `(() => { const s = document.documentElement.style;
  s.setProperty('--safe-area-inset-left', '${INSETS[0]}px');
  s.setProperty('--safe-area-inset-right', '${INSETS[1] ?? INSETS[0]}px');
  s.setProperty('--safe-area-inset-top', '${INSETS[2] ?? 0}px');
  s.setProperty('--safe-area-inset-bottom', '${INSETS[3] ?? 0}px');
  return true })()` : null
const READ_INSETS = `(() => { const cs = getComputedStyle(document.documentElement); const o = {};
  for (const k of ['left','right','top','bottom']) o[k] = (cs.getPropertyValue('--app-safe-' + k) || '').trim();
  return o })()`
// 打开浮层：**只点起始侧的按钮**（`topbar__side` 非 end 侧 = 菜单/返回），
// 点了等 700ms 再由调用方**观察**到底打开了哪个 ——
// ⚠️ 第一版在同一个表达式里「点完立刻查 `.drawer`」，而 Vue 的 DOM 更新是异步的
//   （nextTick 微任务）⇒ 同步查**永远**看不到抽屉，返回值恒为 null；
//   更糟的是那个循环会把所有 topbar 按钮**都点一遍**（菜单 + 账户），
//   于是抽屉与账户 Sheet 同时打开，凭空造出一对 tap-overlap。
const OPEN_OVERLAY = `(() => {
  const b = document.querySelector('.topbar__side:not(.topbar__side--end) .topbar__btn');
  if (!b) return null; b.click(); return true })()`
const WHICH_OVERLAY = `(() => {
  if (document.querySelector('.drawer')) return 'drawer';
  if (document.querySelector('.app-sheet, .focus-layer')) return 'sheet';
  return null })()`
const CLOSE_OVERLAY = `(() => { const s = document.querySelector('.drawer__scrim');
  if (s) s.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
  return true })()`
const PASS = process.env.LLM_GATEWAY_ADMIN_PASSWORD
if (!PASS) { console.error('环境里没有 LLM_GATEWAY_ADMIN_PASSWORD（不要用 argv 传口令）'); process.exit(2) }

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const AUDIT_SRC = layoutAudit.toString()
const SETTLE_SRC = waitForSettle.toString()
const host = new URL(ORIGIN).hostname

// ───────────────────────── 登录（口令全程不落盘） ─────────────────────────
const lr = await fetch(`${ORIGIN}/api/auth/token`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: USER, password: PASS }),
})
if (!lr.ok) { console.error(`登录失败 http=${lr.status}`); process.exit(2) }
const raw = (lr.headers.getSetCookie?.() ?? []).find((c) => c.startsWith('llmgw_session='))
if (!raw) { console.error('登录成功但没拿到 llmgw_session'); process.exit(2) }
const cookie = raw.split(';')[0]
const me = await (await fetch(`${ORIGIN}/api/auth/me`, { headers: { cookie } })).json()
console.error(`登录: user=${(me.user ?? me).username} role=${(me.user ?? me).role} origin=${ORIGIN} tag=${TAG}`)

// ───────────────────────── 起 Chrome ─────────────────────────
const profile = mkdtempSync(join(tmpdir(), 'law-'))
const chrome = spawn(CHROME, [
  '--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-features=Translate,MediaRouter', `--user-data-dir=${profile}`,
  `--remote-debugging-port=${PORT}`, 'about:blank',
], { stdio: ['ignore', 'ignore', 'pipe'] })
let cerr = ''
chrome.stderr?.on('data', (b) => { cerr += b.toString() })
const stop = () => { try { chrome.kill('SIGKILL') } catch {} }
process.on('exit', stop)

let pageWs = null
for (let i = 0; i < 60; i++) {
  try {
    const l = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json()
    const p = l.find((t) => t.type === 'page')
    if (p) { pageWs = p.webSocketDebuggerUrl; break }
  } catch {}
  await sleep(250)
}
if (!pageWs) { console.error('Chrome 调试口没起来\n' + cerr.slice(0, 1200)); stop(); process.exit(2) }

const ws = new WebSocket(pageWs)
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })
let id = 0
const pend = new Map()
ws.onmessage = (e) => {
  const m = JSON.parse(e.data)
  if (m.id && pend.has(m.id)) {
    const { resolve, reject } = pend.get(m.id); pend.delete(m.id)
    m.error ? reject(new Error(JSON.stringify(m.error))) : resolve(m.result)
  }
}
const send = (method, params = {}) => new Promise((res, rej) => {
  const n = ++id; pend.set(n, { resolve: res, reject: rej })
  ws.send(JSON.stringify({ id: n, method, params }))
})

const evaluate = async (expression) => {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.text || '页面内异常')
  return r.result?.value
}

await send('Page.enable'); await send('Runtime.enable'); await send('Network.enable')

// ── 主题开关（2026-10-08，§4.6.64）──────────────────────────────
// 为什么需要：暗色是 `html.dark` **类**驱动（`stores/theme.ts`，键 `llmgw_mobile_theme`），
// 不是 media query；而判据此前只在**浅色**下量过 ⇒ 暗色对比度从来没被量过。
// 而实测已出现过「浅色达标、暗色根本不变色」的退化（4 个硬编码色）。
// ⚠️ 用 `addScriptToEvaluateOnNewDocument` 在**应用初始化前**写 localStorage，
//   而不是加载后加 class：后者会与 store 的初始化赛跑，且量到的是「被改过」的页面。
const THEME = arg('theme', 'light')
const THEME_KEY = 'llmgw_mobile_theme'
const DARK_BG = '#0f141c'
if (THEME === 'dark') {
  await send('Page.addScriptToEvaluateOnNewDocument', {
    source: `try { localStorage.setItem(${JSON.stringify(THEME_KEY)}, 'dark') } catch (e) {}`,
  })
}
// 量具自证：暗色必须真的生效（class 在 + --app-bg 变成暗色值），否则「暗色全绿」
// 只会说明**暗色没开** —— 与「没量到」在报告里长得一模一样。
const READ_THEME = `(() => { const cs = getComputedStyle(document.documentElement); return {
  dark: document.documentElement.classList.contains('dark'),
  bg: (cs.getPropertyValue('--app-bg') || '').trim().toLowerCase() } })()`

// ── 根字号缩放（2026-10-08，§4.6.65）────────────────────────────
// 为什么需要：安卓「显示大小 / 字体大小」与 iOS 动态字体是**最常见**的真机差异，
// 而本仓此前**零覆盖** —— `hyper/scroll/dock.ts:3` 的注释写着
// 「旋转/分屏/字号走 ResizeObserver + visualViewport」，但那只保证吸顶偏移跟着变，
// **不保证版式扛得住**。
// 为什么改根字号是**忠实**的模拟：本仓版式以 `rem` 为主（`0.8125rem` / `0.75rem` …，
// 根字号未被锁定），改 `html{font-size}` 与平台字号放大在 rem 布局上等价。
// ⚠️ 同样必须**回读自证**：不读回就会把「没放大」当成「放大后没问题」。
const FONT_SCALE = Number(arg('font-scale', '1'))
const BASE_FONT_PX = 16
const READ_FONT = `(() => getComputedStyle(document.documentElement).fontSize)()`

// 预热：首导航要拉 bundle + 首次解析，settle 容易在挂载前就返回。
// 不预热的话**第一组**会被自己的量具判成 no-content（实测踩到：/m @320 作废、
// 同一路由 @914 却有 271 字）——那是启动时序，不是页面没内容。
await send('Network.setCookie', { name: 'llmgw_session', value: cookie.split('=')[1], domain: host, path: '/', httpOnly: true, secure: ORIGIN.startsWith('https') })
await send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true })
await send('Page.navigate', { url: ORIGIN + BASE + '/' })
await sleep(4000)

// 页面内容有效性：**长度不够** 或 **DOM 上确实是错误态** ⇒ 这组不作数。
//
// ⚠️⚠️ 这里换过一次判据，理由必须留着（一次真实的假阴性 36 组）：
//   原实现是「抓正文前 400 字，匹配 `加载失败|Internal Server Error|\b500\b`」。
//   结果 **165 组里 36 组被误判成错误页**，而它们其实渲染得好好的：
//     · `/m/` 命中「加载失败」—— 页面**说明文字里提到**了这个词（在讲错误态怎么显示）；
//     · 6 条路由命中 `\b500\b` —— 那是**普通数字**（「前 500」「显示 500 条」）。
//   ⇒ **全文搜关键词 = 把「页面里出现了这个词」当成「页面就是这个状态」**。
//   正确判据是**结构**：`AppStateView` 的 error 分支渲染 `<AppIcon name="alert">`
//   （一个 `<svg>`）与重试按钮，而 empty 分支**不渲染图标**（AppStateView.vue:33-44）。
//   结构判据不看文案 ⇒ i18n 也顺带不再是干扰。
const PROBE = `(() => {
  const main = document.querySelector('#main-content') || document.body;
  const t = (main?.innerText || '').replace(/\\s+/g,' ').trim();
  const sv = main.querySelector('.state-view');
  const isStateCenter = !!(sv && sv.classList.contains('state-view--center'));
  const hasAlertIcon = isStateCenter && !!sv.querySelector('svg');
  const roleAlert = !!main.querySelector('[role="alert"]');
  return { len: t.length, nodes: main.querySelectorAll('*').length,
           errHit: hasAlertIcon || roleAlert,
           stateView: isStateCenter, hasAlertIcon, roleAlert,
           head: t.slice(0, 160), vw: innerWidth, vh: innerHeight };
})()`

const rows = []
for (const route of ROUTES) {
  for (const { w, h } of SIZES) {
    const row = { route, reqW: w, reqH: h }
    // ① 先清 override，否则 CDP 会沿用上一档
    await send('Emulation.clearDeviceMetricsOverride').catch(() => {})
    await send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 2, mobile: true })
    await send('Network.clearBrowserCookies')
    await send('Network.setCookie', { name: 'llmgw_session', value: cookie.split('=')[1], domain: host, path: '/', httpOnly: true, secure: ORIGIN.startsWith('https') })
    await send('Page.navigate', { url: ORIGIN + route })

    // ③ 等稳定态再采样
    let settle = await evaluate(`(${SETTLE_SRC})(${SETTLE_MS})`).catch((e) => ({ error: String(e) }))

    // ⚠️ 这里**曾经**有一段「stable 之后再多观察一轮」的补偿，理由是怀疑
    // `waitForSettle` 假 settle（只证明「此刻没在变」，不证明「已加载完」）。
    // ⇒ 那条假设被实测**否掉**并已删掉：逐秒采样 /m/usage 16s，
    // 文本 101 / 节点 82 / 卡片 6 从 t=1.1s 起**纹丝不动**，
    // 101÷6≈17 字/卡 = 6 张汇总卡的完整内容。页面本来就是短的。
    // 教训留在文件里：先证「它在动而我看不见」，再补观察窗 —— 顺序反了就是给
    // 一个不存在的问题加每次审计都付的等待成本。

    // ② 内容有效性。no-content 有可能是**真的没内容**，也可能是这一组自己没赶上挂载。
    // 分不清就别算数 —— 但也别立刻判死：重试一次，并把「是否被重试救回」记进读数。
    let probe = await evaluate(PROBE).catch((e) => ({ error: String(e) }))
    let rescued = false
    if (probe && !probe.error && !(probe.len > 20 && probe.nodes > 5)) {
      await sleep(2500)
      const again = await evaluate(PROBE).catch(() => null)
      if (again && again.len > 20 && again.nodes > 5) { probe = again; rescued = true }
    }
    // ① 视口回读
    if (!probe || probe.error || probe.vw !== w || probe.vh !== h) {
      rows.push({ ...row, invalid: 'viewport-mismatch', got: probe?.error ?? `${probe?.vw}x${probe?.vh}` })
      continue
    }
    const hasContent = probe.len > 20 && probe.nodes > 5
    if (!hasContent) { rows.push({ ...row, ...probe, invalid: 'no-content' }); continue }
    if (probe.errHit) { rows.push({ ...row, ...probe, invalid: 'error-page', head: probe.head }); continue }

    let rep = null
    // 注入要在**判据跑之前**：自定义属性一改，下一次读 rect 就会重新布局。
    let ins = null
    let thm = null
    let font = null
    if (FONT_SCALE !== 1) {
      const want = (BASE_FONT_PX * FONT_SCALE).toFixed(2) + 'px'
      const got = await evaluate(
        `(() => { document.documentElement.style.fontSize = ${JSON.stringify(want)};
           return getComputedStyle(document.documentElement).fontSize })()`,
      ).catch((e) => ({ error: String(e) }))
      font = { want, got }
      if (typeof got !== 'string' || Math.abs(parseFloat(got) - parseFloat(want)) > 0.5) {
        rows.push({ ...row, invalid: 'font-scale-not-applied', got: JSON.stringify(font) })
        continue
      }
    }
    if (THEME === 'dark') {
      thm = await evaluate(READ_THEME).catch((e) => ({ error: String(e) }))
      if (!thm || thm.error || !thm.dark || thm.bg !== DARK_BG) {
        rows.push({ ...row, invalid: 'theme-not-applied', got: JSON.stringify(thm) })
        continue
      }
    }
    if (INJECT_INSETS) {
      await evaluate(INJECT_INSETS).catch((e) => ({ error: String(e) }))
      ins = await evaluate(READ_INSETS).catch((e) => ({ error: String(e) }))
      const wantL = `${INSETS[0]}px`
      if (!ins || ins.error || ins.left !== wantL) {
        rows.push({ ...row, invalid: 'inset-not-injected', got: JSON.stringify(ins) })
        continue
      }
    }
    try { rep = await evaluate(`(${AUDIT_SRC})()`) } catch (e) { rep = { error: String(e) } }
    rows.push({ route, reqW: w, reqH: h, vw: probe.vw, vh: probe.vh, textLen: probe.len,
                rescuedByRetry: rescued, settle, insets: ins, theme: THEME, font, report: rep })

    // 浮层状态：抽屉 / 账户 Sheet 默认关着，不驱动就量不到
    if (OVERLAY) {
      const clicked = await evaluate(OPEN_OVERLAY).catch((e) => `err:${e}`)
      await sleep(700)
      // 观察而不是假定：点到的可能是「返回」（showBack 路由），那就当没开成
      const which = await evaluate(WHICH_OVERLAY).catch(() => null)
      if (!which) {
        rows.push({ ...row, invalid: 'overlay-not-opened', got: String(clicked) })
      } else {
        let orep = null
        try { orep = await evaluate(`(${AUDIT_SRC})()`) } catch (e) { orep = { error: String(e) } }
        rows.push({ route, reqW: w, reqH: h, vw: probe.vw, vh: probe.vh, textLen: probe.len,
                    rescuedByRetry: rescued, settle, insets: ins, theme: THEME,
                    state: 'overlay:' + which, report: orep })
        await evaluate(CLOSE_OVERLAY).catch(() => {})
        await sleep(400)
        // 量具自证：关不掉的话下一组的读数已经被浮层盖住了，必须报出来而不是继续
        const closed = await evaluate(`!document.querySelector('.drawer, .app-sheet, .focus-layer')`).catch(() => false)
        if (!closed) rows.push({ ...row, invalid: 'overlay-not-closed' })
      }
    }
  }
}
ws.close(); stop()

// ───────────────────────── 报告 ─────────────────────────
const valid = rows.filter((r) => !r.invalid)
const bad = valid.filter((r) => r.report && (r.report.issues?.length ?? 0) > 0)
const byKind = {}
for (const r of bad) for (const i of r.report.issues) (byKind[i.kind] ??= []).push({ route: r.route, size: `${r.reqW}x${r.reqH}`, sev: i.sev, detail: i.detail })

const pad = (s, n) => String(s ?? '').padEnd(n)
console.log(`\n${pad('route', 12)}${pad('尺寸', 10)}${pad('实测', 10)}${pad('状态', 18)}${pad('文本', 7)}${pad('settle', 16)}${pad('问题', 6)}问题类型`)
console.log('-'.repeat(130))
for (const r of rows) {
  if (r.invalid) { console.log(`${pad(r.route, 12)}${pad(r.reqW + 'x' + r.reqH, 10)}作废 ${r.invalid} ${pad(r.got ?? '', 40)}`); continue }
  const k = r.report?.issues?.map((i) => i.kind).sort().join(',') || '（无）'
  console.log(pad(r.route, 12) + pad(`${r.reqW}x${r.reqH}`, 10) + pad(`${r.vw}x${r.vh}`, 10) +
    pad(r.state ?? 'base', 18) + pad(r.textLen, 7) + pad(r.settle?.state ?? '?', 16) +
    pad(r.report?.issues?.length ?? 0, 6) + k)
}

console.log(`\n—— 量具自证 ——`)
console.log(`目标 ${ORIGIN}  tag ${TAG}   路由 ${ROUTES.length} × 视口 ${SIZES.length} = ${rows.length} 组`)
console.log(`根字号 ${FONT_SCALE === 1 ? '16px（默认，未缩放）' : `${FONT_SCALE}× = ${(BASE_FONT_PX * FONT_SCALE).toFixed(2)}px（每组回读 :root font-size，未生效即作废）`}`)
console.log(`主题 ${THEME === 'dark' ? '暗色（每组回读 html.dark 与 --app-bg，未生效即作废）' : '浅色（默认）'}`)
console.log(`inset 注入 ${INSETS ? `left=${INSETS[0]} right=${INSETS[1] ?? INSETS[0]} top=${INSETS[2] ?? 0} bottom=${INSETS[3] ?? 0}（每组回读 --app-safe-*，读不回即作废）` : '未注入（env() 在 headless 恒 0 ⇒ 横向 safe-area 本轮结构性不可见）'}`)
console.log(`浮层状态 ${OVERLAY ? '已开（每组多采一次抽屉/Sheet）' : '未开（抽屉默认关着 ⇒ 本轮量不到）'}`)
console.log(`作废 ${rows.filter((r) => r.invalid).length}（视口回读不符 / 无内容 / 错误页）`)
console.log(`有效 ${valid.length}   ★ 有问题的组 ${bad.length}`)
if (!bad.length) console.log('   （无问题）')
for (const [k, list] of Object.entries(byKind)) {
  console.log(`\n[${k}] ${list.length} 处`)
  for (const x of list.slice(0, 8)) console.log(`   ${pad(x.route, 12)} ${pad(x.size, 10)} ${x.sev ?? ''} ${(x.detail ?? '').slice(0, 70)}`)
  if (list.length > 8) console.log(`   …… 另有 ${list.length - 8} 处`)
}

mkdirSync(resolve(OUT), { recursive: true })
const file = join(resolve(OUT), `layout-audit-${TAG}.json`)
writeFileSync(file, JSON.stringify({ origin: ORIGIN, tag: TAG, at: new Date().toISOString(), rows, byKind }, null, 2))
console.log(`\n落盘：${file}`)
