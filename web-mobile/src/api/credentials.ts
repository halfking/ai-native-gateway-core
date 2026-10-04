import { req } from './_core'

// credentials.ts — 节点（凭据健康），类型对齐 web/src/api/credential-monitor.ts。

export interface CredentialModelStatus {
  raw_model_name: string
  canonical_name?: string | null
  offer_available: boolean
  offer_unavailable_reason?: string | null
  binding_available: boolean
  probe_state: string
  probe_last_status?: string | null
  probe_last_attempt_at?: string | null
  recent_success_rate?: number | null
  recent_samples: number
  p95_latency_ms?: number | null
}

export interface CredentialMonitorSummary {
  id: number
  provider_id: number
  provider_name: string
  label: string
  status: string
  effective_state?: string | null
  effective_reason?: string | null
  availability_state: string
  health_status: string
  quota_state: string
  effective_concurrency: number
  manual_disabled: boolean
  consecutive_failures: number
  total_requests: number
  model_total: number
  model_available: number
  broken_model_count: number
  models?: CredentialModelStatus[]
  aggregated_success_rate?: number | null
}

export function fetchMonitorSummary(signal?: AbortSignal) {
  return req<{ data?: CredentialMonitorSummary[]; summary?: CredentialMonitorSummary[] } | CredentialMonitorSummary[]>(
    'GET',
    '/api/credentials/monitor-summary',
    undefined,
    signal,
  )
}

/** 兼容三种历史形状：{data:[...]} / {summary:[...]} / 裸数组。 */
export function normalizeMonitorPayload(
  payload: { data?: CredentialMonitorSummary[]; summary?: CredentialMonitorSummary[] } | CredentialMonitorSummary[],
): CredentialMonitorSummary[] {
  if (Array.isArray(payload)) return payload
  return payload.data ?? payload.summary ?? []
}
