// ReconciliationReport.test.ts — 对账报表页回归测试（2026-09-28 审计轮补，
// 2026-09-30 按「主表可切六维 + 模型清单折叠」重写断言）。
//
// 背景：本仓 main.ts 不全局注册 ElementPlus（也无 unplugin 自动导入），该页
// 曾因模板 el-* 未显式 import，生产构建里 resolveComponent 静默失败、组件
// 退化为未知标签，el-table 列插槽被 normalizeChildren 无参调用，
// `{ row }` 解构 undefined 抛 TypeError，整页白屏（commit 9946d75b5 修复）。
// 本测试在 jsdom 里真实挂载组件（含真实 ElTable），锁定该失败类：
// 若 el-table/el-table-column 再次退化为未解析组件，渲染即抛错、本测试必红。
//
// **mock 必须用 importActual 展开**（2026-09-30 修复的真实故障）：早先这里
// 手写了一个只含 4 个函数的对象字面量 mock，页面后来新增 UNASSIGNED_ID
// 常量与 getReportDimensions 时它没跟着扩，渲染期读 UNASSIGNED_ID 直接抛
// "No UNASSIGNED_ID export is defined on the mock"，**整棵表树被未处理
// rejection 打断**——症状是「表格 0 行 + wrapper.find 返回 null」，报错点
// 离真因十万八千里，看起来像页面回归，实则是 mock 覆盖面 < 生产实现。
// 展开 actual 后，新导出默认走真实现，mock 只覆盖真正要拦的网络调用，
// 这一类故障不会再发生。同目录 realdata.test.ts 早就是这个写法。
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import ReconciliationReport from './ReconciliationReport.vue'
import type { DimensionOptions, RangeReport } from '../../api/reportrollup'

const getReportSummaryMock = vi.fn()
const getReportDimensionsMock = vi.fn()

vi.mock('../../api/reportrollup', async () => {
  const actual = await vi.importActual<typeof import('../../api/reportrollup')>(
    '../../api/reportrollup',
  )
  return {
    ...actual,
    getReportSummary: (...a: unknown[]) => getReportSummaryMock(...a),
    getReportDimensions: (...a: unknown[]) => getReportDimensionsMock(...a),
    downloadReportExport: vi.fn(),
    runReportRollup: vi.fn(),
  }
})

const routeState = reactive<{ query: Record<string, string> }>({ query: {} })
vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: vi.fn() }),
}))

// echarts 必须 mock：jsdom 没有 canvas 包的 getContext，echarts.init 会在
// mount 期抛错，把**整棵树**的挂载打断——表现为 wrapper 拿不到根节点、
// 表格渲染出 0 行，而报错点离真因十万八千里（真实原因在 stderr 里那句
// "Not implemented: HTMLCanvasElement.prototype.getContext"）。
//
// 本文件测的是页面的表格/筛选/分组逻辑，不是画布渲染；realdata 测试已经这么做了。
vi.mock('echarts', () => ({
  init: () => ({ setOption: vi.fn(), dispose: vi.fn(), resize: vi.fn(), on: vi.fn() }),
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

const emptyDims: DimensionOptions = {
  providers: [],
  credentials: [],
  models: [],
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
    error_breakdown: { timeout: 1 },
    days: [{ date: '2026-09-21', totals: totals(30) }],
    providers: [
      { provider_id: 1, provider_name: 'prov-alpha', quality_score: 88.5, totals: totals(20), error_breakdown: { timeout: 1 } },
      { provider_id: 2, provider_name: 'prov-beta', quality_score: 91, totals: totals(10), error_breakdown: {} },
    ],
    credentials: [],
    models: [{ raw_model_name: 'gpt-test', provider_id: 1, provider_name: 'prov-alpha', totals: totals(30), error_breakdown: {} }],
    // 模型统计清单读的是 model_totals（汇总口径），不是按天拆的 daily_models。
    model_totals: [{ raw_model_name: 'gpt-test', totals: totals(30), error_breakdown: {} }],
    tenants: [],
    persons: [],
    api_keys: [],
    snapshot_dates: ['2026-09-21'],
  } as unknown as RangeReport
}

function internalReport(): RangeReport {
  return {
    ...providerReport(),
    view: 'internal',
    providers: [],
    tenants: [{ tenant_id: 'tenant-x', totals: totals(25), error_breakdown: {}, quality_score: 80 }],
    persons: [{ tenant_id: 'tenant-x', person: 'alice', totals: totals(25), error_breakdown: {}, quality_score: 80 }],
  } as unknown as RangeReport
}

async function mountView() {
  const wrapper = mount(ReconciliationReport, { global: { plugins: [i18n] } })
  await flushPromises()
  return wrapper
}

/**
 * 切换某个 el-radio-button（走真实 v-model 链路，而不是直接改组件 ref）。
 *
 * 为什么点 input 而不是点可见的 `.el-radio-button__inner`：ElRadioButton 把
 * change 监听挂在内部 `<input class="el-radio-button__original-radio">` 上，
 * 真实浏览器靠 label 的激活行为把点击转发给 input；jsdom 在
 * `trigger('click')` 派发的合成事件上不跑这条激活行为，点了没反应。
 * 直接置 checked 再派发 change，触发的是同一段组件内部逻辑。
 */
async function clickRadioButton(wrapper: Awaited<ReturnType<typeof mountView>>, value: string) {
  const input = wrapper
    .findAll<HTMLInputElement>('input.el-radio-button__original-radio')
    .find((el) => el.element.value === value)
  expect(input, `找不到值为 ${value} 的单选按钮`).toBeTruthy()
  input!.element.checked = true
  await input!.trigger('change')
  await flushPromises()
}

const switchGroup = clickRadioButton

beforeEach(() => {
  vi.clearAllMocks()
  getReportDimensionsMock.mockResolvedValue(emptyDims)
})

describe('ReconciliationReport 挂载渲染', () => {
  it('provider 视角：真实 ElTable 渲染出供应商行（未解析 el-* 时此处必抛错）', async () => {
    routeState.query = {}
    // 注意：getReportSummary 在 api 层内部已解包 res.report，mock 直接给 RangeReport 本体。
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()

    // 修复前：el-table 未注册 → 列插槽 ({ row }) 解构 undefined → 渲染抛错。
    // 汇总模式 = 主表（当前维度）+ 按天表；模型清单折叠未展开 → 不挂载。
    const tables = wrapper.findAll('.el-table')
    expect(tables.length).toBe(2)
    expect(tables[0].findAll('tr.el-table__row').length).toBe(2) // 供应商表 2 行
    expect(wrapper.text()).toContain('prov-alpha')
    expect(wrapper.text()).toContain('prov-beta')
  })

  it('主表可切六维：切到租户/用户维度后渲染对应行（不是靠改 ref 绕过交互）', async () => {
    routeState.query = { view: 'internal' }
    getReportSummaryMock.mockResolvedValue(internalReport())
    const wrapper = await mountView()

    // 默认按供应商分组，而 internal 视角没有 providers → 0 行（这是正确行为）。
    expect(wrapper.findAll('.el-table')[0].findAll('tr.el-table__row').length).toBe(0)

    await switchGroup(wrapper, 'tenant')
    expect(wrapper.findAll('.el-table')[0].findAll('tr.el-table__row').length).toBe(1)
    expect(wrapper.text()).toContain('tenant-x')

    await switchGroup(wrapper, 'person')
    expect(wrapper.findAll('.el-table')[0].findAll('tr.el-table__row').length).toBe(1)
    expect(wrapper.text()).toContain('alice')
  })

  it('模型统计清单：默认**不挂载**，点击展开后才渲染（500+ 行表不许常驻 DOM）', async () => {
    routeState.query = {}
    getReportSummaryMock.mockResolvedValue(providerReport())
    const wrapper = await mountView()

    // 折叠前 DOM 里找不到 gpt-test —— v-if 按需挂载，不是 el-collapse 的视觉收起。
    expect(wrapper.text()).not.toContain('gpt-test')

    const header = wrapper.find('.el-collapse-item__header')
    expect(header.exists()).toBe(true)
    await header.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('gpt-test')
    expect(wrapper.findAll('.el-table').length).toBe(3) // 主表 + 按天 + 模型清单
  })

  it('汇总 ⇄ 按天明细：切明细后主表换成按天 × 当前维度，按天表收起', async () => {
    routeState.query = {}
    getReportSummaryMock.mockResolvedValue({
      ...providerReport(),
      daily_providers: [
        { date: '2026-09-21', key: '1', name: 'prov-alpha', totals: totals(20), error_breakdown: {} },
        { date: '2026-09-22', key: '1', name: 'prov-alpha', totals: totals(20), error_breakdown: {} },
      ],
    } as unknown as RangeReport)
    const wrapper = await mountView()
    expect(wrapper.findAll('.el-table').length).toBe(2)

    const detailBtn = wrapper
      .findAll<HTMLInputElement>('input.el-radio-button__original-radio')
      .find((el) => el.element.value === 'true')
    expect(detailBtn, '缺少「按天明细」切换按钮').toBeTruthy()
    await detailBtn!.setValue(true)
    await flushPromises()

    // 主表换成按天行；按天汇总表 v-if="!detail" 收起 → 总表数仍为 2。
    expect(wrapper.findAll('.el-table')[0].findAll('tr.el-table__row').length).toBe(2)
    expect(wrapper.text()).toContain('2026-09-22')
  })
})
