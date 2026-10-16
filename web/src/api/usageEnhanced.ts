import { req } from './_core'

// usageEnhanced.ts — 批 158b（2026-10-17）
// P1 usageEnhanced 孤儿接前端：组合 usage.ts 5 个 API 的综合视图
// （UsageCost 4 + UsageTrend 1 = 5）。
// 后端无新端点（usage.ts 15+ 函数全有，31 跳实证 usage.ts 函数都有租户隔离）。
//
// 与批 158a 同形态：API helper + View + router + 测试。

export interface UsageSummaryResponse {
  days: number
  total_cost: number
  total_requests: number
  total_tokens: number
}

export interface CostTrendPoint {
  date: string
  cost: number
  requests: number
}

export interface CostTrendResponse {
  group_by: string
  data: CostTrendPoint[]
}

export interface PeriodCompareResponse {
  current: { cost: number; requests: number }
  previous: { cost: number; requests: number }
  delta_cost: number
  delta_cost_pct: number
  delta_requests: number
  delta_requests_pct: number
}

export interface CacheEconomicsResponse {
  cache_hit_rate: number
  cache_cost_saved: number
  total_cacheable_requests: number
}

export interface HotApiKey {
  key_id: number
  key_name: string
  total_requests: number
  total_cost: number
}

export interface UsageEnhancedData {
  summary: UsageSummaryResponse
  cost_trend: CostTrendResponse
  period_compare: PeriodCompareResponse
  cache_economics: CacheEconomicsResponse
  hot_api_keys: HotApiKey[]
  refreshedAt: string
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) qs.append(k, String(v))
  }
  return qs.toString() ? '?' + qs.toString() : ''
}

/**
 * 组合 5 个 API 的综合 usage 视图。
 */
export async function getUsageEnhancedData(params: {
  days?: number
  topN?: number
  dateFrom?: string
  dateTo?: string
} = {}): Promise<UsageEnhancedData> {
  const days = params.days ?? 7
  const topN = params.topN ?? 10
  const dateFrom = params.dateFrom
  const dateTo = params.dateTo
  // 5 端点并发
  const [summary, costTrend, periodCompare, cacheEcon, hotKeys] = await Promise.all([
    req<UsageSummaryResponse>('GET', `/api/usage/summary${buildQuery({ days })}`),
    req<CostTrendResponse>('GET', `/api/admin/usage/cost-trend${buildQuery({ group_by: 'model', date_from: dateFrom, date_to: dateTo })}`),
    req<PeriodCompareResponse>('GET', `/api/admin/usage/period-compare${buildQuery({ current: 'last_7d', previous: 'previous_7d' })}`),
    req<CacheEconomicsResponse>('GET', `/api/admin/usage/cache-economics${buildQuery({ date_from: dateFrom, date_to: dateTo })}`),
    req<{ keys: HotApiKey[] }>('GET', `/api/admin/usage/hot-api-keys${buildQuery({ days, limit: topN })}`),
  ])
  return {
    summary,
    cost_trend: costTrend,
    period_compare: periodCompare,
    cache_economics: cacheEcon,
    hot_api_keys: hotKeys.keys ?? [],
    refreshedAt: new Date().toISOString(),
  }
}
