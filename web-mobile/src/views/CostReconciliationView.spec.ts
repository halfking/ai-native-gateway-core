import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import CostReconciliationView from './CostReconciliationView.vue'
import { fetchCostReconciliation, type CostReconciliationRecord } from '@/api/costReconciliation'
import { setLocale } from '@/i18n'
import { createTopbarActionRegistry, TOPBAR_ACTIONS } from '@/components/shell/topbarActions'
vi.mock('@/hyper', () => ({ useHyperPage: vi.fn() }))
vi.mock('@/api/costReconciliation', () => ({ fetchCostReconciliation: vi.fn() }))
const record: CostReconciliationRecord = {
  id: 1, provider_id: 1, reconciliation_month: '2026-10-01T00:00:00Z',
  gateway_total_tokens: 100, gateway_input_tokens: 80, gateway_output_tokens: 20, gateway_total_cost: 5,
  provider_total_tokens: 0, provider_input_tokens: 0, provider_output_tokens: 0, provider_total_cost: 0,
  token_diff_rate: 0, cost_diff_rate: 0, data_source: '', notes: '', updated_at: '2026-10-01T00:00:00Z',
}
let wrapper: ReturnType<typeof mount> | undefined
let topbarRegistry = createTopbarActionRegistry()
beforeEach(() => {
  vi.resetAllMocks()
  setLocale('en-US')
  topbarRegistry = createTopbarActionRegistry()
})
afterEach(() => { wrapper?.unmount(); vi.useRealTimers() })
function mountView(): ReturnType<typeof mount> {
  // 提供 TOPBAR_ACTIONS registry，否则 useTopbarAction 静默 no-op（仍然跑通），
  // 但本测试不依赖顶栏 nameable —— 这里只是遵循 web-mobile 其它视图的 spec 模板。
  return mount(CostReconciliationView, {
    attachTo: document.body,
    global: { provide: { [TOPBAR_ACTIONS as symbol]: topbarRegistry } },
  })
}
describe('reconciliation audit', () => {
  it('uses UTC months and marks a zero provider bill with positive gateway usage as drift', async () => {
    vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-31T23:30:00Z'))
    vi.mocked(fetchCostReconciliation).mockResolvedValue({ month: '2026-10', count: 1, items: [{ ...record, data_source: 'manual' }] })
    wrapper = mountView()
    await flushPromises()
    expect(fetchCostReconciliation).toHaveBeenCalledWith('2026-10', expect.anything())
    expect(wrapper.find('.badge--warning').exists()).toBe(true)
  })
  it('discards results for an old month and does not carry their totals to a new month', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof fetchCostReconciliation>>) => void
    vi.mocked(fetchCostReconciliation).mockReturnValue(new Promise((done) => { resolve = done }))
    wrapper = mountView()
    // 2026-10-11：原 `<input type="month">` 改为两个 `<select>` 后，这里改用
    // data-testid 选择器：年份默认 2026、月份改成 1 ⇒ 后端命中 2026-01。
    const monthSelect = wrapper.find('[data-testid="reconciliation-month"]')
    await monthSelect.setValue('1')
    resolve({ month: '2026-10', count: 1, items: [record] })
    await flushPromises()
    expect(wrapper.findAll('.reconciliation__card')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('$5')
  })
  it('shows unknown provider totals when the invoice has not been imported', async () => {
    vi.mocked(fetchCostReconciliation).mockResolvedValue({ month: '2026-10', count: 1, items: [record] })
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('.badge--muted').exists()).toBe(true)
    expect(wrapper.find('.reconciliation__costs').text()).toContain('—')
  })
  it('aggregates 12 monthly fetches when month is "All months"', async () => {
    vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-15T00:00:00Z'))
    vi.mocked(fetchCostReconciliation).mockResolvedValue({ month: '2026-01', count: 0, items: [] })
    wrapper = mountView()
    await flushPromises()
    const monthSelect = wrapper.find('[data-testid="reconciliation-month"]')
    await monthSelect.setValue('all')
    await flushPromises()
    // 切换月份只重置 generation 与状态，不会自动重拉 —— 用户按顶栏「刷新对账」
    // 按钮才触发拉取。所以「all months」会再发 12 次，而不是 13 次。
    const refresh = topbarRegistry.actions.value.find((a) => a.key === 'reconciliation-refresh')
    expect(refresh).toBeDefined()
    await refresh?.onClick()
    await flushPromises()
    // onMounted 1 次 + 「全部月份」并发 12 次 = 13 次。
    expect(vi.mocked(fetchCostReconciliation).mock.calls.length).toBeGreaterThanOrEqual(13)
  })
})
