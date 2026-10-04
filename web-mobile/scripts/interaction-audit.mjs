#!/usr/bin/env node
/**
 * interaction-audit.mjs —— 逐页面跑**非破坏性**交互，并对每次点击要可观察效果。
 *
 * 定位：mobile-audit.mjs 量的是「长得对不对」（静态布局与样式），
 *      本脚本量的是「点下去有没有反应」（交互行为）。
 *      两车共用 _login-driver.mjs，避免登录路径分叉。
 *
 * ⚠️ 为什么用黑名单而不是白名单：
 *   枚举出来的按钮名是运行时才知道的，白名单必然漏掉新控件（漏 = 假装覆盖了）。
 *   黑名单的代价是「可能漏判一个改状态的控件」——所以**被跳过的按钮要逐个列出**，
 *   报告里能一眼看出哪些交互没测、为什么。
 *
 * ⚠️ 判据不是「点击成功」而是「**有可观察变化**」：`el.click()` 返回不代表页面
 *   做了反应。这里每次点击都比对点击前后的 DOM 指纹（可见文字 + 节点数 + URL）。
 *
 * 用法：node scripts/interaction-audit.mjs --serial emulator-5558 --pass <密码> [--out <dir>]
 */
import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, rmSync, existsSync } from 'node:fs'
import { setTimeout as sleep } from 'node:timers/promises'
import path from 'node:path'
import { driveLogin } from './_login-driver.mjs'

const argv = process.argv.slice(2)
const arg = (n, d) => { const i = argv.indexOf('--' + n); return i >= 0 && argv[i + 1] ? argv[i + 1] : d }
const SERIAL = arg('serial', 'emulator-5558')
const ORIGIN = arg('origin', 'https://llmgateway.internal.example.com')
const BASE = arg('base', '/m')
const USER = arg('user', 'admin')
const PASS = arg('pass', '')
const OUT = arg('out', path.resolve('reports/interaction'))
const FWD = Number(arg('fwd', '9333'))
// 每页最多点几个。/models 有 118 行同形态的模型行，逐行点收益极低而耗时以小时计；
// 交互审计要验的是**形态**（弹层/路由/搜索/页签），不是穷举每一行。
// ★ 上限必须写进报告 —— 采样不是覆盖率，别让它长得像覆盖率。
const MAX_PER_ROUTE = Number(arg('max', '10'))
const ROUTES = arg('routes', '/,/models,/keys,/nodes,/alerts,/usage')
  .split(',').map((s) => s.trim()).filter(Boolean)

// 改状态的控件：宁可误伤（跳过）也不误触。
const MUTATING = new RegExp([
  'delete', 'remove', 'revoke', 'rotate', 'regenerat', 'reset', 'create', '\\badd\\b', 'new ',
  'save', 'submit', 'confirm', 'apply', 'import', 'export', 'disable', 'enable', 'unbind',
  'edit', 'modify', 'update', 'bind', 'invite', 'test-conn', 'sync',
  '删除', '吊销', '移除', '解绑', '重置', '重生成', '轮换', '创建', '新建', '新增', '添加',
  '保存', '提交', '确认', '确定', '应用', '导入', '导出', '禁用', '启用', '启用停用',
  '编辑', '修改', '更新', '绑定', '邀请', '测试连接', '同步',
].join('|'), 'i')

const adbTry = (...a) => { try { return execFileSync('adb', ['-s', SERIAL, ...a], { encoding: 'utf8' }) } catch { return '' } }
const fail = (m) => { console.error('✗ ' + m); process.exit(2) }

class CDP {
  constructor(ws) { this.ws = ws; this.id = 0; this.pending = new Map() }
  static async connect(wsUrl) {
    const ws = new WebSocket(wsUrl)
    await new Promise((res, rej) => {
      ws.addEventListener('open', res, { once: true })
      ws.addEventListener('error', () => rej(new Error('ws 连接失败')), { once: true })
    })
    const c = new CDP(ws)
    ws.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data)
      if (m.id && c.pending.has(m.id)) {
        const { res, rej } = c.pending.get(m.id); c.pending.delete(m.id)
        m.error ? rej(new Error(m.error.message)) : res(m.result)
      }
    })
    return c
  }
  // ⚠️ 25s 太少：/models 有 126 个交互元素，一次 PAGE_PROBE（逐个取 rect +
  //    innerText）+ 上一个弹窗的遮罩，会把 evaluate 拖到超时。
  //    超时不是「没反应」，是真出不来结论 —— 必须加长而不是当结果记。
  send(method, params = {}) {
    const id = ++this.id
    return new Promise((res, rej) => {
      this.pending.set(id, { res, rej })
      this.ws.send(JSON.stringify({ id, method, params }))
      setTimeout(() => { if (this.pending.has(id)) { this.pending.delete(id); rej(new Error(method + ' 超时(60s)')) } }, 60000)
    })
  }
  close() { try { this.ws.close() } catch { /* noop */ } }
}

// 页内取数：一次 evaluate 拿全，签名 + 交互元素清单。
// 每个元素打一个 data-ia-idx，之后用它在页内精确定位（重取 rect / 滚动 / 复位）。
const PAGE_PROBE = `(() => {
  const vis = (el) => { const r = el.getBoundingClientRect(); const s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' }
  const name = (el) => (el.getAttribute('aria-label') || el.innerText || el.placeholder
    || el.getAttribute('title') || el.value || '').replace(/\\s+/g, ' ').trim().slice(0, 40)
  const nodes = []
  let i = 0
  for (const el of document.querySelectorAll('button, a[href], [role="button"], [role="tab"], summary, select, input')) {
    if (!vis(el)) continue
    const r = el.getBoundingClientRect()
    el.setAttribute('data-ia-idx', String(i))
    nodes.push({ idx: i++, tag: el.tagName.toLowerCase(), type: el.type || '', name: name(el),
      disabled: !!el.disabled, w: Math.round(r.width), h: Math.round(r.height),
      inViewport: r.top >= 0 && r.bottom <= window.innerHeight,
      x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2) })
  }
  const text = (document.body.innerText || '').replace(/\\s+/g, ' ').trim()
  const dlg = document.querySelector('[role="dialog"], dialog, .modal, .sheet')
  return { url: location.pathname, sig: text.length + ':' + document.querySelectorAll('*').length + ':' + text.slice(0, 60),
    dialog: dlg ? (dlg.innerText || '').replace(/\\s+/g, ' ').trim().slice(0, 60) : null,
    vh: window.innerHeight,
    rows: document.querySelectorAll('.hyper-list__row, .card-list > *, tbody tr').length, nodes }
})()`

/**
 * 把目标滚进视口并重取坐标。
 *
 * ⚠️ 这是第三个量具坑：PAGE_PROBE 的「可见」只查了 width/height>0，
 *   **不查是否在视口内**。/models 有 118 行，前 7 行在首屏内能点到，
 *   后面 90 行的 rect.y 超过 innerHeight(850) ⇒ Input.dispatchMouseEvent
 *   点的是屏幕外坐标 ⇒ 整齐地全记成「点击后无变化」。
 *   判据没错、元素也没坏，是**没把它带到能点的地方**。
 */
const SCROLL_INTO_VIEW = `(idx) => {
  const el = document.querySelector('[data-ia-idx="' + idx + '"]')
  if (!el) return null
  el.scrollIntoView({ block: 'center', inline: 'center' })
  return true
}`
const RECHECK_POS = `(idx) => {
  const el = document.querySelector('[data-ia-idx="' + idx + '"]')
  if (!el) return null
  const r = el.getBoundingClientRect()
  const cx = Math.round(r.left + r.width / 2), cy = Math.round(r.top + r.height / 2)
  const hit = document.elementFromPoint(cx, cy)
  return JSON.stringify({
    x: cx, y: cy, vh: window.innerHeight,
    // ★ elementFromPoint 视口外直接返回 null —— 这就是「能不能点到」的正解。
    //   我第一版另加了一个 inViewport 谓词（r.top>=0 && r.bottom<=innerHeight），
    //   结果把贴边的底栏项（bottom 恰好 850.x > 850）和顶栏项全判成「不可点」——
    //   朴素谓词在边界上必然出错，而 elementFromPoint 没有这个问题。
    offscreen: hit === null,
    hitsTarget: !!(hit && (hit === el || el.contains(hit) || hit.contains(el))),
    hitsWhat: hit ? (hit.tagName.toLowerCase() + (hit.className && typeof hit.className === 'string' ? '.' + hit.className.split(' ')[0] : '')) : null
  })
}`

async function main() {
  if (!PASS) fail('需要 --pass <密码>')
  if (existsSync(OUT)) rmSync(OUT, { recursive: true, force: true })
  mkdirSync(path.join(OUT, 'shots'), { recursive: true })

  const pid = (adbTry('shell', 'pidof', 'com.kaixuan.llmgw') || '').trim().split('\n')[0]
  if (!pid) fail('com.kaixuan.llmgw 未运行（先 am start -n com.kaixuan.llmgw/.MainActivity）')
  try { adbTry('forward', '--remove', `tcp:${FWD}`) } catch { /* noop */ }
  adbTry('forward', `tcp:${FWD}`, `localabstract:webview_devtools_remote_${pid}`)

  const list = await (await fetch(`http://127.0.0.1:${FWD}/json/list`)).json()
  const pages = list.filter((t) => t.type === 'page' && t.webSocketDebuggerUrl)
  const biz = pages.filter((p) => p.url.startsWith(ORIGIN))
  if (!biz.length) fail(`devtools 里没有 ${ORIGIN} 的 page target（现有：${pages.map((p) => p.url).join(', ') || '无'}）`)
  const cdp = await CDP.connect(biz[0].webSocketDebuggerUrl)
  await cdp.send('Page.enable'); await cdp.send('Runtime.enable')

  const evalJs = async (expr) => {
    const r = await cdp.send('Runtime.evaluate', { returnByValue: true, awaitPromise: true, expression: expr })
    return r.result?.value
  }
  const nav = async (url, ms) => {
    await cdp.send('Page.navigate', { url })
    await sleep(ms || 3200)
  }
  const tap = async (x, y) => {
    for (const type of ['mousePressed', 'mouseReleased'])
      await cdp.send('Input.dispatchMouseEvent', { type, x, y, button: 'left', clickCount: 1, buttons: type === 'mousePressed' ? 1 : 0 })
  }
  /**
   * 关掉弹层，**并验证真的关掉了**。
   *
   * ⚠️ 第一版只发 Escape，实测不可靠：/models 的模型详情是自绘 sheet，
   *   Escape 不响应 ⇒ 弹层留着 ⇒ 后面所有点击都打在遮罩上 ⇒ 15 个模型行
   *   全被记成「点击后无变化」。**量具坏了，结论会整齐地错。**
   * ⇒ 依次尝试：点关闭按钮 → 点遮罩 → Escape；每次都重新探测弹层是否还在。
   *   还在就整页重载复位，并如实记下「复位失败」。
   */
  const closeOverlay = async (routeUrl) => {
    for (const attempt of ['close-btn', 'backdrop', 'escape']) {
      if (!(await evalJs(`!!document.querySelector('[role="dialog"], dialog, .modal, .sheet')`))) return { ok: true, via: attempt }
      if (attempt === 'close-btn') {
        const c = await evalJs(`(() => {
          const d = document.querySelector('[role="dialog"], dialog, .modal, .sheet'); if (!d) return null
          const b = d.querySelector('button[aria-label*="lose"], .close, [data-close], .modal__close, .sheet__close, button')
          if (!b) return null
          const r = b.getBoundingClientRect()
          return JSON.stringify({ x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2) })
        })()`)
        if (c) { const p = JSON.parse(c); await tap(p.x, p.y); await sleep(700); continue }
      }
      if (attempt === 'backdrop') {
        const c = await evalJs(`(() => {
          const d = document.querySelector('[role="dialog"], dialog, .modal, .sheet'); if (!d) return null
          const r = d.getBoundingClientRect()
          return JSON.stringify({ x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2) })
        })()`)
        if (c) { const p = JSON.parse(c); await tap(p.x, p.y); await sleep(700); continue }
      }
      await cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
      await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
      await sleep(700)
    }
    await nav(routeUrl, 2800)
    const still = await evalJs(`!!document.querySelector('[role="dialog"], dialog, .modal, .sheet')`)
    return { ok: !still, via: 'reload', failed: still }
  }

  const report = { startedAt: new Date().toISOString(), serial: SERIAL, origin: ORIGIN, routes: [] }
  report.loggedIn = await driveLogin(cdp, { origin: ORIGIN, base: BASE, user: USER, pass: PASS, nav, evalJs })
  console.log(`▸ 登录 ${report.loggedIn ? '成功' : '失败'} → ${await evalJs('location.pathname')}`)

  for (const route of ROUTES) {
    await nav(`${ORIGIN}${BASE}${route}`, 3400)
    const before = await evalJs(PAGE_PROBE)
    if (!before) { console.log(`  ✗ ${route} 取样失败`); continue }
    const r = { route, url: before.url, rowsBefore: before.rows, total: before.nodes.length, clicked: [], skipped: [], noEffect: [] }
    console.log(`\n▸ ${route}  交互元素 ${before.nodes.length} 个（行数 ${before.rows}）`)

    // 路由链接单独算「导航覆盖」，不混进本页点击 —— 点了就换页了
    const navLinks = before.nodes.filter((n) => n.tag === 'a' && /^(\/|\.\/)/.test(n.name))
    let clickable = before.nodes.filter((n) => !(n.tag === 'a' && n.name.startsWith('/')))
    // 采样：/models 有 126 个交互元素，绝大多数是同一形态的模型行。
    // 意图是验证**交互形态**而不是穷举每一行，所以按「去重后的形态」取样，
    // 并把采样比写进报告 —— 采样不是覆盖率，别让它长得像覆盖率。
    const seenShape = new Set()
    const shape = (n) => (n.tag === 'input' ? 'input' : n.name.replace(/[0-9][0-9.,]*/g, '#').slice(0, 18))
    clickable = clickable.filter((n) => { const s = shape(n); if (seenShape.has(s)) return false; seenShape.add(s); return true })
    const sampledFrom = before.nodes.filter((n) => !(n.tag === 'a' && n.name.startsWith('/'))).length
    r.sampledFrom = sampledFrom
    const beforeCap = clickable.length
    clickable = clickable.slice(0, MAX_PER_ROUTE)
    r.sampled = clickable.length
    r.cappedAt = MAX_PER_ROUTE
    r.cappedOut = beforeCap - clickable.length
    r.navLinks = navLinks.map((n) => n.name)
    console.log(`  采样 ${clickable.length} / ${sampledFrom} 个交互元素（按形态去重${beforeCap > MAX_PER_ROUTE ? `，再按每页上限 ${MAX_PER_ROUTE} 截断，余 ${beforeCap - MAX_PER_ROUTE} 个未点` : ''}）`)

    for (const el of clickable) {
      if (el.disabled) { r.skipped.push({ ...el, reason: 'disabled' }); continue }
      if (MUTATING.test(el.name)) { r.skipped.push({ ...el, reason: 'mutating' }); continue }
      // 搜索框：单独走「输入 → 行数变化 → 清空复原」，这是最有信息量的交互
      if (el.tag === 'input' && (el.type === 'text' || el.type === 'search' || !el.type)) {
        // ⚠️ 必须按**可见 input** 的序号定位：直接用 clickable 的下标去
        //    querySelectorAll('input') 取元素会取错（那个列表含不可见的）。
        const ordinal = clickable.filter((x) => x.tag === 'input').indexOf(el)
        const res = await evalJs(`(async () => {
          const vis = Array.from(document.querySelectorAll('input')).filter((e) => {
            const r = e.getBoundingClientRect(); const s = getComputedStyle(e)
            return r.width > 0 && r.height > 0 && s.display !== 'none' && s.visibility !== 'hidden' })
          const el = vis[${ordinal}]
          if (!el) return null
          const before = document.querySelectorAll('.hyper-list__row, .card-list > *, tbody tr').length
          const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
          setter.call(el, 'a')
          el.dispatchEvent(new Event('input', { bubbles: true }))
          await new Promise(r => setTimeout(r, 900))
          const filtered = document.querySelectorAll('.hyper-list__row, .card-list > *, tbody tr').length
          setter.call(el, '')
          el.dispatchEvent(new Event('input', { bubbles: true }))
          await new Promise(r => setTimeout(r, 900))
          const restored = document.querySelectorAll('.hyper-list__row, .card-list > *, tbody tr').length
          return { before, filtered, restored, focused: document.activeElement === el, label: el.placeholder || el.name || '' }
        })()`)
        if (res) {
          r.clicked.push({ ...el, kind: 'search', before: res.before, filtered: res.filtered, restored: res.restored,
            effect: res.filtered !== res.before ? '筛选改变了行数' : (res.focused ? '聚焦但行数未变' : '无反应') })
          console.log(`    ${res.filtered !== res.before ? '✓' : '·'} 搜索框「${el.name || el.placeholder}」 行数 ${res.before} → ${res.filtered} → ${res.restored}`)
        }
        continue
      }
      // 点击前确认没有残留弹层：有就先复位，**否则本次判定直接作废**
      // （点下去的是遮罩，「无变化」是量具的结论不是产品的）。
      if (await evalJs(`!!document.querySelector('[role="dialog"], dialog, .modal, .sheet')`)) {
        await closeOverlay(`${ORIGIN}${BASE}${route}`)
        await nav(`${ORIGIN}${BASE}${route}`, 2600)
        r.resetFailed = (r.resetFailed || 0) + 1
        console.log(`    ⚠ 点击前存在残留弹层，已复位；本次结果不计入`)
        continue
      }
      const pre = await evalJs(PAGE_PROBE)
      // 滚进视口 → 重取坐标 → 确认落点没被遮挡。三者任一不成立就不下「无变化」的结论。
      await evalJs(`(${SCROLL_INTO_VIEW})(${el.idx})`)
      await sleep(450)
      const posRaw = await evalJs(`(${RECHECK_POS})(${el.idx})`)
      if (!posRaw) { r.offscreen = (r.offscreen || 0) + 1; continue }
      const pos = JSON.parse(posRaw)
      if (pos.offscreen || !pos.hitsTarget) {
        r.offscreen = (r.offscreen || 0) + 1
        r.offscreenDetail = r.offscreenDetail || []
        r.offscreenDetail.push({ name: el.name, offscreen: pos.offscreen, hitsTarget: pos.hitsTarget, hitsWhat: pos.hitsWhat, y: pos.y, vh: pos.vh })
        console.log(`    ⊘ 「${el.name || '(无名称)'}」 落点${pos.offscreen ? '在视口外' : '被 ' + pos.hitsWhat + ' 覆盖'}（y=${pos.y}/${pos.vh}）→ 不下结论`)
        continue
      }
      await tap(pos.x, pos.y)
      await sleep(1300)
      const post = await evalJs(PAGE_PROBE)
      let effect, expected = false
      if (post?.dialog && !pre?.dialog) effect = '打开对话框：' + post.dialog
      else if (post?.url !== pre?.url) effect = '改变路由：' + pre.url + ' → ' + post.url
      else if (post?.sig !== pre?.sig) effect = 'DOM 变化：' + pre.sig.split(':').slice(0, 2).join('/') + ' → ' + post.sig.split(':').slice(0, 2).join('/')
      // 底栏里指向**当前页**的项：路由不变是正确行为，不是「点了没反应」。
      // 不给它单独一档，它就会被记成缺陷，而实际上它是对的。
      else if (el.tag === 'a') { effect = '底栏当前页项：路由不变属预期'; expected = true }
      else effect = null
      const rec = { ...el, effect, expected }
      if (effect) { r.clicked.push(rec); console.log(`    ${expected ? '·' : '✓'} 「${el.name || '(无名称)'}」 ${effect}`) }
      else {
        r.noEffect.push(rec)
        console.log(`    ✗ 「${el.name || '(无名称)'}」 点击后无任何可观察变化`)
      }
      // 复位：弹层必须**确认关掉**，否则下一次点击全打在遮罩上，
      // 后面每一个元素都会被整齐地记成「无反应」。
      const closed = await closeOverlay(`${ORIGIN}${BASE}${route}`)
      if (closed.failed) r.resetFailed = (r.resetFailed || 0) + 1
      await nav(`${ORIGIN}${BASE}${route}`, 2600) // 复位，保证每个按钮都在同一初始态下被测
      const leaked = await evalJs(`!!document.querySelector('[role="dialog"], dialog, .modal, .sheet')`)
      if (leaked) { r.resetFailed = (r.resetFailed || 0) + 1; console.log(`    ⚠ 复位后仍有弹层，后续判定不可信`) }
    }
    const shot = await cdp.send('Page.captureScreenshot', { format: 'png' }).catch(() => null)
    if (shot?.data) writeFileSync(path.join(OUT, 'shots', route.replace(/\//g, '_') + '.png'), Buffer.from(shot.data, 'base64'))
    r.rowsAfter = (await evalJs(PAGE_PROBE))?.rows
    report.routes.push(r)
    console.log(`  ${route}：点击 ${r.clicked.length}（有效 ${r.clicked.filter((c) => c.effect).length}）· 无反应 ${r.noEffect.length} · 跳过 ${r.skipped.length} · 导航项 ${navLinks.length}`)
  }

  const all = report.routes
  report.summary = {
    routes: all.length,
    interactiveElementsSeen: all.reduce((n, r) => n + r.total, 0),
    sampled: all.reduce((n, r) => n + (r.sampled || 0), 0),
    maxPerRoute: MAX_PER_ROUTE,
    clicked: all.reduce((n, r) => n + r.clicked.length, 0),
    effective: all.reduce((n, r) => n + r.clicked.filter((c) => c.effect && !c.expected).length, 0),
    expectedNoChange: all.reduce((n, r) => n + r.clicked.filter((c) => c.expected).length, 0),
    noEffect: all.reduce((n, r) => n + r.noEffect.length, 0),
    offscreen: all.reduce((n, r) => n + (r.offscreen || 0), 0),
    resetFailed: all.reduce((n, r) => n + (r.resetFailed || 0), 0),
    skippedMutating: all.reduce((n, r) => n + r.skipped.filter((s) => s.reason === 'mutating').length, 0),
    skippedDisabled: all.reduce((n, r) => n + r.skipped.filter((s) => s.reason === 'disabled').length, 0),
  }
  writeFileSync(path.join(OUT, 'report.json'), JSON.stringify(report, null, 2))
  cdp.close(); try { adbTry('forward', '--remove', `tcp:${FWD}`) } catch { /* noop */ }

  console.log(`\n▸ 报告 ${path.join(OUT, 'report.json')}`)
  console.log(`▸ 页面 ${report.summary.routes} · 见到交互元素 ${report.summary.interactiveElementsSeen} · 实际点击采样 ${report.summary.sampled}（每页上限 ${report.summary.maxPerRoute}）`)
  console.log(`▸ 点击 ${report.summary.clicked}（有效果 ${report.summary.effective} · 预期无变化 ${report.summary.expectedNoChange}）· 真无反应 ${report.summary.noEffect} · 落点不可用跳过 ${report.summary.offscreen} · 复位失败 ${report.summary.resetFailed}`)
  console.log(`▸ 未测：改状态 ${report.summary.skippedMutating}（黑名单）· 禁用态 ${report.summary.skippedDisabled}`)
  console.log(`ℹ 采样 ${report.summary.sampled}/${report.summary.interactiveElementsSeen} —— 这是交互**形态**覆盖，不是逐行/逐项覆盖`)
  if (report.summary.noEffect) {
    console.log('⚠ 点击后无可观察变化（需逐个判断是产品无效还是判据漏了效果）:')
    for (const r of all) for (const c of r.noEffect) console.log(`   ${r.route} 「${c.name || '(无名称)'}」 ${c.tag}${c.type ? '[' + c.type + ']' : ''}`)
  }
  console.log('ℹ 按黑名单跳过、未测的改状态控件:')
  for (const r of all) for (const s of r.skipped.filter((x) => x.reason === 'mutating')) console.log(`   ${r.route} 「${s.name || '(无名称)'}」 ${s.tag}`)
}

main().catch((e) => { console.error(e); process.exit(2) })
