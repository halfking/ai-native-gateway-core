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
