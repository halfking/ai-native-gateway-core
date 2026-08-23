import { describe, expect, it } from 'vitest'
import { normalizeSessionTrendData } from './dashboard'

describe('normalizeSessionTrendData', () => {
  it('maps active_count / closed_count / growth_rate_pct from wire format', () => {
    const out = normalizeSessionTrendData(
      {
        trend: [
          {
            date: '2026-08-06',
            new_sessions: 10,
            active_count: 2,
            closed_count: 8,
          },
        ],
        summary: {
          total_new: 10,
          total_active: 2,
          total_closed: 8,
          avg_daily_new: 10,
          growth_rate_pct: -12.5,
        },
        period_start: '2026-07-24T00:00:00Z',
        period_end: '2026-08-23T00:00:00Z',
      },
      30,
    )

    expect(out.trend[0]).toEqual({
      date: '2026-08-06',
      new_sessions: 10,
      active_sessions: 2,
      closed_sessions: 8,
      total_cost: 0,
      total_requests: 0,
    })
    expect(out.summary.growth_rate).toBe(-12.5)
    expect(out.period.days).toBe(30)
  })

  it('keeps design-doc field names when already normalized', () => {
    const out = normalizeSessionTrendData({
      trend: [
        {
          date: '2026-08-07',
          new_sessions: 3,
          active_sessions: 1,
          closed_sessions: 2,
          total_cost: 1.5,
          total_requests: 9,
        },
      ],
      summary: {
        total_new: 3,
        total_active: 1,
        total_closed: 2,
        growth_rate: 5,
      },
      period: { start: 'a', end: 'b', days: 7 },
    })

    expect(out.trend[0].active_sessions).toBe(1)
    expect(out.summary.growth_rate).toBe(5)
    expect(out.period.start).toBe('a')
  })

  it('returns empty trend for empty payload', () => {
    const out = normalizeSessionTrendData({})
    expect(out.trend).toEqual([])
    expect(out.summary.growth_rate).toBe(0)
  })
})
