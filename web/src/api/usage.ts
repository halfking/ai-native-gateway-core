import { req } from './_core'

// usage.ts — v6.0 audit T12 (2026-06-22)
// Read-only usage aggregations powering the admin dashboard: global
// summary, per-key / per-model breakdowns, time-bucketed trend.
//
// All endpoints take an optional `days` (default 7) or explicit
// start/end window. The trend endpoint buckets by minute|hour|day|week|month;
// the backend picks a sensible default period based on the window size
// if the caller doesn't specify one.

export interface UsageSummary {
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  avg_latency_ms: number
  success_rate: number
}

export interface DashboardOverview {
  total_api_keys: number
  active_api_keys: number
  active_api_keys_in_window: number
  total_models: number
  active_models_in_window: number
  total_providers: number
  active_providers: number
  offline_models: number
  offline_credentials: number
  total_credentials: number
}

export interface HotApiKeyEntry {
  api_key_id: number
  key_prefix: string | null
  application_code: string | null
  owner_user: string | null
  request_count: number
  total_tokens: number
  total_cost_usd: number
  last_used_at: string | null
}

export interface ModelUsage {
  model: string
  provider_code: string
  total_requests: number
  total_tokens: number
  total_cost_usd: number
}

export interface KeyUsageSummary {
  key_id: number
  key_prefix: string
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_tokens: number
  total_cost_usd: number
  avg_latency_ms: number
  success_rate: number
  unique_models: number
  first_request_at: string | null
  last_request_at: string | null
  window_start?: string
  window_end?: string
}

export interface ModelUsageForKey {
  model: string
  request_count: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
  success_rate: number
  first_used_at: string | null
  last_used_at: string | null
}

export interface TrendEntry {
  period: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
}

export function getUsageSummary(days = 7) {
  return req<UsageSummary>('GET', `/api/usage/summary?days=${days}`)
}

export function getDashboardOverview(days = 7) {
  return req<DashboardOverview>('GET', `/api/usage/dashboard?days=${days}`)
}

export function getHotApiKeys(days = 7, limit = 10) {
  return req<HotApiKeyEntry[]>('GET', `/api/usage/hot-keys?days=${days}&limit=${limit}`)
}

export function getUsageByModel(days = 7) {
  return req<ModelUsage[]>('GET', `/api/usage/by-model?days=${days}`)
}

export function getKeyUsage(keyId: number, params: { days?: number; start?: string; end?: string } = {}) {
  const qs = new URLSearchParams()
  if (params.days) qs.set('days', String(params.days))
  if (params.start) qs.set('start', params.start)
  if (params.end) qs.set('end', params.end)
  const s = qs.toString()
  return req<KeyUsageSummary>('GET', `/api/usage/${keyId}${s ? '?' + s : ''}`)
}

export function getKeyUsageByModel(keyId: number, params: { days?: number; start?: string; end?: string; limit?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.days) qs.set('days', String(params.days))
  if (params.start) qs.set('start', params.start)
  if (params.end) qs.set('end', params.end)
  if (params.limit) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<ModelUsageForKey[]>('GET', `/api/usage/${keyId}/models${s ? '?' + s : ''}`)
}

export type UsageTrendPeriod = 'minute' | 'hour' | 'day' | 'week' | 'month'

export function getKeyUsageTrend(keyId: number, period: UsageTrendPeriod = 'day', opts: { days?: number; start?: string; end?: string } = {}) {
  const qs = new URLSearchParams()
  qs.set('period', period)
  if (opts.start && opts.end) {
    qs.set('start', opts.start)
    qs.set('end', opts.end)
  } else {
    qs.set('days', String(opts.days ?? 30))
  }
  return req<TrendEntry[]>('GET', `/api/usage/${keyId}/trend?${qs.toString()}`)
}

// ──────────────────────────────────────────────────────────────────────────
// Enhanced Usage API (T1.4) — 用量成本增强端点
// ──────────────────────────────────────────────────────────────────────────

export interface CostTrendEntry {
  dimension_value: string
  request_count: number
  total_cost_usd: number
  input_cost_usd: number
  output_cost_usd: number
  prompt_tokens: number
  completion_tokens: number
  avg_latency_ms: number
  error_rate: number
  percentage: number
}

export interface CostTrendResponse {
  group_by: string
  date_from: string
  date_to: string
  total_cost: number
  entries: CostTrendEntry[]
  other_cost: number
  other_count: number
}

export interface PeriodStats {
  period: string
  total_cost_usd: number
  total_requests: number
  total_tokens: number
  avg_cost_per_req: number
  unique_models: number
  unique_sessions: number
}

export interface DimChange {
  dimension_value: string
  current_cost: number
  previous_cost: number
  change_pct: number
}

export interface PeriodCompareResponse {
  current: PeriodStats
  previous: PeriodStats
  change_pct: number
  change_abs: number
  trend: 'up' | 'down' | 'flat'
  significant: boolean
  by_dimension: Record<string, DimChange[]>
}

export interface CacheEconomicsResponse {
  date_from: string
  date_to: string
  total_requests: number
  cache_read_tokens: number
  prompt_tokens: number
  cache_hit_ratio: number
  dollars_saved: number
  dollars_spent: number
  effective_cost_ratio: number
  compressed_requests: number
  compression_saved: number
  total_saved: number
  savings_rate: number
}

export type CostTrendGroupBy = 'model' | 'provider' | 'intent' | 'work_type' | 'api_key'

export function getCostTrend(groupBy: CostTrendGroupBy = 'model', opts: { date_from?: string; date_to?: string } = {}) {
  const qs = new URLSearchParams()
  qs.set('group_by', groupBy)
  if (opts.date_from) qs.set('date_from', opts.date_from)
  if (opts.date_to) qs.set('date_to', opts.date_to)
  return req<CostTrendResponse>('GET', `/api/admin/usage/cost-trend?${qs.toString()}`)
}

export function getPeriodCompare(current: string, previous: string) {
  return req<PeriodCompareResponse>('GET', `/api/admin/usage/period-compare?current=${current}&previous=${previous}`)
}

export function getCacheEconomics(opts: { date_from?: string; date_to?: string } = {}) {
  const qs = new URLSearchParams()
  if (opts.date_from) qs.set('date_from', opts.date_from)
  if (opts.date_to) qs.set('date_to', opts.date_to)
  const s = qs.toString()
  return req<CacheEconomicsResponse>('GET', `/api/admin/usage/cache-economics${s ? '?' + s : ''}`)
}