// ReconciliationCharts.seriesdata.test.ts — trend series must carry real numbers.
// Uncovered zero-fill days stay off the axis; success+fail equals the day's requests.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { reconTrendSeries, type TrendDay } from './chartSeries'
import realSummary from '../../views/admin/__fixtures__/real_internal_summary.json'

const fixture = (realSummary as unknown as { report: { days: TrendDay[]; snapshot_dates: string[] } }).report

describe('reconTrendSeries', () => {
  it('draws only covered days and keeps success + fail equal to request_count', () => {
    const series = reconTrendSeries(fixture.days, fixture.snapshot_dates, 'credits')
    const covered = fixture.snapshot_dates.filter((date) => fixture.days.some((day) => day.date === date))
    expect(covered.length).toBeLessThan(fixture.days.length)
    expect(series.labels).toEqual(covered)
    expect(series.success.length).toBe(covered.length)
    expect(series.fail.length).toBe(covered.length)
    expect(series.money.length).toBe(covered.length)
    covered.forEach((date, index) => {
      const totals = fixture.days.find((day) => day.date === date)!.totals
      expect(series.success[index] + series.fail[index]).toBe(totals.request_count)
      expect(typeof series.success[index]).toBe('number')
      expect(typeof series.fail[index]).toBe('number')
      expect(series.money[index]).toBe(totals.credits_charged)
      expect(series.hit[index]).toBeCloseTo((totals.cache_hit_ratio ?? 0) * 100)
    })
    expect(series.success.reduce((sum, value) => sum + value, 0)).toBeGreaterThan(0)
  })

  it('cost mode uses USD dollars, not cents', () => {
    const series = reconTrendSeries(fixture.days, fixture.snapshot_dates, 'cost')
    const first = fixture.snapshot_dates.find((date) => fixture.days.some((day) => day.date === date))!
    const totals = fixture.days.find((day) => day.date === first)!.totals
    expect(series.money[0]).toBeCloseTo(totals.estimated_cost_cents / 100)
  })

  it('is what the chart component feeds to chart.js', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/components/reconciliation/ReconciliationCharts.vue'), 'utf8')
    expect(source).toContain('reconTrendSeries')
    expect(source).not.toContain('echarts')
  })
})
