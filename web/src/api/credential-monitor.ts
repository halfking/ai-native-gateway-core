import { req } from './_core'

// credential-monitor.ts — credential monitoring, sliding window, and manual promotion/demotion

export interface CredentialMonitorSummary {
  id: number
  provider_id: number
  provider_name: string
  label: string
  status: string
  availability_state: string
  health_status: string
  quota_state: string
  concurrency_limit: number | null
  concurrency_limit_auto: number | null
  effective_concurrency: number
  manual_disabled: boolean
  consecutive_failures: number
  availability_recover_at: string | null
  state_reason_code: string | null
  state_reason_detail: string | null
  health_checked_at: string | null
  total_requests: number
  recent_window_stats?: WindowStats
}

export interface WindowStats {
  total: number
  success: number
  failed: number
  failure_rate: number
  error_kinds: Record<string, number>
  sample_model?: string
}

export interface CallEntry {
  rid: string
  ts: number  // unix milliseconds
  ok: boolean
  lat: number // latency ms
  err?: string // error kind
}

export function getCredentialMonitorSummary(opts?: { provider_id?: number; include_window_stats?: boolean }) {
  const params = new URLSearchParams()
  if (opts?.provider_id) params.set('provider_id', String(opts.provider_id))
  if (opts?.include_window_stats) params.set('include_window_stats', 'true')
  const qs = params.toString()
  return req<{ credentials: CredentialMonitorSummary[]; count: number }>(
    'GET',
    `/api/credentials/monitor-summary${qs ? `?${qs}` : ''}`
  )
}

export function getSlidingWindow(credentialId: number, model: string, minutes = 60) {
  const params = new URLSearchParams()
  params.set('credential_id', String(credentialId))
  params.set('model', model)
  params.set('minutes', String(minutes))
  return req<{
    credential_id: number
    model: string
    window_minutes: number
    entries: CallEntry[]
    stats: {
      total: number
      success: number
      failed: number
      failure_rate: number
      error_kinds: Record<string, number>
    }
  }>('GET', `/api/credentials/sliding-window?${params.toString()}`)
}

export function promoteCredential(credentialId: number, reason: string) {
  return req<{ success: boolean; message: string }>(
    'POST',
    '/api/credentials/promote',
    { credential_id: credentialId, reason }
  )
}

export function demoteCredential(credentialId: number, reason: string, recoverAfterHours = 2) {
  return req<{ success: boolean; message: string; recover_at: string }>(
    'POST',
    '/api/credentials/demote',
    { credential_id: credentialId, reason, recover_after_hours: recoverAfterHours }
  )
}

export function setConcurrencyAuto(credentialId: number, concurrencyLimitAuto: number, reason: string) {
  return req<{ success: boolean; message: string }>(
    'POST',
    '/api/credentials/set-concurrency-auto',
    { credential_id: credentialId, concurrency_limit_auto: concurrencyLimitAuto, reason }
  )
}
