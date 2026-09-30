import { describe, expect, it } from 'vitest'
import { matchQuick, quickRange, topReasons, weightedQuality } from './format'
import { modelDist, providerDist } from './distRows'
import type { RangeReport } from '../../api/reportrollup'

describe('reconciliation format', () => {
  const now = new Date(2026, 8, 30)

  it('builds T+1 quick ranges ending yesterday', () => {
    expect(quickRange('yesterday', now)).toEqual(['2026-09-29', '2026-09-29'])
    expect(quickRange('7d', now)).toEqual(['2026-09-23', '2026-09-29'])
    expect(quickRange('month', now)[0]).toBe('2026-09-01')
    expect(matchQuick(quickRange('30d', now), now)).toBe('30d')
  })

  it('rolls the month chip back when yesterday is still last month', () => {
    expect(quickRange('month', new Date(2026, 9, 1))).toEqual(['2026-09-01', '2026-09-30'])
  })

  it('weights quality by request count and sorts failure reasons descending', () => {
    const score = weightedQuality([
      { quality_score: 50, totals: { request_count: 1 } },
      { quality_score: 100, totals: { request_count: 3 } },
    ])
    expect(score).toBeCloseTo(87.5)
    expect(topReasons({ timeout: 2, quota: 9, empty: 0 }).map((item) => item.code)).toEqual(['quota', 'timeout'])
  })
})

describe('distribution rows', () => {
  const report = {
    view: 'provider',
    totals: { request_count: 30, total_tokens: 100, estimated_cost_cents: 600, credits_charged: 51 },
    providers: [
      { provider_id: 1, provider_name: 'alpha', quality_score: 80, totals: { request_count: 20, total_tokens: 10, estimated_cost_cents: 500, credits_charged: 1, error_rate: 0.01 } },
      { provider_id: 2, provider_name: 'beta', quality_score: 90, totals: { request_count: 10, total_tokens: 90, estimated_cost_cents: 100, credits_charged: 50, error_rate: 0.2 } },
    ],
    model_totals: [
      { raw_model_name: 'keep', totals: { request_count: 1, total_tokens: 1, credits_charged: 1, error_rate: 0, latency_p95_ms: 10, cache_hit_ratio: null }, error_breakdown: { timeout: 2 } },
      { raw_model_name: 'drop', totals: { request_count: 1, total_tokens: 1, credits_charged: 1, error_rate: 0, latency_p95_ms: 10, cache_hit_ratio: 0.5 }, error_breakdown: { quota: 1 } },
    ],
  } as unknown as RangeReport

  it('sorts providers by the selected metric and sizes the bar by share of the range total', () => {
    const byToken = providerDist(report, 'token', true, 'unassigned')
    const byMoney = providerDist(report, 'money', true, 'unassigned')
    expect(byToken.map((row) => row.name)).toEqual(['beta', 'alpha'])
    expect(byMoney.map((row) => row.name)).toEqual(['alpha', 'beta'])
    expect(byToken.find((row) => row.name === 'beta')?.pct).toBeCloseTo(90)
    expect(byMoney.find((row) => row.name === 'alpha')?.pct).toBeCloseTo((500 / 600) * 100)
  })

  it('drops models that do not carry the selected failure reason', () => {
    expect(modelDist(report, 'token', true, 'timeout').map((row) => row.name)).toEqual(['keep'])
  })
})
