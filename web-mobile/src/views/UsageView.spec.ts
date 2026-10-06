import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import UsageView from './UsageView.vue'
import { fetchUsageSummary, fetchUsageByModel, fetchCostTrend } from '@/api/usage'
import { useAuthStore } from '@/stores/auth'

/**
 * UsageView 降级呈现（2026-10-06）。
 *
 * ★ 这条判据针对一个**已存在的缺陷**，不是新功能：
 *   summary 降级时，原实现只在「请求数」一张卡的 hint 上提示，其余 5 张卡
 *   （tokens / cost / credits / 成功率 / 延迟）**照常显示 0**。
 *   ⇒ 后端可选视图缺失，被读成「这段时间零调用、一分钱没花」。
 *
 *   修法：降级时**不再展示那些无依据的 0**，改为整块声明。
 *   ★ 反向判据：若改回「照常显示 0 + 一句小提示」，本用例会红。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/usage', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/usage')>()
  return {
    ...actual,
    fetchUsageSummary: vi.fn(),
    fetchUsageByModel: vi.fn(),
    fetchCostTrend: vi.fn(),
  }
})

function summary(over = {}) {
  return {
    total_requests: 0, total_prompt_tokens: 0, total_completion_tokens: 0,
    total_cost_usd: 0, avg_latency_ms: 0, success_rate: 0,
    degraded: false, ...over,
  }
}

let mounted: Array<{ unmount(): void }> = []

async function mountView() {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().userInfo = {
    id: 1, tenant_id: 'default', username: 'u', display_name: 'U',
    email: 'e', role: 'admin', enabled: true,
  }
  const w = mount(UsageView, { attachTo: document.body, global: { plugins: [pinia] } })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

describe('UsageView 降级呈现', () => {
  beforeEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
    document.body.innerHTML = ''
    vi.resetAllMocks()
  })
  afterEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
  })

  it('★ summary 降级时不再渲染那 6 张含 0 的统计卡', async () => {
    ;(fetchUsageSummary as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      summary({ degraded: true, hint: 'usage_summary_daily 缺失' }),
    )
    ;(fetchUsageByModel as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ items: [], degraded: false })
    const w = await mountView()

    // 降级说明必须在
    expect(w.text()).toContain('summary')
    // ★ 统计卡整块不渲染 —— 原实现在这里会显示 6 张全 0 的卡
    expect(w.findAll('.stat-card').length).toBe(0)
  })

  it('未降级时统计卡照常渲染（别把修法做成「永远不显示」）', async () => {
    ;(fetchUsageSummary as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      summary({ total_requests: 42, total_cost_usd: 1.5, degraded: false }),
    )
    ;(fetchUsageByModel as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ items: [], degraded: false })
    const w = await mountView()
    expect(w.findAll('.stat-card').length).toBeGreaterThan(0)
  })

  it('★ 成本趋势：降级响应显示降级说明，且不显示「总成本 $0」', async () => {
    ;(fetchUsageSummary as ReturnType<typeof vi.fn>).mockResolvedValueOnce(summary())
    ;(fetchUsageByModel as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ items: [], degraded: false })
    ;(fetchCostTrend as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      group_by: 'model', date_from: '2026-10-01', date_to: '2026-10-07',
      total_cost: 0, entries: [], other_cost: 0, other_count: 0,
      degraded: true, degraded_reason: 'relation "usage_cost_daily" does not exist',
    })
    const w = await mountView()

    const tab = w.findAll('.usage__tab').find((t) => t.text().includes('Cost trend'))
    if (!tab) throw new Error('成本趋势 Tab 不存在')
    await tab.trigger('click')
    await flushPromises()
    await flushPromises()

    const text = w.text()
    expect(text).toContain('cost view is not ready')
    expect(text).toContain('usage_cost_daily')
    // ★ 绝不能把降级说成「本期没有成本记录」或显示「总成本 $0.0000」
    expect(text).not.toContain('No cost records')
    expect(w.find('.usage__cost-total').exists()).toBe(false)
  })

  it('成本趋势：真空（未降级且零条数）才显示「本期没有成本记录」', async () => {
    ;(fetchUsageSummary as ReturnType<typeof vi.fn>).mockResolvedValueOnce(summary())
    ;(fetchUsageByModel as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ items: [], degraded: false })
    ;(fetchCostTrend as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      group_by: 'model', date_from: '2026-10-01', date_to: '2026-10-07',
      total_cost: 0, entries: [], other_cost: 0, other_count: 0, degraded: false,
    })
    const w = await mountView()
    const tab = w.findAll('.usage__tab').find((t) => t.text().includes('Cost trend'))
    if (!tab) throw new Error('成本趋势 Tab 不存在')
    await tab.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(w.text()).toContain('No cost records')
    expect(w.text()).not.toContain('cost view is not ready')
  })
})
