import { req } from './_core'

// provider-probe.ts — v6.0 audit T12 (2026-06-22)
// 900-series manual disable + default probe model + probe history.
//
// The "900-series" prefix refers to the credential-availability audit
// design (docs/superpowers/specs/2026-06-12-credential-availability-audit-design.md)
// which introduced:
//   - setProviderManualDisabled / setCredentialManualDisabled
//   - setDefaultProbeModel / pickDefaultProbeModel
//   - getRoutableSummary (routable vs unavailable bindings)
//
// The probe history endpoints expose the per-model auto-test results
// that the background probe scheduler writes to provider_probe_runs.

export function setProviderManualDisabled(providerId: number, manual_disabled: boolean, reason = '') {
  return req<{ message: string; manual_disabled: boolean; actor: string }>(
    'PATCH', `/api/providers/${providerId}/manual-disabled`, { manual_disabled, reason }
  )
}

export function setCredentialManualDisabled(providerId: number, credId: number, manual_disabled: boolean, reason = '') {
  return req<{ message: string; manual_disabled: boolean; actor: string }>(
    'PATCH', `/api/providers/${providerId}/credentials/${credId}/manual-disabled`, { manual_disabled, reason }
  )
}

export function setDefaultProbeModel(providerId: number, credId: number, model: string | null, reason = '') {
  return req<{ message: string; old_model: string; new_model: string; source: string; actor: string }>(
    'PATCH', `/api/providers/${providerId}/credentials/${credId}/default-probe-model`, { model, reason }
  )
}

export function pickDefaultProbeModel(providerId: number, credId: number) {
  return req<{ message: string; model: string; source: string; old_model: string }>(
    'POST', `/api/providers/${providerId}/credentials/${credId}/pick-default-probe-model`
  )
}

export function getRoutableSummary(providerId: number) {
  return req<{
    provider_id: number
    total_bindings: number
    routable_bindings: number
    unavailable_bindings: number
    unavailable_breakdown: Record<string, number>
    routable_ratio: number
  }>('GET', `/api/providers/${providerId}/routable-summary`)
}

// ── Probe history (per-model auto-test) ─────────────────────────────────

export interface ProbeRun {
  id: number
  credential_id: number
  raw_model_name: string
  status: 'ok' | 'http_4xx' | 'http_5xx' | 'network' | 'auth' | 'skipped' | 'unknown'
  http_status: number | null
  error_code: string
  error_message: string
  latency_ms: number
  state_change: 'recovered' | 'broke' | 'unchanged'
  state_applied: boolean
  triggered_by: 'scheduler' | 'manual'
  created_at: string
}

export interface ProbeState {
  credential_id: number
  raw_model_name: string
  state: 'unknown' | 'recovering' | 'healthy_confirmed' | 'broken_confirmed'
  consecutive_successes: number
  consecutive_failures: number
  total_attempts: number
  last_attempt_at: string | null
  next_retry_at: string
  last_status: string | null
  last_state_change_at: string | null
  last_state_change_run: number | null
}

export function getProviderProbeHistory(providerId: number, opts?: { limit?: number; status?: string }) {
  const params = new URLSearchParams()
  if (opts?.limit) params.set('limit', String(opts.limit))
  if (opts?.status) params.set('status', opts.status)
  const qs = params.toString()
  return req<{
    provider_id: number
    count: number
    runs: ProbeRun[]
  }>('GET', `/api/providers/${providerId}/probe-history${qs ? `?${qs}` : ''}`)
}

export function getProviderRecentProbeFailures(providerId: number) {
  return req<{
    provider_id: number
    window: string
    models: { raw_model_name: string; failed_count: number; last_failed_at: string; sample_error_code: string }[]
  }>('GET', `/api/providers/${providerId}/probe-history/recent-failures`)
}

export function getProviderProbeStates(providerId: number, opts?: { state?: string }) {
  const params = new URLSearchParams()
  if (opts?.state) params.set('state', opts.state)
  const qs = params.toString()
  return req<{
    provider_id: number
    state_filter: string
    states: ProbeState[]
  }>('GET', `/api/providers/${providerId}/probe-states${qs ? `?${qs}` : ''}`)
}

export function triggerProviderProbe(providerId: number, credentialId: number, rawModelName: string) {
  return req<{ triggered: boolean }>('POST', `/api/providers/${providerId}/probe-history/trigger`, {
    credential_id: credentialId,
    raw_model_name: rawModelName,
  })
}

export function triggerProviderProbeAll(providerId: number) {
  return req<{
    triggered: boolean
    total: number
    ok: number
    model_unavailable: number
    provider_error: number
    skipped: number
    results: ProbeAllResult[]
  }>('POST', `/api/providers/${providerId}/probe-history/trigger-all`)
}

export interface ProbeAllResult {
  credential_id: number
  raw_model_name: string
  status: string
  category: 'ok' | 'model_unavailable' | 'provider_error' | 'skipped'
  http_status: number | null
  error_code: string
  error_message: string
  latency_ms: number
}

export function getRecentModelFailures(opts?: { limit?: number }) {
  const params = new URLSearchParams()
  if (opts?.limit) params.set('limit', String(opts.limit))
  const qs = params.toString()
  return req<{
    window: string
    models: {
      raw_model_name: string
      canonical_name?: string
      creds_affected: number
      total_failures: number
      last_failed_at: string
      sample_error_code: string
      sources: {
        active_probe: number
        passive_probe: number
        request_logs: number
      }
      error_categories: {
        model_not_found?: number
        quota_exhausted?: number
        rate_limit?: number
        auth_failed?: number
        upstream_error?: number
      }
      in_reviewing: boolean
    }[]
    totals: {
      total_failures: number
      models_affected: number
      creds_affected: number
      models_in_reviewing: number
    }
  }>('GET', `/api/routing/recent-model-failures${qs ? `?${qs}` : ''}`)
}