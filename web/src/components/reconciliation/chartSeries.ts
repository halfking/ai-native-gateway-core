// chartSeries.ts — numbers fed to the reconciliation combo charts.
// Covered-day trimming lives here so a missing snapshot cannot draw a fake cliff.
import { pickCoveredDays } from './coveredDays'

export interface TrendDay {
  date: string
  totals: {
    request_count: number
    success_count: number
    error_count: number
    estimated_cost_cents: number
    credits_charged: number
    input_tokens: number
    output_tokens: number
    cache_read_tokens: number
    cache_write_tokens: number
    cache_hit_ratio: number | null
  }
}

export interface TrendSeries {
  labels: string[]
  success: number[]
  fail: number[]
  money: number[]
  input: number[]
  output: number[]
  cacheRead: number[]
  cacheWrite: number[]
  hit: number[]
}

export function reconTrendSeries(
  days: TrendDay[],
  covered: string[] | undefined,
  money: 'cost' | 'credits',
): TrendSeries {
  const rows = pickCoveredDays(days, covered)
  return {
    labels: rows.map((row) => row.date),
    success: rows.map((row) => row.totals.success_count ?? 0),
    fail: rows.map((row) => row.totals.error_count ?? 0),
    money: rows.map((row) =>
      money === 'cost' ? (row.totals.estimated_cost_cents ?? 0) / 100 : (row.totals.credits_charged ?? 0),
    ),
    input: rows.map((row) => row.totals.input_tokens ?? 0),
    output: rows.map((row) => row.totals.output_tokens ?? 0),
    cacheRead: rows.map((row) => row.totals.cache_read_tokens ?? 0),
    cacheWrite: rows.map((row) => row.totals.cache_write_tokens ?? 0),
    hit: rows.map((row) => (row.totals.cache_hit_ratio ?? 0) * 100),
  }
}
