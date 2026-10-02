// kxDatePresets.test.ts — 预设工厂口径测试（2026-09-30 统一日历轮）。
// 时区口径与旧 BoardPeriodSelector 对齐：UTC 日切。
import { describe, expect, it, vi } from 'vitest'
import { makeDateRangePresets, rangeFromSpanDays, rangeSpanDays, shortRangeLabel, spanDaysPreset } from './kxDatePresets'

// 固定到 2026-09-30T12:00:00Z（周三）
const FIXED = Date.UTC(2026, 8, 30, 12, 0, 0)

function utcDate(ms: number) {
  return new Date(ms).toISOString().slice(0, 10)
}

describe('makeDateRangePresets (date 精度, UTC 日切)', () => {
  it('today = 今天单日', () => {
    vi.setSystemTime(FIXED)
    const r = makeDateRangePresets('date').find((p) => p.id === 'today')!.resolve()
    expect(r).toEqual({ start: '2026-09-30', end: '2026-09-30' })
    vi.useRealTimers()
  })

  it('yesterday = 昨天单日', () => {
    vi.setSystemTime(FIXED)
    const r = makeDateRangePresets('date').find((p) => p.id === 'yesterday')!.resolve()
    expect(r).toEqual({ start: '2026-09-29', end: '2026-09-29' })
    vi.useRealTimers()
  })

  it('date 精度默认剔除 last24h，datetime 精度保留', () => {
    const dateIds = makeDateRangePresets('date').map((p) => p.id)
    const dtIds = makeDateRangePresets('datetime').map((p) => p.id)
    expect(dateIds).not.toContain('last24h')
    expect(dtIds).toContain('last24h')
  })

  it('last24h：date=昨天→今天；datetime=24h 回退到分钟', () => {
    vi.setSystemTime(FIXED)
    expect(makeDateRangePresets('date', ['last24h'])[0].resolve()).toEqual({
      start: '2026-09-29',
      end: '2026-09-30',
    })
    expect(makeDateRangePresets('datetime', ['last24h'])[0].resolve()).toEqual({
      start: '2026-09-29 12:00',
      end: '2026-09-30 12:00',
    })
    vi.useRealTimers()
  })

  it('last7d = 近 7 天（今天含首尾）', () => {
    vi.setSystemTime(FIXED)
    const r = makeDateRangePresets('date').find((p) => p.id === 'last7d')!.resolve()
    expect(r).toEqual({ start: '2026-09-24', end: '2026-09-30' })
    expect(rangeSpanDays(r)).toBe(7)
    vi.useRealTimers()
  })

  it('thisMonth = 本月 1 号 → 今天；lastMonth = 上月整月（跨年正确）', () => {
    vi.setSystemTime(Date.UTC(2026, 0, 15, 3, 0, 0))
    const presets = makeDateRangePresets('date')
    expect(presets.find((p) => p.id === 'thisMonth')!.resolve()).toEqual({ start: '2026-01-01', end: '2026-01-15' })
    expect(presets.find((p) => p.id === 'lastMonth')!.resolve()).toEqual({ start: '2025-12-01', end: '2025-12-31' })
    vi.useRealTimers()
  })

  it('rangeFromSpanDays / spanDaysPreset 与 last7d 同口径', () => {
    vi.setSystemTime(FIXED)
    expect(rangeFromSpanDays(1)).toEqual({ start: '2026-09-30', end: '2026-09-30' })
    expect(rangeFromSpanDays(7)).toEqual({ start: '2026-09-24', end: '2026-09-30' })
    expect(spanDaysPreset(7, 'dashboard.range.last7d').resolve()).toEqual(rangeFromSpanDays(7))
    expect(rangeSpanDays(rangeFromSpanDays(90))).toBe(90)
    vi.useRealTimers()
  })

  it('only 裁剪保持默认顺序', () => {
    const ids = makeDateRangePresets('date', ['last30d', 'today']).map((p) => p.id)
    expect(ids).toEqual(['today', 'last30d'])
  })
})

describe('shortRangeLabel / rangeSpanDays', () => {
  it('短标签 MM/DD，datetime 带时刻', () => {
    expect(shortRangeLabel({ start: '2026-09-24', end: '2026-09-30' })).toBe('09/24 – 09/30')
    expect(shortRangeLabel({ start: '2026-09-29 08:30', end: '2026-09-30 12:00' })).toBe('09/29 08:30 – 09/30 12:00')
  })

  // 2026-09-30 审计 P3-1：date/datetime 精度拆分口径。原实现对两种精度统一
  // floor+1，datetime 下整 24h 倍数区间会多算 1 天（72h 算 4 天）。
  it('rangeSpanDays date 精度：含首尾日；同日为 1', () => {
    expect(rangeSpanDays({ start: utcDate(FIXED), end: utcDate(FIXED) })).toBe(1)
    expect(rangeSpanDays({ start: '2026-09-24', end: '2026-09-30' })).toBe(7)
  })

  it('rangeSpanDays datetime 精度：时刻差向上取整（72h=3 天，超界 1 分钟=4 天）', () => {
    expect(rangeSpanDays({ start: '2026-09-30 10:00', end: '2026-09-30 11:30' }, 'datetime')).toBe(1)
    expect(rangeSpanDays({ start: '2026-09-27 12:00', end: '2026-09-30 12:00' }, 'datetime')).toBe(3)
    expect(rangeSpanDays({ start: '2026-09-27 12:00', end: '2026-09-30 12:01' }, 'datetime')).toBe(4)
  })
})
