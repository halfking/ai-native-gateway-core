#!/usr/bin/env node
// maestro/tools/assert-analytics-tenant.mjs —— /admin/analytics/tenant/default 带登录态两层验证
//
// 第一层：页面真的打开且渲染（不被 /forbidden 重定向、无错误横幅、有数据区块）
// 第二层：页面数字与「页面自己收到的那份响应」逐字对拍（同源、同时刻，不需容差）
// 第三层：布局高度测量（usage-trends 塌陷教训——「空白」可能是 0 高度不是没数据）
//
// 登录方式：API 签发 session → CDP Network.setCookie 种 HttpOnly cookie
//   （headless Chrome 无法走 SPA 登录表单交互的可靠路径；种 cookie 是仓内先例）
//
// 用法： bash maestro/run-analytics-tenant-test.sh

import { connect, openPage, evaluate, waitFor } from './cdp-client.mjs'
import { setTimeout as sleep } from 'timers/promises'

const BASE_URL = process.argv[2] || 'https://llm.itestu.cn'
const CDP_URL = process.argv[3] || 'http://127.0.0.1:9222'
const ADMIN_USER = process.env.ADMIN_USER || 'demoadmin'
const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD || 'Demo@admin123'
const TARGET = `${BASE_URL}/admin/analytics/tenant/default?days=7`

const results = []
let cdp, ws, sessionId

function record(name, pass, details = '') {
  results.push({ name, pass, details })
  const mark = pass ? '✓' : '✗'
  console.log(`${pass ? '\x1b[32m' : '\x1b[31m'}${mark}\x1b[0m ${name}${details ? ` — ${details}` : ''}`)
}

async function main() {
  // ── 0. API 登录拿 session cookie ──────────────────────────────
  console.log(`\n=== 0. API 登录（${ADMIN_USER}） ===`)
  const loginRes = await fetch(`${BASE_URL}/api/auth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASSWORD }),
  })
  if (!loginRes.ok) {
    console.error(`登录失败 HTTP ${loginRes.status}: ${await loginRes.text()}`)
    process.exit(2)
  }
  const setCookie = loginRes.headers.get('set-cookie') || ''
  const m = setCookie.match(/llmgw_session=([^;]+)/)
  if (!m) {
    console.error(`登录响应无 llmgw_session cookie。set-cookie: ${setCookie}`)
    process.exit(2)
  }
  const sessionValue = m[1]
  console.log(`✓ 登录成功，session 长度 ${sessionValue.length}`)

  // 直连 API 冒烟（脱离浏览器的基线）
  const apiBaseline = await fetch(`${BASE_URL}/api/admin/analytics/tenant/default?days=7`, {
    headers: { Cookie: `llmgw_session=${sessionValue}` },
  })
  record('API 直连（cookie 登录后）返回 200', apiBaseline.status === 200, `HTTP ${apiBaseline.status}`)
  let baseline = null
  if (apiBaseline.ok) {
    baseline = await apiBaseline.json()
    console.log(`   直连 summary: total_requests=${baseline.summary?.total_requests} success_rate=${(baseline.summary?.success_rate * 100).toFixed(1)}% cost=$${(baseline.summary?.total_cost_usd ?? 0).toFixed(2)}`)
  } else {
    console.log(`   直连响应体: ${await apiBaseline.text()}`)
  }

  // ── 1. CDP 起 Chrome、种 cookie ───────────────────────────────
  console.log('\n=== 1. CDP 连接 + 种 session cookie ===')
  ;({ ws, cdp } = await connect(CDP_URL))
  ;({ sessionId } = await openPage(cdp))
  await cdp.send('Network.clearBrowserCookies', {}, sessionId)
  const url = new URL(BASE_URL)
  await cdp.send('Network.setCookie', {
    name: 'llmgw_session',
    value: sessionValue,
    domain: url.hostname,
    path: '/',
    secure: true,
    httpOnly: true,
  }, sessionId)
  const planted = await cdp.send('Network.getCookies', { urls: [BASE_URL] }, sessionId)
  record('session cookie 已种入浏览器', planted.cookies.some(c => c.name === 'llmgw_session'))

  // ── 2. 抓「页面自己收到的那份响应」 ───────────────────────────
  console.log('\n=== 2. 打开目标页面 + 同源抓包 ===')
  const captured = { api: null, apiStatus: 0, apiBody: null }
  const onPageEvent = (msg) => {
    if (msg.sessionId !== sessionId) return
    const p = msg.params
    if (msg.method === 'Network.responseReceived' && ['XHR', 'Fetch'].includes(p.type) && p.response.url.includes('/api/admin/analytics/tenant/default')) {
      captured.api = p.requestId
      captured.apiStatus = p.response.status
    }
  }
  ws.addEventListener('message', (ev) => { try { onPageEvent(JSON.parse(ev.data)) } catch {} })

  await cdp.send('Page.navigate', { url: TARGET }, sessionId)
  await sleep(4000)
  try {
    await waitFor(cdp, sessionId,
      `document.readyState === 'complete' && !document.querySelector('.ad-loading')`, 15000)
  } catch { /* 继续用现有 DOM 断言 */ }
  await sleep(1500)

  if (captured.api) {
    try {
      const { body } = await cdp.send('Network.getResponseBody', { requestId: captured.api }, sessionId)
      captured.apiBody = JSON.parse(body)
    } catch (e) { console.log(`   (抓响应体失败: ${e.message})`) }
  }
  record('页面发起了 analytics API 请求', captured.api !== null)
  record('页面收到的 API 响应为 200', captured.apiStatus === 200, `HTTP ${captured.apiStatus}`)

  // ── 3. 第一层：页面渲染断言（结构类名，不锚文案） ────────────
  console.log('\n=== 3. 页面渲染断言 ===')
  const href = await evaluate(cdp, sessionId, 'window.location.href')
  record('未被重定向到 /forbidden', !href.includes('/forbidden'), href)
  record('停留在目标页面', href.includes('/admin/analytics/tenant/default'), href)

  const errText = await evaluate(cdp, sessionId, `document.querySelector('.ad-error')?.textContent?.trim() || ''`)
  record('无错误横幅（.ad-error）', errText === '', errText ? `显示: ${errText.slice(0, 120)}` : '')

  const hasTitle = await evaluate(cdp, sessionId, `document.querySelector('.ad-title') !== null`)
  record('页面标题渲染（.ad-title）', hasTitle)

  const summaryCards = await evaluate(cdp, sessionId, `document.querySelectorAll('.ad-section').length`)
  record('数据区块渲染（.ad-section ≥1）', summaryCards >= 1, `${summaryCards} 个区块`)

  const summaryCardsEl = await evaluate(cdp, sessionId, `document.querySelectorAll('.ad-section')[0]?.getBoundingClientRect().height ?? 0`)
  record('第一个数据区块高度 > 0（塌陷检查）', summaryCardsEl > 20, `${Math.round(summaryCardsEl)}px`)

  const tableRows = await evaluate(cdp, sessionId, `document.querySelectorAll('table tbody tr').length`)
  record('表格有数据行', tableRows > 0, `${tableRows} 行`)

  // ── 4. 第二层：页面数字 vs 页面自己收到的响应 逐字对拍 ────────
  console.log('\n=== 4. 页面数字与同源响应对拍 ===')
  if (captured.apiBody?.summary) {
    const s = captured.apiBody.summary
    const pageText = await evaluate(cdp, sessionId, `document.body.innerText`)
    const totalStr = (s.total_requests ?? 0).toLocaleString('en-US')
    const hit = pageText.includes(totalStr) || pageText.includes(String(s.total_requests))
    record('页面显示的请求数与响应一致', hit, `响应 total_requests=${s.total_requests}，页面含 "${totalStr}": ${hit}`)
    if (baseline) {
      record('页面响应与直连基线同构（total_requests 同为数值）',
        typeof captured.apiBody.summary.total_requests === typeof baseline.summary.total_requests,
        `页面=${captured.apiBody.summary.total_requests} 直连=${baseline.summary.total_requests}`)
    }
  } else if (captured.apiBody) {
    console.log('   响应体:', JSON.stringify(captured.apiBody).slice(0, 300))
    record('响应包含 summary', false, '响应无 summary 字段')
  } else {
    record('响应包含 summary', false, '未抓到响应体')
  }

  // ── 5. 截图证据 ────────────────────────────────────────────────
  const shot = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
  const fs = await import('fs')
  fs.mkdirSync('docs/audit/evidence', { recursive: true })
  const shotPath = 'docs/audit/evidence/2026-11-23-analytics-tenant-page.png'
  fs.writeFileSync(shotPath, Buffer.from(shot.data, 'base64'))
  console.log(`\n📸 截图: ${shotPath}`)

  // ── 汇总 ──────────────────────────────────────────────────────
  const pass = results.filter(r => r.pass).length
  const fail = results.length - pass
  console.log(`\n=== 汇总: ${pass}/${results.length} 通过, ${fail} 失败 ===`)
  if (fail > 0) {
    console.log('失败项:')
    results.filter(r => !r.pass).forEach(r => console.log(`  ✗ ${r.name} — ${r.details}`))
  }
  console.log(fail === 0 ? `\n🎉 RESULT: PASS（${results.length} 条）` : `\n❌ RESULT: FAIL（${fail} 条失败 / ${results.length} 条）`)
  ws.close()
  process.exit(fail === 0 ? 0 : 1)
}

main().catch(e => { console.error('💥 探针失效:', e.message); process.exit(2) })
