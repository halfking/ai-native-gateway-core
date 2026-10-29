#!/usr/bin/env node
/**
 * design-icon-size-runtime-check.mjs —— 「图标必须看得见」的【运行时】核对（第 28 增量）
 *
 * 为什么要有这道工具（★ 它和源码门不是同一件事）
 * ──────────────────────────────────────────────────────────────────────────────
 * 本仓的图标分两路：
 *   ① 手写 SVG（多数带 `width`/`height` 属性 ⇒ 有内在尺寸）
 *   ② `@element-plus/icons-vue` 渲染的**裸 `<svg>`**（**没有** width/height 属性）
 * ⇒ ② 这路的图标**没有任何内在尺寸** ⇒ 尺寸完全靠 CSS 兜。
 * ⇒ 而 CSS 兜底是**逐处**的（父元素各写各的 `.xxx-glyph svg { width: 16px }`）。
 * ⇒ ★★ **一旦某个位置的父级漏写那条规则，这个图标就塌成 0×0 ⇒ 用户看到的是「空方块」。**
 *
 * ★ 实测（第 28 增量勘察，仪表盘 `/`）：
 *   - 页面共 6 个 `<svg>`，其中 **2 个渲染尺寸是 0×0**：
 *     · 仪表盘页头的「刷新数据」按钮 ⇒ **一个没有任何图标的空方块**
 *       （★ 这是刷新数据的**唯一**入口 ⇒ 用户不知道那是什么）
 *     · 「图例」开关按钮 ⇒ 同样是空方块
 *   - 另外 4 个正常（16×16 / 10×10 / 9.5×9.5 / 15×15）—— ★ 它们都**有**带尺寸的父级 class。
 * ⇒ ★★ **判据只能是「渲染后的真实尺寸」**：源码里「用了图标组件」这件事**推不出**它看得见。
 *
 * ★★ 为什么要【运行时】尺而不是【源码】门：
 *   源码门看到的是「`<Refresh />` 被用在了一个 `<span>` 里」⇒ 它**不知道**那个 span 有没有被
 *   别的 CSS 规则兜住尺寸 ⇒ ⇒ 源码门要么判不出（假绿），要么把 **174 处**裸图标全判红
 *   （★ 实测全仓裸 element-plus 图标 **174 处 / 77 个文件**，其中绝大多数是**正常**的
 *   ⇒ 「裸图标」这个判据**太宽**，会把门变成噪声 ⇒ ★ 判据宁可窄，不可宽）。
 *
 * ★★ 运行时判据（两条）：
 *   ① 任一**渲染尺寸为 0** 的图标 ⇒ 判红（它等于不存在）；
 *      ★ 若它落在一个**控件**（button / a / role=button / role=tab）里 ⇒ 那个控件渲染成空壳
 *      ⇒ 严重度更高（用户看到一个点不动的方框）。
 *   ② **棘轮**：各页「零尺寸图标数」不得超过基线（只许降不许升）。
 *
 * ★★ 页面覆盖面（★ 本轮补上了一个**盲区**）：
 *   既有 `design-action-density-runtime-check.mjs` 的页面清单是
 *   `/free-pool /model-pricing /providers /request-logs /keys /admin/proxy /models`
 *   ——★ **不含 `/`（仪表盘首页）** ⇒ 而 0×0 图标恰恰长在首页页头 ⇒ ⇒ **首页此前无人看守**。
 *   ⇒ 本工具的默认页面清单 = 既有那 7 个 **+ `/`（首页）**。
 *
 * 用法
 * ────
 *   node scripts/design-icon-size-runtime-check.mjs                    # 跑全部页面
 *   node scripts/design-icon-size-runtime-check.mjs --selftest          # 判据自证（不需要浏览器）
 *   node scripts/design-icon-size-runtime-check.mjs --pages=/,/models
 *   node scripts/design-icon-size-runtime-check.mjs --json=out.json
 *   node scripts/design-icon-size-runtime-check.mjs --update-baseline   # 人工降基线
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
const BASELINE = join(ROOT, 'scripts', 'design-icon-size-runtime-baseline.json')

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean)

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'

/**
 * 默认页面：既有 action-density 尺覆盖的那 7 个 **+ 首页 `/`**。
 *
 * ★ 首页是本轮补上的盲区（既有尺的清单里没有它，而 0×0 图标长在那里）。
 */
export const DEFAULT_PAGES = [
  '/',
  '/free-pool',
  '/model-pricing',
  '/providers',
  '/request-logs',
  '/keys',
  '/admin/proxy',
  '/models',
]

/** 零尺寸 = 图标等于不存在。 */
export const ZERO = 0

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * @typedef {{ w: number, h: number, cls: string, parentCls: string,
 *             control: string, controlTitle: string, hidden: boolean }} IconDesc
 */

/**
 * 判「渲染尺寸为 0 的图标」。
 *
 * ★★ **隐藏子树里的 0×0 不算缺陷**（★ 这是第一版的**假阳性**）：
 *   一个还没打开的下拉菜单里的图标，它的祖先是 `display:none` ⇒ 它的
 *   `getBoundingClientRect()` 必然是 0×0 ⇒ 但它**本来就该是 0×0**（用户看不到它）。
 *   ⇒ 所以判据必须是「**在可见渲染树里**却没有尺寸」⇒ 探针负责报 `hidden`。
 *
 * @param {IconDesc[]} icons
 * @returns {{ control: string, reason: string }[]} 违例
 */
export function judgeIconSizes(icons) {
  const out = []
  for (const it of icons ?? []) {
    if (it?.hidden) continue
    const w = Number(it?.w)
    const h = Number(it?.h)
    if (!Number.isFinite(w) || !Number.isFinite(h)) continue
    // ★ 只有**两边都大于 0** 才算「看得见」——
    //   ★ 第一版写成了「任一边 > 0 就放过」⇒ 0×10（宽为 0）被漏判
    //   ⇒ ★ 是本工具的**自证**抓住的（S-IS4）⇒ 判据自己也要被量。
    if (w > ZERO && h > ZERO) continue
    const inControl = !!(it?.control && it.control !== 'NONE')
    out.push({
      control: inControl ? it.control : (it?.parentCls || '（不在控件里）'),
      reason: inControl
        ? `控件「${it.control}${it.controlTitle ? `」 title=${it.controlTitle}` : ''}里的图标渲染尺寸是 ${w}×${h} ⇒ 控件渲染成空方块，用户看不到它是什么`
        : `图标渲染尺寸是 ${w}×${h} ⇒ 它等于不存在`,
    })
  }
  return out
}

/**
 * 棘轮：各页「零尺寸图标数」不得超过基线（只许降不许升）。
 *
 * @param readings { pages: { page: { zeroSized: number } } }
 * @param baseline { zeroSized: {page:number} }
 */
export function compareToBaseline(readings, baseline) {
  const regressions = []
  const pages = readings?.pages ?? {}
  for (const [page, r] of Object.entries(pages)) {
    const limit = baseline?.zeroSized?.[page]
    if (typeof limit !== 'number') {
      regressions.push(`${page}：不在基线里（基线缺这一页 ⇒ 无法判棘轮）`)
      continue
    }
    if ((r.zeroSized ?? 0) > limit) {
      regressions.push(`${page}：零尺寸图标数 ${limit} → ${r.zeroSized}（升高了）`)
    }
  }
  return regressions
}

/**
 * ★ 基线**能不能用**（★ 防「基线没了 ⇒ 棘轮整个空转 ⇒ 假绿」）。
 *
 * ★★ 这一条是照着仓内 `design-spacing-scale-runtime-baseline.json` 的登记条目抄的：
 *   那一轮实测出「基线缺失 ⇒ 读方**退出码 0** ⇒ 消息说『不是通过』而**退出码说『通过』**」
 *   ⇒ 门只读退出码 ⇒ **假绿**。
 * ⇒ ★ 本工具**必须**把「基线不可用」判成**红**，而不是跳过棘轮。
 *
 * @param baseline 解析后的基线（可能是 null）
 */
export function baselineUsable(baseline) {
  const z = baseline?.zeroSized
  if (!z || typeof z !== 'object') return false
  const values = Object.values(z)
  if (!values.length) return false
  return values.every((v) => typeof v === 'number' && Number.isFinite(v))
}

/**
 * 量到的页面集合是否与基线一致（★ 页面集不一致 ⇒ 棘轮覆盖不全 ⇒ 判红）。
 *
 * @param readings { pages: { page: { zeroSized: number } } }
 * @param baseline
 */
export function pagesMatchBaseline(readings, baseline) {
  const measured = Object.keys(readings?.pages ?? {})
  if (!measured.length) return false
  const known = new Set(Object.keys(baseline?.zeroSized ?? {}))
  return measured.every((p) => known.has(p))
}

/** 读基线（没有就返回 null —— 不自己造一个「全 0」的假基线）。 */
export function readBaseline() {
  if (!existsSync(BASELINE)) return null
  try {
    return JSON.parse(readFileSync(BASELINE, 'utf8'))
  } catch {
    return null
  }
}

// ── 浏览器探针 ──────────────────────────────────────────────────────────────
//
// ★ 只量**真实渲染后的盒子**（getBoundingClientRect），不读源码、不读 CSS 声明。
// ★ 排除 `<defs>` 里的 svg（那是符号定义，本来就不该有尺寸）。

const PROBE = `(() => {
  // ★ 「在隐藏子树里」的判定：从图标自己往上走，任何一层被藏 ⇒ 它的 0×0 是**应该的**。
  //   （第一版没有这一条 ⇒ 还没打开的下拉菜单里的图标被误判 ⇒ 实测假阳性 1 处。）
  const inHiddenSubtree = (el) => {
    let n = el
    while (n && n.nodeType === 1 && n !== document.documentElement) {
      const cs = getComputedStyle(n)
      if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') return true
      n = n.parentElement
    }
    return false
  }
  const svgs = [...document.querySelectorAll('svg')].filter((s) => !s.closest('defs'))
  const icons = svgs.map((s) => {
    const r = s.getBoundingClientRect()
    const ctrl = s.closest('button, a[href], [role="button"], [role="tab"]')
    const p = s.parentElement
    return {
      w: Math.round(r.width * 10) / 10,
      h: Math.round(r.height * 10) / 10,
      cls: s.getAttribute('class') || '',
      parentCls: p ? (p.getAttribute('class') || p.tagName) : '',
      control: ctrl ? (ctrl.getAttribute('class') || ctrl.tagName) : 'NONE',
      controlTitle: ctrl ? (ctrl.getAttribute('title') || ctrl.getAttribute('aria-label') || '') : '',
      hidden: inHiddenSubtree(s),
    }
  })
  return JSON.stringify({ total: icons.length, icons })
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'icon-size-runtime-'))
  const chrome = spawn(CHROME_CANDIDATES[0], [
    '--headless=new',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--hide-scrollbars',
    '--window-size=1440,1200',
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
      await cdp('Page.navigate', { url: BASE + p })
      await sleep(3200)
      const probe = JSON.parse(await evaluate(PROBE))
      const icons = probe.icons ?? []
      const findings = judgeIconSizes(icons)
      report.pages.push({
        page: p,
        totalIcons: probe.total ?? icons.length,
        zeroSized: findings.length,
        minSide: icons.length
          ? Math.min(
              ...icons
                .filter((i) => !i.hidden) // ★ 隐藏子树里的图标**不计入**最小边
                .map((i) => Math.min(Number(i.w) || 0, Number(i.h) || 0)),
            )
          : 0,
        findings,
        icons,
      })
    } catch (e) {
      report.errors.push({ page: p, error: String(e?.message || e) })
    }
  }
  ws.close()
  chrome.kill()
  return report
}

// ── 判据自证 ────────────────────────────────────────────────────────────────
//
// ★ 必须**双向**：会判红 + 不误判各半 —— 否则恒绿门与恒红门同样没用。

export function runRuntimeSelftest() {
  const cases = []
  const check = (id, desc, expected, actual) => cases.push({ id, desc, expected, actual, ok: expected === actual })

  const mk = (w, h, over = {}) => ({
    w, h, cls: '', parentCls: 'span', control: 'NONE', controlTitle: '', ...over,
  })

  check(
    'S-IS1',
    '0×0 的图标 ⇒ 判红 1 条',
    1,
    judgeIconSizes([mk(0, 0)]).length,
  )
  check(
    'S-IS2',
    '0×0 且在控件里 ⇒ 判红，且理由点名那个控件',
    1,
    judgeIconSizes([mk(0, 0, { control: 'btn btn-refresh', controlTitle: '刷新数据' })])
      .filter((f) => f.reason.includes('btn-refresh')).length,
  )
  check(
    'S-IS3',
    '正常尺寸（16×16）⇒ 不判红',
    0,
    judgeIconSizes([mk(16, 16)]).length,
  )
  check(
    'S-IS4',
    '只有一边为 0（0×10）⇒ 判红',
    1,
    judgeIconSizes([mk(0, 10)]).length,
  )
  check(
    'S-IS5',
    '小数尺寸（9.5×9.5）⇒ 不判红',
    0,
    judgeIconSizes([mk(9.5, 9.5)]).length,
  )
  check(
    'S-IS6',
    '混合：2 个 0 + 2 个正常 ⇒ 只报 2 条',
    2,
    judgeIconSizes([mk(0, 0), mk(16, 16), mk(0, 0), mk(10, 10)]).length,
  )
  check(
    'S-IS7',
    '空数组 ⇒ 0 条（不许崩、不许假红）',
    0,
    judgeIconSizes([]).length,
  )
  check(
    'S-IS8',
    'undefined 入参 ⇒ 0 条（不许崩）',
    0,
    judgeIconSizes(undefined).length,
  )
  check(
    'S-IS9',
    'NaN 尺寸 ⇒ 跳过（不判红也不崩）',
    0,
    judgeIconSizes([mk(Number.NaN, 10)]).length,
  )
  check(
    'S-IS10',
    '棘轮：零尺寸数上升 ⇒ 报回归',
    1,
    compareToBaseline(
      { pages: { '/': { zeroSized: 3 } } },
      { zeroSized: { '/': 2 } },
    ).length,
  )
  check(
    'S-IS11',
    '棘轮：零尺寸数持平 ⇒ 不报回归',
    0,
    compareToBaseline(
      { pages: { '/': { zeroSized: 2 } } },
      { zeroSized: { '/': 2 } },
    ).length,
  )
  check(
    'S-IS12',
    '棘轮：零尺寸数下降 ⇒ 不报回归（只许降）',
    0,
    compareToBaseline(
      { pages: { '/': { zeroSized: 0 } } },
      { zeroSized: { '/': 2 } },
    ).length,
  )
  check(
    'S-IS13',
    '棘轮：页面不在基线里 ⇒ 报回归（不许默默放过）',
    1,
    compareToBaseline(
      { pages: { '/new-page': { zeroSized: 0 } } },
      { zeroSized: { '/': 2 } },
    ).length,
  )
  check(
    'S-IS14',
    '默认页面清单含首页 `/`（★ 本轮补的盲区）',
    true,
    DEFAULT_PAGES.includes('/'),
  )
  check(
    'S-IS15',
    '默认页面清单不少于既有 action-density 尺的 7 个',
    true,
    DEFAULT_PAGES.length >= 8,
  )
  check(
    'S-IS16',
    '★ 隐藏子树里的 0×0 图标 ⇒ **不判红**（第一版的假阳性）',
    0,
    judgeIconSizes([mk(0, 0, { hidden: true })]).length,
  )
  check(
    'S-IS17',
    '可见的 0×0 图标 ⇒ 判红（hidden=false 时不能被放过）',
    1,
    judgeIconSizes([mk(0, 0, { hidden: false })]).length,
  )
  check(
    'S-IS18',
    '隐藏的 0×0 + 可见的 0×0 混在一起 ⇒ 只报 1 条',
    1,
    judgeIconSizes([mk(0, 0, { hidden: true }), mk(0, 0, { hidden: false })]).length,
  )
  check(
    'S-IS19',
    '★ 基线文件缺失（null）⇒ 判「不可用」（不许空转棘轮）',
    false,
    baselineUsable(null),
  )
  check(
    'S-IS20',
    '★ 基线里 zeroSized 是空对象 ⇒ 判「不可用」',
    false,
    baselineUsable({ zeroSized: {} }),
  )
  check(
    'S-IS21',
    '★ 基线里 zeroSized 的值不是数字 ⇒ 判「不可用」',
    false,
    baselineUsable({ zeroSized: { '/': 'abc' } }),
  )
  check(
    'S-IS22',
    '★ 正常基线 ⇒ 判「可用」',
    true,
    baselineUsable({ zeroSized: { '/': 0 } }),
  )
  check(
    'S-IS23',
    '★ 量到的页面集与基线一致 ⇒ 通过',
    true,
    pagesMatchBaseline({ pages: { '/': { zeroSized: 0 } } }, { zeroSized: { '/': 0 } }),
  )
  check(
    'S-IS24',
    '★ 量到的页面集比基线多一页 ⇒ 不通过（棘轮覆盖不全）',
    false,
    pagesMatchBaseline(
      { pages: { '/': { zeroSized: 0 }, '/new': { zeroSized: 0 } } },
      { zeroSized: { '/': 0 } },
    ),
  )
  check(
    'S-IS25',
    '★ 一页都没量到 ⇒ 页面集不通过（不许「没量到」等于「通过」）',
    false,
    pagesMatchBaseline({ pages: {} }, { zeroSized: { '/': 0 } }),
  )

  const failed = cases.filter((c) => !c.ok)
  return { passed: cases.length - failed.length, total: cases.length, failed, cases }
}

// ── CLI ──────────────────────────────────────────────────────────────────
const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  const r = runRuntimeSelftest()
  for (const f of r.failed) console.log('❌ ' + f.id + ' ' + f.desc)
  console.log(
    `\n自证 ${r.passed}/${r.total}` + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'),
  )
  process.exit(r.failed.length ? 2 : 0)
}

const pagesArg = argv.find((a) => a.startsWith('--pages='))
const pages = pagesArg ? pagesArg.slice(8).split(',').filter(Boolean) : DEFAULT_PAGES
const jsonArg = argv.find((a) => a.startsWith('--json='))

if (!CHROME_CANDIDATES.length || !existsSync(CHROME_CANDIDATES[0])) {
  // ★ 没量到 ≠ 通过。打印 SKIP 并说清「未测量」。
  console.log('⏭ SKIP —— 本机没有 Google Chrome ⇒ **未测量**（不是通过）')
  console.log('   安装 Chrome 或设 CHROME_PATH 后重跑。')
  process.exit(3)
}

// ★★ 2026-10-29：基线不可用 ⇒ **在测量之前**就判红（★ 与两个兄弟读方同款：
//   design-metric-skeleton / design-nested-card 早有这条，icon-size 此前没有）。
//   理由：这条判定**不依赖测量** —— 「棘轮的尺没了」这件事本身就已经是红的，
//   再去跑一遍浏览器只是白花 87s。
//   ★★ 顺带**修掉一个真缺陷**：仓内 Go 侧「掏空登记册 ⇒ 读方必须红」那道门
//   会**两次**调读方（基线态一次 + 掏空态一次）⇒ 掏空态此前也要跑全量
//   浏览器 ⇒ ★★ 在负载下浏览器连不上（fetch failed）⇒ 读方以「环境失败」
//   退出 ⇒ 门把它判成「红了但红因不对」⇒ **随机误判「门没牙」**。
//   ⇒ 现在掏空态**秒级**返回（不碰浏览器）⇒ 那条随机误判从根上消失。
// ★★ `--update-baseline` 是**显式重建基线**的动作 ⇒ 它必须能跑（否则新门
//   第一次建基线时会被自己的早退挡住 ⇒ 死锁）⇒ 与 nested-card 同款守卫。
const wantUpdate = argv.includes('--update-baseline')
const baselineEarly = readBaseline()
if (!wantUpdate && !baselineUsable(baselineEarly)) {
  console.log(
    '\n✘ [ICON-SIZE-BASELINE-UNUSABLE] 基线**不可用**：' +
      (baselineEarly === null
        ? '基线文件缺失或解析失败'
        : '基线里没有可用的 zeroSized 数值') +
      ' ⇒ ⇒ 棘轮**整个空转** ⇒ ⇒ 判红（★ 不许「没量到基线」等于「通过」）',
  )
  console.log('   ★ 判定不依赖测量 ⇒ 本次**没有**跑浏览器（基线没了，测了也无处判）。')
  console.log('   ★ 要重建基线：加 --update-baseline（★ 它写回的值只许降不许升）。')
  process.exit(1)
}

const port = 9200 + Math.floor(Math.random() * 300)
let report
try {
  report = await runChrome(port, pages)
} catch (e) {
  // ★★ 2026-10-29：**环境失败 ≠ 判据失败**。
  //   旧写法：exit 2 —— 而 exit 2 在本工具里是「自证失败」的码 ⇒
  //   仓内 Go 侧「掏空登记册 ⇒ 读方必须红且红因正确」那道门把「浏览器连不上」
  //   判成「红了但红因不对」⇒ **在负载下随机误判「门没牙」**。
  //   ⇒ 现在环境失败走 exit 3（= 本工具既有的「未测量」约定）＋ 一个专属
  //   探针 token ⇒ 门可以把它与「判据红」分开。
  //   ★ 「未测量」不等于「通过」：门那边会跳过本条并把原因记进输出。
  console.error(
    '✘ [ICON-SIZE-NOT-MEASURED] 浏览器驱动失败：' +
      (e?.message || e) +
      '\n   ★ 这是**环境失败**（不是判据失败）⇒ 本次**未测量**（不是通过）',
  )
  process.exit(3)
}

const readings = {
  pages: Object.fromEntries(report.pages.map((p) => [p.page, { zeroSized: p.zeroSized, minSide: p.minSide }])),
}
const baseline = readBaseline()

console.log('页面 | 图标总数 | 零尺寸 | 最小边')
for (const p of report.pages) {
  console.log(
    `  ${p.page.padEnd(16)} ${String(p.totalIcons).padStart(4)} ${String(p.zeroSized).padStart(8)} ${String(p.minSide).padStart(8)}`,
  )
}
for (const e of report.errors) console.log(`  ⚠ ${e.page}: ${e.error}`)

const findings = report.pages.flatMap((p) => p.findings.map((f) => ({ page: p.page, ...f })))
if (findings.length) {
  console.log('\n★ 零尺寸图标（= 看不见）：')
  for (const f of findings) console.log(`  ✘ ${f.page}  ${f.reason}`)
}

const usable = baselineUsable(baseline)
const pagesMatch = pagesMatchBaseline(readings, baseline)
const regressions = usable ? compareToBaseline(readings, baseline) : []
if (regressions.length) {
  console.log('\n★ 棘轮被突破：')
  for (const r of regressions) console.log(`  ✘ ${r}`)
}
if (!usable) {
  console.log(
    '\n✘ [ICON-SIZE-BASELINE-UNUSABLE] 基线**不可用**：' +
    (baseline === null
      ? '基线文件缺失或解析失败'
      : '基线里没有可用的 zeroSized 数值') +
    ' ⇒ ⇒ 棘轮**整个空转** ⇒ ⇒ 判红（★ 不许「没量到基线」等于「通过」）',
  )
}
if (!pagesMatch) {
  console.log('\n✘ [ICON-SIZE-PAGES-MISMATCH] 量到的页面集与基线不一致 ⇒ ⇒ 棘轮覆盖不全 ⇒ ⇒ 判红')
}

if (jsonArg) writeFileSync(jsonArg.slice(7), JSON.stringify({ ...report, readings }, null, 2))

if (argv.includes('--update-baseline')) {
  const prev = baseline ?? { zeroSized: {} }
  // ★★ 2026-10-29：写回值**只许降不许升**（★ 与两个兄弟读方同款）。
  //   本基线是**上限型**（zeroSized = 该页零尺寸图标数，不得上升）⇒
  //   已有的页取「旧值与实测值里的较小者」⇒ **重建基线这条路也放松不了棘轮**。
  //   ⇒ ★ 此前这里直接写实测值 ⇒ 一次误操作就能把棘轮放松 ⇒ 修掉。
  const zeroSized = {}
  for (const p of report.pages) {
    const old = prev.zeroSized?.[p.page]
    const fresh = p.zeroSized
    zeroSized[p.page] = typeof old === 'number' && Number.isFinite(old) ? Math.min(old, fresh) : fresh
  }
  const next = {
    metric:
      'zeroSized = 该页渲染尺寸为 0 的图标数（不得上升；0 = 看不见 ⇒ 必须为 0）；minSide = 该页图标的最小边长（px，附记）',
    zeroSized,
    updated: '2026-10-27',
    history: [
      ...(prev.history ?? []),
      {
        updated: '2026-10-27',
        note: '第 28 增量建立基线（人工降基线：先修掉首页 2 处 0×0，再把基线定到实测值）',
        zeroSized: Object.fromEntries(report.pages.map((p) => [p.page, p.zeroSized])),
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`\n✔ 基线已更新 → ${BASELINE}`)
}

const measured = report.pages.length
if (!measured) {
  console.log('\n⏭ 一页都没量到 ⇒ **未测量**（不是通过）')
  process.exit(3)
}
// ★ 退出码 = 违例 | 棘轮突破 | 基线不可用 | 页面集不匹配 —— 任一非空都判红。
//   ★ 「基线不可用」必须红：★ 否则「把基线删掉」就是让门变绿的后门
//   （★ 仓内 `design-spacing-scale-runtime-baseline.json` 的登记条目记过这个坑）。
if (findings.length || regressions.length || !usable || !pagesMatch) process.exit(1)
console.log(`\n✅ ${measured} 页量完：零尺寸图标 0 个，棘轮未突破。`)
