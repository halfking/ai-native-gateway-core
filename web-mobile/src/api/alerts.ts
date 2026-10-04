import { req, type RequestOptions } from './client'

// alerts.ts — /api/candidate-failures/alerts（内存告警环，形状对齐
// bg.CandidateFailureAlert + admin listRecentAlerts 的 {data, count} 包裹）。

export interface CandidateFailureAlert {
  ts: string
  credential_id: number
  provider_id: number
  raw_model_name: string
  error_kind: string
  count: number
  window_sec: number
  distinct_status_codes: number
  last_response_preview?: string
}

export interface AlertsResponse {
  data: CandidateFailureAlert[]
  count: number
}

export function fetchAlerts(options?: RequestOptions): Promise<AlertsResponse> {
  return req<AlertsResponse>('GET', '/api/candidate-failures/alerts', undefined, options)
}
