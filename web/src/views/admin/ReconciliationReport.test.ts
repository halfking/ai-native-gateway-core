// ReconciliationReport.test.ts — 对账报表页回归测试（2026-09-28 审计轮补）。
//
// 背景：本仓 main.ts 不全局注册 ElementPlus（也无 unplugin 自动导入），该页
// 曾因模板 el-* 未显式 import，生产构建里 resolveComponent 静默失败、组件
// 退化为未知标签，el-table 列插槽被 normalizeChildren 无参调用，
// `{ row }` 解构 undefined 抛 TypeError，整页白屏（commit 9946d75b5 修复）。
// 本测试在 jsdom 里真实挂载组件（含真实 ElTable），锁定该失败类：
// 若 el-table/el-table-column 再次退化为未解析组件，渲染即抛错、本测试必红。
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import ReconciliationReport from './ReconciliationReport.vue'

const getReportSummaryMock = vi.fn()

vi.mock('../../api/reportrollup', () => ({
  getReportSummary: (...args: unknown[]) => getReportSummaryMock(...(args as [])),
  downloadReportExport: vi.fn(),
  runReportRollup: vi.fn(),
}))

const routeState = reactive<{ query: Record<string, string> }>({ query: {} })
vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: vi.fn() }),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  messages: { 'zh-CN': {} },
})

const totals = (n: number) => ({
  request_count: n,
  success_count: n - 1,
  error_count: 1,
  error_rate: 1 / n,
  input_tokens: 10,
  output_tokens: 20,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  total_tokens: 30,
  estimated_cost_cents: 123,
  currency: 'USD',
  credits_charged: n * 100,
  internal_cost_cents: 456,
  internal_currency: 'CNY',
  cache_hit_ratio: null,
  latency_p50_ms: 100,
  latency_p95_ms: 900,
})

function providerReport() {
  return {
    start: '2026-09-21',
    end: '2026-09-27',
    view: 'provider',
    totals: totals(30),
    error_breakdown: { timeout: 1 },
    days: [{ date: '2026-09-21', totals: totals(30) }],
    providers: [
      { provider_id: 1, provider_name: 'prov-alpha', quality_score: 88.5, totals: totals(20), error_breakdown: { timeout: 1 } },
      { provider_id: 2, provider_name: 'prov-beta', quality_score: 91, totals: totals(10), error_breakdown: {} },
    ],
    models: [{ raw_model_name: 'gpt-test', provider_id: 1, provider_name: 'prov-alpha', totals: totals(30), error_breakdown: {} }],
    snapshot_dates: ['2026-09-21'],
  }
}

function internalReport() {
  return {
    ...providerReport(),
    view: 'internal',
    providers: undefined,
    tenants: [{ tenant_id: 'tenant-x', totals: totals(25), error_breakdown: {} }],
    persons: [{ tenant_id: 'tenant-x', person: 'alice', totals: totals(25), error_breakdown: {} }],
  }
}

function mountView() {
  return mount(ReconciliationReport, { global: { plugins: [i18n] } })
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('ReconciliationReport 挂载渲染', () => {
  it('provider 视角：真实 ElTable 渲染出供应商行（未解析 el-* 时此处必抛错）', async () => {
    routeState.query = {}
    // 注意：getReportSummary 在 api 层内部已解包 res.report，mock 直接给 RangeReport 本体。
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = mountView()
    await flushPromises()

    // 修复前：el-table 未注册 → 列插槽 ({ row }) 解构 undefined → 渲染抛错。
    const tables = wrapper.findAll('.el-table')
    expect(tables.length).toBe(3) // 供应商分组 + 模型 + 按天（internal 视角才加人员表）
    const providerRows = tables[0].findAll('tr.el-table__row')
    expect(providerRows.length).toBe(2) // 供应商表 2 行
    expect(wrapper.text()).toContain('prov-alpha')
    expect(wrapper.text()).toContain('prov-beta')
  })

  it('internal 深链：租户/人员/模型表渲染，GroupRow 收窄不崩', async () => {
    routeState.query = { view: 'internal' }
    getReportSummaryMock.mockResolvedValue(internalReport())
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('tr.el-table__row').length).toBeGreaterThan(0)
    expect(wrapper.text()).toContain('tenant-x')
    expect(wrapper.text()).toContain('alice')
    expect(wrapper.text()).toContain('gpt-test')
  })
})
