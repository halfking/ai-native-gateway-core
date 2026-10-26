// UsageEnhancedView.test.ts —— 用**真端点的真实响应**挂载页面（2026-10-21 重写）
//
// ## 为什么换掉初版的夹具
//
// 初版夹具是 `{days, total_cost, total_requests}` / `{data:[{date,cost,requests}]}` /
// `{delta_cost, delta_cost_pct}` / `{cache_hit_rate}` / `{keys:[{key_id,key_name}]}` ——
// **全是从未存在过的形状**。视图照着这套装扮出来的字段读，于是真实响应一上来就
// `data.cost_trend.data` 是 undefined ⇒ `.length` 抛 TypeError ⇒ 整页白屏
// （2026-10-21 逐页浏览器验收实测：页面 body 仅 103 字符、控制台 TypeError）。
// 而初版测试**用同一套错形状做夹具** ⇒ 全绿。
//
// ⇒ 「用前端自己声明的错形状做 fixture」是假绿的经典形态。
//
// ## 这一版的规矩
//
// 1. 夹具 = **真端点的原始响应**（src/fixtures/usage/，采集命令见该文件）。
//    整条链路（api/usage → api/usageEnhanced → 视图）都跑真代码，只 mock 传输层
//    （_core.req）——所以「后端真返回这个形状」与「前端真能渲染它」被钉在同一条断言里。
// 2. 为了让「渲染出来的数字」有鉴别力，在**真形状**的前提下把若干数值换成
//    有特征的值（12.34 / 100 / 23.4 / 0.42 / 5.50 / 50 …）。
//    形状不变 ⇒ 字段读错就一定渲染成别的数字或「—」。
// 3. 额外钉住两个**量纲**陷阱（后端口径不同，格式化器不能共用）：
//    · period_compare.change_pct 已经是**百分数**（后端 `(Δ/prev)*100`）⇒ 23.4 → "23.4%"
//    · cache_economics.cache_hit_ratio 是 0~1 的**比例** ⇒ 0.42 → "42.0%"
//    初版把两者都用「乘 100」处理 ⇒ 命中率会显示成 4200.0%。
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import UsageEnhancedView from './UsageEnhancedView.vue'
import zhCN from '../../locales/zh-CN'
import { req } from '../../api/_core'
import { realUsageFixtures, realUsageResponse } from '../../fixtures/usage'
import { formatNumberLocale } from '../../utils/format'

vi.mock('../../api/_core', () => ({ req: vi.fn() }))

const mockedReq = vi.mocked(req)

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'en',
  messages: { 'zh-CN': zhCN },
})

/**
 * 真形状 + 有特征的值。
 *
 * 只改数值，**不改任何键名** —— 键名的真源是 fixtures/usage 下的原始响应。
 */
function payloads() {
  const summary = structuredClone(realUsageFixtures.summary)
  summary.total_cost_usd = 12.34
  summary.total_requests = 100

  const costTrend = structuredClone(realUsageFixtures.costTrend)
  costTrend.entries[0].total_cost_usd = 5.5
  costTrend.entries[0].request_count = 50

  const periodCompare = structuredClone(realUsageFixtures.periodCompare)
  periodCompare.change_pct = 23.4
  periodCompare.change_abs = 2.34

  const cacheEconomics = structuredClone(realUsageFixtures.cacheEconomics)
  cacheEconomics.cache_hit_ratio = 0.42

  const hotKeys = structuredClone(realUsageFixtures.hotKeys)
  hotKeys[0].request_count = 50
  hotKeys[0].total_tokens = 5000

  return { summary, costTrend, periodCompare, cacheEconomics, hotKeys }
}

function install(overrides: Partial<ReturnType<typeof payloads>> = {}) {
  const p = { ...payloads(), ...overrides }
  mockedReq.mockImplementation(async (_method, url) => {
    const path = new URL(url, 'http://localhost').pathname
    switch (path) {
      case '/api/usage/summary': return p.summary
      case '/api/admin/usage/cost-trend': return p.costTrend
      case '/api/admin/usage/period-compare': return p.periodCompare
      case '/api/admin/usage/cache-economics': return p.cacheEconomics
      case '/api/usage/hot-keys': return p.hotKeys
      default: return realUsageResponse(url)
    }
  })
}

async function renderPage() {
  const w = mount(UsageEnhancedView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

/** 渲染结果里出现 NaN / undefined ⇒ 某个字段映射没接上。 */
function expectNoNaN(html: string) {
  expect(html, '页面渲染出 NaN（字段映射没接上）').not.toContain('NaN')
  expect(html, '页面渲染出 undefined（读了后端不返回的键）').not.toContain('undefined')
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers()
  // 钉住「当月/上月」默认值，使默认请求参数与真响应里的 2026-10 / 2026-09 一致。
  vi.setSystemTime(new Date('2026-10-20T12:00:00Z'))
  install()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('UsageEnhancedView 真实数据渲染', () => {
  it('挂载不抛异常，且渲染出的是真数字而不是「—」', async () => {
    const w = await renderPage()
    const html = w.html()

    // 4 张卡片都在
    expect(w.findAll('.enhanced-card')).toHaveLength(4)

    // 总成本 = summary.total_cost_usd（初版读 summary.total_cost ⇒ undefined ⇒ 「—」）
    const cardValues = w.findAll('.enhanced-card-value').map((n) => n.text())
    expect(cardValues[0], '总成本卡片必须是 $12.34 而不是「—」').toBe('$12.34')
    expect(cardValues[0]).not.toBe('—')

    // 总请求数 = summary.total_requests
    expect(cardValues[1]).toBe(formatNumberLocale(100))
    expect(cardValues[1]).not.toBe('—')

    // 同期对比 Δ 成本 = period_compare.change_pct（**已是百分数** ⇒ 23.4%）
    expect(cardValues[2]).toBe('23.4%')
    // 反例：把 change_pct 当比例再乘 100 ⇒ 2340.0%
    expect(cardValues[2]).not.toBe('2340.0%')

    // 缓存命中率 = cache_economics.cache_hit_ratio × 100（比例 ⇒ 42.0%）
    expect(cardValues[3]).toBe('42.0%')
    expect(cardValues[3]).not.toBe('4200.0%')

    expectNoNaN(html)
    w.unmount()
  })

  it('成本趋势列表渲染 dimension_value / total_cost_usd / request_count', async () => {
    const w = await renderPage()
    const rows = w.findAll('.enhanced-trend-row')
    expect(rows.length).toBe(realUsageFixtures.costTrend.entries.length)
    // ⚠️ 必须**逐格**断言，不能用 `rows[0].text()).toContain(...)`：
    // 同一行里有多个数字，其中一个若是另一个的子串（'50' ⊂ '5000'），
    // 整行文本断言会被别的格满足 ⇒ 读错字段照样绿灯（变异 M7 实测）。
    const cells = rows[0].findAll('span')
    expect(cells[0].text()).toBe(realUsageFixtures.costTrend.entries[0].dimension_value)
    expect(cells[1].text()).toBe('$5.50')
    expect(cells[2].text()).toBe(`${formatNumberLocale(50)} req`)
    expectNoNaN(w.html())
    w.unmount()
  })

  it('Top API keys 表格渲染 api_key_id / key_prefix / application_code / 成本', async () => {
    const w = await renderPage()
    const trs = w.findAll('.enhanced-table tbody tr')
    expect(trs.length).toBe(realUsageFixtures.hotKeys.length)
    // 同上：逐格精确断言。key_prefix='local-test' 与 application_code=
    // 'retirement-local-test' 互为子串 ⇒ 整行 toContain 会被后者满足。
    const cells = trs[0].findAll('td')
    expect(cells[0].text()).toBe(String(realUsageFixtures.hotKeys[0].api_key_id))
    expect(cells[1].text()).toBe(realUsageFixtures.hotKeys[0].key_prefix)
    expect(cells[2].text()).toBe(realUsageFixtures.hotKeys[0].application_code)
    expect(cells[3].text()).toBe(formatNumberLocale(50))
    expect(cells[4].text()).toBe(formatNumberLocale(5000))
    expect(cells[5].text()).toBe('$0.00')
    expectNoNaN(w.html())
    w.unmount()
  })

  it('空数据时给出空态占位，而不是崩掉', async () => {
    const p = payloads()
    p.costTrend.entries = []
    p.hotKeys = []
    install({ costTrend: p.costTrend, hotKeys: p.hotKeys })

    const w = await renderPage()
    const empties = w.findAll('.enhanced-empty')
    expect(empties.length).toBeGreaterThanOrEqual(2)
    expect(w.find('.enhanced-table').exists()).toBe(false)
    expectNoNaN(w.html())
    w.unmount()
  })

  it('请求失败时显示错误且不渲染数据区', async () => {
    mockedReq.mockRejectedValue(new Error('boom'))
    const w = await renderPage()
    expect(w.find('.enhanced-error').exists()).toBe(true)
    expect(w.text()).toContain('boom')
    expect(w.find('.enhanced-cards').exists()).toBe(false)
    w.unmount()
  })
})

describe('UsageEnhancedView 降级横幅', () => {
  it('服务端说 degraded:true → 显示横幅并写明原因与不可信指标', async () => {
    const p = payloads()
    // 真响应里 degraded_reason 是 omitempty ⇒ 夹具类型里没有这个键 ⇒ 用 Record 补。
    // 补的是**后端确实会下发的字段**（见 admin/usage_enhanced.go 的 DegradedReason）。
    const periodCompare = p.periodCompare as Record<string, unknown>
    periodCompare.degraded = true
    periodCompare.degraded_reason = 'missing column: usage_ledger.gw_session_id'
    install({ periodCompare: p.periodCompare })

    const w = await renderPage()
    const banner = w.find('.alert-warning')
    expect(banner.exists()).toBe(true)
    // 鉴别力：只断言「有横幅」不够 —— 写死的横幅也会通过。
    // 必须断言**原因真的被透出到 DOM**。
    expect(banner.text()).toContain('missing column: usage_ledger.gw_session_id')
    expect(banner.text()).toContain('周期对比')
    w.unmount()
  })

  it('反向对照：全部健康 → 不得出现降级横幅', async () => {
    const w = await renderPage()
    expect(w.find('.alert-warning').exists()).toBe(false)
    w.unmount()
  })

  it('反向对照：老服务端没有 degraded 字段 → 不得出现横幅（缺字段≠降级）', async () => {
    const p = payloads()
    delete (p.periodCompare as Record<string, unknown>).degraded
    install({ periodCompare: p.periodCompare })

    const w = await renderPage()
    expect(w.find('.alert-warning').exists()).toBe(false)
    w.unmount()
  })
})
