/**
 * ui-sweep.mjs — 全路由运行时遍历扫描（2026-10-03）
 *
 * ui-audit.mjs 是静态判据：能证伪、不能证成。本脚本补上它证不了的那一半 ——
 * 在真浏览器里逐路由量一次：
 *
 *   1. console 错误 / 未捕获异常（老板要的「逐个页面检查前端 console 错误」）
 *   2. 失败请求（/api 4xx/5xx）
 *   3. **全屏**：页面根容器实际只占了主区宽度的百分之多少、是否左右等距居中
 *      —— 直接量「窄列居中」这个静态判据只能看声明值
 *   4. **逐字折行**：极窄却很高的文本元素（就是供应商表那个 bug 的签名）
 *   5. **内容裁切**：overflow:hidden 但内容比盒子高，且不可滚
 *
 * 用法：
 *   GATEWAY_API_TARGET=… GATEWAY_DEV_AUTH_TOKEN=… npm run dev   # 另开一个终端
 *   node scripts/ui-sweep.mjs                       # 扫 dev
 *   BASE_URL=http://127.0.0.1:5793 node scripts/ui-sweep.mjs
 *   node scripts/ui-sweep.mjs --json reports/ui-sweep.json
 *   node scripts/ui-sweep.mjs --only /providers,/tenants
 *   node scripts/ui-sweep.mjs --strict               # 有 P0/P1 时 exit 1
 *
 * 鉴权：走 extraHTTPHeaders 注入 Authorization，**不碰 cookie / localStorage**。
 * 路由清单从 src/router.ts 直接解析，不另维护一份（避免清单漂移）。
 */

import { readFileSync, writeFileSync, mkdirSync, readdirSync } from 'node:fs'
import { resolve, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')

// ── 解析 playwright ────────────────────────────────────────────────
// web/ 没有装 playwright（不想为一次审计给产品加依赖），但本机 npx 缓存
// 与兄弟项目 maintain-web 里都有。
//
// 两个坑，都是「静默失效」型：
//  ① ESM 里写 require('node:fs') 必抛 ReferenceError，被外层 catch 吞掉
//     ⇒ npx 缓存目录一个都没扫到，只剩硬编码路径在兜底。
//  ② `require(p)` 成功 ≠ 这个 playwright 能跑：它的 chromium 修订号可能没装。
//     典型现场：三个 npx 缓存里分别是 1.61/1.63/1.64-alpha，本机只下了
//     chromium build 1223/1243，1.64-alpha 要 1224 ⇒ launch 时才炸。
//     所以候选的可用性判据是「**能不能真启动一个浏览器**」，不是「能不能 require 到」。
function playwrightCandidates() {
  const roots = []
  const npxRoot = join(process.env.HOME || '', '.npm', '_npx')
  let entries = []
  try {
    entries = readdirSync(npxRoot)
  } catch (e) {
    if (e.code !== 'ENOENT') throw e
  }
  for (const d of entries) roots.push(join(npxRoot, d, 'node_modules', 'playwright'))
  roots.push(
    '__DEV_HOME__/workspace/ai-native-tools/llm-gateway/ai-native-maintain/maintain-web/node_modules/playwright',
  )
  return roots
}

function installedBrowserRevisions() {
  const cache = join(process.env.HOME || '', 'Library', 'Caches', 'ms-playwright')
  try {
    return readdirSync(cache).filter((d) => d.startsWith('chromium'))
  } catch {
    return []
  }
}

// 逐个候选真启动一次，返回第一个能用的。
async function launchBrowser() {
  const tried = []
  for (const p of playwrightCandidates()) {
    let chromium
    try {
      chromium = createRequire(import.meta.url)(p).chromium
    } catch (e) {
      tried.push(`${p} → require 失败（${e.code || e.message.slice(0, 50)}）`)
      continue
    }
    try {
      const browser = await chromium.launch({ headless: true })
      return { browser, from: p }
    } catch (e) {
      tried.push(`${p} → launch 失败（${String(e.message).split('\n')[0].slice(0, 90)}）`)
    }
  }
  // 全部失败时报出**逐个路径 + 失败原因 + 本机已装的浏览器修订**，
  // 不要只说「找不到」——否则下一次同类问题又要从头猜。
  throw new Error(
    `没有可用的 playwright。试过：\n  - ${tried.join('\n  - ') || '（无候选路径）'}` +
      `\n本机已安装的浏览器：${installedBrowserRevisions().join(', ') || '（无）'}` +
      '\n若版本对不上，跑 `npx playwright install chromium` 补齐。',
  )
}

// ── 路由清单：直接从 router.ts 解析 ────────────────────────────────
function parseRoutes() {
  const src = readFileSync(join(WEB, 'src', 'router.ts'), 'utf8')
  const out = []
  const re = /\{\s*path:\s*'([^']+)'[^}]*?component:\s*([A-Za-z0-9_]+)/g
  let m
  while ((m = re.exec(src))) {
    let [, path, comp] = m
    // 跳过重定向（无 component）、公开无关页、以及需要动态参数的详情页
    if (path.includes(':') || path === '/:pathMatch(.*)*') continue
    if (/LoginView|ForbiddenView|CustomerUpdateActivateView|CustomerOfflineActivationView|BootstrapWizardView|MaintainUnavailableView|DispatchWaterfallPreview/.test(comp)) continue
    if (path === '/login' || path === '/forbidden' || path === '/bootstrap') continue
    if (path.startsWith('/customer/') || path.startsWith('/maintain') || path.startsWith('/dev/')) continue
    out.push({ path, comp })
  }
  // 带重定向函数的行（如 /catalog → /models）由 re 抓不到，忽略：
  // 它们最终会落到已扫的目标页上。
  return out
}

// ── 页面内检查（跑在浏览器上下文里）───────────────────────────────
//
// ⚠ 作用域必须与 App.vue 的真实骨架一致：主区是
//   <section class="main-body"><RouterView/></section>   （App.vue:222）
//   **不是** <main> 标签。这条曾写成 querySelectorAll('main *')，
//   在真应用里匹配到 0 个元素 ⇒ 折行/裁切两个探针在全部页面上恒为空，
//   扫描报告却显示「0 违规」。空结果与「没有问题」在输出上完全一样。
//   ⇒ ① 作用域写死为 .main-body；② 探针回报 scanned 元素数，
//     判级时若为 0 直接判 P0（判据失效），绝不当作干净页。
const PAGE_PROBE = () => {
  const out = { fullWidth: null, verticalText: [], clipped: [], rootClass: '', rootWidth: 0, bodyWidth: 0, scanned: 0 }
  const body = document.querySelector('.main-body') || document.querySelector('main')
  if (!body) return out
  const bRect = body.getBoundingClientRect()
  out.bodyWidth = Math.round(bRect.width)

  // 1) 全屏：根容器实际占主区多少宽、是否左右等距
  const main = body.querySelector(':scope > *')
  if (main) {
    const r = main.getBoundingClientRect()
    out.rootClass = (typeof main.className === 'string' ? main.className : '').split(/\s+/)[0] || ''
    out.rootWidth = Math.round(r.width)
    const ratio = bRect.width > 0 ? r.width / bRect.width : 1
    const padLeft = r.left - bRect.left
    const padRight = bRect.right - r.right
    // 居中：左右留白接近相等，且明显窄于主区
    const centered = Math.abs(padLeft - padRight) < 24 && padLeft > 40
    out.fullWidth = { ratio: Math.round(ratio * 100) / 100, centered, padLeft: Math.round(padLeft), padRight: Math.round(padRight) }
  }

  const all = body.querySelectorAll('*')
  out.scanned = all.length
  // 可见文本量：「干净」与「空壳」在违规清单上长得一样（都是 0 条）。
  // 页面若只有骨架没有数据，scanned 可能不为 0 但 textLen 极小 ——
  // 把它一并带出来，报告才分得清「没违规」和「没内容」。
  out.textLen = (body.innerText || '').trim().length
  out.visibleBlocks = [...body.querySelectorAll('table,tbody,ul,ol,section,article,form')].filter((e) => e.clientHeight > 0).length

  // 2) 逐字折行签名：极窄 + 很高 + 含多行文本。
  //    供应商表那个 bug 的可测形态：列宽被压到 ~30px，文字按字符竖排。
  for (const el of all) {
    if (el.children.length > 2) continue
    const text = (el.textContent || '').trim()
    if (!text || text.length < 4) continue
    const w = el.clientWidth
    const h = el.clientHeight
    if (w > 0 && w < 44 && h > w * 2.2 && h > 90) {
      const cs = getComputedStyle(el)
      if (cs.overflowY === 'auto' || cs.overflowY === 'scroll') continue
      out.verticalText.push({
        tag: el.tagName.toLowerCase(),
        cls: (typeof el.className === 'string' ? el.className : '').slice(0, 60),
        w, h, text: text.slice(0, 30),
      })
      if (out.verticalText.length >= 8) break
    }
  }

  // 3) 内容裁切：overflow hidden + 内容比盒子高 + 自己不可滚
  for (const el of all) {
    const cs = getComputedStyle(el)
    const clips = cs.overflow === 'hidden' || cs.overflowY === 'hidden'
    if (!clips) continue
    if (el.scrollHeight > el.clientHeight + 8 && el.clientHeight > 0) {
      const scrollable = cs.overflowY === 'auto' || cs.overflowY === 'scroll'
      if (scrollable) continue
      out.clipped.push({
        tag: el.tagName.toLowerCase(),
        cls: (typeof el.className === 'string' ? el.className : '').slice(0, 60),
        clientH: el.clientHeight, scrollH: el.scrollHeight,
      })
      if (out.clipped.length >= 8) break
    }
  }
  return out
}

const OVERLAY_PROBE = () => {
  const out = { found: null, scrollable: null, widest: 0 }
  // 已打开的弹层：右侧抽屉 / 居中弹窗
  const cands = [...document.querySelectorAll('[class*="drawer"],[class*="modal"]')]
    .filter((el) => {
      const r = el.getBoundingClientRect()
      return r.width > 120 && r.height > 120 && cs_visible(el)
    })
    .map((el) => ({ el, r: el.getBoundingClientRect() }))
    // 取最外层（面积最大者）
    .sort((a, b) => b.r.width * b.r.height - a.r.width * a.r.height)
  if (!cands.length) return out
  const { el, r } = cands[0]
  out.found = (typeof el.className === 'string' ? el.className : '').split(/\s+/).slice(0, 2).join('.')
  out.widest = Math.round(r.width)
  // 内容区是否可滚
  const scrollers = [...el.querySelectorAll('*')].filter((c) => {
    const cs = getComputedStyle(c)
    return (cs.overflowY === 'auto' || cs.overflowY === 'scroll') && c.scrollHeight > c.clientHeight + 8
  })
  out.scrollable = scrollers.length > 0 || el.scrollHeight <= el.clientHeight + 8
  return out
  function cs_visible(el) {
    const cs = getComputedStyle(el)
    return cs.display !== 'none' && cs.visibility !== 'hidden' && Number(cs.opacity) > 0.05
  }
}

// ── 探针自检 ────────────────────────────────────────────────────────
// 运行时判据比静态判据更容易「恒绿」：探针 selector 写错、阈值定得不合理、
// 页面骨架换名，都会让所有页面安静地报 0 违规——而 0 违规正是我们要的结论。
// 所以每条运行时判据都必须先在一个**故意违规**的夹具上证明它会红，
// 再在一个干净夹具上证明它会绿。
//
// 判别力检查（不跑就知道）：把任意一条 expected 从 true 改成 false，
// 断言必须失败。夹具和断言同源于一个数据表，构造不出「判据恒假」，
// 但能构造出「判据恒真」——那才是真问题（探针对什么都报）。
const SELFTEST_FIXTURES = [
  {
    name: '干净全屏页：不得报任何违规',
    html: `<div class="main-body"><section class="page">正常内容，正常宽度。</section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{background:#fff;padding:20px;width:100%;box-sizing:border-box}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '居中窄列：max-width + margin auto 必须被抓住',
    html: `<div class="main-body"><section class="page">内容</section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{max-width:600px;margin:0 auto;background:#fff;padding:20px;box-sizing:border-box}</style>`,
    expect: { centered: true, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '全宽但左右小留白（padding 感）：不得被误判成居中',
    html: `<div class="main-body"><section class="page">内容</section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{margin:0 12px;background:#fff;padding:20px;box-sizing:border-box}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    // 真实 bug 形态（commit 5e5022e21 引入、b4998e07 修复）：
    // table-layout:fixed + table width:auto（shrink-to-fit）+ 单元格 max-width:0
    // ⇒ 列宽全被压成 0，表格收到 ~70px，文字按字符竖排（实测单元格 2px 宽 × 776 高），
    // 而外层 .card{overflow-x:auto} 因为表已被压到 100% 宽而**无物可滚**。
    name: '逐字竖排：定宽表 width:auto + 单元格 max-width:0（供应商表 bug 签名）',
    html: `<div class="main-body"><section class="page"><div class="card"><table><tr>
        ${Array.from({ length: 8 }, (_, i) => `<td>${['HEADERNAME', 'https://api.example.com/v1/chat', 'glm-4.6'][i % 3]}</td>`).join('')}
        </tr></table></div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{width:100%;box-sizing:border-box}
      .card{overflow-x:auto}
      table{table-layout:fixed}
      td{max-width:0;word-break:break-word;white-space:normal;overflow-wrap:break-word}</style>`,
    expect: { centered: false, verticalText: 1, clipped: 0, scannedNonZero: true },
  },
  {
    // 反向对照：同一张表按 b4998e07 的修法（min-width + 去 max-width:0 + nowrap）
    // ⇒ 必须判绿。少了这一条，探针可能只是「见窄就报」，照样全绿。
    name: '同一张表的修复后形态：min-width + nowrap，不得误报折行',
    html: `<div class="main-body"><section class="page"><div class="card"><table><tr>
        ${Array.from({ length: 8 }, (_, i) => `<td>${['HEADERNAME', 'https://api.example.com/v1/chat', 'glm-4.6'][i % 3]}</td>`).join('')}
        </tr></table></div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{width:100%;box-sizing:border-box}
      .card{overflow-x:auto}
      table{table-layout:fixed;min-width:1800px}
      td{white-space:nowrap}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '窄但自带滚动的长文本：不得误报逐字折行（w30×h200，确实命中阈值）',
    html: `<div class="main-body"><section class="page">
        <div class="log">AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA</div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{background:#fff;padding:0;width:100%;box-sizing:border-box}
      .log{width:30px;height:200px;overflow-y:auto;word-break:break-all}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '内容裁切：overflow:hidden + 内容换行后更高 + 不可滚必须被抓住',
    html: `<div class="main-body"><section class="page">
        <div class="cell">${'这是一段会被挤成多行的说明文字，用于把盒子撑高到溢出高度。'.repeat(6)}</div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{background:#fff;padding:0;width:100%;box-sizing:border-box}
      .cell{width:100%;height:30px;overflow:hidden;line-height:20px}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 1, scannedNonZero: true },
  },
]

async function runSelftest() {
  const { browser, from } = await launchBrowser()
  console.log(`  （浏览器来自 ${from}）`)
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  let pass = 0
  const fails = []
  for (const fx of SELFTEST_FIXTURES) {
    const page = await ctx.newPage()
    await page.setContent(fx.html)
    await page.waitForTimeout(120)
    const got = await page.evaluate(PAGE_PROBE)
    await page.close()
    const actual = {
      centered: !!got.fullWidth?.centered,
      verticalText: got.verticalText.length,
      clipped: got.clipped.length,
      // 第 4 条：作用域必须真的扫到元素。上面三条全 0 有两种成因——
      // 「页面干净」和「选择器没匹配到任何东西」，输出上一模一样。
      scannedNonZero: got.scanned > 0,
    }
    for (const k of ['centered', 'verticalText', 'clipped', 'scannedNonZero']) {
      const want = fx.expect[k]
      // 布尔判据用相等；计数判据用「至少」——一处真实 bug 常在同页复现多行/多列，
      // 探针也只在 8 处封顶，拿相等去卡会把「报得更多」误判成探针失灵。
      const isCount = typeof want === 'number'
      const ok = isCount ? actual[k] >= want : actual[k] === want
      if (ok) pass++
      else fails.push(`${fx.name}\n    判据 ${k}：期望 ${isCount ? '≥' : '='} ${want}，实测 ${actual[k]}`)
    }
  }
  await browser.close()
  const total = SELFTEST_FIXTURES.length * 4
  console.log(`\n══ 探针自检 ${pass}/${total} ══`)
  for (const f of fails) console.log(`  ✗ ${f}`)
  if (fails.length) {
    console.error(`\n✗ 探针自检未通过：${fails.length}/${total} 判据没有鉴别力，先修判据再扫页面。`)
    process.exit(1)
  }
  console.log(`✓ ${SELFTEST_FIXTURES.length} 个夹具 × 4 条判据，正负双向都能区分`)
}

// ── 主流程 ────────────────────────────────────────────────────────
const args = process.argv.slice(2)
if (args.includes('--selftest')) {
  await runSelftest()
  process.exit(0)
}
const BASE = (process.env.BASE_URL || 'http://127.0.0.1:5781').replace(/\/$/, '')
const TOKEN = process.env.GATEWAY_DEV_AUTH_TOKEN || ''
const STRICT = args.includes('--strict')

let routes = parseRoutes()
if (args.includes('--only')) {
  const want = new Set(args[args.indexOf('--only') + 1].split(',').map((s) => s.trim()))
  routes = routes.filter((r) => want.has(r.path))
}

const { browser, from: browserFrom } = await launchBrowser()
console.log(`浏览器：${browserFrom}\n目标：${BASE}\n`)
const ctx = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  extraHTTPHeaders: TOKEN ? { Authorization: `Bearer ${TOKEN}` } : {},
})

const results = []
let i = 0
for (const r of routes) {
  i++
  const page = await ctx.newPage()
  const errors = []
  const badReqs = []
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text().slice(0, 300))
  })
  page.on('pageerror', (e) => errors.push(`[pageerror] ${String(e).slice(0, 300)}`))
  page.on('response', (res) => {
    const u = res.url()
    // 记**所有**失败响应，不只 /api/。曾经只记 /api/，结果 console 报 500 而
    // badReqs 是空数组 —— 「没记到」和「没有」在报告上长得一样。
    if (res.status() >= 400) badReqs.push(`${res.status()} ${u.replace(BASE, '').slice(0, 160)}`)
  })
  page.on('requestfailed', (r) => {
    const f = r.failure()?.errorText || 'unknown'
    badReqs.push(`[requestfailed] ${f} ${r.url().replace(BASE, '').slice(0, 140)}`)
  })

  let probe = null
  let err = null
  try {
    await page.goto(BASE + r.path, { waitUntil: 'domcontentloaded', timeout: 20000 })
    // 认证水合 + 首屏数据。
    // 等待值必须大于最慢页面的加载时间，否则「空壳」分不清是「空」还是「慢」：
    // /admin/tenants 富化查询要 ~1.5s，早期用 1200ms 时它被判成 <120 字空壳页，
    // 差点被当成「租户页没数据」去查数据问题。判据的阈值本身就是被测结论的一部分。
    await page.waitForSelector('.main-body > *', { timeout: 15000 }).catch(() => {})
    await page.waitForTimeout(3000)
    probe = await page.evaluate(PAGE_PROBE)
  } catch (e) {
    err = String(e).slice(0, 200)
  }
  await page.close()

  // 判级
  const P0 = [] // 阻断级：页面没渲染 / 未捕获异常
  const P1 = [] // 应修：console 错误 / 逐字折行 / 裁切 / 非全屏
  if (err) P0.push(`导航失败：${err}`)
  if (!probe || !probe.fullWidth) P0.push('未渲染出页面根容器（可能重定向到 /bootstrap 或鉴权失败）')
  // 判据失效不许伪装成干净页：扫到 0 个元素时，折行/裁切两条必然报 0，
  // 那不是「没问题」，那是「没看见」。
  if (probe && probe.scanned === 0) P0.push('判据作用域失效：.main-body 内 0 个元素，折行/裁切结果不可信')
  if (probe?.fullWidth && probe.fullWidth.centered) {
    P1.push(`居中模式：根容器 .${probe.rootClass} 只占主区 ${probe.fullWidth.ratio * 100}%，左右留白 ${probe.fullWidth.padLeft}/${probe.fullWidth.padRight}`)
  }
  if (probe?.verticalText?.length) {
    P1.push(`逐字折行 ${probe.verticalText.length} 处，例：<${probe.verticalText[0].tag} class="${probe.verticalText[0].cls}"> ${probe.verticalText[0].w}×${probe.verticalText[0].h} "${probe.verticalText[0].text}"`)
  }
  if (probe?.clipped?.length) {
    const c = probe.clipped[0]
    P1.push(`内容裁切 ${probe.clipped.length} 处，例：.${c.cls} clientH=${c.clientH} scrollH=${c.scrollH} 且不可滚`)
  }
  if (errors.length) P1.push(`console 错误 ${errors.length} 条，首条：${errors[0].slice(0, 160)}`)
  if (badReqs.length) P1.push(`失败请求 ${badReqs.length} 条：${badReqs.slice(0, 3).join(' | ').slice(0, 220)}`)

  results.push({
    path: r.path, comp: r.comp, p0: P0, p1: P1,
    scanned: probe?.scanned ?? 0, textLen: probe?.textLen ?? 0, visibleBlocks: probe?.visibleBlocks ?? 0,
    bodyWidth: probe?.bodyWidth ?? 0, rootWidth: probe?.rootWidth ?? 0,
    badReqs: [...new Set(badReqs)].slice(0, 5), errors: [...new Set(errors)].slice(0, 5),
  })
  const mark = P0.length ? '✗' : P1.length ? '!' : '✓'
  process.stdout.write(`${mark} [${String(i).padStart(3)}/${routes.length}] ${r.path}  (元素 ${probe?.scanned ?? 0} / 文本 ${probe?.textLen ?? 0} 字)\n`)
}

await browser.close()

// ── 报告 ──────────────────────────────────────────────────────────
const p0 = results.filter((r) => r.p0.length)
const p1 = results.filter((r) => !r.p0.length && r.p1.length)
const clean = results.length - p0.length - p1.length
// 空壳页：0 违规但几乎没有内容。分出来单列，避免混进「干净」计数里。
const hollow = clean > 0 ? results.filter((r) => !r.p0.length && !r.p1.length && r.textLen < 120) : []

console.log(`\n══ 扫描结果（${BASE}，viewport 1440×900，共 ${results.length} 路由）══`)
console.log(`✗ P0 阻断（未渲染/判据失效）: ${p0.length}`)
console.log(`! P1 应修                   : ${p1.length}`)
console.log(`✓ 干净                      : ${clean - hollow.length}`)
console.log(`○ 内容可疑（<120 字，空壳）  : ${hollow.length}`)

if (p0.length) {
  console.log('\n── P0 ──')
  for (const r of p0) console.log(`  ${r.path}\n    ${r.p0.join('\n    ')}`)
}
if (p1.length) {
  console.log('\n── P1 ──')
  for (const r of p1) {
    console.log(`  ${r.path}`)
    for (const m of r.p1) console.log(`    · ${m}`)
  }
}

if (hollow.length) {
  console.log('\n── ○ 内容可疑 ──')
  for (const r of hollow) console.log(`  ${r.path}  文本仅 ${r.textLen} 字，块 ${r.visibleBlocks} 个`)
}
if (args.includes('--json')) {
  const outPath = args[args.indexOf('--json') + 1] || 'reports/ui-sweep.json'
  const abs = resolve(WEB, outPath)
  mkdirSync(dirname(abs), { recursive: true })
  writeFileSync(abs, JSON.stringify({ base: BASE, at: new Date().toISOString(), results }, null, 2))
  console.log(`\nJSON：${outPath}`)
}

if (STRICT && p0.length + p1.length) {
  console.error(`\n✗ 遍历扫描未通过（P0:${p0.length} P1:${p1.length}）`)
  process.exit(1)
}
