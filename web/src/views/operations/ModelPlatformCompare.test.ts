import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import ModelPlatformCompare from './ModelPlatformCompare.vue'
import { testI18n } from '../../test-utils/i18n'
import { getAdminMarketModelPlatforms } from '../../api/market'

vi.mock('../../api/market', () => ({
  getAdminMarketModelPlatforms: vi.fn(),
}))

// ★ 2026-11-17（F11）行为门：明细模式**不得**把别的模型的价格混进来，
//   「最便宜」标记**不得**挂到别的模型（glm-5.3-flash）的行上。
//
//   线上实测（211）：`model=glm-5.3` 曾返回 43 行，其中只有 17 行是 glm-5.3；
//   OpenRouter 的 glm-5.3-flash（¥0.2665）曾抢走 glm-5.3 自己的地板（¥0.4668）。
vi.mocked(getAdminMarketModelPlatforms).mockResolvedValue({
  available: true,
  items: [],
  directory: [],
  is_dir: true,
})

const DIR = [
  {
    canonical_name: 'glm-5.3',
    display_name: 'GLM-5.3',
    platforms: 9,
    vendor_rows: 4,
    min_cny_in_per_1m: 0.4668,
    min_cny_out_per_1m: 9.35,
    run_date: '2026-11-17',
  },
]

// glm-5.3 自己的两行（非原厂 1 行 + 原厂 1 行）
const OWN_ROWS = [
  {
    canonical_name: 'glm-5.3',
    platform_id: 'siliconflow',
    platform_name: '硅基流动 SiliconFlow',
    region: 'domestic',
    native_currency: 'CNY',
    is_vendor: false,
    vendor: '',
    currency: 'CNY',
    input_per_1m: 0.8,
    output_per_1m: 2.8,
    cny_in_per_1m: 0.4668,
    cny_out_per_1m: 9.35,
    cache_hit_per_1m: null,
    cny_cache_hit_per_1m: null,
    tier: '',
    model_kind: '文本/对话',
    run_date: '2026-11-17',
    source_url: 'https://example.test',
  },
  {
    canonical_name: 'glm-5.3',
    platform_id: 'baidu-qianfan',
    platform_name: '百度智能云千帆',
    region: 'domestic',
    native_currency: 'CNY',
    is_vendor: true,
    vendor: '百度',
    currency: 'CNY',
    input_per_1m: 8,
    output_per_1m: 28,
    cny_in_per_1m: 8,
    cny_out_per_1m: 28,
    cache_hit_per_1m: 2,
    cny_cache_hit_per_1m: 2,
    tier: '',
    model_kind: '文本/对话',
    run_date: '2026-10-31',
    source_url: 'https://example.test',
  },
]

// 同族变体的行（更便宜 —— 若守卫失效，它会抢走「最便宜」标记）
const FAMILY_ROW = {
  canonical_name: 'glm-5.3-flash',
  platform_id: 'openrouter',
  platform_name: 'OpenRouter',
  region: 'overseas',
  native_currency: 'USD',
  is_vendor: false,
  vendor: '',
  currency: 'USD',
  input_per_1m: 0.0399,
  output_per_1m: 0.0784,
  cny_in_per_1m: 0.2665,
  cny_out_per_1m: 0.5236,
  cache_hit_per_1m: 0.2004,
  cny_cache_hit_per_1m: null,
  tier: '',
  model_kind: '文本/对话',
  run_date: '2026-11-17',
  source_url: 'https://example.test',
}

describe('ModelPlatformCompare（F11 明细匹配口径）', () => {
  beforeEach(() => {
    vi.mocked(getAdminMarketModelPlatforms).mockReset()
  })

  function mockApi(scope: '' | 'family') {
    vi.mocked(getAdminMarketModelPlatforms).mockImplementation(
      async (model?: string, _dim?: string, _limit?: number, s?: string) => {
        if (!model) return { available: true, items: [], directory: DIR, is_dir: true }
        const items = (s || scope) === 'family' ? [...OWN_ROWS, FAMILY_ROW] : OWN_ROWS
        return { available: true, items, directory: [], is_dir: false, scope: s || scope }
      },
    )
  }

  it('默认走精确匹配：请求不带 scope，且响应里只有本模型', async () => {
    mockApi('')
    const w = mount(ModelPlatformCompare, { global: { plugins: [testI18n] } })
    await flushPromises()

    // 从目录里选 glm-5.3
    const pickBtn = w.findAll('tbody').at(0)?.findAll('button').at(0)
    expect(pickBtn).toBeTruthy()
    await pickBtn!.trigger('click')
    await flushPromises()

    const call = vi.mocked(getAdminMarketModelPlatforms).mock.calls.at(-1)
    expect(call?.[0]).toBe('glm-5.3')
    // ★ 默认不得带 scope（scope=family 必须是显式选择）
    expect(call?.[3] ?? '').toBe('')

    const text = w.text()
    expect(text).toContain('glm-5.3')
    expect(text).not.toContain('glm-5.3-flash')
    // 默认视图的**明细表**不显示「模型」列（只有同族视图才需要）
    // ★ 注意：目录表本身就有「模型」列头 ⇒ 必须取最后一张表（明细表）的表头。
    const detailHeaders = w.findAll('table').at(-1)?.findAll('th').map((th) => th.text()) ?? []
    expect(detailHeaders).not.toContain('模型')
  })

  it('「最便宜」标记只落在本模型行上（同族更便宜的行不得抢走）', async () => {
    mockApi('family')
    const w = mount(ModelPlatformCompare, { global: { plugins: [testI18n] } })
    await flushPromises()

    const pickBtn = w.findAll('tbody').at(0)?.findAll('button').at(0)
    await pickBtn!.trigger('click')
    await flushPromises()
    // 切到「含同族变体」
    const familyBtn = w.findAll('button').filter((b) => b.text() === '含同族变体').at(0)
    expect(familyBtn).toBeTruthy()
    await familyBtn!.trigger('click')
    await flushPromises()

    const call = vi.mocked(getAdminMarketModelPlatforms).mock.calls.at(-1)
    expect(call?.[3]).toBe('family')

    // 同族视图必须显示「模型」列（否则分不清哪行是哪个模型）
    const detailHeaders = w.findAll('table').at(-1)?.findAll('th').map((th) => th.text()) ?? []
    expect(detailHeaders).toContain('模型')
    expect(w.text()).toContain('glm-5.3-flash')

    // ★ 最便宜标记：在**本模型**（glm-5.3 / 硅基流动）那一行，不在 glm-5.3-flash 行
    const cheapest = w.findAll('.badge-green')
    expect(cheapest.length).toBe(1)
    const ownerRow = cheapest[0].element.closest('tr')
    expect(ownerRow?.textContent).toContain('硅基流动')
    expect(ownerRow?.textContent).not.toContain('OpenRouter')
  })
})
