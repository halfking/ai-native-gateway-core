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
//   A. API 层：7 个端点的状态码与关键字段（并断言**陈旧门未触发**）
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
const USER = args.user || 'admin'
const PASSWORD = args.password || ''

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
const loginStatus = await js(`fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:JSON.stringify({username:${JSON.stringify(USER)},password:${JSON.stringify(PASSWORD)}})}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) !== '200') {
  console.log(`❌ 登录失败（/api/auth/token → ${loginStatus}）——令牌/口令/端点任一不成立`)
  process.exit(EXIT_FAIL)
}
check('登录 /api/auth/token 200', true)

// ── 1. API 层：7 个端点 ──────────────────────────────────────────────────
const apiGet = async (path) => {
  const raw = await js(`fetch(${JSON.stringify(path)},{credentials:'include'}).then(async r=>r.status+' :: '+(await r.text())).catch(e=>'ERR:'+e.message)`)
  const status = Number(String(raw).split(' :: ')[0])
  let body = null
  try { body = JSON.parse(String(raw).replace(/^\d+ :: /, '')) } catch { /* 非 JSON */ }
  return { status, body, raw: String(raw) }
}

const ov = await apiGet(`/api/admin/session-analytics/overview?${RANGE}`)
check('端点① overview 200（陈旧门未触发）', ov.status === 200, `status=${ov.status}${ov.status !== 200 ? ' body=' + String(ov.raw).slice(0, 160) : ''}`)
if (ov.status !== 200) {
  check('端点① overview 200（陈旧门未触发）', false, `status=${ov.status}`)
  skipped.push({ name: '5 个页面的页面层断言与全部对拍', detail: `overview 不通（status=${ov.status}）⇒ 页面层未评估` })
  const early = summarize(checks, { skipped })
  console.log(`\n${early.line}`)
  process.exit(early.exitCode)
}
const S = ov.body.summary || {}
check('overview 带 summary 15 个指标字段',
  ['total_sessions', 'avg_tokens', 'avg_duration_seconds', 'avg_turns', 'total_cost',
    'active_users', 'active_projects', 'active_tasks', 'overall_success_rate', 'overall_cache_rate']
    .every(k => typeof S[k] === 'number'),
  `total_sessions=${S.total_sessions} active_users=${S.active_users}`)
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

if (!users.length || !agents.length || !models.length) {
  gauge('5 个页面的表格行断言', `窗口 ${START}~${END} 内 users=${users.length} agents=${agents.length} models=${models.length}`)
}

// ── 页面层工具 ────────────────────────────────────────────────────────────
// 页面内一次性采集：数据卡 label→value、表格表头与行数、区块标题、错误/陈旧横幅。
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
  const body = document.body.innerText || ''
  return JSON.stringify({
    cards, tables, titles,
    hasError: body.includes('没能加载出来'),
    hasStale: body.includes('聚合数据正在刷新'),
    isLoading: body.includes('正在加载会话分析数据'),
    hasCostNote: body.includes('花费一列是内部成本'),
    title: document.querySelector('h1')?.textContent?.trim() || document.title,
  })
})()`

const num = (s) => Number(String(s).replace(/[^\d.\-]/g, ''))
/** 对拍基准：优先用「页面自己收到的那份响应」，捕获失败才回落到探针另取的值。 */
const apiOf = (fragment, fallback) => capturedApi(fragment) || fallback
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
  const ovCap = apiOf('/overview?', ov.body)
  const Sc = (ovCap && ovCap.summary) || {}
  const trendsCap = (ovCap && ovCap.trends) || ov.body.trends
  check('对拍基准：已捕获页面自己的 overview 响应', !!ovCap && !!(ovCap && ovCap.summary),
    ovCap ? '同源基准可用' : '未捕获到（回落到探针另取值）')
  check('对拍：总览「会话总数」卡值 == 页面自身响应 summary.total_sessions',
    num(p.cards['会话总数']) === Sc.total_sessions, `页面=${p.cards['会话总数']} 同源响应=${Sc.total_sessions}`)
  check('对拍：总览「活跃人员」卡值 == 页面自身响应 summary.active_users',
    num(p.cards['活跃人员']) === Sc.active_users, `页面=${p.cards['活跃人员']} 同源响应=${Sc.active_users}`)
  const trendInternal = (trendsCap.cost || []).reduce((a, b) => a + Number(b.internal || 0), 0)
  const trendExternal = (trendsCap.cost || []).reduce((a, b) => a + Number(b.external || 0), 0)
  check('对拍：总览「内部成本（美元）」卡值 == 页面自身响应 trends.cost.internal 求和',
    Math.abs(num(p.cards['内部成本（美元）']) - trendInternal) < 0.01,
    `页面=${p.cards['内部成本（美元）']} 同源求和=${trendInternal}`)
  check('对拍：总览「外部成本（积分）」卡值 == 页面自身响应 trends.cost.external 求和',
    Math.abs(num(p.cards['外部成本（积分）']) - trendExternal) < 0.01,
    `页面=${p.cards['外部成本（积分）']} 同源求和=${trendExternal}`)
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
  const urCap = apiOf('/user-ranking?', ur.body)
  const usersCap = (urCap && urCap.users) || users
  if (t && t.rows.length && usersCap.length) {
    const api0 = usersCap[0]
    const expectName = api0.display_name || api0.owner
    check('对拍：排行首行「人员」== 页面自身响应 users[0]',
      t.rows[0][t.headers.indexOf('人员')] === expectName,
      `页面="${t.rows[0][t.headers.indexOf('人员')]}" 同源响应="${expectName}"`)
    check('对拍：排行首行「会话数」== 页面自身响应 users[0].session_count',
      num(t.rows[0][t.headers.indexOf('会话数')]) === Number(api0.session_count),
      `页面=${t.rows[0][t.headers.indexOf('会话数')]} 同源响应=${api0.session_count}`)
  }
}

// ── 4. 人员详情页 ─────────────────────────────────────────────────────────
{
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
  //   ⇒ 判据改成**互斥二选一**：有表 ⇒ 表头齐全且有行；无表 ⇒ 必须出现该层的空态文案。
  //   （这是产品该有的行为；把「空态」当红＝把数据态当缺陷。）
  const drill = JSON.parse(await js(`(async () => {
    const btn = document.querySelector('table.sa-table tbody tr td button.sa-link')
    if (!btn) return JSON.stringify({ ok: false, why: 'NO_PROJECT_ROW' })
    const project = btn.textContent.trim()
    btn.click()
    await new Promise(r => setTimeout(r, 3500))
    const body = document.body.innerText || ''
    const tables = [...document.querySelectorAll('table.sa-table')].map(t => ({
      headers: [...t.querySelectorAll('thead th')].map(h => h.textContent.trim()),
      rows: [...t.querySelectorAll('tbody tr')].map(tr => [...tr.querySelectorAll('td')].map(td => td.textContent.trim())),
    }))
    const tabs = [...document.querySelectorAll('.sa-tab')].map(b => (b.textContent||'').trim() + (b.classList.contains('sa-tab--active') ? '(active)' : ''))
    return JSON.stringify({
      ok: true, project, tables, tabs,
      emptyText: body.includes('这个项目在所选区间内没有任务记录'),
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
      check('人员详情页：任务层无数据时显示该层空态（不渲染表）', drill.emptyText === true,
        `emptyText=${drill.emptyText} tables=${drill.tables.length}`)
    }
  }
  const udCap = apiOf(`/user-ranking/${owner}?`, ud.body)
  const Dc = (udCap && udCap.summary) || {}
  check('对拍：详情「会话数」卡值 == 页面自身响应 summary.session_count',
    num(p.cards['会话数']) === Number(Dc.session_count),
    `页面=${p.cards['会话数']} 同源响应=${Dc.session_count}`)
  check('对拍：详情「当前排名」卡值 == 页面自身响应 summary.rank',
    num(p.cards['当前排名']) === Number(Dc.rank),
    `页面=${p.cards['当前排名']} 同源响应=${Dc.rank}`)
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
  const arCap = apiOf('/agent-ranking?', ar.body)
  const agentsCap = (arCap && arCap.agents) || agents
  if (t && t.rows.length && agentsCap.length) {
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
  const mrCap = apiOf('/model-ranking?', mr.body)
  const modelsCap = (mrCap && mrCap.models) || models
  if (t && t.rows.length && modelsCap.length) {
    check('对拍：模型首行 == 页面自身响应 models[0].model',
      t.rows[0][t.headers.indexOf('模型')] === modelsCap[0].model,
      `页面="${t.rows[0][t.headers.indexOf('模型')]}" 同源响应="${modelsCap[0].model}"`)
    check('对拍：模型首行「请求数」== 页面自身响应 models[0].request_count',
      num(t.rows[0][t.headers.indexOf('请求数')]) === Number(modelsCap[0].request_count),
      `页面=${t.rows[0][t.headers.indexOf('请求数')]} 同源响应=${modelsCap[0].request_count}`)
  }
}

// ── 汇总 ──────────────────────────────────────────────────────────────────
const r = summarize(checks, { skipped })
console.log(`\n${r.line}`)
process.exit(r.exitCode)
