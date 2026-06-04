import { store } from './store'

const BASE = ''  // same origin in prod; proxied in dev

function headers(): Record<string, string> {
  const h: Record<string, string> = { 'Content-Type': 'application/json' }
  if (store.apiKey) h['Authorization'] = `Bearer ${store.apiKey}`
  return h
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(BASE + path, {
    method,
    headers: headers(),
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (r.status === 401) {
    // Clear invalid/stale session (but don't force redirect - let user re-login manually)
    import('./store').then(({ clearApiKey }) => clearApiKey())
    throw new Error('Unauthorized')
  }
  if (!r.ok) {
    const msg = await r.text().catch(() => r.statusText)
    throw new Error(msg || r.statusText)
  }
  if (r.status === 204) return undefined as T
  return r.json()
}

// ── Auth ──────────────────────────────────────────────────────────────────

export interface LoginResponse {
  api_key: string
  key_prefix: string
  message: string
}

export function login(username: string, password: string) {
  return req<LoginResponse>('POST', '/api/auth/token', { username, password })
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
  code: string
  catalog_code: string | null
  display_name: string
  enabled: boolean
  base_url: string | null
  protocol: string
  header_profile_code?: string | null
  vendor_name?: string | null
  notes: string | null
  active_credential_count: number
  healthy_credential_count?: number
  warning_credential_count?: number
  unreachable_credential_count?: number
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
}

export function getProviders() {
  return req<Provider[]>('GET', '/api/providers')
}

export function createProvider(data: { catalog_code: string; display_name?: string; base_url?: string; notes?: string; protocol?: string }) {
  return req<{ id: number; message: string }>('POST', '/api/providers', data)
}

export function updateProvider(id: number, data: { display_name?: string; base_url?: string; protocol?: string; notes?: string }) {
  return req<{ message: string }>('PATCH', `/api/providers/${id}`, data)
}

export function toggleProvider(id: number) {
  return req<{ id: number; enabled: boolean }>('PATCH', `/api/providers/${id}/toggle`)
}

export function checkProvider(id: number) {
  return req<{ accepted: boolean; reason: string; run?: { id: number; status: string } }>('POST', `/api/providers/${id}/check`)
}

export function checkCredential(providerId: number, credId: number) {
  return req<CredentialCheckResult>('POST', `/api/providers/${providerId}/credentials/${credId}/check`)
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
  status: CredentialStatus
  health_status?: 'unknown' | 'healthy' | 'warning' | 'unreachable'
  health_checked_at?: string | null
  health_source?: 'models' | 'probe' | 'mixed' | 'none' | null
  health_warning_code?: string | null
  health_error?: string | null
  health_latency_ms?: number | null
  health_probe_model?: string | null
  trust_level: string
  concurrency_limit: number | null
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
  application_code: string
  default_client_profile?: string | null
  is_system?: boolean
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

export function getKey(id: number) {
  return req<ApiKey[]>('GET', `/api/keys?id=${id}`)
}

export function getKeyDetail(id: number) {
  return req<ApiKey>('GET', `/api/keys/detail/${id}`)
}

export function createKey(data: { application_code: string; owner_user?: string; budget_usd?: number; rate_limit_rpm?: number }) {
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

export function applyForKey(data: { application_code: string; owner_user?: string; description?: string }) {
  return req<{ id: number; key_prefix: string; application_code: string; status: string; message: string }>('POST', '/api/keys/apply', data)
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
  return req<KeyUsageSummary>('GET', `/api/usage/key/${keyId}${s ? '?' + s : ''}`)
}

export function getKeyUsageByModel(keyId: number, params: { days?: number; start?: string; end?: string; limit?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.days) qs.set('days', String(params.days))
  if (params.start) qs.set('start', params.start)
  if (params.end) qs.set('end', params.end)
  if (params.limit) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<ModelUsageForKey[]>('GET', `/api/usage/key/${keyId}/models${s ? '?' + s : ''}`)
}

export function getKeyUsageTrend(keyId: number, period: 'day' | 'week' | 'month' = 'day', days = 30) {
  return req<TrendEntry[]>('GET', `/api/usage/key/${keyId}/trend?period=${period}&days=${days}`)
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

export function resolveRouting(model: string, clientProfile?: string) {
  const qs = new URLSearchParams({ model })
  if (clientProfile) qs.set('client_profile', clientProfile)
  return req<RoutingResolveResponse>('GET', `/api/routing/resolve?${qs}`)
}

export function patchApplicationProfile(applicationCode: string, default_client_profile: string | null) {
  return req<{ id: number; code: string; default_client_profile: string | null }>(
    'PATCH',
    `/api/keys/applications/${encodeURIComponent(applicationCode)}/profile`,
    { default_client_profile },
  )
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
  unmapped: Array<{ raw_model_name: string; credential: RoutingTreeCredential }>
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
}

export function getDecisions(params: { model?: string; canonical?: string; success?: boolean; since_minutes?: number; limit?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.model) qs.set('model', params.model)
  if (params.canonical) qs.set('canonical', params.canonical)
  if (params.success !== undefined) qs.set('success', String(params.success))
  if (params.since_minutes !== undefined) qs.set('since_minutes', String(params.since_minutes))
  if (params.limit !== undefined) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<RoutingDecision[]>('GET', `/api/routing/decisions${s ? '?' + s : ''}`)
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
  latency_ms: number | null
  success: boolean
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
}

export interface RequestLogDetail extends RequestLogRow {
  request_body: any | null
  response_body: any | null
}

export function getRequestLogs(params: {
  api_key_id?: number
  provider_id?: number
  credential_id?: number
  identity_hash?: string
  from?: string
  to?: string
  q?: string
  limit?: number
} = {}) {
  const qs = new URLSearchParams()
  if (params.api_key_id != null) qs.set('api_key_id', String(params.api_key_id))
  if (params.provider_id != null) qs.set('provider_id', String(params.provider_id))
  if (params.credential_id != null) qs.set('credential_id', String(params.credential_id))
  if (params.identity_hash) qs.set('identity_hash', params.identity_hash)
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.q) qs.set('q', params.q)
  if (params.limit != null) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<RequestLogRow[]>('GET', `/api/logs${s ? '?' + s : ''}`)
}

export function getRequestLogDetail(requestId: string) {
  return req<RequestLogDetail>('GET', `/api/logs/${encodeURIComponent(requestId)}`)
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

export interface AvailableModelsResponse {
  families: AvailableFamily[]
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

export interface ModelDetail extends ModelCanonical {
  aliases: ModelAlias[]
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
