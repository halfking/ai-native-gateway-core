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

// 2026-07-21 P0 fix: clear failed node_probe_state rows so the
// routing view v_routable_credential_models immediately re-admits
// bindings that were blocked by NodeProbeWorker's backoff ladder
// (5s/30s/60s/5m/1h/2h/24h). Without this, a single failed probe
// can keep a credential out of routing for up to 24h even when
// "全面探测" (TriggerAllSync) reports it as healthy.
//
// Companion to TriggerAllSync: when the probe run returns ok, the
// server now also writes node_probe_state automatically, so this
// endpoint is only needed when an operator wants to clear stale
// state without re-probing. Body: { credential_id?: number }.
export function resetNodeProbeState(
  providerId: number,
  credentialId?: number,
): Promise<{ message: string; rows_updated: number; credential_id: number }> {
  return req('POST', `/api/providers/${providerId}/node-probe-state/reset`, {
    credential_id: credentialId ?? 0,
  })
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

// ── Routing blocked diagnostics (2026-07-24) ─────────────────────────
// Providers whose "credentials look healthy but routing can't find them"
// can use these endpoints to diagnose and fix blocked bindings.

export interface RoutingBlockedBinding {
  credential_id: number
  credential_label: string
  raw_model_name: string
  is_routable: boolean
  unavailable_reason?: string | null
}

export interface RoutingBlockedCredential {
  credential_id: number
  credential_label: string
  status: string
  availability_state: string
  health_status: string
  manual_disabled: boolean
  lifecycle_status: string
  bindings_total: number
  bindings_routable: number
  bindings_blocked: number
  bindings: RoutingBlockedBinding[]
}

export interface RoutingBlockedDiagnostic {
  provider_id: number
  provider_name: string
  bindings_total: number
  bindings_routable: number
  bindings_blocked: number
  block_reason_breakdown: Record<string, number>
  credentials: RoutingBlockedCredential[]
}

export function getRoutingBlockedDiagnostic(providerId: number) {
  return req<RoutingBlockedDiagnostic>(
    'GET', `/api/admin/diagnostics/routing-blocked?provider_id=${providerId}`
  )
}

export function fixRoutingBlocked(providerId: number) {
  return req<{ triggered: boolean; provider_id: number; timestamp: string; message: string }>(
    'POST', '/api/admin/diagnostics/routing-blocked/fix', { provider_id: providerId }
  )
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
// ── Provider HTTP latency (sub-item ②, 2026-07-23) ──────────────────────
// 复用 node_probe_runs 最近一次成功探测的 direct_latency_ms，
// 按 provider 聚合返回。约 5 分钟级节奏（NodeProbeWorker 退避）。

export interface ProviderLatencyEntry {
  provider_id: number
  provider_name: string
  provider_code: string
  latency_ms: number
  probed_at: string
}

export function fetchProviderLatency() {
  return req<{ entries: ProviderLatencyEntry[] }>(
    'GET', '/api/admin/probe/provider-latency'
  )
}
