// 2026-07-28: Web API client for /api/admin/model-integrity/*.
// Mirrors web/src/api/format-anomalies.ts so the two dashboards
// share a mental model.

import { req } from './_core'

export interface ModelIntegrityRecord {
  id: number
  detected_at: string
  request_id?: string
  provider_id?: number
  provider_code?: string
  credential_id?: number
  client_model?: string
  outbound_model?: string
  raw_model_name?: string
  anomaly_type: string
  severity: 'low' | 'medium' | 'high' | 'critical'
  expected_value?: string
  actual_value?: string
  sample?: string
  context?: Record<string, any>
  resolved: boolean
  resolved_at?: string
  resolution_notes?: string
  tenant_id?: string
}

export interface ModelIntegritySummary {
  hour: string
  provider_code?: string
  client_model?: string
  anomaly_type: string
  severity: string
  anomaly_count: number
  affected_requests: number
  resolved_count: number
}

export interface GetModelIntegrityParams {
  limit?: number
  offset?: number
  provider?: string
  model?: string
  anomaly_type?: string
  severity?: string
  unresolved_only?: boolean
}

export interface GetModelIntegrityResponse {
  events: ModelIntegrityRecord[]
  count: number
  limit: number
  offset: number
}

export interface GetModelIntegritySummaryResponse {
  summaries: ModelIntegritySummary[]
  count: number
  hours: number
}

export interface GetModelIntegrityDriftResponse {
  events: ModelIntegrityRecord[]
  count: number
  days: number
}

export function getModelIntegrityEvents(params: GetModelIntegrityParams = {}) {
  const q = new URLSearchParams()
  if (params.limit) q.set('limit', String(params.limit))
  if (params.offset) q.set('offset', String(params.offset))
  if (params.provider) q.set('provider', params.provider)
  if (params.model) q.set('model', params.model)
  if (params.anomaly_type) q.set('anomaly_type', params.anomaly_type)
  if (params.severity) q.set('severity', params.severity)
  if (params.unresolved_only) q.set('unresolved_only', 'true')
  const qs = q.toString()
  return req<GetModelIntegrityResponse>(
    'GET',
    '/api/admin/model-integrity/events' + (qs ? '?' + qs : ''),
  )
}

export function getModelIntegritySummary(hours = 24) {
  const q = new URLSearchParams()
  q.set('hours', String(hours))
  return req<GetModelIntegritySummaryResponse>(
    'GET',
    '/api/admin/model-integrity/summary?' + q.toString(),
  )
}

export function getModelIntegrityFingerprintDrift(days = 7) {
  const q = new URLSearchParams()
  q.set('days', String(days))
  return req<GetModelIntegrityDriftResponse>(
    'GET',
    '/api/admin/model-integrity/fingerprint-drift?' + q.toString(),
  )
}

export function resolveModelIntegrity(id: number, resolution_notes: string) {
  return req<{ success: boolean; message: string }>(
    'POST',
    `/api/admin/model-integrity/events/${id}/resolve`,
    { resolution_notes },
  )
}
