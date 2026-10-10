import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import CostReconciliationView from './CostReconciliationView.vue'
import { fetchCostReconciliation, type CostReconciliationRecord } from '@/api/costReconciliation'
import { setLocale } from '@/i18n'
vi.mock('@/hyper', () => ({ useHyperPage: vi.fn() }))
vi.mock('@/api/costReconciliation', () => ({ fetchCostReconciliation: vi.fn() }))
const record: CostReconciliationRecord = {
  id: 1, provider_id: 1, reconciliation_month: '2026-10-01T00:00:00Z',
  gateway_total_tokens: 100, gateway_input_tokens: 80, gateway_output_tokens: 20, gateway_total_cost: 5,
  provider_total_tokens: 0, provider_input_tokens: 0, provider_output_tokens: 0, provider_total_cost: 0,
  token_diff_rate: 0, cost_diff_rate: 0, data_source: '', notes: '', updated_at: '2026-10-01T00:00:00Z',
}
let wrapper: ReturnType<typeof mount> | undefined
beforeEach(() => { vi.resetAllMocks(); setLocale('en-US') })
afterEach(() => { wrapper?.unmount(); vi.useRealTimers() })
describe('reconciliation audit', () => {
  it('uses UTC months and marks a zero provider bill with positive gateway usage as drift', async () => {
    vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-31T23:30:00Z'))
    vi.mocked(fetchCostReconciliation).mockResolvedValue({ month: '2026-10', count: 1, items: [{ ...record, data_source: 'manual' }] })
    wrapper = mount(CostReconciliationView)
    await flushPromises()
    expect(fetchCostReconciliation).toHaveBeenCalledWith('2026-10', expect.anything())
    expect(wrapper.find('.badge--warning').exists()).toBe(true)
  })
  it('discards results for an old month and does not carry their totals to a new month', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof fetchCostReconciliation>>) => void
    vi.mocked(fetchCostReconciliation).mockReturnValue(new Promise((done) => { resolve = done }))
    wrapper = mount(CostReconciliationView)
    await wrapper.find('input').setValue('2026-01')
    resolve({ month: '2026-10', count: 1, items: [record] })
    await flushPromises()
    expect(wrapper.findAll('.reconciliation__card')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('$5')
  })
  it('shows unknown provider totals when the invoice has not been imported', async () => {
    vi.mocked(fetchCostReconciliation).mockResolvedValue({ month: '2026-10', count: 1, items: [record] })
    wrapper = mount(CostReconciliationView)
    await flushPromises()
    expect(wrapper.find('.badge--muted').exists()).toBe(true)
    expect(wrapper.find('.reconciliation__costs').text()).toContain('—')
  })
})
