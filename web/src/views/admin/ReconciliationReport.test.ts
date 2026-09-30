// ReconciliationReport.test.ts — reconciliation page interactions (2026-09-30 stats UI).
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zhCN from '../../locales/zh-CN'
import ReconciliationReport from './ReconciliationReport.vue'
import { quickRange } from '../../components/reconciliation/format'
import type { DimensionOptions, RangeReport } from '../../api/reportrollup'

const getReportSummaryMock = vi.fn()
const getReportDimensionsMock = vi.fn()
const replaceMock = vi.fn()

vi.mock('../../api/reportrollup', async () => {
  const actual = await vi.importActual<typeof import('../../api/reportrollup')>('../../api/reportrollup')
  return {
    ...actual,
    getReportSummary: (...args: unknown[]) => getReportSummaryMock(...args),
    getReportDimensions: (...args: unknown[]) => getReportDimensionsMock(...args),
    downloadReportExport: vi.fn(),
    runReportRollup: vi.fn(),
  }
})

const routeState = reactive<{ query: Record<string, string> }>({ query: {} })
vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: vi.fn(), replace: replaceMock }),
}))

vi.mock('../../components/reconciliation/ReconciliationCharts.vue', () => ({
  default: {
    name: 'ReconciliationCharts',
    props: ['days', 'coveredDates', 'money'],
    template: '<div data-testid="charts-stub" />',
  },
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN },
})

const totals = (n: number, extra: Record<string, number> = {}) => ({
  request_count: n,
  success_count: n - 1,
  error_count: 1,
  error_rate: 1 / n,
  input_tokens: 10,
  output_tokens: 20,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  total_tokens: extra.total_tokens ?? 30,
  estimated_cost_cents: extra.estimated_cost_cents ?? 123,
  currency: 'USD',
  credits_charged: extra.credits_charged ?? n * 100,
  internal_cost_cents: 456,
  internal_currency: 'CNY',
  cache_hit_ratio: 0.5,
  latency_p50_ms: 100,
  latency_p95_ms: 900,
})

const emptyDims: DimensionOptions = {
  providers: [{ key: '2', name: 'prov-beta', requests: 10 }],
  credentials: [],
  models: [{ key: 'gpt-test', requests: 30 }],
  tenants: [],
  persons: [],
  api_keys: [],
}

function providerReport(): RangeReport {
  return {
    start: '2026-09-21',
    end: '2026-09-27',
    view: 'provider',
    totals: totals(30),
    error_breakdown: { timeout: 4, quota: 1 },
    days: [{ date: '2026-09-21', totals: totals(30) }],
    providers: [
      { provider_id: 1, provider_name: 'prov-alpha', quality_score: 88.5, totals: totals(20, { total_tokens: 500, credits_charged: 10 }), error_breakdown: { timeout: 1 } },
      { provider_id: 2, provider_name: 'prov-beta', quality_score: 91, totals: totals(10, { total_tokens: 50, credits_charged: 900 }), error_breakdown: {} },
    ],
    model_totals: [
      { raw_model_name: 'gpt-test', provider_name: 'prov-alpha', totals: totals(30), error_breakdown: { timeout: 4 } },
      { raw_model_name: 'haiku-test', provider_name: 'prov-beta', totals: totals(10), error_breakdown: { quota: 1 } },
    ],
    tenants: [],
    persons: [],
    snapshot_dates: ['2026-09-21'],
    coverage: { grain_dates: ['2026-09-21'], legacy_dates: [] },
    source: 'grain',
  }
}

function internalReport(): RangeReport {
  return {
    ...providerReport(),
    view: 'internal',
    providers: [],
    tenants: [{ tenant_id: 'tenant-x', totals: totals(25), error_breakdown: {}, quality_score: 80 }],
    persons: [{ tenant_id: 'tenant-x', person: 'alice', totals: totals(25), error_breakdown: {}, quality_score: 80 }],
  }
}

async function mountView() {
  const wrapper = mount(ReconciliationReport, { global: { plugins: [i18n] } })
  await flushPromises()
  return wrapper
}

async function clickRadio(wrapper: Awaited<ReturnType<typeof mountView>>, value: string) {
  const input = wrapper
    .findAll<HTMLInputElement>('input.el-radio-button__original-radio')
    .find((el) => el.element.value === value)
  expect(input, `missing radio ${value}`).toBeTruthy()
  input!.element.checked = true
  await input!.trigger('change')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  routeState.query = {}
  getReportDimensionsMock.mockResolvedValue(emptyDims)
})

describe('ReconciliationReport', () => {
  it('renders provider rows in a real table and keeps the day table collapsed', async () => {
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    const primary = wrapper.get('[data-testid="primary-dist"]')
    expect(primary.findAll('tr.el-table__row')).toHaveLength(2)
    expect(primary.text()).toContain('prov-alpha')
    expect(wrapper.text()).not.toContain('2026-09-21')
    expect(wrapper.get('[data-testid="recon-kpi"]').text()).toContain('30')
  })

  it('switches to the internal view and shows tenants plus persons', async () => {
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    getReportSummaryMock.mockResolvedValue(internalReport())
    await clickRadio(wrapper, 'internal')
    expect(getReportSummaryMock).toHaveBeenCalledWith(expect.objectContaining({ view: 'internal' }))
    expect(wrapper.text()).toContain('tenant-x')
    expect(wrapper.get('[data-testid="person-dist"]').text()).toContain('alice')
  })

  it('reorders the provider table when the metric switches from tokens to credits', async () => {
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    const names = () => wrapper.get('[data-testid="primary-dist"]').findAll('tr.el-table__row').map((row) => row.text())
    expect(names()[0]).toContain('prov-alpha')
    await wrapper.get('[data-testid="metric-money"]').trigger('click')
    expect(names()[0]).toContain('prov-beta')
  })

  it('assembles provider_id from the toolbar select and from a row drill-down', async () => {
    routeState.query = { provider_id: '2' }
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    expect(getReportSummaryMock.mock.calls[0][0]).toEqual(expect.objectContaining({ provider_id: 2, view: 'provider' }))

    const row = wrapper.get('[data-testid="primary-dist"]').findAll('tr.el-table__row')[0]
    await row.trigger('click')
    await flushPromises()
    const last = getReportSummaryMock.mock.calls.at(-1)?.[0] as { provider_id?: number }
    expect(last.provider_id).toBe(1)
    expect(replaceMock).toHaveBeenCalledWith(expect.objectContaining({
      query: expect.objectContaining({ provider_id: '1' }),
    }))
  })

  it('filters the model table when a failure reason is picked', async () => {
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    expect(wrapper.get('[data-testid="model-dist"]').text()).toContain('haiku-test')
    await wrapper.get('[data-reason="timeout"]').trigger('click')
    expect(wrapper.get('[data-testid="model-dist"]').text()).toContain('gpt-test')
    expect(wrapper.get('[data-testid="model-dist"]').text()).not.toContain('haiku-test')
    expect(wrapper.text()).toContain(zhCN.reports.reasonFilterHint)
  })

  it('shows the empty-snapshot alert and applies the yesterday chip', async () => {
    getReportSummaryMock.mockResolvedValue({ ...providerReport(), snapshot_dates: [], days: [] })
    const wrapper = await mountView()
    expect(wrapper.text()).toContain(zhCN.reports.noSnapshots)
    await wrapper.get('[data-quick="yesterday"]').trigger('click')
    await flushPromises()
    const [start, end] = quickRange('yesterday')
    expect(getReportSummaryMock).toHaveBeenCalledWith(expect.objectContaining({ start, end }))
  })
})
