import { req } from './_core'

// tuning.ts — v6.0 audit T12 (2026-06-22)
// Auto-route analytics + admin tuning surfaces. The auto-router
// evaluates different routing strategies (Phase 5) and A/B-tests them
// against a baseline; the endpoints here surface the verdict, per-
// strategy correlation breakdowns, and admin overrides (pin/ban
// a model for a given task_type × profile combo).
//
// Data Lifecycle endpoints (hot/warm/cold/expired data + cleanup
// preview) live at the bottom of this file because they share the
// "admin observability" theme.

export interface StrategySummaryRow {
  strategy: string
  total: number
  avg_quality: number
  avg_success: number
  avg_latency: number
  avg_cost: number
  drift_rate: number
}

export interface StrategyBreakdownRow {
  strategy: string
  task_type: string
  total: number
  avg_quality: number
  avg_success: number
}

export interface StrategyResponse {
  window_days: number
  summary: StrategySummaryRow[]
  breakdown: StrategyBreakdownRow[]
  ab_verdict: 'pattern_layered_wins' | 'baseline_heuristic_wins' | 'no_significant_difference' | 'insufficient_samples' | 'ab_test_disabled'
  ab_enabled: boolean
  ab_baseline_pct: number
  generated_at: string
}

export function getTuningStrategies(days = 7) {
  return req<StrategyResponse>('GET',
    `/api/admin/auto-route/tuning/strategies?days=${days}`)
}

export interface CorrelationRow {
  label: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_cost_usd: number
  avg_quality?: number
}

export interface CorrelationRowMT {
  model: string
  task_type: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_cost_usd: number
}

export interface CorrelationVerdict {
  task_type: string
  model: string
  success_rate: number
  avg_latency_ms: number
  rank: number
}

export interface AutoRouteCorrelationsResponse {
  window_days: number
  by_model: CorrelationRow[]
  by_strategy: CorrelationRow[]
  by_task_type: CorrelationRow[]
  by_model_task: CorrelationRowMT[]
  verdict: CorrelationVerdict[]
  generated_at: string
}

export function getAutoRouteCorrelations(params: {
  days?: number
  min_samples?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.days) q.set('days', String(params.days))
  if (params.min_samples) q.set('min_samples', String(params.min_samples))
  const path = '/api/admin/auto-route/correlations' + (q.toString() ? '?' + q : '')
  return req<AutoRouteCorrelationsResponse>('GET', path)
}

export interface RoutingOverride {
  id: number
  task_type: string
  profile: string
  mode: 'pin' | 'ban'
  model_chosen?: string
  reason: string
  created_by?: string
  expires_at?: string
  created_at: string
  updated_at: string
}

export interface RoutingOverridesResponse {
  overrides: RoutingOverride[]
  count: number
  filter: { task_type: string; profile: string; active: string }
}

export interface RoutingOverrideCreate {
  task_type: string
  profile?: string
  mode: 'pin' | 'ban'
  model_chosen?: string
  reason: string
  expires_at?: string
}

export function getRoutingOverrides(params: {
  active?: boolean
  task_type?: string
  profile?: string
} = {}) {
  const q = new URLSearchParams()
  if (params.active) q.set('active', 'true')
  if (params.task_type) q.set('task_type', params.task_type)
  if (params.profile) q.set('profile', params.profile)
  const path = '/api/admin/routing/overrides' + (q.toString() ? '?' + q : '')
  return req<RoutingOverridesResponse>('GET', path)
}

export function createRoutingOverride(body: RoutingOverrideCreate) {
  return req<{ id: number; status: string; message: string }>('POST',
    '/api/admin/routing/overrides', body)
}

export function deleteRoutingOverride(id: number) {
  return req<{ id: number; status: string; note: string }>('DELETE',
    `/api/admin/routing/overrides/${id}`)
}

export function extendRoutingOverride(id: number, expires_at: string | null) {
  return req<{ id: number; status: string }>('PATCH',
    `/api/admin/routing/overrides/${id}/extend`, { expires_at })
}

export interface QualityCorrelationRow {
  bucket: string
  samples: number
  success_rate: number
  avg_latency_ms: number
  avg_quality: number
  avg_cost_usd: number
}

export interface QualityCorrelationInsight {
  predictor: 'prompt_length' | 'tools' | 'images' | 'code_block'
  buckets: number
  samples: number
  correlation: number
  abs_r: number
  interpretation: string
}

export interface QualityCorrelationResponse {
  window_days: number
  by: string
  breakdown: QualityCorrelationRow[]
  insights: QualityCorrelationInsight[]
  generated_at: string
}

export function getQualityCorrelations(params: {
  days?: number
  by?: 'prompt_length' | 'tools' | 'images' | 'code_block'
} = {}) {
  const q = new URLSearchParams()
  if (params.days) q.set('days', String(params.days))
  if (params.by) q.set('by', params.by)
  const path = '/api/admin/auto-route/quality-correlations' + (q.toString() ? '?' + q : '')
  return req<QualityCorrelationResponse>('GET', path)
}

export interface RoutingAuditEntry {
  id: number
  ts: string
  action: 'insert' | 'update' | 'delete'
  override_id?: number
  task_type?: string
  profile?: string
  mode?: string
  model_chosen?: string
  reason?: string
  expires_at?: string
  old_expires_at?: string
  actor?: string
}

export interface RoutingAuditResponse {
  entries: RoutingAuditEntry[]
  count: number
  filter: { action: string; actor: string; override_id: string; days: string }
}

export function getRoutingAudit(params: {
  action?: 'insert' | 'update' | 'delete' | ''
  actor?: string
  override_id?: number
  days?: number
  limit?: number
} = {}) {
  const q = new URLSearchParams()
  if (params.action) q.set('action', params.action)
  if (params.actor) q.set('actor', params.actor)
  if (params.override_id) q.set('override_id', String(params.override_id))
  if (params.days) q.set('days', String(params.days))
  if (params.limit) q.set('limit', String(params.limit))
  const path = '/api/admin/routing/overrides/audit' + (q.toString() ? '?' + q : '')
  return req<RoutingAuditResponse>('GET', path)
}

// ── Data Lifecycle Management API ─────────────────────────────────────────

export interface DataSegment {
  rows: number
  size_bytes: number
  size_human: string
  days: number
  percent_of_total: number
}

export interface TenantDataStats {
  tenant_id: string
  rows: number
  size_bytes: number
  size_human: string
}

export interface DailyGrowth {
  date: string
  requests: number
  compressed: number
  compression_rate: number
}

export interface DataLifecycleStatsResponse {
  total_rows: number
  total_size_bytes: number
  total_size_human: string
  hot_data: DataSegment | null
  warm_data: DataSegment | null
  cold_data: DataSegment | null
  expired_data: DataSegment | null
  by_tenant: TenantDataStats[]
  growth_trend: DailyGrowth[]
}

export function dataLifecycleStats() {
  return req<DataLifecycleStatsResponse>('GET', '/api/admin/data-lifecycle/stats')
}

export interface CleanupPreviewResponse {
  affected_rows: number
  estimated_freed_bytes: number
  estimated_freed_human: string
  warning_message?: string
}

export function dataLifecycleCleanupPreview(
  action: string,
  from: string,
  to: string
) {
  return req<CleanupPreviewResponse>('POST', '/api/admin/data-lifecycle/cleanup/preview', {
    action,
    from,
    to
  })
}

export interface DataLifecycleMetricsResponse {
  total_rows: number
  total_size_bytes: number
  hot_data_rows: number
  hot_data_size_bytes: number
  warm_data_rows: number
  warm_data_size_bytes: number
  cold_data_rows: number
  cold_data_size_bytes: number
  expired_data_rows: number
  expired_data_size_bytes: number
  last_cleanup_at?: string
  last_archive_at?: string
}

export function dataLifecycleMetrics() {
  return req<DataLifecycleMetricsResponse>('GET', '/api/admin/data-lifecycle/metrics')
}