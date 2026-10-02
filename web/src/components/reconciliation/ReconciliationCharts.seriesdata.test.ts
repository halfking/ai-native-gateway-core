// ReconciliationCharts.seriesdata.test.ts — trend series must carry real numbers.
// Uncovered zero-fill days stay off the axis; success+fail equals the day's requests.
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { reconTrendSeries, type TrendDay } from './chartSeries'
import ReconciliationCharts from './ReconciliationCharts.vue'
import realSummary from '../../views/admin/__fixtures__/real_internal_summary.json'

const drawn: { labels?: string[]; datasets?: { data?: number[]; fill?: unknown }[] }[] = []

vi.mock('chart.js', () => ({
  Chart: class {
    static register() {}
    static getChart() { return undefined }
    static defaults: { color?: string; borderColor?: string } = {}
    constructor(_canvas: unknown, config: { data?: { labels?: string[]; datasets?: { data?: number[] }[] } }) {
      drawn.push(config.data ?? {})
    }
    destroy() {}
    stop() {}
    update() {}
  },
  registerables: [],
}))

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

  it('drops every day when coverage is an empty list', () => {
    expect(reconTrendSeries(fixture.days, [], 'credits').labels).toEqual([])
  })

  it('passes those series into chart.js instead of only mentioning them in source', async () => {
    drawn.length = 0
    const series = reconTrendSeries(fixture.days, fixture.snapshot_dates, 'credits')
    const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })
    const wrapper = mount(ReconciliationCharts, {
      props: { days: fixture.days, coveredDates: fixture.snapshot_dates, money: 'credits' },
      global: { plugins: [i18n] },
      attachTo: document.body,
    })
    await flushPromises()
    const firstSeries = drawn.map((chart) => chart.datasets?.[0]?.data)
    expect(firstSeries).toContainEqual(series.success)
    expect(firstSeries).toContainEqual(series.input)
    const token = drawn.find((chart) => (chart.datasets?.length ?? 0) >= 5)
    expect(token?.datasets?.slice(0, 4).map((dataset) => dataset.fill)).toEqual([
      'stack', 'stack', 'stack', 'stack',
    ])
    expect(token?.datasets?.[4]?.fill).toBe(false)
    wrapper.unmount()
  })

  it('removes canvases when coverage becomes empty so a blank chart cannot stay in the card', async () => {
    const i18n = createI18n({
      legacy: false,
      locale: 'zh-CN',
      messages: { 'zh-CN': { reports: { noData: '暂无数据' } } },
    })
    const wrapper = mount(ReconciliationCharts, {
      props: { days: fixture.days, coveredDates: fixture.snapshot_dates, money: 'credits' },
      global: { plugins: [i18n] },
      attachTo: document.body,
    })
    await flushPromises()
    expect(wrapper.findAll('canvas').length).toBeGreaterThan(0)

    await wrapper.setProps({ coveredDates: [] })
    await flushPromises()

    expect(wrapper.findAll('canvas')).toHaveLength(0)
    const notes = wrapper.findAll('.chart-box .empty')
    expect(notes).toHaveLength(2)
    expect(notes.every((node) => node.text() === '暂无数据')).toBe(true)
    wrapper.unmount()
  })
})
