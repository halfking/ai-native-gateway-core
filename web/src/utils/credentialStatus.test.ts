import { describe, expect, it } from 'vitest'
import { credentialDisplayState } from './credentialStatus'

describe('credentialDisplayState', () => {
  it('prefers an explicit supported effective state', () => {
    expect(credentialDisplayState({ effective_state: 'cooling', status: 'active' })).toBe('cooling')
  })

  it('prioritizes deleted and manual disabled lifecycle states', () => {
    expect(credentialDisplayState({ status: 'deleted', availability_state: 'ready' })).toBe('deleted')
    expect(credentialDisplayState({ manual_disabled: true, availability_state: 'ready' })).toBe('disabled')
  })

  it('normalizes quota, availability, health, and unknown fallbacks', () => {
    expect(credentialDisplayState({ quota_state: 'balance_exhausted' })).toBe('quota_exhausted')
    expect(credentialDisplayState({ availability_state: 'auth_failed' })).toBe('auth_failed')
    expect(credentialDisplayState({ health_status: 'warning' })).toBe('degraded')
    expect(credentialDisplayState({})).toBe('unknown')
  })
})
