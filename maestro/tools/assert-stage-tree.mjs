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
// ★ 判定口径单点化（两个断言工具共用一份，见 result.mjs）。
import { summarize } from './result.mjs'

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

  // ★ 胶囊格式（设计第 3 轮口径）：「近 5 分钟 · 14 请求 · 86% 成功率 · 在途 0」
  //   ⇒ 读数**带窗口**（不只报一个总数：总数不带窗口就没有分母）。
  //   ⚠️ 成功率有两种形态：有分母时是「86% 成功率」，**不可判定**时是「— 成功率」
  //     （不冒充 0%）⇒ 正则必须同时吃下这两种，不能只认带 % 的那一种。
  const readout = tx(capsule).match(/^(.*?)\\s*·\\s*(\\d+)\\s*请求\\s*·\\s*([\\d\\u2014]+)\\s*%?\\s*成功率\\s*·\\s*在途\\s*(\\d+)$/)
  check('胶囊读数可解析（窗口 · 请求数 · 成功率 · 在途）', !!readout, tx(capsule))
  if (readout) {
    const total = Number(readout[2])
    const rateRaw = readout[3].trim()
    const inFlightRead = Number(readout[4])
    out.facts.readoutWindow = readout[1]
    out.facts.readoutTotal = total
    out.facts.readoutRate = rateRaw
    out.facts.readoutInFlight = inFlightRead
    check('胶囊读数带时间窗（不只报总数）', readout[1].length > 0, readout[1])
    check('胶囊总条数 == 页面阶段带条数', total === bands.length, readout[2] + ' vs ' + bands.length)
    check('胶囊在途数 == 带内 in_progress 徽章数', inFlightRead === isInFlight, readout[4] + ' vs ' + isInFlight)
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

/**
 * ★ 请求详情交互（2026-11-18 补）：点请求行 ⇒ 展开「请求详情」。
 *
 * 设计原型（docs/audit/2026-11-16-live阶段树原型*.html 的 `toggleBandDetail`）
 * 里点行会展开详情，线上此前**缺了这一段** ⇒ 点行毫无反应。
 * ⇒ 本探针在**页面内**点真行、读真 DOM（Maestro 的 web 驱动选不中动态
 *   `data-testid="stage-band-<requestId>"`，且 2026-11-18 实测 `<article>`
 *   行点击在 flow 里不可靠 ⇒ 交互断言放页面内，与统计浮层同一分工口径）。
 */
const DETAIL_PROBE = `(async () => {
  const q = (s) => document.querySelector(s)
  const qa = (s) => Array.from(document.querySelectorAll(s))
  const tx = (el) => ((el && el.textContent) || '').replace(/\\s+/g, ' ').trim()
  const visible = (el) => !!(el && (el.offsetWidth || el.offsetHeight || el.getClientRects().length))
  const out = { checks: [], facts: {}, skipped: [] }
  const check = (name, pass, detail) => {
    out.checks.push({ name, pass: !!pass, detail: detail === undefined ? '' : String(detail) })
  }
  const ridOf = (el) => ((el.getAttribute('data-testid') || '').replace(/^stage-band-/, ''))
  const bands = qa('article.band')
  // ★ 只取**带有效请求 id**的带：实时流里有 request_id 为空的帧
  //   （deriveStageBand: request.request_id ?? ''）⇒ 它的 href 必然为空
  //   ⇒ 「打开请求详情页」入口按设计**不渲染**（没 id 就没法拼地址）。
  //   ⇒ 探针若抓到这种带，会把「按设计不渲染」误判成「产品红」。
  const withId = bands.filter((el) => { const r = ridOf(el); return !!r && r !== 'undefined' })
  out.facts.bandsWithId = withId.length
  if (!withId.length) {
    // ★ 三态纪律：窗口内 0 条真实请求 ⇒ 这一层**没评估到**，不是「评估为失败」。
    //   ⇒ 记成 skipped（量具失效，退出码 2）⇒ 复核者不会误读成「产品缺陷」。
    out.skipped.push('请求详情交互（窗口内没有带请求 id 的真实请求）')
    return out
  }
  // ★ 每一步都**重新查询**面板节点：Vue 的 v-if 会重建 DOM ⇒ 缓存的旧引用
  //   会在下一次点击后变成 detached 节点（offsetWidth 恒 0）⇒ 断言恒假绿/假红。
  const panelOf = (rid) => q('[data-testid="stage-detail-' + rid + '"]')
  const bandOf = (rid) => q('[data-testid="stage-band-' + rid + '"]')
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  // ★ 语义真源：**带自身的 aria-expanded + 详情面板已挂载** ⇒ 「这一条是展开的」。
  //   不用 visible() 当判据：实时列表会持续滚动，被顶出视口的带即使展开，
  //   其布局盒也可能测不到 ⇒ visible() 在高负载下会**假红**（2026-11-18 实测）。
  const isOpen = (rid) => {
    const b = bandOf(rid)
    return !!(b && b.getAttribute('aria-expanded') === 'true' && panelOf(rid))
  }
  // ★ 交互前把目标**滚进视口**（真实用户也是这么做的）＋**每次重新取节点**：
  //   实时流会让列表持续增长/滚动 ⇒ 抓在手里的旧引用可能已被重排或移出视口
  //   ⇒ 点不到 / 点到别的 ⇒ **量具失效，不是产品红**。
  const clickBand = async (rid) => {
    let el = bandOf(rid)
    if (!el) return false
    try { el.scrollIntoView({ block: 'nearest' }) } catch { /* 滚不动就照旧点 */ }
    await sleep(80)
    el = bandOf(rid)
    if (!el) return false
    el.click()
    // ★ Vue 的 DOM 更新是**异步**的（nextTick）⇒ 点击后必须让出一帧。
    await sleep(220)
    return true
  }
  check('页面有请求带（点开详情的前提）', true, '带 ' + bands.length + ' 个')
  check('点开前：详情面板不存在', !q('[data-testid="stage-detail-title"]'), '')
  // ★ 取**最新的两条**（列表最新在下 ⇒ 最底部）：滚动窗口最先丢的是最老的带，
  //   最新的最不容易在断言途中被滚出 ⇒ 少一类「目标消失」的量具失效。
  const rid1 = ridOf(withId[withId.length - 1])
  const rid2 = withId.length > 1 ? ridOf(withId[withId.length - 2]) : null
  const clicked1 = await clickBand(rid1)
  if (!clicked1) {
    out.skipped.push('请求详情交互（目标带在断言前已滚出实时窗口）')
    return out
  }
  check('点请求行 ⇒ 详情面板出现', isOpen(rid1), rid1)
  const title = q('[data-testid="stage-detail-title"]')
  out.facts.detailTitle = tx(title)
  check('详情标题为「请求详情」', out.facts.detailTitle === '请求详情', out.facts.detailTitle)
  // ★ 「打开这一条请求的单页详情」入口（2026-11-18 补）：
  //   详情面板里必须有一个真链接 ⇒ 指向 /request-detail/<requestId>（单页详情）。
  const openLink = q('[data-testid="stage-detail-open"]')
  const openHref = openLink ? openLink.getAttribute('href') : ''
  check('详情里有「打开请求详情页」入口', !!openLink, openHref)
  check('入口 href 指向该请求的单页详情', openHref === '/request-detail/' + rid1, openHref)
  check('入口在新标签打开（target=_blank）', openLink?.getAttribute('target') === '_blank',
        openLink?.getAttribute('target') || '')
  const sections = ['resolve', 'reply', 'identity', 'stages', 'session']
  const missing = sections.filter((s) => !q('[data-testid="stage-detail-section-' + s + '"]'))
  check('详情五个分区齐备', missing.length === 0, missing.length ? ('缺 ' + missing.join(',')) : '')
  out.facts.detailText = tx(panelOf(rid1)).slice(0, 240)
  if (rid2) {
    const clicked2 = await clickBand(rid2)
    if (!clicked2) {
      out.skipped.push('手风琴（第二条在断言前已滚出实时窗口）')
    } else {
      check('手风琴：点第二条 ⇒ 第一条收起', !isOpen(rid1), rid1)
      check('手风琴：第二条展开', isOpen(rid2), rid2)
      await clickBand(rid2)
      check('手风琴：再点第二条 ⇒ 也收起', !isOpen(rid2), rid2)
    }
  }
  // ★ 可逆断言必须**先读当前态**再决定点几次（2026-11-18 实测假红即此因）：
  //   上一步的手风琴往返已经把「第一条是否展开」翻过 ⇒ 无脑再点一次等于
  //   「展开」而不是「收起」⇒ 断言必假红 ⇒ **量具自己有 bug，不是产品红**。
  if (!isOpen(rid1)) await clickBand(rid1)
  const openBefore = isOpen(rid1)
  await clickBand(rid1)
  check('再点同一条 ⇒ 收起（可逆）', openBefore && !isOpen(rid1),
        rid1 + ' / 收起前展开态=' + openBefore)
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

    let overlay = null
    let detail = null
    if (args.page === '/live2') {
      overlay = await evaluate(cdp, sessionId, OVERLAY_PROBE)
      console.log('== 统计浮层交互 ==')
      for (const c of overlay.checks) {
        console.log(`  ${c.pass ? '✓' : '✗'} ${c.name}${c.detail ? '  —— ' + c.detail : ''}`)
      }
      // ★ 请求详情交互（点行 ⇒ 展开）：与浮层同一分工（页面内做，flow 侧选不中动态行）。
      detail = await evaluate(cdp, sessionId, DETAIL_PROBE)
      console.log('== 请求详情交互（点请求行 ⇒ 展开详情）==')
      for (const c of detail.checks) {
        console.log(`  ${c.pass ? '✓' : '✗'} ${c.name}${c.detail ? '  —— ' + c.detail : ''}`)
      }
      console.log('  详情事实：', JSON.stringify(detail.facts, null, 0))
      if (detail.skipped?.length) {
        console.log('  未评估（量具失效，非产品红）：', detail.skipped.join('；'))
      }
    }

    // ★ 判定口径**单点化**（见 maestro/tools/result.mjs）：
    //   失败计数必须**把两层的检查都算进来**（2026-11-18 实测修）——
    //   早先只数第一层（probe.checks），浮层层的红虽被并进 `probe.ok`，
    //   但 `probe.ok` **从未被读取** ⇒ **浮层断言红时照样报「PASS」**
    //   ⇒ 「断言红」被统计口径吃掉＝等于没有断言。
    //   （设计层工具 assert-stage-tree-design.mjs 早已修过同一类缺陷。）
    const verdict = summarize(
      [
        ...probe.checks,
        ...(overlay ? overlay.checks : []),
        ...(detail ? detail.checks : []),
      ],
      // ★ skipped ＝「依赖真实请求、当前没测到」的检查（量具失效，不是产品红）。
      { skipped: detail ? detail.skipped : [] },
    )
    console.log(`\n${verdict.line}`)
    ws.close()
    process.exit(verdict.exitCode)
  } catch (err) {
    console.error('assert-stage-tree failed:', err?.message || err)
    try {
      ws.close()
    } catch {}
    process.exit(1)
  }
}

await main()
