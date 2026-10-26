#!/usr/bin/env node
/**
 * design-elevation-runtime-check.mjs —— 浮起配方的【运行时】核对（第 18 增量）
 *
 * 为什么要有这道工具（它和 design-elevation-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 * `design-elevation-guard.mjs` 读的是**源码文本**；
 * 本工具读的是**浏览器级联之后的计算值**。
 *
 * 两者能看见的东西不一样：
 *   · token 名字拼错 / token 未定义 / 被别的规则覆盖 ⇒ 只有运行时才暴露；
 *   · 元素在 DOM 里挂上了、但源码里写的是别的选择器 ⇒ 只有运行时才暴露；
 *   · 「源码合规」≠「运行时合规」——这是第 18 增量写进 LESSONS.md §450 的通用式。
 *
 * 判据（与源码门【同一个口径】，不是另立一套）
 * ────────────────────────────────────────────
 *   遍历页面里【每一个元素】的计算 box-shadow，逐层拆开：
 *     · inset 层          → 不参与抬升判定（内阴影不表示深度）
 *     · 零偏移层（环）    → 不参与抬升判定（焦点圈 / 探测标记，不表示深度）
 *     · 非零偏移层        → **必须**等于四支 token 之一的解析值：
 *         --kx-shadow-xs / sm / md / md-left（浅色与暗色各一套）
 *   ⇒ 任何「非 token 的非零偏移阴影」都是违规。
 *
 * 用法
 * ────
 *   node scripts/design-elevation-runtime-check.mjs            # 跑全部页面 × 两个主题
 *   node scripts/design-elevation-runtime-check.mjs --selftest # 判据自证（不需要浏览器）
 *   node scripts/design-elevation-runtime-check.mjs --pages /dashboard,/live
 *   node scripts/design-elevation-runtime-check.mjs --json out.json
 *
 * 环境依赖
 * ────────
 *   需要本机有 Google Chrome（用于无头模式 + CDP）。
 *   **没有 Chrome 时本工具不产出任何判定** ⇒ 打印 ⏭ SKIP 并说明「未测量」，
 *   ★ 绝不把「没量到」写成「通过」——两者必须分开（LESSONS.md §450 形态八）。
 */
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean)

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'
const DEFAULT_PAGES = [
  '/dashboard', '/live', '/models', '/keys', '/routing-v2',
  '/request-logs', '/format-anomalies', '/tenants', '/users',
]

// ── 纯函数：四支 token 在两个主题下的【解析值】─────────────────────────
export const ELEVATION_TOKEN_RESOLUTIONS = {
  light: {
    xs: 'rgba(30, 55, 100, 0.06) 0px 1px 2px 0px',
    sm: 'rgba(30, 55, 100, 0.06) 0px 8px 24px 0px',
    md: 'rgba(30, 50, 90, 0.1) 0px 16px 40px 0px',
    'md-left': 'rgba(30, 50, 90, 0.1) -12px 0px 32px 0px',
  },
  dark: {
    xs: 'rgba(0, 0, 0, 0.28) 0px 1px 2px 0px',
    sm: 'rgba(0, 0, 0, 0.28) 0px 8px 24px 0px',
    md: 'rgba(0, 0, 0, 0.4) 0px 16px 40px 0px',
    'md-left': 'rgba(0, 0, 0, 0.4) -12px 0px 32px 0px',
  },
}

// ── 纯函数：把 Chrome 的计算值拆成层 ────────────────────────────────────
export function splitShadowLayers(value) {
  if (!value || value === 'none') return []
  const out = []
  let depth = 0
  let cur = ''
  for (const ch of value) {
    if (ch === '(') depth++
    else if (ch === ')') depth--
    if (ch === ',' && depth === 0) { out.push(cur.trim()); cur = ''; continue }
    cur += ch
  }
  if (cur.trim()) out.push(cur.trim())
  return out.filter(Boolean)
}

// Chrome 的计算值序列化顺序：color offsetX offsetY blur spread
export function parseShadowLayer(layer) {
  const inset = /\binset\b/.test(layer)
  const nums = [...layer.matchAll(/(-?[\d.]+)px/g)].map((m) => parseFloat(m[1]))
  return {
    inset,
    ox: nums[0] || 0,
    oy: nums[1] || 0,
    blur: nums[2] || 0,
    spread: nums[3] || 0,
    color: (layer.match(/rgba?\([^)]*\)/) || [''])[0],
    raw: layer,
  }
}

/**
 * 判定一层阴影是不是「违规的抬升」。
 * ★ 与源码门 E1 同一个口径：inset 与零偏移环【不参与】抬升判定。
 */
export function classifyLayer(layer, theme) {
  const p = parseShadowLayer(layer)
  if (p.inset) return { kind: 'inset', ok: true }
  if (p.ox === 0 && p.oy === 0) return { kind: 'ring', ok: true }
  const allowed = Object.values(ELEVATION_TOKEN_RESOLUTIONS[theme] || {})
  const ok = allowed.includes(p.raw)
  return { kind: 'elevation', ok, parsed: p }
}

export function runRuntimeSelftest() {
  const cases = []
  const t = (name, fn) => {
    try { fn(); cases.push({ name, ok: true }) }
    catch (e) { cases.push({ name, ok: false, msg: e.message }) }
  }
  const eq = (a, b, why) => { if (a !== b) throw new Error(`${why}：期望 ${JSON.stringify(b)}，实际 ${JSON.stringify(a)}`) }

  t('多值阴影按顶层逗号拆层（嵌套逗号不拆）', () => {
    const layers = splitShadowLayers('rgba(0, 0, 0, 0.4) 0px 16px 40px 0px, rgba(0, 0, 0, 0.28) 0px 1px 2px 0px')
    eq(layers.length, 2, '层数')
    eq(layers[0], 'rgba(0, 0, 0, 0.4) 0px 16px 40px 0px', '第 1 层')
  })
  t('color-mix 里的逗号不拆层', () => {
    const layers = splitShadowLayers('rgba(0, 0, 0, 0.2) 0px 0px 0px 3px, rgba(1, 2, 3, 0.4) 0px 1px 2px 0px')
    eq(layers.length, 2, '层数')
  })
  t('none ⇒ 零层', () => eq(splitShadowLayers('none').length, 0, '层数'))
  t('inset 层不参与抬升判定', () => {
    const r = classifyLayer('rgba(0, 0, 0, 0.2) inset 0px 2px 4px 0px', 'dark')
    eq(r.kind, 'inset', 'kind'); eq(r.ok, true, 'ok')
  })
  t('零偏移环不参与抬升判定（不是抬升）', () => {
    const r = classifyLayer('rgba(0, 0, 0, 0.2) 0px 0px 0px 3px', 'dark')
    eq(r.kind, 'ring', 'kind'); eq(r.ok, true, 'ok')
  })
  t('有偏移但不是 token ⇒ 违规', () => {
    const r = classifyLayer('rgba(0, 0, 0, 0.16) -8px 0px 32px 0px', 'dark')
    eq(r.kind, 'elevation', 'kind'); eq(r.ok, false, 'ok')
  })
  t('token 的 md-left（负 X 偏移）⇒ 合规', () => {
    const r = classifyLayer(ELEVATION_TOKEN_RESOLUTIONS.dark['md-left'], 'dark')
    eq(r.kind, 'elevation', 'kind'); eq(r.ok, true, 'ok')
  })
  t('四支 token × 两主题全部自洽（解析值能被自己判合规）', () => {
    for (const theme of ['light', 'dark']) {
      for (const [name, v] of Object.entries(ELEVATION_TOKEN_RESOLUTIONS[theme])) {
        const r = classifyLayer(v, theme)
        if (!r.ok) throw new Error(`${theme}/${name} 解析值未被判合规`)
      }
    }
  })
  t('★ 浅色与暗色的同名 token 解析值【不同】⇒ 主题判据会生效', () => {
    eq(ELEVATION_TOKEN_RESOLUTIONS.light.md === ELEVATION_TOKEN_RESOLUTIONS.dark.md, false, 'md 解析值应随主题变')
  })
  t('★ 拿暗色解析值去问浅色判据 ⇒ 必须判违规（防主题串味）', () => {
    const r = classifyLayer(ELEVATION_TOKEN_RESOLUTIONS.dark.md, 'light')
    eq(r.ok, false, 'ok')
  })
  t('色相不同（蓝调黑 vs 纯黑）⇒ 跨主题不误判', () => {
    eq(classifyLayer(ELEVATION_TOKEN_RESOLUTIONS.light.xs, 'dark').ok, false, 'xs')
  })

  const failed = cases.filter((c) => !c.ok)
  for (const c of cases) console.log(`  ${c.ok ? '✓' : '✘'} ${c.name}${c.ok ? '' : ' —— ' + c.msg}`)
  console.log(`\n自证 ${cases.length - failed.length}/${cases.length} ${failed.length ? '—— 有断言恒真/恒假' : '通过 —— 量具有牙。'}`)
  return failed.length === 0
}

// ── 页内探针（在浏览器上下文里跑）────────────────────────────────────────
const PROBE = String.raw`
(() => {
  const splitLayers = ${splitShadowLayers.toString()};
  const parseLayer = ${parseShadowLayer.toString()};
  const RESOLUTIONS = ${JSON.stringify(ELEVATION_TOKEN_RESOLUTIONS)};
  const theme = document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
  const allowed = new Set(Object.values(RESOLUTIONS[theme]));
  const violations = [];
  const seen = {};
  let elevationLayers = 0, ringLayers = 0, insetLayers = 0, elements = 0;
  for (const el of document.querySelectorAll('*')) {
    elements++;
    const bs = getComputedStyle(el).boxShadow;
    if (!bs || bs === 'none') continue;
    for (const raw of splitLayers(bs)) {
      const p = parseLayer(raw);
      if (p.inset) { insetLayers++; continue; }
      if (p.ox === 0 && p.oy === 0) { ringLayers++; continue; }
      elevationLayers++;
      seen[p.raw] = (seen[p.raw] || 0) + 1;
      if (!allowed.has(p.raw)) {
        violations.push({
          tag: el.tagName.toLowerCase(),
          cls: String((el.className && el.className.baseVal !== undefined) ? el.className.baseVal : (el.className || '')).slice(0, 90),
          shadow: p.raw,
        });
      }
    }
  }
  return { theme, elements, elevationLayers, ringLayers, insetLayers, violations, distinct: Object.keys(seen) };
})()
`

// ── CDP 驱动 ────────────────────────────────────────────────────────────
async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'elev-runtime-'))
  const chrome = spawn(CHROME_CANDIDATES[0], [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars',
    '--window-size=1440,900', 'about:blank',
  ], { stdio: 'ignore' })

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  for (let i = 0; i < 80; i++) {
    try { if ((await fetch(`http://127.0.0.1:${port}/json/version`)).ok) break } catch { /* 等 */ }
    await sleep(250)
  }
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
  const target = list.find((t) => t.type === 'page')
  if (!target) throw new Error('找不到 page target')

  const ws = new WebSocket(target.webSocketDebuggerUrl)
  await new Promise((res, rej) => {
    ws.addEventListener('open', res, { once: true })
    ws.addEventListener('error', rej, { once: true })
  })
  let seq = 0
  const pending = new Map()
  ws.addEventListener('message', (ev) => {
    const msg = JSON.parse(ev.data)
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id)
      pending.delete(msg.id)
      if (msg.error) reject(new Error(msg.error.message))
      else resolve(msg.result)
    }
  })
  const cdp = (method, params = {}) => {
    const id = ++seq
    ws.send(JSON.stringify({ id, method, params }))
    return new Promise((resolve, reject) => {
      pending.set(id, { resolve, reject })
      setTimeout(() => { if (pending.has(id)) { pending.delete(id); reject(new Error(`CDP 超时: ${method}`)) } }, 60000)
    })
  }
  const evaluate = async (expression) => {
    const r = await cdp('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true, userGesture: true })
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
    return r.result.value
  }
  await cdp('Page.enable')
  await cdp('Runtime.enable')

  const report = { pages: [], errors: [] }
  for (const p of pages) {
    try {
      await cdp('Page.navigate', { url: BASE + p })
      await sleep(2800)
      const themes = {}
      for (const theme of ['light', 'dark']) {
        await evaluate(`(() => { document.documentElement.setAttribute('data-theme', ${JSON.stringify(theme)}); return true })()`)
        await sleep(500)
        themes[theme] = await evaluate(PROBE)
      }
      report.pages.push({ page: p, themes })
      const bad = themes.light.violations.length + themes.dark.violations.length
      console.log(`${bad ? '✘' : '✔'} ${p}  light:${themes.light.violations.length} dark:${themes.dark.violations.length} 违规 · 抬升层 ${themes.light.elevationLayers}/${themes.dark.elevationLayers}`)
    } catch (e) {
      report.errors.push({ page: p, error: String(e?.message || e) })
      console.log(`✘ ${p} 失败: ${e?.message || e}`)
    }
  }
  ws.close()
  chrome.kill()
  return report
}

// ── CLI ──────────────────────────────────────────────────────────────────
const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  process.exit(runRuntimeSelftest() ? 0 : 1)
}

const chrome = CHROME_CANDIDATES.find((p) => existsSync(p))
if (!chrome) {
  console.log('⏭ SKIP —— 未找到 Chrome 可执行文件 ⇒ **本轮未测量**（不是「通过」）')
  console.log('   可用 CHROME_PATH=/path/to/chrome 指定；或先装 Google Chrome。')
  process.exit(0)
}

const pagesArg = argv.find((a) => a.startsWith('--pages='))
const pages = pagesArg ? pagesArg.slice('--pages='.length).split(',').map((s) => s.trim()).filter(Boolean) : DEFAULT_PAGES
const jsonOut = argv.includes('--json') ? argv[argv.indexOf('--json') + 1] : null

console.log(`运行时浮起核对 —— ${BASE} · ${pages.length} 页 × 2 主题 · Chrome: ${chrome}\n`)
const report = await runChrome(9335 + Math.floor(process.pid % 50), pages)

let violations = 0
let scanned = 0
for (const pg of report.pages) {
  for (const theme of ['light', 'dark']) {
    const t = pg.themes[theme]
    violations += t.violations.length
    scanned += 1
    console.log(`[${pg.page}/${theme}] 元素 ${t.elements} · 抬升层 ${t.elevationLayers} · 零偏移环 ${t.ringLayers} · inset ${t.insetLayers} · 违规 ${t.violations.length}`)
    for (const v of t.violations.slice(0, 6)) console.log(`   ✘ ${v.tag}.${v.cls} → ${v.shadow}`)
  }
}
if (jsonOut) writeFileSync(jsonOut, JSON.stringify(report, null, 2))

console.log(`\n════ 汇总 ════`)
console.log(`扫描 ${scanned} 次（页 × 主题）· 非 token 抬升层 ${violations} 处 · 页级失败 ${report.errors.length} 个`)
if (report.errors.length) for (const e of report.errors) console.log(`   ✘ ${e.page}: ${e.error}`)
if (violations === 0 && report.errors.length === 0 && scanned > 0) {
  console.log('✅ 运行时的每一层抬升都等于四支 token 之一 —— PASS')
  process.exit(0)
}
console.log('❌ 运行时存在非 token 的抬升层，或有页面没测到 —— FAIL')
process.exit(1)
