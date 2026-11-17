#!/usr/bin/env node
// maestro/tools/assert-stage-tree.mjs —— 请求阶段树（/live2）的**页面内**断言。
//
// ★ 为什么需要它（2026-11-17「打开功能」轮）：
//   Maestro 2.11.0 的 **web 驱动**有三道硬边界（maestro/README.md §6.6）：
//     ① evalScript 是 no-op（`CdpWebDriver` 没有 evaluateScript 方法）；
//     ② `${output.x = ...}` 只在 JVM 侧求值，拿不到 DOM；
//     ③ 断言选择器只有 `text:` / `id:` / `css:` 三种，且
//        **只匹配可见文本、元素完整文本相等**、**`id:` 只认 HTML id 不认 data-testid**。
//   ⇒ 「页面身份（pathname）」「DOM 契约（data-testid 在不在）」「统计口径对拍
//     （胶囊读数 vs 实际阶段带条数）」「数据是活的（真的收到请求帧）」这四类断言，
//     用 flow 的选择器**做不到**（要么不可见、要么数字会变、要么 id 选不到）。
//   ⇒ 与 `assert-overview.mjs` 同一分工：flow 验**行为**，本脚本验**结构与口径**。
//
// ★ 本脚本断言什么（全部在页面里一次求值）：
//   1. 页面身份：location.pathname === 期望路由（/live2 或 /live）
//   2. 阶段树 DOM 契约：根节点 / 标题 / 连接态 / 统计胶囊 / 画布 / 空态 / 阶段带
//   3. **口径对拍**：胶囊读数「N 条 · 成功率 X% · 在途 M」与**实际 DOM**
//      （阶段带条数、成功/在途/失败徽章数）逐项相等
//   4. 过滤栏**常驻顶层**：过滤栏是 header 的子节点，不在画布里
//   5. 空态与阶段带互斥（有带就没有空态）
//   6. **本轮「打开功能」的直接证据**：顶栏导航里存在指向 /live2 的可见链接
//   7. /live 页顶栏入口 `[data-testid="live-stage-tree-link"]` 存在且可见
//   8. 统计浮层：点开 ⇒ 标题「实时统计」出现；Esc ⇒ 关闭（同一脚本内做点击与按键）
//
// 用法：
//   node maestro/tools/assert-stage-tree.mjs \
//     --cdp http://127.0.0.1:9222 \
//     --url https://llm.kxpms.cn \
//     --token-file /tmp/llmgw-jwt.txt \
//     [--page /live2|/live]
//
// ★ 安全约束：JWT 只从 --token-file 读（0600 临时文件），**不打印**；
//   调用方负责用完后删掉那个文件。
// 退出码：0 = 全绿；1 = 有断言失败（报告里逐条列出）。

import { readFileSync } from 'node:fs'
import { connect, evaluate, openPage, waitFor } from './cdp-client.mjs'

function parseArgs(argv) {
  const out = { cdp: 'http://127.0.0.1:9222', url: '', tokenFile: '', page: '/live2' }
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (a === '--cdp') out.cdp = argv[++i]
    else if (a === '--url') out.url = argv[++i]
    else if (a === '--token-file') out.tokenFile = argv[++i]
    else if (a === '--page') out.page = argv[++i]
  }
  return out
}

const args = parseArgs(process.argv.slice(2))
if (!args.url || !args.tokenFile) {
  console.error('需要 --url 与 --token-file')
  process.exit(1)
}
const token = readFileSync(args.tokenFile, 'utf8').trim()

/** 页面内一次求值：结构 + 口径 + 交互，全部在页面内完成。 */
const PAGE_PROBE = `(() => {
  const q  = (s) => document.querySelector(s)
  const qa = (s) => Array.from(document.querySelectorAll(s))
  const tx = (el) => ((el && el.textContent) || '').replace(/\\s+/g, ' ').trim()
  const visible = (el) => !!(el && (el.offsetWidth || el.offsetHeight || el.getClientRects().length))

  const out = { ok: true, checks: [], facts: {} }
  const check = (name, pass, detail) => {
    out.checks.push({ name, pass: !!pass, detail: detail === undefined ? '' : String(detail) })
    if (!pass) out.ok = false
  }

  // ── 1. 页面身份 ───────────────────────────────────────────────────────────
  const path = location.pathname.replace(/\\/$/, '') || '/'
  out.facts.pathname = path
  out.facts.expected = ${JSON.stringify(args.page)}
  check('页面路由命中期望值', path === ${JSON.stringify(args.page)}, path)

  const isStageTree = path === '/live2'

  // ── 2. 导航入口（本轮「打开功能」的直接证据）────────────────────────────
  //   顶栏导航里指向 /live2 的链接必须**可见**（被 hidden 闸门藏掉时链接就不存在）。
  const navLinks = qa('a[href="/live2"]')
  const navVisible = navLinks.filter(visible)
  out.facts.navLive2Links = navLinks.length
  out.facts.navLive2Visible = navVisible.length
  check('顶栏存在指向 /live2 的导航链接', navLinks.length > 0, '共 ' + navLinks.length + ' 个')
  check('该导航链接可见（未被隐藏）', navVisible.length > 0, '可见 ' + navVisible.length + ' 个')

  if (!isStageTree) {
    // ── /live 页：本轮恢复的「阶段树」顶栏入口 ──────────────────────────────
    const entry = q('[data-testid="live-stage-tree-link"]')
    out.facts.liveStageTreeLink = !!entry
    out.facts.liveStageTreeLinkHref = entry ? entry.getAttribute('href') : ''
    out.facts.liveStageTreeLinkText = entry ? tx(entry) : ''
    check('/live 顶栏「请求阶段树」入口存在', !!entry, entry ? tx(entry) : '缺失')
    check('该入口可见', visible(entry), '')
    check('该入口指向 /live2', out.facts.liveStageTreeLinkHref === '/live2', out.facts.liveStageTreeLinkHref)
    return out
  }

  // ── 3. 阶段树 DOM 契约 ───────────────────────────────────────────────────
  const root = q('.stage-tree')
  check('阶段树根节点存在', !!root, '')
  const title = q('.stage-tree__title')
  out.facts.title = tx(title)
  check('标题为「请求阶段树」', out.facts.title === '请求阶段树', out.facts.title)

  const conn = q('.stage-tree__conn')
  const connCls = conn ? conn.className : ''
  out.facts.connClass = connCls
  out.facts.connText = tx(conn)
  check('连接态节点存在', !!conn, out.facts.connText)
  check('SSE 已连接（is-open）', /is-open/.test(connCls), connCls)

  const capsule = q('[data-testid="stage-stats-open"]')
  check('统计胶囊存在', !!capsule, '')
  out.facts.capsuleText = tx(capsule)

  const canvas = q('[data-testid="stage-tree-canvas"]')
  check('阶段树画布存在', !!canvas, '')

  const filters = q('.stage-tree__filters')
  check('过滤栏存在', !!filters, '')
  // ★ 过滤条件必须**常驻顶层**（owner 定稿）：它在 header 里，不在画布里。
  const inCanvas = !!(filters && canvas && canvas.contains(filters))
  check('过滤栏不在画布内（常驻顶层）', filters && !inCanvas, inCanvas ? '在画布内' : '在顶栏')

  // ── 4. 口径对拍：胶囊读数 vs 实际 DOM ────────────────────────────────────
  const bands = qa('article.band')
  const badges = qa('[data-testid="stage-band-status"]')
  const isSuccess = badges.filter((b) => /is-success/.test(b.className)).length
  const isInFlight = badges.filter((b) => /is-in_progress/.test(b.className)).length
  const isFailure = badges.filter((b) => /is-failure/.test(b.className)).length
  out.facts.bands = bands.length
  out.facts.badges = badges.length
  out.facts.success = isSuccess
  out.facts.inFlight = isInFlight
  out.facts.failure = isFailure

  const readout = tx(capsule).match(/^(\\d+)\\s*条\\s*·\\s*成功率\\s*([^%]*?)%\\s*·\\s*在途\\s*(\\d+)$/)
  check('胶囊读数可解析', !!readout, tx(capsule))
  if (readout) {
    const total = Number(readout[1])
    const rateRaw = readout[2].trim()
    const inFlightRead = Number(readout[3])
    out.facts.readoutTotal = total
    out.facts.readoutRate = rateRaw
    out.facts.readoutInFlight = inFlightRead
    check('胶囊总条数 == 页面阶段带条数', total === bands.length, readout[1] + ' vs ' + bands.length)
    check('胶囊在途数 == 带内 in_progress 徽章数', inFlightRead === isInFlight, readout[3] + ' vs ' + isInFlight)
    if (total > 0) {
      const done = bands.length - isFailure - isInFlight
      const expectRate = String(Math.round((done / total) * 100))
      check('胶囊成功率 == (总-失败-在途)/总', rateRaw === expectRate, rateRaw + ' vs ' + expectRate)
    } else {
      check('总条数为 0 时成功率显示「—」（不冒充 0%）', rateRaw === '—', rateRaw)
    }
  }

  // ── 5. 空态与阶段带互斥 ──────────────────────────────────────────────────
  const empty = q('[data-testid="stage-tree-empty"]')
  out.facts.emptyVisible = visible(empty)
  out.facts.emptyText = tx(empty)
  if (bands.length > 0) {
    check('有阶段带时不出现空态', !visible(empty), '带 ' + bands.length + ' 条')
  } else {
    check('无阶段带时出现空态文案', visible(empty) && out.facts.emptyText.length > 0, out.facts.emptyText)
  }

  // ── 6. 数据是活的：阶段带要么有真实请求，要么给出诚实空态 ─────────────────
  out.facts.dataLive = bands.length > 0
  check('实时流有数据（阶段带 ≥ 1）或给出诚实空态', bands.length > 0 || visible(empty),
        '带 ' + bands.length + ' 条 / 空态 ' + (visible(empty) ? '有' : '无'))

  return out
})()`

/** 统计浮层交互：点开 ⇒ 断言标题 ⇒ Esc ⇒ 断言关闭（同一页面内做）。 */
const OVERLAY_PROBE = `(async () => {
  const q = (s) => document.querySelector(s)
  const tx = (el) => ((el && el.textContent) || '').replace(/\\s+/g, ' ').trim()
  const visible = (el) => !!(el && (el.offsetWidth || el.offsetHeight || el.getClientRects().length))
  const out = { checks: [], facts: {} }
  const check = (name, pass, detail) => {
    out.checks.push({ name, pass: !!pass, detail: detail === undefined ? '' : String(detail) })
  }
  const btn = q('[data-testid="stage-stats-open"]')
  if (!btn) { check('统计胶囊存在（点开前）', false, '缺失'); return out }
  check('统计胶囊存在（点开前）', true, '')
  btn.click()
  // ★ Vue 的 DOM 更新是**异步**的（nextTick）⇒ 点击后必须让出一帧，
  //   否则读到的还是点击前的 DOM ⇒ 会被误判成「浮层没打开」。
  await new Promise((r) => setTimeout(r, 150))
  const overlay = q('[data-testid="stage-stats-overlay"]')
  const title = overlay ? q('.stage-stats-overlay__title') : null
  out.facts.overlayTitle = tx(title)
  check('点开统计胶囊 ⇒ 全页浮层出现', visible(overlay), '')
  check('浮层标题为「实时统计」', out.facts.overlayTitle === '实时统计', out.facts.overlayTitle)
  // Esc 出口（浮层组件自己挂了 keydown 监听）
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  await new Promise((r) => setTimeout(r, 150))
  const closed = !visible(q('[data-testid="stage-stats-overlay"]'))
  check('Esc ⇒ 浮层关闭', closed, '')
  return out
})()`

async function main() {
  const { cdp, ws } = await connect(args.cdp)
  try {
    const { sessionId } = await openPage(cdp, 'about:blank')
    await cdp.send('Network.enable', {}, sessionId)
    await cdp.send('Runtime.enable', {}, sessionId)
    // ★ 必须**禁用缓存**：浏览器缓存里的 index.html 仍指向**旧包**（本轮实测：
    //   部署后探针 Chrome 仍加载 3045 的旧 bundle ⇒ 顶栏里「请求阶段树」不出现
    //   ⇒ 误判成「功能没打开」）。关掉缓存 ⇒ 每次都取当前在役版本。
    await cdp.send('Network.setCacheDisabled', { cacheDisabled: true }, sessionId)
    // ★ 必须把视口撑成**桌面尺寸**（1440×1400，与 maestro run-simple-overview.sh
    //   同一个口径）：默认 headless 视口是 800×600 ⇒ 应用进入 **compact 布局**
    //   （顶栏导航不渲染，只有汉堡菜单 + 底栏）⇒ 「顶栏有 /live2 入口」这条断言
    //   在小视口下**必然假红**（菜单在抽屉里，不是没有）。
    await cdp.send(
      'Emulation.setDeviceMetricsOverride',
      { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false },
      sessionId,
    )
    await cdp.send(
      'Network.setCookie',
      { name: 'llmgw_session', value: token, domain: new URL(args.url).hostname, path: '/' },
      sessionId,
    )
    await cdp.send('Page.navigate', { url: `${args.url}${args.page}` }, sessionId)
    // 等页面渲染出该页的根节点
    const readyExpr =
      args.page === '/live2'
        ? "document.querySelector('.stage-tree') !== null"
        : "document.querySelector('[data-testid=\"live-stage-tree-link\"]') !== null"
    await waitFor(cdp, sessionId, readyExpr, 30000)
    // SSE 首帧 + 过滤项候选需要一点时间（等 5 秒让实时流落数据）
    await new Promise((r) => setTimeout(r, 5000))

    const probe = await evaluate(cdp, sessionId, PAGE_PROBE)
    console.log(`== 页面内断言（${args.page}）==`)
    for (const c of probe.checks) {
      console.log(`  ${c.pass ? '✓' : '✗'} ${c.name}${c.detail ? '  —— ' + c.detail : ''}`)
    }
    console.log('  事实：', JSON.stringify(probe.facts, null, 0))

    if (args.page === '/live2') {
      const overlay = await evaluate(cdp, sessionId, OVERLAY_PROBE)
      console.log('== 统计浮层交互 ==')
      for (const c of overlay.checks) {
        console.log(`  ${c.pass ? '✓' : '✗'} ${c.name}${c.detail ? '  —— ' + c.detail : ''}`)
      }
      for (const c of overlay.checks) if (!c.pass) probe.ok = false
    }

    const failed = probe.checks.filter((c) => !c.pass).length
    console.log(failed === 0 ? '\nRESULT: PASS' : `\nRESULT: FAIL（${failed} 条）`)
    ws.close()
    process.exit(failed === 0 ? 0 : 1)
  } catch (err) {
    console.error('assert-stage-tree failed:', err?.message || err)
    try {
      ws.close()
    } catch {}
    process.exit(1)
  }
}

await main()
