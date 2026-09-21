import { req } from './_core'

// request-anomalies.ts — 请求侧异常（reqprobe）API（2026-09-21）。
// 与 format-anomalies（响应格式异常，PG 表）并列的数据面：上游 4xx 中
// "参数被拒 / 请求形态不匹配"的探测记录（Full=Redis / lite=内存存储）。

export type RequestAnomalyTrigger = 'param_rejected' | 'mode_mismatch' | 'upstream_error'

export interface RequestAnomalyRecord {
  id: number
  fingerprint: string
  day: string
  provider_id: number
  provider_code: string
  client_model?: string
  outbound_model?: string
  protocol?: string
  trigger: RequestAnomalyTrigger
  param?: string
  suggest_mode?: string
  http_status: number
  error_kind?: string
  error_sample?: string
  occurrences: number
  recovered_count: number
  first_seen: string
  last_seen: string
  last_request_id?: string
  resolved: boolean
  resolved_at?: string
  resolution_notes?: string
}

export interface RequestAnomalyCounts {
  unresolved: number
  new_today: number
}

export interface GetRequestAnomaliesParams {
  limit?: number
  offset?: number
  day?: string
  provider?: string
  model?: string
  trigger?: string
  unresolved_only?: boolean
}

export interface GetRequestAnomaliesResponse {
  anomalies: RequestAnomalyRecord[]
  count: number
  limit: number
  offset: number
}

export function getRequestAnomalies(params: GetRequestAnomaliesParams = {}) {
  const q = new URLSearchParams()
  if (params.limit) q.set('limit', String(params.limit))
  if (params.offset) q.set('offset', String(params.offset))
  if (params.day) q.set('day', params.day)
  if (params.provider) q.set('provider', params.provider)
  if (params.model) q.set('model', params.model)
  if (params.trigger) q.set('trigger', params.trigger)
  if (params.unresolved_only) q.set('unresolved_only', 'true')
  const qs = q.toString()
  return req<GetRequestAnomaliesResponse>('GET', '/api/admin/request-anomalies' + (qs ? '?' + qs : ''))
}

/** 导航栏徽标计数（未解决总数 + 今日新增）。 */
export function getRequestAnomalyCounts() {
  return req<RequestAnomalyCounts>('GET', '/api/admin/request-anomalies/count')
}

export function resolveRequestAnomaly(id: number, resolution_notes: string) {
  return req<{ success: boolean; message: string }>('POST', `/api/admin/request-anomalies/${id}/resolve`, {
    resolution_notes,
  })
}

export interface BatchResolveRequestAnomaliesBody {
  ids?: number[]
  /** 按 ids 批量；为 true 时按 day/provider/model/trigger 过滤批量解决全部未解决项 */
  all_unresolved?: boolean
  day?: string
  provider?: string
  model?: string
  trigger?: string
  resolution_notes?: string
}

export function batchResolveRequestAnomalies(body: BatchResolveRequestAnomaliesBody) {
  return req<{ success: boolean; resolved: number }>('POST', '/api/admin/request-anomalies/batch-resolve', body)
}
