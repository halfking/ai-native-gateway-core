#!/usr/bin/env node
// design-motion-runtime-check.mjs — 「指数减速到位」的**运行时尺**（2026-10-27 · 第 26 增量）
//
// ★★ 为什么要有运行时尺（源码门看不见的事）：
//   ① **令牌是否真的解析** —— `var(--kx-motion-ease-out)` 写对了，但令牌没定义 ⇒
//      整条声明变成 invalid-at-computed-value-time ⇒ 缓动**静默回落**成 `ease`
//      ⇒ 源码门全绿、界面手感没变。★ 这就是「门绿 ≠ 事实已改」的一个实例。
//   ② **computed 出来的属性列表** —— 源码写 `transition: opacity .2s var(...)`，
//      浏览器算出来的 `transition-property` 就是 `opacity`；写 `all` 就是 `all`。
//      运行时尺量的是**浏览器认的那个值**，不是源码里那个字符串。
//
// ★ 作用域边界（★ 必须写进文档，否则读数会被误读）：
//   · 运行时尺量的是**页面上真实存在的元素**，包括第三方库（element-plus）自带的过渡
//     ⇒ 读数里可能含**不是本仓源码造成**的项；报告会把选择器列出来，供人工分辨。
//   · 源码门管的是「本仓源码写出来的 motion」；运行时尺管的是「浏览器最终怎么算」。
//     两把尺互补，谁也替代不了谁。
//
// 用法：
//   node scripts/design-motion-runtime-check.mjs --selftest
//   GATEWAY_WEB_ORIGIN=http://localhost:5200 node scripts/design-motion-runtime-check.mjs \
//       --pages="/free-pool,/keys" --json=out.json

import { spawn } from 'node:child_process'
// ★ 顶层逗号切分**复用**判据模块（★ 括号感知 —— `cubic-bezier(0.16, 1, 0.3, 1)` 本身含逗号，
//   按逗号裸切会把一条曲线切碎 ⇒ 判据自己判自己不合规）
import { splitTopLevel } from '../src/design/motion.ts'
import { mkdtempSync, rmSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// ───────────────────────── 纯函数层（可自证） ─────────────────────────

/** 指数减速曲线的规范形状 */
export const CANONICAL = 'cubic-bezier(0.16, 1, 0.3, 1)'

/** 把 computed 的 timing-function 归一（去掉多余空白与末尾分号） */
export function normalizeTiming(raw) {
  if (raw === null || raw === undefined) return ''
  return String(raw).trim().replace(/\s+/g, ' ').replace(/;$/, '').toLowerCase()
}

/**
 * 判断**单个** timing-function 是不是那条曲线。
 * ★ 形状判（不是写法判）：`cubic-bezier(x1,y1,x2,y2)` 且 y1/y2 都接近 1
 *   ⇒ 「起步即快、末尾指数减速」。
 */
export function isDecelCurveOne(raw) {
  const t = normalizeTiming(raw)
  if (!t) return false
  if (t === 'linear' || t === 'ease' || t === 'ease-in' || t === 'ease-out' || t === 'ease-in-out') {
    return false
  }
  const m = t.match(/^cubic-bezier\(([^)]+)\)$/)
  if (!m) return false
  // ★ cubic-bezier(x1, y1, x2, y2) —— **y2 是第 4 个数**（nums[3]），不是第 3 个。
  //   ★ 第一版写成 nums[1] >= 0.9 && nums[2] >= 0.9 ⇒ nums[2] 是 **x2 = 0.3**
  //     ⇒ 连**规范曲线自己**都被判不合规 ⇒ 自证 9/12 当场抓住。
  const nums = m[1].split(',').map((x) => parseFloat(x.trim()))
  if (nums.length !== 4 || nums.some((n) => Number.isNaN(n))) return false
  return nums[1] >= 0.9 && nums[3] >= 0.9
}

/**
 * 判断一个 computed 的 timing-function（**可能是列表**）是不是「全是那条曲线」。
 * ★★★ computed 的 `transition-timing-function` 与 `transition-property` **一一对应** ⇒
 *   多属性过渡时它就是**逗号分隔的列表**（`cubic-bezier(...), cubic-bezier(...)`）。
 *   ★ 第一版拿**整串**去匹配单个曲线 ⇒ 多值串必然匹配不上 ⇒ 17 处「非减速曲线」全是**假红**
 *     （实测那 17 处打印出来就是那条曲线）⇒ ★ 两把尺对不上时，先怀疑**尺**，别急着改代码。
 */
export function isDecelCurve(raw) {
  const t = normalizeTiming(raw)
  if (!t) return false
  const items = splitTopLevel(t).map((x) => x.trim()).filter(Boolean)
  if (!items.length) return false
  return items.every((one) => isDecelCurveOne(one))
}

/** computed 的 transition-property 是否是 `all` */
export function isAllProperty(raw) {
  if (!raw) return false
  return String(raw).split(',').some((x) => x.trim().toLowerCase() === 'all')
}

/** 页面探针：在浏览器里跑，返回读数 */
export const PROBE = `(() => {
  // ★ 浏览器侧的顶层逗号切分（括号感知）—— 与纯函数层**同一口径**：
  //   cubic-bezier 本身含逗号 ⇒ 裸切会把一条曲线切碎
  const splitTopLevel = (str) => {
    const parts = []; let depth = 0, cur = ''
    for (const ch of str) {
      if (ch === '(') depth++
      else if (ch === ')') depth--
      if (ch === ',' && depth === 0) { parts.push(cur); cur = '' } else cur += ch
    }
    parts.push(cur)
    return parts
  }
  const CURVE = 'cubic-bezier(0.16, 1, 0.3, 1)'
  const rootStyle = getComputedStyle(document.documentElement)
  const token = rootStyle.getPropertyValue('--kx-motion-ease-out').trim()
  const nonCurve = []
  const allProp = []
  let withTransition = 0
  const seen = new Set()
  for (const el of document.querySelectorAll('*')) {
    const cs = getComputedStyle(el)
    const dur = cs.transitionDuration || ''
    const hasDur = dur.split(',').some((x) => {
      const v = parseFloat(x)
      return !Number.isNaN(v) && v > 0
    })
    const prop = (cs.transitionProperty || '').trim()
    const timing = (cs.transitionTimingFunction || '').trim()
    if (!prop || prop === 'none' || !hasDur) continue
    withTransition++
    const key = el.tagName + '.' + (el.className || '').toString().split(' ').slice(0, 2).join('.') + '  [' + prop + ']'
    const isAll = prop.split(',').some((x) => x.trim().toLowerCase() === 'all')
    const norm = timing.toLowerCase().replace(/\\s+/g, ' ')
    // ★ timing-function 可能是**列表**（与 transition-property 一一对应）⇒ 逐项判
    // ★ y2 = n[3]（第 4 个数），不是 n[2] —— 与纯函数层同一口径
    const isCurve = splitTopLevel(norm).map((x) => x.trim()).filter(Boolean).every((one) => {
      if (one === CURVE) return true
      const m = one.match(/^cubic-bezier\\(([^)]+)\\)$/)
      if (!m) return false
      const n = m[1].split(',').map((x) => parseFloat(x.trim()))
      return n.length === 4 && !n.some((v) => Number.isNaN(v)) && n[1] >= 0.9 && n[3] >= 0.9
    })
    if (isAll && !allProp.some((x) => x.key === key)) allProp.push({ key, prop, timing })
    if (!isCurve && !nonCurve.some((x) => x.key === key)) nonCurve.push({ key, prop, timing })
  }
  return { token, withTransition, nonCurve, allProp }
})()`

// ───────────────────────── 自证 ─────────────────────────

export const SELFTEST = [
  { name: '规范曲线 ⇒ 合规', raw: 'cubic-bezier(0.16, 1, 0.3, 1)', want: true },
  { name: '带分号/多空白 ⇒ 仍合规', raw: ' cubic-bezier(0.16, 1, 0.3, 1); ', want: true },
  { name: '另一个减速曲线（y1/y2 都接近 1）⇒ 合规', raw: 'cubic-bezier(0.22, 1, 0.36, 1)', want: true },
  { name: 'ease-in-out 形（y=0/0）⇒ 不合规', raw: 'cubic-bezier(0.64, 0, 0.78, 0)', want: false },
  { name: 'ease ⇒ 不合规', raw: 'ease', want: false },
  { name: 'ease-out ⇒ 不合规', raw: 'ease-out', want: false },
  { name: 'linear ⇒ 不合规', raw: 'linear', want: false },
  { name: '空 ⇒ 不合规', raw: '', want: false },
  { name: '非法 cubic-bezier ⇒ 不合规', raw: 'cubic-bezier(a, b, c, d)', want: false },
  { name: 'property = all ⇒ 命中', raw: 'all', want: true },
  { name: 'property = opacity, all ⇒ 命中', raw: 'opacity, all', want: true },
  { name: 'property = opacity ⇒ 不命中', raw: 'opacity', want: false },
  { name: '**列表**：两项都是那条曲线 ⇒ 合规（computed 的 timing-function 是列表）', raw: 'cubic-bezier(0.16, 1, 0.3, 1), cubic-bezier(0.16, 1, 0.3, 1)', want: true },
  { name: '**列表**：一项不合规 ⇒ 不合规', raw: 'cubic-bezier(0.16, 1, 0.3, 1), ease', want: false },
]

export function runSelftest() {
  const failed = []
  let passed = 0
  for (const c of SELFTEST) {
    const got = c.name.includes('property') ? isAllProperty(c.raw) : isDecelCurve(c.raw)
    if (got !== c.want) failed.push(`${c.name} —— 期望 ${c.want} 实得 ${got}`)
    else passed++
  }
  return { passed, total: SELFTEST.length, failed }
}

// ───────────────────────── CDP ─────────────────────────

// ★ 2026-10-28 Windows 可移植性：补 win32 的 Chrome/Edge 候选
//   （Chromium 内核的 Edge 同样支持 --headless=new + CDP），
//   并尊重 CHROME_PATH 环境变量（与 design-elevation-runtime-check 的约定一致）。
const CHROME_PATHS = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
].filter(Boolean)

function findChrome() {
  for (const p of CHROME_PATHS) if (existsSync(p)) return p
  return null
}

export async function runRuntimeCheck(pages, base, port = 9333) {
  const chromePath = findChrome()
  if (!chromePath) throw new Error('找不到 Chrome（运行时尺需要本机有 Google Chrome）')
  const profile = mkdtempSync(join(tmpdir(), 'q26-motion-'))
  const chrome = spawn(
    chromePath,
    [
      '--headless=new',
      `--remote-debugging-port=${port}`,
      `--user-data-dir=${profile}`,
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-gpu',
      '--hide-scrollbars',
      '--window-size=1440,900',
      'about:blank',
    ],
    { stdio: 'ignore' },
  )
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  try {
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
        msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result)
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
        await cdp('Page.navigate', { url: base + p })
        await sleep(3200)
        const probe = await evaluate(PROBE)
        report.pages.push({
          page: p,
          token: probe.token,
          withTransition: probe.withTransition,
          nonCurve: probe.nonCurve,
          allProp: probe.allProp,
        })
      } catch (e) {
        report.errors.push({ page: p, error: String(e?.message || e) })
      }
    }
    ws.close()
    return report
  } finally {
    chrome.kill()
    // ★ 清理与 Chrome 退出有竞态（ENOTEMPTY）⇒ 等一下 + 重试，且**清理失败不致命**
    await sleep(400)
    for (let i = 0; i < 5; i++) {
      try {
        rmSync(profile, { recursive: true, force: true })
        break
      } catch {
        await sleep(300)
      }
    }
  }
}

// ───────────────────────── CLI ─────────────────────────

const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  const r = runSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log(`\n自证 ${r.passed}/${r.total}` + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'
const pagesArg = argv.find((a) => a.startsWith('--pages='))
const PAGES = (pagesArg ? pagesArg.slice(8) : '/free-pool,/keys,/request-logs,/models')
  .split(',')
  .filter(Boolean)

const res = await runRuntimeCheck(PAGES, BASE)
const jsonOut = argv.find((a) => a.startsWith('--json='))
const payload = {
  base: BASE,
  pages: res.pages,
  errors: res.errors,
  summary: {
    pages: res.pages.length,
    withTransition: res.pages.reduce((s, p) => s + (p.withTransition ?? 0), 0),
    nonCurve: res.pages.reduce((s, p) => s + (p.nonCurve?.length ?? 0), 0),
    allProp: res.pages.reduce((s, p) => s + (p.allProp?.length ?? 0), 0),
    tokenMissing: res.pages.filter((p) => !p.token).length,
  },
}
if (jsonOut) {
  const { writeFileSync } = await import('node:fs')
  writeFileSync(jsonOut.slice(7), JSON.stringify(payload, null, 2) + '\n')
  console.log(JSON.stringify(payload.summary))
} else {
  for (const p of res.pages) {
    console.log(`\n${p.page}  令牌=${p.token || '★未定义'}  有过渡的元素=${p.withTransition}`)
    console.log(`  非减速曲线 ${p.nonCurve.length} 处：`)
    for (const n of p.nonCurve.slice(0, 8)) console.log(`    ${n.key}  timing=${n.timing}`)
    console.log(`  transition-property 含 all ${p.allProp.length} 处：`)
    for (const n of p.allProp.slice(0, 8)) console.log(`    ${n.key}  timing=${n.timing}`)
  }
  console.log('\nsummary:', JSON.stringify(payload.summary))
}
for (const e of res.errors) console.error('ERR', e.page, e.error)
