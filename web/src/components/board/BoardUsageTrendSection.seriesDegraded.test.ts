// BoardUsageTrendSection.seriesDegraded.test.ts
//   —— 趋势序列降级不得画成「零用量」
//
// ## 挡住的是什么（这是 T1 点名文件里**唯一没闭环**的一处）
//
// `admin/usage_trend_series.go` 的两个端点在 42P01 时返回
// `degraded` / `missing_view` / `hint`（T1 第一批就加了，恒发、无 omitempty），
// `web/src/api/usage.ts` 的类型里也声明了。
//
// 但 `BoardUsageTrendSection.vue` 的 `loadSeries()` 只读：
//
//     series.value = resp.series ?? []      // ← 降级时空数组被当真值
//
// ⇒ 图表画出一条零线，用户读到「这段时间一点用量都没有」，
// 而真相是「聚合视图没迁移，没算出来」。
//
// **这就是「4/5」里差的那一个**：另外四个点名文件
// （dashboard_board_queries / dashboard_board_aux / usage / usage_credits）
// 的降级标记都到了消费者，只有 trend-series 的标记**没有**。
// 它之所以躲过所有既有判据：服务端是对的、前端类型是对的、
// 组件不报错、图表照常渲染 —— 从任何一侧看都自洽。
//
// ## 三态
//
// ① 降级（200 + degraded） → 显示降级说明，**不含**加载失败文案
// ② 成功且有序列         → 渲染图表，不显示任何降级/失败提示
// ③ 成功但真为空         → 显示图表空态，**不**显示降级说明
//                            （防反向 bug：把「真的零用量」也讲成「没算出来」）
// ④ 请求失败（HTTP 错误） → 显示加载失败，**不**显示降级说明
//                            （降级与失败是两件事，排查方向不同）
//
// ## 反向对照（实测，变异先断言确实生效）
//
// · seriesDegraded 恒 false        → ① 红
// · 删模板降级分支、保留状态计算   → ① 红（有状态没消费者）
// · 降级判定恒真                   → ② ③ ④ 红（防恒绿）
// · 从「degraded 判定」退回长度推断（`series.length === 0`）
//   → ③ 必定红：那正是 T1 要挡的形状

import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { createI18n } from 'vue-i18n'

const getUsageTrendSeries = vi.fn()

// mock 目标必须是组件真正 import 的模块（§28.5 教训 #1）：
// 组件第 12 行 `from '../../api/usage'`。
vi.mock('../../api/usage', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../../api/usage')
  return { ...actual, getUsageTrendSeries: (...a: unknown[]) => getUsageTrendSeries(...a) }
})

import BoardUsageTrendSection from './BoardUsageTrendSection.vue'

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      usageTrend: {
        loadFailed: '趋势数据加载失败',
        degraded: '趋势数据不可用（视图 {view} 未初始化）。下方的平线表示「没算出来」，而不是零用量。',
      },
      dashboard: {
        board: {
          trendTitle: '用量趋势',
          metricRequests: '请求',
          metricTokens: 'Token',
          metricCredits: '积分',
          metricCost: '成本',
          granHours: '{n} 小时粒度',
          granMinutes: '{n} 分钟粒度',
          allPage: '全页分析',
        },
      },
      common: { button: { close: '关闭' } },
    },
  },
})

const timeRange = { preset: '7d', startMs: 0, endMs: 0, key: '7d' } as never

async function mountSection() {
  const w = mount(BoardUsageTrendSection, {
    props: { board: null, timeRange },
    global: { plugins: [i18n], stubs: { 'el-radio-group': true, 'el-radio-button': true } },
  })
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  getUsageTrendSeries.mockReset()
})

describe('趋势序列降级四态', () => {
  it('① 降级（200 + degraded）→ 显示降级说明，不是「零用量」', async () => {
    getUsageTrendSeries.mockResolvedValue({
      series: [],
      bucket_minutes: 60,
      degraded: true,
      missing_view: 'request_stats_dim_minute',
    })

    const w = await mountSection()
    const alert = w.find('.trend-sec__degraded')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('request_stats_dim_minute')
    // 降级 ≠ 请求失败：两者必须能分辨
    expect(w.find('.trend-sec__err').exists()).toBe(false)
  })

  it('② 成功且有序列 → 渲染图表，无任何降级/失败提示', async () => {
    getUsageTrendSeries.mockResolvedValue({
      series: [{ model: 'gpt-4o', points: [{ bucket: '2026-10-01T00:00:00Z', requests: 5, tokens: 100, credits: 0, cost_usd: 0.1 }] }],
      bucket_minutes: 60,
      degraded: false,
    })

    const w = await mountSection()
    expect(w.find('.trend-sec__degraded').exists()).toBe(false)
    expect(w.find('.trend-sec__err').exists()).toBe(false)
  })

  it('③ 成功但真为空 → 不显示降级说明（防把「真的零用量」讲成「没算出来」）', async () => {
    getUsageTrendSeries.mockResolvedValue({ series: [], bucket_minutes: 60, degraded: false })

    const w = await mountSection()
    expect(w.find('.trend-sec__degraded').exists()).toBe(false)
    expect(w.find('.trend-sec__err').exists()).toBe(false)
  })

  it('④ 请求失败 → 显示加载失败，不显示降级说明', async () => {
    getUsageTrendSeries.mockRejectedValue(new Error('boom'))

    const w = await mountSection()
    expect(w.find('.trend-sec__err').exists()).toBe(true)
    expect(w.find('.trend-sec__degraded').exists()).toBe(false)
  })
})
