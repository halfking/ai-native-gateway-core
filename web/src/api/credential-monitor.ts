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
  // Per-(credential, model) availability breakdown (2026-06-22). Replaces the
  // single-model recent_window_stats. Empty array when the credential has no
  // model_offers rows.
  models?: CredentialModelStatus[]
  // Min recent success rate across the credential's models (conservative).
  // null when there are no samples.
  aggregated_success_rate?: number | null
}

// Per-(credential, model) availability row for the credential monitor drawer.
export interface CredentialModelStatus {
  raw_model_name: string
  offer_available: boolean
  offer_unavailable_reason?: string | null
  binding_available: boolean
  binding_unavailable_reason?: string | null
  // 'broken_confirmed' | 'healthy_confirmed' | 'recovering' | 'unknown'
  probe_state: string
  probe_last_status?: string | null
  probe_last_attempt_at?: string | null
  recent_success_rate?: number | null
  recent_samples: number
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
  // include_window_stats is retained for backward-compat but the new endpoint
  // always returns models[]; the param is a no-op now.
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
    source: 'redis' | 'request_logs'
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

// ── Manual model online/offline toggle (2026-06-23) ──────────────────────
//
// Operators can flip a single (credential_id, raw_model_name) binding off
// the candidate pool or back on. Manual offline is sticky — the auto probe
// runner will not touch it until the operator toggles it back to online.
// See admin/credential_monitor.go handleModelToggle for the full spec.

export type ModelToggleAction = 'online' | 'offline'

export interface ModelToggleResponse {
  success: boolean
  available: boolean
  unavailable_reason?: string | null
  prev_available: boolean
  prev_reason?: string | null
  action: ModelToggleAction
}

export function toggleModelAvailability(
  credentialId: number,
  rawModel: string,
  action: ModelToggleAction,
  reason: string,
) {
  return req<ModelToggleResponse>('POST', '/api/credentials/model-toggle', {
    credential_id: credentialId,
    raw_model_name: rawModel,
    action,
    reason,
  })
}

// ── State-change history (2026-06-23) ────────────────────────────────────
//
// Merged UNION of automatic probe consensus transitions
// (model_probe_runs.state_change IN ('recovered','broke')) and manual
// operator toggles (routing_audit_log.action IN
// ('credential.model_toggle_online','credential.model_toggle_offline')),
// ordered by timestamp DESC. The history endpoint is read-only and
// capped at 200 events per call.

export type ModelHistorySource = 'auto' | 'manual'

export type ModelHistoryEventKind =
  | 'recovered'  // auto: probe consensus flipped back to healthy_confirmed
  | 'broke'      // auto: probe consensus flipped to broken_confirmed
  | 'online'     // manual: operator toggled a model back into the pool
  | 'offline'    // manual: operator toggled a model out of the pool

export interface ModelHistoryEvent {
  ts: string
  source: ModelHistorySource
  triggered_by?: string | null  // auto: 'scheduler' | 'manual'; manual: null
  event: ModelHistoryEventKind
  probe_status?: string | null  // auto only
  http_status?: number | null   // auto only
  error_code?: string | null    // auto only
  error_message?: string | null // auto only
  actor?: string | null         // manual: admin username; auto: null
  reason?: string | null        // manual: operator-supplied reason
}

export interface ModelHistoryResponse {
  credential_id: number
  raw_model_name: string
  events: ModelHistoryEvent[]
  count: number
}

export function getModelHistory(credentialId: number, rawModel: string, limit = 50) {
  const params = new URLSearchParams()
  params.set('credential_id', String(credentialId))
  params.set('raw_model_name', rawModel)
  params.set('limit', String(limit))
  return req<ModelHistoryResponse>(
    'GET',
    `/api/credentials/model-history?${params.toString()}`,
  )
}

// ── Credential routing decisions (2026-06-23) ────────────────────────────
//
// Get recent routing decisions for a specific credential. This is a
// filtered view of routing_decision_log scoped to chosen_credential_id.

export interface CredentialRoutingDecision {
  ts: string
  request_id: string
  model: string
  tier: number | null
  success: boolean
  latency_ms: number | null
  error_class: string | null
  chosen_provider_id: number | null
  client_model: string | null
  outbound_model: string | null
  sticky_hit: boolean | null
}

export interface CredentialDecisionsResponse {
  credential_id: number
  decisions: CredentialRoutingDecision[]
  total: number
}

export function getCredentialDecisions(credentialId: number, limit = 50) {
  const params = new URLSearchParams()
  params.set('credential_id', String(credentialId))
  params.set('limit', String(limit))
  return req<CredentialDecisionsResponse>(
    'GET',
    `/api/credentials/decisions?${params.toString()}`,
  )
}

// ── Clear manual_disabled (2026-06-23) ───────────────────────────────────
//
// Force-clear manual_disabled flag to restore credential to normal routing pool.

export function clearManualDisabled(credentialId: number, reason: string) {
  return req<{ success: boolean; message: string }>(
    'POST',
    '/api/credentials/clear-manual-disabled',
    { credential_id: credentialId, reason }
  )
}

// ── Set manual_disabled (2026-06-23) ─────────────────────────────────────
//
// Set manual_disabled flag (true/false) to control credential routing availability.

export function setManualDisabled(credentialId: number, disabled: boolean, reason: string) {
  return req<{ success: boolean; message: string }>(
    'POST',
    '/api/credentials/set-manual-disabled',
    { credential_id: credentialId, manual_disabled: disabled, reason }
  )
}
