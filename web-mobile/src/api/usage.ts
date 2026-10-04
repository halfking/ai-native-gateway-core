import { req, type RequestOptions } from './client'

// usage.ts — /api/usage/summary + by-model（形状对齐 web/src/api/usage.ts）。
// by-model 可能被 DegradedListEnvelope 包装：{degraded, missing_view, items}。

export interface UsageSummary {
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  total_credits_charged?: number
  avg_latency_ms: number
  success_rate: number
  degraded?: boolean
  missing_view?: string
  hint?: string
}

export interface ModelUsage {
  model: string
  provider_code: string
  total_requests: number
  total_tokens: number
  total_cost_usd: number
}

export interface DegradedListEnvelope<T> {
  degraded: boolean
  missing_view?: string
  items: T[]
}

export function fetchUsageSummary(days = 7, options?: RequestOptions): Promise<UsageSummary> {
  return req<UsageSummary>('GET', `/api/usage/summary?days=${days}`, undefined, options)
}

export function fetchUsageByModel(days = 7, options?: RequestOptions): Promise<DegradedListEnvelope<ModelUsage> | ModelUsage[]> {
  return req<DegradedListEnvelope<ModelUsage> | ModelUsage[]>('GET', `/api/usage/by-model?days=${days}`, undefined, options)
}

/** 兼容两种形态（envelope 或裸数组）。 */
export function unwrapModelUsage(payload: DegradedListEnvelope<ModelUsage> | ModelUsage[]): { items: ModelUsage[]; degraded: boolean } {
  if (Array.isArray(payload)) return { items: payload, degraded: false }
  return { items: payload.items ?? [], degraded: !!payload.degraded }
}
