// useStatusColor unit tests — covers the 6-way bucket mapping introduced in
// the P2-9 audit follow-up. The dot/text surface intentionally collapses the
// error_kind space into a single "failure" bucket; statusBarColor keeps the
// granular taxonomy.
import { describe, it, expect } from 'vitest'
import { ref } from 'vue'
import { useStatusColor, useReactiveStatusColor } from './useStatusColor'

describe('useStatusColor', () => {
  const { statusColor } = useStatusColor()

  it('maps success to var(--success)', () => {
    expect(statusColor('success')).toBe('var(--success)')
  })

  it('maps in_progress to var(--accent)', () => {
    expect(statusColor('in_progress')).toBe('var(--accent)')
  })

  it('maps failure to var(--danger)', () => {
    expect(statusColor('failure')).toBe('var(--danger)')
  })

  it('maps both cancelled spellings to var(--warning)', () => {
    expect(statusColor('cancelled')).toBe('var(--warning)')
    expect(statusColor('canceled')).toBe('var(--warning)')
  })

  it('maps idle to var(--muted)', () => {
    expect(statusColor('idle')).toBe('var(--muted)')
  })

  it('falls back to var(--muted) for empty / null / undefined input', () => {
    expect(statusColor('')).toBe('var(--muted)')
    expect(statusColor(null)).toBe('var(--muted)')
    expect(statusColor(undefined)).toBe('var(--muted)')
  })

  it('falls back to var(--muted) for unknown statuses (no hex leak)', () => {
    // This is the key P2-9 invariant: unknown statuses must NOT resolve to a
    // raw hex that diverges from the skin palette.
    expect(statusColor('pending')).toBe('var(--muted)')
    expect(statusColor('mystery_state')).toBe('var(--muted)')
  })

  it('ignores error_kind for the dot surface (granularity belongs to bar)', () => {
    // errorKind is accepted for API symmetry with statusBarColor, but the dot
    // does not differentiate 4xx vs 5xx vs timeout — that visual signal lives
    // on the bar.
    expect(statusColor('failure', 'timeout')).toBe('var(--danger)')
    expect(statusColor('failure', 'rate_limit')).toBe('var(--danger)')
    expect(statusColor('failure', undefined)).toBe('var(--danger)')
  })
})

describe('useReactiveStatusColor', () => {
  it('recomputes when the status ref changes', () => {
    const status = ref<string>('success')
    const dot = useReactiveStatusColor(status)
    expect(dot.value).toBe('var(--success)')

    status.value = 'failure'
    expect(dot.value).toBe('var(--danger)')

    status.value = 'in_progress'
    expect(dot.value).toBe('var(--accent)')
  })

  it('accepts plain getter functions', () => {
    const dot = useReactiveStatusColor(() => 'cancelled')
    expect(dot.value).toBe('var(--warning)')
  })

  it('reads optional errorKind ref as well', () => {
    // Even though errorKind is ignored today, the composable must not throw
    // when it is provided — keeps the call site symmetric with statusBarColor.
    const status = ref<string>('failure')
    const errorKind = ref<string | null>('timeout')
    const dot = useReactiveStatusColor(status, errorKind)
    expect(dot.value).toBe('var(--danger)')
  })
})
