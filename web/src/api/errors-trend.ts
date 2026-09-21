import { req, type RequestOptions } from './_core'

// errors-trend.ts — GET /api/errors/trend 客户端（R12 候选18 UI 接入）。
// 读源：supplier_error_stats 预聚合（stats），窗口太新时对
// supplier_errors_unified 实时聚合（fallback，响应标注 source）。

export type ErrorsTrendHours = '1' | '24' | '168'

export interface ErrorsTrendPoint {
  timestamp: string
  error_count: number
  unique_requests: number
  affected_users: number
  by_supplier?: Record<string, number>
  by_error_type?: Record<string, number>
}

export interface ErrorsTrendBreakdownRow {
  key: string
  count: number
}

export interface ErrorsTrendSummary {
  total_errors: number
  unique_requests: number
  top_error_types: ErrorsTrendBreakdownRow[]
  top_suppliers: ErrorsTrendBreakdownRow[]
  affected_credentials: number
}

export interface ErrorsTrendResponse {
  source: 'stats' | 'fallback'
  granularity: 'minute' | 'hour' | 'day'
  hours: number
  since: string
  until: string
  time_series: ErrorsTrendPoint[]
  summary: ErrorsTrendSummary
}

export function getErrorsTrend(hours: ErrorsTrendHours = '24', options?: RequestOptions) {
  const params = new URLSearchParams({ hours })
  return req<ErrorsTrendResponse>(
    'GET',
    `/api/errors/trend?${params.toString()}`,
    undefined,
    options,
  )
}
