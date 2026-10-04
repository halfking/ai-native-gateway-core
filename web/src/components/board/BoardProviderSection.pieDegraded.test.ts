// BoardProviderSection.pieDegraded.test.ts —— providers 维度降级不得显示成「暂无数据」
//
// ## 挡住的是什么
//
// 2026-10-03 做维度对账时发现的遗漏：服务端 `fallbackBoardPies` 产出
// 7 个饼图维度，前 6 个都接了 `isBoardPieDegraded`，唯独 `providers`
// 这一格渲染在 `BoardProviderSection.vue`（不在 BoardDistGrid），漏了。
//
// 降级时 `pv-empty` 显示 `dashboard.board.empty`（「暂无数据」）——
// 与「这段时间真的没有供应商用量」同形。
//
// 为什么**所有既有判据都是绿的**：服务端诚实降级了，前 6 格正常，
// 这一格少一个分支。它只能靠**跨语言维度对账**发现
// （见 boardPieDimensionsParity.test.ts）。
// 本文件负责另一半：证明那个分支真的会渲染。
//
// ## 反向对照（实测，变异先断言确实生效）
//
// · providersDegraded 恒 false  → ① 红
// · 删模板分支、保留计算        → ① 红（有状态没消费者）
// · 降级判定恒真                → ② 红（防恒绿：把「真的没有」也报成降级）

import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createI18n } from 'vue-i18n'

const getUsageByProvider = vi.fn()
const getReportSummary = vi.fn()
const getProviderCredentials = vi.fn()

// mock 目标必须是组件**真正 import 的模块**（§28.5 的教训 #1）。
// 组件第 14-17 行分别从 api/usage、api/reportrollup、api/providers 取数，
// 挂在桶文件 '../api' 上不会生效。
vi.mock('../../api/usage', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../../api/usage')
  return { ...actual, getUsageByProvider: (...a: unknown[]) => getUsageByProvider(...a) }
})
vi.mock('../../api/reportrollup', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../../api/reportrollup')
  return { ...actual, getReportSummary: (...a: unknown[]) => getReportSummary(...a) }
})
vi.mock('../../api/providers', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../../api/providers')
  return { ...actual, getProviderCredentials: (...a: unknown[]) => getProviderCredentials(...a) }
})

import BoardProviderSection from './BoardProviderSection.vue'

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
        loadError: '加载失败',
        board: {
          empty: '暂无数据',
          pieDegraded: '不可用 —— {reason}。此处的空排名表示「没算出来」，而不是「没有数据」。',
          pieDegradedGeneric: '该排行查询失败',
          providerCostTitle: '供应商成本',
          providerCount: '{n} 个',
          providerCredits: '积分',
          providerBalance: '余额',
          providerPlan: '套餐',
          providerScore: '评分',
          providerWindowCost: '窗口成本',
          providerDetail: '详情',
          providerExport: '导出',
          providerStatsTitle: '供应商用量',
          providerStatsHint: '随看板时间范围联动',
        },
        providerUsage: {
          colCode: '编码', colName: '名称', colRequests: '请求',
          colTokens: 'Token', colCost: '成本', colSuccess: '成功', more: '更多',
        },
      },
    },
  },
})

const timeRange = { start: 0, end: 0 } as never
const timeQuery = {} as never

function board(degradedPies: Record<string, unknown>) {
  return {
    summary: {},
    pies: { clients: [], client_ips: [], identity_hashes: [], models: [], errors: [], tenants: [], providers: [] },
    trends: [],
    days: 7,
    degraded: Boolean(degradedPies.dimensions && (degradedPies.dimensions as string[]).length),
    degraded_pies: degradedPies,
    degraded_trends: false,
  }
}

async function mountSection(b: unknown) {
  const w = mount(BoardProviderSection, {
    props: { board: b as never, timeRange, timeQuery },
    global: { plugins: [i18n], stubs: { RouterLink: true } },
  })
  await flushPromises()
  return w
}

beforeEach(() => {
  getUsageByProvider.mockReset()
  getReportSummary.mockReset()
  getProviderCredentials.mockReset()
  // ⚠ 形状要照抄消费点（第 102 行读的是 res.items —— 另一会话 2026-10-03
  // 把后端改成了 degradedList 信封，裸数组会得到 res.items === undefined，
  // 下一行 loadBalances 的 .filter 直接抛 —— 报错出现在**取数之后**，
  // 而不是渲染期，这种「形状错位」在 §28.5 记过。
  getUsageByProvider.mockResolvedValue({ items: [], degraded: false })
  getReportSummary.mockResolvedValue({ providers: [] })
  getProviderCredentials.mockResolvedValue([])
})

describe('providers 维度降级', () => {
  it('① 降级 → 显示原因，不显示「暂无数据」', async () => {
    const w = await mountSection(
      board({ dimensions: ['providers'], reason: 'board pie dimension unavailable: missing view v_dim' }),
    )
    const alert = w.find('.pv-empty[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('v_dim')

    // ⚠ 必须断言**这一格**的 class，不能对整页做「不含暂无数据」——
    // 用量表自己的空态也用 .pv-empty，写整页断言会误报。
    // 这已是本会话第三次踩「整页级断言把同页其它元素的正常文案
    // 当成本元素的缺陷」（§21、§24.4 各一次）。
    const empties = w.findAll('.pv-empty').filter((e) => e.text().includes('暂无数据'))
    // 降级格不再是「暂无数据」那一支；表格空态仍是（它确实是空的）。
    expect(alert.text()).not.toContain('暂无数据')
    expect(empties.every((e) => e.attributes('role') !== 'alert')).toBe(true)
  })

  it('② 真的没有供应商用量 → 仍显示「暂无数据」（防恒绿）', async () => {
    const w = await mountSection(board({}))
    expect(w.text()).toContain('暂无数据')
    // ⚠ 反向对照（降级判定恒真）之所以**一度逃过**，原因就是只断言
    // `w.text()).toContain('暂无数据')`：用量表的空态本来就提供这句话，
    // 与供应商区那三格无关。
    // 必须断言「**降级告警格**不存在」才算真的防住了恒绿。
    expect(w.find('.pv-empty[role="alert"]').exists()).toBe(false)
  })

  it('③ 其它维度降级（不含 providers）→ 本区仍走正常空态', async () => {
    // 判据必须按维度名判：clients 降级不该让供应商区也报降级。
    const w = await mountSection(
      board({ dimensions: ['clients'], reason: 'query failed' }),
    )
    expect(w.text()).toContain('暂无数据')
    expect(w.find('.pv-empty[role="alert"]').exists()).toBe(false)
  })
})
