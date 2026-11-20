// useFormat.test.ts — pin the format helpers used by detail panels.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { useFormat } from './useFormat'
import { localeRef } from './index'
import { resetSystemTimezoneForTest, setSystemTimezoneForTest } from '../utils/systemTimezone'

describe('useFormat', () => {
  const originalLocale = localeRef.value
  beforeEach(() => { localeRef.value = 'en-US' })
  afterEach(() => {
    localeRef.value = originalLocale
    resetSystemTimezoneForTest()
  })
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

  it('fmtDateTimeWithSeconds returns the — sentinel for invalid input', () => {
    const { fmtDateTimeWithSeconds } = useFormat()
    // R12 候选21: unified on the repo-mainstream '—' sentinel (was '').
    expect(fmtDateTimeWithSeconds(undefined)).toBe('—')
    expect(fmtDateTimeWithSeconds(null)).toBe('—')
    expect(fmtDateTimeWithSeconds('')).toBe('—')
    expect(fmtDateTimeWithSeconds('not-a-date')).toBe('—')
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

  // 时区审计轮（2026-11-21）F6 回归钉：useFormat 族的「日/时」必须按系统时区
  // 渲染。+08 注入下，UTC 2026-08-29 17:30 = +08 08-30 01:30 ⇒ 文本里出现
  // 08-30；UTC 注入下同一瞬间是 08-29。若两种注入产出同一文本 ⇒ 注入通道失效。
  it('renders the day in the system timezone (+08 vs UTC differ)', () => {
    const { fmtDateTimeWithSeconds } = useFormat()
    const inst = Date.parse('2026-08-29T17:30:45Z')
    setSystemTimezoneForTest('Asia/Shanghai')
    const cst = fmtDateTimeWithSeconds(new Date(inst))
    // en-US 渲染形态 'Aug 30, 2026, 01:30:45'：+08 下日 = Aug 30、时 = 01:30。
    expect(cst).toContain('Aug 30')
    expect(cst).toMatch(/01:30:45/)
    setSystemTimezoneForTest('UTC')
    const utc = fmtDateTimeWithSeconds(new Date(inst))
    // UTC 下同一瞬间 = Aug 29 17:30:45。
    expect(utc).toContain('Aug 29')
    expect(utc).toMatch(/17:30:45/)
    expect(cst).not.toBe(utc)
  })
})
