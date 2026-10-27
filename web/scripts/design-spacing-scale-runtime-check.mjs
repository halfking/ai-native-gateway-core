#!/usr/bin/env node
/**
 * design-spacing-scale-runtime-check.mjs —— 「间距必须落在角色阶上」的【运行时】核对（第 24 增量）
 *
 * 为什么要有这道工具（它和 design-spacing-scale-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 * 源码门读的是**源码里声明的 spacing**；本工具读的是**浏览器里真实渲染出来的间距**。
 *
 * ★ 源码门看不见的三件事：
 *   ① **组件库/第三方样式**（Element Plus 等）带来的 padding/margin，源码里根本没写；
 *   ② **级联与继承**的实际结果（源码里写 16px，渲染出来可能被更高优先级规则改成别的）；
 *   ③ ★★ **收紧有没有把版面挤坏** —— 这是本轮最实际的风险：
 *      全局收紧约 17%（6px→4px、10px→8px、20px→16px）⇒ 密集表格 / 弹层 / 抽屉一旦被挤破，
 *      用户看到的就是「挤成一团」「挤出了横向滚动条」。
 *
 * ★★ 为什么本工具的主判据是【rect 实测的兄弟间隙】，而不是【computed 的 margin/padding】：
 *   `getComputedStyle` 会把 `margin: 0 auto` 与 `%` 内边距**解析成算出来的 px**
 *   ⇒ 那类值在运行时层**无法与「手写的魔法数」区分** ⇒ 拿它们当判据会造出成片假阳性。
 *   而**量两个相邻子元素的矩形间距**拿到的是**用户真正看到的那个间隙**
 *   （它已经含了两侧 margin + 容器 gap + 边框）⇒ **与写法无关**，是对「节奏」最直接的观测。
 *
 * ★ 运行时判据：
 *   ① **兄弟间隙落在阶上**（rect 实测；0 与 ≤3px 的发丝豁免）；
 *   ② **横向溢出不得增加**（收紧把版面挤破的探针）；
 *   ③ 计算后的 spacing 值分布（**只报告、不作判据** —— `auto`/`%` 在运行时层不可区分，见上）。
 *
 * ★ 这道尺是**趋势型**的（相对基线只降不升），而源码门是**绝对型**的（离阶必须 0）。
 *   ★ 理由：运行时层合法存在「源码层豁免」的形态（auto / % / 发丝 / 派生对齐偏移）
 *     ⇒ 运行时**不可能**也不应该要求「离阶 = 0」⇒ 两把尺作用域不同，边界写在这里。
 *
 * 用法
 * ────
 *   node scripts/design-spacing-scale-runtime-check.mjs            # 跑全部页面 × 两个主题
 *   node scripts/design-spacing-scale-runtime-check.mjs --selftest # 判据自证（不需要浏览器）
 *   node scripts/design-spacing-scale-runtime-check.mjs --pages=/free-pool,/admin/proxy
 *   node scripts/design-spacing-scale-runtime-check.mjs --json=out.json
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
const BASELINE = join(ROOT, 'scripts', 'design-spacing-scale-runtime-baseline.json')

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

/** 发丝/光学微调的上限（与源码门同一常量，见 src/design/spacingScale.ts）。 */
export const HAIRLINE_MAX_PX = 3

/** 间距阶（事实源：src/style.css 里 --kx-space-* 的实际定义）。 */
export function spaceScalePx() {
  const css = readFileSync(join(ROOT, 'src', 'style.css'), 'utf8')
  const px = new Set()
  for (const m of css.matchAll(/--kx-space-[a-z0-9-]+\s*:\s*(\d+(?:\.\d+)?)px/gi)) {
    px.add(parseFloat(m[1]))
  }
  return [...px].sort((a, b) => a - b)
}

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * @typedef {{ cls: string, prop: string, px: number }} SpaceDesc
 * @typedef {{ cls: string, gapPx: number }} GapDesc
 */

/**
 * 裁决：值不在阶上的 ⇒ 离阶。
 * ★ 豁免与源码门**同一口径**：0（没有间距）/ 负值（抵消）/ ≤3px（发丝）不算违规。
 * @param {SpaceDesc[]} values
 * @param {number[]} scalePx
 */
export function judgeOffScaleValues(values, scalePx) {
  const off = []
  for (const v of values) {
    if (v.px === 0) continue
    if (v.px < 0) continue
    if (v.px <= HAIRLINE_MAX_PX) continue
    if (scalePx.includes(v.px)) continue
    off.push(v)
  }
  return off
}

/**
 * 裁决：渲染出来的兄弟间隙不在阶上的 ⇒ 离阶。
 * ★ 间隙 0 常见且合法（inline 元素、边框相邻）⇒ 不判。
 * ★ **超出阶顶（scaleTop）的间隙不判** —— 那不是「节奏」，是**布局距离**
 *   （例如 `margin-left: auto` 把兄弟推到两端 ⇒ 实测踩过 1187px / 546px 这类值）。
 * @param {GapDesc[]} gaps
 * @param {number[]} scalePx
 */
export function judgeOffScaleGaps(gaps, scalePx) {
  const off = []
  const top = scalePx.length ? scalePx[scalePx.length - 1] : Infinity
  for (const g of gaps) {
    if (g.gapPx <= 0) continue
    if (g.gapPx <= HAIRLINE_MAX_PX) continue
    if (g.gapPx > top) continue // 布局距离，不是节奏
    if (scalePx.includes(g.gapPx)) continue
    off.push(g)
  }
  return off
}

/**
 * 裁决：**超出阶顶**的间隙数（布局距离，**只统计不判红**）。
 * @param {GapDesc[]} gaps
 * @param {number[]} scalePx
 */
export function countLayoutDistances(gaps, scalePx) {
  const top = scalePx.length ? scalePx[scalePx.length - 1] : Infinity
  return gaps.filter((g) => g.gapPx > top).length
}

/**
 * 裁决：**过挤**的间隙（0 < gap < 阶底）—— 收紧后的回归探针。
 * ★ 注意：这类间隙**不违规**（它可能是发丝），但它**变多了**就说明版面被挤坏了。
 * @param {GapDesc[]} gaps
 * @param {number} scaleBottom 阶底（最小档）
 */
export function judgeCrowdedGaps(gaps, scaleBottom) {
  return gaps.filter((g) => g.gapPx > 0 && g.gapPx < scaleBottom)
}

/**
 * 对照基线（★ 只降不升）：任何一项**升高**都是回归。
 * @param {{offScaleGaps:number, overflow:number, crowded:number}} readings
 * @param {{offScaleGapsMax:number, overflowMax:number, crowdedMax:number}} baseline
 */
export function compareToBaseline(readings, baseline) {
  const regressions = []
  if (readings.offScaleGaps > baseline.offScaleGapsMax) {
    regressions.push(`离阶的兄弟间隙 ${baseline.offScaleGapsMax} → ${readings.offScaleGaps}（升高了）`)
  }
  if (readings.overflow > baseline.overflowMax) {
    regressions.push(`横向溢出 ${baseline.overflowMax} → ${readings.overflow}（升高了）`)
  }
  if (readings.crowded > baseline.crowdedMax) {
    regressions.push(`过挤的间隙 ${baseline.crowdedMax} → ${readings.crowded}（升高了）`)
  }
  return regressions
}

export function runRuntimeSelftest() {
  const scale = [4, 8, 12, 16, 24, 32, 40, 48, 64]
  const cases = [
    {
      name: '★ 阶上的值 ⇒ 判绿',
      run: () => judgeOffScaleValues([{ cls: 'a', prop: 'paddingTop', px: 16 }], scale).length === 0,
    },
    {
      name: '★ 离阶的值 ⇒ 判红',
      run: () => judgeOffScaleValues([{ cls: 'a', prop: 'paddingTop', px: 20 }], scale).length === 1,
    },
    {
      name: '★ 0 / 负值 / ≤3px 豁免（与源码门同一口径）',
      run: () =>
        judgeOffScaleValues(
          [
            { cls: 'a', prop: 'marginTop', px: 0 },
            { cls: 'a', prop: 'marginTop', px: -8 },
            { cls: 'a', prop: 'paddingTop', px: 2 },
          ],
          scale,
        ).length === 0,
    },
    {
      name: '★ 兄弟间隙在阶上 ⇒ 判绿',
      run: () => judgeOffScaleGaps([{ cls: 'a', gapPx: 12 }], scale).length === 0,
    },
    {
      name: '★ 兄弟间隙离阶 ⇒ 判红',
      run: () => judgeOffScaleGaps([{ cls: 'a', gapPx: 14 }], scale).length === 1,
    },
    {
      name: '★ 间隙 0 不判（inline / 边框相邻是合法的）',
      run: () => judgeOffScaleGaps([{ cls: 'a', gapPx: 0 }], scale).length === 0,
    },
    {
      name: '★ 过挤探针：0 < gap < 阶底 才算挤（发丝不算挤）',
      run: () =>
        judgeCrowdedGaps([{ cls: 'a', gapPx: 2 }, { cls: 'a', gapPx: 1 }, { cls: 'a', gapPx: 4 }], 4)
          .length === 2,
    },
    {
      name: '★ 超出阶顶的间隙是「布局距离」⇒ 不判离阶，但必须能统计出来',
      run: () =>
        judgeOffScaleGaps([{ cls: 'a', gapPx: 1187 }], scale).length === 0 &&
        countLayoutDistances([{ cls: 'a', gapPx: 1187 }], scale) === 1,
    },
    {
      name: '★ 阶顶本身算节奏（不是布局距离）',
      run: () =>
        judgeOffScaleGaps([{ cls: 'a', gapPx: 64 }], scale).length === 0 &&
        countLayoutDistances([{ cls: 'a', gapPx: 64 }], scale) === 0,
    },
    {
      name: '★ 基线对照：升高 ⇒ 报回归',
      run: () =>
        compareToBaseline({ offScaleGaps: 5, overflow: 2, crowded: 1 }, {
          offScaleGapsMax: 4,
          overflowMax: 2,
          crowdedMax: 1,
        }).length === 1,
    },
    {
      name: '★ 基线对照：不升 ⇒ 无回归',
      run: () =>
        compareToBaseline({ offScaleGaps: 4, overflow: 2, crowded: 1 }, {
          offScaleGapsMax: 4,
          overflowMax: 2,
          crowdedMax: 1,
        }).length === 0,
    },
    {
      name: '★ 三项分别升高都要被抓（不是只抓第一项）',
      run: () =>
        compareToBaseline({ offScaleGaps: 9, overflow: 9, crowded: 9 }, {
          offScaleGapsMax: 4,
          overflowMax: 2,
          crowdedMax: 1,
        }).length === 3,
    },
    {
      name: '★ 负向对照：判据被写死成「永远 0」时，基线对照就抓不到回归（证明这道检查不是恒过的）',
      run: () => {
        // 坏的实现：离阶判定永远返回空（= 门永远绿）
        const brokenJudge = () => []
        const readings = { offScaleGaps: brokenJudge().length, overflow: 0, crowded: 0 }
        const regressions = compareToBaseline(readings, {
          offScaleGapsMax: 4,
          overflowMax: 2,
          crowdedMax: 1,
        })
        // ⇒ 坏判据 ⇒ 读数 0 ⇒ 不触发回归 ⇒ 说明「检出回归」这件事**确实依赖判据的正确性**
        return regressions.length === 0
      },
    },
    {
      name: '★ 对照：真判据下同样的升高**必须**被抓（与上一条成对）',
      run: () => {
        // 5 处离阶间隙（> 基线 4）⇒ 必须报回归
        const gaps = Array.from({ length: 5 }, () => ({ cls: 'a', gapPx: 14 }))
        const readings = { offScaleGaps: judgeOffScaleGaps(gaps, scale).length, overflow: 0, crowded: 0 }
        return compareToBaseline(readings, {
          offScaleGapsMax: 4,
          overflowMax: 2,
          crowdedMax: 1,
        }).length === 1
      },
    },
  ]
  const failed = cases.filter((c) => !c.run()).map((c) => c.name)
  return { total: cases.length, passed: cases.length - failed.length, failed }
}

// ── 浏览器探针 ────────────────────────────────────────────────────────────
const PROBE = `(() => {
  const SCALE = ${JSON.stringify(spaceScalePx())}
  const HAIRLINE = ${HAIRLINE_MAX_PX}
  const values = []
  const gaps = []
  let measured = 0
  let overflow = 0
  const overflowSamples = []
  const clsOf = (el) => {
    const c = el.className
    const s = (c && c.baseVal !== undefined ? c.baseVal : c || '').toString()
    return s.slice(0, 60)
  }
  const all = document.querySelectorAll('body *')
  for (const el of all) {
    if (!el.getClientRects || el.getClientRects().length === 0) continue
    measured++
    // 横向溢出（★ 收紧把版面挤破的探针）
    if (el.scrollWidth > el.clientWidth + 1 && el.clientWidth > 0) {
      overflow++
      if (overflowSamples.length < 12) {
        overflowSamples.push({
          cls: clsOf(el),
          tag: el.tagName.toLowerCase(),
          client: el.clientWidth,
          scroll: el.scrollWidth,
        })
      }
    }
    const cs = getComputedStyle(el)
    for (const prop of ['paddingTop','paddingRight','paddingBottom','paddingLeft',
                        'marginTop','marginRight','marginBottom','marginLeft',
                        'rowGap','columnGap']) {
      const raw = cs[prop]
      const v = parseFloat(raw)
      if (!Number.isFinite(v)) continue
      if (String(raw).indexOf('%') >= 0) continue
      values.push({ cls: clsOf(el), prop, px: v })
    }
    // ★★ 兄弟间隙：**rect 实测**（含两侧 margin + 容器 gap + 边框）⇒ 用户真正看到的那段距离
    const kids = el.children
    if (kids.length >= 2) {
      // ★★ 容器用 space-between/around/evenly 时，兄弟之间那段距离是**自由空间**
      //   （由对齐方式插入的），**不是任何声明的间距** ⇒ 测它没有意义（实测第一版踩过：
      //   .page-header-right 间隙 782px —— 那是 space-between 撑出来的）。
      const jc = String(cs.justifyContent || '')
      const ac = String(cs.alignContent || '')
      const spaceDistributed = (jc + ' ' + ac).includes('space-between') ||
        (jc + ' ' + ac).includes('space-around') ||
        (jc + ' ' + ac).includes('space-evenly')
      const rs = []
      for (const k of kids) {
        if (!k.getClientRects || k.getClientRects().length === 0) continue
        const r = k.getBoundingClientRect()
        rs.push({ r, cls: clsOf(k) })
      }
      for (let i = 0; i + 1 < rs.length; i++) {
        const a = rs[i].r, b = rs[i + 1].r
        const dx = Math.max(0, Math.max(a.left, b.left) - Math.min(a.right, b.right))
        const dy = Math.max(0, Math.max(a.top, b.top) - Math.min(a.bottom, b.bottom))
        // ★★ 必须是**同一行或同一列**：另一轴的矩形区间必须**重叠**。
        //   否则会把「同列里隔了 700px 的两个区块」当成 700px 的「节奏间隙」（实测踩过：
        //   第一版探针把 /examples 的 1221px、1057px 全算成间隙 ⇒ 读数毫无意义）。
        const overlapX = Math.min(a.right, b.right) - Math.max(a.left, b.left)
        const overlapY = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)
        // ★★ 必须是**同一行或同一列**：另一轴的矩形区间必须**重叠**。
        //   否则会把「同列里隔了 700px 的两个区块」当成 700px 的「节奏间隙」（实测踩过：
        //   第一版探针把 /examples 的 1221px、1057px 全算成间隙 ⇒ 读数毫无意义）。
        // ★★ 行向容器**只量水平**、列向容器**只量垂直** ——
        //   否则 flex-wrap 换行后的「行间距离」会被当成节奏间隙（实测踩过：
        //   .family-quick-row（flex + wrap）间隙 20px —— 那是两行之间的距离，不是间距）。
        const flexDir = String(cs.flexDirection || '')
        const disp = String(cs.display || '')
        const isGrid = disp.includes('grid')
        const rowDir = flexDir.startsWith('row')
        const colDir = flexDir.startsWith('column')
        let gapPx = null
        if (!isGrid && rowDir) {
          if (dx > 0 && overlapY > 0) gapPx = dx
        } else if (!isGrid && colDir) {
          if (dy > 0 && overlapX > 0) gapPx = dy
        } else {
          if (dx > 0 && overlapY > 0) gapPx = dx          // 同一行，左右分开
          else if (dy > 0 && overlapX > 0) gapPx = dy     // 同一列，上下分开
        }
        if (gapPx === null) continue
        // ★ space-* 分布的容器：那段距离是自由空间 ⇒ 不测
        if (spaceDistributed) continue
        gaps.push({ cls: rs[i + 1].cls, gapPx: Math.round(gapPx * 100) / 100 })
      }
    }
  }
  return { scale: SCALE, values, gaps, measured, overflow, overflowSamples }
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'ss-runtime-'))
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

const scale = spaceScalePx()
const scaleBottom = scale.length ? scale[0] : 4
const summary = {
  pages: [],
  errors: report.errors,
  scale,
  offScaleGaps: [],
  offScaleValues: [],
  overflow: 0,
  measured: 0,
  gapCount: 0,
  crowded: 0,
  layoutDistances: 0,
}
for (const p of report.pages) {
  for (const theme of ['light', 'dark']) {
    const t = p.themes[theme]
    if (!t) continue
    summary.measured += t.measured
    summary.overflow += t.overflow
    summary.gapCount += t.gaps.length
    const offGaps = judgeOffScaleGaps(t.gaps, scale)
    const offValues = judgeOffScaleValues(t.values, scale)
    const crowded = judgeCrowdedGaps(t.gaps, scaleBottom)
    summary.crowded += crowded.length
    summary.layoutDistances += countLayoutDistances(t.gaps, scale)
    for (const o of offGaps) summary.offScaleGaps.push({ page: p.page, theme, ...o })
    for (const o of offValues) summary.offScaleValues.push({ page: p.page, theme, ...o })
    summary.pages.push({
      page: p.page,
      theme,
      measured: t.measured,
      gaps: t.gaps.length,
      offScaleGaps: offGaps.length,
      crowded: crowded.length,
      overflow: t.overflow,
      overflowSamples: t.overflowSamples,
    })
  }
}

const readings = {
  offScaleGaps: summary.offScaleGaps.length,
  overflow: summary.overflow,
  crowded: summary.crowded,
}

const baseline = existsSync(BASELINE) ? JSON.parse(readFileSync(BASELINE, 'utf8')) : null
// ★★★ **基线必须【可用】才有资格判定** —— 缺任一上限 ⇒ ⇒ 棘轮没有门槛 ⇒ ⇒ 这道尺此刻等于没设门槛。
// ★ 根因：★ 兜底写的是 `?? Infinity` ⇒ ⇒ 基线一空（★ 缺失或被掏空成 `{}`）⇒ ⇒ **上限直接消失**
//   ⇒ ⇒ 任何读数都「不超」⇒ ⇒ **假绿**。
// ★★★ 另一条同源的：★ 「没有基线 ⇒ 只测量、不判定」那条**消息是诚实的**，★ **但退出码是 0**
//   ⇒ ⇒ 门只读退出码 ⇒ ⇒ **消息说「不是通过」而退出码说「通过」** ⇒ ⇒ 仍是假绿。
const baselineUsable =
  !!baseline &&
  Number.isFinite(baseline.offScaleGapsMax) &&
  Number.isFinite(baseline.overflowMax) &&
  Number.isFinite(baseline.crowdedMax)
// ★★★ **量具的适用范围必须与基线一致** —— 否则拿「1 页」的读数去比「9 页」的基线，
//   得到的是**假绿**（实测踩过：变异臂只跑 1 页时，读数 0 < 基线 2 ⇒ 注入的离阶间隙被判绿）。
//   ⇒ 页面集不一致 ⇒ **只测量、不判定**，并把这件事说清楚。
const basePages = baseline?.scope?.pages ?? null
const pagesMatch =
  !baseline ||
  !basePages ||
  basePages.length === pages.length &&
    basePages.every((p, i) => p === pages[i])
const regressions =
  // ★ 只在【基线可用】时才比 ⇒ ⇒ 「上限消失 ⇒ 任何读数都不超」这条路被彻底堵死。
  baselineUsable && pagesMatch
    ? compareToBaseline(readings, {
        // ★★ 这三个 `?? Infinity` 是**历史兜底**：★ 在 `baselineUsable` 之后它**永远不生效**
        //   （★ 缺一个上限 ⇒ `baselineUsable` 就是 false ⇒ ⇒ 压根不进这个分支）⇒ ⇒ ★ 保留它只为类型完整。
        offScaleGapsMax: baseline.offScaleGapsMax ?? Infinity,
        overflowMax: baseline.overflowMax ?? Infinity,
        crowdedMax: baseline.crowdedMax ?? Infinity,
      })
    : []

const jsonOut = argv.find((a) => a.startsWith('--json='))
if (jsonOut) writeFileSync(jsonOut.slice(7), JSON.stringify({ summary, readings, baseline, regressions }, null, 2))

console.log(`间距阶：${scale.join('/')}`)
console.log(
  `读数：渲染元素 ${summary.measured} 个 · 兄弟间隙 ${summary.gapCount} 个（离阶 ${readings.offScaleGaps} · 过挤 ${readings.crowded} · 布局距离 ${summary.layoutDistances}（>阶顶，只统计不判））· 横向溢出 ${readings.overflow} · 计算值离阶 ${summary.offScaleValues.length}（**只报告**：auto / % 在运行时层不可区分）`,
)
for (const s of summary.pages) {
  console.log(
    `  ${s.page.padEnd(18)} ${s.theme.padEnd(5)} 元素 ${String(s.measured).padStart(4)} · 间隙 ${String(s.gaps).padStart(4)} · 离阶 ${String(s.offScaleGaps).padStart(3)} · 过挤 ${String(s.crowded).padStart(3)} · 溢出 ${s.overflow}`,
  )
}
if (summary.offScaleGaps.length) {
  console.log('\n离阶的兄弟间隙（去重后）：')
  const seen = new Set()
  for (const o of summary.offScaleGaps) {
    const k = `${o.cls}|${o.gapPx}`
    if (seen.has(k)) continue
    seen.add(k)
    console.log(`  ${o.page} [${o.theme}] .${o.cls.split(/\s+/)[0]} 间隙 ${o.gapPx}px`)
  }
}
if (report.errors.length) {
  console.log('\n⚠️ 采集失败的页面：')
  for (const e of report.errors) console.log(`  ${e.page}: ${e.error}`)
}
if (!baseline) {
  console.log('\n⏭ 没有运行时基线 ⇒ 本次**只测量、不判定**（不是「通过」）。')
  console.log('   用 --json 落盘后手工核对，再写 scripts/design-spacing-scale-runtime-baseline.json。')
} else if (!pagesMatch) {
  console.log(
    `\n⏭ 页面集与基线不一致（基线 ${basePages.length} 页 / 本次 ${pages.length} 页）⇒ 本次**只测量、不判定**。`,
  )
  console.log('   ★ 拿小样本的读数去比大基线会得到**假绿** ⇒ 宁可「不判定」，也不给一个假的通过。')
}
// ★★★ **「不判定」必须【红】** —— 消息说「不是通过」⇒ ⇒ 而**退出码说「通过」**⇒ ⇒ 门只读退出码 ⇒ ⇒ 仍是假绿。
if (!baselineUsable) {
  console.error('\n❌ 基线缺失或非法：scripts/design-spacing-scale-runtime-baseline.json')
  console.error('   ⇒ 棘轮没有上限（★ 兜底 `?? Infinity` ⇒ 上限直接消失）⇒ ⇒ 这道尺此刻等于没设门槛 ⇒ ⇒ 必须红。')
  console.error('   修法：node scripts/design-spacing-scale-runtime-check.mjs --json=out.json ⇒ 核对后写回基线。')
} else if (!pagesMatch) {
  console.error('\n❌ 页面集与基线不一致 ⇒ 本次**不判定**⇒ ⇒ 不是「通过」⇒ ⇒ 必须红。')
  console.error('   ★ 拿小样本的读数去比大基线会得到**假绿**⇒ ⇒ 宁可红，也不要一个假的通过。')
}
if (regressions.length) {
  console.error('\nFAIL: 运行时回归。')
  console.error('  ' + regressions.join('\n  '))
}
process.exit(regressions.length || !baselineUsable || !pagesMatch ? 1 : 0)
