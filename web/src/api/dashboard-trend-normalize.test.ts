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

  it('preserves gaps in the trend array without backfilling', () => {
    // The contract says trend points are server-side snapshots; missing dates
    // are caller responsibility (e.g. chart layer backfills). Normalize must
    // pass through and not invent rows for missing dates.
    const out = normalizeSessionTrendData({
      trend: [
        { date: '2026-08-20', active_count: 5, closed_count: 3 },
        { date: '2026-08-22', active_count: 7, closed_count: 1 },
      ],
      period_start: '2026-08-20T00:00:00Z',
      period_end: '2026-08-22T00:00:00Z',
    })
    expect(out.trend).toHaveLength(2)
    expect(out.trend[0].date).toBe('2026-08-20')
    expect(out.trend[1].date).toBe('2026-08-22')
    expect(out.trend.map((p) => p.date)).not.toContain('2026-08-21')
  })

  it('keeps summary intact when trend array is empty (empty window)', () => {
    const out = normalizeSessionTrendData({
      trend: [],
      summary: {
        total_new: 12,
        total_active: 0,
        total_closed: 0,
        growth_rate_pct: 0,
      },
      period: { start: 'a', end: 'b', days: 0 },
    })
    expect(out.trend).toEqual([])
    expect(out.summary.total_new).toBe(12)
    expect(out.summary.total_active).toBe(0)
    expect(out.summary.growth_rate).toBe(0)
    expect(out.period.days).toBe(0)
  })

  it('coerces missing numeric fields to 0 instead of leaking undefined', () => {
    const out = normalizeSessionTrendData({
      trend: [{ date: '2026-08-23' }],
      summary: {},
    })
    expect(out.trend[0]).toEqual({
      date: '2026-08-23',
      new_sessions: 0,
      active_sessions: 0,
      closed_sessions: 0,
      total_cost: 0,
      total_requests: 0,
    })
    expect(out.summary).toEqual({
      total_new: 0,
      total_active: 0,
      total_closed: 0,
      growth_rate: 0,
    })
  })

  it('prefers the legacy *_count fields when both naming styles coexist', () => {
    const out = normalizeSessionTrendData({
      trend: [
        {
          date: '2026-08-21',
          new_sessions: 4,
          active_sessions: 9,
          active_count: 999,
          closed_sessions: 2,
          closed_count: 222,
          total_cost: 1.5,
          total_requests: 8,
        },
      ],
      summary: {
        growth_rate: 12,
        growth_rate_pct: -50,
      },
    })
    expect(out.trend[0].active_sessions).toBe(9)
    expect(out.trend[0].closed_sessions).toBe(2)
    expect(out.summary.growth_rate).toBe(12)
  })

  it('prefers explicit period object over period_start/end fallback', () => {
    const out = normalizeSessionTrendData({
      trend: [],
      period: { start: 'explicit', end: 'period', days: 14 },
      period_start: 'should-be-ignored',
      period_end: 'should-be-ignored',
    })
    expect(out.period).toEqual({ start: 'explicit', end: 'period', days: 14 })
  })

  it('uses caller-supplied days when no period metadata is present', () => {
    const out = normalizeSessionTrendData({ trend: [] }, 30)
    expect(out.period.days).toBe(30)
  })

  it('passes through non-array null trend without throwing', () => {
    const out = normalizeSessionTrendData(
      // @ts-expect-error exercise the null guard
      { trend: null, summary: null },
    )
    expect(out.trend).toEqual([])
    expect(out.summary.growth_rate).toBe(0)
  })
})
