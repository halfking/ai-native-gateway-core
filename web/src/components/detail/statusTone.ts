// statusTone.ts — map request/turn/outcome status strings to CSS tone classes.
export type StatusTone = 'ok' | 'err' | 'warn' | 'info' | 'muted'

/** Normalize status-like strings into a visual tone for badges / borders. */
export function statusTone(status: unknown): StatusTone {
  const s = String(status ?? '').trim().toLowerCase()
  if (!s || s === '—' || s === '-') return 'muted'
  if (
    s === 'success'
    || s === 'ok'
    || s === 'completed'
    || s === 'done'
    || s === 'hit'
    || s === 'passed'
  ) {
    return 'ok'
  }
  if (
    s === 'failure'
    || s === 'failed'
    || s === 'error'
    || s === 'timeout'
    || s === 'cancelled'
    || s === 'canceled'
    || s === 'abort'
    || s === 'aborted'
  ) {
    return 'err'
  }
  if (
    s === 'rate_limited'
    || s === 'throttled'
    || s === 'warning'
    || s === 'degraded'
    || s === 'partial'
    || s === 'skipped'
    || s === 'miss'
  ) {
    return 'warn'
  }
  if (
    s === 'in_progress'
    || s === 'running'
    || s === 'pending'
    || s === 'queued'
    || s === 'waiting'
  ) {
    return 'info'
  }
  return 'muted'
}

export function statusToneClass(status: unknown, prefix = 'tone'): string {
  return `${prefix}--${statusTone(status)}`
}
