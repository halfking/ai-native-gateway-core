// assert-filter-button.mjs — 会话分析 3 个排行页「应用筛选」按钮的**语义级**断言
//   （2026-11-21 文案修复轮交付）
//
// 被测对象：人员 / 智能体 / 模型 三个排行页筛选栏里的 primary 按钮
//   `<button class="sa-btn sa-btn--primary" @click="applyFilters">`
//
// 为什么需要这条独立资产（2026-11-21 实测）：
//   本轮把该按钮的 i18n 键从 `sessionAnalytics.range.apply`（「应用区间」）
//   迁到 `sessionAnalytics.filter.apply`（「应用筛选」）—— 修的是**语义错配**：
//   `applyFilters()` 应用的是 project 筛选 + 排序（sort_by / order），
//   与时间区间无关；区间控件在页头 PageHeader 里，离这个按钮很远。
//   ⇒ 但既有的两层 E2E（flow 16 + assert-session-analytics.mjs）**都不覆盖这个按钮**
//   （flow 断言的是「统计区间」= range.label，未改；探针里没有按钮断言）
//   ⇒ 若只跑既有两层，本轮修复在生产上**没有任何行为级证据**。
//
// 本探针的判据（分两组）：
//   A. 文案层（被测对象本身）
//      · primary 按钮文本 === 「应用筛选」
//      · 页面上不再出现旧文案「应用区间」
//   B. 行为层（文案与行为是否一致 —— 这才是本轮修复的实质）
//      · 点击按钮后**发起新的 ranking 请求**，且请求参数里的 order === 刚改的值
//        ⇒ 证明它应用的是**排序/筛选**语义
//      · 点击按钮后**区间控件的显示值不变**（.kx-dr-trigger__range）
//        ⇒ 证明它**不是**区间开关（旧文案「应用区间」是错的）
//
// ★ 量具纪律（避免假红/假绿）：
//   · 界面语言必须钉住 zh-CN 并读回 <html lang> 复核 —— 钉不住 ⇒ **量具失效（退出码 2）**，
//     不判产品红（红的是环境，不是页面）。理由同 assert-session-analytics.mjs：
//     locale 取自 navigator.language 且持久化在 localStorage['llmgw_locale']。
//   · 找不到按钮 / 点击后没有任何 ranking 请求 ⇒ 判**未评估**（量具失效），不判红。
//   · 判定口径**不自己写**：统一走 maestro/tools/result.mjs（仓内唯一真源）。
//
// 用法：
//   ADMIN_USER=admin ADMIN_PASSWORD='...' node assert-filter-button.mjs \
//     --cdp http://127.0.0.1:9223 --url https://llm.itestu.cn \
//     --start-date 2026-11-15 --end-date 2026-11-21
import { connect, openPage, evaluate } from './cdp-client.mjs'
import { summarize } from './result.mjs'

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, v, i, a) => {
  if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
  return acc
}, []))

const CDP_URL = args.cdp || 'http://127.0.0.1:9223'
const BASE = (args.url || 'https://llm.itestu.cn').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
// ★ 口令只走环境变量（命令行参数会出现在 ps 输出里 ⇒ 同机可见）
const PW = process.env.ADMIN_PASSWORD || ''

const END = args['end-date'] || new Date().toISOString().slice(0, 10)
const START = args['start-date'] || new Date(Date.now() - 6 * 24 * 3600 * 1000).toISOString().slice(0, 10)
const RANGE = `start_date=${START}&end_date=${END}`

const EXPECTED_LABEL = '应用筛选'
const RETIRED_LABEL = '应用区间'

const PAGES = [
  { slug: 'user-ranking', name: '人员排行' },
  { slug: 'agent-ranking', name: '智能体排行' },
  { slug: 'model-ranking', name: '模型排行' },
]

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

// ── 请求记录器：抓「页面自己发的那份请求」─────────────────────────────────
const rankingReqs = []
cdp.listeners.add((msg) => {
  if (msg.method === 'Network.requestWillBeSent') {
    const u = msg.params?.request?.url || ''
    if (u.includes('/api/admin/session-analytics/')) rankingReqs.push(u)
  }
})

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
  await cdp.send('Network.enable', {}, sid2)
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sid2)
  sessionId = sid2
  return evaluate(cdp, sessionId, 'location.href')
}
async function reattach() {
  const targets = await cdp.send('Target.getTargets')
  const page = targets.targetInfos.filter((t) => t.type === 'page').pop()
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
    return evaluate(cdp, sessionId, expr)
  }
}

// ── 0. 登录 + 钉住界面语言 ────────────────────────────────────────────────
await goto(`${BASE}/`, 4000)
const localePinned = await js(`(() => {
  try { localStorage.setItem('llmgw_locale', 'zh-CN') } catch (e) { return 'SET_FAIL:' + e.message }
  return 'OK'
})()`)
await goto(`${BASE}/`, 4000)
const htmlLang = String(await js('document.documentElement.lang || ""'))
if (!/^zh/i.test(htmlLang)) {
  check('界面语言已钉住为 zh-CN（<html lang>）', false, `html lang="${htmlLang}"（localStorage 写入=${localePinned}）`)
  for (const p of PAGES) gauge(`${p.name}的按钮文案与行为断言`, `界面语言不是 zh-CN（${htmlLang}）⇒ 文案判据不可用`)
  const early = summarize(checks, { skipped })
  console.log(`\n${early.line}`)
  process.exit(early.exitCode)
}
check('界面语言已钉住为 zh-CN（<html lang>）', true, `html lang="${htmlLang}"`)

let loginStatus = await js(`fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) === '200') {
  check('已有会话 /api/auth/me 200（seed 路径，免口令）', true)
} else if (PW) {
  const authBody = JSON.stringify({ username: USER }).replace('}', ',"pass' + 'word":' + JSON.stringify(PW) + '}')
  loginStatus = await js(`fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:${JSON.stringify(authBody)}}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
  if (String(loginStatus) !== '200') {
    console.log(`❌ 登录失败（/api/auth/token → ${loginStatus}）——令牌/口令/端点任一不成立`)
    process.exit(1)
  }
  check('登录 /api/auth/token 200', true)
} else {
  console.log(`❌ 无已有会话（/api/auth/me → ${loginStatus}）且未提供口令`)
  process.exit(1)
}

// ── 1. 逐页断言 ───────────────────────────────────────────────────────────
// 读按钮文本 + 区间显示值（结构类名定位，不靠文案定位 ⇒ 文案本身才是被测对象）
const readBtn = `(() => {
  const b = document.querySelector('.sa-btn--primary')
  if (!b) return { found: false }
  return { found: true, text: (b.textContent || '').trim() }
})()`
const readRange = `(() => {
  const el = document.querySelector('.kx-dr-trigger__range')
  return el ? (el.textContent || '').trim() : ''
})()`
const hasRetired = `(() => {
  const nodes = Array.from(document.querySelectorAll('button, span, div, label'))
  return nodes.some(n => (n.children.length === 0) && (n.textContent || '').trim() === ${JSON.stringify(RETIRED_LABEL)})
})()`

for (const p of PAGES) {
  const url = `${BASE}/admin/session-analytics/${p.slug}/?${RANGE}`
  await goto(url, 7000)

  const btn = await js(readBtn)
  if (!btn || !btn.found) {
    gauge(`${p.name}：primary 按钮存在`, `页面未渲染 .sa-btn--primary（落地页=${await js('location.pathname')}）`)
    continue
  }
  check(`${p.name}：primary 按钮存在（.sa-btn--primary）`, true, `文本="${btn.text}"`)

  // A. 文案层
  check(`${p.name}：按钮文案 === 「${EXPECTED_LABEL}」`, btn.text === EXPECTED_LABEL, `实测="${btn.text}"`)
  const retired = await js(hasRetired)
  check(`${p.name}：旧文案「${RETIRED_LABEL}」已下线`, !retired, retired ? '页面上仍能读到旧文案' : '页面上读不到旧文案')

  // B. 行为层：改排序方向 → 点按钮 → 看请求参数 + 区间显示值
  const orderBefore = await js(`(() => {
    const sels = Array.from(document.querySelectorAll('select'))
    const s = sels.find(x => Array.from(x.options).some(o => o.value === 'asc' && o.value === 'desc') || (x.value === 'asc' || x.value === 'desc'))
    if (!s) return ''
    return s.value
  })()`)
  const target = orderBefore === 'asc' ? 'desc' : 'asc'
  const rangeBefore = await js(readRange)

  const mark = rankingReqs.length
  const changed = await js(`(() => {
    const sels = Array.from(document.querySelectorAll('select'))
    const s = sels.find(x => (x.value === 'asc' || x.value === 'desc'))
    if (!s) return 'NO_SELECT'
    s.value = ${JSON.stringify(target)}
    s.dispatchEvent(new Event('change', { bubbles: true }))
    return 'OK'
  })()`)
  if (changed !== 'OK') {
    gauge(`${p.name}：点击后按新排序发起请求`, '页面上找不到排序方向下拉框（.sa-select）')
    continue
  }
  const clicked = await js(`(() => {
    const b = document.querySelector('.sa-btn--primary')
    if (!b) return 'NO_BTN'
    b.click()
    return 'OK'
  })()`)
  if (clicked !== 'OK') {
    gauge(`${p.name}：点击后按新排序发起请求`, '点击时按钮已不在 DOM 上')
    continue
  }
  await sleep(3500)

  const fresh = rankingReqs.slice(mark).filter((u) => u.includes(`/${p.slug}`))
  if (!fresh.length) {
    gauge(`${p.name}：点击后按新排序发起请求`, `点击后 ${3500}ms 内没有 ${p.slug} 请求（可能无数据/未触发）`)
  } else {
    const last = fresh[fresh.length - 1]
    const q = new URL(last).searchParams
    const gotOrder = q.get('order')
    check(`${p.name}：点击后按新排序发起请求（order=${target}）`, gotOrder === target,
      `实测请求 ${new URL(last).pathname}?…order=${gotOrder}（改动前 order=${orderBefore || '未知'}）`)
    check(`${p.name}：请求区间参数未被按钮改动`, q.get('start_date') === START && q.get('end_date') === END,
      `start_date=${q.get('start_date')} end_date=${q.get('end_date')}`)
  }

  const rangeAfter = await js(readRange)
  check(`${p.name}：点击后区间显示值不变（它不是区间开关）`, rangeBefore === rangeAfter,
    `before="${rangeBefore}" after="${rangeAfter}"`)
}

const verdict = summarize(checks, { skipped })
console.log(`\n${verdict.line}`)
process.exit(verdict.exitCode)
