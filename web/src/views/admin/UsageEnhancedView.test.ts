import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import UsageEnhancedView from './UsageEnhancedView.vue'
import { getUsageEnhancedData } from '../../api/usageEnhanced'

vi.mock('../../api/usageEnhanced', () => ({
  getUsageEnhancedData: vi.fn(),
}))

const mockedGet = vi.mocked(getUsageEnhancedData)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE = {
  summary: { days: 7, total_cost: 12.34, total_requests: 100, total_tokens: 5000 },
  cost_trend: {
    group_by: 'model',
    data: [
      { date: '2026-10-15', cost: 5.5, requests: 50 },
      { date: '2026-10-16', cost: 6.84, requests: 50 },
    ],
  },
  period_compare: {
    current: { cost: 12.34, requests: 100 },
    previous: { cost: 10, requests: 80 },
    delta_cost: 2.34,
    delta_cost_pct: 0.234,
    delta_requests: 20,
    delta_requests_pct: 0.25,
  },
  cache_economics: { cache_hit_rate: 0.42, cache_cost_saved: 1.5, total_cacheable_requests: 200 },
  hot_api_keys: [
    { key_id: 1, key_name: 'k-alpha', total_requests: 50, total_cost: 5.5 },
    { key_id: 2, key_name: 'k-beta', total_requests: 30, total_cost: 3.5 },
  ],
  refreshedAt: '2026-10-17T08:00:00.000Z',
}

describe('UsageEnhancedView', () => {
  it('renders 4 cards + 1 trend list + 1 keys table from sample data', async () => {
    mockedGet.mockResolvedValueOnce(SAMPLE)
    const w = mount(UsageEnhancedView)
    await flushPromises()
    // 4 cards = 总成本 / 总请求数 / 同期对比Δ成本 / 缓存命中率
    expect(w.findAll('.enhanced-card')).toHaveLength(4)
    expect(w.text()).toContain('$12.34')
    expect(w.text()).toContain('100')
    expect(w.text()).toContain('23.4%')
    expect(w.text()).toContain('42.0%')
    // trend list
    expect(w.findAll('.enhanced-trend-row')).toHaveLength(2)
    expect(w.text()).toContain('2026-10-15')
    expect(w.text()).toContain('2026-10-16')
    // keys table
    expect(w.findAll('.enhanced-table tbody tr')).toHaveLength(2)
    expect(w.text()).toContain('k-alpha')
    expect(w.text()).toContain('k-beta')
    // refresh button enabled after loaded (loading=false)
    expect(w.find('.btn').attributes('disabled')).toBeUndefined()
  })

  it('shows empty placeholder when backend returns no trend data and no keys', async () => {
    mockedGet.mockResolvedValueOnce({
      ...SAMPLE,
      cost_trend: { group_by: 'model', data: [] },
      hot_api_keys: [],
    })
    const w = mount(UsageEnhancedView)
    await flushPromises()
    const empties = w.findAll('.enhanced-empty')
    expect(empties.length).toBeGreaterThanOrEqual(2)
    expect(w.text()).toContain('无数据')
    expect(w.find('.enhanced-table').exists()).toBe(false)
  })

  it('shows error message when request rejects and prevents rendering data section', async () => {
    mockedGet.mockRejectedValueOnce(new Error('boom'))
    const w = mount(UsageEnhancedView)
    await flushPromises()
    expect(w.find('.enhanced-error').exists()).toBe(true)
    expect(w.text()).toContain('boom')
    expect(w.find('.enhanced-cards').exists()).toBe(false)
    expect(w.find('.enhanced-table').exists()).toBe(false)
  })
})
