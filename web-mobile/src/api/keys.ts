import { req } from './_core'

// keys.ts — 密钥管理（卡片列表 + 创建 + 禁用/启用/揭示），对齐 web/src/api/keys.ts。
// 揭示结果不缓存不预取（UI 规范 13 §4 / 17 §5）。

export interface ApiKey {
  id: number
  key_prefix: string
  owner_user: string | null
  enabled: boolean
  status: 'active' | 'pending' | 'disabled'
  expires_at: string | null
  last_used_at: string | null
  last_request_at?: string | null
  budget_usd: number | null
  rate_limit_rpm: number | null
  application_code: string
  remark?: string | null
  total_requests: number
  total_tokens: number
  total_cost_usd: number
  tenant_id: string
  key_alias: string | null
}

export interface KeyCreatedResponse {
  id: number
  api_key?: string
  application_code?: string
  key_alias?: string
}

export function getKeys(signal?: AbortSignal) {
  return req<{ data?: ApiKey[]; keys?: ApiKey[] } | ApiKey[]>('GET', '/api/keys', undefined, signal)
}

export function normalizeKeysPayload(
  payload: { data?: ApiKey[]; keys?: ApiKey[] } | ApiKey[],
): ApiKey[] {
  if (Array.isArray(payload)) return payload
  return payload.data ?? payload.keys ?? []
}

export function createKey(
  data: {
    application_code: string
    tenant_id?: string
    key_alias?: string
    owner_user?: string
    budget_usd?: number
    rate_limit_rpm?: number
    remark?: string
  },
  signal?: AbortSignal,
) {
  return req<KeyCreatedResponse>('POST', '/api/keys', data, signal)
}

export function disableKey(id: number, signal?: AbortSignal) {
  return req<unknown>('POST', `/api/keys/${id}/disable`, undefined, signal)
}

export function enableKey(id: number, signal?: AbortSignal) {
  return req<unknown>('POST', `/api/keys/${id}/enable`, undefined, signal)
}

/** 揭示：确认框后调用；结果只在内存展示一次。 */
export function revealKey(id: number, signal?: AbortSignal) {
  return req<{ api_key?: string; key?: string; plaintext?: string }>(
    'POST',
    `/api/keys/${id}/reveal`,
    undefined,
    signal,
  )
}
