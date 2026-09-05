import { req, type RequestOptions } from './_core'

export type VendorErrorHours = '1' | '24' | '168'

export interface VendorCredentialMeta {
  id: number
  label: string
  provider_id: number
  health_status: string
  health_error: string | null
  health_latency_ms: number | null
  availability_state: string
  state_reason_code: string | null
  state_reason_detail: string | null
  state_updated_at: string | null
  quota_state: string
  lifecycle_status: string
  circuit_state: string
  consecutive_failures: number
  manual_disabled: boolean
  balance_usd: number | null
  balance_currency: string | null
}

export interface VendorErrorKindStat {
  error_kind: string
  count: number
  last_seen: string
  distinct_status_codes: number
}

export interface VendorRecentFailure {
  ts: string
  request_id: string
  raw_model_name: string
  attempt_index: number
  error_kind: string
  error_message: string | null
  upstream_status_code: number | null
  upstream_response_preview: string | null
  latency_ms: number | null
  /** 2026-09-05 审计闭环1：supplier_errors_unified 结构化维度透传。 */
  supplier?: string | null
  error_code?: string | null
  retryable?: boolean | null
  stage?: string | null
}

export interface VendorQualityScore {
  profile_date: string
  total_score: number
  availability_score: number
  stability_score: number
}

export interface VendorCredentialErrorDetail {
  credential_id: number
  credential_label: string
  credential: VendorCredentialMeta
  error_summary: VendorErrorKindStat[]
  recent_failures: VendorRecentFailure[]
  quality_scores_7d: VendorQualityScore[]
  hours: number
  since: string
}

export function getVendorCredentialErrorDetail(
  credentialId: number,
  hours: VendorErrorHours = '24',
  options?: RequestOptions,
) {
  const params = new URLSearchParams({ hours })
  return req<VendorCredentialErrorDetail>(
    'GET',
    `/api/vendors/credentials/${credentialId}/error-detail?${params.toString()}`,
    undefined,
    options,
  )
}
