import { req } from './_core'

// usage.ts — 用量页（汇总卡 + 模型分布），对齐 web/src/api/usage.ts。

export interface UsageSummary {
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  total_credits_charged?: number
  avg_latency_ms: number
  success_rate: number
  degraded?: boolean
  hint?: string
}

export interface ModelUsage {
  model: string
  provider_code: string
  total_requests: number
  total_tokens: number
  total_cost_usd: number
}

export function getUsageSummary(days = 7, signal?: AbortSignal) {
  return req<UsageSummary>('GET', `/api/usage/summary?days=${days}`, undefined, signal)
}

export function getUsageByModel(days = 7, signal?: AbortSignal) {
  return req<{ data?: ModelUsage[]; models?: ModelUsage[] } | ModelUsage[]>(
    'GET',
    `/api/usage/by-model?days=${days}`,
    undefined,
    signal,
  )
}

export function normalizeUsageByModel(
  payload: { data?: ModelUsage[]; models?: ModelUsage[] } | ModelUsage[],
): ModelUsage[] {
  if (Array.isArray(payload)) return payload
  return payload.data ?? payload.models ?? []
}
