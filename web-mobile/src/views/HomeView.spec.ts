import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import HomeView from './HomeView.vue'
import { fetchBoard, type BoardPayload } from '@/api/board'
import { useAuthStore } from '@/stores/auth'

/**
 * HomeView 降级呈现（2026-10-06）。
 *
 * ★ 与 UsageView 是**同一个缺陷的两处副本**，而这处更严重 —— 后端原话
 *   （admin/dashboard_board_queries.go:159-164）：
 *     「数据源 X 不可用，本页所有汇总数字（请求/Token/费用）
 *       **均为 0，不可作为结论**」
 *
 *   移动端原先只在「请求数」「Token」两张卡挂 summaryMissing 提示，其余 6 张
 *   （费用 / 积分 / 成功率 / 延迟 / 活跃 Key / 活跃模型）**照常显示 0**。
 *   ⇒ 主聚合表缺失（42P01）会被读成「今天没人用、花了 0 块、成功率 0%」。
 *
 * 修法：降级时整块声明，**不再渲染那 8 张含无依据 0 的卡**。
 * ★ 反向判据：若改回「照常显示 + 2 张小提示」，本用例会红。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/board', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/board')>()
  return { ...actual, fetchBoard: vi.fn() }
})

vi.mock('@/api/system', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/system')>()
  return { ...actual, ...(actual as object), fetchSystemVersion: vi.fn(async () => ({ version: '1.0.0' })) }
})

function board(over: Partial<BoardPayload> = {}): BoardPayload {
  return {
    summary: {
      total_requests: 0,
      total_prompt_tokens: 0,
      total_completion_tokens: 0,
      total_tokens: 0,
      total_cost_usd: 0,
      total_credits_charged: 0,
      success_rate: 0,
      avg_latency_ms: 0,
      active_api_keys: 0,
      active_models: 0,
      providers: 0,
      degraded_summary: false,
    },
    pies: { models: [], clients: [], providers: [], errors: [] },
    trends: [],
    background_tasks: {},
    ...over,
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
  const w = mount(HomeView, { attachTo: document.body, global: { plugins: [pinia] } })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

describe('HomeView 降级呈现', () => {
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

  it('★ summary 降级时不再渲染那 8 张含 0 的统计卡', async () => {
    ;(fetchBoard as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      board({
        summary: {
          degraded_summary: true,
          summary_missing_view: 'request_stats_minute',
          summary_hint:
            '数据源 request_stats_minute 不可用，本页所有汇总数字（请求/Token/费用）均为 0，不可作为结论',
        },
      }),
    )
    const w = await mountView()

    expect(w.find('.home__degraded').exists()).toBe(true)
    // ★ 统计卡整块不渲染
    expect(w.findAll('.stat-card').length).toBe(0)
    // 后端自带的 hint 要透出来（那是它对「不可作为结论」的原话）
    expect(w.find('.home__degraded-reason').text()).toContain('不可作为结论')
  })

  it('未降级时统计卡照常渲染（别把修法做成「永远不显示」）', async () => {
    ;(fetchBoard as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      board({
        summary: {
          total_requests: 128, total_tokens: 4096, total_cost_usd: 3.5,
          success_rate: 0.98, avg_latency_ms: 820, active_api_keys: 4, active_models: 12,
          degraded_summary: false,
        },
      }),
    )
    const w = await mountView()
    expect(w.findAll('.stat-card').length).toBeGreaterThan(0)
    expect(w.find('.home__degraded').exists()).toBe(false)
  })

  it('★ 积分单独降级（credits_missing_view）时仍显示其余汇总卡，只标积分那张', async () => {
    // 这是**另一种**降级：积分走的是另一个数据源（queryTotalCreditsCharged，
    // dashboard_board_queries.go:136-137），只有积分不可信。
    // ⇒ 绝不能把它也整块降级 —— 那会把「只有积分没数据」说成「整页都没数据」。
    ;(fetchBoard as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      board({
        summary: {
          total_requests: 128, total_tokens: 4096, total_cost_usd: 3.5,
          credits_missing_view: 'credits_ledger', degraded_summary: false,
        },
      }),
    )
    const w = await mountView()
    expect(w.findAll('.stat-card').length).toBeGreaterThan(0)
    expect(w.find('.home__degraded').exists()).toBe(false)
  })
})
