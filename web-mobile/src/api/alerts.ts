import { req } from './_core'

// alerts.ts — 告警时间线（内存告警环），对齐 bg.CandidateFailureAlert。

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

export function fetchAlerts(signal?: AbortSignal) {
  return req<{ data: CandidateFailureAlert[]; count: number }>(
    'GET',
    '/api/candidate-failures/alerts',
    undefined,
    signal,
  )
}
