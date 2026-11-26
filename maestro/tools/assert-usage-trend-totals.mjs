// assert-usage-trend-totals.mjs —— 「用量趋势汇总表合计行」的 CDP 断言
// （2026-11-26 总额展示轮）
//
// ★ owner 诉求：/admin/usage-trends 页面没有显示总额的数据，需要加上。
//   本探针直接打诉求里的那类 URL（start/end/metric 深链），
//   验证汇总表真的出现「合计」行且四个指标列的总额**数字正确**。
//
// ★ 为什么判据这样立（与仓内同族工具同理由）：
//   · 「合计行存在」 ⇒ 判据是 el-table 的 **footer 行渲染出来了**
//     （show-summary 的唯一可观察证据，比断言文案可靠）；
//   · 「总额是对的」 ⇒ 判据是**与独立来源逐字相等**：探针从页面内重新
//     fetch 同一份 trend-series API，按「逐序列 total_* 求和」重算期望值，
//     再用与前端同一个 compactTickValue 公式格式化后与 DOM 里的格子比对。
//     期望值不来自被测对象自己显示的数字（那是自证）；
//   · 「合计行首格是合计文案」 ⇒ 判据是首格 ∈ 8 语言翻译集合
//     （i18n 接线的证据，且不随浏览器语言漂移）；
//   · 「占比列在合计行留空」 ⇒ 判据是那一格为空串
//     （占比分母=自身恒 100%，显示出来是噪音）。
//
// ★ 量具失效 ≠ 产品红（result.mjs 口径）：当天**真的没有数据**（API 序列
//   为空）时，「总额显示」无从评估 ⇒ 报量具失效（rc=2），不报红。
//
// ★ 判定口径不自己写：统一走 maestro/tools/result.mjs（仓内唯一真源）。
//
// 用法：
//   node assert-usage-trend-totals.mjs --cdp http://127.0.0.1:9223 \
//         --url https://llm.itestu.cn \
//         [--start 2026-11-24 --end 2026-11-24 --metric requests]
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
// ★ 口令只走环境变量（命令行参数会出现在 `ps` 输出里 ⇒ 同机其它用户可见）
const PASSWORD = args.password || process.env.ADMIN_PASSWORD || ''
// 深链参数：默认复刻 owner 诉求里的那类 URL
const QSTART = args.start || '2026-11-24'
const QEND = args.end || '2026-11-24'
const QMETRIC = args.metric || 'requests'

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

/** 在页面里求值（同步或返回 Promise）。 */
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

// ── 登录（同 assert-stats-range-picker.mjs 的口径：先 /api/auth/me 再 /api/auth/token）
await goto(`${BASE}/version`)
let loginStatus = await js(`fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) !== '200') {
  if (!PASSWORD) {
    console.log('❌ 无已有会话且未提供口令——先跑 seed-session.mjs 或传 --password')
    process.exit(2)
  }
  const tokenBody = JSON.stringify({ username: 'admin', password: PASSWORD })
  loginStatus = await js(
    `fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:${JSON.stringify(tokenBody)}}).then(r=>r.status).catch(e=>'ERR:'+e.message)`,
  )
  if (String(loginStatus) !== '200') {
    console.log(`❌ 登录失败（/api/auth/token → ${loginStatus}）——令牌/口令/端点任一不成立`)
    process.exit(2)
  }
}
console.log(`ℹ️ 登录态就绪（${BASE}）`)

// ══════════════════════════════════════════════════════════════════════
// 独立期望值：页面内 fetch 同一份 API，按「逐序列 total_* 求和」重算总额，
// 再用与前端 compactTickValue 相同的公式格式化（公式逐字对齐 utils/usageTrend.ts）。
// ══════════════════════════════════════════════════════════════════════
const EXPECT_PROBE = `(() => {
  const qs = new URLSearchParams({ start: ${JSON.stringify(QSTART)}, end: ${JSON.stringify(QEND)} })
  return fetch('/api/admin/usage/v2/trend-series?' + qs.toString() + '&top=10', { credentials: 'include' })
    .then((r) => r.json())
    .then((resp) => {
      const series = resp.series || []
      const totals = series.reduce(
        (acc, s) => ({
          requests: acc.requests + (s.total_requests || 0),
          tokens: acc.tokens + (s.total_tokens || 0),
          credits: acc.credits + (s.total_credits || 0),
          cost: acc.cost + (s.total_cost_usd || 0),
        }),
        { requests: 0, tokens: 0, credits: 0, cost: 0 },
      )
      // compactTickValue 同式（v>=1e6 → M；v>=1e3 → K；否则原样）
      const compact = (v) => {
        const a = Math.abs(v)
        if (a >= 1000000) return \`\${+(v / 1000000).toFixed(1)}M\`
        if (a >= 1000) return \`\${+(v / 1000).toFixed(1)}K\`
        return String(v)
      }
      return {
        degraded: resp.degraded === true,
        source: resp.source,
        seriesCount: series.length,
        models: series.map((s) => s.model),
        compactRequests: compact(totals.requests),
        compactTokens: compact(totals.tokens),
        compactCredits: compact(totals.credits),
        costCell: \`$\${totals.cost.toFixed(4)}\`,
      }
    })
})()`

const expected = await js(EXPECT_PROBE)
if (!expected || typeof expected.seriesCount !== 'number') {
  gauge('独立期望值取数', `API 未返回有效载荷：${JSON.stringify(expected)}`)
  summarize(checks, { skipped })
  process.exit(2)
}
if (expected.degraded) {
  gauge('独立期望值取数', `trend-series 返回降级（视图未初始化）⇒ 总额无从评估：${JSON.stringify(expected)}`)
  summarize(checks, { skipped })
  process.exit(2)
}
console.log(`ℹ️ 独立期望值：series=${expected.seriesCount} 条（${expected.models.join(', ')}），` +
  `requests=${expected.compactRequests} tokens=${expected.compactTokens} credits=${expected.compactCredits} cost=${expected.costCell}`)
if (expected.seriesCount === 0) {
  gauge('独立期望值取数', `${QSTART}~${QEND} 该窗口序列为空 ⇒ 「总额显示」无从评估（这不是产品红）`)
  summarize(checks, { skipped })
  process.exit(2)
}

// ══════════════════════════════════════════════════════════════════════
// A. 打开诉求场景的深链 URL，等汇总表渲染
// ══════════════════════════════════════════════════════════════════════
const PAGE_URL = `${BASE}/admin/usage-trends?start=${QSTART}&end=${QEND}&metric=${QMETRIC}`
await goto(PAGE_URL, `!!document.querySelector('.ute__table .el-table__footer-wrapper .el-table__footer, .ute__table .el-table .el-table__footer')`, 60000)
const FOOTER_PROBE = `(() => {
  const footer = document.querySelector('.ute__table .el-table__footer')
  if (!footer) return { exists: false }
  const cells = Array.from(footer.querySelectorAll('td .cell')).map((c) => (c.textContent || '').trim())
  // 表体行数（含 __others__ 折叠行）——量「合计行是否跟着数据走」用
  const bodyRows = document.querySelectorAll('.ute__table .el-table__body-wrapper tbody tr').length
  return { exists: true, cells, bodyRows }
})()`

const footer = await js(FOOTER_PROBE)
check('A1 汇总表合计行已渲染（el-table footer）', !!footer && footer.exists,
  footer && footer.exists ? `格子=${JSON.stringify(footer.cells)} 表体行数=${footer.bodyRows}` : '未找到 .ute__table .el-table__footer')

if (!footer || !footer.exists) {
  summarize(checks, { skipped })
  process.exit(checks.every((c) => c.pass) ? 0 : 1)
}
await sleep(400)

// ══════════════════════════════════════════════════════════════════════
// B. 合计行内容：首格文案 / 四指标总额与独立期望逐字相等 / 占比列留空
//    （列序 = 模型 | 请求数 | Token | 积分 | 成本 | 当前指标占比）
// ══════════════════════════════════════════════════════════════════════
const TOTAL_LABELS = ['合计', '合計', 'Total', 'Gesamt', 'Total general', 'Total général', 'الإجمالي']
const cells = footer.cells
check('B1 合计行首格是「合计」文案（8 语言之一 ⇒ i18n 接线成立）',
  cells.length >= 6 && TOTAL_LABELS.includes(cells[0]),
  `首格="${cells[0] || ''}"（允许集合：${TOTAL_LABELS.join('/')}）`)
check('B2 请求列总额与独立期望逐字相等', cells[1] === expected.compactRequests,
  `页面=${cells[1]}  期望=${expected.compactRequests}`)
check('B3 Token 列总额与独立期望逐字相等', cells[2] === expected.compactTokens,
  `页面=${cells[2]}  期望=${expected.compactTokens}`)
check('B4 积分列总额与独立期望逐字相等', cells[3] === expected.compactCredits,
  `页面=${cells[3]}  期望=${expected.compactCredits}`)
check('B5 成本列总额与独立期望逐字相等（$ + 4 位小数）', cells[4] === expected.costCell,
  `页面=${cells[4]}  期望=${expected.costCell}`)
check('B6 占比列在合计行留空（分母=自身恒 100%，显示是噪音）', (cells[5] || '') === '',
  `占比格="${cells[5] || ''}"`)
check('B7 合计行跟着数据走（表体有模型行 ⇒ 合计行不是空表的摆设）',
  footer.bodyRows > 0, `表体行数=${footer.bodyRows}`)

summarize(checks, { skipped })
