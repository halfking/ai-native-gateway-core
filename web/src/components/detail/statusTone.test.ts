import { describe, expect, it } from 'vitest'
import { statusTone, statusToneClass } from './statusTone'

describe('statusTone', () => {
  it('maps success / failure / rate_limited / in_progress', () => {
    expect(statusTone('success')).toBe('ok')
    expect(statusTone('FAILURE')).toBe('err')
    expect(statusTone('rate_limited')).toBe('warn')
    expect(statusTone('in_progress')).toBe('info')
    expect(statusTone('')).toBe('muted')
  })

  it('builds prefixed class names', () => {
    expect(statusToneClass('ok', 'st')).toBe('st--ok')
    expect(statusToneClass('boom', 'st')).toBe('st--muted')
  })
})
