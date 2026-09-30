import { describe, expect, it } from 'vitest'
import { boardRangeElapsedMinutes, usageQueryForBoardRange } from './boardTimeRange'

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

describe('usageQueryForBoardRange', () => {
  it('预设今天传 UTC 日历日，避免 usage days=1 从昨天零点起算', () => {
    expect(usageQueryForBoardRange({ preset: 'today', days: 1 }, now)).toEqual({
      start: '2026-09-30',
      end: '2026-09-30',
      days: 1,
    })
  })

  it('UTC 零点整不把结束日滑到前一天', () => {
    const midnight = Date.parse('2026-09-30T00:00:00.000Z')
    expect(usageQueryForBoardRange({ preset: 'today', days: 1 }, midnight)).toEqual({
      start: '2026-09-30',
      end: '2026-09-30',
      days: 1,
    })
  })

  it('预设 7 天和 30 天与看板同一起点', () => {
    expect(usageQueryForBoardRange({ preset: '7d', days: 7 }, now)).toEqual({
      start: '2026-09-24',
      end: '2026-09-30',
      days: 7,
    })
    expect(usageQueryForBoardRange({ preset: '30d', days: 30 }, now)).toEqual({
      start: '2026-09-01',
      end: '2026-09-30',
      days: 30,
    })
  })

  it('自定义范围原样传 start/end', () => {
    expect(usageQueryForBoardRange({
      preset: 'custom',
      days: 1,
      start: '2026-09-28',
      end: '2026-09-28',
    }, now)).toEqual({
      start: '2026-09-28',
      end: '2026-09-28',
      days: 1,
    })
  })
})
