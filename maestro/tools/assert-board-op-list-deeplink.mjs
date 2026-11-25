// assert-board-op-list-deeplink.mjs —— 「运营看板**列表页深链**」的 CDP 断言
// （2026-11-25 第 30 轮：列表页并入共享日期组件后的真机证据）
//
// ★ 为什么独立成工具（而不是塞进 assert-stats-range-picker.mjs /
//   assert-board-op-custom-range.mjs）：
//   ① 主探针验的是**运营面板页**（/admin/board-operational）的预设档位；
//      补充探针验的是同一页的**自定义区间 → start/end 透传**；
//   ⇒ 而本轮改的是**另一个页面**（/admin/board-operational/:dimension 列表页），
//      它有一条**别的页都没有**的能力：**URL 深链（start/end）直接决定初始区间**
//      ⇒ ⇒ 这条能力在生产上**没有独立证据**（不是缺信心，是缺覆盖）。
//   ② 列表页的失败模式（路由守卫 403 / 维度参数不合法 / 表格空态）会污染主探针
//      ⇒ 独立成工具 ⇒ 量具缺陷不拖累其它 26 条的绿。
//
// ★ 五条判据（全部是**网络层 / DOM 层**证据，不靠文案自证）：
//   D1 深链 ?start=&end= ⇒ 看板请求真的带这两个值（⇒ 深链到达后端）
//   D2 触发器上**显示的区间** == 请求窗口（⇒ 显示 = 数据，文案不说假话）
//   D3 深链**优先于** days（URL 同时带 days 时仍以 start/end 为准）
//   D4 无参进入 ⇒ 默认 7 天窗口（⇒ 默认档位没被并入改坏）
//   D5 页面上有**共享封装**的壳（★ 唯一能区分「并入前后」的判据：★ 旧页是裸控件 ★）
//
// ★ 判定口径不自己写：统一走 maestro/tools/result.mjs（仓内唯一真源）。
//   退出码：0 绿 / 1 红 / 2 量具失效。
//
// 用法：
//   node assert-board-op-list-deeplink.mjs --cdp http://127.0.0.1:9223 \
//         --url https://llm.itestu.cn --user admin --password '...'
import { connect, openPage, evaluate, waitFor } from './cdp-client.mjs'
import { summarize } from './result.mjs'

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, v, i, a) => {
    if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
    return acc
  }, []),
)

const CDP_URL = args.cdp || 'http://127.0.0.1:9223'
const BASE = (args.url || 'https://llm.itestu.cn').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
// ★ 口令只走环境变量（命令行参数会出现在 ps 输出里 ⇒ 同机其它用户可见）
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
const sessionId = (await openPage(cdp, 'about:blank')).sessionId
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const js = (expression) => evaluate(cdp, sessionId, expression)

async function goto(url, readyExpr, timeoutMs = 30000) {
  await js(`location.href = ${JSON.stringify(url)}; true`)
  await waitFor(cdp, sessionId, `document.readyState === 'complete'`, timeoutMs)
  if (readyExpr) {
    try {
      await waitFor(cdp, sessionId, readyExpr, timeoutMs)
    } catch {
      /* 交给断言去判红（页面没渲染出目标元素 = 产品问题，不是工具坏了） */
    }
  }
  await sleep(1000)
}

// ── 登录（与另外两个探针同口径：先 /api/auth/me 再 /api/auth/token）
await goto(`${BASE}/dashboard`, '!!document.querySelector("#app, body")')
let loginStatus = await js(`fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) !== '200') {
  if (!PASSWORD) {
    console.log('❌ 无已有会话且未提供口令——传 --password 或设 ADMIN_PASSWORD')
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
  await goto(`${BASE}/dashboard`, '!!document.querySelector("#app, body")')
}
console.log(`ℹ️ 登录态就绪（${BASE}）`)

const LIST_PATH = '/admin/board-operational/models'

/** 从触发器显示的区间文本（"11/01 – 11/07"）反推出应请求的窗口。 */
function windowFromRangeText(text) {
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

/** 打开列表页并等一条**看板请求**落地；返回该请求的 query。 */
async function boardQueryFor(url, readyExpr) {
  await goto(url, readyExpr)
  let last = ''
  for (let i = 0; i < 40; i++) {
    await sleep(500)
    const urls =
      (await js(
        `(() => performance.getEntriesByType('resource').map(e => e.name).filter(n => new URL(n).pathname === '/api/admin/dashboard/v2/board'))()`,
      )) || []
    last = urls[urls.length - 1] || last
    if (last) break
  }
  let q = {}
  try {
    q = Object.fromEntries(new URL(last).searchParams.entries())
  } catch {
    q = {}
  }
  return { q, url: last }
}

/** 读触发器上显示的区间文本（空串＝控件没渲染出来）。 */
async function shownRange() {
  return await js(
    `(() => { const e = document.querySelector('.kx-dr-trigger__range') || document.querySelector('.kx-dr-trigger'); return e ? (e.textContent || '').replace(/\\s+/g,' ').trim() : '' })()`,
  )
}

const TRIGGER_READY = `(() => { const t = document.querySelector('.kx-dr-trigger'); return !!t })()`

// ══════════════════════════════════════════════════════════════════════
// D1/D2：深链 ?start=&end= ⇒ 请求带 start/end，且显示的区间 == 请求窗口
// ══════════════════════════════════════════════════════════════════════
const DL_START = '2026-11-01'
const DL_END = '2026-11-07'

const deep = await boardQueryFor(
  `${BASE}${LIST_PATH}?start=${DL_START}&end=${DL_END}`,
  TRIGGER_READY,
)
const deepText = await shownRange()
const deepShown = windowFromRangeText(deepText)

check(
  'D1 ★ **深链 ?start=&end=** 真的把这两个值发给了看板接口（⇒ 深链到达后端，不是只在地址栏）',
  deep.q.start === DL_START && deep.q.end === DL_END,
  `请求=[${deep.q.start || '?'}..${deep.q.end || '?'}]  期望=[${DL_START}..${DL_END}]  url=${deep.url || '(无)'}`,
)

check(
  'D2 ★ 深链时**触发器显示的区间** == 请求窗口（⇒ 显示 = 数据）',
  !!deep.url && !!deepShown && deepShown.start === deep.q.start && deepShown.end === deep.q.end,
  `触发器="${deepText}"  推出=[${deepShown?.start || '?'}..${deepShown?.end || '?'}]  请求=[${deep.q.start || '?'}..${deep.q.end || '?'}]`,
)

// ══════════════════════════════════════════════════════════════════════
// D5：**用的是共享封装**（⇒ 这次「并入共享组件」真的上线了）
// ══════════════════════════════════════════════════════════════════════
// ★★ 为什么这条是**唯一能区分「并入前后」**的判据（★ 第 30 轮补记 ★★）：
//   ① 深链能力**并入之前就有**（★ 旧页用 `useRangeDays` ＋ `setCustomRange` ★）
//      ⇒ ⇒ D1/D2/D3 在**旧构建上也是绿的** ⇒ ⇒ 它们**证明不了「并入已上线」**；
//   ② 真正能把「并入前」与「并入后」区分开的，是**页面上那个壳**：
//      共享封装 `StatsDateRangePicker` 的壳 = `data-testid="stats-date-range-picker"`
//      （★ 旧页是**裸 `KxDateRangePicker`** ⇒ **没有这个壳** ★★）
//      ⇒ ⇒ 这条断言在旧构建上**会红**、在并入后的构建上**才绿**。
const sharedWrap = await js(
  `(() => !!document.querySelector('[data-testid="stats-date-range-picker"]'))()`,
)
check(
  'D5 ★ 列表页用的是**共享封装** StatsDateRangePicker（⇒ 「并入」真的上线了；★ 旧构建是裸控件 ⇒ 本条会红）',
  !!sharedWrap,
  `stats-date-range-picker=${sharedWrap ? 'present' : 'absent'}`,
)

// ══════════════════════════════════════════════════════════════════════
// D3：深链优先于 days（URL 同时带 days ⇒ 请求仍以 start/end 为准）
// ══════════════════════════════════════════════════════════════════════
const both = await boardQueryFor(
  `${BASE}${LIST_PATH}?start=${DL_START}&end=${DL_END}&days=30`,
  TRIGGER_READY,
)
check(
  'D3 ★ 深链**优先于** days（URL 同时带 start/end 与 days ⇒ 请求仍走 start/end 通道）',
  both.q.start === DL_START && both.q.end === DL_END && both.q.days === undefined,
  `请求=[${both.q.start || '?'}..${both.q.end || '?'}] days=${both.q.days ?? '(未带)'}  url=${both.url || '(无)'}`,
)

// ══════════════════════════════════════════════════════════════════════
// D4：无参进入 ⇒ 默认 7 天窗口（默认档位没被并入改坏）
// ══════════════════════════════════════════════════════════════════════
const plain = await boardQueryFor(`${BASE}${LIST_PATH}`, TRIGGER_READY)
const plainText = await shownRange()
const plainShown = windowFromRangeText(plainText)
const plainOk =
  !!plainShown && plainShown.days === 7 &&
  plain.q.start === plainShown.start && plain.q.end === plainShown.end
check(
  'D4 ★ 无参进入列表页 ⇒ 默认 7 天窗口（⇒ 并入共享组件没改坏默认档位）',
  plainOk,
  `请求=${plain.q.days !== undefined ? `days=${plain.q.days}` : `[${plain.q.start || '?'}..${plain.q.end || '?'}]`}  触发器="${plainText}"`,
)

// ★ 控件整个没渲染出来 ⇒ 量具失效（不是产品缺陷）：此时上面的判据没有评估对象。
if (!deepText && !plainText) {
  gauge(
    'D1/D2/D3/D4 深链四件套',
    '列表页上没找到日期控件（.kx-dr-trigger）⇒ 量具失效（路由守卫/维度参数/渲染失败）',
  )
}

const out = summarize(checks, { skipped })
console.log('\n' + out.line)
console.log(`退出码：${out.exitCode}`)
process.exit(out.exitCode)
