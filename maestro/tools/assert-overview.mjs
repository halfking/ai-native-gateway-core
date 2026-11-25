#!/usr/bin/env node
// maestro/tools/assert-overview.mjs —— 简易模式「总览页」的**页面内**数值断言。
//
// ★ 为什么需要它（2026-11-17 实测）：
//   Maestro 2.11.0 的 **web 驱动不支持 evalScript**（`CdpWebDriver` 没有
//   `evaluateScript` 方法）⇒ 没法在 flow 里读 DOM 数值、也没法把页面里的
//   API 响应与页面上的数字**逐字对拍**。
//   而「统计口径与 /api/admin/dashboard/v2/board 逐字一致」恰恰**必须在页面内做**：
//   同一页面、同一时刻、同一份响应 ⇒ 口径天然一致；在外面用 curl 对拍会
//   因为「两次请求之间流量在涨」而假红。
//
// ★ 本脚本做什么（全部在页面里一次求值）：
//   1. 统计卡恰好 6 张、快捷操作恰好 4 个（结构计数）
//   2. 六张卡的 label / value 逐字抓出
//   3. 与 /api/admin/dashboard/v2/board 的 summary **同页同刻**对拍：
//      今日请求 ↔ total_requests；活跃密钥 ↔ active_api_keys；
//      可用模型 ↔ active_models
//   4. 「读不到 ≠ 0」：统计源不可读时，卡片必须是占位符而不是 0
//   5. 不得出现 per-key 的 /api/usage/{id}/models 扇出（上一版实现是 208 次）
//   6. 状态条 data-tone 必须是 ok / warn / blocked 之一
//   7. 三步接入：不存在，或恰好三步
//   8. 上游健康分级：各档计数，且「未探活」必须单列一档
//   9. 接入清单的两个分页列表：都在、行数 ≤ 每页条数、分页栏到位、
//      「用得最多」的累计请求数单调不增
//  10. 翻页行为：真的点一次「下一页」⇒ 首行与页码都必须变
//
// 用法：
//   node maestro/tools/assert-overview.mjs \
//     --cdp http://127.0.0.1:9222 \
//     --url https://llm.itestu.cn \
//     --token-file /tmp/llmgw-jwt.txt
//
// ★ 安全约束：JWT 只从 --token-file 读（0600 临时文件），**不打印**；
//   调用方负责用完后删掉那个文件。
// 退出码：0 = 全绿；1 = 有断言失败（报告里逐条列出）。

import { readFileSync } from 'node:fs'
import { connect, evaluate, openPage, waitFor } from './cdp-client.mjs'

function parseArgs(argv) {
  const out = { cdp: 'http://127.0.0.1:9222', url: '', tokenFile: '', mode: 'simple' }
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (a === '--cdp') out.cdp = argv[++i]
    else if (a === '--url') out.url = argv[++i]
    else if (a === '--token-file') out.tokenFile = argv[++i]
    else if (a === '--mode') out.mode = argv[++i]
  }
  return out
}

const USER_INFO = {
  id: 41,
  tenant_id: 'default',
  username: 'admin',
  display_name: 'admin',
  email: '',
  role: 'super_admin',
  enabled: true,
}

/**
 * 页面内的对拍脚本（一次求值，返回结构化报告）。
 * ★ 刻意写成同步 XHR：要在**同一时刻**取到 board API 的响应，才能与页面上
 *   已经渲染出来的数字逐字比。异步 promise 会让「取数时刻」晚于「渲染时刻」。
 */
const PAGE_PROBE = `(() => {
  const q = (s) => document.querySelector(s)
  const qa = (s) => Array.from(document.querySelectorAll(s))
  const text = (el) => ((el && el.textContent) || '').trim()

  const out = { ok: true, checks: [], facts: {} }

  const check = (name, pass, detail) => {
    out.checks.push({ name, pass: !!pass, detail: detail === undefined ? '' : String(detail) })
    if (!pass) out.ok = false
  }

  // ── 1. 结构计数 ─────────────────────────────────────────────────────────
  const cards = qa('[data-testid="overview-stats"] .app-stat-card')
  const quickButtons = qa('[data-testid="overview-quick-actions"] button')
  out.facts.statCards = cards.length
  out.facts.quickActions = quickButtons.length
  check('统计卡恰好 6 张', cards.length === 6, '实际 ' + cards.length)
  check('常用操作恰好 4 个', quickButtons.length === 4, '实际 ' + quickButtons.length)

  // ── 2. 卡片内容 ─────────────────────────────────────────────────────────
  const cardData = cards.map((c) => ({
    label: text(c.querySelector('.app-stat-card__label')),
    value: text(c.querySelector('.app-stat-card__value')),
    sub: text(c.querySelector('.app-stat-card__sub')),
  }))
  out.facts.cards = cardData
  const byLabel = (needle) => cardData.find((c) => c.label.includes(needle)) || null
  const requestsCard = byLabel('请求')
  const keysCard = byLabel('密钥')
  const modelsCard = byLabel('模型')
  check('六张卡都有标签与值', cardData.every((c) => c.label && c.value), JSON.stringify(cardData.map((c) => c.label)))

  // ── 3. 与 board API 同页同刻对拍 ────────────────────────────────────────
  let board = null
  let boardError = ''
  try {
    const x = new XMLHttpRequest()
    x.open('GET', '/api/admin/dashboard/v2/board?days=1', false)
    x.send()
    if (x.status >= 200 && x.status < 300) board = JSON.parse(x.responseText)
    else boardError = 'HTTP ' + x.status
  } catch (e) {
    boardError = (e && e.name) || 'xhr-failed'
  }
  out.facts.boardError = boardError
  check('今日统计使用 request_metrics', board?.source === 'request_metrics', board?.source || boardError || '未报告来源')
  const summary = (board && board.summary) || null
  out.facts.boardSummary = summary

  if (summary) {
    const num = (s) => String(s == null ? '' : s).replace(/,/g, '').trim()
    const uiRequests = requestsCard ? num(requestsCard.value) : ''
    const apiRequests = num(summary.total_requests)
    // ★ 活流量漂移容差：页面渲染 → 探针 XHR 之间新请求持续落账（211 实测几秒内 +9）。
    //   UI ≤ API 且差 ≤50 ⇒ 视为同源漂移并披露；取错字段/量纲仍会以数量级爆红。
    const reqDelta = (Number(apiRequests) || 0) - (Number(uiRequests) || 0)
    const reqOk = uiRequests === apiRequests || (reqDelta >= 0 && reqDelta <= 50)
    check(
      '今日请求 = board.total_requests（容活流量漂移 ≤50 并披露）',
      reqOk,
      'UI=' + uiRequests + ' API=' + apiRequests + (uiRequests !== apiRequests ? '（漂移 +' + reqDelta + ' 笔）' : ''),
    )

    // 活跃密钥展示成「启用数 / 总数」⇒ 取斜杠前半
    const uiKeys = keysCard ? num(String(keysCard.value).split('/')[0] || '') : ''
    const apiKeys = num(summary.active_api_keys)
    check('活跃密钥启用数 = board.active_api_keys', uiKeys === apiKeys, 'UI=' + uiKeys + ' API=' + apiKeys)

    const uiModels = modelsCard ? num(modelsCard.value) : ''
    const apiModels = num(summary.active_models)
    check('可用模型 = board.active_models', uiModels === apiModels, 'UI=' + uiModels + ' API=' + apiModels)

    // ── 4. 「读不到 ≠ 0」：源可读就不许出现无出处的 0 ────────────────────
    const zeroCards = cardData.filter((c) => c.value === '0').map((c) => c.label)
    const zeroExpected = (num(summary.total_requests) === '0') ? ['今日请求'] : []
    check(
      '没有无出处的 0（源可读时）',
      zeroCards.every((l) => zeroExpected.includes(l)),
      '值为 0 的卡：' + JSON.stringify(zeroCards),
    )
  } else {
    // 源不可读 ⇒ 卡片必须是占位符，不能是 0
    const placeholders = cardData.filter((c) => c.value === '暂不可用').length
    const zeros = cardData.filter((c) => c.value === '0').length
    out.facts.boardUnreadable = true
    check('统计源不可读时不得显示 0', zeros === 0, '值为 0 的卡数=' + zeros)
    check('统计源不可读时显示占位符', placeholders > 0, '占位符数=' + placeholders)
  }

  // ── 5. 不得出现 per-key 的模型扇出 ──────────────────────────────────────
  const hits = performance.getEntriesByType('resource')
    .filter((e) => e.name.indexOf('/api/') >= 0)
    .map((e) => e.name.replace(location.origin, ''))
  const perKey = hits.filter((u) => u.indexOf('/api/usage/') >= 0 && u.indexOf('/models') >= 0)
  out.facts.apiHits = hits.length
  out.facts.perKeyCalls = perKey.length
  check('无 per-key /api/usage/{id}/models 扇出', perKey.length === 0, '实际 ' + perKey.length + ' 次')

  // ── 6. 状态条 ───────────────────────────────────────────────────────────
  const statusEl = q('[data-testid="overview-status"]')
  const tone = statusEl ? statusEl.getAttribute('data-tone') : ''
  out.facts.tone = tone
  out.facts.headline = text(q('[data-testid="overview-status"] .status-headline'))
  out.facts.detail = text(q('[data-testid="overview-status"] .status-detail'))
  out.facts.issues = qa('[data-testid="overview-status-issues"] li').map((li) => text(li))
  check('状态条结论合法（ok/warn/blocked）', ['ok', 'warn', 'blocked'].indexOf(tone) >= 0, 'tone=' + tone)
  check('状态条有一句话结论', out.facts.headline.length > 0, out.facts.headline)

  // ── 7. 三步接入 ─────────────────────────────────────────────────────────
  const onboardingEl = q('[data-testid="overview-onboarding"]')
  const steps = qa('[data-testid="overview-onboarding"] .onboarding-step')
  out.facts.onboardingPresent = !!onboardingEl
  out.facts.onboardingSteps = steps.length
  check('三步接入：不存在或恰好三步', !onboardingEl || steps.length === 3, '存在=' + !!onboardingEl + ' 步数=' + steps.length)

  // ── 8. 上游健康分级 ─────────────────────────────────────────────────────
  const rows = qa('[data-testid="overview-provider-health"] .provider-health-row')
  const tones = rows.map((r) => text(r.querySelector('.badge')))
  out.facts.providerRows = rows.length
  out.facts.providerTones = tones
  // ★ 注意口径：这里统计的是**可见行**（明细被截断到前 12 行，按严重度排序 ⇒
  //   「未探活」排在最后、通常被切掉）。**全量口径**在面板的汇总行上
  //   （可用 / 告警 / 不可达 / 未探活，四个数都算全量供应商）。
  out.facts.providerUnknown = tones.filter((t) => t === '未探活').length
  check('上游健康面板有行', rows.length > 0, '行数=' + rows.length)
  // 分级口径：可用 / 有告警 / 不可达 / 未探活（未探活必须单列一档，不并进「可用」）
  const legalTones = ['可用', '有告警', '不可达', '未探活']
  check('上游健康分级只出现四档', tones.every((t) => legalTones.indexOf(t) >= 0), JSON.stringify([...new Set(tones)]))

  // ── 9. 三个主体面板必须在 DOM 里（**与视口无关**）───────────────────────
  // ★ 为什么要有这一段：Maestro web 驱动的 view hierarchy **只覆盖视口**
  //   （默认 1024×625），而本页滚动容器是 .main-body（非 window）
  //   ⇒ Maestro 滚不动 ⇒ 「面板在不在」这件事在 flow 里只能靠「放大视口」间接证明。
  //   这里是**直接证据**：DOM 查询不看视口，面板在就是在。
  const panelSel = {
    '接入清单': '[data-testid="overview-access-list"]',
    '上游健康': '[data-testid="overview-provider-health"]',
    '最常用的模型': '[data-testid="overview-top-models"]',
  }
  const panels = {}
  for (const name of Object.keys(panelSel)) {
    panels[name] = !!q(panelSel[name])
    check('面板在 DOM 里：' + name, panels[name], panelSel[name])
  }
  out.facts.panels = panels

  // 最常用的模型：要么有行，要么是明确的空态（不能是「面板不见了」）
  const modelRows = qa('[data-testid="overview-top-models"] .model-row')
  const modelEmpty = q('[data-testid="overview-top-models"] .empty')
  out.facts.modelRows = modelRows.length
  check(
    '热门模型面板有行或明确空态',
    modelRows.length > 0 || !!modelEmpty,
    '行数=' + modelRows.length + ' 空态=' + !!modelEmpty,
  )

  // ── 10. 上游健康明细的截断必须**披露** ──────────────────────────────────
  // ★ 线上实测（2026-11-17）：徽标写「19 个上游可用」，而列表里只有 3 行标着
  //   「可用」—— 因为明细按严重度排序、只留前 12 行，被切掉的几乎全是健康的，
  //   而截断**没有任何披露** ⇒ 用户只会判定「这页数据坏了」。
  const ph = q('[data-testid="overview-provider-health"]')
  const phTotal = Number(ph ? ph.getAttribute('data-total') : NaN)
  const phShown = Number(ph ? ph.getAttribute('data-shown') : NaN)
  const phHidden = Number(ph ? ph.getAttribute('data-hidden') : NaN)
  const truncEl = q('[data-testid="overview-provider-truncated"]')
  out.facts.providerTotal = phTotal
  out.facts.providerShown = phShown
  out.facts.providerHidden = phHidden
  out.facts.providerTruncatedNote = text(truncEl)
  check(
    '上游健康：明细行数 + 隐藏数 = 在册总数',
    Number.isFinite(phTotal) && phShown + phHidden === phTotal,
    '总数=' + phTotal + ' 显示=' + phShown + ' 隐藏=' + phHidden,
  )
  check(
    '上游健康：明细被截断时必须有披露文案',
    phHidden === 0 || !!truncEl,
    '隐藏=' + phHidden + ' 披露元素=' + !!truncEl,
  )

  // ── 11. 接入清单的两个分页列表 ──────────────────────────────────────────
  // ★ 2026-11-17：接入清单从「整表铺开 118 行」改成「最近在用」+「用得最多」
  //   两个分页列表（每页 10 条）。这里验三件事：两个列表都在、行数受页长约束、
  //   「用得最多」的请求数**单调不增**（排序没被页面上别的逻辑打乱）。
  const recentRows = qa('[data-testid="overview-client-recent"] tbody tr')
  const busiestRows = qa('[data-testid="overview-client-busiest"] tbody tr')
  out.facts.recentRows = recentRows.length
  out.facts.busiestRows = busiestRows.length
  check(
    '最近在用列表存在且不超过每页条数',
    recentRows.length > 0 && recentRows.length <= 10,
    '行数=' + recentRows.length,
  )
  check(
    '用得最多列表存在且不超过每页条数',
    busiestRows.length > 0 && busiestRows.length <= 10,
    '行数=' + busiestRows.length,
  )
  const bars = qa('[data-testid="overview-access-list"] .pagination-bar')
  out.facts.paginationBars = bars.length
  check('两个列表都带分页栏', bars.length >= 2, '分页栏数=' + bars.length)

  // 「用得最多」：第 2 列是累计请求数（en-US 千分位）⇒ 单调不增
  const reqs = busiestRows
    .map((r) => Number(String((r.querySelectorAll('td')[1] || {}).textContent || '').replace(/,/g, '').trim()))
    .filter((n) => Number.isFinite(n))
  check(
    '用得最多按累计请求数单调不增',
    reqs.length > 0 && reqs.every((v, i) => i === 0 || reqs[i - 1] >= v),
    JSON.stringify(reqs.slice(0, 10)),
  )

  // ── 12. 近 7 日消耗卡（2026-11-18 新增）────────────────────────────────
  // ★ 数值对拍：UI 的每日柱值必须与**同一时刻** board?days=7 的 trends
  //   （15min 桶 → UTC 日聚合 → 补零 → 截最近 7 天）逐日相等。
  //   聚合/补零/截窗逻辑与 web/src/composables/simpleOverviewModel.ts 的
  //   aggregateTrendByDay / fillMissingDays / lastDailyPoints 同构（纯 JS 复刻）。
  const trendEl = q('[data-testid="overview-trend"]')
  check('趋势卡在 DOM 里：近 7 日消耗', !!trendEl, '[data-testid="overview-trend"]')
  const trendUnavailable = q('[data-testid="overview-trend-unavailable"]')
  const trendEmpty = q('[data-testid="overview-trend-empty"]')
  const trendCols = qa('[data-testid="overview-trend-chart"] .trend-col')
  out.facts.trendCols = trendCols.length
  out.facts.trendDegraded = !!trendUnavailable
  out.facts.trendEmptyShown = !!trendEmpty

  let b7 = null
  let b7Error = ''
  try {
    const x = new XMLHttpRequest()
    x.open('GET', '/api/admin/dashboard/v2/board?days=7', false)
    x.send()
    if (x.status >= 200 && x.status < 300) b7 = JSON.parse(x.responseText)
    else b7Error = 'HTTP ' + x.status
  } catch (e) {
    b7Error = (e && e.name) || 'xhr-failed'
  }
  out.facts.board7Error = b7Error
  check('七日统计使用 request_metrics', b7?.source === 'request_metrics', b7?.source || b7Error || '未报告来源')
  const b7Degraded =
    !b7 || b7.degraded_trends === true || (b7.summary && b7.summary.degraded_summary === true)

  if (b7Degraded) {
    check('趋势源降级时显示占位符（不画零线）', !!trendUnavailable, '占位符=' + !!trendUnavailable)
    check('趋势源降级时不画柱条', trendCols.length === 0, '柱数=' + trendCols.length)
  } else {
    check('趋势源健康时不显示占位符', !trendUnavailable, '占位符=' + !!trendUnavailable)
    // 复刻聚合：UTC 日分组 → 首末日补零 → 最近 7 天
    const byDay = {}
    for (const p of (b7 && b7.trends) || []) {
      const d = String((p && p.bucket) || '').slice(0, 10)
      if (!/^\\d{4}-\\d{2}-\\d{2}$/.test(d)) continue
      const cur = byDay[d] || (byDay[d] = { r: 0, t: 0, c: 0 })
      if (Number.isFinite(p.requests)) cur.r += p.requests
      if (Number.isFinite(p.tokens)) cur.t += p.tokens
      if (Number.isFinite(p.cost_usd)) cur.c += p.cost_usd
    }
    const dates = Object.keys(byDay).sort()
    const filled = []
    if (dates.length) {
      let cur = dates[0]
      let i = 0
      const last = dates[dates.length - 1]
      let guard = 0
      while (cur <= last && guard++ < 400) {
        if (dates[i] === cur) {
          filled.push({ date: cur, r: byDay[cur].r, t: byDay[cur].t, c: byDay[cur].c })
          i++
        } else {
          filled.push({ date: cur, r: 0, t: 0, c: 0 })
        }
        const ms = Date.parse(cur + 'T00:00:00Z') + 86400000
        cur = new Date(ms).toISOString().slice(0, 10)
      }
    }
    const last7 = filled.slice(-7)
    out.facts.trendExpectedDays = last7.map((d) => d.date)
    check('趋势柱数 = 预期日数（≤7）', trendCols.length === last7.length, 'UI=' + trendCols.length + ' 预期=' + last7.length)
    // 默认指标 = 请求数 ⇒ 每根柱的值 = formatCount(当日 requests)（千分位分组）
    const group = (n) => String(Math.trunc(Math.abs(n))).replace(/\\B(?=(\\d{3})+(?!\\d))/g, ',')
    const uiDays = trendCols.map((col) => ({
      value: text(col.querySelector('.trend-col__value')),
      label: text(col.querySelector('.trend-col__label')),
    }))
    const dayLabel = (date) => Number(date.slice(5, 7)) + '/' + Number(date.slice(8, 10))
    let mismatch = ''
    let drift = 0
    const todayUtc = new Date().toISOString().slice(0, 10)
    for (let i = 0; i < Math.min(uiDays.length, last7.length); i++) {
      const want = group(last7[i].r)
      const wantLabel = dayLabel(last7[i].date)
      if (uiDays[i].value !== want || uiDays[i].label !== wantLabel) {
        // ★ 最后一根柱若是「今天」：页面渲染与探针取数之间**活流量在涨**
        //   （211 实测 3~10 笔/几秒）⇒ 允许 ≤50 笔的正向漂移并**披露漂移量**；
        //   结构性派生错误（取错字段/换错量纲）仍会以数量级差距在这里爆红。
        //   历史日（不可变）保持严格相等。
        const isToday = last7[i].date === todayUtc
        const uiN = Number(uiDays[i].value.replace(/,/g, ''))
        const apiN = last7[i].r
        const d = apiN - uiN
        if (isToday && uiDays[i].label === wantLabel && Number.isFinite(uiN) && d >= 0 && d <= 50) {
          drift += d
          continue
        }
        mismatch += ' [' + i + '] UI=' + uiDays[i].value + '@' + uiDays[i].label + ' 预期=' + want + '@' + wantLabel
      }
    }
    check(
      '每日柱值与 board7 trends 逐日相等（请求数口径；今日柱容活流量漂移 ≤50 并披露）',
      mismatch === '',
      (mismatch || trendCols.length + ' 根柱全对') + (drift > 0 ? '（今日柱漂移 +' + drift + ' 笔）' : ''),
    )
    out.facts.trendDrift = drift
    // 合计行：7 天合计的请求数来自 board7.summary
    const totalEl = text(q('[data-testid="overview-trend-total"]'))
    out.facts.trendTotal = totalEl
    const s7 = (b7 && b7.summary) || null
    if (s7 && s7.total_requests != null) {
      // 同活流量漂移口径：合计数在页面渲染后只会涨（≤50 笔容差并披露）
      let totalOk = totalEl.indexOf(group(s7.total_requests)) >= 0
      let driftNote = ''
      if (!totalOk && Number.isFinite(s7.total_requests)) {
        for (let back = 1; back <= 50 && !totalOk; back++) {
          if (totalEl.indexOf(group(s7.total_requests - back)) >= 0) {
            totalOk = true
            driftNote = '（漂移 +' + back + ' 笔）'
          }
        }
      }
      check(
        '趋势合计行带 7 天请求总数（容活流量漂移 ≤50 并披露）',
        totalOk,
        'UI=' + totalEl + ' API=' + group(s7.total_requests) + driftNote,
      )
    }
    // 指标切换按钮三枚（复用专家面文案键）
    const metricBtns = qa('[data-testid="overview-trend"] .el-radio-button')
    out.facts.trendMetricBtns = metricBtns.length
    check('趋势指标切换恰好三枚', metricBtns.length === 3, '实际 ' + metricBtns.length)
  }

  // ── 13. 运维状态 chips（复用专家面 BoardOpsBar）──────────────────────────
  const opsEl = q('[data-testid="overview-ops"]')
  const opsChips = qa('[data-testid="overview-ops"] .ops-chip')
  out.facts.opsChips = opsChips.length
  if (opsEl) {
    check('运维 chips 至少两枚（后台任务 + 系统自检）', opsChips.length >= 2, '实际 ' + opsChips.length)
  }

  // ── 14. issue 行内「去处理」动作 ────────────────────────────────────────
  const issueLis = qa('[data-testid="overview-status-issues"] li')
  const issueBtns = qa('[data-testid^="overview-issue-action-"]')
  out.facts.issueLis = issueLis.length
  out.facts.issueButtons = issueBtns.length
  check('issue 按钮数不超过 issue 行数', issueBtns.length <= issueLis.length, '按钮=' + issueBtns.length + ' 行=' + issueLis.length)
  check('issue 按钮的 testid 格式合法', issueBtns.every((b) => /^overview-issue-action-[a-z_]+$/.test(b.getAttribute('data-testid'))), JSON.stringify(issueBtns.map((b) => b.getAttribute('data-testid'))))
  // 每条 issue 行都有非空文案
  check('每条 issue 行都有文案', issueLis.every((li) => text(li).length > 0), '行数=' + issueLis.length)

  // ── 15. 最常用模型的花费列（2026-11-18 新增）────────────────────────────
  const modelCosts = qa('[data-testid="overview-top-models"] .model-row .model-cost')
  out.facts.modelCostCells = modelCosts.length
  if (modelRows.length > 0) {
    check(
      '热门模型每行都带今日花费列',
      modelCosts.length === modelRows.length && modelCosts.every((c) => text(c).length > 0),
      '行=' + modelRows.length + ' 花费格=' + modelCosts.length,
    )
  }

  return out
})()`

async function main() {
  const args = parseArgs(process.argv.slice(2))
  if (!args.url || !args.tokenFile) {
    console.error('usage: assert-overview.mjs --url <base> --token-file <path> [--cdp ...] [--mode ...]')
    process.exit(2)
  }
  const token = readFileSync(args.tokenFile, 'utf8').trim()
  if (!token) {
    console.error('token file is empty')
    process.exit(2)
  }

  const { cdp, ws } = await connect(args.cdp)
  const { targetId, sessionId } = await openPage(cdp, 'about:blank')

  // 预置会话 cookie（HttpOnly，域/路径/secure 与后端 setSessionCookieRemember 一致）
  const setRes = await cdp.send(
    'Network.setCookie',
    {
      name: 'llmgw_session',
      value: token,
      url: `${args.url}/`,
      path: '/',
      secure: true,
      httpOnly: true,
      sameSite: 'Lax',
    },
    sessionId,
  )
  if (!setRes.success) throw new Error('Network.setCookie failed')

  // 打开总览页：先写本机选择（简易模式），再导航（工作模式在 SPA 启动时读一次）
  await cdp.send('Page.navigate', { url: `${args.url}/dashboard` }, sessionId)
  await evaluate(
    cdp,
    sessionId,
    `(() => {
      localStorage.setItem('llmgw_work_mode', ${JSON.stringify(args.mode)});
      localStorage.setItem('llmgw_user_info', ${JSON.stringify(JSON.stringify(USER_INFO))});
      return true;
    })()`,
  )
  await cdp.send('Page.navigate', { url: `${args.url}/dashboard` }, sessionId)

  // 等总览页渲染出来（简易模式下 /dashboard ⇒ 总览页）
  await waitFor(
    cdp,
    sessionId,
    `!!document.querySelector('[data-testid="overview-stats"]')`,
    40000,
    500,
  )
  // 再等数据源落定：★ 必须等 data-loading="0"（2026-11-19 实测撞出）——
  //   旧的判据「统计卡值非空」会被**加载中占位符「暂不可用」**满足（它也非空）
  //   ⇒ 探针抢跑 ⇒ 全部数值断言对着「还没加载完的页面」打 ⇒ 假红。
  //   视图根元素带 :data-loading（loading=true ⇒ '1'），落定后归 '0'。
  await waitFor(
    cdp,
    sessionId,
    `(() => {
      const root = document.querySelector('.simple-overview');
      if (!root || root.getAttribute('data-loading') !== '0') return false;
      const c = document.querySelector('[data-testid="overview-stats"] .app-stat-card .app-stat-card__value');
      return !!(c && c.textContent && c.textContent.trim());
    })()`,
    40000,
    500,
  )

  const report = await evaluate(cdp, sessionId, PAGE_PROBE)

  // ── 翻页行为（要真的点一下，不能只断言「分页栏在」）──────────────────────
  // ★ 用「第 2 个按钮」定位「下一页」：分页栏的按钮顺序恒为 [上一页, 下一页]，
  //   按中文文案定位会把断言绑死在 zh-CN 语言上（页面语言随会话变）。
  const before = await evaluate(cdp, sessionId, `(() => {
    const rows = document.querySelectorAll('[data-testid="overview-client-recent"] tbody tr');
    const info = document.querySelector('[data-testid="overview-client-recent"] .pagination-info');
    return {
      first: rows.length ? rows[0].textContent.trim() : '',
      count: rows.length,
      info: info ? info.textContent.replace(/\\s+/g, ' ').trim() : '',
    };
  })()`)
  const clicked = await evaluate(cdp, sessionId, `(() => {
    const btns = document.querySelectorAll('[data-testid="overview-client-recent"] .pagination-controls button');
    if (btns.length < 2) return false;
    btns[1].click();
    return true;
  })()`)
  await new Promise((r) => setTimeout(r, 600))
  const after = await evaluate(cdp, sessionId, `(() => {
    const rows = document.querySelectorAll('[data-testid="overview-client-recent"] tbody tr');
    const info = document.querySelector('[data-testid="overview-client-recent"] .pagination-info');
    return {
      first: rows.length ? rows[0].textContent.trim() : '',
      count: rows.length,
      info: info ? info.textContent.replace(/\\s+/g, ' ').trim() : '',
    };
  })()`)
  report.facts.pageBefore = before
  report.facts.pageAfter = after
  const paged = clicked && after.first !== before.first && after.info !== before.info
  report.checks.push({
    name: '分页真的能翻（点「下一页」后首行与页码都变）',
    pass: paged,
    detail: `before.first=${before.first.slice(0, 24)} after.first=${after.first.slice(0, 24)} before.info=${before.info} after.info=${after.info}`,
  })
  if (!paged) report.ok = false

  await cdp.send('Target.closeTarget', { targetId })

  const failed = (report.checks || []).filter((c) => !c.pass)
  console.log(JSON.stringify({ ok: report.ok, checks: report.checks, facts: report.facts }, null, 2))
  ws.close()
  if (!report.ok) {
    console.error(`\n断言失败 ${failed.length}/${report.checks.length} 条`)
    process.exit(1)
  }
  console.error(`\n全部通过 ${report.checks.length} 条`)
}

main().catch((err) => {
  console.error(`assert-overview failed: ${err.message}`)
  process.exit(1)
})
