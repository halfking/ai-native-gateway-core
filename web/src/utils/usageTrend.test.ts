// usageTrend.test.ts — 按模型趋势透视纯函数单测（2026-10-02 看板轮）。
import { describe, expect, it } from 'vitest'
import {
  USAGE_TREND_METRICS,
  compactTickValue,
  pivotUsageTrendSeries,
  usageTrendMetricLabelKey,
  usageTrendPointValue,
  usageTrendTotals,
} from './usageTrend'
import type { UsageTrendModelSeries } from '../api/usage'

const series: UsageTrendModelSeries[] = [
  {
    model: 'model-a',
    total_requests: 10,
    total_tokens: 100,
    total_credits: 20,
    total_cost_usd: 0.5,
    points: [
      { bucket: '2026-10-02T00:00:00Z', requests: 3, tokens: 30, credits: 6, cost_usd: 0.15 },
      { bucket: '2026-10-02T00:15:00Z', requests: 7, tokens: 70, credits: 14, cost_usd: 0.35 },
    ],
  },
  {
    model: 'model-b',
    total_requests: 5,
    total_tokens: 50,
    total_credits: 10,
    total_cost_usd: 0.25,
    points: [
      // 缺第一个桶：透视必须补 0 对齐。
      { bucket: '2026-10-02T00:15:00Z', requests: 5, tokens: 50, credits: 10, cost_usd: 0.25 },
    ],
  },
]

describe('usageTrendMetricLabelKey', () => {
  it('maps all four metrics to board locale keys', () => {
    expect(usageTrendMetricLabelKey('requests')).toBe('dashboard.board.trendRequests')
    expect(usageTrendMetricLabelKey('tokens')).toBe('dashboard.board.trendTokens')
    expect(usageTrendMetricLabelKey('credits')).toBe('dashboard.board.trendCredits')
    expect(usageTrendMetricLabelKey('cost')).toBe('dashboard.board.trendCost')
    expect(USAGE_TREND_METRICS).toHaveLength(4)
  })
})

describe('usageTrendPointValue', () => {
  it('selects the metric column', () => {
    const p = series[0].points[0]
    expect(usageTrendPointValue(p, 'requests')).toBe(3)
    expect(usageTrendPointValue(p, 'tokens')).toBe(30)
    expect(usageTrendPointValue(p, 'credits')).toBe(6)
    expect(usageTrendPointValue(p, 'cost')).toBeCloseTo(0.15)
  })
})

describe('pivotUsageTrendSeries', () => {
  it('aligns all series onto the bucket union, zero-filling gaps', () => {
    const pivot = pivotUsageTrendSeries(series, 'requests', 15)
    // 标签走本地时区渲染（与旧 TrendLineChart 同惯例），断言形状与时区无关。
    expect(pivot.labels).toHaveLength(2)
    expect(pivot.labels[0]).toMatch(/^\d+\/\d+ \d{2}:\d{2}$/)
    expect(pivot.rows).toHaveLength(2)
    expect(pivot.rows[0]).toEqual({ model: 'model-a', data: [3, 7] })
    expect(pivot.rows[1]).toEqual({ model: 'model-b', data: [0, 5] })
  })

  it('keeps input series order (backend pre-sorted, __others__ last)', () => {
    const pivot = pivotUsageTrendSeries(series, 'tokens', 15)
    expect(pivot.rows.map((r) => r.model)).toEqual(['model-a', 'model-b'])
  })

  it('handles empty input', () => {
    const pivot = pivotUsageTrendSeries([], 'requests', 60)
    expect(pivot.labels).toEqual([])
    expect(pivot.rows).toEqual([])
  })

  it('renders hour-granularity labels without minutes', () => {
    const pivot = pivotUsageTrendSeries(series, 'requests', 60)
    expect(pivot.labels[0]).toMatch(/^\d+\/\d+ \d{2}:00$/)
  })
})

describe('usageTrendTotals', () => {
  it('sums all four metrics across series (row set is closed incl. __others__)', () => {
    const totals = usageTrendTotals(series)
    expect(totals).toEqual({ requests: 15, tokens: 150, credits: 30, cost_usd: 0.75 })
  })

  it('includes the __others__ folded series (sum = grand total, not top-N total)', () => {
    // 后端折叠后 '__others__' 也是一条普通序列 ⇒ 逐行求和必须把它算进去。
    const withOthers: UsageTrendModelSeries[] = [
      ...series,
      { model: '__others__', total_requests: 2, total_tokens: 20, total_credits: 4, total_cost_usd: 0.1, points: [] },
    ]
    const totals = usageTrendTotals(withOthers)
    expect(totals.requests).toBe(17)
    expect(totals.cost_usd).toBeCloseTo(0.85)
  })

  it('handles null/undefined/empty input', () => {
    expect(usageTrendTotals(null)).toEqual({ requests: 0, tokens: 0, credits: 0, cost_usd: 0 })
    expect(usageTrendTotals(undefined)).toEqual({ requests: 0, tokens: 0, credits: 0, cost_usd: 0 })
    expect(usageTrendTotals([])).toEqual({ requests: 0, tokens: 0, credits: 0, cost_usd: 0 })
  })
})

describe('compactTickValue', () => {
  it('compacts thousands and millions', () => {
    expect(compactTickValue(950)).toBe('950')
    expect(compactTickValue(1234)).toBe('1.2K')
    expect(compactTickValue(4_500_000)).toBe('4.5M')
  })
})
