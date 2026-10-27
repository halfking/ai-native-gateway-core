#!/usr/bin/env node
/**
 * design-type-scale-runtime-check.mjs —— 「字号必须落在角色阶上」的【运行时】核对（第 23 增量）
 *
 * 为什么要有这道工具（它和 design-type-scale-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 * 源码门读的是**源码里声明的 font-size**；本工具读的是**浏览器里真实渲染出来的字号**。
 *
 * ★ 源码门看不见的三件事：
 *   ① **组件库/第三方样式**（Element Plus、图标字体）带来的字号，源码里根本没写；
 *   ② **继承与级联**的实际结果（源码里写 12px，渲染出来可能被更高优先级规则改成别的）；
 *   ③ ★★ **字号变大有没有把布局撑破** —— 这是本轮最实际的风险：
 *      10px → 11px、9px → 11px、8px → 11px 都是**变大**（可读性变好），
 *      但密集表格/时间轴/图表标注一旦撑破列宽，用户看到的就是「挤成一团」。
 *      ⇒ 本工具量**横向溢出**（scrollWidth > clientWidth）的元素数，改造前后对拍。
 *
 * ★ 运行时判据（与源码门同一口径）：
 *
 *   **承载文字的元素（自身有直接文本节点），其渲染字号必须落在角色阶上。**
 *
 *   ★ 为什么用「有直接文本节点」而不是「类名像不像文字」：
 *     图标承载（`<div class="error-icon"><Warning /></div>`、字形 `<span class="tool-icon">🚀</span>`）
 *     **没有直接文本节点**（前者是组件，后者虽是文本但属登记豁免）⇒ 天然被跳过
 *     ⇒ 运行时不必重复实现源码层那套图标证据推导。
 *
 * ★ 「可见」的口径：**已渲染**（`getClientRects().length > 0`），**不要求在视口内**。
 *
 * 用法
 * ────
 *   node scripts/design-type-scale-runtime-check.mjs            # 跑全部页面 × 两个主题
 *   node scripts/design-type-scale-runtime-check.mjs --selftest # 判据自证（不需要浏览器）
 *   node scripts/design-type-scale-runtime-check.mjs --pages=/free-pool,/admin/proxy
 *   node scripts/design-type-scale-runtime-check.mjs --json out.json
 *
 * 环境依赖
 * ────────
 *   需要本机有 Google Chrome（无头模式 + CDP）。
 *   **没有 Chrome 时本工具不产出任何判定** ⇒ 打印 ⏭ SKIP 并说明「未测量」，
 *   ★ 绝不把「没量到」写成「通过」——两者必须分开。
 */
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const DECISIONS = join(ROOT, 'scripts', 'design-type-scale-decisions.json')

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
  '/', '/free-pool', '/admin/proxy', '/models', '/examples', '/chat',
  '/tenant/pricing', '/keys', '/routing-v2',
]

/** 角色阶（事实源：DESIGN.md §4 的 --kx-text-* 实际取值）。 */
export function roleScalePx() {
  const css = readFileSync(join(ROOT, 'src', 'style.css'), 'utf8')
  const px = new Set()
  for (const m of css.matchAll(/--(?:kx-)?text-[a-z0-9]+\s*:\s*(\d+(?:\.\d+)?)px/gi)) {
    px.add(parseFloat(m[1]))
  }
  return [...px].sort((a, b) => a - b)
}

/** 登记豁免（字形图标等；与源码门、迁移脚本**同一份**裁决簿）。 */
export function manualExemptClasses() {
  try {
    const dec = JSON.parse(readFileSync(DECISIONS, 'utf8'))
    return new Set((dec.manualIconExemptions?.entries ?? []).map((e) => e.class))
  } catch {
    return new Set()
  }
}

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * 一个承载文字的元素的描述符。
 * @typedef {{ cls: string, fontPx: number, text: string }} TextDesc
 */

/**
 * 裁决：承载文字的元素里，字号不在角色阶上的 ⇒ 违规。
 *
 * @param {TextDesc[]} texts
 * @param {number[]} scalePx
 * @param {Set<string>} exemptClasses
 */
export function judgeOffScaleTexts(texts, scalePx, exemptClasses) {
  const off = []
  for (const t of texts) {
    if (scalePx.includes(t.fontPx)) continue
    const cls = (t.cls || '').split(/\s+/).filter(Boolean)
    if (cls.length && cls.every((c) => exemptClasses.has(c))) continue
    off.push(t)
  }
  return off
}

export function runRuntimeSelftest() {
  const scale = [11, 12, 13, 14, 16, 20, 24, 30, 38]
  const ex = new Set(['tool-icon'])
  const cases = [
    {
      name: '★ 在阶上的字号 ⇒ 判绿',
      run: () => judgeOffScaleTexts([{ cls: 'a', fontPx: 14, text: 'x' }], scale, ex).length === 0,
    },
    {
      name: '★ 离阶的字号 ⇒ 判红',
      run: () => judgeOffScaleTexts([{ cls: 'a', fontPx: 10, text: 'x' }], scale, ex).length === 1,
    },
    {
      name: '★ 登记豁免的 class ⇒ 判绿（豁免是活分支）',
      run: () => judgeOffScaleTexts([{ cls: 'tool-icon', fontPx: 22, text: '🚀' }], scale, ex).length === 0,
    },
    {
      name: '★ 对照：豁免 class 换了字号仍然豁免（豁免看 class 不看值）',
      run: () => judgeOffScaleTexts([{ cls: 'tool-icon', fontPx: 99, text: '🚀' }], scale, ex).length === 0,
    },
    {
      name: '★ class 里**全部**类都在豁免名单里才豁免（部分命中不豁免）',
      run: () =>
        judgeOffScaleTexts([{ cls: 'tool-icon other', fontPx: 22, text: 'x' }], scale, ex).length === 1,
    },
    {
      name: '★ 没有 class 的元素不豁免（判红）',
      run: () => judgeOffScaleTexts([{ cls: '', fontPx: 13.5, text: 'x' }], scale, ex).length === 1,
    },
    {
      name: '★ 判据不含全局状态（同一输入两次结果一致）',
      run: () => {
        const a = judgeOffScaleTexts([{ cls: 'a', fontPx: 10, text: 'x' }], scale, ex)
        const b = judgeOffScaleTexts([{ cls: 'a', fontPx: 10, text: 'x' }], scale, ex)
        return JSON.stringify(a) === JSON.stringify(b)
      },
    },
    {
      name: '★ 判据对「第二个是否离阶」敏感（改一处必须改结论）',
      run: () => {
        const before = judgeOffScaleTexts(
          [{ cls: 'a', fontPx: 10, text: 'x' }, { cls: 'b', fontPx: 14, text: 'y' }],
          scale,
          ex,
        ).length
        const after = judgeOffScaleTexts(
          [{ cls: 'a', fontPx: 10, text: 'x' }, { cls: 'b', fontPx: 15, text: 'y' }],
          scale,
          ex,
        ).length
        return before === 1 && after === 2
      },
    },
  ]
  const failed = []
  let passed = 0
  for (const c of cases) {
    let ok = false
    try {
      ok = c.run()
    } catch {
      ok = false
    }
    if (ok) passed++
    else failed.push(c.name)
  }
  return { passed, total: cases.length, failed }
}

// ── 浏览器探针 ────────────────────────────────────────────────────────────
const PROBE = `(() => {
  const SCALE = ${JSON.stringify(roleScalePx())}
  const texts = []
  let measured = 0
  let overflow = 0
  const overflowSamples = []
  const all = document.querySelectorAll('body *')
  for (const el of all) {
    if (!el.getClientRects || el.getClientRects().length === 0) continue
    measured++
    // 横向溢出（★ 字号变大把布局撑破的探针）
    if (el.scrollWidth > el.clientWidth + 1 && el.clientWidth > 0) {
      overflow++
      if (overflowSamples.length < 12) {
        overflowSamples.push({
          cls: (el.className && el.className.baseVal !== undefined ? el.className.baseVal : el.className || '').toString().slice(0, 60),
          tag: el.tagName.toLowerCase(),
          client: el.clientWidth,
          scroll: el.scrollWidth,
        })
      }
    }
    // 承载文字？⇒ 自身有直接文本节点
    let own = ''
    for (const n of el.childNodes) {
      if (n.nodeType === 3) own += n.nodeValue
    }
    own = own.trim()
    if (!own) continue
    const cs = getComputedStyle(el)
    const fs = parseFloat(cs.fontSize)
    if (!Number.isFinite(fs)) continue
    texts.push({
      cls: (el.className && el.className.baseVal !== undefined ? el.className.baseVal : el.className || '').toString(),
      fontPx: Math.round(fs * 100) / 100,
      text: own.slice(0, 40),
    })
  }
  return { scale: SCALE, texts, measured, overflow, overflowSamples }
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'ts-runtime-'))
  const chrome = spawn(CHROME_CANDIDATES[0], [
    '--headless=new',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--hide-scrollbars',
    '--window-size=1440,900',
    'about:blank',
  ], { stdio: 'ignore' })

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  for (let i = 0; i < 80; i++) {
    try {
      if ((await fetch(`http://127.0.0.1:${port}/json/version`)).ok) break
    } catch {
      /* 等 */
    }
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
      setTimeout(() => {
        if (pending.has(id)) {
          pending.delete(id)
          reject(new Error(`CDP 超时: ${method}`))
        }
      }, 60000)
    })
  }
  const evaluate = async (expression) => {
    const r = await cdp('Runtime.evaluate', {
      expression,
      awaitPromise: true,
      returnByValue: true,
      userGesture: true,
    })
    if (r.exceptionDetails) {
      throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
    }
    return r.result.value
  }
  await cdp('Page.enable')
  await cdp('Runtime.enable')

  const report = { pages: [], errors: [] }
  for (const p of pages) {
    try {
      await cdp('Page.navigate', { url: BASE + p })
      await sleep(3200)
      const themes = {}
      for (const theme of ['light', 'dark']) {
        await evaluate(
          `(() => { document.documentElement.setAttribute('data-theme', ${JSON.stringify(theme)}); return true })()`,
        )
        await sleep(600)
        themes[theme] = await evaluate(PROBE)
      }
      report.pages.push({ page: p, themes })
    } catch (e) {
      report.errors.push({ page: p, error: String(e?.message || e) })
    }
  }
  ws.close()
  chrome.kill()
  return report
}

// ── CLI ──────────────────────────────────────────────────────────────────
const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  const r = runRuntimeSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log(
    `\n自证 ${r.passed}/${r.total}` + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'),
  )
  process.exit(r.failed.length ? 2 : 0)
}

if (!CHROME_CANDIDATES.length || !existsSync(CHROME_CANDIDATES[0])) {
  console.log('⏭ SKIP：找不到 Chrome ⇒ **未测量**（不是「通过」）。')
  console.log('   设 CHROME_PATH 指向可执行文件后重跑。')
  process.exit(0)
}

const pagesArg = argv.find((a) => a.startsWith('--pages='))
const pages = pagesArg ? pagesArg.slice(8).split(',').filter(Boolean) : DEFAULT_PAGES
const port = 9331 + Math.floor(Math.random() * 300)
const report = await runChrome(port, pages)

const scale = roleScalePx()
const exempt = manualExemptClasses()
const summary = { pages: [], errors: report.errors, scale, textOffScale: [], overflow: 0, measured: 0 }
for (const p of report.pages) {
  for (const theme of ['light', 'dark']) {
    const t = p.themes[theme]
    if (!t) continue
    const off = judgeOffScaleTexts(t.texts, scale, exempt)
    summary.measured += t.measured
    summary.overflow += t.overflow
    for (const o of off) summary.textOffScale.push({ page: p.page, theme, ...o })
    summary.pages.push({
      page: p.page,
      theme,
      measured: t.measured,
      textEls: t.texts.length,
      offScale: off.length,
      overflow: t.overflow,
      overflowSamples: t.overflowSamples,
    })
  }
}

const jsonOut = argv.find((a) => a.startsWith('--json='))
if (jsonOut) writeFileSync(jsonOut.slice(7), JSON.stringify({ summary, raw: report }, null, 2))

console.log(`角色阶：${scale.join('/')}`)
console.log(
  `读数：渲染元素 ${summary.measured} 个 · 承载文字的元素里**离阶** ${summary.textOffScale.length} 处 · 横向溢出 ${summary.overflow} 处`,
)
for (const s of summary.pages) {
  console.log(
    `  ${s.page.padEnd(18)} ${s.theme.padEnd(5)} 元素 ${String(s.measured).padStart(4)} · 文字元素 ${String(s.textEls).padStart(4)} · 离阶 ${s.offScale} · 溢出 ${s.overflow}`,
  )
}
if (summary.textOffScale.length) {
  console.log('\n离阶的承载文字元素：')
  const seen = new Set()
  for (const o of summary.textOffScale) {
    const k = `${o.cls}|${o.fontPx}`
    if (seen.has(k)) continue
    seen.add(k)
    console.log(`  ${o.page} [${o.theme}] .${o.cls.split(/\s+/)[0]} ${o.fontPx}px  「${o.text}」`)
  }
}
if (report.errors.length) {
  console.log('\n⚠️ 采集失败的页面：')
  for (const e of report.errors) console.log(`  ${e.page}: ${e.error}`)
}
process.exit(summary.textOffScale.length ? 1 : 0)
