// assert-stats-range-picker.mjs —— 「时间范围统一 + 统计说明收成提示符」的 CDP 断言
// （2026-11-24 统一轮交付）
//
// ★ 为什么要有这个工具（与仓内同族工具同理由）：
//   本轮改的是**交互契约**（同一个日期范围组件 / 自定义区间未被禁 / 统计说明
//   点击才展开 / 挂在刷新按钮旁）—— 这些**不是**「页面上有没有某个字」的静态
//   断言能表达的：
//     · 「自定义起止存在」 ⇒ 判据是 `.kx-dr-inputs` **渲染出来了**
//       （KxDateRangePicker 只在 `!presetsOnly` 时渲染它 ⇒ 这是「没禁自定义」的
//        **唯一**可观察证据，比断言文案可靠）；
//     · 「两页是同一个组件」 ⇒ 判据是**两页的固定档位逐字同序相等**
//       （同一 presets 工厂 ⇒ 同序同文；各写一套 ⇒ 顺序或措辞必有一处不同）；
//     · 「点击才显示」 ⇒ 判据是**点击前 popper 不可见、点击后可见**
//       （只断「元素存在」会把常驻横幅误判为提示符）；
//     · 「在刷新数据旁边」 ⇒ 判据是**DOM 同容器**（不是视觉近似）。
//   ⇒ 全部走 CDP 页面内 DOM 级断言。
//
// ★ 与 Maestro 第一层的关系：
//   Maestro flow（18-stats-range-and-scope-hint.yaml）证明「浏览器真的能打开
//   这两页并点到那个元素」；本工具证明「点完之后**语义**是对的」。
//   两层都绿才算达成预期（与 run-session-analytics.sh 的三层分工同构）。
//
// ★ 判定口径不自己写：统一走 maestro/tools/result.mjs（仓内唯一真源）。
//
// 用法：
//   node assert-stats-range-picker.mjs --cdp http://127.0.0.1:9223 \
//         --url https://llm.itestu.cn --user admin --password '...'
import { connect, openPage, evaluate, waitFor, navigateToDocument } from './cdp-client.mjs'
import { summarize } from './result.mjs'

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, v, i, a) => {
    if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
    return acc
  }, []),
)

const CDP_URL = args.cdp || 'http://127.0.0.1:9223'
const BASE = (args.url || 'http://127.0.0.1:8782').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
// ★ 口令只走环境变量（命令行参数会出现在 `ps` 输出里 ⇒ 同机其它用户可见）
const PASSWORD = args.password || process.env.ADMIN_PASSWORD || ''

const checks = []
const skipped = []
function check(name, ok, detail = '') {
  checks.push({ name, pass: !!ok, detail })
  console.log(`${ok ? '✅' : '❌'} ${name}${detail ? '  — ' + detail : ''}`)
  return !!ok
}
function gauge(name, detail = '') {
  skipped.push({ name, detail })
  console.log(`○ 未评估（量具失效） ${name}${detail ? '  — ' + detail : ''}`)
}

const { cdp } = await connect(CDP_URL)
let sessionId = (await openPage(cdp, 'about:blank')).sessionId
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

/** 在页面里求值（同步）。 */
async function js(expression) {
  return evaluate(cdp, sessionId, expression)
}

/** 导航到某 URL 并等 DOM 就绪。 */
async function goto(url, readyExpr, timeoutMs = 60000) {
  await navigateToDocument(cdp, sessionId, url, timeoutMs)
  if (readyExpr) {
    try {
      await waitFor(cdp, sessionId, readyExpr, timeoutMs)
    } catch {
      const state = await js(`({ path: location.pathname, ready: document.readyState, app: !!document.querySelector('#app') })`)
      console.log('页面条件等待超时', JSON.stringify(state))
    }
  }
  await sleep(800)
}

// ── 登录（同 assert-session-analytics.mjs 的口径：先 /api/auth/me 再 /api/auth/token）
await goto(`${BASE}/version`)
let loginStatus = await js(`fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) !== '200') {
  if (!PASSWORD) {
    console.log('❌ 无已有会话且未提供口令——先跑 seed-session.mjs 或传 --password')
    process.exit(2)
  }
  const tokenBody = JSON.stringify({ username: USER, password: PASSWORD })
  loginStatus = await js(
    `fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:${JSON.stringify(tokenBody)}}).then(r=>r.status).catch(e=>'ERR:'+e.message)`,
  )
  if (String(loginStatus) !== '200') {
    console.log(`❌ 登录失败（/api/auth/token → ${loginStatus}）——令牌/口令/端点任一不成立`)
    process.exit(2)
  }
}
await goto(`${BASE}/dashboard`, `!!document.querySelector('#app .app-layout, #app main, .kx-dr-trigger')`)
console.log(`ℹ️ 登录态就绪（${BASE}）`)

// ══════════════════════════════════════════════════════════════════════
// 探针：抓一个页面上「所有日期范围 trigger」的结构与显示值
// ══════════════════════════════════════════════════════════════════════
const PROBE_TRIGGERS = `(() => {
  const triggers = Array.from(document.querySelectorAll('.kx-dr-trigger'))
  return triggers.map((el) => {
    const preset = el.querySelector('.kx-dr-trigger__preset')
    const range = el.querySelector('.kx-dr-trigger__range')
    // 所属容器链（判「在刷新按钮旁」用）
    let chain = []
    for (let n = el.parentElement; n && chain.length < 6; n = n.parentElement) {
      chain.push((n.className && typeof n.className === 'string') ? n.className.split(/\\s+/).filter(Boolean).slice(0,3).join('.') : n.tagName.toLowerCase())
    }
    return {
      preset: preset ? preset.textContent.trim() : '',
      range: range ? range.textContent.trim() : '',
      chain,
      // 「在页头」：3 层祖先内能找到刷新按钮 ⇒ 与刷新按钮同容器
      nearRefresh: (() => {
        for (let n = el.parentElement; n; n = n.parentElement) {
          if (n.querySelector && n.querySelector('.btn-refresh, .sa-btn')) return true
          if (n === document.body) break
        }
        return false
      })(),
      // 「埋在第二块（Top10 区块）」：祖先里有 top-users-section
      inTopUsers: (() => {
        for (let n = el.parentElement; n; n = n.parentElement) {
          if (n.classList && n.classList.contains('top-users-section')) return true
          if (n === document.body) break
        }
        return false
      })(),
    }
  })
})()`

/** 展开一个 trigger（点它），返回展开后面板内的档位与自定义输入。 */
const PROBE_PANEL = `(() => {
  const triggers = Array.from(document.querySelectorAll('.kx-dr-trigger'))
  const el = triggers[0]
  if (!el) return { error: 'no-trigger' }
  el.click()
  return new Promise((resolve) => setTimeout(() => {
    const panel = document.querySelector('.kx-dr-panel')
    if (!panel) return resolve({ error: 'no-panel' })
    const presets = Array.from(panel.querySelectorAll('.kx-dr-preset')).map(p => p.textContent.trim())
    const inputs = Array.from(panel.querySelectorAll('.kx-dr-input')).map(i => {
      const label = i.querySelector('.kx-dr-input__label')
      const input = i.querySelector('input')
      return { label: label ? label.textContent.trim() : '', type: input ? input.getAttribute('type') : null }
    })
    resolve({
      presets,
      inputs,
      hasPresetsBlock: !!panel.querySelector('.kx-dr-presets'),
      hasInputsBlock: !!panel.querySelector('.kx-dr-inputs'),
    })
  }, 700))
})()`

/** 关掉展开的面板（点别处）。 */
const CLOSE_PANEL = `(() => {
  const triggers = Array.from(document.querySelectorAll('.kx-dr-trigger'))
  const el = triggers[0]
  if (el) el.click()
  document.body.click()
  return true
})()`

// ══════════════════════════════════════════════════════════════════════
// A. 仪表盘 stats 页签
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── A. /dashboard?tab=stats ────')
await goto(`${BASE}/dashboard?tab=stats`, `document.querySelectorAll('.kx-dr-trigger').length > 0`)
const statsTriggers = (await js(PROBE_TRIGGERS)) || []
check(
  'A1 stats 页签存在日期范围选择器',
  statsTriggers.length > 0,
  `找到 ${statsTriggers.length} 个 .kx-dr-trigger`,
)

let statsPanel = null
if (statsTriggers.length > 0) {
  statsPanel = await js(PROBE_PANEL)
}
check(
  'A2 stats 页签日期范围有**固定档位**',
  !!statsPanel && statsPanel.hasPresetsBlock && (statsPanel.presets || []).length >= 5,
  statsPanel ? `档位 ${(statsPanel.presets || []).length} 项：${(statsPanel.presets || []).join(' / ')}` : '未取到面板',
)
check(
  'A3 stats 页签日期范围**保留自定义起止**（.kx-dr-inputs 已渲染 ⇒ presets-only 未开）',
  !!statsPanel && statsPanel.hasInputsBlock && (statsPanel.inputs || []).length === 2,
  statsPanel ? `自定义输入 ${(statsPanel.inputs || []).length} 个：${(statsPanel.inputs || []).map(i => i.label).join(' + ')}` : '未取到面板',
)
await js(CLOSE_PANEL)

// ══════════════════════════════════════════════════════════════════════
// B. 看板 board 页签（作为「基准」）
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── B. /dashboard?tab=board ────')
await goto(`${BASE}/dashboard?tab=board`, `document.querySelectorAll('.kx-dr-trigger').length > 0`)
const boardTriggers = (await js(PROBE_TRIGGERS)) || []
let boardPanel = null
if (boardTriggers.length > 0) {
  boardPanel = await js(PROBE_PANEL)
  await js(CLOSE_PANEL)
}
check(
  'B1 看板页签存在日期范围选择器',
  boardTriggers.length > 0,
  `找到 ${boardTriggers.length} 个 .kx-dr-trigger`,
)
check(
  'B2 看板页签同样有固定档位 + 自定义起止',
  !!boardPanel && boardPanel.hasPresetsBlock && boardPanel.hasInputsBlock,
  boardPanel ? `档位 ${(boardPanel.presets || []).length} 项 / 自定义 ${(boardPanel.inputs || []).length} 个` : '未取到面板',
)

// ══════════════════════════════════════════════════════════════════════
// C. 两页档位**逐字同序相等** ⇒ 同一个组件（不是「形似」而是「同一份」）
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── C. 同一组件判定 ────')
const statsPresets = (statsPanel && statsPanel.presets) || []
const boardPresets = (boardPanel && boardPanel.presets) || []
check(
  'C1 stats 与 board 的固定档位**逐字同序相等**（⇒ 同一个组件/同一预设工厂）',
  statsPresets.length > 0 && statsPresets.length === boardPresets.length && statsPresets.every((p, i) => p === boardPresets[i]),
  `stats=[${statsPresets.join('|')}]  board=[${boardPresets.join('|')}]`,
)

// C2 两个页签的**显示值一致** ⇒ 同一份状态（round 1 的核心：区间同源）
await goto(`${BASE}/dashboard?tab=stats`, `document.querySelectorAll('.kx-dr-trigger').length > 0`)
const statsShown = ((await js(PROBE_TRIGGERS)) || []).map((t) => `${t.preset}|${t.range}`)
await goto(`${BASE}/dashboard?tab=board`, `document.querySelectorAll('.kx-dr-trigger').length > 0`)
const boardShown = ((await js(PROBE_TRIGGERS)) || []).map((t) => `${t.preset}|${t.range}`)
const statsKey = statsShown[0] || ''
const boardKey = boardShown[0] || ''
check(
  'C2 stats 与 board 的区间**显示值一致**（⇒ 共用同一份范围状态）',
  !!statsKey && statsKey === boardKey,
  `stats=${statsKey}  board=${boardKey}`,
)

// ══════════════════════════════════════════════════════════════════════
// D. 会话统计整合页（/admin/session-analytics/users/overview）
// ══════════════════════════════════════════════════════════════════════
// ★ 2026-11-25：**URL 已更新**（别的会话 28957fe55 把 `/session-analytics/users`
//   改指向用户画像列表页，会话统计整合页搬到 `/users/overview`）⇒ 本组必须打
//   整合页，否则量具过期会伪装成「页面坏了」。
// ★ 已知例外（不在统一口径）：用户画像列表页（`/session-analytics/users`）用的是
//   **datetime 精度**预设（8 档，含「近 24 小时」）⇒ 时刻窗 ≠ 另外四处的日历日窗。
console.log('\n──── D. /admin/session-analytics/users/overview ────')
await goto(`${BASE}/admin/session-analytics/users/overview`, `document.querySelectorAll('.kx-dr-trigger').length > 0`)
const saTriggers = (await js(PROBE_TRIGGERS)) || []
check(
  'D1 会话分析用户页存在日期范围选择器',
  saTriggers.length > 0,
  `找到 ${saTriggers.length} 个 .kx-dr-trigger`,
)
let saPanel = null
if (saTriggers.length > 0) {
  saPanel = await js(PROBE_PANEL)
}
check(
  'D2 会话分析用户页有**固定档位**',
  !!saPanel && saPanel.hasPresetsBlock && (saPanel.presets || []).length >= 5,
  saPanel ? `档位 ${(saPanel.presets || []).length} 项：${(saPanel.presets || []).join(' / ')}` : '未取到面板',
)
check(
  'D3 会话分析用户页**保留自定义起止**',
  !!saPanel && saPanel.hasInputsBlock && (saPanel.inputs || []).length === 2,
  saPanel ? `自定义输入 ${(saPanel.inputs || []).length} 个` : '未取到面板',
)
await js(CLOSE_PANEL)

const saPresets = (saPanel && saPanel.presets) || []
check(
  'D4 会话分析用户页的档位与 stats 页**逐字同序相等**（⇒ 三处同一组件）',
  saPresets.length > 0 && saPresets.length === statsPresets.length && saPresets.every((p, i) => p === statsPresets[i]),
  `sa=[${saPresets.join('|')}]  stats=[${statsPresets.join('|')}]`,
)
check(
  'D5 会话分析用户页的日期范围控件**在页头**（与刷新按钮同容器）',
  saTriggers.length > 0 && saTriggers[0].nearRefresh,
  saTriggers.length > 0 ? `祖先链：${saTriggers[0].chain.join(' ← ')}` : '未取到',
)
check(
  'D6 会话分析用户页的日期范围控件**不再埋在 Top10 区块里**',
  saTriggers.length > 0 && !saTriggers[0].inTopUsers,
  saTriggers.length > 0 ? `inTopUsers=${saTriggers[0].inTopUsers}` : '未取到',
)

// ══════════════════════════════════════════════════════════════════════
// E. 统计说明 = 刷新按钮旁的**点击才展开**提示符
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── E. 统计说明提示符 ────')
const PROBE_HINT = `(() => {
  const hint = document.querySelector('.stats-scope-hint')
  if (!hint) return { exists: false }
  const popper = document.querySelector('.stats-scope-hint-popper')
  const body = document.querySelector('.stats-scope-hint__body')
  const visible = (el) => {
    if (!el) return false
    const r = el.getBoundingClientRect()
    return r.width > 0 && r.height > 0
  }
  let nearRefresh = false
  for (let n = hint.parentElement; n; n = n.parentElement) {
    if (n.querySelector && n.querySelector('.btn-refresh, .sa-btn')) { nearRefresh = true; break }
    if (n === document.body) break
  }
  return {
    exists: true,
    label: (hint.textContent || '').trim(),
    popperExists: !!popper,
    popperVisible: visible(popper),
    bodyExists: !!body,
    bodyVisible: visible(body),
    bodyText: body ? body.textContent.trim().slice(0, 200) : '',
    nearRefresh,
  }
})()`

const PROBE_HINT_CLICK = `(() => {
  const hint = document.querySelector('.stats-scope-hint')
  if (!hint) return { clicked: false }
  hint.click()
  return { clicked: true }
})()`

async function probeHint(label) {
  const before = await js(PROBE_HINT)
  if (!before || !before.exists) {
    check(`${label}1 存在「统计说明」提示符`, false, '未找到 .stats-scope-hint')
    return
  }
  check(`${label}1 存在「统计说明」提示符`, true, `标签文案：${before.label}`)
  check(
    `${label}2 **点击前**说明正文不可见（⇒ 点击才展开，不是常驻横幅）`,
    !before.bodyVisible,
    `bodyExists=${before.bodyExists} bodyVisible=${before.bodyVisible}`,
  )
  await js(PROBE_HINT_CLICK)
  await sleep(800)
  const after = await js(PROBE_HINT)
  check(
    `${label}3 **点击后**说明正文可见且有实质内容`,
    after.bodyVisible && after.bodyText.includes('request_metrics'),
    `bodyVisible=${after.bodyVisible}  正文前 80 字：${(after.bodyText || '').slice(0, 80)}`,
  )
  check(
    `${label}4 提示符**在刷新按钮旁**（同容器）`,
    after.nearRefresh,
    `nearRefresh=${after.nearRefresh}`,
  )
  // 复原（收起），避免影响后续断言
  await js(PROBE_HINT_CLICK)
  await sleep(300)
}

await goto(`${BASE}/dashboard?tab=stats`, `document.querySelector('.stats-scope-hint')`)
await probeHint('E-stats ')
await goto(`${BASE}/admin/session-analytics/users/overview`, `document.querySelector('.stats-scope-hint')`)
await probeHint('E-sa ')

// ══════════════════════════════════════════════════════════════════════
// F. 运营面板（/admin/board-operational）—— 第四处并入共享组件（2026-11-25）
// ══════════════════════════════════════════════════════════════════════
// ★ 本组守两件事：
//   ① **第四处也是同一个组件** ⇒ 档位与其他三页**逐字同序相等**（此前它是第四套
//     预设：今天/近7/近30/近90、跨度上限 365 天 ⇒ 形态不同）；
//   ② ★★ **网络层**判据：页面**真的把窗口发给了接口** ⇒ 「界面显示的区间 =
//     数据窗口」不是靠文案自证，而是看真实请求 URL 里的窗口参数。
//     ★ 为什么单列一条：这一轮的核心交付就是「显示 = 数据」⇒ 必须有**独立于
//       界面文本**的证据（请求 URL）⇒ 否则文案与请求各说各话时没人发现。
console.log('\n──── F. /admin/board-operational ────')
// ★ 等「可交互」的触发器（加载期控件是 disabled ⇒ 点击无效 ⇒ 量具假红）。
await goto(
  `${BASE}/admin/board-operational`,
  `(() => { const b = document.querySelector('.kx-dr-trigger'); return !!b && !b.disabled })()`,
)
const boTriggers = (await js(PROBE_TRIGGERS)) || []
check(
  'F1 运营面板存在日期范围选择器',
  boTriggers.length > 0,
  `找到 ${boTriggers.length} 个 .kx-dr-trigger`,
)
let boPanel = null
if (boTriggers.length > 0) {
  boPanel = await js(PROBE_PANEL)
  await js(CLOSE_PANEL)
}
check(
  'F2 运营面板日期范围有**固定档位**',
  !!boPanel && boPanel.hasPresetsBlock && (boPanel.presets || []).length >= 5,
  boPanel ? `档位 ${(boPanel.presets || []).length} 项：${(boPanel.presets || []).join(' / ')}` : '未取到面板',
)
check(
  'F3 运营面板日期范围**保留自定义起止**',
  !!boPanel && boPanel.hasInputsBlock && (boPanel.inputs || []).length === 2,
  boPanel ? `自定义输入 ${(boPanel.inputs || []).length} 个` : '未取到面板',
)
const boPresets = (boPanel && boPanel.presets) || []
check(
  'F4 运营面板的档位与 stats 页**逐字同序相等**（⇒ 四处同一组件/同一预设工厂）',
  boPresets.length > 0 &&
    boPresets.length === statsPresets.length &&
    boPresets.every((p, i) => p === statsPresets[i]),
  `bo=[${boPresets.join('|')}]  stats=[${statsPresets.join('|')}]`,
)

// ★★ F5：网络层判据 —— 真实请求 URL 里的窗口参数 = 控件显示的区间。
/** 从控件显示的区间文本（"11/19 – 11/25"）推出请求应当携带的窗口参数。 */
function expectedWindowFromRangeText(text) {
  const m = /(\d{1,2})\/(\d{1,2})\s*[–—\-~]\s*(\d{1,2})\/(\d{1,2})/.exec(text || '')
  if (!m) return null
  const y = new Date().getFullYear()
  const start = new Date(y, Number(m[1]) - 1, Number(m[2]))
  let end = new Date(y, Number(m[3]) - 1, Number(m[4]))
  if (end < start) end = new Date(y + 1, Number(m[3]) - 1, Number(m[4])) // 跨年
  const fmt = (dt) =>
    `${dt.getFullYear()}-${String(dt.getMonth() + 1).padStart(2, '0')}-${String(dt.getDate()).padStart(2, '0')}`
  return { start: fmt(start), end: fmt(end), days: Math.round((end - start) / 86400000) + 1 }
}
const boReqUrls =
  (await js(`(() => performance.getEntriesByType('resource').map(e => e.name).filter(n => new URL(n).pathname === '/api/admin/dashboard/v2/board'))()`)) || []
const boReqUrl = boReqUrls[boReqUrls.length - 1] || ''
let boQs = {}
try {
  boQs = Object.fromEntries(new URL(boReqUrl).searchParams.entries())
} catch {
  boQs = {}
}
const boExpected = expectedWindowFromRangeText(boTriggers[0] ? boTriggers[0].range : '')
let boWindowOk = false
let boWindowDetail = ''
if (boExpected && boQs.start && boQs.end) {
  boWindowOk = boQs.start === boExpected.start && boQs.end === boExpected.end
  boWindowDetail = `请求=[${boQs.start}..${boQs.end}]  控件=[${boExpected.start}..${boExpected.end}]`
} else if (boExpected && boQs.days !== undefined) {
  boWindowOk = Number(boQs.days) === boExpected.days
  boWindowDetail = `请求 days=${boQs.days}  控件跨度=${boExpected.days} 天`
} else {
  boWindowDetail = `请求=${boReqUrl ? boReqUrl.split('?')[1] : '(没抓到请求)'}  控件显示=${boTriggers[0] ? boTriggers[0].range : '?'}`
}
check(
  'F5 ★ 运营面板把**控件显示的区间**发给了看板接口（⇒ 显示 = 数据窗口，网络层证据）',
  boWindowOk,
  boWindowDetail,
)

// ══════════════════════════════════════════════════════════════════════
// 汇总（口径走 result.mjs）
// ══════════════════════════════════════════════════════════════════════
const out = summarize(checks, { skipped })
console.log('\n' + out.line)
console.log(`退出码：${out.exitCode}`)
process.exit(out.exitCode)
