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
  // 2026-07-13: 总积分消耗（admin）— tenant-side credits_charged 之和，
  // 由 maas ChargeRequest 在请求落库时按 model_credit_rates × token 实时
  // 计算并写入 usage_ledger.credits_charged。前端仪表盘 "总积分消耗" 卡片
  // 展示此字段，使平台运营能直接看到「按当前定价 × 总 token」折算的
  // 销售口径积分消耗量，与 cost_usd 上游成本口径并列。
  total_credits_charged?: number
  avg_latency_ms: number
  success_rate: number
  // Optional degradation markers. When the backend cannot run its aggregation
  // because a database view is missing, it returns zeroed metrics together
  // with these flags so the UI can show a non-blocking hint instead of a
  // destructive error banner.
  degraded?: boolean
  missing_view?: string
  error_code?: string
  hint?: string
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

/**
 * 降级列表信封（后端 admin.DegradedListResponse）。
 *
 * 这三个端点原本返回裸数组，而 JSON 数组没有位置放降级标记 ——
 * 42P01 时页面把「聚合视图没迁移」渲染成「这段时间真的没有数据」。
 * 后端改为 { items, degraded, ... } 后，前端必须解包，
 * 否则所有消费方会拿到 undefined 并崩。
 */
export interface DegradedListEnvelope<T> {
  items: T[]
  degraded?: boolean
  degraded_reason?: string
  missing_view?: string
  hint?: string
}

export class DegradedListResult<T> {
  readonly items: T[]
  readonly degraded: boolean
  readonly reason: string

  constructor(items: T[], degraded: boolean, reason: string) {
    this.items = items
    this.degraded = degraded
    this.reason = reason
  }
}

/**
 * 解包降级列表信封。
 *
 * 兼容旧形状：后端若仍是裸数组，直接当作未降级处理。
 * 这么做不是为了容忍错误，而是让「后端尚未部署」与「后端已部署」在前端
 * 各自给出正确结果 —— 灰度期间两者会同时存在。
 */
export function unwrapDegradedList<T>(payload: DegradedListEnvelope<T> | T[]): DegradedListResult<T> {
  if (Array.isArray(payload)) {
    return new DegradedListResult<T>(payload, false, '')
  }
  const items = payload?.items ?? []
  return new DegradedListResult<T>(
    items,
    payload?.degraded === true,
    payload?.degraded_reason ?? payload?.missing_view ?? '',
  )
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

export function getUsageSummary(time: number | BoardTimeQuery = 7) {
  const qs = usageTimeQs(typeof time === 'number' ? { days: time } : time)
  return req<UsageSummary>('GET', `/api/usage/v2/summary?${qs}`)
}

// ★ 2026-11-26 统计 V2 审计轮：getDashboardOverview（v1 /api/usage/dashboard）
//   已删除 —— 该端点读 usage_ledger 账本，前端早已零调用点（死代码导出），
//   统计口径已统一到 /api/usage/v2/summary（request_metrics 投影）。
//   后端路由（handleUsage case "dashboard"）仍在，属后端清理项，待 owner。

export function getHotApiKeys(time: number | BoardTimeQuery = 7, limit = 10) {
  const qs = usageTimeQs({ ...(typeof time === 'number' ? { days: time } : time), limit })
  return req<DegradedListEnvelope<HotApiKeyEntry> | HotApiKeyEntry[]>(
    'GET', `/api/usage/v2/hot-keys?${qs}`,
  ).then(unwrapDegradedList<HotApiKeyEntry>)
}

export function getUsageByModel(time: number | BoardTimeQuery = 7) {
  const qs = usageTimeQs(typeof time === 'number' ? { days: time } : time)
  return req<DegradedListEnvelope<ModelUsage> | ModelUsage[]>('GET', `/api/usage/v2/by-model?${qs}`)
    .then(unwrapDegradedList<ModelUsage>)
}

export function getKeyUsage(keyId: number, params: { days?: number; start?: string; end?: string } = {}) {
  const qs = new URLSearchParams()
  if (params.days) qs.set('days', String(params.days))
  if (params.start) qs.set('start', params.start)
  if (params.end) qs.set('end', params.end)
  const s = qs.toString()
  return req<KeyUsageSummary>('GET', `/api/usage/v2/keys/${keyId}${s ? '?' + s : ''}`)
}

export function getKeyUsageByModel(keyId: number, params: { days?: number; start?: string; end?: string; limit?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.days) qs.set('days', String(params.days))
  if (params.start) qs.set('start', params.start)
  if (params.end) qs.set('end', params.end)
  if (params.limit) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<ModelUsageForKey[]>('GET', `/api/usage/v2/keys/${keyId}/models${s ? '?' + s : ''}`)
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
  return req<TrendEntry[]>('GET', `/api/usage/v2/keys/${keyId}/trend?${qs.toString()}`)
}

// ──────────────────────────────────────────────────────────────────────────
// Enhanced Usage API (T1.4) — 用量成本增强端点
// ──────────────────────────────────────────────────────────────────────────

export interface CostTrendEntry {
  dimension_value: string
  request_count: number
  total_cost_usd: number
  input_cost_usd: number | null
  output_cost_usd: number | null
  prompt_tokens: number
  completion_tokens: number
  avg_latency_ms: number
  error_rate: number
  percentage: number
}

export interface CostTrendResponse extends DegradationMarker {
  group_by: string
  date_from: string
  date_to: string
  total_cost: number
  entries: CostTrendEntry[]
  other_cost: number
  other_count: number
  source?: 'request_metrics'
  cost_rows_outside_usd?: number
}

export interface PeriodStats {
  period: string
  total_cost_usd: number
  total_requests: number
  total_tokens: number
  avg_cost_per_req: number
  unique_models: number
  // unique_sessions 已随 PeriodStats 一并移除（2026-10-03）：它需要
  // COUNT(DISTINCT gw_session_id)，而 usage_ledger 是计费宽表不含会话维度，
  // 换源后在交互预算内也算不出来（request_logs 实测 30 天窗口 72s）。
  // 该字段全库无渲染点，故删除而非返回 0 —— 见
  // admin/usage_enhanced.go 的 PeriodStats 注释。
}

export interface DimChange {
  dimension_value: string
  current_cost: number
  previous_cost: number
  change_pct: number | null
}

/**
 * 降级标记（2026-10-03）。契约本体在 composables/useDegradationMarker.ts ——
 * api/admin.ts 也要用它，定义必须落在更底层模块，否则每加一个降级端点就复制
 * 一份。这里转出以保持既有 import 路径可用。
 */
import type { DegradationMarker } from '../composables/useDegradationMarker'
export type { DegradationMarker }

export interface PeriodCompareResponse extends DegradationMarker {
  current: PeriodStats
  previous: PeriodStats
  change_pct: number | null
  change_abs: number
  trend: 'up' | 'down' | 'flat'
  significant: boolean
  by_dimension: Record<string, DimChange[]>
}

export interface CacheEconomicsResponse extends DegradationMarker {
  date_from: string
  date_to: string
  total_requests: number
  cache_read_tokens: number
  prompt_tokens: number
  cache_hit_ratio: number | null
  cache_input_tokens?: number
  eligible_cache_read_tokens?: number
  cache_scope_requests?: number
  compression_scope_requests?: number
  cache_write_tokens?: number
  dollars_saved: number | null
  dollars_spent: number
  effective_cost_ratio: number | null
  compressed_requests: number | null
  compression_saved: number | null
  total_saved: number | null
  savings_rate: number | null
  source?: 'request_metrics'
}

export type CostTrendGroupBy = 'model' | 'provider' | 'intent' | 'work_type' | 'api_key'

type EconomicsWindowQuery = { days?: number; date_from?: string; date_to?: string }

function economicsWindowParams(opts: EconomicsWindowQuery): URLSearchParams {
  const qs = new URLSearchParams()
  if (opts.date_from || opts.date_to) {
    if (opts.date_from) qs.set('date_from', opts.date_from)
    if (opts.date_to) qs.set('date_to', opts.date_to)
  } else if (opts.days !== undefined) {
    qs.set('days', String(opts.days))
  }
  return qs
}

export function getCostTrend(groupBy: CostTrendGroupBy = 'model', opts: EconomicsWindowQuery = {}) {
  const qs = economicsWindowParams(opts)
  qs.set('group_by', groupBy)
  return req<CostTrendResponse>('GET', `/api/admin/usage/v2/cost-trend?${qs.toString()}`)
}

export function getPeriodCompare(current: string, previous: string) {
  const params = new URLSearchParams({ current, previous })
  return req<PeriodCompareResponse>('GET', `/api/admin/usage/v2/period-compare?${params}`)
}

export function getCacheEconomics(opts: EconomicsWindowQuery = {}) {
  const s = economicsWindowParams(opts).toString()
  return req<CacheEconomicsResponse>('GET', `/api/admin/usage/v2/cache-economics${s ? '?' + s : ''}`)
}

// ── Provider usage (dashboard reconciliation) ─────────────────────────

export interface ProviderUsageRow {
  provider_id: number
  provider_name: string
  provider_code: string
  request_count: number
  prompt_tokens: number
  completion_tokens: number
  credits?: number
  total_tokens?: number
  total_cost_usd: number
  success_rate: number
}

export interface ProviderUsageSummary extends ProviderUsageRow {
  total_tokens: number
  unique_models: number
  window_start: string
  window_end: string
}

export interface ProviderModelUsage {
  model: string
  request_count: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
  success_rate: number
}

export interface ProviderDailyModelUsage {
  date: string
  model: string
  request_count: number
  total_tokens: number
  cost_usd: number
}

import type { BoardTimeQuery } from '../utils/boardTimeRange'
import { exportFile } from '../utils/exportFile'
import { readExportBlob } from '../utils/exportResponse'

function usageTimeQs(q: BoardTimeQuery & { limit?: number }) {
  const qs = new URLSearchParams()
  if (q.start && q.end) {
    qs.set('start', q.start)
    qs.set('end', q.end)
  } else {
    qs.set('days', String(q.days ?? 1))
  }
  if (q.tenant_id) qs.set('tenant_id', q.tenant_id)
  if (q.include_probes) qs.set('include_probes', '1')
  if (q.limit != null) qs.set('limit', String(q.limit))
  return qs
}

export function getUsageByProvider(time: BoardTimeQuery, limit = 200) {
  const qs = usageTimeQs({ ...time, limit })
  return req<DegradedListEnvelope<ProviderUsageRow> | ProviderUsageRow[]>(
    'GET', `/api/usage/v2/by-provider?${qs}`,
  ).then(unwrapDegradedList<ProviderUsageRow>)
}

export function getProviderUsageSummary(providerId: number, time: BoardTimeQuery) {
  const qs = usageTimeQs(time)
  return req<ProviderUsageSummary>('GET', `/api/usage/v2/providers/${providerId}?${qs}`)
}

export function getProviderUsageTrend(
  providerId: number,
  period: UsageTrendPeriod = 'day',
  time: BoardTimeQuery,
) {
  const qs = usageTimeQs(time)
  qs.set('period', period)
  return req<TrendEntry[]>('GET', `/api/usage/v2/providers/${providerId}/trend?${qs}`)
}

export function getProviderUsageModels(providerId: number, time: BoardTimeQuery, limit = 100) {
  const qs = usageTimeQs({ ...time, limit })
  return req<ProviderModelUsage[]>('GET', `/api/usage/v2/providers/${providerId}/models?${qs}`)
}

export function getProviderDailyModels(providerId: number, time: BoardTimeQuery) {
  const qs = usageTimeQs(time)
  return req<ProviderDailyModelUsage[]>('GET', `/api/usage/v2/providers/${providerId}/daily-models?${qs}`)
}

function exportFilename(prefix: string, time: BoardTimeQuery) {
  if (time.start && time.end) return `${prefix}-${time.start}_${time.end}.csv`
  return `${prefix}-${time.days ?? 1}d.csv`
}

export async function downloadProviderUsageExport(time: BoardTimeQuery) {
  const { BASE, headers } = await import('./_core')
  const qs = usageTimeQs(time)
  const res = await fetch(`${BASE}/api/usage/v2/providers/export?${qs}`, { headers: headers('GET') })
  if (!res.ok) throw new Error(`export failed: ${res.status}`)
  // 2026-10-06（UI规范 10 §4.6.18）：经 readExportBlob 取体，超上限即拒。
  // 此前是裸 res.blob()：整个文件先落进 WebView 堆，大报表在中低端机是 OOM 面。
  // 该端点为 csv.NewWriter 流式，Go 在 >2048 字节后转 chunked ⇒ 拿不到 Content-Length，
  // 故必须走边读边计数，不能照抄参考仓「读 Content-Length 设限」那条判据。
  const blob = await readExportBlob(res)
  // 2026-10-05（UI规范 19 §3.1）：走 exportFile 降级链（分享面→壳桥→blob）。
  await exportFile({ filename: exportFilename('provider-usage', time), blob })
}

export async function downloadProviderDetailExport(providerId: number, time: BoardTimeQuery) {
  const { BASE, headers } = await import('./_core')
  const qs = usageTimeQs(time)
  const res = await fetch(`${BASE}/api/usage/v2/providers/${providerId}/export?${qs}`, { headers: headers('GET') })
  if (!res.ok) throw new Error(`export failed: ${res.status}`)
  // 2026-10-06（UI规范 10 §4.6.18）：同上，经受控取流而非裸 blob()。
  const blob = await readExportBlob(res)
  await exportFile({ filename: exportFilename(`provider-${providerId}-daily`, time), blob })
}
// ─── 2026-10-02 看板轮：按模型拆分的用量趋势序列 ───
// 后端 admin/usage_trend_series.go。trend-series 供看板「用量趋势」卡与全页
// 用量趋势视图共用；trend-models 供全页视图的模型下拉选项（随当前过滤条件变化）。

export type UsageTrendMetric = 'requests' | 'tokens' | 'credits' | 'cost'

export interface UsageTrendPoint {
  bucket: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface UsageTrendModelSeries {
  model: string
  total_requests: number
  total_tokens: number
  total_credits: number
  total_cost_usd: number
  points: UsageTrendPoint[]
}

export interface UsageTrendSeriesResponse {
  start: string
  end: string
  bucket_minutes: number
  top: number
  source: string
  series: UsageTrendModelSeries[]
  degraded?: boolean
  degraded_reason?: string
  missing_view?: string
  hint?: string
}

export interface UsageTrendModelEntry {
  model: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface UsageTrendModelsResponse {
  start: string
  end: string
  source: string
  models: UsageTrendModelEntry[]
  degraded?: boolean
  degraded_reason?: string
  missing_view?: string
  hint?: string
}

/**
 * ★ 五维排行过滤（client_model / end_user_id / api_key_prefix /
 *   application_code / task_type）。
 *
 * ★ 为什么用量趋势端点也要认这五维（2026-11-23 深链轮实测取证）：
 *   六张排行卡片跳请求日志页带的就是这五个参数 ⇒ **同一个链接在两页必须同义**；
 *   而实测 `/admin/usage-trends?end_user_id=anonymous` 落地后条件被**完全忽略**
 *   （排行与图表都是全站）⇒ URL 承诺的条件与页面呈现的数据不一致。
 *
 * ★ 参数名与请求日志页**逐字相同**（不是另发明一套）⇒ 两页可互相粘贴链接。
 */
export interface RankingDimensionFilters {
  client_model?: string
  end_user_id?: string
  api_key_prefix?: string
  application_code?: string
  task_type?: string
}

/** ★ 把五维过滤写进 query（truthy 才写 ⇒ 空值不进 URL）。 */
export function applyRankingDimensionQs(qs: URLSearchParams, f: RankingDimensionFilters): URLSearchParams {
  if (f.client_model) qs.set('client_model', f.client_model)
  if (f.end_user_id) qs.set('end_user_id', f.end_user_id)
  if (f.api_key_prefix) qs.set('api_key_prefix', f.api_key_prefix)
  if (f.application_code) qs.set('application_code', f.application_code)
  if (f.task_type) qs.set('task_type', f.task_type)
  return qs
}

export interface UsageTrendFilterQuery {
  time: BoardTimeQuery
  tenant_id?: string
  provider_id?: number
  api_key_id?: number
  /** 模型过滤：单值向后兼容；数组序列化为重复 model 参数（后端 ANY 多选）。 */
  model?: string | string[]
  top?: number
  /** ★ 五维排行过滤（见 RankingDimensionFilters）。 */
  dims?: RankingDimensionFilters
}

export function usageTrendQs(q: UsageTrendFilterQuery): URLSearchParams {
  const qs = usageTimeQs(q.time)
  if (q.tenant_id) qs.set('tenant_id', q.tenant_id)
  if (q.provider_id != null && q.provider_id > 0) qs.set('provider_id', String(q.provider_id))
  if (q.api_key_id != null && q.api_key_id > 0) qs.set('api_key_id', String(q.api_key_id))
  if (Array.isArray(q.model)) {
    q.model.filter((m) => !!m).forEach((m) => qs.append('model', m))
  } else if (q.model) {
    qs.set('model', q.model)
  }
  if (q.top != null) qs.set('top', String(q.top))
  if (q.dims) applyRankingDimensionQs(qs, q.dims)
  return qs
}

export function getUsageTrendSeries(q: UsageTrendFilterQuery, signal?: AbortSignal) {
  return req<UsageTrendSeriesResponse>('GET', `/api/admin/usage/v2/trend-series?${usageTrendQs(q)}`, undefined, { signal })
}

export function getUsageTrendModels(q: UsageTrendFilterQuery, signal?: AbortSignal) {
  return req<UsageTrendModelsResponse>('GET', `/api/admin/usage/v2/trend-models?${usageTrendQs(q)}`, undefined, { signal })
}

// ── Top Rankings (2026-11-21) ─────────────────────────────────────────

export interface RankingEntry {
  name: string
  request_count: number
  total_tokens: number
  total_cost_usd: number
}

export interface TopRankingsResponse {
  top_models: RankingEntry[]
  top_clients: RankingEntry[]
  top_users: RankingEntry[]
  top_api_keys: RankingEntry[]
  top_projects: RankingEntry[]
  top_tasks: RankingEntry[]
  degraded?: boolean
  degraded_reason?: string
}

/**
 * Top 用量排行（/api/admin/usage/v2/top-rankings）。
 *
 * ★ 2026-11-23 深链钉住轮：可带维度过滤（供应商 / 模型 / API Key）——
 *   页面被深链钉住某个维度时，**下方排行榜必须跟着那个维度走**，否则同一屏里
 *   上半段是该供应商的数据、下半段还是全站 ⇒ 口径打架。
 *   ★ 参数缺省 = 不过滤（与改动前逐字等价）。
 */
export interface TopRankingsFilters {
  provider_id?: number | null
  api_key_id?: number | null
  model?: string[] | null
  /** ★ 五维排行过滤（见 RankingDimensionFilters）——深链钉住某个排行维度时，
   *   **排行卡片本身**也要按它收敛（否则图表已过滤、排行还是全站 ⇒ 口径打架）。 */
  dims?: RankingDimensionFilters
}

export function getTopRankings(time: BoardTimeQuery, usersLimit = 10, filters: TopRankingsFilters = {}) {
  const qs = usageTimeQs(time)
  if (filters.provider_id) qs.set('provider_id', String(filters.provider_id))
  if (filters.api_key_id) qs.set('api_key_id', String(filters.api_key_id))
  for (const m of filters.model ?? []) {
    if (m) qs.append('model', m)
  }
  if (filters.dims) applyRankingDimensionQs(qs, filters.dims)
  return req<TopRankingsResponse>('GET', `/api/admin/usage/v2/top-rankings?${qs}&users_limit=${usersLimit}`)
}
