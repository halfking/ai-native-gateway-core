import { req, type RequestOptions } from './client'

// keys.ts — /api/keys 全套（形状对齐 web/src/api/keys.ts）。
// 红线（17 §5）：reveal 结果不缓存不预取（13 §4 敏感 reveal）。

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

export interface CreateKeyPayload {
  application_code: string
  key_alias?: string
  budget_usd?: number
  rate_limit_rpm?: number
  remark?: string
}

export interface RevealedKey {
  key_id: number
  api_key: string
}

export function getKeys(options?: RequestOptions): Promise<ApiKey[]> {
  return req<ApiKey[]>('GET', '/api/keys', undefined, options)
}

export function createKey(payload: CreateKeyPayload, options?: RequestOptions): Promise<KeyCreatedResponse> {
  return req<KeyCreatedResponse>('POST', '/api/keys', payload, options)
}

export function revealKey(id: number, options?: RequestOptions): Promise<RevealedKey> {
  return req<RevealedKey>('GET', `/api/keys/${id}/reveal`, undefined, options)
}

export function disableKey(id: number, options?: RequestOptions): Promise<{ message: string }> {
  return req<{ message: string }>('POST', `/api/keys/${id}/disable`, undefined, options)
}

export function enableKey(id: number, options?: RequestOptions): Promise<{ message: string }> {
  return req<{ message: string }>('POST', `/api/keys/${id}/enable`, undefined, options)
}
