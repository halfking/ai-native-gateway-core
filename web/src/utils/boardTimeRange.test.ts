import { describe, expect, it } from 'vitest'
import { boardRangeElapsedMinutes } from './boardTimeRange'

const now = Date.parse('2026-09-30T03:00:00Z')

describe('boardRangeElapsedMinutes', () => {
  it('今天只算到当前时刻，不把剩余小时算进分母', () => {
    expect(boardRangeElapsedMinutes({ preset: 'today', days: 1 }, now)).toBe(180)
  })

  it('自定义范围含今天时，截止到现在而不是末日次日零点', () => {
    expect(boardRangeElapsedMinutes({
      preset: 'custom',
      days: 2,
      start: '2026-09-29',
      end: '2026-09-30',
    }, now)).toBe(24 * 60 + 180)
  })

  it('已结束的历史窗口用完整跨度', () => {
    expect(boardRangeElapsedMinutes({
      preset: 'custom',
      days: 1,
      start: '2026-09-28',
      end: '2026-09-28',
    }, now)).toBe(24 * 60)
  })
})
