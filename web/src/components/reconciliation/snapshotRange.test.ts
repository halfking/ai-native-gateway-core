import { describe, expect, it, vi, afterEach } from 'vitest'
import { defaultSnapshotRange, snapshotDatePresets, snapshotNotAfter } from './snapshotRange'
import { makeDateRangePresets, rangeFromSpanDays } from '../ui/kxDatePresets'

// 2026-11-20 统一轮：reconciliation 预设改为站内标准（owner 口径「近 N 天最晚到
// 今天结束」）。本测试钉住三件事：预设=标准工厂原样、近7天含今天、notAfter=今天。
// 旧契约（止于昨日）已被 owner 图批否决——「非标准的时间选择组件，并且近7天
// 的日期也不太对」。
afterEach(() => {
  vi.useRealTimers()
})

describe('snapshotDatePresets（站内标准预设）', () => {
  it('① 预设与 makeDateRangePresets("date") 完全一致（不再自带第二套）', () => {
    vi.setSystemTime(Date.parse('2026-11-20T15:00:00Z'))
    // resolve 是闭包，toEqual 比不了函数——按 id/labelKey 对齐（resolve 行为由 ② 钉）
    const strip = (list: { id: string; labelKey: string }[]) => list.map(({ id, labelKey }) => ({ id, labelKey }))
    expect(strip(snapshotDatePresets())).toEqual(strip(makeDateRangePresets('date')))
    // 标准形状：今天/昨天/近7天/近14天/近30天/本月/上月（date 精度无 last24h）
    expect(snapshotDatePresets().map((item) => item.id)).toEqual([
      'today', 'yesterday', 'last7d', 'last14d', 'last30d', 'thisMonth', 'lastMonth',
    ])
  })

  it('② 近 7 天含今天（11/14–11/20），不再止于昨日', () => {
    vi.setSystemTime(Date.parse('2026-11-20T15:00:00Z'))
    expect(snapshotDatePresets().find((item) => item.id === 'last7d')?.resolve()).toEqual({
      start: '2026-11-14',
      end: '2026-11-20',
    })
    expect(defaultSnapshotRange()).toEqual(['2026-11-14', '2026-11-20'])
    // 与全站 span 口径同源
    expect(defaultSnapshotRange()).toEqual((() => {
      const r = rangeFromSpanDays(7)
      return [r.start, r.end]
    })())
  })

  it('③ notAfter = 今天（今天可进区间，未来不可选）', () => {
    vi.setSystemTime(Date.parse('2026-11-20T00:30:00Z'))
    expect(snapshotNotAfter()).toBe('2026-11-20')
  })
})
