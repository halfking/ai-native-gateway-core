// BoardDistGrid.drillFailed.test.ts —— 错误下钻失败不得显示成「没有错误」
//
// ## 挡住的是什么
//
// 用户点开某个错误类型的「下钻」面板，意图是看这个错误由哪些模型/供应商造成。
// 修前（2026-10-03）：
//
//     try   { errorDrillItems.value = res.items }
//     catch { errorDrillItems.value = [] }        // ← 静默
//
// 后端 `handleDashboardBoardErrorDrill` 失败时写 500 + `error.detail`
// （writeInternalErr），前端把失败丢成空数组，面板于是显示
// `dashboard.board.empty`（「暂无数据」）。
//
// **用户点开「错误下钻」是为了看错误，屏幕上写的是「没有错误」** ——
// 而真相是「这次查询失败了」。这不是「少一个提示」，
// 是把一次失败陈述成了一个相反的事实。
//
// 与本轮 pie 降级（boardPieDegradation）、credits 降级显示 0、
// ProbeHealthDetailView 的 `text="{{ … }}"` 文字面量是同一族：
// **把「不知道」渲染成「知道」比不显示更糟。**
//
// ## 三态
//
// ① 请求失败      → 出现失败态，且**不含**「暂无数据」文案
// ② 成功且有数据  → 渲染行，不出现失败态
// ③ 成功但为空    → 显示「暂无数据」，**不**出现失败态
//                  （这一条防的是反向 bug：把「真的没有」也讲成「失败」）
//
// ## 反向对照（全部实测，变异先断言确实发生）
//
// · 退回 `catch { errorDrillItems.value = [] }`  → ① 红
// · 删模板失败态分支、只留 error 状态           → ① 红（有状态没消费者）
// · 失败态判定恒真（`v-else-if="true"`）        → ② ③ 红（防恒绿）

import { mount } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createI18n } from 'vue-i18n'

const fetchBoardErrorDrill = vi.fn()
vi.mock('../../api/board', async () => {
  const actual = await vi.importActual<typeof import('../../api/board')>('../../api/board')
  return { ...actual, fetchBoardErrorDrill: (...a: unknown[]) => fetchBoardErrorDrill(...a) }
})

import BoardDistGrid from './BoardDistGrid.vue'

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      dashboard: {
        loading: '加载中',
        board: {
          distTitle: '分布分析',
          metricToggle: '指标',
          metricRequests: '请求',
          metricTokens: 'Token',
          distTop: 'Top {n}',
          distOthers: '其他 {n}',
          distDrillHint: '点击下钻',
          empty: '暂无数据',
          pieClients: '客户端类型',
          pieErrors: '错误类型',
          pieIdentity: '身份指纹',
          pieTenants: '租户用量',
          pieClientIp: '来源 IP',
          errorDrill: '错误下钻 {kind}',
          drillModel: '模型',
          drillProvider: '供应商',
          drillClient: '客户端',
          pieDegraded: '不可用 —— {reason}。此处的空排名表示「没算出来」，而不是「没有数据」。',
          pieDegradedGeneric: '该排行查询失败',
          drillFailed: '无法加载该错误构成 —— {reason}。此处显示为空不代表真的没有这类错误。',
        },
        table: { colModel: '模型' },
        providerUsage: { colRequests: '请求' },
        v2: { totalTokensShort: 'Token' },
      },
      common: { button: { close: '关闭' } },
    },
  },
})

const board = (errors: any[] = [{ key: 'rate_limit', requests: 3, tokens: 10, credits: 0, cost_usd: 0 }]) => ({
  summary: { total_requests: 10 },
  pies: {
    clients: [], client_ips: [], identity_hashes: [], models: [],
    errors, tenants: [], providers: [],
  },
  trends: [],
  days: 7,
  degraded: false,
  degraded_pies: {},
  degraded_trends: false,
})

async function openDrill(b: any) {
  const w = mount(BoardDistGrid, { props: { board: b, days: 7 }, global: { plugins: [i18n] } })
  // 点错误排行榜的第一行 → 打开下钻面板并发起请求
  await w.find('.rank-row').trigger('click')
  await new Promise((r) => setTimeout(r, 0))
  await w.vm.$nextTick()
  return w
}

beforeEach(() => {
  fetchBoardErrorDrill.mockReset()
})

describe('错误下钻三态', () => {
  it('① 请求失败 → 显示失败原因，不显示「暂无数据」', async () => {
    const err: any = new Error('internal error (see server logs)')
    err.detail = 'internal error (see server logs)'
    fetchBoardErrorDrill.mockRejectedValue(err)

    const w = await openDrill(board())

    const failEl = w.find('.drill-panel__error')
    expect(failEl.exists()).toBe(true)
    // 关键：失败态不能同时出现下钻面板的「暂无数据」——那正是要挡的反向陈述。
    //
    // ⚠ 必须**按面板**断言，不能断言整页：其余 4 个空维度卡本来就在显示
    // 「暂无数据」（那是对的）。写整页断言时本用例红了，
    // 探针显示失败文案其实渲染正常 —— 又一次「红的是判据，不是产品」。
    // 这与本轮 pie 判据那次是同一个坑，隔了几个小时又踩一遍。
    const panel = w.find('.drill-panel')
    expect(panel.text()).not.toContain('暂无数据')
    // 失败面板里必须带出服务端给的原因，而不是一句「加载失败」。
    expect(failEl.text()).toContain('internal error')
  })

  it('② 成功且有数据 → 渲染行，不出现失败态', async () => {
    fetchBoardErrorDrill.mockResolvedValue({
      items: [{ key: 'gpt-4o', requests: 7, tokens: 100, credits: 0, cost_usd: 0.2 }],
    })

    const w = await openDrill(board())

    expect(w.find('.drill-panel__error').exists()).toBe(false)
    expect(w.findAll('.drill-row').length).toBeGreaterThan(0)
    expect(w.text()).toContain('gpt-4o')
  })

  it('③ 成功但为空 → 显示「暂无数据」，不出现失败态', async () => {
    fetchBoardErrorDrill.mockResolvedValue({ items: [] })

    const w = await openDrill(board())

    expect(w.find('.drill-panel__error').exists()).toBe(false)
    expect(w.text()).toContain('暂无数据')
  })

  it('④ 切维度后重试失败，不显示上一次的旧行', async () => {
    fetchBoardErrorDrill.mockResolvedValueOnce({
      items: [{ key: 'stale-row', requests: 1, tokens: 1, credits: 0, cost_usd: 0 }],
    })
    const w = await openDrill(board())
    expect(w.text()).toContain('stale-row')

    // 第二次点击失败：面板必须换成失败态，不能继续显示 stale-row
    const err: any = new Error('boom')
    err.detail = 'board drill cache: boom'
    fetchBoardErrorDrill.mockRejectedValueOnce(err)
    await w.find('.rank-row').trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    await w.vm.$nextTick()

    expect(w.find('.drill-panel__error').exists()).toBe(true)
    // 旧行必须消失：不清 items 时失败会被上一次的数据掩盖
    // （同样按面板断言，整页含排行榜区的同名 key）
    expect(w.find('.drill-panel').text()).not.toContain('stale-row')
  })
})
