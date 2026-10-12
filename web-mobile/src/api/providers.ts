import { req, type RequestOptions } from './client'

// providers.ts — /api/providers（供应商列表，桌面 web/src/api/providers.ts 的
// 移动端裁剪面）。
//
// ⚠️ 形状事实（2026-10-06 实读后端 admin/handler.go:1224-1225 + 桌面
// web/src/api/providers.ts:130 `req<Provider[]>`）：该端点返回**裸数组**，
// 不是 {data,count} 信封 —— 与 alerts / monitor-summary 恰好相反。
// 这不是 bug，是三个端点三种形态；故每处都必须**显式**解包而不是套同一个
// helper（套通用解包函数反而会掩盖真正的契约差异）。
//
// 鉴权：h.providerConsole（admin/handler.go:1224），非纯 superAdmin。

export interface Provider {
  id: number
  code: string
  display_name: string | null
  catalog_code: string
  protocol: string
  base_url: string | null
  enabled: boolean
  health_status?: 'unknown' | 'healthy' | 'warning' | 'unreachable' | 'manual_disabled'
  routability?: 'available' | 'unavailable' | 'no_models' | 'manual_disabled'
  manual_disabled?: boolean
  credential_count?: number
  model_count?: number
  notes?: string | null
  [k: string]: unknown
}

export interface ProviderDetail extends Provider {
  kind?: string
  category?: string
  vendor_name?: string | null
  egress_profile?: string | null
  header_profile_code?: string | null
  domestic?: boolean
  discount_rate?: number
  active_cred_count?: number
  healthy_cred_count?: number
  warning_cred_count?: number
  cooling_cred_count?: number
  unreachable_cred_count?: number
  available_model_count?: number
  unavailable_model_count?: number
  error_rate_24h?: number
  health_checked_at?: string | null
  created_at?: string | null
}

export interface ProviderCredentialDetail {
  id: number
  label: string
  status: string
  lifecycle_status?: string | null
  availability_state?: string | null
  health_status?: string | null
  health_error?: string | null
  state_reason_detail?: string | null
  manual_disabled?: boolean
  total_requests?: number
  total_cost_usd?: number | string
  total_prompt_tokens?: number
  total_completion_tokens?: number
  balance_usd?: number | string | null
  updated_at?: string
}

export interface ProviderModelOffer {
  id: number
  credential_id: number
  credential_label: string
  raw_model_name: string
  standardized_name: string | null
  display_name: string
  available: boolean
  unavailable_reason: string | null
  p95_latency_ms: number | null
  success_rate: number | null
  input_price: number | null
  output_price: number | null
  cache_read_price?: number | null
  cache_write_price?: number | null
  routing_tier: string
  availability_source: string
  canonical_name?: string
}

/** 与桌面 providers.ts:107-113 的四态口径一致，避免「true=only / false=exclude」二义。 */
export type RoutabilityFilter = 'available' | 'unavailable' | 'no_models' | 'manual_disabled' | 'all'

export function getProviders(
  params?: { search?: string; routability?: RoutabilityFilter; manual_disabled?: boolean },
  options?: RequestOptions,
): Promise<Provider[]> {
  const qs = new URLSearchParams()
  if (params?.search) qs.set('search', params.search)
  if (params?.routability && params.routability !== 'all') qs.set('routability', params.routability)
  if (params?.manual_disabled != null) qs.set('manual_disabled', params.manual_disabled ? 'true' : 'false')
  const q = qs.toString()
  return req<Provider[] | { providers?: Provider[]; data?: Provider[] }>(
    'GET',
    `/api/providers${q ? '?' + q : ''}`,
    undefined,
    options,
  ).then(unwrapProviders)
}

export function getProviderDetail(id: number, options?: RequestOptions): Promise<ProviderDetail> {
  return req<ProviderDetail>('GET', `/api/providers/${id}`, undefined, options)
}

export function getProviderCredentials(id: number, options?: RequestOptions): Promise<ProviderCredentialDetail[]> {
  return req<ProviderCredentialDetail[]>('GET', `/api/providers/${id}/credentials`, undefined, options)
}

export function getProviderModels(id: number, options?: RequestOptions): Promise<ProviderModelOffer[]> {
  return req<ProviderModelOffer[]>('GET', `/api/providers/${id}/models`, undefined, options)
}

export function toggleProvider(id: number, options?: RequestOptions): Promise<{ message: string }> {
  return req('PATCH', `/api/providers/${id}/toggle`, undefined, options)
}

export function setProviderManualDisabled(id: number, manual_disabled: boolean, reason = '', options?: RequestOptions): Promise<{ message: string; manual_disabled: boolean; actor: string }> {
  return req('PATCH', `/api/providers/${id}/manual-disabled`, { manual_disabled, reason }, options)
}

/**
 * 裸数组是后端的**当前**真实形态（桌面 providers.ts:130 就是这么收的）；
 * 信封形态仅作容错，且形状不符**抛错**而非返 [] ——
 * 否则「契约漂移」会被显示成「一个供应商都没有」，看起来像数据被清空。
 */
export function unwrapProviders(resp: Provider[] | { providers?: Provider[]; data?: Provider[] }): Provider[] {
  if (Array.isArray(resp)) return resp
  if (resp && typeof resp === 'object') {
    if (Array.isArray(resp.providers)) return resp.providers
    if (Array.isArray(resp.data)) return resp.data
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`providers 响应形状不符：期望 array 或 {providers:[…]}，实得 ${actual}`)
}

/** 展示名优先 display_name，其次 code —— 与桌面 ProvidersView 口径一致。 */
export function providerName(p: Provider): string {
  return p.display_name || p.code
}
