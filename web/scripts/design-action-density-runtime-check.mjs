#!/usr/bin/env node
/**
 * design-action-density-runtime-check.mjs —— 「动作区必须可扫读」的【运行时】核对（第 25 增量）
 *
 * 为什么要有这道工具（它和 design-action-density-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 * 源码门读的是**源码里声明的动作**；本工具读的是**浏览器里真实渲染出来的动作**。
 *
 * ★ 源码门看不见的三件事：
 *   ① **组件内部渲染出来的按钮** —— 溢出菜单的触发器在组件里，源码门只能靠
 *      「会渲染动作的组件」白名单登记来折算（1 个触发器）；
 *      运行时尺则是**直接数 DOM 里看得见的按钮** ⇒ 两把尺互为对拍。
 *   ② **运行时才存在的动作**（v-if 的另一分支、v-show 切开的元素）。
 *   ③ ★★ **最要紧的一条：折叠进去的动作还够不够得着** ——
 *      源码门只能证明「按钮还在源码里」，**证明不了**用户点得开它。
 *      ⇒ 本工具会**真的点开「更多」**，逐条核对菜单项有标签、能点到。
 *
 * ★★ 运行时判据（三条）：
 *   ① 任一动作区里**可见**动作数 > 4 ⇒ 判红；
 *   ② 每个「更多」菜单：项数 ≥ 1 且每项有非空文案 ⇒ 否则判红（够不着）；
 *   ③ **可达动作总数**（可见 + 菜单项）不得低于基线 ⇒ 防止「把按钮删了来让门变绿」。
 *
 * ★ 两把尺的**作用域边界**（必须写清楚，否则读数会被误读）：
 *   · 源码门：读**整棵源码** ⇒ 覆盖**关闭状态的弹窗底栏**（如定价页的编辑弹窗）；
 *   · 运行时尺：读**页面加载那一刻看得见的东西** ⇒ 看不到没打开的弹窗。
 *   ⇒ 两把尺**互补**，谁也替代不了谁。
 *
 * ★★ 运行时的一个关键修正：**溢出菜单的触发器在 DOM 里的父元素是组件根节点**
 *   （`.action-overflow`），不是宿主动作区 ⇒ 若照直分组，触发器会**从宿主动作区里消失**
 *   ⇒ 宿主区的可见数被少算 1 个。
 *   ⇒ 探针在分组时**穿透** `.action-overflow` 这一层，把触发器算回宿主区。
 *
 * 用法
 * ────
 *   node scripts/design-action-density-runtime-check.mjs            # 跑全部页面
 *   node scripts/design-action-density-runtime-check.mjs --selftest # 判据自证（不需要浏览器）
 *   node scripts/design-action-density-runtime-check.mjs --pages=/free-pool,/model-pricing
 *   node scripts/design-action-density-runtime-check.mjs --json=out.json
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
const BASELINE = join(ROOT, 'scripts', 'design-action-density-runtime-baseline.json')

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean)

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'

/** 默认页面：4 个整改点所在的页面 + 几个同类页做对照。 */
const DEFAULT_PAGES = [
  '/free-pool',
  '/model-pricing',
  '/providers',
  '/request-logs',
  '/keys',
  '/admin/proxy',
  '/models',
]

/** 一个动作区里允许的可见动作数（与源码门同一常量）。 */
export const MAX_VISIBLE_ACTIONS = 4

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * @typedef {{ cls: string, tag: string, n: number, labels: string[] }} ZoneDesc
 * @typedef {{ zone: string, itemCount: number, unlabeled: number }} TriggerDesc
 */

/** 判「任一动作区可见动作数超阈值」。 */
export function judgeZones(zones, max = MAX_VISIBLE_ACTIONS) {
  return (zones ?? [])
    .filter((z) => (z?.n ?? 0) > max)
    .map((z) => ({
      zone: `${z.tag}${z.cls ? '.' + z.cls.split(/\s+/)[0] : ''}`,
      n: z.n,
      labels: z.labels,
      reason: `动作区里同时可见 ${z.n} 个动作（上限 ${max}）⇒ 眼睛必须扫一遍才能定位`,
    }))
}

/**
 * 判「折叠进去的动作还够不够得着」。
 *
 * ★ 两个失败形态各自独立：
 *   ① 菜单里一项都没有（点开是空的 ⇒ 动作被吞了）；
 *   ② 菜单项没有文案（点开是一排空白 ⇒ 用户不知道那是什么）。
 */
export function judgeCoverage(triggers) {
  const out = []
  for (const t of triggers ?? []) {
    if ((t?.itemCount ?? 0) < 1) {
      out.push({
        zone: t?.zone ?? '?',
        reason: `「更多」菜单里一项都没有 ⇒ 折叠进去的动作点不到`,
      })
      continue
    }
    if ((t?.unlabeled ?? 0) > 0) {
      out.push({
        zone: t?.zone ?? '?',
        reason: `「更多」菜单里有 ${t.unlabeled} 项没有文案 ⇒ 用户不知道那是什么`,
      })
    }
  }
  return out
}

/**
 * 对拍基线（两条棘轮）：
 *   ① **可见动作数上限不得上升**（新页面不得再出现一排药丸）；
 *   ② **可达动作总数不得下降**（防「把按钮删了来让门变绿」）。
 *
 * @param readings { pages: { page: { maxVisible: number, reachable: number } } }
 * @param baseline { maxVisible: {page:number}, reachable: {page:number} }
 */
export function compareToBaseline(readings, baseline) {
  const regressions = []
  const pages = readings?.pages ?? {}
  for (const [page, r] of Object.entries(pages)) {
    const bMax = baseline?.maxVisible?.[page]
    if (typeof bMax === 'number' && (r.maxVisible ?? 0) > bMax) {
      regressions.push(`${page}：可见动作数上限 ${bMax} → ${r.maxVisible}（升高了）`)
    }
    const bReach = baseline?.reachable?.[page]
    if (typeof bReach === 'number' && (r.reachable ?? 0) < bReach) {
      regressions.push(`${page}：可达动作总数 ${bReach} → ${r.reachable}（少了 ⇒ 有动作被删掉或够不着了）`)
    }
  }
  return regressions
}

// ── 自证 ──────────────────────────────────────────────────────────────────

export function runRuntimeSelftest() {
  const cases = []
  const add = (name, ok) => cases.push({ name, ok: !!ok })

  add('动作区 5 个可见动作 ⇒ 判红', judgeZones([{ cls: 'bar', tag: 'div', n: 5, labels: [] }]).length === 1)
  add('动作区 4 个可见动作 ⇒ 判绿', judgeZones([{ cls: 'bar', tag: 'div', n: 4, labels: [] }]).length === 0)
  add('★ 对照：4 与 5 之间是判据的分界', (() => {
    const four = judgeZones([{ cls: 'b', tag: 'div', n: 4, labels: [] }]).length
    const five = judgeZones([{ cls: 'b', tag: 'div', n: 5, labels: [] }]).length
    return four === 0 && five === 1
  })())
  add('「更多」菜单为空 ⇒ 判红（够不着）', judgeCoverage([{ zone: 'z', itemCount: 0, unlabeled: 0 }]).length === 1)
  add('★ 「更多」菜单项没文案 ⇒ 判红', judgeCoverage([{ zone: 'z', itemCount: 2, unlabeled: 1 }]).length === 1)
  add('★ 对照：「更多」菜单项都有文案 ⇒ 判绿', judgeCoverage([{ zone: 'z', itemCount: 5, unlabeled: 0 }]).length === 0)
  add('可见动作数上限升高 ⇒ 报回归', compareToBaseline(
    { pages: { '/p': { maxVisible: 6, reachable: 9 } } },
    { maxVisible: { '/p': 4 }, reachable: { '/p': 9 } },
  ).length === 1)
  add('★ 可达动作总数下降 ⇒ 报回归（防「删按钮让门变绿」）', compareToBaseline(
    { pages: { '/p': { maxVisible: 3, reachable: 7 } } },
    { maxVisible: { '/p': 4 }, reachable: { '/p': 9 } },
  ).length === 1)
  add('★ 可达动作总数不变 ⇒ 不报回归', compareToBaseline(
    { pages: { '/p': { maxVisible: 3, reachable: 9 } } },
    { maxVisible: { '/p': 4 }, reachable: { '/p': 9 } },
  ).length === 0)
  add('★ 基线里没有的页面不报回归（新页面不受旧基线约束）', compareToBaseline(
    { pages: { '/new': { maxVisible: 7, reachable: 1 } } },
    { maxVisible: { '/p': 4 }, reachable: { '/p': 9 } },
  ).length === 0)
  add('判据不含全局状态（同一输入两次结果一致）', (() => {
    const z = [{ cls: 'b', tag: 'div', n: 5, labels: ['x'] }]
    return JSON.stringify(judgeZones(z)) === JSON.stringify(judgeZones(z))
  })())

  const failed = cases.filter((c) => !c.ok).map((c) => c.name)
  return { passed: cases.length - failed.length, total: cases.length, failed }
}

// ── 浏览器探针 ────────────────────────────────────────────────────────────

/**
 * 探针（跑在浏览器里）：把 DOM 翻译成描述符。
 *
 * ★ 「可见」的判定用 **rect + computed style**，不是 offsetParent：
 *   `position: fixed` 的元素 offsetParent 为 null，但仍可见。
 */
const PROBE = `(() => {
  const isVisible = (el) => {
    if (!el) return false
    const r = el.getBoundingClientRect()
    if (r.width <= 0 || r.height <= 0) return false
    const cs = getComputedStyle(el)
    if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') return false
    return true
  }
  const ACTION_SEL = 'button, a[href], [role="button"]'
  const NON_ACTION_ZONE_RE = /(?:^|[\\s_-])(tab|tabs|nav|navbar|menu|filter|filters|bulk|breadcrumb|pagination)(?:[\\s_-]|$)/i
  // ★ 与源码门**同一口径**：只排除「菜单」（逐项呈现 ⇒ 不与页面动作同屏竞争）
  //   与组件库自带的模态/抽屉（role=dialog / el-dialog / el-drawer / app-modal / app-drawer）。
  //   ★ 仓内**手写**的模态（.modal-backdrop / .modal，如定价页的编辑弹窗）
  //     **不排除** —— 源码门也是判它的（底栏 5 个动作本轮收了）。
  //   ★ 第一版按「在 Teleport 内」排除 ⇒ 抽屉失明（真仓变异臂抓出来的判据缺陷）。
  const OVERLAY_SEL = '.action-overflow__menu, [role="menu"], [role="listbox"], [role="dialog"], .el-dialog, .el-drawer, .app-modal, .app-drawer'

  const zones = new Map()
  for (const el of document.querySelectorAll(ACTION_SEL)) {
    if (!isVisible(el)) continue
    if (el.closest(OVERLAY_SEL)) continue
    let p = el.parentElement
    if (!p) continue
    // ★ 穿透溢出菜单的组件根：触发器在 DOM 里的父元素是 .action-overflow，
    //   宿主动作区是它的祖父 ⇒ 不穿透的话触发器会从宿主区的计数里消失。
    if (p.classList && p.classList.contains('action-overflow')) p = p.parentElement
    if (!p) continue
    const cls = (typeof p.className === 'string') ? p.className : ''
    if (NON_ACTION_ZONE_RE.test(cls)) continue
    if (!zones.has(p)) zones.set(p, [])
    zones.get(p).push(el)
  }

  // ★ 「筛选片」形状排除：一排 chip 是**筛选器**，不是一组竞争动作。
  //   判据按**形状**判（class token 含 chip），不看文案 ⇒ 跨语种一致。
  //   ★ 口径窄：必须**全部**动作都是 chip 形状才排除（混排仍然判）。
  const allChip = (els) => els.every((e) => {
    const c = (typeof e.className === 'string') ? e.className : ''
    return /(^|[\\s_-])chip([\\s_-]|$)/i.test(c)
  })

  const out = []
  for (const [p, els] of zones) {
    if (els.length < 2) continue
    if (allChip(els)) continue
    const cls = (typeof p.className === 'string') ? p.className : ''
    out.push({
      cls: cls.slice(0, 60),
      tag: p.tagName.toLowerCase(),
      n: els.length,
      labels: els.slice(0, 8).map((e) => (e.getAttribute('aria-label') || e.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 24)),
    })
  }
  out.sort((a, b) => b.n - a.n)

  const triggers = [...document.querySelectorAll('[data-testid="action-overflow-trigger"]')]
  return {
    zones: out.slice(0, 25),
    maxVisible: out.length ? out[0].n : 0,
    triggerCount: triggers.length,
  }
})()`

/** 打开每个「更多」，逐条核对菜单项（可点开 ⇒ 用户够得着）。 */
const PROBE_MENUS = `(async () => {
  const triggers = [...document.querySelectorAll('[data-testid="action-overflow-trigger"]')]
  const results = []
  for (const t of triggers) {
    t.click()
    // ★★★ 必须等 Vue 把 DOM 更新完再查：Vue 的渲染是**异步**的
    //   （更新排在微任务队列里）⇒ 同步查 DOM 必然查到「菜单还没出现」。
    //   ★ 这正是本轮第一版探针的 bug —— 它报「菜单里一项都没有」，
    //   而真相是「我查得太早」。⇒ 量具自己出错时，报出来的是**假红**。
    await new Promise((r) => setTimeout(r, 150))
    const menu = document.querySelector('.action-overflow__menu')
    // 找出这个触发器所在的动作区（穿透 .action-overflow）
    let p = t.parentElement
    if (p && p.classList && p.classList.contains('action-overflow')) p = p.parentElement
    const zoneCls = p && typeof p.className === 'string' ? p.className.replace(/\\s+/g, ' ').trim().slice(0, 40) : ''
    if (!menu) {
      results.push({ zone: zoneCls || 'header', zoneVisible: 0, itemCount: 0, unlabeled: 0 })
      continue
    }
    const items = [...menu.querySelectorAll('[role="menuitem"]')]
    const unlabeled = items.filter((i) => !(i.textContent || '').replace(/\\s+/g, ' ').trim()).length
    // 该动作区里可见的动作数（触发器本身也算一个）
    const zoneVisible = p ? p.querySelectorAll('button, a[href], [role="button"]').length : 0
    results.push({ zone: zoneCls || 'header', zoneVisible, itemCount: items.length, unlabeled })
    t.click()
    await new Promise((r) => setTimeout(r, 80))
  }
  return results
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'ad-runtime-'))
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
      const probe = await evaluate(PROBE)
      const menus = await evaluate(PROBE_MENUS)
      await sleep(300)
      // ★★ 可达动作总数 = **按触发器逐个算**（zoneVisible + itemCount）后取最大。
      //   ★ 第一版是在事后按「区类名」去关联菜单项，结果**类名为空的动作区
      //   （free-pool 的页头就是一个没有类名的 div）匹配不上** ⇒ 可达数被少算
      //   5 项（读数 9 → 4，看着像「删了 5 个动作」）。
      //   ★ 教训：**关联要在产生数据的那一层做**，事后按名字猜关联必然出错。
      const reachables = menus.map((m) => (m.zoneVisible ?? 0) + (m.itemCount ?? 0))
      const maxZone = probe.zones?.[0]
      const reachable = reachables.length ? Math.max(...reachables) : (maxZone?.n ?? 0)
      report.pages.push({
        page: p,
        maxVisible: probe.maxVisible ?? 0,
        zones: probe.zones ?? [],
        menus,
        reachable,
      })
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
const jsonArg = argv.find((a) => a.startsWith('--json='))

const baseline = existsSync(BASELINE) ? JSON.parse(readFileSync(BASELINE, 'utf8')) : { maxVisible: {}, reachable: {} }
const port = 9431 + Math.floor(Math.random() * 300)
const report = await runChrome(port, pages)

const readings = {
  pages: Object.fromEntries(report.pages.map((p) => [p.page, { maxVisible: p.maxVisible, reachable: p.reachable }])),
}
const zoneFindings = report.pages.flatMap((p) => judgeZones(p.zones).map((f) => ({ page: p.page, ...f })))
const coverageFindings = report.pages.flatMap((p) =>
  judgeCoverage(p.menus).map((f) => ({ page: p.page, ...f })),
)
const regressions = compareToBaseline(readings, baseline)

// ── 基线收紧（只允许「可见数下降」与「可达数上升」）──────────────────────
if (argv.includes('--update-baseline')) {
  const bad = []
  for (const [page, r] of Object.entries(readings.pages)) {
    const bMax = baseline.maxVisible?.[page]
    const bReach = baseline.reachable?.[page]
    if (typeof bMax === 'number' && r.maxVisible > bMax) bad.push(`${page} 可见动作上限 ${bMax} → ${r.maxVisible}`)
    if (typeof bReach === 'number' && r.reachable < bReach) bad.push(`${page} 可达动作总数 ${bReach} → ${r.reachable}`)
  }
  if (bad.length) {
    console.error('❌ 基线只能往「更少可见 / 更多可达」的方向收紧：')
    for (const b of bad) console.error('   ' + b)
    process.exit(1)
  }
  const next = {
    metric:
      'maxVisible = 该页任一动作区里同时可见的动作数上限（不得上升）；' +
      'reachable = 该页一个动作区里「可见 + 「更多」菜单项」的动作总数（不得下降 ⇒ 防「删按钮让门变绿」）',
    maxVisible: Object.fromEntries(Object.entries(readings.pages).map(([k, v]) => [k, v.maxVisible])),
    reachable: Object.fromEntries(Object.entries(readings.pages).map(([k, v]) => [k, v.reachable])),
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        updated: '2026-10-27',
        maxVisible: next_maxVisible(readings),
        reachable: next_reachable(readings),
        reason: process.env.AD_RT_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log('✅ 基线已收紧')
  for (const [k, v] of Object.entries(readings.pages)) {
    console.log(`   ${k}: 可见上限 ${v.maxVisible} · 可达 ${v.reachable}`)
  }
  process.exit(0)
}

function next_maxVisible(r) {
  return Object.fromEntries(Object.entries(r.pages).map(([k, v]) => [k, v.maxVisible]))
}
function next_reachable(r) {
  return Object.fromEntries(Object.entries(r.pages).map(([k, v]) => [k, v.reachable]))
}

if (jsonArg) {
  writeFileSync(jsonArg.slice(7), JSON.stringify({ report, readings, zoneFindings, coverageFindings, regressions }, null, 2))
  console.log(`✅ 读数已写入 ${jsonArg.slice(7)}`)
} else {
  for (const p of report.pages) {
    console.log(`\n${p.page}  可见动作上限 ${p.maxVisible} · 可达动作总数 ${p.reachable}`)
    for (const z of p.zones.slice(0, 5)) {
      console.log(`  动作区 <${z.tag}${z.cls ? ' class="' + z.cls + '"' : ''}>  ${z.n} 个：${z.labels.join(' | ')}`)
    }
    for (const m of p.menus) {
      console.log(`  「更多」菜单：${m.itemCount} 项${m.unlabeled ? `（其中 ${m.unlabeled} 项没文案）` : ''}`)
    }
  }
  for (const e of report.errors) console.log(`\n⚠️ ${e.page} 探测失败：${e.error}`)
  console.log(`\n判红：动作区超阈值 ${zoneFindings.length} 处 · 折叠动作够不着 ${coverageFindings.length} 处`)
  for (const f of zoneFindings) console.log(`  ❌ ${f.page}  ${f.zone}  ${f.n} 个可见动作 —— ${f.reason}`)
  for (const f of coverageFindings) console.log(`  ❌ ${f.page}  ${f.zone} —— ${f.reason}`)
  if (regressions.length) {
    console.log('\n棘轮回归：')
    for (const r of regressions) console.log('  ❌ ' + r)
  }
}

if (zoneFindings.length || coverageFindings.length) process.exit(1)
if (regressions.length) process.exit(1)
if (report.errors.length) {
  console.error('\n⚠️ 有页面探测失败 ⇒ 读数不完整（不是「通过」）。')
  process.exit(1)
}
console.log('\n✅ 每个动作区的可见动作数 ≤ 4，且折叠进去的动作都还够得着 —— PASS')
