import { req } from './_core'

export type StatsQuery = {
  start?: string
  end?: string
  days?: number
  tenant_id?: string
  provider_id?: number
  credential_id?: number
  canonical_id?: number
  model?: string
  traffic_class?: string
}

export interface StatsSummary {
  requests: number
  success: number
  failures: number
  timeouts: number
  rate_limited: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
  avg_latency_ms: number
}

export interface StatsTrendPoint {
  bucket: string
  requests: number
  success: number
  failures: number
  total_tokens: number
  credits_charged: number
  cost_usd: number
}

function queryString(query?: StatsQuery) {
  const params = new URLSearchParams()
  if (!query) return ''
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== null && value !== '') params.set(key, String(value))
  }
  const encoded = params.toString()
  return encoded ? `?${encoded}` : ''
}

export function getStatsSummary(query?: StatsQuery) {
  return req<{ source: string; as_of: string; degraded?: boolean; error_code?: string; summary: StatsSummary }>(
    'GET',
    `/api/admin/stats/summary${queryString(query)}`,
  )
}

export function getStatsTrend(query?: StatsQuery) {
  return req<{ source: string; items: StatsTrendPoint[] }>('GET', `/api/admin/stats/trend${queryString(query)}`)
}

export function getStatsMonthly(query?: StatsQuery) {
  return req<{ source: string; items: Array<Record<string, unknown>> }>('GET', `/api/admin/stats/monthly${queryString(query)}`)
}

export function getStatsBreakdown(query?: StatsQuery & { dimension?: string; limit?: number }) {
  return req<{ source: string; dimension: string; items: Array<Record<string, unknown>> }>(
    'GET',
    `/api/admin/stats/breakdown${queryString(query)}`,
  )
}

export function getStatsErrors(query?: StatsQuery & { limit?: number }) {
  return req<{ source: string; items: Array<Record<string, unknown>> }>('GET', `/api/admin/stats/errors${queryString(query)}`)
}

export function getStatsReconciliation(query?: { run_id?: string; limit?: number }) {
  return req<{ runs: Array<Record<string, unknown>>; diffs: Array<Record<string, unknown>> }>(
    'GET',
    `/api/admin/stats/reconciliation${queryString(query)}`,
  )
}
