// assert-request-logs-time-ranges.mjs —— 请求日志页「时间范围 / 加载态 / 过期请求 /
//   多条件」的 CDP 断言（2026-11-25 request-logs「切换时间范围后无数据 / 超时」根修轮）
//
// ★ 为什么要有这个工具（与仓内同族工具同理由）：Maestro flow（第一层，
//   19-request-logs-time-ranges.yaml）证明「浏览器真的能点到档位、点完页面处在正确
//   状态」；但下面这些是**语义**，驱动原生的 text/id 断言表达不了：
//     · 「选『今天』查的就是今天」⇒ 判据是请求里的 from/to **等于**该档位的窗口；
//     · 「窗口没有被静默截断」⇒ 判据是响应里的 query_window_start/end **等于**请求值
//       （上一版的自动截窗正是改这两个值 ⇒ 今天 / 近 N 天恒为空）；
//     · 「列表显示的就是这次查询的结果」⇒ 判据是行数 == min(页大小, count)、
//       页头总数 == count、行时间都落在窗口内、按 ts 倒序；
//     · 「连续切换时旧请求不覆盖新结果」⇒ 判据是被取代的 fetch **被中止**（AbortError）
//       且最终界面 == 最后一次请求的结果（修前：慢的旧请求后到 ⇒ 覆盖 / 冒旧错）；
//     · 「加载态看得见」⇒ 判据是在响应被扣住期间加载块**可见、在动、有读数**，
//       且不显示「无记录」（修前是一行 visibility:hidden 的占位 ⇒ 看起来是空表）。
//
// ★ 怎么做到确定性：响应延迟不靠「挑一个慢的档位」（那会随数据量变化），
//   而是用 CDP 的 Fetch 域**扣住 /api/logs 的响应** N 毫秒 ⇒ 加载态的判据与
//   数据量、缓存冷热无关。
//
// ★ 多条件矩阵（API 层，页面上下文里带 cookie 直接调 /api/logs）：
//   {今天 / 近 7 / 14 / 30 天} × {业务 / 含探测} × {全部 / 成功 / 失败 / 限流 / 请求中}
//   × {第 1 页 / 第 2 页}，外加跨条件的不变式（各状态计数之和 == 全量计数、
//   含探测 ≥ 业务、嵌套窗口计数单调、第 2 页不比第 1 页新）。
//
// ★ 判定口径不自己写：统一走 maestro/tools/result.mjs（仓内唯一真源）。
//
// 用法：
//   node assert-request-logs-time-ranges.mjs --cdp http://127.0.0.1:9223 \
//         --url https://llm.itestu.cn --user admin
//   环境变量：ADMIN_PASSWORD（仅当无预置会话时才需要）；MATRIX_ROUNDS（默认 1）；
//             HEAVY=0 可跳过近 14 / 30 天的重窗口。
import { connect, openPage, evaluate, waitFor } from './cdp-client.mjs'
import { summarize } from './result.mjs'

const args = Object.fromEntries(
  process.argv
    .slice(2)
    .reduce((acc, v, i, a) => {
      if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
      return acc
    }, []),
)

const CDP_URL = args.cdp || 'http://127.0.0.1:9223'
const BASE = (args.url || 'http://127.0.0.1:8782').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
// ★ 口令只走环境变量（命令行参数会出现在 `ps` 输出里 ⇒ 同机其它用户可见）
const PASSWORD = process.env.ADMIN_PASSWORD || ''
const MATRIX_ROUNDS = Math.max(1, Number(args.rounds || process.env.MATRIX_ROUNDS || 1))
const HEAVY = (args.heavy ?? process.env.HEAVY ?? '1') !== '0'

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
const { sessionId } = await openPage(cdp, 'about:blank')
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

/** 在页面里求值（awaitPromise）。 */
async function js(expression) {
  return evaluate(cdp, sessionId, expression)
}

// ── 响应扣留（CDP Fetch 域）：delayLogsMs > 0 时 /api/logs 的请求被扣这么久 ──────
let delayLogsMs = 0
cdp.listeners.add((msg) => {
  if (msg.method !== 'Fetch.requestPaused' || msg.sessionId !== sessionId) return
  const { requestId } = msg.params
  const wait = delayLogsMs
  const release = () => {
    cdp.send('Fetch.continueRequest', { requestId }, sessionId).catch(() => {})
  }
  if (wait > 0) setTimeout(release, wait)
  else release()
})

/** 导航到某 URL 并等 DOM 就绪。 */
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
}

// ── 页面侧助手：界面快照 / API 调用 / fetch 流水 / 档位窗口期望 ─────────────────
// ★ 安装在**每次导航之后**（页面重载会清掉 window 上的东西）。
const PAGE_HELPERS = `(() => {
  if (window.__rlq) return 'already'
  const q = (t) => document.querySelector('[data-testid="' + t + '"]')
  const txt = (el) => (el ? el.textContent.replace(/\\s+/g, ' ').trim() : null)
  const iso = (d) => d.toISOString()
  window.__rlq = {
    ui() {
      const totalTxt = txt(q('logs-total'))
      const m = totalTxt && /共\\s*([\\d,]+)\\s*条/.exec(totalTxt)
      return {
        loadingBlock: !!q('logs-loading'),
        loadingBar: !!q('logs-loading-bar'),
        elapsed: txt(q('logs-loading-elapsed')),
        empty: txt(q('logs-empty')),
        preset: txt(q('kx-dr-trigger-preset')),
        panelOpen: !!q('kx-dr-panel'),
        rows: document.querySelectorAll('table.request-log-table tbody tr.request-log-row').length,
        total: m ? Number(m[1].replace(/,/g, '')) : null,
        probeBadge: !!q('logs-probe-scope-badge'),
        refreshing: !!document.querySelector('table.request-log-table.is-refreshing'),
        errorBanner: Array.from(document.querySelectorAll('p'))
          .map((p) => p.textContent)
          .filter((t) => /服务暂时不可用|查询超时|服务器没能完成/.test(t)),
      }
    },
    // 加载块的可见性细节（尺寸 / 动画 / 是否真在动）
    loadingDetail() {
      const block = q('logs-loading')
      if (!block) return null
      const ring = block.querySelector('.logs-loading__ring')
      const dot = block.querySelector('.logs-loading__dot')
      const sk = block.querySelector('.logs-loading__skeleton span')
      const anim = (el) => (el ? getComputedStyle(el).animationName : null)
      const rect = block.getBoundingClientRect()
      const cs = getComputedStyle(block)
      return {
        height: Math.round(rect.height),
        width: Math.round(rect.width),
        visible: rect.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && cs.opacity !== '0',
        ringAnim: anim(ring),
        dotAnim: anim(dot),
        skeletonAnim: anim(sk),
        skeletonBars: block.querySelectorAll('.logs-loading__skeleton span').length,
        title: txt(block.querySelector('.logs-loading__title')),
      }
    },
    async api(params) {
      const qs = new URLSearchParams()
      for (const [k, v] of Object.entries(params || {})) {
        if (v !== undefined && v !== null && v !== '') qs.set(k, String(v))
      }
      const t0 = Date.now()
      const r = await fetch('/api/logs?' + qs.toString(), { credentials: 'include' })
      const ms = Date.now() - t0
      let body = null
      try {
        body = await r.json()
      } catch {
        body = null
      }
      return { status: r.status, ms, body }
    },
    // 档位 ⇒ 期望窗口（浏览器本地时区的日历日）。
    // ★ 口径（2026-11-25「查询窗口 ≡ 显示窗口」轮）：页头每个档位的查询窗口都等于
    //   选择器显示的窗口，日历型档位一律到当天**结束**（23:59:59.999）——
    //   「今天 / 本月」走页面预设分支；其余走 custom 分支，选择器的日界标记
    //   「23:59」由 utils/requestLogsWindow.ts 补齐到 23:59:59.999。
    windows() {
      const now = new Date()
      const at = (y, m, d, h, mi, s, ms) => new Date(y, m, d, h, mi, s, ms)
      const dayStart = (off) => at(now.getFullYear(), now.getMonth(), now.getDate() - off, 0, 0, 0, 0)
      const endOf = (d) => at(d.getFullYear(), d.getMonth(), d.getDate(), 23, 59, 59, 999)
      const w = {
        today: { from: iso(dayStart(0)), to: iso(endOf(dayStart(0))) },
        yesterday: { from: iso(dayStart(1)), to: iso(endOf(dayStart(1))) },
        last7d: { from: iso(dayStart(6)), to: iso(endOf(dayStart(0))) },
        last14d: { from: iso(dayStart(13)), to: iso(endOf(dayStart(0))) },
        last30d: { from: iso(dayStart(29)), to: iso(endOf(dayStart(0))) },
        thisMonth: { from: iso(at(now.getFullYear(), now.getMonth(), 1, 0, 0, 0, 0)), to: iso(endOf(dayStart(0))) },
        lastMonth: {
          from: iso(at(now.getFullYear(), now.getMonth() - 1, 1, 0, 0, 0, 0)),
          to: iso(endOf(at(now.getFullYear(), now.getMonth(), 0, 0, 0, 0, 0))),
        },
      }
      return w
    },
    tz() {
      return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
    },
  }
  // fetch 流水：只记 /api/logs，记结局（成功 / 被中止 / 其它失败）与响应里的窗口、计数。
  const origFetch = window.fetch.bind(window)
  window.__rlqF = { seq: 0, log: [] }
  window.fetch = function (input, init) {
    const url = typeof input === 'string' ? input : (input && input.url) || ''
    if (!/\\/api\\/logs(\\?|$)/.test(url)) return origFetch(input, init)
    const entry = {
      id: ++window.__rlqF.seq,
      url,
      t0: Date.now(),
      state: 'pending',
      status: null,
      ms: null,
      aborted: false,
      error: null,
      body: null,
    }
    window.__rlqF.log.push(entry)
    const signal = init && init.signal
    if (signal) {
      signal.addEventListener('abort', () => {
        entry.aborted = true
      })
    }
    const p = origFetch(input, init)
    p.then(
      async (r) => {
        entry.status = r.status
        entry.ms = Date.now() - entry.t0
        entry.state = 'done'
        try {
          entry.body = await r.clone().json()
        } catch (e) {
          entry.parseError = String(e)
        }
      },
      (e) => {
        entry.ms = Date.now() - entry.t0
        entry.state = 'rejected'
        entry.error = String((e && e.name) || e)
      },
    )
    return p
  }
  return 'installed'
})()`

async function installHelpers() {
  await waitFor(cdp, sessionId, `!!document.querySelector('[data-testid="kx-dr-trigger"]')`, 30000)
  return js(PAGE_HELPERS)
}

const ui = () => js('window.__rlq.ui()')
/**
 * 触发器范围文案首尾相差的天数。文案形态（shortRangeLabel）：同年 'MM/DD HH:mm – MM/DD HH:mm'，
 * 跨年带年份 'YYYY/MM/DD HH:mm – …'；无年份的按当年解释（跨年且不带年份时不成立 ⇒ NaN ⇒ 判红）。
 */
function rangeTextSpanDays(text) {
  const m = /(?:(\d{4})\/)?(\d{2})\/(\d{2}) (\d{2}):(\d{2})\s*[^\d]+\s*(?:(\d{4})\/)?(\d{2})\/(\d{2}) (\d{2}):(\d{2})/.exec(text || '')
  if (!m) return Number.NaN
  const thisYear = new Date().getFullYear()
  const a = new Date(+(m[1] || thisYear), +m[2] - 1, +m[3], +m[4], +m[5]).getTime()
  const b = new Date(+(m[6] || thisYear), +m[7] - 1, +m[8], +m[9], +m[10]).getTime()
  return Math.abs(b - a) / 86400000
}
const api = (params) => js(`window.__rlq.api(${JSON.stringify(params)})`)
const fetchLog = () => js('window.__rlqF.log.map((e) => ({ id: e.id, url: e.url, state: e.state, status: e.status, ms: e.ms, aborted: e.aborted, error: e.error, count: e.body ? e.body.count : null, items: e.body && e.body.items ? e.body.items.length : null, window: e.body ? { start: e.body.query_window_start, end: e.body.query_window_end } : null, clamped: e.body ? "window_clamped" in e.body : null, lane: e.body ? e.body.traffic_lane : null }))')
const fetchLogLen = () => js('window.__rlqF.log.length')

/** 等第 idx 条（0 基）fetch 记录完成（响应体已读）。 */
async function waitEntry(idx, timeoutMs = 150000) {
  return waitFor(
    cdp,
    sessionId,
    `(() => { const e = window.__rlqF.log[${idx}]; return !!(e && (e.state === 'done' ? !!e.body || e.parseError : e.state === 'rejected')) })()`,
    timeoutMs,
    200,
  )
}
/** 等界面收尾：加载条消失且没有在途的 /api/logs。 */
async function waitSettled(timeoutMs = 150000) {
  await waitFor(
    cdp,
    sessionId,
    `!document.querySelector('[data-testid="logs-loading-bar"]') && !window.__rlqF.log.some((e) => e.state === 'pending')`,
    timeoutMs,
    200,
  )
  await sleep(250)
}

/** 通过真实点击选一个档位；返回这次触发的 fetch 在流水里的下标。 */
async function pickPreset(presetId) {
  await js(`document.querySelector('[data-testid="kx-dr-trigger"]').click(); true`)
  await waitFor(cdp, sessionId, `!!document.querySelector('[data-testid="kx-dr-panel"] [data-testid="${presetId}"]')`, 10000, 100)
  const chip = await js(`document.querySelector('[data-testid="kx-dr-panel"] [data-testid="${presetId}"]').textContent.trim()`)
  const before = await fetchLogLen()
  await js(`document.querySelector('[data-testid="kx-dr-panel"] [data-testid="${presetId}"]').click(); true`)
  return { before, chip }
}

// ══════════════════════════════════════════════════════════════════════
// 0. 登录态（预置会话优先；没有才用口令——口令路径受 captcha 闸门约束）
// ══════════════════════════════════════════════════════════════════════
await goto(`${BASE}/dashboard`, '!!document.querySelector("#app, body")')
let meStatus = await js(`fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(meStatus) !== '200') {
  if (!PASSWORD) {
    console.log('❌ 无已有会话且未提供口令（ADMIN_PASSWORD）——先经 runner 的 CDP 预置会话')
    process.exit(2)
  }
  const tokenBody = JSON.stringify({ username: USER, password: PASSWORD })
  meStatus = await js(
    `fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:${JSON.stringify(tokenBody)}}).then(r=>r.status).catch(e=>'ERR:'+e.message)`,
  )
  if (String(meStatus) !== '200') {
    console.log(`❌ 登录失败（/api/auth/token → ${meStatus}）`)
    process.exit(2)
  }
}
console.log(`ℹ️ 登录态就绪（${BASE}）`)

// 时区：档位的日期串按**系统时区**算，再按**浏览器时区**解释成时刻。两者不同时，
// 「期望窗口」算不准 ⇒ 相关断言记为量具失效（不是产品缺陷）。
const sysTz = await js(`fetch('/api/admin/system-timezone',{credentials:'include'}).then(r=>r.json()).then(j=>j.timezone||'').catch(()=>'')`)
const browserTz = await js(`Intl.DateTimeFormat().resolvedOptions().timeZone || ''`)
const tzAligned = !sysTz || sysTz === browserTz
if (!tzAligned) console.log(`ℹ️ 系统时区（${sysTz}）≠ 浏览器时区（${browserTz}）⇒ 档位窗口的精确对拍记为量具失效`)

await cdp.send('Fetch.enable', { patterns: [{ urlPattern: '*/api/logs*' }] }, sessionId)

// ══════════════════════════════════════════════════════════════════════
// A. 首屏加载态（响应被扣住 ⇒ 确定性）：加载块可见、在动、有读数；不显示「无记录」
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── A. 首屏加载态（扣住响应 6000ms）────')
// ★ 导航前把延迟设好：页面脚本一加载就会发首个 /api/logs ⇒ 被扣住 ⇒ 加载块稳定可见。
delayLogsMs = 6000
await goto(`${BASE}/request-logs`, null, 30000)
await installHelpers()
let seenBlock = false
try {
  await waitFor(cdp, sessionId, `!!document.querySelector('[data-testid="logs-loading"]')`, 4000, 100)
  seenBlock = true
} catch {
  /* 下面按「没看到加载块」判 */
}
let e0 = ''
let u1 = null
if (seenBlock) {
  const d0 = await js('window.__rlq.loadingDetail()')
  e0 = (await ui()).elapsed || ''
  await sleep(1300)
  const d1 = await js('window.__rlq.loadingDetail()')
  u1 = await ui()
  check('A1 首屏查询在途时加载块可见（高度 ≥ 300px）', !!d0 && d0.visible && d0.height >= 300, d0 ? `高 ${d0.height}px / 宽 ${d0.width}px` : '加载块未渲染')
  check('A2 加载块里有动画（转轮 / 脉冲点 / 骨架条都在动）', !!d0 && [d0.ringAnim, d0.dotAnim, d0.skeletonAnim].every((a) => a && a !== 'none'), d0 ? `${d0.ringAnim} / ${d0.dotAnim} / ${d0.skeletonAnim}` : '')
  const n0 = Number((/(\d+)/.exec(e0) || [])[1])
  const n1 = Number((/(\d+)/.exec((u1 && u1.elapsed) || '') || [])[1])
  check('A3 加载块带「已等待 N 秒」读数且在走', /已等待 \d+ 秒/.test(e0) && /已等待 \d+ 秒/.test((u1 && u1.elapsed) || '') && n1 >= n0, `${e0} → ${u1 && u1.elapsed}`)
  check('A4 顶部进度条在途时可见', !!u1 && u1.loadingBar)
  check('A5 在途时不显示「无记录」', !!u1 && !u1.empty)
  check('A5b 骨架条渲染了 3 条、主文案在场', !!d0 && d0.skeletonBars === 3 && !!d0.title, d0 ? `bars=${d0.skeletonBars} title=${d0.title}` : '')
} else {
  check('A1 首屏查询在途时加载块可见（高度 ≥ 300px）', false, '响应被扣住 6s 期间没有出现 [data-testid=logs-loading]')
}
// 放行后收尾
delayLogsMs = 0
await waitSettled(150000)
const uA = await ui()
check('A6 加载完成后加载块与进度条撤掉', !uA.loadingBlock && !uA.loadingBar)
check('A7 默认档（近 24 小时）加载完成且无报错', uA.errorBanner.length === 0 && (!uA.empty || uA.total === 0), `rows=${uA.rows} total=${uA.total}`)
// A8：默认档「近 24 小时」在选择器上显示的就是这个档（修前显示成「自定义 ｜ 24 天」）。
//    语言无关的判法：拿触发器上的档名与面板里 last24h 档位的文字逐字比。
{
  await js(`document.querySelector('[data-testid="kx-dr-trigger"]').click(); true`)
  await waitFor(cdp, sessionId, `!!document.querySelector('[data-testid="kx-dr-panel"] [data-testid="kx-dr-preset-last24h"]')`, 10000, 100)
  const chip24 = await js(`document.querySelector('[data-testid="kx-dr-panel"] [data-testid="kx-dr-preset-last24h"]').textContent.trim()`)
  const range24 = await js(`(() => { const r = document.querySelector('.kx-dr-trigger__range'); return r ? r.textContent.trim() : '' })()`)
  const labelDefault = uA.preset
  // 关掉面板（点触发器）
  await js(`document.querySelector('[data-testid="kx-dr-trigger"]').click(); true`)
  check('A8 默认档在选择器上显示为「近 24 小时」档（不是「自定义 ｜ 24 天」）', labelDefault === chip24, `触发器=${labelDefault} / 档位=${chip24} / 范围=${range24}`)
  const spanDays = rangeTextSpanDays(range24)
  check('A9 默认档显示的范围跨度 ≈ 1 天（修前是 24 天）', Number.isFinite(spanDays) && spanDays > 0.9 && spanDays < 1.1, `范围=${range24}（${Number.isFinite(spanDays) ? spanDays.toFixed(2) + ' 天' : '解析不出'}）`)
}

// ══════════════════════════════════════════════════════════════════════
// B. 八个档位逐个切换：请求窗口 == 档位窗口；响应窗口 == 请求窗口；界面 == 响应
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── B. 档位逐个切换（业务口径）────')
const expectedWindows = await js('window.__rlq.windows()')
const PRESETS = ['today', 'yesterday', 'last24h', 'last7d', 'last14d', 'last30d', 'thisMonth', 'lastMonth']
const firstCountByPreset = {}
for (const id of PRESETS) {
  const heavy = id === 'last14d' || id === 'last30d'
  if (heavy && !HEAVY) {
    gauge(`B.${id}`, 'HEAVY=0 ⇒ 跳过近 14 / 30 天的重窗口')
    continue
  }
  const { before, chip } = await pickPreset(`kx-dr-preset-${id}`)
  try {
    await waitEntry(before, 150000)
  } catch (e) {
    check(`B.${id} 查询收尾`, false, `150s 内没有完成：${e.message}`)
    continue
  }
  await waitSettled(150000)
  const log = await fetchLog()
  const e = log[before]
  const state = await ui()
  const qs = new URL(e.url, BASE).searchParams
  const from = qs.get('from')
  const to = qs.get('to')
  const budget = 60000

  check(`B.${id} 请求成功（HTTP 200）`, e.status === 200, `status=${e.status} ${e.error || ''}`)
  check(`B.${id} 在预算内完成`, e.ms != null && e.ms < budget, `${e.ms}ms（预算 ${budget / 1000}s）`)
  check(`B.${id} 没有报错横幅`, state.errorBanner.length === 0, state.errorBanner.join(' | '))
  // 请求窗口 == 档位窗口（近 24 小时是滚动窗 ⇒ 只验跨度）
  if (id === 'last24h') {
    const span = Date.parse(to) - Date.parse(from)
    check('B.last24h 请求窗口跨度 = 24h', span === 24 * 3600 * 1000, `${from} → ${to}`)
  } else if (tzAligned) {
    const w = expectedWindows[id]
    check(`B.${id} 请求窗口 == 档位窗口`, Date.parse(from) === Date.parse(w.from) && Date.parse(to) === Date.parse(w.to), `${from} → ${to}`)
  } else {
    gauge(`B.${id} 请求窗口 == 档位窗口`, '系统时区 ≠ 浏览器时区')
  }
  // 响应窗口 == 请求窗口（自动截窗已撤销的直接证据）
  check(
    `B.${id} 响应窗口 == 请求窗口（没有被静默截断）`,
    !!e.window && Date.parse(e.window.start) === Date.parse(from) && Date.parse(e.window.end) === Date.parse(to) && e.clamped === false,
    e.window ? `${e.window.start} → ${e.window.end}` : '无响应体',
  )
  // 界面 == 响应
  const items = await js(`(window.__rlqF.log[${before}].body.items || []).length`)
  const count = await js(`window.__rlqF.log[${before}].body.count`)
  check(`B.${id} 触发器显示的正是点中的档位`, state.preset === chip, `触发器=${state.preset} / 档位=${chip}`)
  check(`B.${id} 页头总数 == 响应 count`, state.total === count, `页头=${state.total} / count=${count}`)
  check(`B.${id} 表格行数 == min(页大小, count)`, state.rows === Math.min(50, count), `rows=${state.rows} / count=${count}`)
  check(`B.${id} 空态与 count 一致`, count > 0 ? !state.empty : state.empty === '无记录', `count=${count} empty=${state.empty}`)
  const tsOk = await js(`(() => { const b = window.__rlqF.log[${before}].body; const f = Date.parse(${JSON.stringify(from)}), t = Date.parse(${JSON.stringify(to)}); const it = b.items || []; let inWin = true, desc = true; for (let i = 0; i < it.length; i++) { const x = Date.parse(it[i].ts); if (x < f || x > t) inWin = false; if (i > 0 && Date.parse(it[i-1].ts) < x) desc = false } return { inWin, desc, n: it.length } })()`)
  check(`B.${id} 行时间都落在窗口内且按时间倒序`, tsOk.inWin && tsOk.desc, `n=${tsOk.n}`)
  if (count === 0) gauge(`B.${id} 有数据`, '该窗口内确实没有请求（不是「有数据却显示不出来」）')
  else check(`B.${id} 有数据`, true, `count=${count}`)
  firstCountByPreset[id] = count
  console.log(`   ℹ️ ${id}: ${e.ms}ms count=${count} rows=${state.rows}`)
}
// 嵌套窗口计数单调（同口径、无过滤）：今天 ≤ 近 7 天 ≤ 近 14 天 ≤ 近 30 天
if (['today', 'last7d', 'last14d', 'last30d'].every((k) => typeof firstCountByPreset[k] === 'number')) {
  const c = firstCountByPreset
  check('B.嵌套窗口计数单调：今天 ≤ 近7天 ≤ 近14天 ≤ 近30天', c.today <= c.last7d && c.last7d <= c.last14d && c.last14d <= c.last30d, `${c.today} ≤ ${c.last7d} ≤ ${c.last14d} ≤ ${c.last30d}`)
}

// ══════════════════════════════════════════════════════════════════════
// C. 连续快速切换：近 14 天 → 近 7 天 → 今天（中间不等）
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── C. 连续快速切换（近14天 → 近7天 → 今天）────')
const c0 = await fetchLogLen()
const p14 = await pickPreset('kx-dr-preset-last14d')
const p7 = await pickPreset('kx-dr-preset-last7d')
const p1 = await pickPreset('kx-dr-preset-today')
try {
  await waitEntry(c0 + 2, 150000)
} catch (e) {
  check('C.最终请求收尾', false, e.message)
}
await waitSettled(150000)
const cLog = await fetchLog()
const c14 = cLog[c0]
const c7 = cLog[c0 + 1]
const c1 = cLog[c0 + 2]
check('C1 三次切换各发出一次 /api/logs', cLog.length >= c0 + 3 && !!c14 && !!c7 && !!c1, `新增 ${cLog.length - c0} 条`)
check('C2 被取代的请求（近14天、近7天）已被中止，不是跑完后再覆盖', (c14 && (c14.aborted || c14.error === 'AbortError')) && (c7 && (c7.aborted || c7.error === 'AbortError')), `14d: aborted=${c14 && c14.aborted} err=${c14 && c14.error} / 7d: aborted=${c7 && c7.aborted} err=${c7 && c7.error}`)
const cState = await ui()
check('C3 最终停在「今天」且与它的响应一致', c1 && c1.status === 200 && cState.preset === p1.chip && cState.total === c1.count && cState.rows === Math.min(50, c1.count), `触发器=${cState.preset} 页头=${cState.total} / count=${c1 && c1.count} rows=${cState.rows}`)
check('C4 没有旧请求的报错冒出来、没有加载态残留', cState.errorBanner.length === 0 && !cState.loadingBar && !cState.loadingBlock, cState.errorBanner.join(' | '))
const sig1 = `${cState.preset}|${cState.total}|${cState.rows}`
await sleep(5000)
const cState2 = await ui()
check('C5 等待 5s 后界面不变（没有过期响应回来覆盖）', `${cState2.preset}|${cState2.total}|${cState2.rows}` === sig1 && cState2.errorBanner.length === 0, `${sig1} → ${cState2.preset}|${cState2.total}|${cState2.rows}`)

// ══════════════════════════════════════════════════════════════════════
// D. 已有行时刷新（响应被扣住）：不遮住现有内容，只走进度条 + 淡化
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── D. 刷新态（扣住响应 3000ms，已有行）────')
delayLogsMs = 3000
const dBefore = await fetchLogLen()
await pickPreset('kx-dr-preset-last7d')
try {
  await waitFor(cdp, sessionId, `!!document.querySelector('[data-testid="logs-loading-bar"]')`, 2500, 100)
  const uD = await ui()
  check('D1 刷新时进度条可见、表格淡化（is-refreshing）', uD.loadingBar && uD.refreshing)
  check('D2 刷新时不弹整块加载态、现有行仍在（不遮住内容）', !uD.loadingBlock && uD.rows > 0, `rows=${uD.rows}`)
  check('D3 刷新时没有报错', uD.errorBanner.length === 0)
} catch (e) {
  check('D1 刷新时进度条可见、表格淡化（is-refreshing）', false, `没观察到加载条：${e.message}`)
}
delayLogsMs = 0
try {
  await waitEntry(dBefore, 150000)
} catch (e) {
  check('D4 刷新请求收尾', false, e.message)
}
await waitSettled(150000)
const uD2 = await ui()
check('D4 刷新完成后恢复：进度条撤、淡化撤、无报错', !uD2.loadingBar && !uD2.refreshing && !uD2.loadingBlock && uD2.errorBanner.length === 0 && uD2.rows > 0, `rows=${uD2.rows} total=${uD2.total}`)

// ══════════════════════════════════════════════════════════════════════
// E. 「包含探测」口径：徽标出现、traffic_lane=all、窗口原样、有数据
// ══════════════════════════════════════════════════════════════════════
console.log('\n──── E. 包含探测口径 ────')
await js(`document.querySelector('[data-testid="logs-probe-scope"]').click(); true`)
try {
  await waitFor(cdp, sessionId, `!!document.querySelector('[data-testid="logs-probe-scope-badge"]')`, 10000, 100)
} catch {
  /* 由断言判红 */
}
await waitSettled(150000)
const eLog0 = await fetchLog()
const eToggle = eLog0[eLog0.length - 1]
const eState0 = await ui()
check('E1 打开开关 ⇒ 徽标出现、响应 traffic_lane=all', !!eState0.probeBadge && !!eToggle && eToggle.lane === 'all', `badge=${eState0.probeBadge} lane=${eToggle && eToggle.lane}`)
check('E2 开关触发的重查成功且无报错', eToggle && eToggle.status === 200 && eState0.errorBanner.length === 0, eState0.errorBanner.join(' | '))
const bizCountToday = firstCountByPreset.today
for (const id of ['today', 'last14d']) {
  if (id === 'last14d' && !HEAVY) continue
  const { before, chip } = await pickPreset(`kx-dr-preset-${id}`)
  try {
    await waitEntry(before, 150000)
  } catch (e) {
    check(`E.${id} 查询收尾`, false, e.message)
    continue
  }
  await waitSettled(150000)
  const log = await fetchLog()
  const e = log[before]
  const state = await ui()
  const qs = new URL(e.url, BASE).searchParams
  check(`E.${id} 请求带 include_probe=1 且在预算（120s）内`, qs.get('include_probe') === '1' && e.ms < 120000, `${e.ms}ms`)
  check(`E.${id} 响应窗口 == 请求窗口`, !!e.window && Date.parse(e.window.start) === Date.parse(qs.get('from')) && Date.parse(e.window.end) === Date.parse(qs.get('to')), e.window ? `${e.window.start} → ${e.window.end}` : '')
  check(`E.${id} 触发器 == 点中的档位、界面 == 响应、无报错`, state.preset === chip && state.total === e.count && state.rows === Math.min(50, e.count) && state.errorBanner.length === 0, `count=${e.count} rows=${state.rows}`)
  check(`E.${id} 有数据`, e.count > 0, `count=${e.count}`)
  if (id === 'today' && typeof bizCountToday === 'number') {
    check('E.今天：含探测计数 ≥ 业务计数', e.count >= bizCountToday, `含探测 ${e.count} ≥ 业务 ${bizCountToday}`)
  }
  console.log(`   ℹ️ 含探测 ${id}: ${e.ms}ms count=${e.count}`)
}
// 关回业务口径（给后面的矩阵留干净起点）
await js(`document.querySelector('[data-testid="logs-probe-scope"]').click(); true`)
await waitSettled(150000)

// ══════════════════════════════════════════════════════════════════════
// F. 多条件矩阵（API 层，页面上下文带 cookie）—— 逐轮、逐条件
// ══════════════════════════════════════════════════════════════════════
console.log(`\n──── F. 多条件矩阵（${MATRIX_ROUNDS} 轮）────`)
const W = await js('window.__rlq.windows()')
const STATUSES = ['', 'success', 'failure', 'rate_limited', 'in_progress']
const MATRIX_IDS = ['today', 'last7d', 'last14d', 'last30d'].filter((k) => HEAVY || (k !== 'last14d' && k !== 'last30d'))
const maxMs = { biz: 0, probe: 0 }
// 不变式校验（纯函数，便于自检）：逐条都能说出「在哪种坏法下会红」。
function responseOk(r, win, status, probe) {
  const b = r.body || {}
  const items = b.items || []
  const budget = probe ? 120000 : 60000
  const f = Date.parse(win.from)
  const t = Date.parse(win.to)
  return (
    r.status === 200 &&
    Number.isFinite(b.count) &&
    b.count >= 0 &&
    items.length === Math.min(50, Math.max(0, b.count)) &&
    Date.parse(b.query_window_start) === f &&
    Date.parse(b.query_window_end) === t &&
    !('window_clamped' in b) &&
    (status ? items.every((it) => it.request_status === status) : true) &&
    items.every((it, i) => {
      const x = Date.parse(it.ts)
      return x >= f && x <= t && (i === 0 || Date.parse(items[i - 1].ts) >= x)
    }) &&
    r.ms < budget
  )
}
// ★ 活流量漂移容差：窗口结束于「今天结束」的查询是**开放**的——两次请求之间会有新
//   请求落库 ⇒ 第 2 页请求的 count 比第 1 页略大（211 实测几秒内 +1 ~ +22）。
//   逐字相等会把活流量判成缺陷（假红）。容差 = max(100, 0.1%)：结构性错误（口径漏了
//   一整类状态）是成千上万的差距，容差不掩盖它。
function driftOk(a, b) {
  return Math.abs((a || 0) - (b || 0)) <= Math.max(100, Math.ceil(Math.max(a || 0, b || 0) * 0.001))
}
function page2Ok(r1, r2, win) {
  const i1 = (r1.body && r1.body.items) || []
  const i2 = (r2.body && r2.body.items) || []
  // 第 2 页该有几行，以**第 2 页自己那次请求**的 count 为准（漂移只增不减）。
  const count = r2.body ? r2.body.count : undefined
  return (
    r1.status === 200 &&
    r2.status === 200 &&
    r2.body &&
    r2.body.count >= r1.body.count &&
    driftOk(r1.body.count, r2.body.count) &&
    Date.parse(r2.body.query_window_start) === Date.parse(win.from) &&
    Date.parse(r2.body.query_window_end) === Date.parse(win.to) &&
    i2.length === Math.min(50, Math.max(0, count - 50)) &&
    i2.every((it, i) => {
      const x = Date.parse(it.ts)
      return (
        x >= Date.parse(win.from) &&
        x <= Date.parse(win.to) &&
        (i === 0 || Date.parse(i2[i - 1].ts) >= x) &&
        (i1.length === 0 || x <= Date.parse(i1[i1.length - 1].ts))
      )
    })
  )
}
for (let round = 1; round <= MATRIX_ROUNDS; round++) {
  const counts = { 0: {}, 1: {} }
  for (const id of MATRIX_IDS) {
    const win = W[id]
    for (const probe of [0, 1]) {
      const byStatus = {}
      const details = []
      let slowest = 0
      let page1 = null
      for (const status of STATUSES) {
        const r = await api({ from: win.from, to: win.to, include_probe: probe ? 1 : '', request_status: status, page: 1, page_size: 50 })
        slowest = Math.max(slowest, r.ms)
        byStatus[status || 'all'] = r.body ? r.body.count : NaN
        if (!status) page1 = r
        if (!responseOk(r, win, status, probe)) {
          const b = r.body || {}
          details.push(`${status || 'all'}: status=${r.status} count=${b.count} items=${(b.items || []).length} ${r.ms}ms`)
        }
      }
      const page2 = await api({ from: win.from, to: win.to, include_probe: probe ? 1 : '', page: 2, page_size: 50 })
      slowest = Math.max(slowest, page2.ms)
      if (!page2Ok(page1, page2, win)) {
        details.push(`分页: p1=${(page1.body.items || []).length} p2=${(page2.body.items || []).length} count=${page1.body.count}/${page2.body.count}`)
      }
      // 跨条件不变式：各状态计数之和 == 全量计数（口径完备、不重不漏；带活流量漂移容差）
      const statusSum = STATUSES.filter((k) => k).reduce((acc, k) => acc + byStatus[k], 0)
      const sumOk = driftOk(statusSum, byStatus.all)
      if (!sumOk) details.push(`状态计数之和 ${statusSum} ≠ 全量 ${byStatus.all}`)
      maxMs[probe ? 'probe' : 'biz'] = Math.max(maxMs[probe ? 'probe' : 'biz'], slowest)
      counts[probe][id] = byStatus.all
      check(
        `F.r${round} ${id} ${probe ? '含探测' : '业务'}：五种状态过滤 × 第 1/2 页 全部成立`,
        details.length === 0 && sumOk,
        `count(all)=${byStatus.all} 最慢 ${slowest}ms${details.length ? ' ✗ ' + details.join(' ; ') : ''}`,
      )
    }
  }
  const mono = (m) => MATRIX_IDS.every((k, i) => i === 0 || m[MATRIX_IDS[i - 1]] <= m[k])
  check(`F.r${round} 嵌套窗口计数单调（业务 & 含探测）`, mono(counts[0]) && mono(counts[1]), `业务 ${MATRIX_IDS.map((k) => counts[0][k]).join(' ≤ ')} / 含探测 ${MATRIX_IDS.map((k) => counts[1][k]).join(' ≤ ')}`)
  check(
    `F.r${round} 同窗口含探测计数 ≥ 业务计数`,
    MATRIX_IDS.every((k) => counts[1][k] >= counts[0][k]),
    MATRIX_IDS.map((k) => `${k}: ${counts[1][k]} ≥ ${counts[0][k]}`).join(' / '),
  )
}
check('F.全矩阵最慢一次也在预算内（业务 < 60s / 含探测 < 120s）', maxMs.biz < 60000 && maxMs.probe < 120000, `业务最慢 ${maxMs.biz}ms / 含探测最慢 ${maxMs.probe}ms`)

await cdp.send('Fetch.disable', {}, sessionId).catch(() => {})

const result = summarize(checks, { skipped })
console.log(`\n${result.line}`)
process.exit(result.exitCode)
