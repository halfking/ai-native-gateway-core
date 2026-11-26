import { req } from './_core'
import type { ProviderUsageRow, UsageTrendModelSeries } from './usage'

export interface BoardPieItem {
  key: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface BoardTrendPoint {
  bucket: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
  success?: number
}

export interface BoardSummary {
  total_requests?: number
  total_prompt_tokens?: number
  total_completion_tokens?: number
  total_tokens?: number
  total_cost_usd?: number
  total_credits_charged?: number
  success_rate?: number
  avg_latency_ms?: number
  failed_count?: number
  cost_rows_outside_usd?: number
  active_api_keys?: number
  active_models?: number
  providers?: number
  /**
   * 2026-10-03：credits 降级时，服务端仍会给出真实的请求/费用数字，
   * 只有 total_credits_charged 是「没算出来」的 0。
   *
   * ⚠ 判据刻意**只认 credits_missing_view**、不认 `degraded`：
   * `degraded` 在载荷顶层是 board 整体（pies/trends）的降级标记，
   * 若拿它当积分降级依据，整屏降级时积分卡也会显示「不可信」——
   * 而那一屏明明有真实数字可显示。用例④固化的就是这个边界。
   */
  degraded?: boolean
  degraded_reason?: string
  credits_missing_view?: string
  credits_hint?: string
  /**
   * 2026-10-03：整屏汇总（请求/Token/费用）整体降级。
   * 与 credits 降级是**两个作用域**：`degraded_summary` 为真时，
   * 首屏所有汇总数字都是 0 且不可作为结论。
   */
  degraded_summary?: boolean
  summary_missing_view?: string
  summary_hint?: string
  /** 2026-11-20：汇总数字的真源（'usage_facts' = 对账级；缺省 = 分钟表/明细回退）。 */
  summary_source?: string
}

export interface BoardBackgroundTasks {
  discovery?: { running?: boolean; status?: string; trigger?: string; started_at?: string; heartbeat_at?: string }
  probe_loop?: { checks_last_10m?: number }
  /**
   * 2026-10-03：服务端查询失败时恒发 true。
   * 这两个 chip 讲的是「系统健康吗」——把「没查出来」渲染成 0 + 绿点，
   * 等于主动宣称健康，比显示不出数字更糟。
   */
  degraded?: boolean
  degraded_reason?: string
  probe_degraded?: boolean
}

export interface BoardSelfcheck {
  total_runs_24h?: number
  success_rate?: number
  last_status?: string
  last_run_at?: string
  /** 2026-10-03：见 BoardBackgroundTasks.degraded。 */
  degraded?: boolean
  degraded_reason?: string
}

export interface BoardOperationalPayload {
  background_tasks?: BoardBackgroundTasks
  selfcheck?: BoardSelfcheck
}

export interface BodySizeStats {
  avg_request_bytes?: number
  max_request_bytes?: number
  avg_response_bytes?: number
  max_response_bytes?: number
}

/**
 * 2026-10-03：看板饼图/趋势的降级账本。
 *
 * 服务端以前把 42P01 变成「该维度空数组」并且不写任何标记，前端 `?? []`
 * 之后与「这个维度真的没有数据」完全同形 —— 排行榜卡照常画一张空表。
 * 现在服务端恒发这两个字段：缺失与「空」在 API 层就分得开。
 *
 * `dimensions` 是**部分**降级的维度名，不是布尔：
 * 六个维度算得出来、第七个算不出来时，只有第七个需要提示。
 */
export interface BoardPiesDegradation {
  /**
   * 可选 —— 服务端健康时恒发 `degraded_pies: {}`（**不带** dimensions），
   * 降级时才带 `{ dimensions: [...], reason, ... }`。
   * 标成必填会让健康载荷在类型上不成立：那时 dimensions 根本不存在。
   */
  dimensions?: string[]
  reason?: string
  missing_view?: string
  hint?: string
}

export interface BoardPayload {
  summary: BoardSummary
  pies: {
    clients: BoardPieItem[]
    client_ips: BoardPieItem[]
    identity_hashes: BoardPieItem[]
    models: BoardPieItem[]
    errors: BoardPieItem[]
    tenants: BoardPieItem[]
    providers: BoardPieItem[]
  }
  trends: BoardTrendPoint[]
  model_series?: UsageTrendModelSeries[]
  bucket_minutes?: number
  provider_rows?: ProviderUsageRow[]
  window_start?: string
  window_end?: string
  tenant_id?: string
  provider_id?: number | null
  /**
   * 恒发（服务端无 omitempty）。`false` + 空对象 = 一切正常。
   * 用 `?.` 而不是 `?? {}` 兜底成 false：那会把「服务端没这个字段」
   * （老版本/缓存里的旧 payload）也读成「健康」，正是要避免的静默。
   */
  degraded?: boolean
  degraded_pies?: BoardPiesDegradation
  degraded_trends?: boolean
  degraded_trends_reason?: string
  trends_missing_view?: string
  trends_hint?: string
  degraded_reason?: string
  background_tasks?: BoardBackgroundTasks
  selfcheck?: BoardSelfcheck
  operational?: BoardOperationalPayload
  body_stats?: BodySizeStats
  days: number
  source?: 'postgresql_baseline' | 'redis_baseline_delta' | 'live_sse_delta' | string
  /**
   * 2026-11-20 口径对齐轮：英雄区汇总的探测口径。
   * The v2 board applies this traffic scope to its summary, trends and distributions.
   */
  probe_scope?: 'included' | 'excluded'
  cache_meta?: {
    fold_unit?: string
    scope?: string
    built_at?: string
    source?: string
  }
}

/**
 * 该饼图维度是否处于降级态（= 没算出来，而不是「为 0」）。
 *
 * 判据刻意**不**看 items 是否为空：空列表在正常情况下完全合法
 * （这段时间真没有新客户端）。只有服务端点名了这个维度才提示。
 */
export function isBoardPieDegraded(board: BoardPayload | null | undefined, dimension: string): boolean {
  const dims = board?.degraded_pies?.dimensions
  return Array.isArray(dims) && dims.includes(dimension)
}

export interface BoardQuery {
  days?: number
  start?: string
  end?: string
  tenant_id?: string
  provider_id?: number
  /** 含探测流量，同时作用于看板的全部请求统计。 */
  include_probes?: boolean
}

export function fetchDashboardBoard(params: BoardQuery = {}, signal?: AbortSignal): Promise<BoardPayload> {
  const qs = new URLSearchParams()
  if (params.start && params.end) {
    qs.set('start', params.start)
    qs.set('end', params.end)
  } else if (params.days != null) {
    qs.set('days', String(params.days))
  }
  if (params.tenant_id) qs.set('tenant_id', params.tenant_id)
  if (params.provider_id != null) qs.set('provider_id', String(params.provider_id))
  if (params.include_probes) qs.set('include_probes', '1')
  const q = qs.toString()
  const suffix = q ? `?${q}&include_operational=1` : '?include_operational=1'
  return req<BoardPayload>('GET', `/api/admin/dashboard/v2/board${suffix}`, undefined, { signal })
}

export function fetchBoardOperational(): Promise<BoardOperationalPayload> {
  return req<BoardOperationalPayload>('GET', '/api/admin/dashboard/operational')
}

export function fetchBoardErrorDrill(params: {
  error_kind: string
  days?: number
  dimension?: 'model' | 'provider' | 'client'
  tenant_id?: string
  provider_id?: number | null
  include_probes?: boolean
  window_start?: string
  window_end?: string
}): Promise<{ error_kind: string; dimension: string; items: BoardPieItem[] }> {
  const qs = new URLSearchParams()
  qs.set('error_kind', params.error_kind)
  if (params.days != null) qs.set('days', String(params.days))
  if (params.dimension) qs.set('dimension', params.dimension)
  if (params.provider_id != null) qs.set('provider_id', String(params.provider_id))
  if (params.include_probes) qs.set('include_probes', '1')
  if (params.window_start && params.window_end) {
    qs.set('window_start', params.window_start)
    qs.set('window_end', params.window_end)
  }
  if (params.tenant_id) qs.set('tenant_id', params.tenant_id)
  return req('GET', `/api/admin/dashboard/v2/board/error-drill?${qs}`)
}

/**
 * 分析详情数据结构 (2026-11-20)
 * 
 * 用于展示单个对象（模型/供应商/客户端/租户）的完整统计分析
 */
export interface AnalyticsTrendPoint {
  bucket: string              // ISO 8601 timestamp
  requests: number
  success: number
  errors: number
  tokens: number
  cost_usd: number
  credits: number
  avg_latency_ms: number
}

export interface AnalyticsSummary {
  total_requests: number
  success_count: number
  success_rate: number
  error_count: number
  total_tokens: number
  total_prompt_tokens?: number
  total_completion_tokens?: number
  total_cost_usd: number
  total_credits: number
  avg_latency_ms: number
  p95_latency_ms?: number
  p99_latency_ms?: number
}

export interface AnalyticsCrossAnalysis {
  by_model?: BoardPieItem[]
  by_provider?: BoardPieItem[]
  by_client?: BoardPieItem[]
  by_tenant?: BoardPieItem[]
  by_error?: BoardPieItem[]
  by_status?: BoardPieItem[]
}

export interface AnalyticsDetailPayload {
  dimension: 'model' | 'provider' | 'client' | 'tenant'
  identifier: string
  display_name?: string
  time_range: {
    start: string
    end: string
    days: number
  }
  summary: AnalyticsSummary
  trends: AnalyticsTrendPoint[]
  cross_analysis: AnalyticsCrossAnalysis
  
  // 降级标记
  degraded?: boolean
  degraded_reason?: string
  degraded_trends?: boolean
  degraded_cross?: string[]  // 降级的交叉分析维度
}

export interface AnalyticsQuery {
  days?: number
  start?: string
  end?: string
  tenant_id?: string
  include_probes?: boolean
}

/**
 * 获取分析详情数据
 */
export function fetchAnalyticsDetail(
  dimension: 'model' | 'provider' | 'client' | 'tenant',
  identifier: string,
  params: AnalyticsQuery = {},
  signal?: AbortSignal
): Promise<AnalyticsDetailPayload> {
  const qs = new URLSearchParams()
  
  if (params.start && params.end) {
    qs.set('start', params.start)
    qs.set('end', params.end)
  } else if (params.days != null) {
    qs.set('days', String(params.days))
  }
  
  if (params.tenant_id) {
    qs.set('tenant_id', params.tenant_id)
  }
  if (params.include_probes) qs.set('include_probes', 'true')
  
  const query = qs.toString()
  const url = `/api/admin/analytics/v2/${dimension}/${encodeURIComponent(identifier)}${query ? '?' + query : ''}`
  
  return req<AnalyticsDetailPayload>('GET', url, undefined, { signal })
}
