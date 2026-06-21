import { req } from './_core'

// keys.ts — v6.0 audit T12 (2026-06-22)
// API key CRUD + the key-conflict lookup that powers the
// "before you create a key, check if (tenant, application, alias)
// is already taken" guard. Approve / enable / disable / revoke are
// separate endpoints because each emits a different audit event.

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