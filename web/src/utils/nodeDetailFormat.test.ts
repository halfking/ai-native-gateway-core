import { describe, expect, it } from 'vitest'
import { fmtTime, pct, statusClass } from './nodeDetailFormat'

describe('nodeDetailFormat', () => {
  it('fmtTime returns dash for empty', () => {
    expect(fmtTime(null)).toBe('—')
    expect(fmtTime('')).toBe('—')
  })

  it('pct formats rate', () => {
    expect(pct(null)).toBe('—')
    expect(pct(0.5)).toBe('50.0%')
  })

  it('statusClass maps known states', () => {
    expect(statusClass('healthy')).toBe('is-ok')
    expect(statusClass('cooling')).toBe('is-warn')
    expect(statusClass('open')).toBe('is-bad')
  })
})
