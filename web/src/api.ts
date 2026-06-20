import { store, clearApiKey, clearAll, authBearer } from './store'
import type { UserInfo } from './store'

const BASE = ''  // same origin in prod; proxied in dev

function headers(method: string): Record<string, string> {
  const h: Record<string, string> = {}
  // Only send Content-Type when we actually have a body — some
  // middleware/WAFs reject GETs with application/json content-type.
  if (method !== 'GET') {
    h['Content-Type'] = 'application/json'
  }
  const bearer = authBearer()
  if (bearer) h['Authorization'] = `Bearer ${bearer}`
  return h
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(BASE + path, {
    method,
    headers: headers(method),
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (r.status === 401) {
    // Token expired or invalid. Clear credentials and redirect to /login
    // so the user can re-authenticate instead of seeing a cascade of 401s.
    // Using window.location to force a full page reset (clears all
    // in-flight requests that would also 401 with the now-empty store).
    clearAll()
    if (typeof window !== 'undefined' && !window.location.pathname.startsWith('/login')) {
      window.location.href = '/login'
    }
    throw new Error('Unauthorized')
  }
  if (!r.ok) {
    // Try to parse JSON error first (backend uses {"error": "..."}),
    // fall back to plain text.
    let msg = r.statusText
    try {
      const text = await r.text()
      if (text) {
        try {
          const j = JSON.parse(text)
          msg = (j && typeof j.error === 'string') ? j.error :
                (j && j.error && typeof j.error.detail === 'string') ? j.error.detail :
                text
        } catch {
          msg = text
        }
      }
    } catch {
      // network/abort error reading body; keep statusText
    }
    throw new Error(msg)
  }
  if (r.status === 204) return undefined as T
  return r.json()
}

// ── Auth ──────────────────────────────────────────────────────────────────

export interface LoginResponse {
  access_token?: string
  token_type?: string
  expires_in?: number
  user?: UserInfo

  api_key: string
  key_prefix: string
  message: string
}

export function login(username: string, password: string) {
  return req<LoginResponse>('POST', '/api/auth/token', { username, password })
}

// ── Gateway sessions (OpenAI-compatible /v1/sessions) ───────────────────

export interface GatewaySessionCreated {
  session_id: string
  session_key: string
  expires_at: string
  created_at: string
}

/** Create a gw_ session for /v1/chat/completions (sk-* auth, not JWT). */
export async function createGatewaySession(
  apiKey: string,
  taskId?: string,
): Promise<GatewaySessionCreated> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${apiKey}`,
  }
  if (taskId) headers['X-Gw-Task-Id'] = taskId
  const deviceSeed = localStorage.getItem('llmgw_device_seed') ?? 'default'
  headers['X-Device-Seed'] = deviceSeed

  const r = await fetch('/v1/sessions', {
    method: 'POST',
    headers,
    body: JSON.stringify(taskId ? { task_id: taskId } : {}),
  })
  if (!r.ok) {
    const text = await r.text()
    let msg = `HTTP ${r.status}`
    try {
      const j = JSON.parse(text)
      msg = j?.error?.message || text || msg
    } catch {
      msg = text || msg
    }
    throw new Error(msg)
  }
  return r.json()
}

/** Delete a gateway session (sk-* auth). Best-effort cleanup when removing a chat. */
export async function deleteGatewaySession(apiKey: string, sessionId: string): Promise<void> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${apiKey}`,
  }
  const deviceSeed = localStorage.getItem('llmgw_device_seed') ?? 'default'
  headers['X-Device-Seed'] = deviceSeed

  const r = await fetch(`/v1/sessions/${encodeURIComponent(sessionId)}`, {
    method: 'DELETE',
    headers,
  })
  if (!r.ok && r.status !== 404) {
    const text = await r.text()
    let msg = `HTTP ${r.status}`
    try {
      const j = JSON.parse(text)
      msg = j?.error?.message || text || msg
    } catch {
      msg = text || msg
    }
    throw new Error(msg)
  }
}

// ── Catalog ──────────────────────────────────────────────────────────────

export interface CatalogEntry {
  code: string
  tier: string
  display_name: string
  display_name_en: string
  category: string
  kind: string
  protocol: string
  base_url_template: string
  docs_url: string
  default_egress_profile: string
  domestic: boolean
  models_manifest_json: Array<{ id: string; display_name: string; ctx_k?: number }>
  discovery_strategy: string
  hidden: boolean
  notes: string
}

export function getCatalog(tier?: string) {
  const qs = tier ? `?tier=${encodeURIComponent(tier)}` : ''
  return req<CatalogEntry[]>('GET', `/api/catalog${qs}`)
}

export function getCatalogEntry(code: string) {
  return req<CatalogEntry>('GET', `/api/catalog/${encodeURIComponent(code)}`)
}

// ── Providers ────────────────────────────────────────────────────────────

export interface Provider {
  id: number
  catalog_code: string
  display_name: string
  enabled: boolean
  base_url: string | null
  header_profile_code?: string | null
  notes: string | null
  active_credential_count: number
  healthy_credential_count?: number
  warning_credential_count?: number
  unreachable_credential_count?: number
  free_model_count?: number
  health_status?: 'unknown' | 'healthy' | 'warning' | 'unreachable'
  health_checked_at?: string | null
  created_at: string
}

export interface CredentialCheckResult {
  credential_id: number
  provider_id: number
  health_status: string
  health_source: string | null
  health_warning_code: string | null
  health_error: string | null
  health_latency_ms: number | null
  health_probe_model: string | null
  models_ok: boolean
  probe_ok: boolean
  classification_reason: string
  models_failure_reason: string | null
  models_http_status: number | null
  probe_http_status: number | null
  models_latency_ms: number | null
  probe_latency_ms: number | null
  models_error: string | null
  probe_error: string | null
  returned_models?: string[]
  // v0.81 diagnostic capture (admin UI inspect)
  request_url: string | null
  request_method: string
  request_headers_sanitized: Record<string, string>
  request_body_preview: string
  response_status: number | null
  response_headers: Record<string, string>
  response_body_preview: string
  attempt_index: number
  effective_source: 'api' | 'manifest' | 'manifest_only' | 'none' | string
  models_endpoint_resolved: string | null
  models_endpoint_template: string | null
  discovery_strategy: string | null
}

export interface DiagnoseProviderResponse {
  provider_id: number
  credential_count: number
  results: CredentialCheckResult[]
}

export function getProviders(params?: { search?: string; health_status?: string; has_free_model?: boolean }) {
  const query = new URLSearchParams()
  if (params?.search) query.set('search', params.search)
  if (params?.health_status && params.health_status !== 'all') query.set('health_status', params.health_status)
  if (params?.has_free_model != null) query.set('has_free_model', String(params.has_free_model))
  const qs = query.toString()
  return req<Provider[]>('GET', `/api/providers${qs ? '?' + qs : ''}`)
}

export function createProvider(data: { catalog_code: string; code?: string; display_name?: string; base_url?: string; notes?: string; protocol?: string }) {
  return req<{ id: number; message: string }>('POST', '/api/providers', data)
}

export function updateProvider(id: number, data: {
  display_name?: string
  base_url?: string
  protocol?: string
  kind?: string
  category?: string
  discount_rate?: number
  egress_profile?: string
  notes?: string
  enabled?: boolean
}) {
  return req<{ message: string }>('PATCH', `/api/providers/${id}`, data)
}

export function toggleProvider(id: number) {
  return req<{ id: number; enabled: boolean }>('PATCH', `/api/providers/${id}/toggle`)
}

export function checkProvider(id: number) {
  return req<{ accepted: boolean; reason: string; run?: { id: number; status: string } }>('POST', `/api/providers/${id}/check`)
}

export async function checkCredential(providerId: number, credId: number) {
  const { task_id } = await req<{ task_id: number; status: string }>(
    'POST', `/api/providers/${providerId}/credentials/${credId}/check`,
  )
  const task = await pollTask(task_id)
  if (task.status === 'failed') {
    throw new Error(task.error || 'credential check failed')
  }
  return (task.result ?? {}) as CredentialCheckResult
}

export async function diagnoseProvider(providerId: number, opts: { force?: boolean } = {}) {
  const { task_id } = await startDiagnose(providerId, opts)
  return pollTask(task_id)
}

export function addCredential(providerId: number, data: { api_key: string; label?: string }) {
  return req<{ id: number }>('POST', `/api/providers/${providerId}/credentials`, data)
}

export function deleteCredential(providerId: number, credId: number) {
  return req<void>('DELETE', `/api/providers/${providerId}/credentials/${credId}`)
}

export type CredentialStatus = 'active' | 'cooling' | 'degraded' | 'quarantine' | 'quota_expired' | 'disabled'

export interface CredentialQuota {
  id: number
  quota_name: string
  window_type: string
  cap_total_tokens: number | null
  cap_input_tokens: number | null
  cap_output_tokens: number | null
  cap_requests: number | null
  cap_cost_usd: number | string | null
  used_total_tokens: number
  used_input_tokens: number
  used_output_tokens: number
  used_requests: number
  used_cost_usd: number | string
  quota_exhausted: boolean | null
}

export interface CredentialQuotaSummary {
  total_cap_usd: number
  total_used_usd: number
  remaining_usd: number | null
  has_active_quotas: boolean
  any_exhausted: boolean
}

export interface ProviderCredential {
  id: number
  provider_id: number
  label: string
  key_masked?: string | null
  key_mask_error?: string | null
  status: CredentialStatus
  // 900-series: 3-layer state machine fields
  lifecycle_status?: 'active' | 'disabled' | 'suspended' | 'retired' | null
  availability_state?: 'ready' | 'cooling' | 'rate_limited' | 'auth_failed' | 'unreachable' | 'suspended' | null
  quota_state?: 'ok' | 'cooling' | 'periodic_exhausted' | 'balance_exhausted' | 'permanently_exhausted' | null
  manual_disabled?: boolean
  state_reason_code?: string | null
  state_reason_detail?: string | null
  // 900-series: default probe model (spec §4)
  default_probe_model?: string | null
  default_probe_model_source?: 'manual' | 'auto:request_log' | 'auto:domestic_random' | 'cleared' | null
  default_probe_model_picked_at?: string | null
  health_status?: 'unknown' | 'healthy' | 'warning' | 'unreachable'
  health_checked_at?: string | null
  health_source?: 'models' | 'probe' | 'mixed' | 'none' | null
  health_warning_code?: string | null
  health_error?: string | null
  health_latency_ms?: number | null
  health_probe_model?: string | null
  // v0.81: API model-list verification status (null = not yet probed)
  api_models_ok?: boolean | null
  api_models_last_checked_at?: string | null
  api_models_error?: string | null
  trust_level: string
  concurrency_limit: number | null
  effective_fp_slot_limit?: number | null
  fp_slot_limit?: number | null
  fp_slots_used?: number | null
  fp_slots_free?: number | null
  effective_at: string | null
  expires_at: string | null
  tags: string[]
  notes: string | null
  created_at: string
  updated_at: string
  total_requests: number
  total_cost_usd: number | string
  total_prompt_tokens: number
  total_completion_tokens: number
  quotas: CredentialQuota[]
  quota_summary: CredentialQuotaSummary | null
}

export interface CredentialUsage {
  credential_id: number
  label: string
  status: CredentialStatus
  provider_name: string
  days: number
  request_count: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: number | string
  avg_latency_ms: number | string
  success_rate: number | string
}

export function getProviderCredentials(providerId: number) {
  return req<ProviderCredential[]>('GET', `/api/providers/${providerId}/credentials`)
}

// ── GET/POST dual-mode utility ─────────────────────────────────────────────

export async function getOrPost<T>(path: string, getParams?: Record<string, string>, postBody?: any): Promise<T> {
  try {
    const qs = getParams && Object.keys(getParams).length > 0 ? '?' + new URLSearchParams(getParams).toString() : ''
    return await req<T>('GET', path + qs)
  } catch {
    return req<T>('POST', path, postBody)
  }
}

// ── Background task API ───────────────────────────────────────────────────

export interface BackgroundTask {
  id: number
  task_type: string
  status: 'running' | 'succeeded' | 'failed'
  result?: any
  error?: string
  started_at: string
  finished_at?: string
}

export function getTask(taskId: number) {
  return req<BackgroundTask>('GET', `/api/tasks/${taskId}`)
}

export async function pollTask(taskId: number, maxWaitMs = 120000, intervalMs = 2000): Promise<BackgroundTask> {
  const deadline = Date.now() + maxWaitMs
  while (Date.now() < deadline) {
    const task = await getTask(taskId)
    if (task.status !== 'running') return task
    await new Promise(r => setTimeout(r, intervalMs))
  }
  throw new Error('Task polling timeout')
}

// ── Provider Detail ──────────────────────────────────────────────────────

export interface ProviderDetail {
  id: number
  code: string
  display_name: string
  catalog_code: string | null
  kind: string
  category: string
  protocol: string
  base_url: string
  egress_profile: string | null
  domestic: boolean
  discount_rate: number
  enabled: boolean
  manual_disabled?: boolean
  header_profile_code?: string | null
  notes: string | null
  vendor_name: string | null
  active_cred_count: number
  healthy_cred_count: number
  warning_cred_count: number
  cooling_cred_count: number
  unreachable_cred_count: number
  available_model_count: number
  unavailable_model_count: number
  error_rate_24h: number
  created_at: string | null
}

export function getProviderDetail(id: number) {
  return req<ProviderDetail>('GET', `/api/providers/${id}`)
}

export function getProviderModels(providerId: number) {
  return getOrPost<ModelOffer[]>(
    `/api/providers/${providerId}/models`,
    {},
    {}
  )
}

// ── Per-provider model-list refresh (force fetch from vendor API) ────────
// POST  /api/providers/{id}/refresh-models → 202 + { run }
// GET   /api/providers/{id}/refresh-models → { running, latest }
//
// Distinct from getProviderModels which only re-reads the database; this
// actually calls the vendor's /v1/models endpoint and upserts new rows
// into model_offers.  Already-existing rows are kept untouched (we only
// touch availability/last_seen_at on conflict), new model names are
// added.
export interface ProviderRefreshRun {
  run_id: string
  provider_id: number
  status: 'running' | 'succeeded' | 'failed' | 'idle'
  started_at: string
  finished_at: string | null
  heartbeat_at: string | null
  credentials_scanned: number
  models_upserted: number
  credentials_failed: number
  errors: string[]
  message: string
}

export interface ProviderRefreshStartResponse {
  accepted: boolean
  reason: 'started' | 'already_running' | string
  run: ProviderRefreshRun
}

export interface ProviderRefreshStatusResponse {
  running: ProviderRefreshRun | null
  latest: ProviderRefreshRun | null
}

export function refreshProviderModels(providerId: number) {
  return req<ProviderRefreshStartResponse>(
    'POST', `/api/providers/${providerId}/refresh-models`, {}
  )
}

export function getProviderRefreshStatus(providerId: number) {
  return req<ProviderRefreshStatusResponse>(
    'GET', `/api/providers/${providerId}/refresh-models`
  )
}

export function clearProviderModels(providerId: number) {
  return req<{ message: string; deleted: number }>(
    'DELETE', `/api/providers/${providerId}/models`
  )
}

export function toggleModelOfferState(providerId: number, offerId: number, body: { available: boolean }) {
  return req<{ message: string; available: boolean }>('PATCH', `/api/providers/${providerId}/models/${offerId}/state`, body)
}

export interface ModelOfferSuggestion {
  offer_id: number
  raw_model_name: string
  rule_based: string
  canonical_options: Array<{
    id: number
    canonical_name: string
    display_name: string | null
    family: string | null
  }>
}

export function getModelOfferSuggestions(providerId: number, offerId: number) {
  return req<ModelOfferSuggestion>('GET', `/api/providers/${providerId}/models/${offerId}/suggestions`)
}

export function updateModelOffer(
  providerId: number,
  offerId: number,
  body: {
    standardized_name?: string | null
    canonical_id?: number | null
    // outbound_model_name is the upstream-side model identifier (e.g. a
    // Volcano Ark endpoint ID like "ep-20241227XXXX").  Pass an empty
    // string to clear it (revert to raw_model_name).
    outbound_model_name?: string | null
  }
) {
  return req<{
    id: number
    raw_model_name: string
    standardized_name: string | null
    canonical_id: number | null
    canonical_name: string | null
    display_name: string | null
    outbound_model_name: string | null
  }>('PATCH', `/api/providers/${providerId}/models/${offerId}`, body)
}

export function updateCredential(providerId: number, credId: number, data: Partial<{
  label: string
  status: CredentialStatus
  concurrency_limit: number | null
  effective_at: string | null
  expires_at: string | null
  tags: string[]
  notes: string
  balance_usd: number | null
}>) {
  return req<{ message: string }>('PATCH', `/api/providers/${providerId}/credentials/${credId}`, data)
}

export function getCredentialUsage(providerId: number, credId: number, days = 7) {
  return req<CredentialUsage>('GET', `/api/providers/${providerId}/credentials/${credId}/usage?days=${days}`)
}

export function revealCredentialKey(providerId: number, credId: number) {
  return req<{ credential_id: number; api_key: string }>('POST', `/api/providers/${providerId}/credentials/${credId}/reveal`)
}

// ── Provider Detail ──────────────────────────────────────────────────────

export interface ProviderDetail {
  id: number
  code: string
  display_name: string
  catalog_code: string | null
  kind: string
  category: string
  protocol: string
  base_url: string
  egress_profile: string | null
  domestic: boolean
  discount_rate: number
  enabled: boolean
  notes: string | null
  vendor_name: string | null
  active_cred_count: number
  healthy_cred_count: number
  warning_cred_count: number
  cooling_cred_count: number
  unreachable_cred_count: number
  available_model_count: number
  unavailable_model_count: number
  error_rate_24h: number
  created_at: string | null
}

export interface ModelOffer {
  id: number
  credential_id: number
  credential_label: string
  raw_model_name: string
  standardized_name: string | null
  canonical_id: number | null
  display_name: string
  available: boolean
  unavailable_reason: string | null
  unavailable_at: string | null
  p95_latency_ms: number | null
  success_rate: number | null
  input_price: number | null
  output_price: number | null
  last_seen_at: string | null
  routing_tier: string
  availability_source: string
  /**
   * Upstream-side model identifier — for providers like Volcano Ark this is
   * the deployment endpoint ID (e.g. "ep-20241227XXXX") that must be sent
   * in the request body instead of raw_model_name.  null when unset.
   */
  outbound_model_name?: string | null
}

export interface QueryModelsResponse {
  items: ModelOffer[]
  total: number
  page: number
  page_size: number
}

export interface ProviderLogEntry {
  ts: string | null
  request_id: string | null
  credential_id: number | null
  client_model: string | null
  outbound_model: string | null
  success: boolean
  error_kind: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
  total_tokens: number | null
  cost_usd: number | null
  latency_ms: number | null
  stream: boolean | null
}

export interface ProviderLogsResponse {
  items: ProviderLogEntry[]
  total: number
  page: number
  page_size: number
}

export interface DiagnoseModelsProbe {
  status_code: number
  latency_ms: number
  error: string
  models_count: number
  sample_models: string[]
}

export interface DiagnoseChatProbe {
  status_code: number
  latency_ms: number
  error: string
  model_in_response: string
}

export interface DiagnoseCredResult {
  credential_id: number
  label: string
  status: string
  circuit_state: string
  availability_state: string
  health_status: string
  consecutive_failures: number
  models_probe: DiagnoseModelsProbe
  chat_probe: DiagnoseChatProbe
}

export interface DiagnoseErrorClassification {
  auth_errors: number
  rate_limit_errors: number
  timeout_errors: number
  model_not_found_errors: number
  other_errors: number
}

export interface DiagnoseHealthScore {
  credential_id: number
  score: number
}

export interface DiagnoseSummary {
  total_credentials: number
  healthy: number
  degraded: number
  unreachable: number
  cooling: number
  disabled: number
  models_coverage_pct: number
  avg_latency_ms: number
}

export interface FullDiagnoseResponse {
  provider_id: number
  provider_code: string
  enabled: boolean
  base_url: string
  protocol: string
  timestamp: string
  credentials: DiagnoseCredResult[]
  summary: DiagnoseSummary
  error_classification: DiagnoseErrorClassification
  health_scores: DiagnoseHealthScore[]
}

export function queryProviderModels(providerId: number, body: {
  q?: string
  available?: boolean
  unavailable_reason?: string
  credential_id?: number
  min_success_rate?: number
  max_p95_latency?: number
  page?: number
  page_size?: number
}) {
  const getParams: Record<string, string> = {}
  if (body.q) getParams.q = body.q
  if (body.available !== undefined) getParams.available = String(body.available)
  if (body.unavailable_reason) getParams.unavailable_reason = body.unavailable_reason
  if (body.credential_id) getParams.credential_id = String(body.credential_id)
  if (body.min_success_rate) getParams.min_success_rate = String(body.min_success_rate)
  if (body.max_p95_latency) getParams.max_p95_latency = String(body.max_p95_latency)
  if (body.page) getParams.page = String(body.page)
  if (body.page_size) getParams.page_size = String(body.page_size)
  return getOrPost<QueryModelsResponse>(`/api/providers/${providerId}/query`, getParams, body)
}

export function getProviderLogs(providerId: number, body: {
  credential_id?: number
  model?: string
  from_ts?: string
  to_ts?: string
  success?: boolean
  error_kind?: string
  page?: number
  page_size?: number
} = {}) {
  const getParams: Record<string, string> = {}
  if (body.credential_id) getParams.credential_id = String(body.credential_id)
  if (body.model) getParams.model = body.model
  if (body.from_ts) getParams.from_ts = body.from_ts
  if (body.to_ts) getParams.to_ts = body.to_ts
  if (body.success !== undefined) getParams.success = String(body.success)
  if (body.error_kind) getParams.error_kind = body.error_kind
  if (body.page) getParams.page = String(body.page)
  if (body.page_size) getParams.page_size = String(body.page_size)
  return getOrPost<ProviderLogsResponse>(`/api/providers/${providerId}/logs`, getParams, body)
}

export function startDiagnose(providerId: number, opts: { force?: boolean } = {}) {
  return req<{ task_id: number; status: string }>('POST', `/api/providers/${providerId}/diagnose`, opts)
}

export function getDiagnoseResult(providerId: number) {
  return req<any>('GET', `/api/providers/${providerId}/diagnose`)
}

export function startCredentialCheck(providerId: number, credId: number) {
  return req<{ task_id: number; status: string }>('POST', `/api/providers/${providerId}/credentials/${credId}/check`)
}

export function batchRecoverCredentials(providerId: number) {
  return req<{ recovered: number; message: string }>('POST', `/api/providers/${providerId}/batch-recover`)
}

export function resetCredentialAvailability(providerId: number, credId: number) {
  return req<{ message: string }>('POST', `/api/providers/${providerId}/credentials/${credId}/reset-availability`)
}

export function resetCredentialQuota(providerId: number, credId: number) {
  return req<{ message: string }>('POST', `/api/providers/${providerId}/credentials/${credId}/reset-quota`)
}

export function forceRecoverCredential(credId: number) {
  return req<{ triggered: boolean; credential_id: number }>(
    'POST', `/api/providers/credentials/${credId}/force-recover`
  )
}

export function updateCredentialLifecycle(providerId: number, credId: number, lifecycle_status: string) {
  return req<{ message: string }>(
    'PATCH', `/api/providers/${providerId}/credentials/${credId}/lifecycle`, { lifecycle_status }
  )
}

export async function checkCredentialHealth(providerId: number, credId: number) {
  const { task_id } = await startCredentialCheck(providerId, credId)
  return pollTask(task_id)
}

// ── 900-series: manual disable + default probe model ──────────────────────
// Spec: docs/superpowers/specs/2026-06-12-credential-availability-audit-design.md

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

// ── Keys ─────────────────────────────────────────────────────────────────

export interface ApiKey {
  id: number
  key_prefix: string
  owner_user: string | null
  enabled: boolean
  status: 'active' | 'pending' | 'disabled'
  expires_at: string | null
  last_used_at: string | null
  budget_usd: number | null
  rate_limit_rpm: number | null
  rate_limit_concurrent?: number | null
  rate_limit_tpm?: number | null
  key_tier?: string
  application_code: string
  default_client_profile?: string | null
  is_system?: boolean
  remark?: string | null
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  last_request_at: string | null
  tenant_id: string
  key_alias: string | null
}

export interface KeyCreatedResponse {
  id: number
  api_key: string
  key_prefix: string
  application_code: string
  message: string
}

export function getKeys() {
  return req<ApiKey[]>('GET', '/api/keys')
}

export function getKeyDetail(id: number) {
  return req<ApiKey>('GET', `/api/keys/${id}`)
}

export function createKey(data: { application_code: string; tenant_id?: string; key_alias?: string; owner_user?: string; budget_usd?: number; rate_limit_rpm?: number; remark?: string }) {
  return req<KeyCreatedResponse>('POST', '/api/keys', data)
}

export function revokeKey(id: number) {
  return req<void>('DELETE', `/api/keys/${id}`)
}

export function revealKey(id: number) {
  return req<{ key_id: number; api_key: string }>('GET', `/api/keys/${id}/reveal`)
}

export function approveKey(id: number) {
  return req<{ message: string }>('POST', `/api/keys/${id}/approve`)
}

export function disableKey(id: number) {
  return req<{ message: string }>('PATCH', `/api/keys/${id}/disable`)
}

export function enableKey(id: number) {
  return req<{ message: string }>('PATCH', `/api/keys/${id}/enable`)
}

export interface UpdateKeyLimitsRequest {
  rate_limit_rpm: number | null
  rate_limit_concurrent: number | null
  rate_limit_tpm: number | null
}

export function updateKeyLimits(id: number, data: UpdateKeyLimitsRequest) {
  return req<{ status: string } & UpdateKeyLimitsRequest>('PATCH', `/api/keys/${id}/limits`, data)
}

export function applyForKey(data: { application_code: string; owner_user?: string; description?: string }) {
  return req<{ id: number; key_prefix: string; application_code: string; status: string; message: string }>('POST', '/api/keys/apply', data)
}

// ── Key conflict lookup ─────────────────────────────────────────────
// Server-side guard for the "签发新密钥" form: returns the live key (if any)
// that already occupies the (tenant, application, alias) tuple the user is
// about to submit.  The endpoint is mounted under adminMiddleware, so this
// call reuses the same admin bearer token as getKeys().
export interface KeyConflict {
  id: number
  key_prefix: string
  is_system: boolean
  status: string
  enabled: boolean
  expires_at: string | null
  owner_user: string
}

export interface KeyConflictResponse {
  conflict: KeyConflict | null
  application_code: string
  tenant_id: string
  key_alias: string
}

export function getKeyConflict(params: {
  application_code: string
  tenant_id?: string
  key_alias: string
}): Promise<KeyConflictResponse> {
  const qs = new URLSearchParams()
  qs.set('application_code', params.application_code)
  if (params.tenant_id) qs.set('tenant_id', params.tenant_id)
  qs.set('key_alias', params.key_alias)
  return req<KeyConflictResponse>('GET', `/api/keys/lookup?${qs.toString()}`)
}

// ── Tenants ─────────────────────────────────────────────────────────

export interface TenantSummary {
  tenant_id: string
  key_count: number
  total_requests: number
  total_tokens: number
  total_cost_usd: number
}

export interface TenantUsage {
  tenant_id: string
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  unique_keys: number
  unique_models: number
  unique_applications: number
}

export function getTenants() {
  return req<TenantSummary[]>('GET', '/api/usage/tenants')
}

export function getTenantUsage(tenant: string, days = 30) {
  return req<TenantUsage>('GET', `/api/usage/by-tenant?tenant=${encodeURIComponent(tenant)}&days=${days}`)
}


// ── Configuration ─────────────────────────────────────────────────────────

export interface DefaultLimits {
  rate_limit_rpm: number
  rate_limit_concurrent: number
  rate_limit_tpm: number | null
}

export function getDefaultLimits() {
  return req<DefaultLimits>('GET', '/api/config/default-limits')
}

export function setDefaultLimits(data: DefaultLimits) {
  return req<DefaultLimits & { status: string }>('PUT', '/api/config/default-limits', data)
}

// ── Usage ─────────────────────────────────────────────────────────────────

export interface UsageSummary {
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  avg_latency_ms: number
  success_rate: number
}

export interface DashboardOverview {
  total_api_keys: number
  active_api_keys: number
  active_api_keys_in_window: number
  total_models: number
  active_models_in_window: number
  total_providers: number
  active_providers: number
  offline_models: number
  offline_credentials: number
  total_credentials: number
}

export interface HotApiKeyEntry {
  api_key_id: number
  key_prefix: string | null
  application_code: string | null
  owner_user: string | null
  request_count: number
  total_tokens: number
  total_cost_usd: number
  last_used_at: string | null
}

export interface ModelUsage {
  model: string
  provider_code: string
  total_requests: number
  total_tokens: number
  total_cost_usd: number
}

export interface KeyUsageSummary {
  key_id: number
  key_prefix: string
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_tokens: number
  total_cost_usd: number
  avg_latency_ms: number
  success_rate: number
  unique_models: number
  first_request_at: string | null
  last_request_at: string | null
  window_start?: string
  window_end?: string
}

export interface ModelUsageForKey {
  model: string
  request_count: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
  success_rate: number
  first_used_at: string | null
  last_used_at: string | null
}

export interface TrendEntry {
  period: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
}

export function getUsageSummary(days = 7) {
  return req<UsageSummary>('GET', `/api/usage/summary?days=${days}`)
}

export function getDashboardOverview(days = 7) {
  return req<DashboardOverview>('GET', `/api/usage/dashboard?days=${days}`)
}

export function getHotApiKeys(days = 7, limit = 10) {
  return req<HotApiKeyEntry[]>('GET', `/api/usage/hot-keys?days=${days}&limit=${limit}`)
}

export function getUsageByModel(days = 7) {
  return req<ModelUsage[]>('GET', `/api/usage/by-model?days=${days}`)
}

export function getKeyUsage(keyId: number, params: { days?: number; start?: string; end?: string } = {}) {
  const qs = new URLSearchParams()
  if (params.days) qs.set('days', String(params.days))
  if (params.start) qs.set('start', params.start)
  if (params.end) qs.set('end', params.end)
  const s = qs.toString()
  return req<KeyUsageSummary>('GET', `/api/usage/${keyId}${s ? '?' + s : ''}`)
}

export function getKeyUsageByModel(keyId: number, params: { days?: number; start?: string; end?: string; limit?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.days) qs.set('days', String(params.days))
  if (params.start) qs.set('start', params.start)
  if (params.end) qs.set('end', params.end)
  if (params.limit) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<ModelUsageForKey[]>('GET', `/api/usage/${keyId}/models${s ? '?' + s : ''}`)
}

export type UsageTrendPeriod = 'minute' | 'hour' | 'day' | 'week' | 'month'

export function getKeyUsageTrend(keyId: number, period: UsageTrendPeriod = 'day', opts: { days?: number; start?: string; end?: string } = {}) {
  const qs = new URLSearchParams()
  qs.set('period', period)
  if (opts.start && opts.end) {
    qs.set('start', opts.start)
    qs.set('end', opts.end)
  } else {
    qs.set('days', String(opts.days ?? 30))
  }
  return req<TrendEntry[]>('GET', `/api/usage/${keyId}/trend?${qs.toString()}`)
}

// ── Routing ──────────────────────────────────────────────────────────────

export interface RoutingCandidate {
  rank: number
  provider_id: number
  provider_name: string
  catalog_code: string
  protocol: string
  base_url: string | null
  provider_enabled: boolean
  credential_id: number
  credential_label: string
  credential_status: string
  lifecycle_status: string | null
  availability_state: string | null
  availability_recover_at: string | null
  quota_state: string | null
  quota_recover_at: string | null
  concurrency_limit: number | null
  effective_concurrency: number | null
  effective_at: string | null
  expires_at: string | null
  balance_usd: number | string | null
  circuit_state: 'closed' | 'open' | 'half_open' | null
  cooling_until: string | null
  available: boolean
  tier: number
  weight: number
  unit_price_in_per_1m: number | string | null
  unit_price_out_per_1m: number | string | null
  currency: string | null
  success_rate: number
  p95_latency_ms: number
  quota_cap_usd: number | string | null
  quota_used_usd: number | string | null
  model_name: string
  routable: boolean
  runtime_routable: boolean
  runtime_block_reason: string | null
  manual_priority?: number
  active_sessions?: number
  consecutive_failures?: number
  composite_score?: number
  billing_mode?: string
  billing_round?: number
}

export interface RoutingOverviewRow {
  model_name: string
  provider_id: number
  provider_name: string
  catalog_code: string
  protocol: string
  base_url: string | null
  provider_enabled: boolean
  credential_id: number
  credential_label: string
  credential_status: string
  lifecycle_status: string | null
  availability_state: string | null
  availability_recover_at: string | null
  quota_state: string | null
  quota_recover_at: string | null
  balance_usd: number | string | null
  effective_at: string | null
  expires_at: string | null
  circuit_state: 'closed' | 'open' | 'half_open' | null
  cooling_until: string | null
  available: boolean
  tier: number
  weight: number
  unit_price_in_per_1m: number | string | null
  unit_price_out_per_1m: number | string | null
  currency: string | null
  success_rate: number
  p95_latency_ms: number
  runtime_routable: boolean
  runtime_block_reason: string | null
}

export interface RoutingOverviewResponse {
  featured: string[]
  rows: RoutingOverviewRow[]
}

export interface ProbeResult {
  success: boolean
  provider_id: number | null
  provider_name: string
  catalog_code: string
  credential_id: number | null
  latency_ms: number
  reply?: string
  error?: string
}

export interface RoutingResolveResponse {
  client_model: string
  canonical_name: string | null
  canonical_id: number | null
  resolution_path: string
  raw_models: string[]
  plan_order: Array<{ credential_id: number; provider_id: number; raw_model: string; tier: number }>
  candidates: RoutingCandidate[]
}

export function resolveRouting(model: string, clientProfile?: string, persistProbe = false) {
  const qs = new URLSearchParams({ model })
  if (clientProfile) qs.set('client_profile', clientProfile)
  if (persistProbe) qs.set('persist_probe', '1')
  return req<RoutingResolveResponse>('GET', `/api/routing/resolve?${qs}`)
}

export function patchApplicationProfile(applicationCode: string, default_client_profile: string | null) {
  return req<{ id: number; code: string; default_client_profile: string | null }>(
    'PATCH',
    `/api/keys/applications/${encodeURIComponent(applicationCode)}/profile`,
    { default_client_profile },
  )
}

export function patchKeyProfile(keyId: number, fields: Record<string, string>) {
  return req<{ message: string }>('PATCH', `/api/keys/${keyId}`, fields)
}

export function getRoutingOverview(featuredOnly = false) {
  const qs = featuredOnly ? '?featured_only=true' : ''
  return req<RoutingOverviewResponse>('GET', `/api/routing/overview${qs}`)
}

export interface RoutingTreeCredential {
  provider_id: number
  provider_name: string
  catalog_code: string
  protocol: string
  provider_enabled: boolean
  credential_id: number
  credential_label: string
  credential_status: string
  lifecycle_status: string | null
  availability_state: string | null
  availability_recover_at: string | null
  quota_state: string | null
  quota_recover_at: string | null
  concurrency_limit: number | null
  effective_concurrency: number | null
  effective_at: string | null
  expires_at: string | null
  balance_usd: number | string | null
  circuit_state: 'closed' | 'open' | 'half_open' | null
  cooling_until: string | null
  available: boolean
  runtime_routable: boolean
  runtime_block_reason: string | null
  tier: number
  weight: number
  unit_price_in_per_1m: number | string | null
  unit_price_out_per_1m: number | string | null
  currency: string | null
  success_rate: number
  p95_latency_ms: number
  quota_cap_usd: number | string | null
  quota_used_usd: number | string | null
  raw_model_name: string
  standardized_name: string | null
}

export interface RoutingTreeVariant {
  variant: string
  canonical_name: string
  tags: string[]
  credentials: RoutingTreeCredential[]
}

export interface RoutingTreeGeneration {
  generation: string
  variants: RoutingTreeVariant[]
}

export interface RoutingTreeSeries {
  series: string
  generations: RoutingTreeGeneration[]
}

export interface RoutingModelTreeResponse {
  featured: string[]
  series: RoutingTreeSeries[]
  unmapped: Array<{ raw_model_name: string; standardized_name: string | null; credential: RoutingTreeCredential }>
}

export function getRoutingModelTree(featuredOnly = false) {
  const qs = featuredOnly ? '?featured_only=true' : ''
  return req<RoutingModelTreeResponse>('GET', `/api/routing/model-tree${qs}`)
}

// ── Routing v2: policy / featured / decisions / health / audit ──────────

export interface RoutingPolicy {
  tenant_id: string
  algorithm_version: number
  retry_per_credential: number
  tier_fallback_max: number
  slot_soft_limit_ratio: number | string
  slot_hard_limit_ratio: number | string
  slot_wait_max_ms: number
  circuit_open_seconds: number
  circuit_failure_threshold: number
  circuit_max_open_seconds: number
  featured_models: string[]
  updated_at?: string
}

export function getPolicy() {
  return req<RoutingPolicy>('GET', '/api/routing/policy')
}

export function patchPolicy(patch: Partial<RoutingPolicy> & { actor?: string }) {
  return req<RoutingPolicy>('PATCH', '/api/routing/policy', patch)
}

export function getFeatured() {
  return req<{ featured_models: string[] }>('GET', '/api/routing/featured')
}

export function patchFeatured(featured_models: string[], actor = 'admin') {
  return req<{ featured_models: string[] }>('PATCH', '/api/routing/featured', { featured_models, actor })
}

export interface RoutingDecision {
  ts: string
  request_id: string
  idempotency_key: string | null
  tenant_id: string
  api_key_id: number | null
  model: string
  chosen_credential_id: number | null
  chosen_provider_id: number | null
  tier: number | null
  candidates_tried: number
  latency_ms: number | null
  success: boolean
  error_class: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
  cost_usd: number | string | null
  request_bytes: number | null
  response_bytes: number | null
  client_model: string | null
  resolved_raw_model: string | null
  outbound_model: string | null
  sticky_hit: boolean | null
  client_profile: string | null
  request_mode: string | null
  identity_hash: string | null
  transform_rule_id: string | null
  egress_protocol: string | null
  failure_stage: string | null
  failure_detail_code: string | null
  resolution_path: string | null
  canonical_model: string | null
  resolution_raw_models: string[]
  decision_trace: Record<string, unknown>
}

export interface DecisionsResponse {
  total: number
  offset: number
  limit: number
  decisions: RoutingDecision[]
}

export function getDecisions(params: { model?: string; canonical?: string; success?: boolean; since_minutes?: number; limit?: number; offset?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.model) qs.set('model', params.model)
  if (params.canonical) qs.set('canonical', params.canonical)
  if (params.success !== undefined) qs.set('success', String(params.success))
  if (params.since_minutes !== undefined) qs.set('since_minutes', String(params.since_minutes))
  if (params.limit !== undefined) qs.set('limit', String(params.limit))
  if (params.offset !== undefined) qs.set('offset', String(params.offset))
  const s = qs.toString()
  return req<DecisionsResponse>('GET', `/api/routing/decisions${s ? '?' + s : ''}`)
}

export interface CircuitInfo {
  credential_id: number
  label: string
  status: string
  circuit_state: 'closed' | 'open' | 'half_open'
  consecutive_failures: number
  circuit_open_count_window: number
  cooling_until: string | null
  provider_name: string
  catalog_code: string
}

export function getRoutingHealth() {
  return req<{ credentials: CircuitInfo[]; summary: { total: number; open: number; closed: number } }>(
    'GET', '/api/routing/health'
  )
}

export interface AuditEntry {
  id: number
  ts: string
  actor: string
  action: string
  target_type: string
  target_id: string | null
  before_json: Record<string, unknown> | null
  after_json: Record<string, unknown> | null
}

export function getAudit(limit = 50) {
  return req<AuditEntry[]>('GET', `/api/routing/audit?limit=${limit}`)
}

// ── Request logs ──────────────────────────────────────────────────────────

export interface RequestLogRow {
  ts: string
  request_id: string
  api_key_id: number | null
  end_user_id: string | null
  client_model: string | null
  outbound_model: string | null
  credential_id: number | null
  credential_label: string | null
  provider_id: number | null
  provider_name: string | null
  provider_code: string | null
  client_profile: string | null
  request_mode: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  total_tokens: number | null
  cost_usd: number | string | null
  cost_display: number | string | null
  cost_currency: string | null
  latency_ms: number | null
  success: boolean
  request_status: 'in_progress' | 'success' | 'failure'
  error_kind: string | null
  search_text: string | null
  identity_hash: string | null
  virtual_client_id: string | null
  virtual_ip: string | null
  virtual_mac: string | null
  affinity_hit: boolean | null
  stream_first_chunk_ms: number | null
  stream_chunk_count: number | null
  stream_interrupted: boolean | null
  stream_done_sent: boolean | null
  usage_source: 'llm' | 'estimated' | null
  gw_session_id: string | null
  gw_task_id: string | null
  api_key_prefix: string | null
  api_key_owner_user: string | null
  application_code: string | null
  canonical_name: string | null
  provider_model: string | null
  trace_seq: number | null
  credits_charged: number | null

  // Round 47 compression v7: parent-child chain tracking.
  // Populated when a request was rewritten by the compressor (either
  // pre-request mode=1 auto_threshold or post-error mode=2 on_4xx).
  // parent_request_id points to the pre-compression request_id;
  // compression_* describe the strategy + payload.
  parent_request_id: string | null
  compression_reason: 'mode_1_auto_threshold' | 'mode_2_on_4xx' | null
  compression_strategy:
    | 'mechanical_trim'
    | 'memora_l1_inject'
    | 'llm_summary'
    | 'noop'
    // v3 (2026-06-19) session-level strategies.
    | 'delta_append'
    | 'sliding_window_token'
    | 'sliding_window_count'
    | 'sliding_window_idle'
    | null
  compression_meta: any | null
  // v3 (2026-06-19) session-level outbound body. NULL when v3 did not
  // rewrite the body (outbound == client request_body).
  outbound_body: any | null
  outbound_msg_count: number | null
  outbound_token_est: number | null
  outbound_msg_hashes: any | null

  // 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
  // `upstream_finish_reason` is the SOLE home for the upstream finish_reason
  // (stop, tool_calls, length, end_turn, function_call, max_tokens, …). It is
  // populated for BOTH success and failure rows and should NOT be displayed
  // as a "失败详情" / failure label. `failure_detail_code` and
  // `failure_stage` are now reserved for actual failure / interruption codes.
  upstream_finish_reason: string | null
  failure_detail_code: string | null
  failure_stage: string | null
}

export interface RequestLogDetail extends RequestLogRow {
  request_body: any | null
  response_body: any | null
}

export interface RequestLogsResponse {
  items: RequestLogRow[]
  count: number
}

export interface SessionSummaryMeta {
  session_id: string
  log_count: number
  data_from: string
  data_to: string
  generated_at: string
  api_key_id: number
  model: string
}

export interface SessionSummaryResponse {
  summary: string
  key_points: string[]
  meta: SessionSummaryMeta
}

export interface MemoraWriteResult {
  written: number
  user_id: string
  project_id: string
  status: string
  error?: string
}

export interface SessionSummaryToMemoraResponse {
  summary: string
  key_points: string[]
  meta: SessionSummaryMeta
  memora: MemoraWriteResult
}

export interface TopRequestModel {
  canonical_id: number | null
  canonical_name: string
  display_name: string
  request_count: number
}

export function getRequestLogs(params: {
  api_key_id?: number
  provider_id?: number
  credential_id?: number
  identity_hash?: string
  from?: string
  to?: string
  q?: string
  model?: string
  error_kind?: string
  request_status?: 'in_progress' | 'success' | 'failure'
  success?: boolean
  canonical_id?: number
  usage_source?: 'llm' | 'estimated'
  gw_session_id?: string
  gw_task_id?: string
  chrono?: boolean
  page?: number
  page_size?: number
} = {}) {
  const qs = new URLSearchParams()
  if (params.api_key_id != null) qs.set('api_key_id', String(params.api_key_id))
  if (params.provider_id != null) qs.set('provider_id', String(params.provider_id))
  if (params.credential_id != null) qs.set('credential_id', String(params.credential_id))
  if (params.identity_hash) qs.set('identity_hash', params.identity_hash)
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.q) qs.set('q', params.q)
  if (params.model) qs.set('model', params.model)
  if (params.error_kind) qs.set('error_kind', params.error_kind)
  if (params.request_status) qs.set('request_status', params.request_status)
  if (params.success != null) qs.set('success', String(params.success))
  if (params.canonical_id != null) qs.set('canonical_id', String(params.canonical_id))
  if (params.usage_source) qs.set('usage_source', params.usage_source)
  if (params.gw_session_id) qs.set('gw_session_id', params.gw_session_id)
  if (params.gw_task_id) qs.set('gw_task_id', params.gw_task_id)
  if (params.chrono) qs.set('chrono', '1')
  if (params.page != null) qs.set('page', String(params.page))
  if (params.page_size != null) qs.set('page_size', String(params.page_size))
  const s = qs.toString()
  return req<RequestLogsResponse>('GET', `/api/logs${s ? '?' + s : ''}`)
}

export function getRequestLogDetail(requestId: string) {
  return req<RequestLogDetail>('GET', `/api/logs/${encodeURIComponent(requestId)}`)
}

export function getRequestLogTopModels(params: { from?: string; to?: string; limit?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.limit != null) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<{ items: TopRequestModel[] }>('GET', `/api/logs/top-models${s ? '?' + s : ''}`)
}

export function getSessionSummary(gwSessionId: string) {
  return req<SessionSummaryResponse>('POST', '/api/logs/session-summary', {
    gw_session_id: gwSessionId,
  })
}

export function sessionSummaryToMemora(gwSessionId: string) {
  return req<SessionSummaryToMemoraResponse>('POST', '/api/logs/session-summary-to-memora', {
    gw_session_id: gwSessionId,
  })
}

export function probeModel(model: string, messages?: Array<{role: string; content: string}>, maxTokens = 20, clientProfile = 'roocode') {
  return req<ProbeResult>('POST', '/api/routing/probe', {
    model,
    messages: messages ?? [{ role: 'user', content: 'Hello, please reply with one word: OK' }],
    max_tokens: maxTokens,
    client_profile: clientProfile,
    request_mode: 'chat',
  })
}

// ── Available models (taxonomy) ─────────────────────────────────────────

export interface AvailableVersion {
  canonical_name: string
  display_name: string
  modality: string
  context_window: number | null
  parameters_b: number | null
  aliases: string[]
  raw_names: string[]
  provider_count: number
  featured: boolean
  tags: string[]
}

export interface AvailableFamily {
  id: string
  display_name: string
  vendor: string
  versions: AvailableVersion[]
}

export interface PopularModel {
  canonical_name: string
  display_name: string
  source: 'policy' | 'usage' | string
  count?: number | null
}

export interface AvailableModelsResponse {
  families: AvailableFamily[]
  popular?: PopularModel[]
  unmapped: string[]
  total_raw: number
}

export function getAvailableModels() {
  return req<AvailableModelsResponse>('GET', '/api/routing/available-models')
}

export function getAvailableModelsRaw() {
  return req<string[]>('GET', '/api/routing/available-models/raw')
}

// ── Model tags ───────────────────────────────────────────────────────────

export interface ModelCanonical {
  id: number
  canonical_name: string
  display_name: string | null
  family: string | null
  vendor?: string | null
  modality: string
  context_window: number | null
  parameters_b: number | string | null
  notes: string | null
  status: 'active' | 'disabled' | 'deprecated' | 'hidden'
  disabled_reason: string | null
  source: string | null
  tags: string[]
  tags_locked: boolean
  tags_updated_at: string | null
  updated_at: string | null
  offer_count: number
  alias_count: number
}

export interface ModelAlias {
  id: number
  canonical_id: number
  raw_name: string
  quantization: string | null
  surface: string | null
  status: 'active' | 'disabled' | 'deprecated' | 'hidden'
  notes: string | null
  updated_at: string | null
}

export interface ModelFamily {
  id: string
  display_name: string
  vendor: string | null
  status: 'active' | 'disabled' | 'deprecated' | 'hidden'
  source: string
  notes: string | null
  model_count: number
}

export interface ModelOffer {
  provider_id: number
  provider_name: string
  catalog_code: string
  base_url: string | null
  provider_enabled: boolean
  credential_id: number
  credential_label: string
  credential_status: string
  health_status: string | null
  concurrency_limit: number | null
  raw_model_name: string
  standardized_name: string | null
  p95_latency_ms: number | null
  success_rate: number | null
  available: boolean
  input_price: number | null
  output_price: number | null
  cache_read_price: number | null
  cache_write_price: number | null
}

export interface ModelDetail extends ModelCanonical {
  aliases: ModelAlias[]
  offers: ModelOffer[]
  created_at: string
}

export interface ModelListResponse {
  total: number
  items: ModelCanonical[]
}

export interface TagInfo {
  tag: string
  count: number
  sample_models?: string[]
}

export interface TagNamespaceGroup {
  namespace: string
  tags: TagInfo[]
}

export interface TagsResponse {
  namespaces: TagNamespaceGroup[]
}

export function listModels(params: { tags?: string[]; family?: string; modality?: string; status?: string } = {}) {
  const qs = new URLSearchParams()
  for (const tag of params.tags ?? []) qs.append('tag', tag)
  if (params.family) qs.set('family', params.family)
  if (params.modality) qs.set('modality', params.modality)
  if (params.status) qs.set('status', params.status)
  const s = qs.toString()
  return req<ModelListResponse>('GET', `/api/models${s ? '?' + s : ''}`)
}

export function listModelFamilies() {
  return req<{ items: ModelFamily[] }>('GET', '/api/models/families')
}

export function createModel(data: {
  canonical_name: string
  display_name?: string | null
  family?: string | null
  modality?: string
  context_window?: number | null
  parameters_b?: number | null
  notes?: string | null
  tags?: string[]
  aliases?: string[]
}) {
  return req<ModelCanonical>('POST', '/api/models', data)
}

export interface DiscoverModelsResult {
  credentials_scanned: number
  credentials_succeeded: number
  credentials_failed: number
  models_seen: number
  offers_upserted: number
  canonicals_created_or_matched: number
  items: Array<{
    provider_id: number
    credential_id: number
    provider_name: string
    source: string
    models: number
    sample?: string[]
    error?: string | null
  }>
}

export interface ModelDiscoveryRun {
  id: number
  tenant_id: string
  trigger: 'manual' | 'scheduled'
  status: 'running' | 'succeeded' | 'failed'
  started_at: string
  finished_at: string | null
  heartbeat_at: string | null
  lease_expires_at: string
  request: Record<string, unknown>
  summary: DiscoverModelsResult | null
  error: string | null
}

export interface ModelDiscoveryStartResponse {
  accepted: boolean
  reason: 'started' | 'already_running' | 'recent_success'
  run: ModelDiscoveryRun
}

export interface ModelDiscoveryStatusResponse {
  running: ModelDiscoveryRun | null
  latest: ModelDiscoveryRun | null
  interval_seconds: number
  timeout_seconds: number
}

export function discoverModels(data: { provider_id?: number; credential_id?: number; include_disabled?: boolean; use_manifest_fallback?: boolean; force?: boolean } = {}) {
  return req<ModelDiscoveryStartResponse>('POST', '/api/models/discover', data)
}

export function getModelDiscoveryStatus() {
  return req<ModelDiscoveryStatusResponse>('GET', '/api/models/discover/status')
}

export function getModel(id: number) {
  return req<ModelDetail>('GET', `/api/models/${id}`)
}

export function updateModel(id: number, data: Partial<{
  display_name: string | null
  family: string | null
  modality: string | null
  context_window: number | null
  parameters_b: number | null
  notes: string | null
  status: 'active' | 'disabled' | 'deprecated' | 'hidden'
  disabled_reason: string | null
}>) {
  return req<ModelCanonical>('PATCH', `/api/models/${id}`, data)
}

export function createModelAliasesBulk(
  modelId: number,
  data: { raw_names: string[]; client_profiles?: string[] | null; notes?: string | null },
) {
  return req<{ created: unknown[]; count: number }>('POST', `/api/models/${modelId}/aliases/bulk`, data)
}

export function createModelAlias(modelId: number, data: { raw_name: string; quantization?: string | null; surface?: string | null; notes?: string | null; client_profiles?: string[] | null }) {
  return req<ModelAlias>('POST', `/api/models/${modelId}/aliases`, data)
}

export function updateModelAlias(modelId: number, aliasId: number, data: Partial<{
  raw_name: string
  quantization: string | null
  surface: string | null
  status: 'active' | 'disabled' | 'deprecated' | 'hidden'
  notes: string | null
}>) {
  return req<ModelAlias>('PATCH', `/api/models/${modelId}/aliases/${aliasId}`, data)
}

export function listTags() {
  return req<TagsResponse>('GET', '/api/tags')
}

export function patchModelTags(canonicalId: number, tags: string[]) {
  return req<ModelCanonical>('PATCH', `/api/models/${canonicalId}/tags`, { tags })
}

export function resetModelTags(canonicalId: number) {
  return req<ModelCanonical>('POST', `/api/models/${canonicalId}/tags/reset`)
}

// ── Key Applications (W5) ────────────────────────────────────────────────

export interface KeyApplication {
  id: string
  client_ip: string
  contact: string
  purpose: string | null
  status: 'pending' | 'approved' | 'rejected' | 'expired'
  issued_key_id: number | null
  admin_notes: string | null
  reviewed_by: string | null
  reviewed_at: string | null
  created_at: string
  expires_at: string | null
}

export interface ApproveApplicationResponse {
  application_id: string
  status: string
  key_id: number
  key_prefix: string
  message: string
}

export function listKeyApplications(status?: string) {
  const qs = status ? `?status=${encodeURIComponent(status)}` : ''
  return req<KeyApplication[]>('GET', `/api/key-applications${qs}`)
}

export function approveKeyApplication(id: string, adminNotes?: string) {
  return req<ApproveApplicationResponse>('POST', `/api/key-applications/${id}/approve`, {
    admin_notes: adminNotes ?? null,
    reviewed_by: 'admin',
  })
}

export function rejectKeyApplication(id: string, adminNotes?: string) {
  return req<{ application_id: string; status: string; message: string }>(
    'POST',
    `/api/key-applications/${id}/reject`,
    { admin_notes: adminNotes ?? null, reviewed_by: 'admin' },
  )
}

// ── System Background Tasks ──────────────────────────────────────────────

export interface BackgroundTaskDiscovery {
  alive: boolean
  running: boolean
  status: string | null
  trigger: string | null
  started_at: string | null
  finished_at: string | null
  heartbeat_at: string | null
  error: string | null
  summary: Record<string, unknown> | null
  elapsed_seconds: number | null
  since_last_seconds: number | null
}

export interface BackgroundTaskLoop {
  alive: boolean
  last_check_at: string | null
  checks_last_10m?: number
}

export interface BackgroundTasksStatus {
  discovery: BackgroundTaskDiscovery
  probe_loop: BackgroundTaskLoop
  cycler: BackgroundTaskLoop
  recovery: BackgroundTaskLoop
  telemetry: BackgroundTaskLoop
}

export function getBackgroundTasksStatus() {
  return req<BackgroundTasksStatus>('GET', '/api/system/background-tasks')
}

// ── Routing: Score Details & Manual Priority ──────────────────────────────

export interface ScoreDetail {
  credential_id: number
  provider_id: number
  provider_name: string
  raw_model: string
  manual_priority: number
  price_in: number
  price_out: number
  blended_cost: number
  active_sessions: number
  consecutive_failures: number
  concurrency_limit: number | null
  currency: string
  normalized_cost: number
  session_load: number
  composite_score: number
}

export interface ScoreDetailsResponse {
  model: string
  weights: ScoringWeights
  candidates: ScoreDetail[]
}

export interface ScoringWeights {
  price: number
  session_load: number
  failure_penalty: number
  default_price_cny: number
  default_price_usd: number
}

export function getScoreDetails(model: string) {
  return req<ScoreDetailsResponse>('GET', `/api/routing/score-details?model=${encodeURIComponent(model)}`)
}

export function updateManualPriority(credentialId: number, modelName: string, priority: number) {
  return req<{ status: string }>('PATCH', '/api/routing/manual-priority', {
    credential_id: credentialId,
    model_name: modelName,
    manual_priority: priority,
  })
}

export function getScoringWeights() {
  return req<ScoringWeights>('GET', '/api/routing/scoring-weights')
}

export function updateScoringWeights(weights: Partial<ScoringWeights>) {
  return req<{ status: string }>('PATCH', '/api/routing/scoring-weights', weights)
}

export interface FeaturedModel {
  name: string
  standardized_name: string
  count: number
}

export function getFeaturedModelsDynamic() {
  return req<{ models: FeaturedModel[] }>('GET', '/api/routing/featured-models')
}

// ── Free Pool ────────────────────────────────────────────────────────────

export interface FreePoolModelEntry {
  offer_id: number
  raw_model_name: string
  standardized_name?: string | null
  canonical_name?: string | null
  available: boolean
  billing_mode: string
  routing_tier: number
  catalog_code: string
  provider_name: string
  protocol: string
  base_url: string
  credential_id: number
  credential_label: string
  credential_status: string
  availability_state: string
  quota_state: string
  routable: boolean
}

export interface FreePoolProviderModel {
  offer_id: number
  raw_model_name: string
  standardized_name?: string | null
  available: boolean
  routable: boolean
  routing_tier: number
}

export interface FreePoolEntry {
  catalog_code: string
  provider_name: string
  credential_id: number
  credential_label: string
  credential_status: string
  availability_state: string
  quota_state: string
  total_offers: number
  available_offers: number
  free_offers: number
  has_secret?: boolean
  balance_usd?: number | null
  models?: FreePoolProviderModel[]
  model_names?: string[]
}

export interface FreePoolStatusResponse {
  pool: FreePoolEntry[]
  models: FreePoolModelEntry[]
  catalog: FreePoolCatalogEntry[]
  active_catalog_codes: string[]
  live_models_by_code: Record<string, string[]>
  stats: {
    total_providers: number
    available_providers: number
    total_models: number
    free_models: number
    routable_models: number
    catalog_templates: number
    catalog_registered: number
  }
}

export function getFreePoolStatus() {
  return req<FreePoolStatusResponse>('GET', '/api/free-pool/status')
}

export function registerFreeProvider(data: {
  catalog_code: string
  display_name?: string
  base_url: string
  protocol?: string
  api_key?: string
  models?: string[]
  no_api_key_required?: boolean
}) {
  return req<{ status: string; provider_id: number }>('POST', '/api/free-pool/register', data)
}

export interface FreePoolCatalogEntry {
  catalog_code: string
  display_name: string
  base_url: string
  models: string[]
  live_models: string[]
  model_count_template: number
  model_count_live: number
  pool_registered: boolean
  rpm_limit: number
  signup_url: string
  env_vars: string[]
  tags: string[]
  acquisition_mode: string
  needs_key: boolean
  env_configured: boolean
}

export function getFreePoolModels() {
  return req<{ models: FreePoolModelEntry[]; total: number; routable: number }>(
    'GET',
    '/api/free-pool/models',
  )
}

export function getFreePoolCatalog() {
  return req<{ providers: FreePoolCatalogEntry[] }>('GET', '/api/free-pool/catalog')
}

export function importFreePoolEnv() {
  return req<{ mode: string; registered: number; results: unknown[] }>('POST', '/api/free-pool/import-env')
}

export function bridgeFreePoolOAuth() {
  return req<{ mode: string; registered: number; results: unknown[] }>('POST', '/api/free-pool/bridge-oauth')
}

export function discoverFreePool() {
  return req<{ registered: number; acquisition: unknown }>('POST', '/api/free-pool/discover')
}

export function bootstrapFreePool() {
  return req<{ cleanup: unknown; mirror: unknown; discover: unknown; status: FreePoolStatusResponse }>(
    'POST',
    '/api/free-pool/bootstrap',
  )
}

export interface FreePoolMethod {
  mode: string
  title: string
  summary: string
  steps: string[]
  risk: string
  automated: boolean
}

export interface FreePoolAuditRule {
  id: string
  title: string
  status: string
  detail: string
}

export interface FreePoolKeyEntry {
  credential_id: number
  credential_label: string
  credential_status: string
  availability_state: string
  quota_state: string
  acquisition_source: string | null
  acquisition_detail: string | null
  tags: string[] | null
  has_secret: boolean
  key_masked: string | null
  provider_id: number
  catalog_code: string
  provider_name: string
  base_url: string
  created_at?: string
  updated_at?: string
}

export function getFreePoolKeys() {
  return req<{ keys: FreePoolKeyEntry[]; total: number }>('GET', '/api/free-pool/keys')
}

export function addFreePoolKey(data: {
  catalog_code: string
  api_key: string
  source?: string
  source_detail?: string
  label?: string
  display_name?: string
  base_url?: string
  models?: string[]
}) {
  return req<{ status: string; credential_id?: number }>('POST', '/api/free-pool/keys', data)
}

export function addFreePoolKeysBulk(keys: Array<{
  catalog_code: string
  api_key: string
  source?: string
  source_detail?: string
  label?: string
}>) {
  return req<{ registered: number; results: unknown[] }>('POST', '/api/free-pool/keys/bulk', { keys })
}

export function getFreePoolMethods() {
  return req<{
    methods: FreePoolMethod[]
    audit_rules: FreePoolAuditRule[]
    scheduler: { enabled: boolean; interval_sec: number; last_result: Record<string, unknown> }
  }>('GET', '/api/free-pool/methods')
}

export interface SignupPlatformEntry {
  id: string
  name: string
  category: string
  signup_url: string
  api_key_url: string
  base_url: string
  catalog_code: string
  display_name: string
  models_hint: string
  notes: string
  difficulty: string
  needs_email: boolean
  env_vars: string[]
  tags: string[]
  pool_registered: boolean
}

export interface SignupToolEntry {
  id: string
  name: string
  tool_type: string
  url: string
  description: string
  builtin: boolean
}

export interface SignupHubResponse {
  platforms: SignupPlatformEntry[]
  tools: SignupToolEntry[]
  workflow: Array<{ step: number; title: string; detail: string }>
  categories: Array<{ id: string; label: string; description: string }>
}

export function getFreePoolSignupHub() {
  return req<SignupHubResponse>('GET', '/api/free-pool/signup-hub')
}

export function probeFreePoolCredential(data: { base_url: string; api_key?: string }) {
  return req<{ probe: Record<string, unknown> }>('POST', '/api/free-pool/probe', data)
}

export function quickEntryFreePool(data: {
  signup_url?: string
  base_url: string
  api_key?: string
  display_name?: string
  catalog_code?: string
  models?: string[]
  source?: string
  source_detail?: string
  label?: string
  platform_id?: string
  probe_first?: boolean
  save?: boolean
  no_api_key_required?: boolean
}) {
  return req<{
    status: string
    probe?: Record<string, unknown>
    catalog_code?: string
    credential_id?: number
    provider_id?: number
    error?: string
  }>('POST', '/api/free-pool/quick-entry', data)
}

export function createFreePoolTempEmail() {
  return req<{
    ok: boolean
    address?: string
    password?: string
    token?: string
    web_url?: string
    expires_hint?: string
    error?: string
  }>('POST', '/api/free-pool/temp-email')
}

export function pollFreePoolTempEmail(token: string) {
  return req<{ ok: boolean; messages?: Array<{ id: string; from?: string; subject?: string; intro?: string }>; total?: number; error?: string }>(
    'POST',
    '/api/free-pool/temp-email/poll',
    { token },
  )
}

// ── System health ─────────────────────────────────────────────────────────

export interface HealthResponse {
  status: string
  version: string
  proxy?: {
    proxy: string
    healthy: boolean
    health_done: boolean
    domestic: string[]
  }
}

export function getHealth(full = false) {
  return req<HealthResponse>('GET', `/healthz${full ? '?full=true' : ''}`)
}

// ── Compression Stats (2026-06-20 P2) ──────────────────────────────────────

export interface HourBucket {
  hour: string
  total: number
  compressed: number
  rate: number
}

export interface CompressionStats {
  total_requests: number
  compressed_total: number
  compression_rate: number
  strategy_distribution: Record<string, number>
  total_outbound_tokens?: number
  estimated_original_tokens?: number
  estimated_tokens_saved?: number
  hourly_series: HourBucket[]
}

export function getCompressionStats(params: { hours?: number; from?: string; to?: string } = {}) {
  const qs = new URLSearchParams()
  if (params.hours) qs.set('hours', String(params.hours))
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  const s = qs.toString()
  return req<CompressionStats>('GET', `/api/admin/compression/stats${s ? '?' + s : ''}`)
}

export interface CompressionSessionItem {
  gw_session_id: string
  compression_strategy: string
  request_count: number
  first_ts: string
  last_ts: string
  outbound_msg_count: number | null
  outbound_token_est: number | null
  estimated_original_msgs: number | null
  msg_reduction: number | null
  sample_request_id: string
}

export interface CompressionSessionsResponse {
  items: CompressionSessionItem[]
  count: number
}

export function getCompressionSessions(params: {
  hours?: number
  from?: string
  to?: string
  strategy?: string
  page?: number
  page_size?: number
} = {}) {
  const qs = new URLSearchParams()
  if (params.hours) qs.set('hours', String(params.hours))
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.strategy) qs.set('strategy', params.strategy)
  if (params.page) qs.set('page', String(params.page))
  if (params.page_size) qs.set('page_size', String(params.page_size))
  const s = qs.toString()
  return req<CompressionSessionsResponse>('GET', `/api/admin/compression/sessions${s ? '?' + s : ''}`)
}

// ── User Management (JWT) ──────────────────────────────────────────────────


export interface UserListItem {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  last_login_at: string | null
  created_at: string
}

export function getUsers() {
  return req<UserListItem[]>('GET', '/api/users')
}

export function createUser(data: {
  username: string
  password: string
  tenant_id?: string
  display_name?: string
  email?: string
  role?: string
}) {
  return req<UserListItem>('POST', '/api/users', data)
}

export function updateUser(id: number, data: {
  display_name?: string
  email?: string
  role?: string
  enabled?: boolean
  password?: string
}) {
  return req<UserListItem>('PUT', `/api/users/${id}`, data)
}

export function deleteUser(id: number) {
  return req<{ status: string }>('DELETE', `/api/users/${id}`)
}

export function resetUserPassword(id: number, password: string) {
  return req<{ status: string }>('PUT', `/api/users/${id}/password`, { password })
}

export function getAuthMe() {
  return req<UserInfo>('GET', '/api/auth/me')
}

export function changeMyPassword(old_password: string, new_password: string) {
  return req<{ status: string }>('PUT', '/api/auth/change-password', { old_password, new_password })
}

// ── Audit Logs (super_admin only) ──────────────────────────────────────────

export interface AuditLogEntry {
  id: number
  ts: string
  actor: string
  action: string
  target_type?: string
  target_id?: number
  before_json?: any
  after_json?: any
}

export function getAuditLogs(params: {
  page?: number
  size?: number
  actor?: string
  action?: string
  from?: string  // RFC3339
  to?: string
} = {}) {
  const q = new URLSearchParams()
  if (params.page) q.set('page', String(params.page))
  if (params.size) q.set('size', String(params.size))
  if (params.actor) q.set('actor', params.actor)
  if (params.action) q.set('action', params.action)
  if (params.from) q.set('from', params.from)
  if (params.to) q.set('to', params.to)
  const qs = q.toString()
  return req<{ total: number; page: number; size: number; entries: AuditLogEntry[] }>(
    'GET', '/api/admin/audit-logs' + (qs ? '?' + qs : '')
  )
}

// ── Tenant Management (super_admin only) ─────────────────────────────────

export interface Tenant {
  code: string
  name: string
  status: string  // active | trial | suspended | expired | disabled
  description: string
  contact_email: string
  created_at: string
  updated_at: string
  user_count?: number
  api_key_count?: number
  requests_7d?: number
  tokens_7d?: number
  credits_7d?: number
  cost_7d_usd?: number
  total_requests?: number
}

export interface CreateTenantResponse extends Tenant {
  default_admin?: TenantUser
  initial_password?: string
}

export function getTenantsAdmin(status?: string) {
  const qs = status ? '?status=' + status : ''
  return req<Tenant[]>('GET', '/api/admin/tenants' + qs)
}

export function getTenant(code: string) {
  return req<Tenant>('GET', `/api/admin/tenants/${code}`)
}

export function createTenant(data: {
  code: string
  name: string
  status?: string
  description?: string
  contact_email?: string
}) {
  return req<CreateTenantResponse>('POST', '/api/admin/tenants', data)
}

export function updateTenant(code: string, data: {
  name?: string
  status?: string
  description?: string
  contact_email?: string
}) {
  return req<Tenant>('PATCH', `/api/admin/tenants/${code}`, data)
}

export interface TenantUser {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  last_login_at: string | null
  created_at: string
}

export function getTenantUsers(code: string) {
  return req<TenantUser[]>('GET', `/api/admin/tenants/${code}/users`)
}

export interface TenantKey {
  id: number
  tenant_id: string
  key_prefix: string
  key_alias: string
  owner_user: string
  enabled: boolean
  status: string
  application_id: number
  application_code: string
  total_requests: number
  total_cost_usd: number
  expires_at: string | null
  created_at: string
}

export function getTenantKeys(code: string) {
  return req<TenantKey[]>('GET', `/api/admin/tenants/${code}/keys`)
}

export interface TenantStats {
  days: number
  total_requests: number
  total_tokens: number
  total_credits: number
  total_cost_usd: number
  unique_keys: number
  unique_models: number
  unique_apps: number
  by_model: Array<{ model: string; requests: number; tokens: number; credits: number; cost_usd: number }>
  by_application: Array<{ application_code: string; requests: number; tokens: number; credits: number; cost_usd: number }>
}

export function getTenantStats(code: string, days?: number) {
  const qs = days ? '?days=' + days : ''
  return req<TenantStats>('GET', `/api/admin/tenants/${code}/stats` + qs)
}

export const TENANT_STATUSES = ['active', 'trial', 'suspended', 'expired', 'disabled'] as const
export const TENANT_STATUS_LABELS: Record<string, string> = {
  active: '正常',
  trial: '试用',
  suspended: '暂停',
  expired: '过期',
  disabled: '已禁用',
}
export const TENANT_STATUS_COLORS: Record<string, string> = {
  active: 'badge-green',
  trial: 'badge-blue',
  suspended: 'badge-yellow',
  expired: 'badge-gray',
  disabled: 'badge-red',
}

// ─────────────────────────────────────────────────────────────────
// Auto-route A/B strategy API (Phase 5 strategy breakdown)
// ─────────────────────────────────────────────────────────────────

export interface StrategySummaryRow {
  strategy: string
  total: number
  avg_quality: number
  avg_success: number
  avg_latency: number
  avg_cost: number
  drift_rate: number
}

export interface StrategyBreakdownRow {
  strategy: string
  task_type: string
  total: number
  avg_quality: number
  avg_success: number
}

export interface StrategyResponse {
  window_days: number
  summary: StrategySummaryRow[]
  breakdown: StrategyBreakdownRow[]
  ab_verdict: 'pattern_layered_wins' | 'baseline_heuristic_wins' | 'no_significant_difference' | 'insufficient_samples' | 'ab_test_disabled'
  ab_enabled: boolean
  ab_baseline_pct: number
  generated_at: string
}

export function getTuningStrategies(days = 7) {
  return req<StrategyResponse>('GET',
    `/api/admin/auto-route/tuning/strategies?days=${days}`)
}

// ── Memora session memory status ───────────────────────────────────────

export interface MemoraSinkStats {
  enqueued: number
  dropped: number
  processed: number
  errored: number
  queue_len: number
  queue_cap: number
  consecutive_errors: number
  last_error: string | null
  last_error_at: string | null
  paused?: boolean
}

export interface MemoraStatus {
  enabled: boolean
  base_url: string | null
  connected: boolean
  last_error: string | null
  last_error_at: string | null
  ping_latency_ms: number | null
  sink_paused?: boolean
  sink: MemoraSinkStats | null
}

export function getMemoraStatus(): Promise<MemoraStatus> {
  return req<MemoraStatus>('GET', '/api/system/memora-status')
}

export function pingMemora(): Promise<{ connected: boolean; latency_ms: number; error: string | null }> {
  return req('POST', '/api/system/memora-ping')
}

export function controlMemoraSink(action: 'pause' | 'resume'): Promise<{ paused: boolean; sink: MemoraSinkStats }> {
  return req('POST', '/api/system/memora-sink', { action })
}

export interface MemoraSession {
  task_id: string | null
  session_id: string | null
  title?: string | null
  no_topic: boolean
  no_topic_label: string | null
  hour_start?: string | null
  api_key_prefix: string | null
  api_key_owner_user: string | null
  application_code: string | null
  request_count: number
  ok_count: number
  fail_count: number
  first_activity: string
  last_activity: string
  latest_model: string | null
  memora_preview?: string | null
  memora_status?: 'ok' | 'empty' | 'error' | 'skipped' | null
}

export interface MemoraSessionsResponse {
  sessions: MemoraSession[]
  hours: number
  no_topic_window: number
  topic_count: number
  no_topic_count: number
}

export function getMemoraSessions(params: {
  q?: string
  hours?: number
  limit?: number
  owner_user?: string
  key_prefix?: string
  no_topic_window?: number
  include_no_topic?: boolean
  include_memora?: boolean
} = {}): Promise<MemoraSessionsResponse> {
  const qs = new URLSearchParams()
  if (params.q) qs.set('q', params.q)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.owner_user) qs.set('owner_user', params.owner_user)
  if (params.key_prefix) qs.set('key_prefix', params.key_prefix)
  if (params.no_topic_window != null) qs.set('no_topic_window', String(params.no_topic_window))
  if (params.include_no_topic === false) qs.set('include_no_topic', '0')
  if (params.include_memora) qs.set('include_memora', '1')
  const s = qs.toString()
  return req<MemoraSessionsResponse>('GET', `/api/system/memora-sessions${s ? '?' + s : ''}`)
}

export interface MemoraFact {
  id: string
  memory: string
  score: number
  tags: string[] | null
  kind?: 'text' | 'json'
  source?: 'task' | 'gw-session'
}

export interface ReadableBlock {
  id: string
  text: string
  kind: 'text' | 'json'
  source: 'task' | 'gw-session'
  tags?: string[] | null
  score?: number
}

export interface MemoraContextResponse {
  task_id: string
  user_id: string
  request_count: number
  latest_model?: string
  facts: MemoraFact[]
  readable_blocks?: ReadableBlock[]
  facts_visible?: number
  facts_written?: number
  hours?: number
  scoped_session_id?: string | null
  extracted_at?: string
  facts_search_error?: string
  title: string
}

export interface SessionScopeParams {
  hours?: number
  session_id?: string
}

function appendSessionScopeQS(qs: URLSearchParams, scope?: SessionScopeParams) {
  if (!scope) return
  if (scope.hours != null) qs.set('hours', String(scope.hours))
  if (scope.session_id) qs.set('session_id', scope.session_id)
}

export function getMemoraContext(taskId: string, scope?: SessionScopeParams): Promise<MemoraContextResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  const s = qs.toString()
  return req<MemoraContextResponse>(
    'GET',
    `/api/system/memora-context/${encodeURIComponent(taskId)}${s ? '?' + s : ''}`,
  )
}

export interface RequestMessage {
  ts: string
  request_id: string
  seq: number
  direction: 'user' | 'assistant'
  client_model: string | null
  outbound_model: string | null
  prompt_preview: string | null
  response_preview: string | null
  user_turn?: string | null
  assistant_text?: string | null
  tool_summary?: string | null
  prompt_tokens: number
  completion_tokens: number
  latency_ms: number
  cost_usd: number
  status: string | null
  error_kind?: string
}

export interface SessionMessagesResponse {
  task_id: string
  session_id: string | null
  messages: RequestMessage[]
  message_count?: number
  request_count?: number
  hours?: number
  hour_start?: string
  api_key_prefix?: string
  title?: string
  scoped_session_id?: string | null
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
}

export function getSessionMessages(
  taskId: string,
  scope?: SessionScopeParams,
  limit?: number,
): Promise<SessionMessagesResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  if (limit != null) qs.set('limit', String(limit))
  const s = qs.toString()
  return req<SessionMessagesResponse>(
    'GET',
    `/api/system/session-messages/${encodeURIComponent(taskId)}${s ? '?' + s : ''}`,
  )
}

export interface SessionExtractToMemoraResponse {
  task_id: string
  user_id: string
  project_id: string
  written: number
  skipped_noise: number
  skipped_duplicate: number
  memora_message_ids: string[]
  extracted_at: string
  samples: string[]
  error?: string
}

export interface SessionExtractionStatusResponse {
  task_id: string
  extracted: boolean
  extracted_at?: string
  written?: number
  skipped_noise?: number
  skipped_duplicate?: number
  status?: string
}

export function extractSessionToMemora(
  taskId: string,
  scope?: SessionScopeParams,
  dryRun = false,
): Promise<SessionExtractToMemoraResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  const s = qs.toString()
  return req<SessionExtractToMemoraResponse>(
    'POST',
    `/api/system/session-context/${encodeURIComponent(taskId)}/extract-to-memora${s ? '?' + s : ''}`,
    { dry_run: dryRun },
  )
}

export function getSessionExtractionStatus(taskId: string): Promise<SessionExtractionStatusResponse> {
  return req<SessionExtractionStatusResponse>(
    'GET',
    `/api/system/session-context/${encodeURIComponent(taskId)}/extraction-status`,
  )
}

export interface SessionTitleMeta {
  task_id: string
  scoped_session_id?: string
  log_count: number
  generated_at: string
  api_key_id: number
  model: string
}

export interface SessionTitleResponse {
  title: string
  meta: SessionTitleMeta
}

export function summarizeSessionTitle(
  taskId: string,
  scope?: SessionScopeParams,
): Promise<SessionTitleResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  const s = qs.toString()
  return req<SessionTitleResponse>(
    'POST',
    `/api/system/session-context/${encodeURIComponent(taskId)}/summarize-title${s ? '?' + s : ''}`,
    {},
  )
}

// ─────────────────────────────────────────────────────────────────
// No-topic session API (aggregated sessions without gw_task_id)
// ─────────────────────────────────────────────────────────────────

export interface NoTopicSessionParams {
  prefix: string
  hours?: number
  hour_start?: string
  limit?: number
}

export function getNoTopicSessionMessages(params: NoTopicSessionParams): Promise<SessionMessagesResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  if (params.limit != null) qs.set('limit', String(params.limit))
  return req<SessionMessagesResponse>('GET', `/api/system/no-topic-session/messages?${qs.toString()}`)
}

export function summarizeNoTopicSessionTitle(params: NoTopicSessionParams): Promise<SessionTitleResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  return req<SessionTitleResponse>('POST', `/api/system/no-topic-session/summarize-title?${qs.toString()}`, {})
}

export function extractNoTopicSessionToMemora(params: NoTopicSessionParams): Promise<SessionExtractToMemoraResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  return req<SessionExtractToMemoraResponse>('POST', `/api/system/no-topic-session/extract-to-memora?${qs.toString()}`, {})
}

export function getNoTopicSessionExtractionStatus(params: NoTopicSessionParams): Promise<SessionExtractionStatusResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  return req<SessionExtractionStatusResponse>('GET', `/api/system/no-topic-session/extraction-status?${qs.toString()}`)
}

// ─────────────────────────────────────────────────────────────────
// Auto-route correlations API (Phase 7.2.3 endpoint)
// ─────────────────────────────────────────────────────────────────

export interface CorrelationRow {
  label: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_cost_usd: number
  avg_quality?: number
}

export interface CorrelationRowMT {
  model: string
  task_type: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_cost_usd: number
}

export interface CorrelationVerdict {
  task_type: string
  model: string
  success_rate: number
  avg_latency_ms: number
  rank: number
}

export interface AutoRouteCorrelationsResponse {
  window_days: number
  by_model: CorrelationRow[]
  by_strategy: CorrelationRow[]
  by_task_type: CorrelationRow[]
  by_model_task: CorrelationRowMT[]
  verdict: CorrelationVerdict[]
  generated_at: string
}

export function getAutoRouteCorrelations(params: {
  days?: number
  min_samples?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.days) q.set('days', String(params.days))
  if (params.min_samples) q.set('min_samples', String(params.min_samples))
  const path = '/api/admin/auto-route/correlations' + (q.toString() ? '?' + q : '')
  return req<AutoRouteCorrelationsResponse>('GET', path)
}

// ── MaaS (积分计费) ─────────────────────────────────────────────────────

export interface MaasPublicSettings {
  cents_per_credit: number
  base_credits_per_1m: number
  base_credits_per_1m_in?: number
  base_credits_per_1m_out?: number
  base_credits_per_1m_cache_in?: number
  base_credits_per_1m_cache_out?: number
  global_discount?: number
  currency_display: string
}

export interface MaasAdminSettings extends MaasPublicSettings {
  alipay_account?: string
  wechat_mch_id?: string
  stub_alipay_qr_url?: string
  stub_wechat_qr_url?: string
}

export interface AdminMaasModelRate {
  canonical_id: number
  canonical_name: string
  display_name: string
  vendor: string
  family: string | null
  status: string
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
  manual_in: boolean
  manual_out: boolean
  manual_cache_in: boolean
  manual_cache_out: boolean
  custom_credits_per_1m_in: number | null
  custom_credits_per_1m_out: number | null
  custom_credits_per_1m_cache_in: number | null
  custom_credits_per_1m_cache_out: number | null
  is_custom: boolean
  updated_at: string | null
}

export interface MaasModelRateUpsert {
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
  manual_in: boolean
  manual_out: boolean
  manual_cache_in: boolean
  manual_cache_out: boolean
}

export interface AdminMaasModelRatesResponse {
  settings: MaasAdminSettings
  items: AdminMaasModelRate[]
}

export interface MaasModel {
  canonical_name: string
  display_name: string
  vendor: string
  family?: string | null
  family_display_name?: string | null
  context_window?: number | null
  modality: string
  billing_mode: string
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in?: number
  credits_per_1m_cache_out?: number
}

export interface MaasPlan {
  id: number
  code: string
  tier: string
  name: string
  price_cents: number
  monthly_credits: number
  enabled: boolean
  sort_order: number
}

export interface MaasTopupPackage {
  id: number
  code: string
  tier: string
  name: string
  price_cents: number
  credits_amount: number
  enabled: boolean
  sort_order: number
}

export interface MaasWallet {
  tenant_id: string
  quota_remaining: number
  granted_balance: number
  purchased_balance: number
  balance_credits: number
  total_available: number
  subscription?: {
    plan_id: number
    plan_name: string
    status: string
    period_start: string
    period_end: string
  }
}

export interface MaasBillingOrder {
  id: number
  order_no: string
  tenant_id: string
  order_type: 'subscribe' | 'topup'
  status: 'pending' | 'paid' | 'cancelled' | 'expired'
  amount_cents: number
  credits: number
  plan_id?: number
  package_id?: number
  plan_name?: string
  package_name?: string
  payment_channel: 'alipay' | 'wechat' | 'manual'
  qr_payload: string
  qr_url: string
  payment_hint?: string
  stub_mode?: boolean
  paid_at?: string
  expires_at: string
  note: string
  created_at: string
  updated_at: string
}

export interface MaasAccount {
  wallet: MaasWallet
  recent_ledger: MaasLedgerEntry[]
  recent_orders: MaasBillingOrder[]
}

export interface MaasLedgerEntry {
  id: number
  entry_type: string
  amount: number
  balance_after: number
  pool: string | null
  ref_type: string | null
  ref_id: string | null
  note: string
  created_at: string
}

export function getMaasSettings() {
  return req<MaasPublicSettings>('GET', '/api/maas/settings')
}

export function getAdminMaasSettings() {
  return req<MaasAdminSettings>('GET', '/api/admin/maas/settings')
}

export function updateAdminMaasSettings(body: {
  cents_per_credit: number
  base_credits_per_1m?: number
  base_credits_per_1m_in?: number
  base_credits_per_1m_out?: number
  base_credits_per_1m_cache_in?: number
  base_credits_per_1m_cache_out?: number
  global_discount?: number
  currency_display: string
}) {
  return req<{ status: string }>('PUT', '/api/admin/maas/settings', body)
}

export function getAdminMaasModelRates() {
  return req<AdminMaasModelRatesResponse>('GET', '/api/admin/maas/model-rates')
}

export function upsertAdminMaasModelRate(canonicalId: number, body: MaasModelRateUpsert) {
  return req<{ status: string }>('PUT', `/api/admin/maas/model-rates/${canonicalId}`, body)
}

export function resetAdminMaasModelRateFields(canonicalId: number, fields: string[]) {
  return req<{ status: string }>('PATCH', `/api/admin/maas/model-rates/${canonicalId}`, { fields })
}

export function deleteAdminMaasModelRate(canonicalId: number) {
  return req<{ status: string }>('DELETE', `/api/admin/maas/model-rates/${canonicalId}`)
}

export function getMaasModels() {
  return req<{ items: MaasModel[] }>('GET', '/api/maas/models')
}

export function getMaasPlans() {
  return req<{ items: MaasPlan[] }>('GET', '/api/maas/plans')
}

export function getMaasTopupPackages() {
  return req<{ items: MaasTopupPackage[] }>('GET', '/api/maas/topup-packages')
}

export function getMaasWallet() {
  return req<MaasWallet>('GET', '/api/maas/wallet')
}

export function getMaasAccount() {
  return req<MaasAccount>('GET', '/api/maas/account')
}

export function getMaasOrders(limit = 20) {
  return req<{ items: MaasBillingOrder[] }>('GET', `/api/maas/orders?limit=${limit}`)
}

export function getMaasOrder(id: number) {
  return req<MaasBillingOrder>('GET', `/api/maas/orders/${id}`)
}

export function createMaasOrder(body: {
  type: 'subscribe' | 'topup'
  plan_id?: number
  package_id?: number
  payment_channel?: 'alipay' | 'wechat'
}) {
  return req<MaasBillingOrder>('POST', '/api/maas/orders', body)
}

export function getMaasLedger(limit = 50) {
  return req<{ items: MaasLedgerEntry[] }>('GET', `/api/maas/ledger?limit=${limit}`)
}

export interface MaasUsageModelRow {
  model: string
  requests: number
  credits: number
  cost_usd?: number
}

export interface MaasUsageTrendRow {
  date: string
  requests: number
  credits: number
  cost_usd?: number
}

export interface MaasUsageSummary {
  days: number
  tenant_id: string
  total_requests: number
  total_credits: number
  total_cost_usd?: number
  by_model: MaasUsageModelRow[]
  trend: MaasUsageTrendRow[]
}

export function getMaasUsageSummary(days = 7, limit = 10) {
  const q = new URLSearchParams()
  q.set('days', String(days))
  q.set('limit', String(limit))
  return req<MaasUsageSummary>('GET', `/api/maas/usage/summary?${q.toString()}`)
}

export function getAdminMaasWallet(tenantCode: string) {
  return req<MaasWallet>('GET', `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/wallet`)
}

export function getAdminMaasAccount(tenantCode: string) {
  return req<MaasAccount>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/account`,
  )
}

export function getAdminMaasUsageSummary(tenantCode: string, days = 7, limit = 10) {
  const q = new URLSearchParams()
  q.set('days', String(days))
  q.set('limit', String(limit))
  return req<MaasUsageSummary>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/usage/summary?${q.toString()}`,
  )
}

export function getAdminMaasLedger(tenantCode: string, limit = 50) {
  return req<{ items: MaasLedgerEntry[] }>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/ledger?limit=${limit}`,
  )
}

export function adjustAdminMaasCredits(tenantCode: string, amount: number, note: string) {
  return req<{ status: string }>(
    'POST',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/adjust`,
    { amount, note },
  )
}

export function grantAdminMaasCredits(tenantCode: string, grantedCredits: number, note: string) {
  return req<{ status: string }>(
    'POST',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/grant`,
    { granted_credits: grantedCredits, note },
  )
}

export function getAdminMaasOrders(limit = 50) {
  return req<{ items: MaasBillingOrder[] }>('GET', `/api/admin/maas/orders?limit=${limit}`)
}

export function getAdminMaasTenantOrders(tenantCode: string, limit = 20) {
  return req<{ items: MaasBillingOrder[] }>(
    'GET',
    `/api/admin/maas/tenants/${encodeURIComponent(tenantCode)}/orders?limit=${limit}`,
  )
}

export function confirmAdminMaasOrder(orderId: number, note = '') {
  return req<{ status: string }>('POST', `/api/admin/maas/orders/${orderId}/confirm`, { note })
}

export const MAAS_LEDGER_TYPE_LABELS: Record<string, string> = {
  consume: '消耗',
  topup: '充值',
  subscribe: '订阅',
  adjust: '调整',
  refund: '退款',
}

export const MAAS_POOL_LABELS: Record<string, string> = {
  subscription_quota: '订阅额度',
  granted: '信用积分',
  purchased: '充值积分',
}

export const MAAS_ORDER_STATUS_LABELS: Record<string, string> = {
  pending: '待支付',
  paid: '已支付',
  cancelled: '已取消',
  expired: '已过期',
}

// ─────────────────────────────────────────────────────────────────
// Routing overrides API (Phase 7.6 endpoint)
// ─────────────────────────────────────────────────────────────────

export interface RoutingOverride {
  id: number
  task_type: string
  profile: string
  mode: 'pin' | 'ban'
  model_chosen?: string
  reason: string
  created_by?: string
  expires_at?: string
  created_at: string
  updated_at: string
}

export interface RoutingOverridesResponse {
  overrides: RoutingOverride[]
  count: number
  filter: { task_type: string; profile: string; active: string }
}

export interface RoutingOverrideCreate {
  task_type: string
  profile?: string
  mode: 'pin' | 'ban'
  model_chosen?: string
  reason: string
  expires_at?: string
}

export function getRoutingOverrides(params: {
  active?: boolean
  task_type?: string
  profile?: string
} = {}) {
  const q = new URLSearchParams()
  if (params.active) q.set('active', 'true')
  if (params.task_type) q.set('task_type', params.task_type)
  if (params.profile) q.set('profile', params.profile)
  const path = '/api/admin/routing/overrides' + (q.toString() ? '?' + q : '')
  return req<RoutingOverridesResponse>('GET', path)
}

export function createRoutingOverride(body: RoutingOverrideCreate) {
  return req<{ id: number; status: string; message: string }>('POST',
    '/api/admin/routing/overrides', body)
}

export function deleteRoutingOverride(id: number) {
  return req<{ id: number; status: string; note: string }>('DELETE',
    `/api/admin/routing/overrides/${id}`)
}

export function extendRoutingOverride(id: number, expires_at: string | null) {
  return req<{ id: number; status: string }>('PATCH',
    `/api/admin/routing/overrides/${id}/extend`, { expires_at })
}

// ─────────────────────────────────────────────────────────────────
// Quality correlations API (Phase 8.2 endpoint)
// ─────────────────────────────────────────────────────────────────

export interface QualityCorrelationRow {
  bucket: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_quality: number
  avg_cost_usd: number
}

export interface QualityCorrelationInsight {
  predictor: 'prompt_length' | 'tools' | 'images' | 'code_block'
  buckets: number
  samples: number
  correlation: number
  abs_r: number
  interpretation: string
}

export interface QualityCorrelationResponse {
  window_days: number
  by: string
  breakdown: QualityCorrelationRow[]
  insights: QualityCorrelationInsight[]
  generated_at: string
}

export function getQualityCorrelations(params: {
  days?: number
  by?: 'prompt_length' | 'tools' | 'images' | 'code_block'
} = {}) {
  const q = new URLSearchParams()
  if (params.days) q.set('days', String(params.days))
  if (params.by) q.set('by', params.by)
  const path = '/api/admin/auto-route/quality-correlations' + (q.toString() ? '?' + q : '')
  return req<QualityCorrelationResponse>('GET', path)
}

// ─────────────────────────────────────────────────────────────────
// Routing overrides audit API (Phase 7.9 endpoint)
// ─────────────────────────────────────────────────────────────────

export interface RoutingAuditEntry {
  id: number
  ts: string
  action: 'insert' | 'update' | 'delete'
  override_id?: number
  task_type?: string
  profile?: string
  mode?: string
  model_chosen?: string
  reason?: string
  expires_at?: string
  old_expires_at?: string
  actor?: string
}

export interface RoutingAuditResponse {
  entries: RoutingAuditEntry[]
  count: number
  filter: { action: string; actor: string; override_id: string; days: string }
}

export function getRoutingAudit(params: {
  action?: 'insert' | 'update' | 'delete' | ''
  actor?: string
  override_id?: number
  days?: number
  limit?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.action) q.set('action', params.action)
  if (params.actor) q.set('actor', params.actor)
  if (params.override_id) q.set('override_id', String(params.override_id))
  if (params.days) q.set('days', String(params.days))
  if (params.limit) q.set('limit', String(params.limit))
  const path = '/api/admin/routing/overrides/audit' + (q.toString() ? '?' + q : '')
  return req<RoutingAuditResponse>('GET', path)
}

// ─────────────────────────────────────────────────────────────────
// Data Lifecycle Management API
// ─────────────────────────────────────────────────────────────────

export interface DataSegment {
  rows: number
  size_bytes: number
  size_human: string
  days: number
  percent_of_total: number
}

export interface TenantDataStats {
  tenant_id: string
  rows: number
  size_bytes: number
  size_human: string
}

export interface DailyGrowth {
  date: string
  requests: number
  compressed: number
  compression_rate: number
}

export interface DataLifecycleStatsResponse {
  total_rows: number
  total_size_bytes: number
  total_size_human: string
  hot_data: DataSegment | null
  warm_data: DataSegment | null
  cold_data: DataSegment | null
  expired_data: DataSegment | null
  by_tenant: TenantDataStats[]
  growth_trend: DailyGrowth[]
}

export function dataLifecycleStats() {
  return req<DataLifecycleStatsResponse>('GET', '/api/admin/data-lifecycle/stats')
}

export interface CleanupPreviewResponse {
  affected_rows: number
  estimated_freed_bytes: number
  estimated_freed_human: string
  warning_message?: string
}

export function dataLifecycleCleanupPreview(
  action: string,
  from: string,
  to: string
) {
  return req<CleanupPreviewResponse>('POST', '/api/admin/data-lifecycle/cleanup/preview', {
    action,
    from,
    to
  })
}

export interface DataLifecycleMetricsResponse {
  total_rows: number
  total_size_bytes: number
  hot_data_rows: number
  hot_data_size_bytes: number
  warm_data_rows: number
  warm_data_size_bytes: number
  cold_data_rows: number
  cold_data_size_bytes: number
  expired_data_rows: number
  expired_data_size_bytes: number
  last_cleanup_at?: string
  last_archive_at?: string
}

export function dataLifecycleMetrics() {
  return req<DataLifecycleMetricsResponse>('GET', '/api/admin/data-lifecycle/metrics')
}

// ── Settings Management (2026-06-20) ───────────────────────────────────

export type SettingType = 'enum' | 'int' | 'float' | 'bool' | 'string' | 'url' | 'duration'
export type SettingScope = 'platform' | 'tenant'

export interface SettingSpec {
  key: string
  env_name: string
  type: SettingType
  scope: SettingScope
  category: string
  default: any
  options?: string[]
  min?: number
  max?: number
  description: string
  danger_level: 0 | 1 | 2 | 3
  hot_reload: boolean
  observability?: string
}

export interface SettingItem extends SettingSpec {
  value: any
  source: 'db' | 'env' | 'default' | ''
}

export interface SettingAuditEntry {
  id: number
  setting_key: string
  tenant_id?: string
  action: 'update' | 'rollback' | 'delete'
  old_value?: any
  new_value?: any
  operator_user: string
  operator_role: string
  client_ip?: string
  created_at: string
}

export function listSettings(params: { category?: string } = {}) {
  const qs = new URLSearchParams()
  if (params.category) qs.set('category', params.category)
  const s = qs.toString()
  return req<{ items: SettingItem[] }>('GET', `/api/admin/settings${s ? '?' + s : ''}`)
}

export function getSetting(key: string) {
  return req<{ spec: SettingSpec; value: any; source: string }>('GET', `/api/admin/settings/${key}`)
}

export function updateSetting(key: string, body: { value: any }) {
  return req<{ status: string; old_value?: any; new_value: any; applied_at: string }>(
    'PUT', `/api/admin/settings/${key}`, body)
}

export function rollbackSetting(key: string) {
  return req<{ status: string; rolled_back_to: any }>('POST', `/api/admin/settings/${key}/rollback`)
}

export function getSettingHistory(key: string) {
  return req<{ items: SettingAuditEntry[] }>('GET', `/api/admin/settings/${key}/history`)
}

export function updateTenantSetting(tenantID: string, key: string, body: { value: any }) {
  return req<{ status: string; new_value: any }>(
    'PUT', `/api/admin/tenant-settings/${encodeURIComponent(tenantID)}/${key}`, body)
}

// ── Tenant model policy (Round 48, 2026-06-21) ──────────────────────

export interface TenantModelPolicy {
  id: number
  tenant_id: string
  canonical_name: string
  reason: string
  created_by: string
  deleted_at: string | null
  deleted_by: string | null
  created_at: string
  updated_at: string
}

export interface TenantModelPolicyListResp {
  policies: TenantModelPolicy[]
  count: number
  tenant: string
}

export interface TenantModelPolicyCheckResp {
  exists: boolean
  canonical_name: string
  family?: string
  vendor?: string
  modality?: string
}

export interface TenantModelPolicyAuditEntry {
  id: number
  ts: string
  action: 'insert' | 'update' | 'delete' | 'undelete'
  policy_id: number | null
  tenant_id: string
  canonical_name: string
  reason: string
  actor: string
}

export function listTenantModelPolicies(tenantCode: string, opts: { includeDeleted?: boolean } = {}) {
  const qs = opts.includeDeleted ? '?include_deleted=true' : ''
  return req<TenantModelPolicyListResp>(
    'GET', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies${qs}`)
}

export function createTenantModelPolicy(
  tenantCode: string,
  body: { canonical_name: string; reason: string },
) {
  return req<{ id: number; status: string; policy: TenantModelPolicy; message: string }>(
    'POST', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies`, body)
}

export function patchTenantModelPolicy(
  tenantCode: string, id: number, body: { reason: string },
) {
  return req<TenantModelPolicy>(
    'PATCH', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/${id}`, body)
}

export function deleteTenantModelPolicy(tenantCode: string, id: number) {
  return req<{ id: number; status: string; message: string }>(
    'DELETE', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/${id}`)
}

export function undeleteTenantModelPolicy(tenantCode: string, id: number) {
  return req<{ id: number; status: string; message: string }>(
    'POST', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/${id}/undelete`)
}

export function checkTenantModelPolicy(
  tenantCode: string, body: { canonical_name: string },
) {
  return req<TenantModelPolicyCheckResp>(
    'POST', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/check`, body)
}

export function listTenantModelPoliciesAudit(tenantCode: string, limit = 100) {
  return req<{ audit: TenantModelPolicyAuditEntry[]; count: number }>(
    'GET', `/api/admin/tenants/${encodeURIComponent(tenantCode)}/model-policies/audit?limit=${limit}`)
}

// ── Pending response cache (Track C client-side resume, 2026-06-21) ──
//
// When the client disconnects mid-stream (IDE crash, browser close, mobile
// background-then-foreground), the gateway's pending-store capturer keeps
// reading upstream and caches the full SSE response keyed by
// (sessionID, requestID). On reconnect the client calls this function to
// recover the cached body without re-sending the LLM request.
//
// See sessions/handler.go:381 (server endpoint) and relay/stream.go:74
// (Track C C2 capturer) for the server side. The cached entry has TTL
// 7 days (pending/pending.go DefaultTTL) and a 1 MiB body cap.

export type PendingResponseStatus = 'completed' | 'in_progress' | 'failed' | 'not_found'

export interface PendingResponse {
  status: PendingResponseStatus
  /** SSE text (when contentType is text/event-stream) or JSON body */
  body?: string
  contentType?: string
  errorMessage?: string
}

/**
 * GET /v1/sessions/{sessionID}/pending-response
 *
 * Returns the cached vendor response for the most recent request under
 * this session (or a specific request_id if supplied). The 200 OK path
 * carries the full SSE body in `body`; 202 means still in-flight;
 * 404 / 503 mean nothing to resume.
 *
 * Failures (network, 5xx, malformed JSON) collapse to `not_found` so
 * callers can simply `if (status === 'completed')` without worrying
 * about exception handling. The cache is best-effort.
 */
export async function getPendingResponse(
  sessionId: string,
  apiKey: string,
  requestId?: string,
): Promise<PendingResponse> {
  if (!sessionId) return { status: 'not_found' }
  try {
    const qs = requestId ? `?request_id=${encodeURIComponent(requestId)}` : ''
    const resp = await fetch(
      `/v1/sessions/${encodeURIComponent(sessionId)}/pending-response${qs}`,
      {
        method: 'GET',
        headers: { Authorization: `Bearer ${apiKey}` },
      },
    )
    if (resp.status === 404 || resp.status === 503) return { status: 'not_found' }
    if (resp.status === 202) return { status: 'in_progress' }
    if (!resp.ok) return { status: 'not_found' }

    // 200 — body is the replayed vendor response. The X-Gw-Pending-Replay
    // header is the canonical signal that this is a replay (vs some
    // accidental 200 with empty body). Without it we treat as not_found
    // to avoid swallowing unrelated 200s.
    if (resp.headers.get('X-Gw-Pending-Replay') !== 'true') {
      return { status: 'not_found' }
    }
    const ct = resp.headers.get('Content-Type') ?? ''
    const body = await resp.text()
    if (ct.includes('text/event-stream')) {
      return { status: 'completed', body, contentType: ct }
    }
    // Non-SSE 200 — could be a JSON status envelope (e.g. failed entry).
    try {
      const obj = JSON.parse(body) as { status?: string; error_message?: string; body?: string }
      if (obj.status === 'failed') {
        return { status: 'failed', errorMessage: obj.error_message, contentType: ct }
      }
      if (obj.status === 'completed' && typeof obj.body === 'string') {
        return { status: 'completed', body: obj.body, contentType: ct }
      }
    } catch {
      /* fall through */
    }
    return { status: 'not_found' }
  } catch {
    // Network errors / CORS / aborted — treat as cache miss so the
    // caller falls through to a normal request.
    return { status: 'not_found' }
  }
}
