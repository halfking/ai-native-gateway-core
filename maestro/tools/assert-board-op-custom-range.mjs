// assert-board-op-custom-range.mjs —— 「运营面板**自定义区间**」的 CDP 断言
// （2026-11-25 统一轮的补充量具）
//
// ★ 为什么要有这个**独立**工具（而不是塞进 assert-stats-range-picker.mjs）：
//   主探针的 F 组验的是**预设档位**路径（days=7 ⇒ 请求带 days）。
//   而本轮改动最大的恰恰是**自定义区间 → start/end 透传**这条路径 ⇒ 它需要
//   「真的在浏览器里填两个日期框、点应用、看真实请求」才叫证据。
//   ⇒ 这段流程要**驱动日历组件的内部状态**（Element Plus 的 ElDatePicker），
//     步骤多、失败模式多 ⇒ 独立成工具 ⇒ 失败时不会把主探针的其它 26 条一起拖红，
//     也不会让主探针的绿被这段的量具缺陷污染。
//
// ★ 三条判据（全部是**网络层 / DOM 层**证据，不靠文案自证）：
//   G1 真实看板请求 URL 带 start/end，且等于所填起止
//   G2 触发器上**显示的区间** == 看板接口收到的窗口（⇒ 显示 = 数据）
//   G3 页面文案显示**起止日期**，且**不含「最近 N 天」**（⇒ 文案不说假话）
//
// ★ 判定口径不自己写：统一走 maestro/tools/result.mjs（仓内唯一真源）。
//   退出码：0 绿 / 1 红 / 2 量具失效。
//
// 用法：
//   node assert-board-op-custom-range.mjs --cdp http://127.0.0.1:9223 \
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

// ── 登录（与 assert-stats-range-picker.mjs 同口径：先 /api/auth/me 再 /api/auth/token）
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

// ── 运营面板
const CUSTOM_START = '2026/09/01'
const CUSTOM_END = '2026/09/10'
const CUSTOM_START_ISO = '2026-09-01'
const CUSTOM_END_ISO = '2026-09-10'

await goto(
  `${BASE}/admin/board-operational`,
  `(() => { const b = document.querySelector('.kx-dr-trigger'); return !!b && !b.disabled })()`,
)

/**
 * 往指定日期输入框写入日期值。
 * ★ 这套步骤是**逐轮实测收敛出来的**（改了四轮才对），每一步都有原因，别乱动：
 *   ① `el.focus()` + `el.select()` ⇒ 选中旧值，后续 insertText 整段替换；
 *      隐藏元素 focus 会失败 ⇒ 必须先确认 activeElement 真的是它。
 *   ② `Input.insertText` 走**真实键盘通道**（CDP 文本输入）⇒ 触发 input 事件
 *      ⇒ Element Plus 的 `userInput` 被记下（这是后续 change 解析能成功的前提之一）。
 *   ③ 按 **Enter**（rawKeyDown）⇒ 让 ElDatePicker 收掉自己的 calendar popper。
 *   ④ **显式补发 change**：Element Plus 把「解析用户输入并 emit update:modelValue」
 *      挂在 **change** 上（不是 input，也不是 keydown）⇒
 *      · CDP 的 rawKeyDown **不产生浏览器 default action** ⇒ 按 Enter 也不会自动 change；
 *      · 所以 insertText + Enter 之后**必须自己 dispatch 一个 change**，
 *        否则值只停在 DOM 里，组件状态没变，apply 提交的还是旧值。
 *   ⑤ 读回校验 value === 期望 ⇒ 填不进去就**报量具失效**，绝不静默跳过。
 *   ★ 为什么不用「原生 setter 直接写 value」：那样**不触发 input** ⇒ Element Plus 的
 *     `userInput` 没被记 ⇒ change 解析拿不到用户输入 ⇒ emit 不发生（实测：白填）。
 */
async function typeInto(selector, text) {
  const focusOk = await js(
    `(() => {
      const el = document.querySelector(${JSON.stringify(selector)});
      if (!el) return false;
      el.focus();
      if (el.select) el.select();
      return document.activeElement === el;
    })()`,
  )
  if (!focusOk) return false
  await sleep(300)
  await cdp.send('Input.insertText', { text }, sessionId)
  await sleep(500)
  await cdp.send(
    'Input.dispatchKeyEvent',
    { type: 'rawKeyDown', windowsVirtualKeyCode: 13, key: 'Enter', code: 'Enter', text: '\r' },
    sessionId,
  )
  await cdp.send(
    'Input.dispatchKeyEvent',
    { type: 'keyUp', windowsVirtualKeyCode: 13, key: 'Enter', code: 'Enter' },
    sessionId,
  )
  await sleep(800)
  const got = await js(
    `(() => { const el = document.querySelector(${JSON.stringify(selector)}); return el ? el.value : '' })()`,
  )
  if (got !== text) return false
  await js(
    `(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return false; el.dispatchEvent(new Event('change', { bubbles: true })); return true })()`,
  )
  await sleep(600)
  return true
}

// 打开日期面板（点触发器）
await js(`(() => { const t = document.querySelector('.kx-dr-trigger'); if (t) t.click(); return true })()`)
await sleep(800)

const startOk = await typeInto('[data-testid="kx-dr-input-start"] input', CUSTOM_START)
const endOk = await typeInto('[data-testid="kx-dr-input-end"] input', CUSTOM_END)

if (!startOk || !endOk) {
  gauge(
    'G1/G2/G3 自定义区间三件套',
    `日期框没填进去（start=${startOk ? 'ok' : 'fail'} end=${endOk ? 'ok' : 'fail'}）⇒ 量具失效（不是产品缺陷）`,
  )
  const out = summarize(checks, { skipped })
  console.log('\n' + out.line)
  console.log(`退出码：${out.exitCode}`)
  process.exit(out.exitCode)
}

const boardURLQuery = `performance.getEntriesByType('resource').map(e => e.name).filter(n => new URL(n).pathname === '/api/admin/dashboard/v2/board')`
const beforeCustom = ((await js(boardURLQuery)) || []).length

// 点「应用」（非 instant 模式 ⇒ 必须点应用才提交）
const applyClicked = await js(
  `(() => { const b = document.querySelector('[data-testid="kx-dr-apply"]'); if (b) b.click(); return !!b })()`,
)

// 等自定义区间触发的看板请求（带 start= 的那条）
let customUrl = ''
for (let i = 0; i < 40; i++) {
  await sleep(500)
  const urls = (await js(boardURLQuery)) || []
  const hit = urls.slice(beforeCustom).at(-1)
  if (hit) {
    customUrl = hit
    break
  }
}
let qs = {}
try {
  qs = Object.fromEntries(new URL(customUrl).searchParams.entries())
} catch {
  qs = {}
}

check(
  'G1 ★ 自定义区间真的带 start/end 发给了看板接口（⇒ 自定义路径网络层证据）',
  qs.start === CUSTOM_START_ISO && qs.end === CUSTOM_END_ISO,
  `请求=[${qs.start || '?'}..${qs.end || '?'}]  期望=[${CUSTOM_START_ISO}..${CUSTOM_END_ISO}]  apply已点=${applyClicked}`,
)

// G2：触发器上**显示的区间** == 看板接口收到的窗口
const triggerText = await js(
  `(() => { const t = document.querySelector('.kx-dr-trigger'); return t ? t.innerText.replace(/\\s+/g, ' ').trim() : '' })()`,
)
const mStart = /(\d{1,2})\/(\d{1,2})/.exec(triggerText || '')
const mEnd = /(\d{1,2})\/(\d{1,2})\s*$/.exec(triggerText || '')
const toIso = (m) =>
  m ? `${CUSTOM_START_ISO.slice(0, 4)}-${String(Number(m[1])).padStart(2, '0')}-${String(Number(m[2])).padStart(2, '0')}` : ''
const shownStart = toIso(mStart)
const shownEnd = toIso(mEnd)
check(
  'G2 ★ **触发器显示的区间** == 看板接口收到的窗口（⇒ 显示 = 数据，自定义路径）',
  !!customUrl && shownStart === qs.start && shownEnd === qs.end,
  `触发器="${triggerText}"  推出=[${shownStart || '?'}..${shownEnd || '?'}]  请求=[${qs.start || '?'}..${qs.end || '?'}]`,
)

// G3：文案说起止日期、不说「最近 N 天」
let hints = []
for (let i = 0; i < 20; i++) {
  await sleep(500)
  hints =
    (await js(
      `(() => Array.from(document.querySelectorAll('.bo-time-range-hint')).map(e => (e.textContent || '').trim()).filter(Boolean))()`,
    )) || []
  if (hints.length > 0 && hints.every((t) => t.includes(CUSTOM_START_ISO))) break
}
const hintAll = hints.join(' | ')
check(
  'G3 ★ 自定义区间时文案显示**起止日期**且**不含「最近 N 天」**（⇒ 文案不说假话）',
  hints.length > 0 &&
    hints.every((t) => t.includes(CUSTOM_START_ISO) && t.includes(CUSTOM_END_ISO)) &&
    !/最近\s*\d+\s*天/.test(hintAll),
  `${hints.length} 处提示：${hintAll.slice(0, 160)}`,
)

// ══════════════════════════════════════════════════════════════════════
// H. 逐档位对拍：7 个固定档位，每个都证明「显示的区间 == 请求的窗口」
// ══════════════════════════════════════════════════════════════════════
// ★ 为什么必须逐档位（2026-11-25 审计轮发现）：主探针的 F5 只验了**默认档位**
//   （近 7 ⇒ 请求 days=7），而 7 个档位里**有 4 个走的是 start/end 路径**
//   （昨天 / 近14天 / 本月 / 上月 —— boardRangeFromKxRange 只归位 today/7d/30d，
//   其余一律落 custom ⇒ 走 start/end）⇒ 那 4 条路径在生产上**没有独立证据**
//   （不是缺信心，是缺覆盖）⇒ 本组把 7 个档位逐个走一遍并逐个对拍。
const PRESET_IDS = ['today', 'yesterday', 'last7d', 'last14d', 'last30d', 'thisMonth', 'lastMonth']

/** 从触发器显示的区间文本（"11/19 – 11/25"）反推出应请求的窗口。 */
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

/** 幂等地确保日期面板**可见**（触发器是 toggle ⇒ 盲目点会把它关上）。 */
const PANEL_VISIBLE = `(() => {
  const p = document.querySelector('.kx-dr-panel');
  if (!p) return false;
  const r = p.getBoundingClientRect();
  const st = getComputedStyle(p);
  return r.height > 0 && r.width > 0 && st.display !== 'none' && st.visibility !== 'hidden' && st.opacity !== '0';
})()`
async function ensurePanelOpen() {
  for (let i = 0; i < 12; i++) {
    if (await js(PANEL_VISIBLE)) return true
    await js(`(() => { const t = document.querySelector('.kx-dr-trigger'); if (t) t.click(); return true })()`)
    await sleep(400)
  }
  return false
}

const presetRows = []
let presetWindowOk = true
let presetHintOk = true
let presetMeasured = 0

for (const pid of PRESET_IDS) {
  const row = { id: pid }
  const opened = await ensurePanelOpen()
  if (!opened) {
    row.note = '面板打不开（量具）'
    presetWindowOk = false
    presetRows.push(row)
    continue
  }
  const clicked = await js(
    `(() => { const b = document.querySelector('[data-testid="kx-dr-preset-${pid}"]'); if (b) { b.click(); return true; } return false })()`,
  )
  if (!clicked) {
    row.note = '档位按钮没找到（量具）'
    presetWindowOk = false
    presetRows.push(row)
    continue
  }
  await sleep(400)
  const beforePreset = ((await js(boardURLQuery)) || []).length
  // 非 instant 模式需要点击应用。按新条目计数，同一个窗口也可能重新发请求。
  await js(`(() => { const b = document.querySelector('[data-testid="kx-dr-apply"]'); if (b) b.click(); return !!b })()`)
  let url = ''
  for (let i = 0; i < 30; i++) {
    await sleep(500)
    const urls = (await js(boardURLQuery)) || []
    if (urls.length > beforePreset) {
      url = urls.at(-1)
      break
    }
  }
  let q = {}
  try {
    q = Object.fromEntries(new URL(url).searchParams.entries())
  } catch {
    q = {}
  }
  const rangeText = await js(
    `(() => { const e = document.querySelector('.kx-dr-trigger__range'); return e ? (e.textContent || '').trim() : '' })()`,
  )
  const shown = windowFromRangeText(rangeText)
  row.shown = rangeText
  row.req = q.start ? `[${q.start}..${q.end}]` : `days=${q.days}`
  if (!shown || !url) {
    row.note = !url ? '没等到请求' : '显示区间读不到'
    presetWindowOk = false
  } else if (q.start && q.end) {
    row.ok = q.start === shown.start && q.end === shown.end
    if (!row.ok) presetWindowOk = false
  } else if (q.days !== undefined) {
    row.ok = Number(q.days) === shown.days
    if (!row.ok) presetWindowOk = false
  } else {
    row.note = '请求既没带 start/end 也没带 days'
    presetWindowOk = false
  }
  // 文案判据：预设档位的文案要么说「最近 N 天」（N == 窗口跨度），要么说起止日期（== 窗口起止）
  let hintTexts = []
  for (let i = 0; i < 12; i++) {
    await sleep(400)
    hintTexts =
      (await js(
        `(() => Array.from(document.querySelectorAll('.bo-time-range-hint')).map(e => (e.textContent || '').trim()).filter(Boolean))()`,
      )) || []
    if (hintTexts.length > 0) break
  }
  const hint = hintTexts[0] || ''
  row.hint = hint
  const mDays = /最近\s*(\d+)\s*天/.exec(hint)
  const mDates = /(\d{4}-\d{2}-\d{2})\s*至\s*(\d{4}-\d{2}-\d{2})/.exec(hint)
  let hintOk = false
  if (mDays && shown) hintOk = Number(mDays[1]) === shown.days
  else if (mDates && shown) hintOk = mDates[1] === shown.start && mDates[2] === shown.end
  else if (hint === '' && shown) hintOk = true // 该区块未渲染（没有数据）⇒ 无文案可说
  row.hintOk = hintOk
  if (!hintOk) presetHintOk = false
  presetMeasured++
  presetRows.push(row)
}

check(
  'H1 ★ **7 个固定档位逐个**对拍：触发器显示的区间 == 看板接口收到的窗口（⇒ 显示 = 数据，无档位盲区）',
  presetMeasured === PRESET_IDS.length && presetWindowOk,
  presetRows
    .map((r) => `${r.id}:${r.note ? r.note : `${r.shown}→${r.req}${r.ok ? '✔' : '✘'}`}`)
    .join(' | '),
)
check(
  'H2 ★ **7 个固定档位逐个**：页面文案说的天数/起止 == 实际窗口（⇒ 文案在每个档位都不说假话）',
  presetMeasured === PRESET_IDS.length && presetHintOk,
  presetRows.map((r) => `${r.id}:${r.hint || '(无文案)'}${r.hintOk ? '✔' : '✘'}`).join(' | '),
)

const out = summarize(checks, { skipped })
console.log('\n' + out.line)
console.log(`退出码：${out.exitCode}`)
process.exit(out.exitCode)
