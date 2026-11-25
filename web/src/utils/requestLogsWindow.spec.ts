// requestLogsWindow.spec.ts —— 「页头时间范围选择器显示的窗口 ≡ 实际查询的窗口」的纯函数边界。
//
// ★ 断言一律用本地时间构造（new Date(y, m, d, …)）比较，不写 UTC 字面量 ⇒ 与跑测试的
//   机器时区无关。
import { describe, expect, it } from 'vitest'
import {
  endOfDay,
  headerPresetById,
  isEndOfDayMarker,
  pickerRangeToQueryWindow,
  rollingHoursWindow,
} from './requestLogsWindow'

// 固定「现在」：本地时区 2026-11-25 07:51:30。
const NOW = new Date(2026, 10, 25, 7, 51, 30, 0)
const local = (y: number, m: number, d: number, h = 0, mi = 0, s = 0, ms = 0) =>
  new Date(y, m, d, h, mi, s, ms).getTime()

describe('rollingHoursWindow（滚动小时窗的显示）', () => {
  it('h24 显示的是 24 小时，不是 24 天（修前的缺陷）', () => {
    const w = rollingHoursWindow(NOW, 24)
    expect(w.start).toBe('2026-11-24 07:51')
    expect(w.end).toBe('2026-11-25 07:51')
  })

  it('h1 / h6 / 72h / 168h 各自是对应的小时数（分钟截断）', () => {
    expect(rollingHoursWindow(NOW, 1).start).toBe('2026-11-25 06:51')
    expect(rollingHoursWindow(NOW, 6).start).toBe('2026-11-25 01:51')
    expect(rollingHoursWindow(NOW, 72).start).toBe('2026-11-22 07:51')
    expect(rollingHoursWindow(NOW, 168).start).toBe('2026-11-18 07:51')
  })

  it('与 timeRange() 的 h24 查询窗口同跨度（显示 ≡ 查询）', () => {
    const w = rollingHoursWindow(NOW, 24)
    expect(Date.parse(w.end.replace(' ', 'T') + ':00') - Date.parse(w.start.replace(' ', 'T') + ':00')).toBe(
      24 * 3600 * 1000,
    )
  })
})

describe('isEndOfDayMarker / pickerRangeToQueryWindow（选择器范围 ⇒ 查询窗口）', () => {
  it('日界标记：HH:mm = 23:59（分钟或带秒的写法都算；23:58 / 23:59:30 不算）', () => {
    expect(isEndOfDayMarker('2026-11-25 23:59')).toBe(true)
    expect(isEndOfDayMarker('2026-11-25T23:59')).toBe(true)
    expect(isEndOfDayMarker('2026-11-25T23:59:00')).toBe(true)
    expect(isEndOfDayMarker('2026-11-25 23:58')).toBe(false)
    expect(isEndOfDayMarker('2026-11-24 23:59:30')).toBe(false)
  })

  it('结束于日界标记 ⇒ 补到当天 23:59:59.999（最后 59.999 秒不再被漏掉）', () => {
    const w = pickerRangeToQueryWindow({ start: '2026-11-24 00:00', end: '2026-11-24 23:59' })
    expect(w.start.getTime()).toBe(local(2026, 10, 24, 0, 0, 0, 0))
    expect(w.end.getTime()).toBe(local(2026, 10, 24, 23, 59, 59, 999))
  })

  it('结束不是日界标记 ⇒ 原样（用户选的具体时刻不被改写）', () => {
    const w = pickerRangeToQueryWindow({ start: '2026-11-24 10:00', end: '2026-11-24 10:30' })
    expect(w.start.getTime()).toBe(local(2026, 10, 24, 10, 0, 0, 0))
    expect(w.end.getTime()).toBe(local(2026, 10, 24, 10, 30, 0, 0))
  })

  it('近 7 天档位 ⇒ 日历窗：6 天前 00:00 ~ 今天 23:59:59.999（不再是滚动 168h）', () => {
    const w = pickerRangeToQueryWindow({ start: '2026-11-19 00:00', end: '2026-11-25 23:59' })
    expect(w.start.getTime()).toBe(local(2026, 10, 19, 0, 0, 0, 0))
    expect(w.end.getTime()).toBe(local(2026, 10, 25, 23, 59, 59, 999))
  })
})

describe('headerPresetById（页头档位 ⇒ 内部预设）', () => {
  it('只有窗口同口径的档位才映射到内部预设', () => {
    expect(headerPresetById).toEqual({ today: 'today', last24h: 'h24', thisMonth: 'thisMonth' })
  })

  it('近 7 天不再映射到滚动 d7（深链 ?preset=d7 的语义不变，只是页头不再走它）', () => {
    expect('last7d' in headerPresetById).toBe(false)
  })
})

describe('endOfDay', () => {
  it('返回当天 23:59:59.999，且不改入参', () => {
    const d = new Date(2026, 10, 25, 7, 51, 30, 0)
    expect(endOfDay(d).getTime()).toBe(local(2026, 10, 25, 23, 59, 59, 999))
    expect(d.getHours()).toBe(7)
  })
})
