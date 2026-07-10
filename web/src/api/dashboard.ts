// web/src/api/dashboard.ts
// 首页 Dashboard 标准 API 封装
//
// 提供：
//   - 类型安全的 TypeScript 接口
//   - 统一的错误处理
//   - 自动重试和缓存
//   - 请求埋点（自动调用 telemetry）

import { req } from './_core'
import type { AxiosError } from 'axios'

// ════════════════════════════════════════════════════════════════
// 统一响应格式
// ════════════════════════════════════════════════════════════════

export interface ApiResponse<T> {
  success: boolean
  code?: string
  message?: string
  data?: T
  error?: {
    code: string
    message: string
    details?: string
  }
  metadata?: {
    total?: number
    page?: number
    size?: number
    pages?: number
    cache_hit?: boolean
    generated_at: string
    took_ms?: number
  }
  timestamp: string
}

// ════════════════════════════════════════════════════════════════
// 标准错误码
// ════════════════════════════════════════════════════════════════

export const ErrorCode = {
  INVALID_PARAM: 'INVALID_PARAM',
  UNAUTHORIZED: 'UNAUTHORIZED',
  FORBIDDEN: 'FORBIDDEN',
  NOT_FOUND: 'NOT_FOUND',
  INTERNAL_ERROR: 'INTERNAL_ERROR',
  DATABASE_ERROR: 'DATABASE_ERROR',
  CACHE_ERROR: 'CACHE_ERROR',
  TOO_MANY_REQUESTS: 'TOO_MANY_REQUESTS',
  DATA_NOT_READY: 'DATA_NOT_READY',
} as const

export type ErrorCodeType = typeof ErrorCode[keyof typeof ErrorCode]

// ════════════════════════════════════════════════════════════════
// 通用查询参数
// ════════════════════════════════════════════════════════════════

export interface QueryParams {
  tenant_id?: string
  days?: number
  page?: number
  size?: number
  sort_by?: string
  sort_dir?: 'asc' | 'desc'
  search?: string
  refresh?: boolean
}

// ════════════════════════════════════════════════════════════════
// 1. 会话总览 API
// ════════════════════════════════════════════════════════════════

export interface SessionOverviewData {
  // 核心指标
  total_sessions: number
  active_sessions: number
  new_sessions_24h: number
  closed_sessions_24h: number

  // 健康度分布
  health_distribution: {
    total: number
    a: number
    b: number
    c: number
    d: number
    f: number
    a_percent: number
    b_percent: number
    c_percent: number
    d_percent: number
    f_percent: number
    avg_score: number
  }

  // 合规状态
  compliance_stats: {
    total: number
    compliant: number
    warning: number
    violation: number
    prompt_injection_detected: number
    pii_detected: number
    toxic_output_detected: number
    compliance_rate: number
  }

  // 成本统计
  cost_stats: {
    total_cost_usd: number
    avg_cost_per_session: number
    avg_cost_per_request: number
    max_cost_session: number
    input_cost_usd: number
    output_cost_usd: number
    cost_growth_pct: number
  }

  // 模型使用
  model_usage: Array<{
    model: string
    session_count: number
    request_count: number
    total_cost: number
    avg_latency_ms: number
    success_rate: number
  }>

  // Top 排行
  top_clients: Array<{
    client_id: string
    session_count: number
    total_cost: number
    avg_health?: number
    last_activity: string
  }>

  top_tasks: Array<{
    task_id: string
    session_count: number
    total_cost: number
    avg_health?: number
    last_activity: string
  }>

  // 趋势数据
  cost_trend: Array<{
    date: string
    cost: number
    sessions: number
    requests: number
  }>

  session_trend: Array<{
    date: string
    new_sessions: number
    active_count: number
    closed_count: number
  }>

  // 时间戳
  generated_at: string
  period_start: string
  period_end: string
}

/**
 * 获取会话总览数据
 *
 * @param params 查询参数
 * @returns 会话总览数据
 */
export function getSessionOverview(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    days: params.days || 7,
    ...params,
  })
  return req<ApiResponse<SessionOverviewData>>(
    'GET',
    `/api/admin/dashboard/session-overview?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 2. 会话趋势 API
// ════════════════════════════════════════════════════════════════

export interface SessionTrendData {
  period: {
    start: string
    end: string
    days: number
  }
  trend: Array<{
    date: string
    new_sessions: number
    active_sessions: number
    closed_sessions: number
    total_cost: number
    total_requests: number
  }>
  summary: {
    total_new: number
    total_active: number
    total_closed: number
    avg_daily_cost: number
    growth_rate: number
  }
}

export function getSessionTrend(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    days: params.days || 7,
    ...params,
  })
  return req<ApiResponse<SessionTrendData>>(
    'GET',
    `/api/admin/dashboard/session-trend?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 3. 健康度分布 API
// ════════════════════════════════════════════════════════════════

export interface HealthDistributionData {
  distribution: {
    a: number
    b: number
    c: number
    d: number
    f: number
  }
  percentages: {
    a: number
    b: number
    c: number
    d: number
    f: number
  }
  total: number
  avg_score: number
  by_tenant: Array<{
    tenant_id: string
    distribution: { a: number; b: number; c: number; d: number; f: number }
    avg_score: number
  }>
}

export function getHealthDistribution(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    days: params.days || 7,
    ...params,
  })
  return req<ApiResponse<HealthDistributionData>>(
    'GET',
    `/api/admin/dashboard/session-health?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 4. 活跃会话 API
// ════════════════════════════════════════════════════════════════

export interface ActiveSessionItem {
  session_id: string
  tenant_id: string
  last_activity: string
  duration_seconds: number
  request_count: number
  total_cost: number
  primary_model: string
  health_grade?: string
  health_score?: number
  client_id?: string
  task_id?: string
}

export interface ActiveSessionsData {
  sessions: ActiveSessionItem[]
  total: number
}

export function getActiveSessions(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    size: params.size || 20,
    ...params,
  })
  return req<ApiResponse<ActiveSessionsData>>(
    'GET',
    `/api/admin/dashboard/session-active?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 5. 最近会话 API
// ════════════════════════════════════════════════════════════════

export interface RecentSessionItem {
  session_id: string
  tenant_id: string
  first_request_at: string
  last_request_at: string
  request_count: number
  total_cost: number
  models_used: string[]
  health_grade?: string
  title?: string
}

export function getRecentSessions(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    size: params.size || 20,
    ...params,
  })
  return req<ApiResponse<{ sessions: RecentSessionItem[]; total: number }>>(
    'GET',
    `/api/admin/dashboard/session-recent?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 6. 模块执行统计 API
// ════════════════════════════════════════════════════════════════

export interface ModuleStatsItem {
  module_name: string
  module_version: string
  total_executions: number
  success_count: number
  failed_count: number
  skipped_count: number
  avg_duration_ms: number
  p95_duration_ms: number
  cache_hit_rate: number
  unique_sessions: number
  last_executed_at?: string
}

export interface ModuleStatsSummary {
  total_modules: number
  total_executions: number
  avg_cache_hit_rate: number
  avg_duration_ms: number
}

export interface ModuleStatsData {
  modules: ModuleStatsItem[]
  summary: ModuleStatsSummary
  period_start: string
  period_end: string
}

export function getModuleStats(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    days: params.days || 7,
    ...params,
  })
  return req<ApiResponse<ModuleStatsData>>(
    'GET',
    `/api/admin/dashboard/module-stats?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 7. 错误统计 API
// ════════════════════════════════════════════════════════════════

export interface ErrorSummary {
  total_errors: number
  error_rate: number
  total_requests: number
  avg_error_latency_ms: number
}

export interface ErrorDistItem {
  error_type: string
  count: number
  percentage: number
}

export interface ErrorTrendItem {
  date: string
  error_count: number
  total_count: number
}

export interface ErrorDetail {
  error_message: string
  count: number
  last_occurred: string
  module: string
}

export interface ErrorStatsData {
  summary: ErrorSummary
  distribution: ErrorDistItem[]
  trend: ErrorTrendItem[]
  top_errors: ErrorDetail[]
}

export function getErrorStats(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    days: params.days || 7,
    ...params,
  })
  return req<ApiResponse<ErrorStatsData>>(
    'GET',
    `/api/admin/dashboard/errors?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 8. 性能指标 API
// ════════════════════════════════════════════════════════════════

export interface PerformanceSummary {
  avg_latency_ms: number
  p50_latency_ms: number
  p95_latency_ms: number
  p99_latency_ms: number
  max_latency_ms: number
  total_requests: number
  avg_throughput_rps: number
}

export interface LatencyDistribution {
  under_100ms: number
  under_500ms: number
  under_1000ms: number
  under_5000ms: number
  over_5000ms: number
}

export interface ThroughputPoint {
  date: string
  request_count: number
  avg_latency_ms: number
}

export interface SlowQueryItem {
  session_key: string
  module_name: string
  duration_ms: number
  executed_at: string
  error_message?: string
}

export interface PerformanceData {
  summary: PerformanceSummary
  latency_distribution: LatencyDistribution
  throughput: ThroughputPoint[]
  slow_queries: SlowQueryItem[]
}

export function getPerformanceStats(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    days: params.days || 7,
    ...params,
  })
  return req<ApiResponse<PerformanceData>>(
    'GET',
    `/api/admin/dashboard/performance?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 9. 异常会话 API
// ════════════════════════════════════════════════════════════════

export interface AnomalySessionItem {
  session_id: string
  tenant_id: string
  anomaly_type: string  // high_cost / high_latency / low_health / compliance_violation
  severity: 'low' | 'medium' | 'high' | 'critical'
  description: string
  detected_at: string
  metrics: Record<string, any>
}

export function getSessionAnomalies(params: QueryParams = {}) {
  const searchParams = buildSearchParams({
    size: params.size || 20,
    ...params,
  })
  return req<ApiResponse<{ anomalies: AnomalySessionItem[]; total: number }>>(
    'GET',
    `/api/admin/dashboard/session-anomalies?${searchParams}`
  )
}

// ════════════════════════════════════════════════════════════════
// 7. 导出数据 API
// ════════════════════════════════════════════════════════════════

export interface ExportRequest {
  export_type: 'overview' | 'trend' | 'health' | 'sessions'
  format: 'csv' | 'json' | 'excel'
  days: number
  tenant_id?: string
  filters?: Record<string, any>
}

export interface ExportResponse {
  export_id: string
  status: 'pending' | 'processing' | 'completed' | 'failed'
  download_url?: string
  expires_at?: string
}

export function exportSessionData(request: ExportRequest) {
  return req<ApiResponse<ExportResponse>>(
    'POST',
    '/api/admin/dashboard/session-export',
    request
  )
}

// ════════════════════════════════════════════════════════════════
// 工具函数
// ════════════════════════════════════════════════════════════════

function buildSearchParams(params: Record<string, any>): string {
  const searchParams = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') {
      searchParams.append(key, String(value))
    }
  }
  return searchParams.toString()
}

// ════════════════════════════════════════════════════════════════
// 错误处理
// ════════════════════════════════════════════════════════════════

export class DashboardApiError extends Error {
  code: ErrorCodeType
  details?: string

  constructor(error: { code: string; message: string; details?: string }) {
    super(error.message)
    this.code = error.code as ErrorCodeType
    this.details = error.details
  }
}

/**
 * 统一处理 API 错误
 */
export function handleApiError(error: unknown): never {
  if (error instanceof Error) {
    // Axios 错误
    if ('response' in error) {
      const axiosError = error as AxiosError<ApiResponse<any>>
      const responseData = axiosError.response?.data
      if (responseData?.error) {
        throw new DashboardApiError(responseData.error)
      }
      throw new DashboardApiError({
        code: ErrorCode.INTERNAL_ERROR,
        message: axiosError.message,
      })
    }
    throw new DashboardApiError({
      code: ErrorCode.INTERNAL_ERROR,
      message: error.message,
    })
  }
  throw new DashboardApiError({
    code: ErrorCode.INTERNAL_ERROR,
    message: 'Unknown error',
  })
}

// ════════════════════════════════════════════════════════════════
// 便捷方法：获取数据（自动解包 response.data）
// ════════════════════════════════════════════════════════════════

/**
 * 获取会话总览数据（自动解包）
 */
export async function fetchSessionOverview(params?: QueryParams): Promise<{
  data: SessionOverviewData
  metadata?: ApiResponse<SessionOverviewData>['metadata']
}> {
  try {
    const response = await getSessionOverview(params)
    if (!response.success || !response.data) {
      throw new DashboardApiError(response.error || {
        code: ErrorCode.INTERNAL_ERROR,
        message: 'Invalid response',
      })
    }
    return {
      data: response.data,
      metadata: response.metadata,
    }
  } catch (error) {
    handleApiError(error)
  }
}

/**
 * 获取会话趋势数据（自动解包）
 */
export async function fetchSessionTrend(params?: QueryParams): Promise<SessionTrendData> {
  try {
    const response = await getSessionTrend(params)
    if (!response.success || !response.data) {
      throw new DashboardApiError(response.error || {
        code: ErrorCode.INTERNAL_ERROR,
        message: 'Invalid response',
      })
    }
    return response.data
  } catch (error) {
    handleApiError(error)
  }
}

/**
 * 获取活跃会话列表（自动解包）
 */
export async function fetchActiveSessions(params?: QueryParams): Promise<ActiveSessionItem[]> {
  try {
    const response = await getActiveSessions(params)
    if (!response.success || !response.data) {
      throw new DashboardApiError(response.error || {
        code: ErrorCode.INTERNAL_ERROR,
        message: 'Invalid response',
      })
    }
    return response.data.sessions
  } catch (error) {
    handleApiError(error)
  }
}

/**
 * 获取模块执行统计（自动解包）
 */
export async function fetchModuleStats(params?: QueryParams): Promise<ModuleStatsData> {
  try {
    const response = await getModuleStats(params)
    if (!response.success || !response.data) {
      throw new DashboardApiError(response.error || {
        code: ErrorCode.INTERNAL_ERROR,
        message: 'Invalid response',
      })
    }
    return response.data
  } catch (error) {
    handleApiError(error)
  }
}

/**
 * 获取错误统计（自动解包）
 */
export async function fetchErrorStats(params?: QueryParams): Promise<ErrorStatsData> {
  try {
    const response = await getErrorStats(params)
    if (!response.success || !response.data) {
      throw new DashboardApiError(response.error || {
        code: ErrorCode.INTERNAL_ERROR,
        message: 'Invalid response',
      })
    }
    return response.data
  } catch (error) {
    handleApiError(error)
  }
}

/**
 * 获取性能指标（自动解包）
 */
export async function fetchPerformanceStats(params?: QueryParams): Promise<PerformanceData> {
  try {
    const response = await getPerformanceStats(params)
    if (!response.success || !response.data) {
      throw new DashboardApiError(response.error || {
        code: ErrorCode.INTERNAL_ERROR,
        message: 'Invalid response',
      })
    }
    return response.data
  } catch (error) {
    handleApiError(error)
  }
}