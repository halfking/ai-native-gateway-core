import { afterAll, describe, expect, it } from 'vitest'
import { boardRangeElapsedMinutes, resolveBoardRangeMs, usageQueryForBoardRange } from './boardTimeRange'
import { resetSystemTimezoneForTest, setSystemTimezoneForTest } from './systemTimezone'

// 时区轮（2026-11-20）：日切按系统时区。注入 UTC 与旧断言逐字兼容；
// 本文件断言对固定时区稳健（日期串随注入时区的日历日），额外 +08 用例
// 钉「时区真被消费」。跑完复位，避免泄漏到其它测试文件。
setSystemTimezoneForTest('UTC')
afterAll(() => resetSystemTimezoneForTest())

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

describe('系统时区注入（2026-11-20 时区轮）', () => {
  it('+08 注入：今日窗起点 = +08 零点（UTC 09-29 16:00Z）⇒ elapsed = 11h；UTC 注入 = 3h', () => {
    const plus8Now = Date.parse('2026-09-30T03:00:00Z')
    setSystemTimezoneForTest('Asia/Shanghai')
    expect(boardRangeElapsedMinutes({ preset: 'today', days: 1 }, plus8Now)).toBe(11 * 60)
    setSystemTimezoneForTest('UTC')
    expect(boardRangeElapsedMinutes({ preset: 'today', days: 1 }, plus8Now)).toBe(3 * 60)
  })

  it('+08 注入：usageQueryForBoardRange(today) 在 +08 深夜后日期串进新日', () => {
    setSystemTimezoneForTest('Asia/Shanghai')
    // UTC 09-30 20:00 = +08 10-01 04:00 ⇒ 日期串为 10-01。
    const lateNight = Date.parse('2026-09-30T20:00:00Z')
    expect(usageQueryForBoardRange({ preset: 'today', days: 1 }, lateNight)).toEqual({
      start: '2026-10-01',
      end: '2026-10-01',
      days: 1,
    })
  })
})

describe('custom 窗口系统时区（2026-11-21 时区审计轮 F2）', () => {
  it('+08 注入：custom 2026-09-28..09-28 = +08 零点起 24h（非 UTC 零点）', () => {
    setSystemTimezoneForTest('Asia/Shanghai')
    const { startMs, endMs } = resolveBoardRangeMs(
      { preset: 'custom', days: 1, start: '2026-09-28', end: '2026-09-28' },
      now,
    )
    // +08 09-28 零点 = 09-27T16:00Z；终点 = 09-28T16:00Z。
    expect(startMs).toBe(Date.parse('2026-09-27T16:00:00Z'))
    expect(endMs).toBe(Date.parse('2026-09-28T16:00:00Z'))
  })

  it('UTC 注入护栏：custom 同串 = UTC 零点起（与旧契约逐字一致）', () => {
    setSystemTimezoneForTest('UTC')
    const { startMs, endMs } = resolveBoardRangeMs(
      { preset: 'custom', days: 1, start: '2026-09-28', end: '2026-09-28' },
      now,
    )
    expect(startMs).toBe(Date.parse('2026-09-28T00:00:00Z'))
    expect(endMs).toBe(Date.parse('2026-09-29T00:00:00Z'))
  })
})
