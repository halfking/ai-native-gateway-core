// useSpanDaysRange / useRangeDays unit tests.
//
// ★ 2026-11-23 批判式审计轮钉住的核心契约：
//
//	useSpanDaysRange（端点只收 days）：
//	  - 选预设档位 ⇒ days 变成该档位的跨度；
//	  - 选自定义区间 ⇒ days 变成**跨度天数**（不是用户选的那个区间）。
//	    ⚠️ 这是 days-only 端点的固有语义 ⇒ 调用方必须用 KxDateRangePicker 的
//	    presetsOnly 禁掉自定义入口，否则界面显示的区间 ≠ 数据窗口。
//
//	useRangeDays（端点收 start/end）：
//	  - 选预设档位 ⇒ customRange = null（走 days 快路，后端缓存/MV 不受影响）；
//	  - 选自定义区间 ⇒ customRange **原样保留** start/end（不再被压成天数），
//	    且 days 同步为跨度天数（供标签/展示用）。
import { describe, it, expect } from 'vitest'
import { ref } from 'vue'
import { useSpanDaysRange, useRangeDays } from './useSpanDaysRange'

const BUCKETS = [
  { days: 1, labelKey: 'common.dateRange.preset.today' },
  { days: 7, labelKey: 'common.dateRange.preset.last7d' },
  { days: 30, labelKey: 'common.dateRange.preset.last30d' },
  { days: 90, labelKey: 'dashboard.range.last90d' },
]

describe('useSpanDaysRange', () => {
  it('applies a preset bucket to days', () => {
    const days = ref(7)
    const { applyRange } = useSpanDaysRange(days, BUCKETS)
    const preset = useSpanDaysRange(ref(1), BUCKETS).presets.value[1]
    applyRange(preset.resolve())
    expect(days.value).toBe(7)
  })

  it('collapses a custom range to its span (days-only semantics)', () => {
    const days = ref(7)
    const { applyRange } = useSpanDaysRange(days, BUCKETS)
    // 2026-09-01 ~ 2026-09-10 = 10 天（含首尾）
    applyRange({ start: '2026-09-01', end: '2026-09-10' })
    expect(days.value).toBe(10)
  })

  it('exposes the current range for the trigger label', () => {
    const days = ref(7)
    const { rangeValue } = useSpanDaysRange(days, BUCKETS)
    expect(rangeValue.value.start).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(rangeValue.value.end).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})

describe('useRangeDays', () => {
  it('keeps customRange null for a preset selection', () => {
    const days = ref(7)
    const { presets, applyRange, customRange } = useRangeDays(days, BUCKETS)
    const preset = presets.value[2] // 近 30 天
    applyRange(preset.resolve())
    expect(customRange.value).toBeNull()
    expect(days.value).toBe(30)
  })

  it('preserves a custom range verbatim (the audited defect)', () => {
    const days = ref(7)
    const { applyRange, customRange } = useRangeDays(days, BUCKETS)
    applyRange({ start: '2026-09-01', end: '2026-09-10' })
    // ★ 不再被压成「10 天后回落到今天」——原始区间原样保留。
    expect(customRange.value).toEqual({ start: '2026-09-01', end: '2026-09-10' })
    // days 仍同步为跨度天数（供标签与 days-only 旁路使用）。
    expect(days.value).toBe(10)
  })

  it('falls back to the days window after a custom range is replaced by a preset', () => {
    const days = ref(7)
    const { presets, applyRange, customRange } = useRangeDays(days, BUCKETS)
    applyRange({ start: '2026-09-01', end: '2026-09-10' })
    expect(customRange.value).not.toBeNull()
    applyRange(presets.value[0].resolve()) // 今天
    expect(customRange.value).toBeNull()
    expect(days.value).toBe(1)
  })

  it('setCustomRange injects an externally supplied range (URL query)', () => {
    const days = ref(7)
    const { customRange, setCustomRange } = useRangeDays(days, BUCKETS)
    setCustomRange({ start: '2026-08-01', end: '2026-08-05' })
    expect(customRange.value).toEqual({ start: '2026-08-01', end: '2026-08-05' })
    expect(days.value).toBe(5)
    setCustomRange(null)
    expect(customRange.value).toBeNull()
  })
})
