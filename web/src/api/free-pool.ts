import { req } from './_core'

// free-pool.ts — v6.0 audit T12 (2026-06-22)
// The "free pool" is a curated catalog of public LLM providers that
// offer free-tier or no-key-required access. This file covers the
// admin UI for browsing, registering, importing keys, and bootstrapping
// the pool from environment variables + OAuth bridges.
//
// Notable endpoints:
//   - /api/free-pool/status      : live snapshot of pool + models
//   - /api/free-pool/catalog     : template catalog (rpm, signup_url, env)
//   - /api/free-pool/keys        : live keys per provider
//   - /api/free-pool/signup-hub  : the "platform × tool" workflow matrix
//   - /api/free-pool/temp-email  : disposable mailboxes for sign-up
//   - /api/free-pool/quick-entry : one-shot register + probe + save
//   - /api/free-pool/bootstrap   : cleanup + mirror + discover

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