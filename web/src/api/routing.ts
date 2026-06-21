import { req } from './_core'

// routing.ts — v6.0 audit T12 (2026-06-22)
// Routing decision endpoints: resolve (what would I route to?),
// overview (all current routable credentials), model tree
// (raw_model → standardized_name → credential), policy CRUD,
// featured models, decision log, circuit-breaker health, audit.
//
// "v2" in the original section header refers to the routing
// algorithm rewrite (algorithm_version field in RoutingPolicy);
// the v1 endpoints are gone.

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