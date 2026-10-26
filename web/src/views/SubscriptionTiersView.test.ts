import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import SubscriptionTiersView from './SubscriptionTiersView.vue'
import { getSubscriptionTiers } from '../api/ops'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/ops', () => ({
  getSubscriptionTiers: vi.fn(),
}))

const mockedGet = vi.mocked(getSubscriptionTiers)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE = [
  {
    code: 'free',
    name: '免费版',
    description: '基础接入能力',
    price_cents: 0,
    sort_order: 1,
    module_keys: ['llm_basic'],
  },
  {
    code: 'pro',
    name: '专业版',
    description: '高级路由 + SLA',
    price_cents: 29900,
    sort_order: 2,
    module_keys: ['llm_basic', 'routing_v2', 'sla_monitor'],
  },
  {
    code: 'enterprise',
    name: '企业版',
    description: '',
    price_cents: 99900,
    sort_order: 3,
    module_keys: [],
  },
]

describe('SubscriptionTiersView', () => {
  it('renders 3 tier cards sorted by sort_order with formatted price', async () => {
    // out-of-order sample, view should sort
    mockedGet.mockResolvedValueOnce([SAMPLE[2], SAMPLE[0], SAMPLE[1]])
    const w = mount(SubscriptionTiersView, { global: { plugins: [testI18n] } })
    await flushPromises()
    const cards = w.findAll('.st-card')
    expect(cards).toHaveLength(3)
    // 排序后第一张应是 sort_order=1（free）
    expect(cards[0].text()).toContain('免费版')
    expect(cards[0].text()).toContain('免费')   // price_cents=0 → "免费"
    // 第二张 pro 价格 ¥299.00
    expect(cards[1].text()).toContain('专业版')
    expect(cards[1].text()).toContain('¥299.00')
    // 第三张 enterprise 无描述
    expect(cards[2].text()).toContain('企业版')
    expect(cards[2].text()).toContain('¥999.00')
    // module_keys 数量
    expect(cards[0].text()).toContain('包含模块（1）')
    expect(cards[1].text()).toContain('包含模块（3）')
    expect(cards[2].text()).toContain('包含模块（0）')
  })

  it('shows empty state when backend returns no tiers', async () => {
    mockedGet.mockResolvedValueOnce([])
    const w = mount(SubscriptionTiersView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.st-empty').exists()).toBe(true)
    expect(w.findAll('.st-card')).toHaveLength(0)
  })

  it('shows error message on fetch failure and refresh works after recovery', async () => {
    mockedGet.mockRejectedValueOnce(new Error('nginx 502'))
    const w = mount(SubscriptionTiersView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.st-error').exists()).toBe(true)
    expect(w.text()).toContain('nginx 502')
    expect(w.findAll('.st-card')).toHaveLength(0)
    // 重试成功
    mockedGet.mockResolvedValueOnce(SAMPLE)
    await w.find('.btn').trigger('click')
    await flushPromises()
    expect(w.find('.st-error').exists()).toBe(false)
    expect(w.findAll('.st-card')).toHaveLength(3)
  })
})
