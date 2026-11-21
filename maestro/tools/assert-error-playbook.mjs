// assert-error-playbook.mjs — 错误处理手册管理页「第二层 CDP 断言」（2026-11-21 交付轮）
//
// 与 assert-session-analytics.mjs 同一分工：Maestro flow（17-error-playbook.yaml）
// 管「页面能打开 + 视口内断言」；本探针管
//   A. API 层：error-playbook 7 端点状态码与门控原文
//   B. DOM 层：列表页统计卡数字 / 全部表头（含右侧列）/ 行数；详情页徽标/方案区/时间线
//   C. 对拍层：页面渲染数字 == 页面自己收到的那份响应（fetch 拦截同源采样）
//   D. 入口层：菜单里有指向 /admin/error-playbook 的链接（菜单接入的活体证据）
//
// 退出码：0 = 全绿；1 = 产品红；2 = 量具失效。
// ★ 判定口径不自己写：统一走 maestro/tools/result.mjs（§⑰ 纪律）。
//
// 用法：node assert-error-playbook.mjs --cdp http://127.0.0.1:9223 \
//         --url https://llm.itestu.cn --user demoadmin --password '...'
import { connect, openPage, evaluate } from './cdp-client.mjs'
import { summarize, EXIT_FAIL } from './result.mjs'

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, v, i, a) => {
  if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
  return acc
}, []))

const CDP_URL = args.cdp || 'http://127.0.0.1:9223'
const BASE = (args.url || 'http://127.0.0.1:8782').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
const PASSWORD = args.password || process.env.ADMIN_PASSWORD || ''

const checks = []
const skipped = []
function check(name, ok, detail = '') {
  checks.push({ name, pass: ok, detail })
  console.log(`${ok ? '✓' : '✗'} ${name}${detail ? ` — ${detail}` : ''}`)
}
function skip(name, reason) {
  skipped.push({ name, reason })
  console.log(`○ ${name} — 未评估：${reason}`)
}

// ── 登录 + 打开列表页（真浏览器会话；cookie 由 SPA 登录种下）────────────────
const cdp = await connect(CDP_URL)
const page = await openPage(cdp, `${BASE}/`)

// 钉语种（与 runner --lang=zh-CN 双保险；探针侧读回 <html lang> 复核）
await evaluate(cdp, page.sessionId, `localStorage.setItem('llmgw_locale','zh-CN')`)

// SPA 内登录（走页面自己的提交路径 ⇒ HttpOnly cookie 与内存态同源）
const loginOk = await evaluate(cdp, page.sessionId, `(async () => {
  const r = await fetch('/api/auth/token', {
    method: 'POST', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: ${JSON.stringify(USER)}, password: ${JSON.stringify(PASSWORD)} }),
  })
  if (!r.ok) return 'HTTP ' + r.status
  const d = await r.json()
  // SPA 的鉴权是 HttpOnly cookie（服务端会话）；token 存 localStorage 供 SPA 水合
  localStorage.setItem('llmgw_token', d.access_token || d.token || '')
  return 'OK'
})()`)
check('API 登录（/api/auth/token）', loginOk === 'OK', String(loginOk))

// 整页加载列表路由
await evaluate(cdp, page.sessionId, `location.href = '${BASE}/admin/error-playbook'`)
await new Promise((r) => setTimeout(r, 6000))

// 语言钉住复核（钉不住 ⇒ 量具失效：文案判据全部失去前提）
const htmlLang = await evaluate(cdp, page.sessionId, `document.documentElement.lang || ''`)
const langOk = /zh/i.test(String(htmlLang))
if (!langOk) {
  skip('界面语言钉住 zh-CN', `html lang="${htmlLang}"`)
} else {
  check('界面语言钉住 zh-CN（<html lang>）', true, `lang=${htmlLang}`)
}

// ── 同源采样：页面自己 fetch 的 /stats 与 /entries（同一时刻同一浏览器会话）──
const api = await evaluate(cdp, page.sessionId, `(async () => {
  const auth = {}
  const tok = localStorage.getItem('llmgw_token')
  if (tok) auth['Authorization'] = 'Bearer ' + tok
  const s = await fetch('/api/admin/error-playbook/stats', { credentials: 'same-origin', headers: auth })
  const e = await fetch('/api/admin/error-playbook/entries?limit=100', { credentials: 'same-origin', headers: auth })
  const stats = s.ok ? await s.json() : null
  const entries = e.ok ? await e.json() : null
  return { statsStatus: s.status, entriesStatus: e.status, stats, entries }
})()`)
let apiData = null
try { apiData = JSON.parse(api) } catch { apiData = null }

if (!apiData || apiData.statsStatus !== 200 || apiData.entriesStatus !== 200) {
  skip('API 层采样', `stats=${apiData?.statsStatus} entries=${apiData?.entriesStatus}（登录态或路由不可用）`)
} else {
  const st = apiData.stats
  const entries = apiData.entries.entries ?? []
  check('GET /stats 200 + provisioned', st.provisioned === true, `total=${st.total_entries}`)
  check('种子 5 条在库（source=seed）', entries.filter((e) => e.source === 'seed').length === 5,
    `seed=${entries.filter((e) => e.source === 'seed').length}/total=${apiData.entries.total}`)
  check('种子全 prompt 型', entries.filter((e) => e.source === 'seed').every((e) => e.plan_kind === 'prompt'))

  // ── DOM 对拍层（数据落定：等统计卡渲染出「手册条目」）───────────────────
  const dom = await evaluate(cdp, page.sessionId, `(async () => {
    for (let i = 0; i < 40; i++) {
      const cards = [...document.querySelectorAll('.ep-stats [class*="stat"], .ep-stats *')]
        .map((el) => el.textContent || '').join(' ')
      if (cards.includes('手册条目')) break
      await new Promise((r) => setTimeout(r, 500))
    }
    const bodyText = document.body.innerText
    const statCards = [...document.querySelectorAll('.ep-stats .app-stat-card, .ep-stats [class*="stat-card"]')]
      .map((el) => (el.textContent || '').trim())
    const headers = [...document.querySelectorAll('.ep-table th')].map((th) => (th.textContent || '').trim())
    const rows = document.querySelectorAll('.ep-table tbody tr').length
    const menuLink = !!document.querySelector('a[href*="error-playbook"], [data-nav*="error-playbook"]')
    return { bodyText: bodyText.slice(0, 4000), statCards, headers, rows, menuLink }
  })()`)
  let domData = null
  try { domData = JSON.parse(dom) } catch { domData = null }
  if (!domData) {
    skip('DOM 层采样', '页面 JSON 解析失败')
  } else {
    check('统计卡「手册条目」渲染', domData.bodyText.includes('手册条目'))
    // 数字对拍：统计卡的数值 == API 的 total_entries（同一份响应）
    const cardText = domData.statCards.join(' | ')
    const m = cardText.match(/手册条目\W*(\d+)/)
    if (m) {
      check('统计卡数字 == API total_entries', Number(m[1]) === Number(st.total_entries),
        `card=${m[1]} api=${st.total_entries}`)
    } else {
      skip('统计卡数字 == API total_entries', `统计卡文本未解析到数字：${cardText.slice(0, 120)}`)
    }
    check('表头齐全（含右侧列）', ['标题', '错误类型', '方案形态', '来源', '自动执行', '最近执行状态', '最近执行时间']
      .every((h) => domData.headers.some((x) => x.includes(h))),
    `headers=${JSON.stringify(domData.headers)}`)
    check('行数 == entries 返回条数', domData.rows === (apiData.entries.entries ?? []).length,
      `dom=${domData.rows} api=${(apiData.entries.entries ?? []).length}`)
    check('菜单入口存在（/admin/error-playbook）', domData.menuLink)
  }
}

// ── 详情页（seed id=1）徽标 + 方案区 + 时间线 ─────────────────────────────
await evaluate(cdp, page.sessionId, `location.href = '${BASE}/admin/error-playbook/1'`)
await new Promise((r) => setTimeout(r, 6000))
const detail = await evaluate(cdp, page.sessionId, `(async () => {
  for (let i = 0; i < 30; i++) {
    if (document.body.innerText.includes('执行留痕时间线')) break
    await new Promise((r) => setTimeout(r, 500))
  }
  const t = document.body.innerText
  return {
    hasTimeline: t.includes('执行留痕时间线'),
    hasKind: t.includes('AI 提示词'),
    hasSource: t.includes('历史沉淀'),
    hasPlan: t.includes('执行方案'),
    hasPrompt: t.includes('提示词正文'),
    hasExecuteBtn: t.includes('手动执行'),
    hasRevertHint: t.includes('按快照回滚') || true, // 无留痕时该按钮不在 DOM；只作信息项
    entryType: (t.match(/auth/) || [''])[0],
  }
})()`)
let det = null
try { det = JSON.parse(detail) } catch { det = null }
if (!det) {
  skip('详情页 DOM', '页面 JSON 解析失败')
} else {
  check('详情页时间线区块渲染', det.hasTimeline === true)
  check('方案形态徽标（AI 提示词）', det.hasKind === true)
  check('来源徽标（历史沉淀）', det.hasSource === true)
  check('方案编辑器（提示词正文在场）', det.hasPlan && det.hasPrompt)
  check('手动执行按钮在场', det.hasExecuteBtn === true)
}

// ── API 门控原文（422/404；只读无写入）────────────────────────────────────
if (apiData) {
  const gate = await evaluate(cdp, page.sessionId, `(async () => {
    const tok = localStorage.getItem('llmgw_token')
    const auth = tok ? { Authorization: 'Bearer ' + tok } : {}
    const g1 = await fetch('/api/admin/error-playbook/entries', {
      method: 'POST', credentials: 'same-origin',
      headers: { ...auth, 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: 'x' }),
    })
    const g1t = await g1.text()
    const g2 = await fetch('/api/admin/error-playbook/entries', {
      method: 'POST', credentials: 'same-origin',
      headers: { ...auth, 'Content-Type': 'application/json' },
      body: JSON.stringify({ error_type: 't_probe', plan_kind: 'api', plan: { method: 'POST', path: '/api/admin/not-a-route' } }),
    })
    const g2t = await g2.text()
    const g3 = await fetch('/api/admin/error-playbook/entries/999999', { credentials: 'same-origin', headers: auth })
    return { s1: g1.status, t1: g1t, s2: g2.status, t2: g2t, s3: g3.status }
  })()`)
  let g = null
  try { g = JSON.parse(gate) } catch { g = null }
  if (!g) {
    skip('门控原文验证', 'JSON 解析失败')
  } else {
    check('POST entries 缺 error_type → 422 原文', g.s1 === 422 && String(g.t1).includes('error_type is required'),
      `s=${g.s1} body=${String(g.t1).slice(0, 80)}`)
    check('POST entries 未登记路由 → 422 fail-fast 原文', g.s2 === 422 && String(g.t2).includes('is not registered as an executable internal route'),
      `s=${g.s2}`)
    check('GET 不存在条目 → 404', g.s3 === 404)
  }
}

await cdp.close?.()
const out = summarize(checks, { skipped })
console.log(out.line)
process.exit(out.exitCode)
