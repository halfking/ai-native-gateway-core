import { describe, expect, it } from 'vitest'
import { isIdleMarker, isNoTrafficErrorKind } from './liveStreamIdle'
import { errorKindBg, errorKindLabel, statusCategory } from '../composables/liveStreamDisplay'

describe('isIdleMarker', () => {
  it('recognises status idle, type idle_marker, and idle- request ids', () => {
    expect(isIdleMarker({ status: 'idle' })).toBe(true)
    expect(isIdleMarker({ type: 'idle_marker', request_id: 'x' })).toBe(true)
    expect(isIdleMarker({ requestId: 'idle-global-vendor-zyphra_zamba2-7b-instruct' })).toBe(true)
  })

  it('treats no_traffic error kinds as idle, not failure', () => {
    expect(isNoTrafficErrorKind('no_traffic_5min')).toBe(true)
    expect(isIdleMarker({ error_kind: 'no_traffic_1min', status: 'idle' })).toBe(true)
    expect(statusCategory('idle', 'no_traffic_5min')).toBe('success')
    expect(errorKindLabel('no_traffic_5min')).toBe('idle')
    expect(errorKindBg('no_traffic_5min')).toBe('transparent')
  })

  it('does not misclassify real failures', () => {
    expect(isIdleMarker({ status: 'failure', error_kind: 'upstream_timeout', request_id: 'abc' })).toBe(false)
    expect(statusCategory('failure', 'upstream_timeout')).toBe('failure_timeout')
  })
})
