// ReconciliationCharts.seriesdata.test.ts —— 图表「有轴没系列」故障类回归门。
//
// 背景（2026-09-30 浏览器实拍发现）：真数据下页面 KPI 有数、图表坐标轴和图例
// 都画出来了，但**绘图区是空的**。这类症状很容易被误判成「浏览器没动画完」
// 或「截图时机不对」——单元测试里 echarts 被 mock，看不见画布；截图又会超时。
// 所以把判据落在**交给 echarts 的 option 上**：只要 series[].data 里是真值，
// 画布上就没东西可画这件事就不成立，空白就必然是渲染/环境侧，而不是数据侧。
//
// 这道门用的夹具是真端点原样响应（__fixtures__/real_internal_summary.json），
// 不是手搓的假数据：手搓数据会同时骗过「后端真的返回这个形状」和「前端真的
// 能画它」两边。
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import ReconciliationCharts from './ReconciliationCharts.vue'
import realSummary from '../../views/admin/__fixtures__/real_internal_summary.json'

/** 记录每个 init 过的容器拿到了什么 option，顺序即三张图。 */
const setOptionCalls: Record<string, unknown>[] = []
const initTargets: HTMLElement[] = []

vi.mock('echarts', () => ({
  init: (el: HTMLElement) => {
    initTargets.push(el)
    return {
      setOption: (opt: unknown) => {
        setOptionCalls.push(opt)
      },
      dispose: vi.fn(),
      resize: vi.fn(),
      on: vi.fn(),
    }
  },
}))

const fixture = (realSummary as unknown as { report: unknown }).report as {
  days: { date: string; totals: { request_count: number; error_rate: number } }[]
  daily_models: { date: string; raw_model_name: string; totals: { request_count: number } }[]
  error_breakdown: Record<string, number>
  snapshot_dates: string[]
}

const i18n = createI18n({ legacy: false, globalInjection: true, locale: 'zh-CN', messages: { 'zh-CN': {} } })

type Opt = {
  xAxis?: { data?: string[] } | { data?: string[] }[]
  yAxis?: unknown
  series?: { name?: string; data?: (number | null)[] }[]
}

function mountCharts() {
  return mount(ReconciliationCharts, {
    props: {
      days: fixture.days,
      dailyModels: fixture.daily_models,
      errorBreakdown: fixture.error_breakdown,
      coveredDates: fixture.snapshot_dates,
    },
    global: { plugins: [i18n] },
  })
}

beforeEach(() => {
  setOptionCalls.length = 0
  initTargets.length = 0
})

describe('ReconciliationCharts 交给 echarts 的 option', () => {
  it('三张图都真的拿到了 option（没被 v-show/el-empty 短路掉）', () => {
    mountCharts()
    expect(initTargets.length).toBe(3)
    expect(setOptionCalls.length).toBe(3)
  })

  it('趋势图：只画有快照的天，且每个点都是真请求量（非 null/非 0）', () => {
    mountCharts()
    const opt = setOptionCalls[0] as Opt
    const xDays = (Array.isArray(opt.xAxis) ? opt.xAxis[0] : opt.xAxis)?.data ?? []
    const req = opt.series![0].data!
    const rate = opt.series![1].data!

    // 夹具里 days 有 7 天、snapshot_dates 只有 6 天（区间末日还没聚合）。趋势图
    // 必须按 coveredDates 裁掉那天——补零日画出来是「末日断崖」假象。所以这里的
    // 期望值是**覆盖日**，不是 days 的长度。
    const covered = fixture.snapshot_dates.filter((d) => fixture.days.some((x) => x.date === d))
    expect(covered.length).toBeLessThan(fixture.days.length) // 前提：夹具确实含未覆盖日
    expect(xDays).toEqual(covered)
    expect(req.length).toBe(covered.length)
    expect(rate.length).toBe(covered.length)
    // 关键断言：坐标轴有数 ≠ 画得出来，series 里有 null 就会画出「有轴无线」。
    expect(req.every((v) => typeof v === 'number' && v > 0)).toBe(true)
    expect(rate.every((v) => typeof v === 'number' && v >= 0)).toBe(true)
    expect(req).toEqual(covered.map((d) => fixture.days.find((x) => x.date === d)!.totals.request_count))
  })

  it('模型图：系列数 = Top N(+其它)，且堆叠高度等于当天真实请求量', () => {
    mountCharts()
    const opt = setOptionCalls[1] as Opt
    const series = opt.series!
    expect(series.length).toBeGreaterThan(0)
    expect(series.length).toBeLessThanOrEqual(9) // Top 8 + 其它

    const dates = (Array.isArray(opt.xAxis) ? opt.xAxis[0] : opt.xAxis)?.data ?? []
    expect(dates.length).toBeGreaterThan(0)
    // 逐日核对：所有系列（含「其它」）按日求和 == 当天 daily_models 的真实合计。
    // 只断言「非空」会漏掉「图少画了一类模型」这种静默错误。
    for (const date of dates) {
      const stacked = series.reduce(
        (sum, s) => sum + (s.data![dates.indexOf(date)] ?? 0),
        0,
      )
      const truth = fixture.daily_models
        .filter((d) => d.date === date)
        .reduce((sum, d) => sum + d.totals.request_count, 0)
      expect(stacked, `日期 ${date} 堆叠高度与真实合计不符`).toBe(truth)
      expect(truth).toBeGreaterThan(0)
    }
  })

  it('错误图：按次数降序取前 10，长度与去零后的错误种类数一致', () => {
    mountCharts()
    const opt = setOptionCalls[2] as Opt
    const cats = (Array.isArray(opt.yAxis) ? opt.yAxis[0] : opt.yAxis) as { data?: string[] }
    const data = opt.series![0].data!
    const truth = Object.entries(fixture.error_breakdown).filter(([, v]) => v > 0)
    expect(cats.data!.length).toBe(Math.min(10, truth.length))
    expect(data.length).toBe(cats.data!.length)
    expect(data.every((v) => typeof v === 'number' && v > 0)).toBe(true)
    // echarts 的 y 轴是自下而上的，所以数组是升序（代码里 reverse 过一次）。
    expect([...data].sort((a, b) => a! - b!)).toEqual(data)
  })
})
