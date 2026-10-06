import { describe, it, expect } from 'vitest'
import { readCostTrend, COST_TREND_DIMENSIONS, type CostTrendResponse } from './usage'

/**
 * 成本趋势的降级语义（2026-10-06）。
 *
 * ★★ 这是本仓**最容易被误读**的一个响应形状，后端原注释写得很直白
 *   （admin/usage_enhanced.go:48-52）：
 *
 *   「这里的降级形状是『可选视图未迁移』⇒ 200 + 空 entries + total_cost 0，
 *     页面上表现为一张空饼图。**若不标记，它与『这段时间真的一分钱没花』同形。**」
 *
 * 后端那行注释还记了一个真实教训：同文件的 PeriodCompare / CacheEconomics
 * 先后加了 degraded 标记，CostTrend 自己漏了 ⇒「本轮已修降级载荷」这句话
 * 对自己文件都不成立。移动端读同一族载荷时要**逐端点核对**它带不带标记。
 *
 * 本组判据把三态钉死，且要求**互斥** —— 把 degraded 显示成「暂无数据」，
 * 就是用一句没有依据的话解释一次系统降级。
 */

function trend(over: Partial<CostTrendResponse> = {}): CostTrendResponse {
  return {
    group_by: 'model',
    date_from: '2026-10-01',
    date_to: '2026-10-07',
    total_cost: 0,
    entries: [],
    other_cost: 0,
    other_count: 0,
    degraded: false,
    ...over,
  }
}

const ENTRY = {
  dimension_value: 'claude-sonnet-4-6',
  request_count: 120,
  total_cost_usd: 3.21,
  input_cost_usd: 1.1,
  output_cost_usd: 2.11,
  prompt_tokens: 1000,
  completion_tokens: 500,
  avg_latency_ms: 820,
  error_rate: 0.02,
  percentage: 100,
}

describe('readCostTrend 三态', () => {
  it('有数据 → ok，且成本可作结论', () => {
    const r = readCostTrend(trend({ entries: [ENTRY], total_cost: 3.21 }))
    expect(r.kind).toBe('ok')
    expect(r.costIsMeaningful).toBe(true)
    expect(r.entries).toHaveLength(1)
    expect(r.totalCost).toBe(3.21)
  })

  it('没降级 + 零条数 → empty（这次是真的没有）', () => {
    const r = readCostTrend(trend())
    expect(r.kind).toBe('empty')
    expect(r.costIsMeaningful).toBe(true)
  })

  it('★ 降级 → degraded，且 total_cost 0 **不可**当真实零花费', () => {
    // 这就是后端 IsMissingRelationError 分支的返回（usage_enhanced.go:211-229）
    const r = readCostTrend(
      trend({ degraded: true, degraded_reason: 'relation "usage_cost_daily" does not exist', total_cost: 0 }),
    )
    expect(r.kind).toBe('degraded')
    // ★ 核心判据：数字照样透传（UI 可能要显示），但明确标记它不可信
    expect(r.costIsMeaningful).toBe(false)
    expect(r.reason).toContain('does not exist')
  })

  it('★ 降级与 empty 互斥：degraded=true 时绝不返回 empty', () => {
    // ★ 反向判据：若 readCostTrend 改成「先判 entries.length===0」，
    //   降级响应会被读成 empty —— 那正是我们要防的误报。
    const degraded = readCostTrend(trend({ degraded: true, degraded_reason: 'missing view' }))
    const empty = readCostTrend(trend())
    expect(degraded.kind).not.toBe(empty.kind)
  })

  it('degraded 与 entries 非空可并存（视图部分迁移），仍以 degraded 为准', () => {
    const r = readCostTrend(trend({ degraded: true, degraded_reason: 'partial', entries: [ENTRY], total_cost: 3.21 }))
    expect(r.kind).toBe('degraded')
    expect(r.costIsMeaningful).toBe(false)
  })

  it('缺 degraded 字段（老后端）按 false 处理，不误判为降级', () => {
    const r = readCostTrend({ ...trend({ entries: [ENTRY], total_cost: 3.21 }), degraded: undefined as never })
    expect(r.kind).toBe('ok')
  })

  it('entries 缺失时返回空数组而不是 undefined（模板 v-for 不炸）', () => {
    const r = readCostTrend({ ...trend(), entries: undefined as never })
    expect(r.entries).toEqual([])
    expect(r.kind).toBe('empty')
  })
})

describe('COST_TREND_DIMENSIONS', () => {
  it('覆盖后端支持的分组维度（usage_enhanced.go planCostTrend）', () => {
    expect(COST_TREND_DIMENSIONS).toContain('model')
    expect(COST_TREND_DIMENSIONS).toContain('provider')
    expect(COST_TREND_DIMENSIONS).toContain('tenant')
  })
  it('model 是后端默认值（:135-138）', () => {
    expect(COST_TREND_DIMENSIONS[0]).toBe('model')
  })
})
