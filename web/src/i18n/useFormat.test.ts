// useFormat.test.ts — pin the format helpers used by detail panels.
import { describe, expect, it } from 'vitest'
import { useFormat } from './useFormat'

describe('useFormat', () => {
  it('fmtDateTimeWithSeconds renders seconds-precision timestamp', () => {
    const { fmtDateTimeWithSeconds } = useFormat()
    // Build the instant directly from local-time components so the test
    // is not sensitive to the runner's timezone offset.
    const local = new Date(2026, 7, 30, 1, 23, 45) // month is 0-based
    const text = fmtDateTimeWithSeconds(local)
    // We only assert on the seconds field, which is the new property
    // this helper adds on top of fmtDateTime.
    expect(text).toMatch(/01:23:45/)
  })

  it('fmtDateTimeWithSeconds returns empty string for invalid input', () => {
    const { fmtDateTimeWithSeconds } = useFormat()
    expect(fmtDateTimeWithSeconds(undefined)).toBe('')
    expect(fmtDateTimeWithSeconds(null)).toBe('')
    expect(fmtDateTimeWithSeconds('')).toBe('')
    expect(fmtDateTimeWithSeconds('not-a-date')).toBe('')
  })

  it('fmtDateTimeWithSeconds renders seconds where fmtDateTime would not', () => {
    const { fmtDateTime, fmtDateTimeWithSeconds } = useFormat()
    const local = new Date(2026, 7, 30, 1, 23, 45)
    const precise = fmtDateTimeWithSeconds(local)
    const coarse = fmtDateTime(local)
    // The seconds-precise formatter must include the seconds field; the
    // coarse one intentionally does not. This is the entire point of
    // exposing a separate helper for the detail view's request time.
    expect(precise.length).toBeGreaterThan(coarse.length)
    expect(precise).toMatch(/:\d{2}:\d{2}/)
  })
})
