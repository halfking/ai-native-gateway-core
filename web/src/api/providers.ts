import { req } from './_core'

// providers.ts — v6.0 audit T12 (2026-06-22)
// Provider CRUD + the GET/POST dual-mode helper + BackgroundTask polling
// + ProviderDetail + per-provider model-list refresh + credential
// long-running operations.
//
// Most "long-running" operations (refresh-models, diagnose, credential
// check) follow a 2-step pattern: POST returns { task_id }, the caller
// polls /api/tasks/{id} until status != 'running'. The pollTask helper
// does that loop with a 2s tick + 120s cap.

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
  // Optional fields populated by /api/providers/.../{id} (ProviderDetail)
  // but not by the /api/providers list endpoint. The provider list
  // view falls back to N/A when these are missing.
  protocol?: string | null
  category?: string | null
  vendor_name?: string | null
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

export interface ProbeURLResult {
  reachable: boolean
  protocol?: string
  http_status?: number
  models_count?: number
  sample_models?: string[]
  auth_ok?: boolean
  error?: string
}

export function probeURL(data: { base_url: string; api_key?: string }) {
  return req<ProbeURLResult>('POST', '/api/providers/probe-url', data)
}

export function probeProviderURL(providerId: number) {
  return req<ProbeURLResult>('POST', `/api/providers/${providerId}/probe-url`)
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
  // Optional display name (ModelsTab falls back to cred.name when
  // cred.label is empty). Backend may or may not populate it.
  name?: string | null
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
  // Per-credential USD balance (from /api/providers/{id}/credentials
  // detail endpoint). Optional because some legacy list paths omit it.
  balance_usd?: number | string | null
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

export function addCredential(providerId: number, data: { api_key: string; label?: string }) {
  return req<{ id: number }>('POST', `/api/providers/${providerId}/credentials`, data)
}

export function deleteCredential(providerId: number, credId: number) {
  return req<void>('DELETE', `/api/providers/${providerId}/credentials/${credId}`)
}

export function updateCredential(providerId: number, credId: number, data: Partial<{
  label: string
  status: CredentialStatus
  concurrency_limit: number | null
  effective_at: string | null
  expires_at: string | null
  balance_usd: number | null
  tags: string[]
  notes: string
}>) {
  return req<{ message: string }>('PATCH', `/api/providers/${providerId}/credentials/${credId}`, data)
}

export function getCredentialUsage(providerId: number, credId: number, days = 7) {
  return req<CredentialUsage>('GET', `/api/providers/${providerId}/credentials/${credId}/usage?days=${days}`)
}

export function revealCredentialKey(providerId: number, credId: number) {
  return req<{ credential_id: number; api_key: string }>('POST', `/api/providers/${providerId}/credentials/${credId}/reveal`)
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

export function startCredentialCheck(providerId: number, credId: number) {
  return req<{ task_id: number; status: string }>('POST', `/api/providers/${providerId}/credentials/${credId}/check`)
}

export async function checkCredential(providerId: number, credId: number) {
  const { task_id } = await startCredentialCheck(providerId, credId)
  const task = await pollTask(task_id)
  if (task.status === 'failed') {
    throw new Error(task.error || 'credential check failed')
  }
  return (task.result ?? {}) as CredentialCheckResult
}

export async function checkCredentialHealth(providerId: number, credId: number) {
  const { task_id } = await startCredentialCheck(providerId, credId)
  return pollTask(task_id)
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

export function resetCredentialFpSlots(providerId: number, credId: number) {
  return req<{ message: string; deleted_slots: number; deleted_pins: number }>(
    'POST', `/api/providers/${providerId}/credentials/${credId}/reset-fp-slots`
  )
}

export interface FpSlotDetail {
  index: number
  holder: string
  ttl_seconds: number
  expired: boolean
  memory_mode?: boolean
  session_title?: string
  session_id?: string
}

export interface FpSlotStats {
  credential_id: number
  slot_limit?: number
  healthy_slots: number
  occupied_slots: number
  free_slots?: number
  holders?: string[]
  details?: FpSlotDetail[]
  unlimited?: boolean
  message?: string
}

export function getCredentialFpSlotStats(providerId: number, credId: number) {
  return req<FpSlotStats>(
    'GET', `/api/providers/${providerId}/credentials/${credId}/fp-slot-stats`
  )
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

export function startDiagnose(providerId: number, opts: { force?: boolean } = {}) {
  return req<{ task_id: number; status: string }>('POST', `/api/providers/${providerId}/diagnose`, opts)
}

export function getDiagnoseResult(providerId: number) {
  return req<any>('GET', `/api/providers/${providerId}/diagnose`)
}

export async function diagnoseProvider(providerId: number, opts: { force?: boolean } = {}) {
  const { task_id } = await startDiagnose(providerId, opts)
  return pollTask(task_id)
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

// ── Provider logs (one entry per request log row, scoped to a provider) ──

export interface ProviderLogEntry {
  ts: string | null
  request_id: string | null
  credential_id: number | null
  credential_label?: string | null
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

// ── Diagnose response shape (full snapshot from /diagnose endpoint) ──────

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