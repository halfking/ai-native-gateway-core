import { describe, expect, it } from 'vitest'
import { topReasons, weightedQuality } from './format'
import { dayDist, modelDist, providerDist, tenantDist } from './distRows'
import type { RangeReport } from '../../api/reportrollup'

describe('reconciliation format', () => {
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

  it('tenant share column follows the selected metric, not the request share', () => {
    const internal = {
      view: 'internal',
      totals: { request_count: 30, total_tokens: 100, credits_charged: 100 },
      tenants: [
        { tenant_id: 'a', totals: { request_count: 10, total_tokens: 90, credits_charged: 10, error_rate: 0 } },
        { tenant_id: 'b', totals: { request_count: 20, total_tokens: 10, credits_charged: 90, error_rate: 0 } },
      ],
    } as unknown as RangeReport
    const byToken = tenantDist(internal, 'token').find((row) => row.name === 'a')
    const byMoney = tenantDist(internal, 'money').find((row) => row.name === 'a')
    expect(byToken?.cells[0].sub).toBe('33.33%')
    expect(byToken?.cells[4].text).toBe('90.00%')
    expect(byMoney?.cells[4].text).toBe('10.00%')
  })

  it('labels only days missing from snapshot_dates, including a real zero day that has a snapshot', () => {
    const days = {
      view: 'provider',
      snapshot_dates: ['2026-09-28', '2026-09-29'],
      days: [
        { date: '2026-09-28', totals: { request_count: 4, success_count: 4, error_count: 0, error_rate: 0, total_tokens: 1, credits_charged: 1, estimated_cost_cents: 1 } },
        { date: '2026-09-29', totals: { request_count: 0, success_count: 0, error_count: 0, error_rate: 0, total_tokens: 0, credits_charged: 0, estimated_cost_cents: 0 } },
        { date: '2026-09-30', totals: { request_count: 0, success_count: 0, error_count: 0, error_rate: 0, total_tokens: 0, credits_charged: 0, estimated_cost_cents: 0 } },
      ],
    } as unknown as RangeReport
    const rows = dayDist(days, true, '未聚合')
    expect(rows.find((row) => row.name === '2026-09-28')?.sub).toBeUndefined()
    expect(rows.find((row) => row.name === '2026-09-29')?.sub).toBeUndefined()
    expect(rows.find((row) => row.name === '2026-09-30')?.sub).toBe('未聚合')
    const unknown = dayDist({ ...days, snapshot_dates: undefined } as unknown as RangeReport, true, '未聚合')
    expect(unknown.every((row) => row.sub == null)).toBe(true)
  })
})
