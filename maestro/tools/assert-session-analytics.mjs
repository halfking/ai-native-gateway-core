// assert-session-analytics.mjs — 会话分析系统 5 页「第二层 CDP 断言」（2026-11-20 交付轮）
//
// 为什么是**第二层**而不是并入 flow（与 assert-stage-tree.mjs / assert-users-usage.mjs 同一分工）：
//   ① Maestro web 驱动的 `assertVisible` 只认**视口内**的元素，而
//      · 本应用的滚动发生在**内部容器**（documentElement.scrollHeight == innerHeight）
//        ⇒ 折叠线以下的内容 Maestro 物理上滚不到；
//      · 宽表横向滚动在 DataTable 的 `.app-data-table__scroller`（overflow-x:auto）里
//        ⇒ **右侧列**（如「花费」「最近使用」「查看详情」）不在视口内。
//   ② 「端点 200」≠「数据到了前端」——API 与页面必须**逐字对拍**，
//      这只能在页面内做（同一时刻、同一份响应）。
//   ⇒ 本探针用 CDP 直接驱动页面，逐页做 DOM 级断言 + 数字对拍。
//
// 覆盖：
//   A. API 层：9 个端点的状态码与关键字段（并断言**陈旧门未触发**）
//      ① /api/admin/session-analytics/overview
//      ② /api/admin/session-analytics/user-ranking
//      ③ /api/admin/session-analytics/user-ranking/:owner
//      ④ /api/admin/session-analytics/user-ranking/:owner/projects
//      ⑤ /api/admin/session-analytics/user-ranking/:owner/projects/:project/tasks
//      ⑥ /api/admin/session-analytics/agent-ranking
//      ⑦ /api/admin/session-analytics/model-ranking
//      ⑧ /api/admin/session-analytics/user-ranking/:owner/models（2026-11-21 新增）
//      ⑨ /api/admin/session-analytics/user-ranking/:owner/api-keys（2026-11-21 新增）
//   B. 页面层：5 个页面的数据卡 / 区块标题 / 全部表头（含右侧列）/ 行数
//   C. 对拍层：页面渲染数字 == API 返回数字（会话总数、活跃人员、排行首行、详情卡）
//   D. 入口层：菜单里有指向 /admin/session-analytics 的链接（菜单接入的活体证据）
//
// 退出码：0 = 全绿；1 = 产品红；2 = 量具失效（窗口内无数据 ⇒ 断言未评估）。
//
// ★ 判定口径**不自己写**：统一走 `maestro/tools/result.mjs`（仓内判定口径的唯一真源，
//   selftest.mjs 把「绿 / 红 / 量具失效」三种结局的格式钉死）——
//   本工具早期版本自己写了一份 summarize + 退出码 ⇒ verdict 格式与仓内其它工具不一致
//   （下游按 `RESULT: PASS（…）` 解析时读不懂）⇒ 已改为调用公共模块。
//
// 用法：node assert-session-analytics.mjs --cdp http://127.0.0.1:9223 \
//         --url https://llm.itestu.cn --user admin --password '...'
import { connect, openPage, evaluate } from './cdp-client.mjs'
import { summarize, EXIT_FAIL } from './result.mjs'

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, v, i, a) => {
  if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
  return acc
}, []))

const CDP_URL = args.cdp || 'http://127.0.0.1:9223'
const BASE = (args.url || 'http://127.0.0.1:8782').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
// ★ 口令优先走环境变量（命令行参数会出现在 `ps` 输出里 ⇒ 同机其它用户可见）
const PASSWORD = args.password || process.env.ADMIN_PASSWORD || ''

// 窗口与页面默认口径一致（web/src/api/sessionAnalytics.ts 的 defaultDateRange(7)）。
// ★ 窗口取 **7 天**而不是更长：断言要证明的是「页面显示的数就是接口返回的数」，
//   窗口再长只会让活流量漂移放大（两次取样之间的差值）。
const END = args['end-date'] || new Date().toISOString().slice(0, 10)
const START = args['start-date'] || new Date(Date.now() - 6 * 24 * 3600 * 1000).toISOString().slice(0, 10)
const RANGE = `start_date=${START}&end_date=${END}`

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
const sleep = (ms) => new Promise(r => setTimeout(r, ms))

// ── 响应记录器：抓「页面自己收到的那份响应」──────────────────────────────
// ★ 为什么必须抓页面自己的响应，而不是探针另发一次 API 请求来对拍：
//   线上有活流量 ⇒ 页面渲染那一刻的数与探针几秒后另取一次**必然可能不等**
//   （实测同一轮里总会话数从 14,746 涨到 14,761）⇒ 逐字相等会变成**假红**。
//   ⇒ 对拍基准改成「**页面收到的那份响应**」⇒ 同一份数据、同一时刻 ⇒ 严格相等。
//   （与 2026-11-18 轮「活流量漂移用容差 + 自报漂移量」是同一族问题的两种解法：
//     本轮选了更严的**同源**解法——不用容差，因为基准不再是另一次取样。）
const pendingApi = new Map()
const apiResponses = []
cdp.listeners.add(async (msg) => {
  if (msg.method === 'Network.responseReceived') {
    const { requestId, response } = msg.params
    if (response.url.includes('/api/admin/session-analytics/')) {
      // ★ 必须带上 sessionId：flatten 模式下 requestId 是**会话内**标识，
      //   浏览器级 getResponseBody 不带 sessionId 会报「No resource with given identifier」。
      pendingApi.set(requestId, { url: response.url, status: response.status, sessionId: msg.sessionId })
    }
  } else if (msg.method === 'Network.loadingFinished') {
    const rec = pendingApi.get(msg.params.requestId)
    if (!rec) return
    pendingApi.delete(msg.params.requestId)
    try {
      const { body } = await cdp.send('Network.getResponseBody', { requestId: msg.params.requestId }, rec.sessionId)
      apiResponses.push({ ...rec, body })
    } catch { /* body 偶发取不到：本次不参与对拍 */ }
  }
})
/** 取最近一次捕获的、路径匹配的响应体（按 URL 子串）。 */
function capturedApi(fragment) {
  for (let i = apiResponses.length - 1; i >= 0; i--) {
    if (apiResponses[i].url.includes(fragment)) {
      try { return JSON.parse(apiResponses[i].body) } catch { return null }
    }
  }
  return null
}

async function goto(url, waitMs = 6000) {
  const created = await cdp.send('Target.createTarget', { url: 'about:blank' })
  const { sessionId: sid } = await cdp.send('Target.attachToTarget', { targetId: created.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid)
  await cdp.send('Network.enable', {}, sid)
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sid)
  await evaluate(cdp, sid, `location.href = ${JSON.stringify(url)}`)
  await sleep(waitMs)
  const { sessionId: sid2 } = await cdp.send('Target.attachToTarget', { targetId: created.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid2)
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sid2)
  sessionId = sid2
  return evaluate(cdp, sessionId, 'location.href')
}
async function reattach() {
  const targets = await cdp.send('Target.getTargets')
  const page = targets.targetInfos.filter(t => t.type === 'page').pop()
  const { sessionId: sid } = await cdp.send('Target.attachToTarget', { targetId: page.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid)
  await cdp.send('Network.enable', {}, sid)
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sid)
  sessionId = sid
}
const js = async (expr) => {
  try {
    return await evaluate(cdp, sessionId, expr)
  } catch (e) {
    if (!/navigated or closed|-32000/.test(String(e?.message))) throw e
    await reattach()
    return await evaluate(cdp, sessionId, expr)
  }
}

// ── 0. 登录（API 落 HttpOnly cookie ⇒ SPA 与服务端同源）───────────────────
await goto(`${BASE}/`, 4000)

// ── 0a. 钉住界面语言（zh-CN）——★ 不钉住则整套断言可能**静默假绿** ─────
// ★ 根因：应用 locale 取自 `navigator.language`（`web/src/i18n.ts:82` `defaultLocale()`）
//   且**持久化**在 `localStorage['llmgw_locale']` ⇒ 浏览器语言一变，整页文案就换语种。
//   ⇒ 本探针的落定判据（数据卡 label / 表头 / 区块标题）全是**中文文案**，
//     换语种后要么找不到（假红），要么——更糟——**状态判据找不到错误横幅 ⇒「无错误」假绿**。
// ⇒ 处置：① 显式写 localStorage 再重载；② 读回 `<html lang>`（`web/src/i18n.ts:121` 会写它）复核；
//   ③ 钉不住 ⇒ 判**量具失效**（退出码 2），**不判产品红**（红的是环境，不是页面）。
const localePinned = await js(`(() => {
  try { localStorage.setItem('llmgw_locale', 'zh-CN') } catch (e) { return 'SET_FAIL:' + e.message }
  return 'OK'
})()`)
await goto(`${BASE}/`, 4000)
const htmlLang = String(await js('document.documentElement.lang || ""'))
const localeOk = /^zh/i.test(htmlLang)
if (!localeOk) {
  check(`界面语言已钉住为 zh-CN（<html lang>）`, false, `html lang="${htmlLang}"（localStorage 写入=${localePinned}）`)
  skipped.push({ name: '5 个页面的页面层断言与全部对拍', detail: `界面语言不是 zh-CN（${htmlLang}）⇒ 文案判据不可用 ⇒ 页面层未评估` })
  const early = summarize(checks, { skipped })
  console.log(`\n${early.line}`)
  process.exit(early.exitCode)
}
check('界面语言已钉住为 zh-CN（<html lang>）', true, `html lang="${htmlLang}"`)

// ★ 2026-11-20 增补「种子会话优先」路径（与 seed-session.mjs 的无口令设计同向）：
//   先探 /api/auth/me —— 已有有效会话（HttpOnly cookie 已种）则不再走口令登录。
//   背景：自适应 captcha 闸门是**进程内存计数**且全站经 nginx 同源（remote=172.21.0.1），
//   并行车与测试机共享失败计数 ⇒ 密码轮换窗口期里口令登录可能整体被 captcha 强制。
//   种子路径不依赖口令，只依赖一枚有效 JWT（exp 7 天）。
let loginStatus = await js(`fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) === '200') {
  check('已有会话 /api/auth/me 200（seed 路径，免口令）', true)
} else if (PASSWORD) {
  loginStatus = await js(`fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:JSON.stringify({username:${JSON.stringify(USER)},password:${JSON.stringify(PASSWORD)}})}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
  if (String(loginStatus) !== '200') {
    console.log(`❌ 登录失败（/api/auth/token → ${loginStatus}）——令牌/口令/端点任一不成立`)
    process.exit(EXIT_FAIL)
  }
  check('登录 /api/auth/token 200', true)
} else {
  console.log(`❌ 无已有会话（/api/auth/me → ${loginStatus}）且未提供口令——先跑 seed-session.mjs 或传 --password`)
  process.exit(EXIT_FAIL)
}

// ── 1. API 层：7 个端点 ──────────────────────────────────────────────────
const apiGet = async (path) => {
  const raw = await js(`fetch(${JSON.stringify(path)},{credentials:'include'}).then(async r=>r.status+' :: '+(await r.text())).catch(e=>'ERR:'+e.message)`)
  const status = Number(String(raw).split(' :: ')[0])
  let body = null
  try { body = JSON.parse(String(raw).replace(/^\d+ :: /, '')) } catch { /* 非 JSON */ }
  return { status, body, raw: String(raw) }
}

const ov = await apiGet(`/api/admin/session-analytics/overview?${RANGE}`)
if (ov.status !== 200) {
  check('端点① overview 200（陈旧门未触发）', false, `status=${ov.status}${String(ov.raw).slice(0, 160)}`)
  skipped.push({ name: '5 个页面的页面层断言与全部对拍', detail: `overview 不通（status=${ov.status}）⇒ 页面层未评估` })
  const early = summarize(checks, { skipped })
  console.log(`\n${early.line}`)
  process.exit(early.exitCode)
}
check('端点① overview 200（陈旧门未触发）', true, 'status=200')
const S = ov.body.summary || {}
// ★ 断言覆盖 OverviewSummary 的**全部 14 个字段**（`admin/session_analytics/queries.go:22`）。
//   旧版名字写「15 个」而实际只查 10 个 ⇒ 标签与断言不符、契约覆盖不足（门禁绿 ≠ 断言到位）。
const SUMMARY_FIELDS = ['total_sessions', 'total_sessions_growth', 'avg_tokens', 'avg_context_size',
  'avg_duration_seconds', 'avg_turns', 'total_cost', 'total_cost_growth', 'active_users',
  'active_users_growth', 'active_projects', 'active_tasks', 'overall_success_rate', 'overall_cache_rate']
const missingSummary = SUMMARY_FIELDS.filter(k => typeof S[k] !== 'number')
check(`overview 带 summary 全部 ${SUMMARY_FIELDS.length} 个指标字段`,
  missingSummary.length === 0,
  missingSummary.length ? '缺 ' + missingSummary.join('/') : `total_sessions=${S.total_sessions} active_users=${S.active_users}`)
check('overview 带 trends 三序列（sessions/tokens/cost）',
  !!(ov.body.trends && ov.body.trends.sessions && ov.body.trends.tokens && ov.body.trends.cost),
  `sessions=${(ov.body.trends?.sessions || []).length} tokens=${(ov.body.trends?.tokens || []).length} cost=${(ov.body.trends?.cost || []).length}`)

const ur = await apiGet(`/api/admin/session-analytics/user-ranking?${RANGE}&limit=50`)
check('端点② user-ranking 200', ur.status === 200, `status=${ur.status}`)
const users = (ur.body && ur.body.users) || []
check('端点② user-ranking 带人员行', Array.isArray(users) && users.length > 0, `rows=${users.length}`)
const owner = (args.owner || (users[0] && users[0].owner) || '')

const ud = await apiGet(`/api/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}?${RANGE}`)
check(`端点③ user-ranking/:owner 200（owner=${owner}）`, ud.status === 200, `status=${ud.status}`)

const up = await apiGet(`/api/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}/projects?${RANGE}`)
check('端点④ user-ranking/:owner/projects 200', up.status === 200, `status=${up.status}`)
const projects = (up.body && up.body.projects) || []

let pt = { status: 0, body: null }
if (projects.length) {
  const proj = projects[0].project
  pt = await apiGet(`/api/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}/projects/${encodeURIComponent(proj)}/tasks?${RANGE}`)
  check(`端点⑤ …/projects/:project/tasks 200（project=${proj}）`, pt.status === 200, `status=${pt.status}`)
} else {
  gauge('端点⑤ …/projects/:project/tasks', `该 owner 在窗口内没有项目行（projects=${projects.length}）`)
}

const ar = await apiGet(`/api/admin/session-analytics/agent-ranking?${RANGE}`)
check('端点⑥ agent-ranking 200', ar.status === 200, `status=${ar.status}`)
const agents = (ar.body && ar.body.agents) || []

const mr = await apiGet(`/api/admin/session-analytics/model-ranking?${RANGE}`)
check('端点⑦ model-ranking 200', mr.status === 200, `status=${mr.status}`)
const models = (mr.body && mr.body.models) || []

const um = await apiGet(`/api/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}/models?${RANGE}`)
check(`端点⑧ user-ranking/:owner/models 200（owner=${owner}）`, um.status === 200, `status=${um.status}`)
const userModels = (um.body && um.body.models) || []

const uak = await apiGet(`/api/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}/api-keys?${RANGE}`)
check(`端点⑨ user-ranking/:owner/api-keys 200（owner=${owner}）`, uak.status === 200, `status=${uak.status}`)
const userApiKeys = (uak.body && uak.body.api_keys) || []

if (!users.length || !agents.length || !models.length) {
  gauge('5 个页面的表格行断言', `窗口 ${START}~${END} 内 users=${users.length} agents=${agents.length} models=${models.length}`)
}

// ── 页面层工具 ────────────────────────────────────────────────────────────
// 页面内一次性采集：数据卡 label→value、表格表头与行数、区块标题、错误/陈旧横幅。
// ★ 状态判据一律用**结构类名**、不用中文文案（2026-11-20 批判式审计修正）：
//   · 错误横幅 = `SaStateBanner` 的 `.sa-banner--error`
//   · 陈旧横幅 = `SaStateBanner` 的 `.sa-banner--stale`
//   · 加载占位 = `.sa-loading`
//   · 口径说明 = `.sa-note`
//   · 空态     = `EmptyState` 的 `.app-empty-state`
//   ⇒ 文案只是**展示层**，会随 locale 变；类名是**契约层**，不会。
//     （旧版按 `body.includes('没能加载出来')` 判错误 ⇒ 换语种后找不到 ⇒ 假绿。）
const PAGE_PROBE = `(() => {
  const cards = {}
  for (const c of document.querySelectorAll('.stat-card')) {
    const l = c.querySelector('.app-stat-card__label')?.textContent?.trim() || ''
    const v = c.querySelector('.app-stat-card__value')?.textContent?.trim() || ''
    if (l) cards[l] = v
  }
  const tables = [...document.querySelectorAll('table.sa-table')].map(t => ({
    headers: [...t.querySelectorAll('thead th')].map(h => h.textContent.trim()),
    rows: [...t.querySelectorAll('tbody tr')].map(tr => [...tr.querySelectorAll('td')].map(td => td.textContent.trim())),
  }))
  const titles = [...document.querySelectorAll('.sa-section__title')].map(h => h.textContent.trim())
  return JSON.stringify({
    cards, tables, titles,
    hasError: !!document.querySelector('.sa-banner--error'),
    hasStale: !!document.querySelector('.sa-banner--stale'),
    isLoading: !!document.querySelector('.sa-loading'),
    hasCostNote: !!document.querySelector('.sa-note'),
    hasEmptyState: !!document.querySelector('.app-empty-state'),
    title: document.querySelector('h1')?.textContent?.trim() || document.title,
  })
})()`

// ★ 数字解析：空串 / 破折号 / 非数字 ⇒ **null**（不是 0）。
//   旧版 `Number('')` = 0 ⇒ 页面显示「—」时会被当成 0，与接口的 0 **假绿**。
const num = (s) => {
  const t = String(s ?? '').replace(/[^\d.\-]/g, '')
  if (!t || t === '-' || t === '.') return null
  const n = Number(t)
  return Number.isFinite(n) ? n : null
}
/** 同源基准：**只接受「页面自己收到的那份响应」**；捕获不到 ⇒ null（不回落到探针另取的值）。**
 *   ★ 旧版 `apiOf(fragment, fallback)` 会把「捕获失败」悄悄换成探针另取的那份
 *     ⇒ 对拍从「同源、同时刻、严格相等」退化成「跨时刻相等」（活流量下必然假红/假绿），
 *     而那条「基准已捕获」守卫因为拿了 fallback 而**永远为真** ⇒ 守卫是空的。
 *   ⇒ 现在：捕获不到 ⇒ 该页对拍判**量具失效**（未评估），不假装对拍过。
 */
const sameSource = (fragment) => capturedApi(fragment)
const loadPage = async (url) => {
  await goto(`${BASE}${url}`, 7000)
  // 等数据落定：数据卡或表格行出现（不是加载态占位符）
  for (let i = 0; i < 20; i++) {
    const raw = await js(PAGE_PROBE)
    const p = JSON.parse(raw)
    if (!p.isLoading && (Object.keys(p.cards).length > 0 || p.tables.some(t => t.rows.length > 0))) return p
    await sleep(1000)
  }
  return JSON.parse(await js(PAGE_PROBE))
}

// ── 1b. 菜单入口（走**真实用户路径**：展开顶栏分组 → 点菜单项 → 断言落地页）──
// ★ 为什么必须这么做：顶栏导航是**分组下拉**，下拉面板是 Teleport 到 body 且
//   `v-if="activeGroup && openGroupId"` ⇒ **分组未展开时，菜单项根本不在 DOM 里**。
//   ⇒ 「页面上查得到 a[href=...]」这种查法在未展开时必然查不到 ⇒ 那是**量具错**，不是产品红。
//   ⇒ 正确判据＝「点得开、点得动、点完落到对的页面」。
{
  await goto(`${BASE}/dashboard`, 5000)
  const menu = JSON.parse(await js(`(async () => {
    const triggers = [...document.querySelectorAll('.app-topbar__group-trigger')]
    const findLink = () => document.querySelector('a[href="/admin/session-analytics"]')
    for (const t of triggers) {
      // ① hover 展开（@mouseenter="openGroupMenu"）
      t.dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }))
      await new Promise(r => setTimeout(r, 450))
      let link = findLink()
      // ② 回落到点击触发器（@click="toggleGroup"）
      if (!link) {
        t.click()
        await new Promise(r => setTimeout(r, 450))
        link = findLink()
      }
      if (link) {
        const label = link.textContent.trim()
        link.click()
        await new Promise(r => setTimeout(r, 2500))
        return JSON.stringify({ ok: true, label, triggers: triggers.length, path: location.pathname })
      }
    }
    return JSON.stringify({ ok: false, triggers: triggers.length })
  })()`))
  check('菜单：分组下拉里存在「会话分析」入口', menu.ok === true, menu.ok ? `label="${menu.label}" 触发器 ${menu.triggers} 个` : `未找到入口（触发器 ${menu.triggers} 个）`)
  check('菜单：点击入口后落到 /admin/session-analytics', menu.path === '/admin/session-analytics', `path=${menu.path}`)
}

// ── 2. 总览页 ─────────────────────────────────────────────────────────────
{
  const p = await loadPage('/admin/session-analytics')
  check('总览页：无错误横幅', !p.hasError)
  check('总览页：无陈旧横幅（503 analytics_view_stale 未触发）', !p.hasStale)
  const needCards = ['会话总数', '活跃人员', '活跃项目', '活跃任务', '平均 Token 数', '平均轮次',
    '平均会话时长', '成功率', '缓存命中率', '内部成本（美元）', '外部成本（积分）']
  const missing = needCards.filter(l => !(l in p.cards))
  check(`总览页：11 张数据卡齐全`, missing.length === 0, missing.length ? '缺 ' + missing.join('/') : `${needCards.length} 张全在`)
  const needTitles = ['会话趋势', 'Token 趋势', '花费趋势', '高频模型', '活跃人员', '活跃项目']
  const missingT = needTitles.filter(t => !p.titles.includes(t))
  check('总览页：3 个趋势段 + 3 个榜单标题齐全', missingT.length === 0, missingT.length ? '缺 ' + missingT.join('/') : '6 个标题全在')
  // 榜单表头
  const tbls = p.tables
  const topModels = tbls.find(t => t.headers.includes('模型') && t.headers.includes('请求数'))
  const topUsers = tbls.find(t => t.headers.includes('人员') && t.headers.includes('花费'))
  const topProjects = tbls.find(t => t.headers.includes('项目') && t.headers.includes('使用人数'))
  check('总览页：高频模型表头 [模型, 请求数]', !!topModels, topModels ? topModels.headers.join(' | ') : '未找到')
  check('总览页：活跃人员表头 [人员, 会话数, Token 数, 花费]', !!topUsers, topUsers ? topUsers.headers.join(' | ') : '未找到')
  check('总览页：活跃项目表头 [项目, 会话数, 使用人数, 花费]', !!topProjects, topProjects ? topProjects.headers.join(' | ') : '未找到')
  check('总览页：活跃人员榜有行', !!topUsers && topUsers.rows.length > 0, topUsers ? `rows=${topUsers.rows.length}` : '')
  // ★ 对拍：页面渲染值 == API 返回值
  // ★ 对拍基准＝**页面自己收到的 overview 响应**（同源、同时刻 ⇒ 严格相等）
  // ★ 捕获不到 ⇒ 判「未评估」而不是拿探针另取的值凑（那会让同源退化成跨时刻）。
  const ovCap = sameSource('/overview?')
  const ovBaselineOk = !!(ovCap && ovCap.summary)
  check('对拍基准：已捕获页面自己的 overview 响应', ovBaselineOk,
    ovBaselineOk ? '同源基准可用' : '未捕获 ⇒ 下面的对拍未评估')
  if (!ovBaselineOk) {
    gauge('对拍：总览「会话总数」/「活跃人员」/「内部成本」/「外部成本」', '同源基准未捕获')
  } else {
    const Sc = ovCap.summary || {}
    const trendsCap = ovCap.trends || ov.body.trends
    check('对拍：总览「会话总数」卡值 == 页面自身响应 summary.total_sessions',
      num(p.cards['会话总数']) !== null && num(p.cards['会话总数']) === Sc.total_sessions,
      `页面=${p.cards['会话总数']} 同源响应=${Sc.total_sessions}`)
    check('对拍：总览「活跃人员」卡值 == 页面自身响应 summary.active_users',
      num(p.cards['活跃人员']) !== null && num(p.cards['活跃人员']) === Sc.active_users,
      `页面=${p.cards['活跃人员']} 同源响应=${Sc.active_users}`)
    const trendInternal = (trendsCap.cost || []).reduce((a, b) => a + Number(b.internal || 0), 0)
    const trendExternal = (trendsCap.cost || []).reduce((a, b) => a + Number(b.external || 0), 0)
    check('对拍：总览「内部成本（美元）」卡值 == 页面自身响应 trends.cost.internal 求和',
      num(p.cards['内部成本（美元）']) !== null && Math.abs(num(p.cards['内部成本（美元）']) - trendInternal) < 0.01,
      `页面=${p.cards['内部成本（美元）']} 同源求和=${trendInternal}`)
    check('对拍：总览「外部成本（积分）」卡值 == 页面自身响应 trends.cost.external 求和',
      num(p.cards['外部成本（积分）']) !== null && Math.abs(num(p.cards['外部成本（积分）']) - trendExternal) < 0.01,
      `页面=${p.cards['外部成本（积分）']} 同源求和=${trendExternal}`)
  }
}

// ── 3. 人员排行页 ─────────────────────────────────────────────────────────
{
  const p = await loadPage('/admin/session-analytics/user-ranking')
  check('人员排行页：无错误横幅', !p.hasError)
  check('人员排行页：无陈旧横幅', !p.hasStale)
  const need = ['排名', '人员', '邮箱', '会话数', '项目数', '任务数', 'Token 数', '花费', '最近活跃', '最近任务', '查看详情']
  const t = p.tables.find(x => x.headers.includes('排名'))
  check('人员排行页：11 列表头齐全（含右侧列「查看详情」）', !!t && need.every(h => t.headers.includes(h)),
    t ? (need.filter(h => !t.headers.includes(h)).length ? '缺 ' + need.filter(h => !t.headers.includes(h)).join('/') : '11 列全在') : '未找到表')
  check('人员排行页：有数据行', !!t && t.rows.length > 0, t ? `rows=${t.rows.length}` : '')
  const urCap = sameSource('/user-ranking?')
  const urBaselineOk = !!(urCap && Array.isArray(urCap.users) && urCap.users.length)
  check('对拍基准：已捕获页面自己的 user-ranking 响应', urBaselineOk,
    urBaselineOk ? '同源基准可用' : '未捕获 ⇒ 下面的对拍未评估')
  const usersCap = (urCap && urCap.users) || []
  if (!urBaselineOk) {
    gauge('对拍：排行首行「人员」/「会话数」', '同源基准未捕获')
  } else if (t && t.rows.length) {
    const api0 = usersCap[0]
    const expectName = api0.display_name || api0.owner
    check('对拍：排行首行「人员」== 页面自身响应 users[0]',
      t.rows[0][t.headers.indexOf('人员')] === expectName,
      `页面="${t.rows[0][t.headers.indexOf('人员')]}" 同源响应="${expectName}"`)
    check('对拍：排行首行「会话数」== 页面自身响应 users[0].session_count',
      num(t.rows[0][t.headers.indexOf('会话数')]) !== null && num(t.rows[0][t.headers.indexOf('会话数')]) === Number(api0.session_count),
      `页面=${t.rows[0][t.headers.indexOf('会话数')]} 同源响应=${api0.session_count}`)
  }
}

// ── 4. 人员详情页 ─────────────────────────────────────────────────────────
// ★ 动态段 owner 为空时**不能**去断详情页：URL 会变成 `/user-ranking/`
//   ⇒ 命中的是「没有那个路由」而不是「详情页坏了」⇒ 那是**量具错**，不是产品红。
if (!owner) {
  gauge('人员详情页：页面层与对拍', `窗口内没有人员行 ⇒ 动态段无真值（owner=''）⇒ 未评估`)
} else {
  const p = await loadPage(`/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}`)
  check('人员详情页：无错误横幅', !p.hasError)
  check('人员详情页：无陈旧横幅', !p.hasStale)
  const needCards = ['会话数', '任务数', '总花费', '当前排名']
  const missing = needCards.filter(l => !(l in p.cards))
  check('人员详情页：4 张数据卡齐全', missing.length === 0, missing.length ? '缺 ' + missing.join('/') : '4 张全在')
  const needTitles = ['会话趋势', '任务趋势', '花费趋势', '排名变化']
  const missingT = needTitles.filter(t => !p.titles.includes(t))
  check('人员详情页：4 个趋势段齐全', missingT.length === 0, missingT.length ? '缺 ' + missingT.join('/') : '4 段全在')
  check('人员详情页：跨单位口径说明在屏（costNote）', p.hasCostNote === true)
  check('人员详情页：项目列表表头齐全',
    p.tables.some(t => t.headers.includes('项目') && t.headers.includes('任务数')),
    p.tables.map(t => t.headers.join('|')).join(' || '))
  // ★ 任务列表**不是常驻的第二张表**：详情页用 tab（projects | tasks），
  //   点项目行（button.sa-link → selectProject）才切到 tasks 并拉端点⑤。
  //   ⇒ 断言必须是「点项目行 → 切到任务列表」，而不是「两张表同时在场」
  //   （后者会把设计当成缺陷）。
  // ★★ 而且任务层**可能真的没有数据**（211 实测：sessions.task 全空 ⇒ task_count=0）
  //   ⇒ DataTable 渲染的是 EmptyState、**不渲染 table** ⇒ 「表头齐全」在空数据下必然红。
  //   ⇒ 判据改成**互斥二选一**：有表 ⇒ 表头齐全且有行；无表 ⇒ 必须出现该层的空态。
  //   （这是产品该有的行为；把「空态」当红＝把数据态当缺陷。）
  //   ★ 空态判据用**结构类名** `.app-empty-state`（`web/src/components/EmptyState.vue`），
  //     不用中文文案（文案随 locale 变 ⇒ 换语种后找不到 ⇒ 假红）。
  const drill = JSON.parse(await js(`(async () => {
    const btn = document.querySelector('table.sa-table tbody tr td button.sa-link')
    if (!btn) return JSON.stringify({ ok: false, why: 'NO_PROJECT_ROW' })
    const project = btn.textContent.trim()
    btn.click()
    await new Promise(r => setTimeout(r, 3500))
    const tables = [...document.querySelectorAll('table.sa-table')].map(t => ({
      headers: [...t.querySelectorAll('thead th')].map(h => h.textContent.trim()),
      rows: [...t.querySelectorAll('tbody tr')].map(tr => [...tr.querySelectorAll('td')].map(td => td.textContent.trim())),
    }))
    const tabs = [...document.querySelectorAll('.sa-tab')].map(b => (b.textContent||'').trim() + (b.classList.contains('sa-tab--active') ? '(active)' : ''))
    return JSON.stringify({
      ok: true, project, tables, tabs,
      hasEmptyState: !!document.querySelector('.app-empty-state'),
      projectsGone: !document.querySelector('table.sa-table'),
    })
  })()`))
  if (!drill.ok) {
    gauge('人员详情页：点项目行下钻到任务列表', drill.why || '无项目行')
  } else {
    const taskTable = drill.tables.find(t => t.headers.some(h => h.includes('任务')) && t.headers.includes('请求数'))
    check('人员详情页：点项目行 ⇒ 切到「任务列表」tab',
      drill.tabs.some(t => t.includes('任务列表') && t.includes('active')) || !!taskTable,
      `tabs=${drill.tabs.join(' / ')} project=${drill.project}`)
    if (taskTable) {
      check('人员详情页：任务列表表头齐全且有行', taskTable.rows.length > 0,
        `rows=${taskTable.rows.length} headers=${taskTable.headers.join('|')}`)
    } else {
      check('人员详情页：任务层无数据时显示该层空态（不渲染表）', drill.hasEmptyState === true,
        `hasEmptyState=${drill.hasEmptyState} tables=${drill.tables.length}`)
    }
  }
  const udCap = sameSource(`/user-ranking/${owner}?`)
  const udBaselineOk = !!(udCap && udCap.summary)
  check('对拍基准：已捕获页面自己的 user-detail 响应', udBaselineOk,
    udBaselineOk ? '同源基准可用' : '未捕获 ⇒ 下面的对拍未评估')
  if (!udBaselineOk) {
    gauge('对拍：详情「会话数」/「当前排名」', '同源基准未捕获')
  } else {
    const Dc = udCap.summary || {}
    check('对拍：详情「会话数」卡值 == 页面自身响应 summary.session_count',
      num(p.cards['会话数']) !== null && num(p.cards['会话数']) === Number(Dc.session_count),
      `页面=${p.cards['会话数']} 同源响应=${Dc.session_count}`)
    check('对拍：详情「当前排名」卡值 == 页面自身响应 summary.rank',
      num(p.cards['当前排名']) !== null && num(p.cards['当前排名']) === Number(Dc.rank),
      `页面=${p.cards['当前排名']} 同源响应=${Dc.rank}`)
  }

  // ── 4.1 人员详情页 - 模型用量 tab ────────────────────────────────────
  const modelsTab = JSON.parse(await js(`(async () => {
    const tab = [...document.querySelectorAll('.sa-tab')].find(b => (b.textContent||'').includes('模型用量'))
    if (!tab) return JSON.stringify({ ok: false, why: 'NO_MODELS_TAB' })
    tab.click()
    await new Promise(r => setTimeout(r, 2500))
    const tables = [...document.querySelectorAll('table.sa-table')].map(t => ({
      headers: [...t.querySelectorAll('thead th')].map(h => h.textContent.trim()),
      rows: [...t.querySelectorAll('tbody tr')].map(tr => [...tr.querySelectorAll('td')].map(td => td.textContent.trim())),
    }))
    return JSON.stringify({
      ok: true, tables,
      hasEmptyState: !!document.querySelector('.app-empty-state'),
    })
  })()`))
  if (!modelsTab.ok) {
    gauge('人员详情页：模型用量 tab', modelsTab.why || '找不到模型用量 tab')
  } else {
    const modelsTable = modelsTab.tables.find(t => t.headers.includes('模型'))
    const needHeaders = ['模型', '请求数', 'Token 总数', '成功率', '缓存率', '平均响应时间']
    if (modelsTable) {
      check('人员详情页：模型用量表头齐全', needHeaders.every(h => modelsTable.headers.includes(h)),
        needHeaders.filter(h => !modelsTable.headers.includes(h)).length ? '缺 ' + needHeaders.filter(h => !modelsTable.headers.includes(h)).join('/') : '6 列全在')
      check('人员详情页：模型用量有行或显示空态', modelsTable.rows.length > 0 || modelsTab.hasEmptyState,
        `rows=${modelsTable.rows.length} hasEmptyState=${modelsTab.hasEmptyState}`)
    } else {
      check('人员详情页：模型用量无数据时显示空态', modelsTab.hasEmptyState === true,
        `hasEmptyState=${modelsTab.hasEmptyState} tables=${modelsTab.tables.length}`)
    }
    const modelsCap = sameSource(`/user-ranking/${owner}/models?`)
    check('对拍基准：已捕获页面自己的 models 响应', !!modelsCap,
      modelsCap ? '同源基准可用' : '未捕获')
    if (modelsCap && modelsCap.models && modelsCap.models.length > 0 && modelsTable && modelsTable.rows.length > 0) {
      check('对拍：模型用量首行「模型」== 页面自身响应 models[0].model',
        modelsTable.rows[0][modelsTable.headers.indexOf('模型')] === modelsCap.models[0].model,
        `页面="${modelsTable.rows[0][modelsTable.headers.indexOf('模型')]}" 同源响应="${modelsCap.models[0].model}"`)
    }
  }

  // ── 4.2 人员详情页 - API Key 统计 tab ──────────────────────────────
  const apiKeysTab = JSON.parse(await js(`(async () => {
    const tab = [...document.querySelectorAll('.sa-tab')].find(b => (b.textContent||'').includes('API Key'))
    if (!tab) return JSON.stringify({ ok: false, why: 'NO_APIKEYS_TAB' })
    tab.click()
    await new Promise(r => setTimeout(r, 2500))
    const tables = [...document.querySelectorAll('table.sa-table')].map(t => ({
      headers: [...t.querySelectorAll('thead th')].map(h => h.textContent.trim()),
      rows: [...t.querySelectorAll('tbody tr')].map(tr => [...tr.querySelectorAll('td')].map(td => td.textContent.trim())),
    }))
    return JSON.stringify({
      ok: true, tables,
      hasEmptyState: !!document.querySelector('.app-empty-state'),
    })
  })()`))
  if (!apiKeysTab.ok) {
    gauge('人员详情页：API Key 统计 tab', apiKeysTab.why || '找不到 API Key 统计 tab')
  } else {
    const apiKeysTable = apiKeysTab.tables.find(t => t.headers.includes('密钥别名'))
    const needHeaders = ['密钥别名', '密钥 ID', '请求数', 'Token 总数', '项目数', '主要项目']
    if (apiKeysTable) {
      check('人员详情页：API Key 统计表头齐全', needHeaders.every(h => apiKeysTable.headers.includes(h)),
        needHeaders.filter(h => !apiKeysTable.headers.includes(h)).length ? '缺 ' + needHeaders.filter(h => !apiKeysTable.headers.includes(h)).join('/') : '6 列全在')
      check('人员详情页：API Key 统计有行或显示空态', apiKeysTable.rows.length > 0 || apiKeysTab.hasEmptyState,
        `rows=${apiKeysTable.rows.length} hasEmptyState=${apiKeysTab.hasEmptyState}`)
    } else {
      check('人员详情页：API Key 统计无数据时显示空态', apiKeysTab.hasEmptyState === true,
        `hasEmptyState=${apiKeysTab.hasEmptyState} tables=${apiKeysTab.tables.length}`)
    }
    const apiKeysCap = sameSource(`/user-ranking/${owner}/api-keys?`)
    check('对拍基准：已捕获页面自己的 api-keys 响应', !!apiKeysCap,
      apiKeysCap ? '同源基准可用' : '未捕获')
    if (apiKeysCap && apiKeysCap.api_keys && apiKeysCap.api_keys.length > 0 && apiKeysTable && apiKeysTable.rows.length > 0) {
      const expectKeyId = String(apiKeysCap.api_keys[0].key_id)
      check('对拍：API Key 首行「密钥 ID」== 页面自身响应 api_keys[0].key_id',
        apiKeysTable.rows[0][apiKeysTable.headers.indexOf('密钥 ID')] === expectKeyId,
        `页面="${apiKeysTable.rows[0][apiKeysTable.headers.indexOf('密钥 ID')]}" 同源响应="${expectKeyId}"`)
    }
  }
}

// ── 5. 智能体排行页 ───────────────────────────────────────────────────────
{
  const p = await loadPage('/admin/session-analytics/agent-ranking')
  check('智能体排行页：无错误横幅', !p.hasError)
  check('智能体排行页：无陈旧横幅', !p.hasStale)
  const need = ['排名', '智能体', '使用人数', '会话数', '任务数', 'Token 数', '花费', '平均时长', '最近使用']
  const t = p.tables.find(x => x.headers.includes('智能体'))
  check('智能体排行页：9 列表头齐全', !!t && need.every(h => t.headers.includes(h)),
    t ? (need.filter(h => !t.headers.includes(h)).length ? '缺 ' + need.filter(h => !t.headers.includes(h)).join('/') : '9 列全在') : '未找到表')
  check('智能体排行页：有数据行', !!t && t.rows.length > 0, t ? `rows=${t.rows.length}` : '')
  const arCap = sameSource('/agent-ranking?')
  const arBaselineOk = !!(arCap && Array.isArray(arCap.agents) && arCap.agents.length)
  check('对拍基准：已捕获页面自己的 agent-ranking 响应', arBaselineOk,
    arBaselineOk ? '同源基准可用' : '未捕获 ⇒ 下面的对拍未评估')
  if (!arBaselineOk) {
    gauge('对拍：智能体首行', '同源基准未捕获')
  } else if (t && t.rows.length) {
    const agentsCap = arCap.agents
    check('对拍：智能体首行 == 页面自身响应 agents[0].agent_name',
      t.rows[0][t.headers.indexOf('智能体')] === agentsCap[0].agent_name,
      `页面="${t.rows[0][t.headers.indexOf('智能体')]}" 同源响应="${agentsCap[0].agent_name}"`)
  }
}

// ── 6. 模型排行页 ─────────────────────────────────────────────────────────
{
  const p = await loadPage('/admin/session-analytics/model-ranking')
  check('模型排行页：无错误横幅', !p.hasError)
  check('模型排行页：无陈旧横幅', !p.hasStale)
  const need = ['排名', '模型', '供应商', '请求数', '成功率', 'Token 数', '缓存命中率', '平均时长', '花费', '使用人数', '最近使用']
  const t = p.tables.find(x => x.headers.includes('模型'))
  check('模型排行页：11 列表头齐全', !!t && need.every(h => t.headers.includes(h)),
    t ? (need.filter(h => !t.headers.includes(h)).length ? '缺 ' + need.filter(h => !t.headers.includes(h)).join('/') : '11 列全在') : '未找到表')
  check('模型排行页：有数据行', !!t && t.rows.length > 0, t ? `rows=${t.rows.length}` : '')
  const mrCap = sameSource('/model-ranking?')
  const mrBaselineOk = !!(mrCap && Array.isArray(mrCap.models) && mrCap.models.length)
  check('对拍基准：已捕获页面自己的 model-ranking 响应', mrBaselineOk,
    mrBaselineOk ? '同源基准可用' : '未捕获 ⇒ 下面的对拍未评估')
  if (!mrBaselineOk) {
    gauge('对拍：模型首行/「请求数」', '同源基准未捕获')
  } else if (t && t.rows.length) {
    const modelsCap = mrCap.models
    check('对拍：模型首行 == 页面自身响应 models[0].model',
      t.rows[0][t.headers.indexOf('模型')] === modelsCap[0].model,
      `页面="${t.rows[0][t.headers.indexOf('模型')]}" 同源响应="${modelsCap[0].model}"`)
    check('对拍：模型首行「请求数」== 页面自身响应 models[0].request_count',
      num(t.rows[0][t.headers.indexOf('请求数')]) !== null && num(t.rows[0][t.headers.indexOf('请求数')]) === Number(modelsCap[0].request_count),
      `页面=${t.rows[0][t.headers.indexOf('请求数')]} 同源响应=${modelsCap[0].request_count}`)
  }
}

// ── 汇总 ──────────────────────────────────────────────────────────────────
const r = summarize(checks, { skipped })
console.log(`\n${r.line}`)
process.exit(r.exitCode)
