import { mount, flushPromises } from '@vue/test-utils'
import { computed } from 'vue'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import PriceCompareView from './PriceCompareView.vue'
import { testI18n } from '../../test-utils/i18n'
import { getAdminMarketCompare, type MarketCompareRow } from '../../api/market'

vi.mock('../../composables/useDataViewMode', () => ({
  useDataViewMode: () => ({ effective: computed(() => 'table'), canSwitch: computed(() => false), setMode: vi.fn() }),
}))
vi.mock('../../api/market', () => ({
  getAdminMarketCompare: vi.fn(),
}))

const ROW = {
  credential_id: 7,
  provider_model_id: 33,
  canonical_name: 'glm-5.3',
  currency: 'CNY',
  supplier_in_per_1m: 2.0,
  supplier_out_per_1m: 8.0,
  price_confidence: 'trusted',
  discount_vs_vendor: 0.15,
  vendor_ref_in_per_1m: 2.35,
  vendor_ref_out_per_1m: 9.4,
  vendor_ref_currency: 'CNY',
  fx_used: null,
  market_position: 1.1,
  market_ref_in_per_1m: 1.9,
  market_ref_out_per_1m: 7.6,
  market_ref_platform_id: 'zhipu',
  market_ref_platform_count: 4,
  market_ref_used: 'domestic',
  market_advantage_score: -2.5,
  market_position_vs_overseas: 0.9,
  market_position_vs_domestic: 1.1,
  computed_at: '2026-11-13T03:10:00Z',
  overseas_min_in_per_1m: 1.9,
  overseas_min_out_per_1m: 7.6,
  overseas_min_platform_id: 'zhipu',
  overseas_min_platform_count: 4,
  domestic_min_in_per_1m: 3.5,
  domestic_min_out_per_1m: 14.0,
  domestic_min_platform_id: 'minimax',
  domestic_min_platform_count: 2,
  vendor_list_currency: 'CNY',
  vendor_list_native_in_per_1m: 2.35,
  vendor_list_native_out_per_1m: 9.4,
  vendor_list_cny_in_per_1m: 2.35,
  vendor_list_cny_out_per_1m: 9.4,
  mpr_fx_rate: 6.687755,
}

describe('PriceCompareView', () => {
  beforeEach(() => {
    vi.mocked(getAdminMarketCompare).mockReset()
  })

  it('renders scored rows with dual floors and summary badges', async () => {
    vi.mocked(getAdminMarketCompare).mockResolvedValue({
      available: true, items: [ROW as unknown as MarketCompareRow], summary: { trusted: 27, empty: 4099, suspect_symmetric: 0, unknown_currency: 0 },
    })
    const w = mount(PriceCompareView, { global: { plugins: [testI18n] } })
    await flushPromises()

    expect(w.text()).toContain('glm-5.3')
    expect(w.text()).toContain('trusted')
    // 双地板三级币：海外 USD 原生 1.9 与快照 fx 折 CNY（1.9×6.687755≈12.7067）都要可见。
    expect(w.text()).toContain('1.9')
    expect(w.text()).toContain('12.7067')
    // 覆盖率摘要：绑定总数 = 27+4099。
    expect(w.text()).toContain('4126')
    // 平台数 <3 的国内地板要有薄覆盖警示类。
    expect(w.findAll('.thin-floor').length).toBeGreaterThanOrEqual(1)
  })

  it('NULL prices stay em-dash（NULL≠0）', async () => {
    const emptyRow = { ...ROW, supplier_in_per_1m: null, supplier_out_per_1m: null,
      price_confidence: 'empty', market_position: null, market_ref_in_per_1m: null,
      market_ref_out_per_1m: null, market_ref_used: '', market_ref_platform_id: '',
      market_position_vs_overseas: null, market_position_vs_domestic: null,
      market_advantage_score: null, mpr_fx_rate: null,
      overseas_min_in_per_1m: null, overseas_min_out_per_1m: null,
      domestic_min_in_per_1m: null, domestic_min_out_per_1m: null,
      vendor_list_native_in_per_1m: null, vendor_list_cny_in_per_1m: null }
    vi.mocked(getAdminMarketCompare).mockResolvedValue({
      available: true, items: [emptyRow as unknown as MarketCompareRow], summary: { empty: 1 },
    })
    const w = mount(PriceCompareView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // NULL 格子必须显示「—」而不是 0。
    expect(w.text()).not.toContain('0.0000')
    expect(w.findAll('td').some(td => td.text() === '—')).toBe(true)
  })

  it('unavailable shows why + next-step copy（空状态门）', async () => {
    vi.mocked(getAdminMarketCompare).mockResolvedValue({
      available: false, reason: 'supplier_market_position not present (migration 959 / scoring job not applied)',
      items: [], summary: {},
    })
    const w = mount(PriceCompareView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.findAll('.app-empty-state').length).toBe(1)
    // 空态必须带「为什么」指针（服务端 reason 原文）与「下一步」。
    expect(w.text()).toContain('migration 959')
  })
})
