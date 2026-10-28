// StatsPanel.test.ts — 头部区间统计面板判据（2026-10-28 头部统计轮）
//
// ★ 核心不变量：
//  1. 「统计不可用」与「0 请求」是两件事 —— degraded / 端点失败必须渲染成
//     不可用文案，绝不能渲染成一张全是 0 的表（沿袭退役栏的判据）；
//  2. 区间切换必须真的改调 ?window=（5m/1h 直查 request_logs），day 才带
//     ?by=provider 交叉展开 —— 调参错了口径就错了；
//  3. 总量行常驻：折叠收掉明细两组后，请求数/成功率/输入/输出/缓存/失败
//     仍必须可见（面板存在的意义就是总账常驻）；
//  4. 成功率的假零禁令：0 请求显示「—」，不显示 0%；
//  5. 口径行以响应自己的 window 字段为准（数据说话，不是选中态说话）。

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import StatsPanel from './StatsPanel.vue'
import { getClientsToday, getProvidersToday, getTopologyModelsToday } from '../../api/topology'
import zhCN from '../../locales/zh-CN/liveTopology'

vi.mock('../../api/topology', () => ({
  getClientsToday: vi.fn(),
  getProvidersToday: vi.fn(),
  getTopologyModelsToday: vi.fn(),
}))

const mockedClients = vi.mocked(getClientsToday)
const mockedProviders = vi.mocked(getProvidersToday)
const mockedModels = vi.mocked(getTopologyModelsToday)

// 用真实的 zh-CN 文案（组件走的是 useI18n() 组合式 API）—— 缺键当场红。
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

type PanelProps = {
  activeClient?: string
  activeProvider?: string
  activeModel?: string
  inFlight?: number | null
  queueDepth?: number | null
}

// 后端 totals 是全字段返回的；夹具也写全，避免「半个信封」假绿。
function totals(over: Partial<Record<string, number>> = {}) {
  return {
    requests: 0, success: 0, failure: 0, rate_limited: 0,
    prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0,
    cache_write_tokens: 0, total_tokens: 0, credits_charged: 0, cost_usd: 0,
    ...over,
  } as never
}

function clientItem(over: Record<string, unknown> = {}) {
  return {
    client_key: 'claude-code', requests: 50, success: 48, failure: 2, rate_limited: 0,
    prompt_tokens: 800, completion_tokens: 400, cache_read_tokens: 30, cache_write_tokens: 6,
    total_tokens: 1200, credits_charged: 1, cost_usd: 0.2, share: 1,
    ...over,
  } as never
}

function providerItem(over: Record<string, unknown> = {}) {
  return {
    provider_id: 12, catalog_code: 'deepseek', display_name: 'DeepSeek',
    enabled: true, known: true,
    requests: 100, success: 90, failure: 8, rate_limited: 2,
    prompt_tokens: 1000, completion_tokens: 500, cache_read_tokens: 40, cache_write_tokens: 10,
    total_tokens: 1500, credits_charged: 1, cost_usd: 0.1, share: 1,
    ...over,
  } as never
}

function modelItem(over: Record<string, unknown> = {}) {
  return {
    canonical_id: 1, canonical_name: 'glm-4.5', raw_model_name: '',
    requests: 100, success: 98, failure: 2, rate_limited: 0,
    prompt_tokens: 10, completion_tokens: 5, cache_read_tokens: 40, cache_write_tokens: 10,
    total_tokens: 15, credits_charged: 1, cost_usd: 0.1, share: 1,
    ...over,
  } as never
}

function mountPanel(props: PanelProps = {}) {
  return mount(StatsPanel, { props: props as never, global: { plugins: [i18n] } })
}

describe('StatsPanel 区间切换与口径', () => {
  beforeEach(() => {
    mockedClients.mockReset()
    mockedProviders.mockReset()
    mockedModels.mockReset()
    mockedClients.mockResolvedValue({ day_utc: '2026-10-28', items: [] })
    mockedProviders.mockResolvedValue({ day_utc: '2026-10-28', totals: totals({ requests: 10, success: 9 }), items: [] })
    mockedModels.mockResolvedValue({ day_utc: '2026-10-28', items: [] })
  })

  it('默认当天区间：day 端点带 ?by=provider，不带 window；clients 端点同参', async () => {
    mountPanel()
    await flushPromises()
    expect(mockedClients).toHaveBeenCalledWith(expect.objectContaining({ window: undefined }))
    expect(mockedProviders).toHaveBeenCalledWith(expect.objectContaining({ window: undefined }))
    expect(mockedModels).toHaveBeenCalledWith(expect.objectContaining({ byProvider: true, window: undefined }))
  })

  it('切到 5 分钟：两个端点都改调 ?window=5m 且不带 by=provider', async () => {
    const w = mountPanel()
    await flushPromises()
    ;(w.find('[data-testid="stats-interval"]').element as HTMLSelectElement).value = '5m'
    ;(w.find('[data-testid="stats-interval"]').element as HTMLSelectElement).dispatchEvent(new Event('change'))
    await flushPromises()
    expect(mockedClients).toHaveBeenLastCalledWith(expect.objectContaining({ window: '5m' }))
    expect(mockedProviders).toHaveBeenLastCalledWith(expect.objectContaining({ window: '5m' }))
    expect(mockedModels).toHaveBeenLastCalledWith(expect.objectContaining({ window: '5m', byProvider: false }))
  })

  it('口径行以响应自己的 window 字段为准（数据说话）', async () => {
    const w = mountPanel()
    await flushPromises()
    expect(w.find('[data-testid="stats-note"]').text()).toContain('UTC 日桶')

    mockedProviders.mockResolvedValue({ window: '5m', totals: totals(), items: [] })
    ;(w.find('[data-testid="stats-interval"]').element as HTMLSelectElement).value = '5m'
    ;(w.find('[data-testid="stats-interval"]').element as HTMLSelectElement).dispatchEvent(new Event('change'))
    await flushPromises()
    expect(w.find('[data-testid="stats-note"]').text()).toContain('实时口径')
    expect(w.find('[data-testid="stats-note"]').text()).not.toContain('UTC 日桶')
  })

  it('短窗口空态点名区间，不沿用「当天」文案', async () => {
    mockedProviders.mockResolvedValue({ window: '5m', totals: totals(), items: [] })
    mockedModels.mockResolvedValue({ window: '5m', items: [] })
    const w = mountPanel()
    await flushPromises()
    // 三组默认全展开（2026-10-29 单一统计轮）
    expect(w.find('[data-testid="stats-group-models"] .stats__empty').text()).toContain('5分钟')
  })
})

describe('StatsPanel 总量行（常驻）', () => {
  beforeEach(() => {
    mockedClients.mockReset()
    mockedProviders.mockReset()
    mockedModels.mockReset()
    mockedClients.mockResolvedValue({ items: [] })
    mockedModels.mockResolvedValue({ items: [] })
  })

  it('渲染请求数/成功率/输入/输出/缓存/失败；缓存 = read+write；失败 = failure+rate_limited', async () => {
    mockedProviders.mockResolvedValue({
      day_utc: '2026-10-28',
      totals: totals({
        requests: 1000, success: 900, failure: 80, rate_limited: 20,
        prompt_tokens: 1_500_000, completion_tokens: 640_000,
        cache_read_tokens: 90_000, cache_write_tokens: 4_000, total_tokens: 2_234_000,
      }),
      items: [],
    })
    const w = mountPanel()
    await flushPromises()

    const text = w.find('[data-testid="stats-totals"]').text()
    expect(text).toContain('请求数')
    expect(text).toContain('1.0K')
    expect(text).toContain('成功率')
    expect(text).toContain('90.0%')
    expect(text).toContain('1.50M tok')
    expect(text).toContain('640.0K tok')
    expect(text).toContain('94.0K tok') // 缓存 = 90000 + 4000
    expect(text).toContain('100') // 失败 = 80 + 20
  })

  it('假零禁令：0 请求时成功率显示「—」，不显示 0%', async () => {
    mockedProviders.mockResolvedValue({ day_utc: '2026-10-28', totals: totals(), items: [] })
    const w = mountPanel()
    await flushPromises()
    expect(w.find('[data-testid="stats-totals"]').text()).toContain('—')
    expect(w.find('[data-testid="stats-totals"]').text()).not.toContain('0.0%')
  })

  it('折叠后总量行仍可见，明细两组消失；展开恢复', async () => {
    mockedProviders.mockResolvedValue({
      day_utc: '2026-10-28',
      totals: totals({ requests: 10, success: 9 }),
      items: [providerItem()],
    })
    const w = mountPanel()
    await flushPromises()
    expect(w.find('[data-testid="stats-group-providers"]').exists()).toBe(true)

    await w.find('[data-testid="stats-toggle"]').trigger('click')
    expect(w.find('[data-testid="stats-totals"]').exists()).toBe(true)
    expect(w.find('[data-testid="stats-group-providers"]').exists()).toBe(false)

    await w.find('[data-testid="stats-toggle"]').trigger('click')
    expect(w.find('[data-testid="stats-group-providers"]').exists()).toBe(true)
  })

  it('供应商组 degraded：总量行渲染「不可用」，不是 0 表；模型组独立', async () => {
    mockedProviders.mockResolvedValue({ degraded: true, error_code: 'STATS_NOT_MIGRATED', items: [] })
    mockedModels.mockResolvedValue({ day_utc: '2026-10-28', items: [modelItem()] })
    const w = mountPanel()
    await flushPromises()

    expect(w.find('[data-testid="stats-totals-down"]').exists()).toBe(true)
    expect(w.find('[data-testid="stats-totals"]').exists()).toBe(false)
    // 模型组不受供应商组拖垮
    expect(w.find('[data-testid="stats-group-models"] .stats__group-down').exists()).toBe(false)
  })

  it('端点失败（非 degraded）同样渲染不可用', async () => {
    mockedProviders.mockRejectedValue(new Error('503'))
    const w = mountPanel()
    await flushPromises()
    expect(w.find('[data-testid="stats-totals-down"]').exists()).toBe(true)
  })
})

describe('StatsPanel 客户端组（2026-10-29 单一统计轮并入）', () => {
  beforeEach(() => {
    mockedClients.mockReset()
    mockedProviders.mockReset()
    mockedModels.mockReset()
    mockedProviders.mockResolvedValue({ day_utc: '2026-10-28', totals: totals({ requests: 10, success: 9 }), items: [] })
    mockedModels.mockResolvedValue({ day_utc: '2026-10-28', items: [] })
  })

  it('三组并陈：clients/providers/models 各自渲染（组头合计来自各自数据）', async () => {
    mockedClients.mockResolvedValue({
      day_utc: '2026-10-28',
      items: [clientItem(), clientItem({ client_key: 'unknown', requests: 2, success: 0, share: 0 })],
    })
    const w = mountPanel()
    await flushPromises()
    expect(w.find('[data-testid="stats-group-clients"]').exists()).toBe(true)
    expect(w.find('[data-testid="stats-group-providers"]').exists()).toBe(true)
    expect(w.find('[data-testid="stats-group-models"]').exists()).toBe(true)
    const rows = w.findAll('[data-testid="stats-group-clients"] .stats__row')
    expect(rows.length).toBe(2)
    expect(rows[0].text()).toContain('Claude Code')
    // unknown 桶不丢、走 i18n 显示名
    expect(rows[1].text()).toContain('未知客户端')
    expect(rows[1].classes()).toContain('is-unknown')
  })

  it('点客户端行发 select-client；activeClient 命中的行带 is-active', async () => {
    mockedClients.mockResolvedValue({ day_utc: '2026-10-28', items: [clientItem()] })
    const w = mountPanel({ activeClient: 'claude-code' })
    await flushPromises()
    expect(w.find('[data-testid="stats-group-clients"] .stats__row.is-active').exists()).toBe(true)
    await w.find('[data-testid="stats-group-clients"] .stats__row-btn').trigger('click')
    expect(w.emitted('select-client')?.[0]).toEqual(['claude-code'])
  })

  it('客户端组降级独立（不拖垮供应商/模型组）', async () => {
    mockedClients.mockRejectedValue(new Error('503'))
    const w = mountPanel()
    await flushPromises()
    expect(w.find('[data-testid="stats-group-clients"] .stats__group-down').exists()).toBe(true)
    expect(w.find('[data-testid="stats-totals"]').exists()).toBe(true)
    expect(w.find('[data-testid="stats-group-providers"] .stats__group-down').exists()).toBe(false)
  })
})

describe('StatsPanel 实时流程状态（在途/排队并入总量行）', () => {
  beforeEach(() => {
    mockedClients.mockReset()
    mockedProviders.mockReset()
    mockedModels.mockReset()
    mockedClients.mockResolvedValue({ items: [] })
    mockedProviders.mockResolvedValue({ day_utc: '2026-10-28', totals: totals({ requests: 10, success: 9 }), items: [] })
    mockedModels.mockResolvedValue({ items: [] })
  })

  it('给到 inFlight/queueDepth 时在失败指标后并排显示（与 DB 指标隔开）', async () => {
    const w = mountPanel({ inFlight: 3, queueDepth: 2 })
    await flushPromises()
    expect(w.find('[data-testid="stats-inflight"]').text()).toContain('在途')
    expect(w.find('[data-testid="stats-inflight"]').text()).toContain('3')
    expect(w.find('[data-testid="stats-queue"]').text()).toContain('排队')
    expect(w.find('.stats__live-divider').exists()).toBe(true)
  })

  it('未测量（null/undefined）时不显示，也不冒充 0', async () => {
    const w = mountPanel({ inFlight: null, queueDepth: undefined })
    await flushPromises()
    expect(w.find('[data-testid="stats-inflight"]').exists()).toBe(false)
    expect(w.find('[data-testid="stats-queue"]').exists()).toBe(false)
  })
})

describe('StatsPanel 明细与过滤联动', () => {
  beforeEach(() => {
    mockedProviders.mockReset()
    mockedModels.mockReset()
    mockedClients.mockResolvedValue({ day_utc: '2026-10-28', items: [] })
    mockedProviders.mockResolvedValue({
      day_utc: '2026-10-28',
      totals: totals({ requests: 10, success: 9 }),
      items: [providerItem(), providerItem({ provider_id: 0, catalog_code: '', display_name: '', known: false, requests: 7, success: 0, share: 0 })],
    })
    mockedModels.mockResolvedValue({ day_utc: '2026-10-28', items: [modelItem()] })
  })

  it('点供应商行发 select-provider（与图收敛到同一过滤子集）', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.find('[data-testid="stats-group-providers"] .stats__row-btn').trigger('click')
    expect(w.emitted('select-provider')?.[0]).toEqual(['deepseek'])
  })

  it('activeProvider 命中的行带 is-active', async () => {
    const w = mountPanel({ activeProvider: 'deepseek' })
    await flushPromises()
    expect(w.find('[data-testid="stats-group-providers"] .stats__row.is-active').exists()).toBe(true)
  })

  it('未知供应商桶不丢（provider_id=0 也渲染成一行，标 is-unknown）', async () => {
    const w = mountPanel()
    await flushPromises()
    const rows = w.findAll('[data-testid="stats-group-providers"] .stats__row')
    expect(rows.length).toBe(2)
    expect(rows[1].classes()).toContain('is-unknown')
  })

  it('day 区间模型行可展开供应商分布；点展开钮不触发 select-model', async () => {
    mockedModels.mockResolvedValue({
      day_utc: '2026-10-28',
      items: [modelItem({ providers: [{ provider_id: 12, catalog_code: 'deepseek', display_name: 'DeepSeek', known: true, requests: 100, success: 98, failure: 2, rate_limited: 0, prompt_tokens: 10, completion_tokens: 5, cache_read_tokens: 40, cache_write_tokens: 10, total_tokens: 15, credits_charged: 1, cost_usd: 0.1, share: 1 }] })],
    })
    const w = mountPanel()
    await flushPromises()

    const before = w.emitted('select-model')?.length ?? 0
    await w.find('[data-testid="stats-model-expand"]').trigger('click')
    expect(w.emitted('select-model')?.length ?? 0).toBe(before)
    expect(w.find('[data-testid="stats-model-providers"]').exists()).toBe(true)
  })

  it('窗口口径的模型行没有 providers 子行时，不渲染展开钮', async () => {
    mockedModels.mockResolvedValue({ window: '5m', items: [modelItem()] })
    const w = mountPanel()
    await flushPromises()
    expect(w.find('[data-testid="stats-model-expand"]').exists()).toBe(false)
  })
})
