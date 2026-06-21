import { req } from './_core'

// models.ts — v6.0 audit T12 (2026-06-22)
// Two related surfaces:
//
//  1. /api/routing/available-models* — the "taxonomy" view: families →
//     versions, surfaced on the user-facing chat UI for model pickers.
//  2. /api/models* + /api/tags — the admin CRUD: create / patch /
//     disable canonical models, manage aliases, run model discovery.
//
// Both surfaces share the same ModelCanonical record but expose it
// under different shapes (AvailableVersion vs ModelCanonical).

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

export interface AdminModelOffer {
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
  offers: AdminModelOffer[]
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