import { mount, flushPromises } from '@vue/test-utils'
import { computed } from 'vue'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import MarketPricesView from './MarketPricesView.vue'
import { testI18n } from '../../test-utils/i18n'
import { getAdminMarketPlatforms, putAdminMarketPlatform } from '../../api/market'

vi.mock('../../composables/useDataViewMode', () => ({
  useDataViewMode: () => ({ effective: computed(() => 'table'), canSwitch: computed(() => false), setMode: vi.fn() }),
}))
vi.mock('../../api/market', () => ({
  getAdminMarketPlatforms: vi.fn(),
  putAdminMarketPlatform: vi.fn(),
}))
vi.mock('../../api/maas', () => ({
  getAdminMaasMarketPriceSyncStatus: vi.fn(async () => ({ items: [
    { id: 1, run_date: '2026-11-13', source_platforms: 25, source_rows: 1200, landed_rows: 1100, rejected_rows: 100, conflicts: { platform_unmapped_region: 90, other: 10 }, created_at: '2026-11-13T02:30:00Z' },
  ] })),
}))

const PLATFORMS = [
  { platform_id: 'anthropic', platform_name: 'Anthropic', region: 'overseas', native_currency: 'USD', is_ours: false, enabled: true, notes: '', updated_at: '2026-11-13T03:10:00Z' },
  { platform_id: 'zhipu', platform_name: '智谱AI', region: 'domestic', native_currency: 'CNY', is_ours: true, enabled: false, notes: '对拍过', updated_at: '2026-11-12T03:10:00Z' },
]

describe('MarketPricesView', () => {
  beforeEach(() => {
    vi.mocked(getAdminMarketPlatforms).mockReset()
    vi.mocked(putAdminMarketPlatform).mockReset()
  })

  it('renders registry rows and sync ledger', async () => {
    vi.mocked(getAdminMarketPlatforms).mockResolvedValue({ items: PLATFORMS })
    const w = mount(MarketPricesView, { global: { plugins: [testI18n] } })
    await flushPromises()

    const tables = w.findAll('table')
    expect(tables.length).toBe(2)
    expect(w.text()).toContain('anthropic')
    expect(w.text()).toContain('智谱AI')
    // 同步台账：拒绝 100 / 冲突 2 类 都要可见（对账读数）。
    expect(w.text()).toContain('100')
    expect(w.text()).toContain('2026-11-13')
  })

  it('edit flow calls PUT with full form and reloads', async () => {
    vi.mocked(getAdminMarketPlatforms).mockResolvedValue({ items: PLATFORMS })
    vi.mocked(putAdminMarketPlatform).mockResolvedValue({ item: PLATFORMS[0] })
    const w = mount(MarketPricesView, { global: { plugins: [testI18n] } })
    await flushPromises()

    await w.findAll('tbody')[0].findAll('button')[0].trigger('click')
    const modal = w.find('.modal')
    expect(modal.exists()).toBe(true)

    // 改 notes 之外还改了 is_ours 开关 ⇒ PUT body 携带全部字段（部分更新语义由后端收）。
    const checkbox = modal.findAll('input[type="checkbox"]')[0]
    await checkbox.setValue(false)
    await modal.find('.modal-actions .btn-primary').trigger('click')
    await flushPromises()

    expect(putAdminMarketPlatform).toHaveBeenCalledTimes(1)
    const [pid, body] = vi.mocked(putAdminMarketPlatform).mock.calls[0]
    expect(pid).toBe('anthropic')
    expect(body.is_ours).toBe(false)
    expect(body.region).toBe('overseas')
    expect(body.native_currency).toBe('USD')
  })

  it('empty registry shows why-empty + next-step copy（空状态门）', async () => {
    vi.mocked(getAdminMarketPlatforms).mockResolvedValue({ items: [] })
    const w = mount(MarketPricesView, { global: { plugins: [testI18n] } })
    await flushPromises()

    // 空态必须走 EmptyState（app-empty-state）而不是空表格 —— 且文案给「为什么 + 下一步」。
    // registry 空走 ResponsiveDataView 的 #empty 槽（958 种子指针），sync 有数据 ⇒ 只有一块空态。
    const emptyEls = w.findAll('.app-empty-state')
    expect(emptyEls.length).toBe(1)
    expect(w.text()).toContain('958')
    expect(w.html()).not.toContain('undefined')
  })

  it('empty sync ledger shows syncer why/next copy（空状态门）', async () => {
    vi.mocked(getAdminMarketPlatforms).mockResolvedValue({ items: PLATFORMS })
    const { getAdminMaasMarketPriceSyncStatus } = await import('../../api/maas')
    vi.mocked(getAdminMaasMarketPriceSyncStatus).mockResolvedValue({ items: [] })
    const w = mount(MarketPricesView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.text()).toContain('market-price-sync.py')
    expect(w.findAll('.app-empty-state').length).toBe(1)
  })

  it('registry load failure degrades to why/next empty state', async () => {
    vi.mocked(getAdminMarketPlatforms).mockRejectedValue(new Error('500'))
    const w = mount(MarketPricesView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.text()).not.toContain('anthropic')
    expect(w.findAll('.app-empty-state').length).toBe(2) // registry + sync 两块都有空态
  })
})
