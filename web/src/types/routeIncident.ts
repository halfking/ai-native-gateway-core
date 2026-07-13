// routeIncident.ts — types for the dashboard's "diagnose" feature.
//
// Mirrors the server-side DTOs in domains/routeincident/types.go and
// admin/route_incidents.go. Only sanitized, dashboard-safe fields
// appear here. The names align with the spec
// docs/superpowers/specs/2026-07-13-route-incident-diagnosis-design.md
// so a cross-walk against the spec is a 1:1 find-replace.

export type RouteIncidentState = 'active' | 'recovering' | 'recovered'

export interface RouteIncidentRouteKey {
  endpoint_protocol: string
  model: string
  provider_id?: number | null
  credential_id?: number | null
  // NOTE: tenant_id is intentionally NOT in the wire body (see
  // admin/live_stream_sse.go BuildUpdate). The dashboard infers the
  // tenant from the cookie scope; cross-tenant viewing is impossible.
}

export interface RouteIncidentAffectedLane {
  dimension: 'provider' | 'model' | 'vendor'
  value: string
}

export interface RouteIncidentSanitizedError {
  kind: string
  stage?: string
}

export interface RouteIncidentUpdate {
  type: 'incident_update'
  incident_id: string
  state: RouteIncidentState
  failure_streak: number
  recovery_streak: number
  visible: boolean
  affected_lanes?: RouteIncidentAffectedLane[]
  last_error?: RouteIncidentSanitizedError
  route_key: RouteIncidentRouteKey
  updated_at: string
}

export interface RouteIncident {
  id: string
  route_key: RouteIncidentRouteKey
  state: RouteIncidentState
  failure_streak: number
  recovery_streak: number
  first_failure_at: string
  last_failure_at?: string | null
  last_success_at?: string | null
  recovered_at?: string | null
  total_failures: number
  total_successes: number
  last_error_kind?: string | null
  last_failure_stage?: string | null
  version: number
  created_at: string
  updated_at: string
}

export type RouteIncidentEventType =
  | 'opened'
  | 'failure_observed'
  | 'recovery_progress'
  | 'recovered'
  | 'diagnostic_run'
  | 'operator_action'

export interface RouteIncidentEvent {
  id: number
  incident_id: string
  event_type: RouteIncidentEventType
  request_id?: string | null
  terminal_status?: 'success' | 'failure' | null
  failure_kind?: string | null
  failure_stage?: 'gateway' | 'upstream' | null
  failure_streak?: number | null
  recovery_streak?: number | null
  evidence?: Record<string, unknown>
  actor?: string | null
  created_at: string
}

export interface RouteIncidentTimelinePoint {
  bucket_start: string
  requests: number
  errors: number
  avg_latency_ms?: number | null
  p99_latency_ms?: number | null
  recoveries: number
}

export interface RouteIncidentFinding {
  kind: string
  label: string
  sample_count: number
  interval_sec: number
  request_ids?: string[]
  note?: string
}

export interface RouteIncidentRouteSnapshot {
  protocol: string
  canonical_model: string
  outbound_model: string
  provider_code?: string
  provider_id?: number | null
  credential_id?: number | null
  credential_label?: string
  routing_decision: string
  retry_path?: string
}

export interface RouteIncidentResourceSnapshot {
  slots_in_use?: number | null
  slots_total?: number | null
  live_concurrent?: number | null
  circuit_state?: string | null
  quota_remaining?: number | null
  availability_state?: string | null
  action_history_count: number
}

export interface RouteIncidentRecoveryProgress {
  current: number
  target: number
}

export interface RouteIncidentDetail {
  incident: RouteIncident
  current_route: RouteIncidentRouteSnapshot
  findings: RouteIncidentFinding[]
  sample_request_ids?: string[]
  recovery_progress: RouteIncidentRecoveryProgress
  timeline: RouteIncidentTimelinePoint[]
  resource_snapshot: RouteIncidentResourceSnapshot
  insufficient_data?: string[]
}

export interface RouteIncidentListResponse {
  items: RouteIncident[]
  count: number
}

export interface RouteIncidentEventsResponse {
  items: RouteIncidentEvent[]
  count: number
}

export interface RouteIncidentTimelineResponse {
  items: RouteIncidentTimelinePoint[]
  count: number
  insufficient_data?: string[]
}

// LaneKey is the per-dimension key the dashboard uses to associate
// an incident with a swim lane. The backend's incident_update
// envelope carries the affected_lanes list; the frontend reduces
// it to a LaneKey per active groupBy dimension.
export interface LaneKey {
  dimension: 'vendor' | 'provider' | 'model'
  value: string
}
