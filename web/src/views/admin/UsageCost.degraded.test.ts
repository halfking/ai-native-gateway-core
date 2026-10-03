// UsageCost.degraded.test.ts —— 降级横幅的渲染判据
//
// 为什么必须有这条：横幅的行为此前只被纯函数单测覆盖，而「纯函数对」与
// 「页面真的显示了」在报告上长得一样 —— 这正是本轮反复出现的那类误判。
// 2026-10-03 的事故是「200 + 全 0 被当成真实测量值」，如果只测合并逻辑、
// 不测渲染，插错模板条件（v-if 写反、变量名拼错、挂在 v-else 分支里）
// 不会有任何一条测试变红。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const getCostTrendMock = vi.fn()
const getPeriodCompareMock = vi.fn()
const getCacheEconomicsMock = vi.fn()

vi.mock('../../api/usage', () => ({
  getCostTrend: (...a: unknown[]) => getCostTrendMock(...a),
  getPeriodCompare: (...a: unknown[]) => getPeriodCompareMock(...a),
  getCacheEconomics: (...a: unknown[]) => getCacheEconomicsMock(...a),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'en',
  messages: {
    'zh-CN': {
      dataLifecycle: {
        usageCost: {
          degraded: {
            title: '部分指标不可用：',
            hint: '这些数字是占位，不是真实测量值。',
          },
          compare: { title: '成本对比' },
          cache: { title: '缓存经济学' },
          errors: { costTrend: 'x', periodCompare: 'x', cacheEconomics: 'x' },
          loading: '加载中…',
        },
      },
    },
  },
})

// 造一份最小可用的响应；degraded 由各用例决定。
const periodPayload = (degraded: boolean, reason?: string) => ({
  current: { period: '2026-09', total_cost_usd: 0, total_requests: 0, total_tokens: 0, avg_cost_per_req: 0, unique_models: 0 },
  previous: { period: '2026-08', total_cost_usd: 0, total_requests: 0, total_tokens: 0, avg_cost_per_req: 0, unique_models: 0 },
  change_pct: 0, change_abs: 0, trend: 'flat' as const, significant: false, by_dimension: {},
  degraded,
  ...(reason ? { degraded_reason: reason } : {}),
})

const cachePayload = (degraded: boolean, reason?: string) => ({
  date_from: '2026-09-03', date_to: '2026-10-03', total_requests: 0, cache_read_tokens: 0,
  prompt_tokens: 0, cache_hit_ratio: 0, dollars_saved: 0, dollars_spent: 0,
  effective_cost_ratio: 0, compressed_requests: 0, compression_saved: 0, total_saved: 0,
  savings_rate: 0, degraded,
  ...(reason ? { degraded_reason: reason } : {}),
})

async function renderPage() {
  const UsageCost = (await import('./UsageCost.vue')).default
  const w = mount(UsageCost, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

describe('UsageCost 降级横幅', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getCostTrendMock.mockResolvedValue({
      group_by: 'model', date_from: '', date_to: '', total_cost: 0,
      entries: [], other_cost: 0, other_count: 0,
    })
  })

  it('服务端说 degraded:true → 必须显示横幅并写明原因', async () => {
    getPeriodCompareMock.mockResolvedValue(periodPayload(true, 'missing column: usage_ledger.gw_session_id'))
    getCacheEconomicsMock.mockResolvedValue(cachePayload(false))

    const w = await renderPage()
    const banner = w.find('.alert-warning')
    expect(banner.exists()).toBe(true)
    // 鉴别力：只断言「有横幅」不够 —— 一个写死的横幅也会通过。
    // 必须断言**原因真的被透出到 DOM**，那是这条测试的承重点。
    expect(banner.text()).toContain('missing column: usage_ledger.gw_session_id')
    expect(banner.text()).toContain('成本对比')
  })

  it('反向对照：两个源都健康 → 不得出现降级横幅', async () => {
    getPeriodCompareMock.mockResolvedValue(periodPayload(false))
    getCacheEconomicsMock.mockResolvedValue(cachePayload(false))

    const w = await renderPage()
    expect(w.find('.alert-warning').exists()).toBe(false)
  })

  it('反向对照：老服务端没有 degraded 字段 → 不得出现横幅（缺字段≠降级）', async () => {
    // 刻意剥掉 degraded 字段，模拟未升级的后端。
    getPeriodCompareMock.mockResolvedValue({ ...periodPayload(false) } as never)
    delete (getPeriodCompareMock.mock.results[0]?.value as Record<string, unknown>)?.degraded
    getCacheEconomicsMock.mockResolvedValue(cachePayload(false))

    const w = await renderPage()
    expect(w.find('.alert-warning').exists()).toBe(false)
  })

  it('两个源同时降级 → 两条原因都列出（用户要知道是哪些指标不可信）', async () => {
    getPeriodCompareMock.mockResolvedValue(periodPayload(true, 'r1'))
    getCacheEconomicsMock.mockResolvedValue(cachePayload(true, 'r2'))

    const w = await renderPage()
    const banner = w.find('.alert-warning')
    expect(banner.text()).toContain('r1')
    expect(banner.text()).toContain('r2')
  })
})
