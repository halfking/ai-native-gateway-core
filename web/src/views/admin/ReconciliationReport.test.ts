// ReconciliationReport.test.ts — reconciliation page interactions (2026-09-30 stats UI).
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zhCN from '../../locales/zh-CN'
import ReconciliationReport from './ReconciliationReport.vue'
import KxDateRangePicker from '../../components/ui/KxDateRangePicker.vue'
import { snapshotDatePresets, snapshotNotAfter } from '../../components/reconciliation/snapshotRange'
import { systemTzDateStr } from '../../components/ui/kxDatePresets'
import { downloadReportExport, type DimensionOptions, type RangeReport, type ReportQuery } from '../../api/reportrollup'
import { readDailyReconciliationDefault, readDailyReport, type DailyQuery } from '../../api/requestReconciliation'
import { isSuperAdmin } from '../../store'

const getReportSummaryMock = vi.fn()
const getReportDimensionsMock = vi.fn()
const pushMock = vi.fn()

vi.mock('../../store', async importOriginal => ({
  ...await importOriginal<typeof import('../../store')>(),
  isSuperAdmin: vi.fn(),
}))
vi.mock('../../api/requestReconciliation', async importOriginal => ({
  ...await importOriginal<typeof import('../../api/requestReconciliation')>(),
  readDailyReconciliationDefault: vi.fn(),
  readDailyReport: vi.fn(),
}))

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
  useRouter: () => ({ push: pushMock, replace: vi.fn() }),
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

function presetRange(id: string) {
  const preset = snapshotDatePresets().find((item) => item.id === id)
  if (!preset) throw new Error(`missing preset ${id}`)
  return preset.resolve()
}

async function applyPreset(wrapper: Awaited<ReturnType<typeof mountView>>, id: string) {
  const picker = wrapper.findComponent(KxDateRangePicker)
  await picker.vm.$emit('apply', presetRange(id))
  await flushPromises()
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
  vi.mocked(isSuperAdmin).mockReturnValue(false)
  vi.mocked(readDailyReconciliationDefault).mockReset().mockResolvedValue(null)
  vi.mocked(readDailyReport).mockReset().mockImplementation(async (query: DailyQuery) => ({
    ...query, version: 'request-reconciliation-v2', platform_scope: true, coverage: 'retained',
    start: '2026-11-21T08:00:00Z', end: '2026-11-22T08:00:00Z', read_at: '2026-11-23T00:00:00Z',
    summary: { received_requests: 12 }, rows: [],
  }))
  routeState.query = {}
  getReportDimensionsMock.mockResolvedValue(emptyDims)
})

describe('ReconciliationReport', () => {
  it('opens the platform day without requesting snapshots and allows a manual rollback', async () => {
    vi.mocked(isSuperAdmin).mockReturnValue(true)
    vi.mocked(readDailyReconciliationDefault).mockResolvedValue({ day: '2026-11-21', timezone: 'America/Los_Angeles' })
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    expect(readDailyReport).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({
      day: '2026-11-21', timezone: 'America/Los_Angeles', section: 'coverage',
    }), expect.anything())
    expect(getReportSummaryMock).not.toHaveBeenCalled()
    expect(getReportDimensionsMock).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="reconciliation-mode-daily"]').attributes('aria-pressed')).toBe('true')
    await wrapper.get('[data-testid="reconciliation-mode-legacy"]').trigger('click')
    await flushPromises()
    expect(getReportSummaryMock).toHaveBeenCalledTimes(1)
    expect(getReportDimensionsMock).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="primary-dist"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('keeps deep-linked snapshot filters and returns to snapshots if the default is unavailable', async () => {
    vi.mocked(isSuperAdmin).mockReturnValue(true)
    vi.mocked(readDailyReconciliationDefault).mockResolvedValue({ day: '2026-11-21', timezone: 'UTC' })
    routeState.query = { view: 'provider', provider_id: '2', model: 'gpt-test' }
    getReportSummaryMock.mockResolvedValue(providerReport())
    const linked = await mountView()
    expect(getReportSummaryMock).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ provider_id: 2, model: 'gpt-test' }))
    expect(readDailyReport).not.toHaveBeenCalled()
    linked.unmount()

    routeState.query = {}
    getReportSummaryMock.mockClear()
    vi.mocked(readDailyReconciliationDefault).mockRejectedValue(new Error('unavailable'))
    const fallback = await mountView()
    expect(getReportSummaryMock).toHaveBeenCalledTimes(1)
    expect(fallback.find('[data-testid="reconciliation-mode-daily"]').exists()).toBe(false)
    expect(fallback.get('details.daily-panel').attributes('open')).toBeUndefined()
    fallback.unmount()
  })

  it('keeps the fallback when a timed-out default arrives late', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(isSuperAdmin).mockReturnValue(true)
      let deliver: ((calendar: { day: string; timezone: string }) => void) | undefined
      vi.mocked(readDailyReconciliationDefault).mockReturnValue(new Promise(resolve => { deliver = resolve }))
      getReportSummaryMock.mockResolvedValue(providerReport())
      const wrapper = mount(ReconciliationReport, { global: { plugins: [i18n] } })
      await vi.advanceTimersByTimeAsync(3500)
      expect(getReportSummaryMock).toHaveBeenCalledTimes(1)
      expect(vi.mocked(readDailyReconciliationDefault).mock.calls[0][0]?.signal?.aborted).toBe(true)
      deliver?.({ day: '2026-11-21', timezone: 'UTC' })
      await vi.advanceTimersByTimeAsync(0)
      expect(readDailyReport).not.toHaveBeenCalled()
      expect(wrapper.find('[data-testid="reconciliation-mode-daily"]').exists()).toBe(false)
      wrapper.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

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

  it('pages people and resets the ranking when drilling down', async () => {
    routeState.query = { view: 'internal' }
    getReportSummaryMock.mockImplementation(async ({ person_page }: ReportQuery) => {
      const report = internalReport()
      const number = person_page ?? 1
      report.person_page = { number, page_size: 50, total: 54 }
      if (number === 2) report.persons![0].person = 'bob'
      return report
    })
    const wrapper = await mountView()
    expect(getReportSummaryMock).toHaveBeenLastCalledWith(expect.objectContaining({ person_page: 1 }))
    await wrapper.get('.person-pages .btn-next').trigger('click')
    await flushPromises()
    expect(getReportSummaryMock).toHaveBeenLastCalledWith(expect.objectContaining({ person_page: 2 }))
    const people = wrapper.get('[data-testid="person-dist"]')
    expect(people.text()).toContain('bob')
    expect(people.text()).not.toContain('alice')

    wrapper.getComponent({ name: 'ReconciliationToolbar' }).vm.$emit('export')
    expect(downloadReportExport).toHaveBeenCalledWith(expect.objectContaining({ view: 'internal', group: 'tenant' }))
    expect(vi.mocked(downloadReportExport).mock.calls.at(-1)?.[0].person_page).toBeUndefined()

    await people.get('tr.el-table__row').trigger('click')
    await flushPromises()
    expect(getReportSummaryMock).toHaveBeenLastCalledWith(expect.objectContaining({
      person_page: 1, tenant_id: 'tenant-x', person: 'bob',
    }))
    wrapper.unmount()
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
    expect(getReportSummaryMock.mock.calls[0][0]).toEqual(expect.objectContaining({ provider_id: 2, view: 'provider', summary_scope: 'distribution' }))

    const row = wrapper.get('[data-testid="primary-dist"]').findAll('tr.el-table__row')[0]
    await row.trigger('click')
    await flushPromises()
    const last = getReportSummaryMock.mock.calls.at(-1)?.[0] as { provider_id?: number }
    expect(last.provider_id).toBe(1)
    expect(pushMock).toHaveBeenCalledWith(expect.objectContaining({
      query: expect.objectContaining({ provider_id: '1' }),
    }))
  })

  it('browser back drops provider_id and refetches without that filter', async () => {
    routeState.query = { view: 'provider', provider_id: '2' }
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    const callsBefore = getReportSummaryMock.mock.calls.length
    routeState.query = { view: 'provider' }
    // ★ 触发异步链（watcher → 取数 → 重渲染）之后，**单次 `flushPromises()`
    //   不足以保证链路走完**：隔离 6/6 绿、全量跑偶发红（本仓实测 2026-10-05）。
    //   改成有界轮询，断言一条不动。
    for (let i = 0; i < 5 && getReportSummaryMock.mock.calls.length <= callsBefore; i++) {
      await flushPromises()
    }
    const last = getReportSummaryMock.mock.calls.at(-1)?.[0] as { provider_id?: number }
    expect(getReportSummaryMock.mock.calls.length).toBeGreaterThan(callsBefore)
    expect(last.provider_id).toBeUndefined()
    // ★ 渲染落地单独等：取数完成 ≠ DOM 已更新（Vue 的渲染在 nextTick 排空）。
    //   这里**不编一个「新内容」当等待条件** —— 编出来的条件要么永远不成立
    //   （白等 5 轮），要么一开始就不成立（一次都不等），两种都是假等待。
    //   所以取数条件轮询到之后，再固定补几轮让它渲染。
    for (let i = 0; i < 3; i++) await flushPromises()
    expect(wrapper.get('[data-testid="primary-dist"]').text()).toContain('prov-alpha')
  })

  it('ignores a slower response that returns after a newer query', async () => {
    let releaseStale: (value: RangeReport) => void = () => {}
    getReportSummaryMock.mockImplementationOnce(
      () => new Promise((resolve) => { releaseStale = resolve }),
    )
    const wrapper = mount(ReconciliationReport, { global: { plugins: [i18n] } })
    await flushPromises()
    const fresh = providerReport()
    getReportSummaryMock.mockResolvedValue(fresh)
    await applyPreset(wrapper, 'yesterday')
    await flushPromises()
    releaseStale({
      ...providerReport(),
      providers: [{ provider_id: 9, provider_name: 'stale-name', quality_score: 1, totals: totals(1), error_breakdown: {} }],
    })
    await flushPromises()
    expect(wrapper.text()).toContain('prov-alpha')
    expect(wrapper.text()).not.toContain('stale-name')
    wrapper.unmount()
  })

  it('mounts the day table only after the section is expanded', async () => {
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="day-dist"]').exists()).toBe(false)
    await wrapper.get('.el-collapse-item__header').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="day-dist"]').text()).toContain('2026-09-21')
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

  it('uses site-standard presets with last-N-days ending today (2026-11-20 owner 口径)', async () => {
    // 旧契约（T+1 止于昨日的自带预设）已被 owner 图批否决：
    // 「非标准的时间选择组件，并且近7天的日期也不太对」——钉测必须跟着决定翻转，
    // 否则就是把你锁回旧决定的反向锁。
    getReportSummaryMock.mockResolvedValue({ ...providerReport(), snapshot_dates: [], days: [] })
    const wrapper = await mountView()
    expect(wrapper.find('[data-quick]').exists()).toBe(false)
    const picker = wrapper.getComponent(KxDateRangePicker)
    const ids = (picker.props('presets') as { id: string }[]).map((item) => item.id)
    expect(ids).toEqual(['today', 'yesterday', 'last7d', 'last14d', 'last30d', 'thisMonth', 'lastMonth'])
    expect(picker.props('maxSpanDays')).toBe(367)
    // notAfter = 今天：今天可进区间，未来不可选
    // ★ 判据与生产同源（2026-11-20）：日切已改系统时区（时区设置轮），若测试
    //   用 UTC 的 toISOString() 抄日期，则每天 0:00–8:00（CST）生产与判据分属
    //   两个日历日 ⇒ 时间依赖型必红（本轮 08:00 前实测复现）。改引生产同源
    //   systemTzDateStr() ⇒ 期望值与实现同一时区语义。
    const today = systemTzDateStr()
    expect(picker.props('notAfter')).toBe(snapshotNotAfter())
    expect(snapshotNotAfter()).toBe(today)
    const last7 = presetRange('last7d')
    expect(last7.end).toBe(today)
    expect(getReportSummaryMock.mock.calls[0][0]).toEqual(expect.objectContaining({ start: last7.start, end: last7.end }))
    expect(wrapper.text()).toContain(zhCN.reports.noSnapshots)
    // 区间含今天且快照不足 ⇒ 覆盖行披露「今日快照次日凌晨生成」
    expect(wrapper.text()).toContain(zhCN.reports.todayPendingHint)
    await applyPreset(wrapper, 'yesterday')
    const yesterday = presetRange('yesterday')
    expect(getReportSummaryMock).toHaveBeenCalledWith(expect.objectContaining({
      start: yesterday.start,
      end: yesterday.end,
    }))
  })

  it('marks an uncovered zero-fill day and leaves a covered day unmarked', async () => {
    const report = providerReport()
    report.days = [
      ...report.days,
      {
        date: '2026-09-22',
        totals: {
          ...report.days[0].totals,
          request_count: 0,
          success_count: 0,
          error_count: 0,
          error_rate: 0,
          total_tokens: 0,
          credits_charged: 0,
          estimated_cost_cents: 0,
        },
      },
    ]
    getReportSummaryMock.mockResolvedValue(report)
    const wrapper = await mountView()
    await wrapper.get('.el-collapse-item__header').trigger('click')
    await flushPromises()
    const rows = wrapper.get('[data-testid="day-dist"]').findAll('tr.el-table__row').map((row) => row.text())
    expect(rows.find((text) => text.includes('2026-09-21'))).not.toContain('未聚合')
    expect(rows.find((text) => text.includes('2026-09-22'))).toContain('未聚合')
  })
})
