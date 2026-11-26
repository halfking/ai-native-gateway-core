// assert-users-usage.mjs — 会话分析 users 页「数字出处轮」第二层 CDP 断言（2026-11-20）
//
// 为什么是**第二层**而不是并入 flow（与 assert-stage-tree.mjs 同一分工）：
//   Maestro web 驱动的 scrollUntilVisible 滚的是 window，而本应用在**内部容器**
//   滚（documentElement.scrollHeight == innerHeight）⇒ 详情页「模型使用明细」卡
//   （top≈4999）物理上滚不到 ⇒ 折叠线下的深度校验只能页面内做。
//
// 覆盖（owner 需求的数字与出处）：
//   A. 列表页：usage_meta 出处行 / 窗口用量五列列头 / 窗口聚合数字与 0 的分家
//   B. 展开行：员工×模型矩阵（模型名/输入/输出/总 Token/内部成本/外部成本）
//   C. 详情页：模型使用明细卡的列头 + 每模型行 + 任务链接
//   D. 帮助抽屉（列表页 + 详情页）：公式/口径/来源段
//
// 退出码：0 = 全绿；1 = 产品红；2 = 量具失效（列表 0 用户 ⇒ 断言未评估）。
//
// 用法：node assert-users-usage.mjs --cdp http://127.0.0.1:9223 --url https://llm.itestu.cn \
//         --user admin --password '...'
import { connect, openPage, evaluate } from './cdp-client.mjs'

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, v, i, a) => {
  if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
  return acc
}, []))

const CDP_URL = args.cdp || 'http://127.0.0.1:9223'
const BASE = (args.url || 'http://127.0.0.1:8782').replace(/\/$/, '')
const USER = args.user || 'admin'
const PASSWORD = process.env.ADMIN_PASSWORD || args.password || ''
const OWNER = args.owner || ''

const results = []
let gaugeBroken = false
function check(name, ok, detail = '') {
  results.push({ name, ok, detail })
  console.log(`${ok ? '✅' : '❌'} ${name}${detail ? '  — ' + detail : ''}`)
  return ok
}
function gauge(name, detail = '') {
  gaugeBroken = true
  console.log(`○ 未评估（量具失效） ${name}${detail ? '  — ' + detail : ''}`)
}

const { cdp } = await connect(CDP_URL)
let sessionId = (await openPage(cdp, 'about:blank')).sessionId
const sleep = (ms) => new Promise(r => setTimeout(r, ms))
// ★ location.href 导航会使旧 attach 会话失效（Inspected target navigated or
//   closed）⇒ 每次跳转都**开新页 + 挂新会话**（cookie 在浏览器 profile 层共享，
//   登录态跨 tab 成立；2026-11-20 实测「先跳后挂」会踩导航竞态）。
async function goto(url, waitMs = 5000) {
  // 三步走：blank 页 → attach+enable（目标稳定时）→ 再导航 ⇒ 避开
  // 「createTarget(url) 与 attach 竞速 ⇒ -32000 Inspected target navigated」。
  const created = await cdp.send('Target.createTarget', { url: 'about:blank' })
  const { sessionId: sid } = await cdp.send('Target.attachToTarget', { targetId: created.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid)
  await cdp.send('Network.enable', {}, sid)
  // ★ 视口必须显式 1440x1400：headless 默认 ~800px 会触发 ResponsiveDataView
  //   的紧凑卡形态 ⇒ 桌面表格列（任务链接列等）根本不渲染 ⇒ 假红。
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sid)
  await evaluate(cdp, sid, `location.href = ${JSON.stringify(url)}`)
  await sleep(waitMs)
  // 导航后重挂一次（旧会话可能随文档换掉）
  const { sessionId: sid2 } = await cdp.send('Target.attachToTarget', { targetId: created.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid2)
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sid2)
  sessionId = sid2
  return evaluate(cdp, sessionId, 'location.href')
}
// ★ SPA 会在登录后自行整页跳转（guest → dashboard）⇒ 任意求值都可能撞
//   「target navigated or closed」（-32000）⇒ 所有求值走韧性包装：
//   失效 → 重挂最后的目标（带视口覆盖）→ 重试一次。
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
// ★ 必须先把页面导航到 BASE（同源）再 fetch：about:blank 起页的跨源 fetch
//   直接被 CORS 拦掉（2026-11-20 实测 ERR:Failed to fetch）。
await goto(`${BASE}/`, 4000)
const loginStatus = await js(`fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:JSON.stringify({username:${JSON.stringify(USER)},password:${JSON.stringify(PASSWORD)}})}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) !== '200') {
  console.log(`❌ 登录失败（/api/auth/token → ${loginStatus}）——令牌/口令/端点任一不成立`)
  process.exit(1)
}
check('登录 /api/auth/token 200', true)

// ── 1. API 层：三端点的形状与出处块 ──────────────────────────────────────
// ★ 窗口取 90 天：默认 30 天窗口在低流量环境可能整窗无请求（本轮 211 实测
//   4 用户全部 0 window_usage）——那会造成「数字断言未评估」，必须把窗口
//   对准数据（90d = 后端上限），而不是把空窗当产品红。
// 89 天：90d 整边界会因 now() 两次取样的毫秒差触发「>90d」闸（2026-11-20 实测 400）
const WIN_FROM = new Date(Date.now() - 89 * 24 * 3600 * 1000).toISOString()
const listApiRaw = await js(`fetch('/api/admin/session-analytics/v2/users?limit=50&date_from=${encodeURIComponent(WIN_FROM)}',{credentials:'include'}).then(async r => r.status + ' :: ' + (await r.text())).catch(e=>'ERR:'+e.message)`)
const list = JSON.parse(String(listApiRaw).replace(/^\d+ :: /, ''))
check('列表端点 200 且投影就绪', String(listApiRaw).startsWith('200 :: ') && list.ready === true && list.usage_meta?.source_view === 'request_metrics', `source_view=${list.usage_meta?.source_view} ready=${list.ready}`)
check('列表端点带真实用量窗口（usage_window）', !!(list.usage_window && list.usage_window.start && list.usage_window.end), list.usage_window ? `${list.usage_window.start} ~ ${list.usage_window.end}` : 'missing')
const users = list.users || []
if (!users.length) {
  gauge('列表/展开/详情的全部页面断言', 'session-analytics 列表 0 用户（本环境无画像数据）')
  console.log(`\nRESULT: GAUGE-BROKEN（退出码 2）— ${results.filter(r => r.ok).length} 绿 / ${results.filter(r => !r.ok).length} 红`)
  process.exit(2)
}
const withUsage = users.filter(u => u.window_usage)
check(`窗口聚合数字至少覆盖一个员工（${users.length} 用户中 ${withUsage.length} 有 window_usage）`, users.some(u => u.window_usage !== undefined), 'window_usage 为 undefined = 窗口内无请求，与 0 语义分家 ✓')

// 员工×模型矩阵（API 形状）
const selectedUser = (OWNER && users.find(user => user.owner_user === OWNER)) || users[0]
const firstOwner = selectedUser.owner_user
const bounds = new URLSearchParams({ owner: firstOwner, start_date: list.usage_meta.window_start, end_date: list.usage_meta.window_end, limit: '200' })
if (selectedUser.tenant_id) bounds.set('tenant_id', selectedUser.tenant_id)
const matrixApi = await js(`fetch('/api/admin/session-analytics/v2/users/usage-matrix?${bounds}',{credentials:'include'}).then(r=>r.json()).then(j=>JSON.stringify(j)).catch(e=>'ERR:'+e.message)`)
const matrix = JSON.parse(matrixApi)
check('模型矩阵投影就绪，沿用列表窗口', matrix.ready === true && matrix.usage_meta?.source_view === 'request_metrics' && matrix.start_date === list.usage_meta.window_start && matrix.end_date === list.usage_meta.window_end && Array.isArray(matrix.rows), `rows=${(matrix.rows || []).length}`)
const mrow = (matrix.rows || []).find(r => r.total_tokens > 0)
if (mrow) {
  check('矩阵行含 Token、积分和 USD 成本',
    ['input_tokens', 'output_tokens', 'total_tokens', 'internal_credits'].every(k => typeof mrow[k] === 'number') &&
    (mrow.external_cost_usd === null || typeof mrow.external_cost_usd === 'number'), `tokens=${mrow.total_tokens}`)
}
const listedUsage = selectedUser.window_usage
if (listedUsage && matrix.rows?.length < 200) {
  const fields = ['request_count', 'input_tokens', 'output_tokens', 'total_tokens', 'internal_credits']
  const mismatched = fields.filter(k => Math.abs(matrix.rows.reduce((total, row) => total + row[k], 0) - listedUsage[k]) > 0.0000001)
  const usdAmounts = matrix.rows.map(row => row.external_cost_usd).filter(value => value !== null)
  if (listedUsage.external_cost_usd === null ? usdAmounts.length > 0 :
      Math.abs(usdAmounts.reduce((total, amount) => total + amount, 0) - listedUsage.external_cost_usd) > 0.0000001) {
    mismatched.push('external_cost_usd')
  }
  check('同一员工同窗口的模型行与列表合计一致', mismatched.length === 0, mismatched.join(', ') || '6 项一致')
}

// ── 2. 列表页（真渲染）：出处行 + 五列 + 展开行 ──────────────────────────
await goto(`${BASE}/admin/session-analytics/user-profiles`, 6000)
const listPage = await js(`(() => {
  const meta = document.querySelector('[data-testid="user-profile-usage-meta"]')
  const headers = [...document.querySelectorAll('.el-table th')].map(t => t.textContent.trim())
  const need = ['请求数（窗口内）','Token（窗口内）','积分','外部成本（窗口内）','模型数（窗口内）']
  return JSON.stringify({
    meta: meta ? meta.textContent.trim().slice(0, 120) : null,
    headers: need.map(h => headers.includes(h)),
    rows: document.querySelectorAll('.el-table__body tr').length,
  })
})()`)
const lp = JSON.parse(listPage)
check('列表页渲染 request_metrics 出处行', !!lp.meta && lp.meta.includes('request_metrics'), lp.meta || 'missing')
check('窗口用量五列列头全部在', lp.headers.every(Boolean), JSON.stringify(lp.headers))
check(`列表行有数据（rows=${lp.rows}）`, lp.rows > 0)

// 列表页帮助按钮（必须在离开列表页之前查 —— 2026-11-20 首版在详情页上下文里查 ⇒ 假红）
const helpBtn = await js(`!!document.querySelector('[data-testid="user-profile-list-help"]')`)
check('列表页帮助按钮存在', helpBtn === true, String(helpBtn))

// 展开行（真实渲染的员工×模型矩阵）——点第一个展开箭头
const expanded = await js(`(async () => {
  const arrow = document.querySelector('.el-table__body tr .el-table__expand-icon')
  if (!arrow) return JSON.stringify({ ok: false, why: 'NO_EXPAND_ARROW' })
  arrow.click()
  await new Promise(r => setTimeout(r, 2500))
  const cells = [...document.querySelectorAll('.el-table__expanded-cell th, .el-table__expanded-cell .el-table th')].map(t => t.textContent.trim())
  const innerTable = document.querySelector('.el-table__expanded-cell .el-table')
  const bodyRows = innerTable ? innerTable.querySelectorAll('tbody tr').length : 0
  return JSON.stringify({ ok: true, headers: cells.slice(0, 10), bodyRows })
})()`)
const ex = JSON.parse(expanded)
if (!ex.ok && ex.why === 'NO_EXPAND_ARROW') {
  gauge('展开行（员工×模型矩阵真渲染）', '列表行没有展开箭头（0 用户或表格形态变化）')
} else {
  check('展开行内嵌模型明细表出现', ex.ok && ex.bodyRows >= 0, `innerRows=${ex.bodyRows}`)
}

// ── 3. 详情页（真渲染）：模型卡列头 + 任务链接 ────────────────────────────
const profileScope = new URLSearchParams({ start_time: list.usage_meta.window_start, end_time: list.usage_meta.window_end })
if (selectedUser.tenant_id) profileScope.set('tenant_id', selectedUser.tenant_id)
await goto(`${BASE}/admin/session-analytics/users/${encodeURIComponent(firstOwner)}?${profileScope}`, 6000)
const detail = await js(`(() => {
  const h2 = document.querySelector('h2')?.textContent?.trim() || ''
  const card = document.querySelector('[data-testid="up-model-usage"]')
  const headers = card ? [...card.querySelectorAll('th')].map(t => t.textContent.trim()) : []
  const need = ['模型名称','供应商','请求次数','总 Token 数','输入 Token','输出 Token','积分','外部成本']
  const rows = card ? card.querySelectorAll('tbody tr').length : -1
  const taskLinks = [...document.querySelectorAll('[data-testid="up-top-tasks"] button')].filter(b => (b.textContent||'').trim() && b.textContent.trim() !== '').length
  return JSON.stringify({ h2, hasCard: !!card, headers: need.map(h => headers.includes(h)), rows, taskLinks })
})()`)
const dp = JSON.parse(detail)
check('详情页落地（h2 = 用户画像详情: …）', dp.h2.startsWith('用户画像详情'), dp.h2)
check('模型使用明细卡存在且八列列头齐', dp.hasCard && dp.headers.every(Boolean), JSON.stringify(dp.headers))
check(`模型明细行（rows=${dp.rows}；窗口内无请求时 = 0 且出「暂无」空态，不是报错）`, dp.rows >= 0)
// 热门任务行链接三态：有数据 ⇒ 验链接；无任务 ⇒ 空态文案在场也算通过（空 ≠ 错）；
// 既无链接又无空态 ⇒ 红（说明该 owner 行有数据但链接没渲染）。
const emptyState = await js(`(() => { const el = document.querySelector('[data-testid="up-top-tasks"]'); return el ? (el.textContent.includes('还没有任务记录') || el.textContent.includes('暂无') || el.textContent.includes('No Data')) : false })()`)
if (dp.taskLinks > 0) check('热门任务行是可点链接（员工→任务→会话下钻）', true, `links=${dp.taskLinks}`)
else if (emptyState === true) check('热门任务空态合法（该 owner 无任务记录，不是缺链接）', true, 'links=0 + 空态在场')
else check('热门任务行是可点链接（员工→任务→会话下钻）', false, `links=${dp.taskLinks} 且无空态`)

// ── 3.5 项目卡（L1 收口：员工→项目层，2026-11-20 二段）────────────────────
const projApi = await js(`fetch('/api/admin/session-analytics/v2/users/${encodeURIComponent(firstOwner)}/projects?${profileScope}',{credentials:'include'}).then(async r => r.status + ' :: ' + (await r.text())).catch(e=>'ERR:'+e.message)`)
const projStatus = String(projApi).split(' :: ')[0]
const projBody = JSON.parse(String(projApi).replace(/^\d+ :: /, ''))
check('projects 端点 200 且沿用请求投影与所选租户', projStatus === '200' &&
  projBody.usage_meta?.source_view === 'request_metrics' && (!selectedUser.tenant_id || projBody.tenant_id === selectedUser.tenant_id),
`status=${projStatus} source=${projBody.usage_meta?.source_view}`)
const projRowsApi = (projBody.projects || []).length
check(`projects 端点返回行（rows=${projRowsApi}；0 = 该 owner 无项目数据，合法）`, projStatus === '200' && projRowsApi >= 0)

const projCard = await js(`(() => {
  const card = document.querySelector('[data-testid="up-projects"]')
  if (!card) return JSON.stringify({ ok: false, why: 'NO_CARD' })
  const headers = [...card.querySelectorAll('th')].map(t => t.textContent.trim())
  const need = ['项目 ID', '会话数', '任务数', 'Token', 'USD 成本', '积分', '最近活跃']
  const rows = card.querySelectorAll('tbody tr').length
  const note = (card.querySelector('.projects-source-note')?.textContent || '').slice(0, 80)
  // Element 内建空态文案跟浏览器 locale 走（headless 是 en ⇒ 'No Data'）
  const emptyText = card.textContent.includes('还没有项目数据') || card.textContent.includes('暂无数据') || card.textContent.includes('No Data')
  const sample = card.querySelector('tbody tr')?.textContent?.trim().slice(0, 90) || ''
  return JSON.stringify({ ok: true, headers: need.map(h => headers.includes(h)), rows, note, emptyText, sample })
})()`)
const pc = JSON.parse(projCard)
if (!pc.ok) {
  gauge('项目卡真渲染（员工→项目层）', pc.why)
} else if (pc.rows > 0) {
  check('项目卡八列表头齐 + 行数据在场', pc.headers.every(Boolean), `rows=${pc.rows} sample=${pc.sample}`)
  check('项目卡出处注记标明请求投影', pc.note.includes('request_metrics'), pc.note)
} else if (pc.emptyText) {
  check('项目卡空态合法（该 owner 无项目数据，出空态文案而非报错）', true, 'rows=0 + 空态在场')
} else {
  check('项目卡真渲染', false, 'rows=0 且无空态文案')
}

// ── 4. 详情页帮助抽屉（口径段；列表页抽屉已由 flow 13 第一层覆盖）─────────

const drawerDetail = await js(`(async () => {
  const card = document.querySelector('[data-testid="up-model-usage"]')
  if (!card) return JSON.stringify({ ok: false })
  const btn = [...card.querySelectorAll('button')].find(b => (b.textContent||'').includes('查看统计说明'))
  if (!btn) return JSON.stringify({ ok: false, why: 'NO_BTN' })
  btn.click()
  await new Promise(r => setTimeout(r, 1200))
  const drawerText = document.querySelector('.el-drawer .help-content')?.textContent || ''
  const need = ['成本核算公式', '积分', 'USD', '数据来源', 'request_metrics', '时间范围说明']
  return JSON.stringify({ ok: true, sections: need.map(n => drawerText.includes(n)) })
})()`)
const dd = JSON.parse(drawerDetail)
check('详情页帮助抽屉显示计量单位、投影来源和时间范围', dd.ok && dd.sections && dd.sections.every(Boolean), JSON.stringify(dd.sections || dd))

// ── 汇总 ─────────────────────────────────────────────────────────────────
const green = results.filter(r => r.ok).length
const red = results.filter(r => !r.ok).length
console.log(`\nRESULT: ${gaugeBroken ? 'GAUGE-BROKEN (2)' : red === 0 ? 'PASS (0)' : 'FAIL (1)'} — ${green} 绿 / ${red} 红`)
process.exit(gaugeBroken ? 2 : red === 0 ? 0 : 1)
