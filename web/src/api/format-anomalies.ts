import { req } from './_core'

export interface FormatAnomalyRecord {
  id: number
  detected_at: string
  request_id: string
  provider_id?: number
  provider_code?: string
  client_model?: string
  outbound_model?: string
  anomaly_type: string
  severity: string
  usage_source?: string
  expected_tokens?: number
  actual_tokens?: number
  content_size_bytes?: number
  response_structure?: Record<string, any>
  response_sample?: string
  resolved: boolean
  resolved_at?: string
  resolution_notes?: string
  tenant_id?: string
  created_at: string
}

export interface FormatAnomalySummary {
  hour: string
  provider_code?: string
  client_model?: string
  anomaly_type: string
  severity: string
  anomaly_count: number
  affected_requests: number
  avg_content_size?: number
  avg_expected_tokens?: number
  avg_actual_tokens?: number
  resolved_count: number
}

export interface GetFormatAnomaliesParams {
  limit?: number
  offset?: number
  provider?: string
  model?: string
  anomaly_type?: string
  unresolved_only?: boolean
}

export interface GetFormatAnomaliesResponse {
  anomalies: FormatAnomalyRecord[]
  count: number
  limit: number
  offset: number
}

export interface GetFormatAnomalySummaryResponse {
  summaries: FormatAnomalySummary[]
  count: number
  hours: number
}

export function getFormatAnomalies(params: GetFormatAnomaliesParams = {}) {
  const q = new URLSearchParams()
  if (params.limit) q.set('limit', String(params.limit))
  if (params.offset) q.set('offset', String(params.offset))
  if (params.provider) q.set('provider', params.provider)
  if (params.model) q.set('model', params.model)
  if (params.anomaly_type) q.set('anomaly_type', params.anomaly_type)
  if (params.unresolved_only) q.set('unresolved_only', 'true')
  const qs = q.toString()
  return req<GetFormatAnomaliesResponse>('GET', '/api/admin/format-anomalies' + (qs ? '?' + qs : ''))
}

export function getFormatAnomalySummary(hours = 24) {
  const q = new URLSearchParams()
  q.set('hours', String(hours))
  return req<GetFormatAnomalySummaryResponse>('GET', '/api/admin/format-anomaly-summary?' + q.toString())
}

export function resolveFormatAnomaly(id: number, resolution_notes: string) {
  return req<{ success: boolean; message: string }>('POST', `/api/admin/format-anomalies/${id}/resolve`, {
    resolution_notes,
  })
}
