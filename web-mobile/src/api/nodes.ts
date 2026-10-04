import { req, type RequestOptions } from './client'

// nodes.ts — /api/credentials/monitor-summary（凭据/节点健康，形状对齐
// web/src/api/credential-monitor.ts，字段裁剪到移动端渲染面）。

export interface CredentialModelStatus {
  raw_model_name: string
  canonical_name?: string | null
  offer_available: boolean
  binding_available: boolean
  /** 'broken_confirmed' | 'healthy_confirmed' | 'recovering' | 'unknown' */
  probe_state: string
  probe_last_status?: string | null
  recent_success_rate?: number | null
  p95_latency_ms?: number | null
  offer_unavailable_reason?: string | null
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
  concurrency_limit: number | null
  manual_disabled: boolean
  consecutive_failures: number
  state_reason_code: string | null
  state_reason_detail: string | null
  health_checked_at: string | null
  total_requests: number
  model_total: number
  model_available: number
  broken_model_count: number
  models?: CredentialModelStatus[]
  aggregated_success_rate?: number | null
}

export function fetchMonitorSummary(options?: RequestOptions): Promise<CredentialMonitorSummary[]> {
  return req<CredentialMonitorSummary[]>('GET', '/api/credentials/monitor-summary', undefined, options)
}
