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

// ─── Phase 2: action / diagnostic-run / audit / evidence ────────

export type ActionKind =
  | 'direct_upstream_test'
  | 'through_gateway_test'
  | 'reprobe'
  | 'release_slot'
  | 'reset_slots'
  | 'reset_availability'
  | 'recover'
  | 'evidence_export'

export type ActionOutcome = 'success' | 'noop' | 'failed'

export type DiagnosticRunState =
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'cancelled'

export interface ActionRequest {
  reason: string
  confirmation_token?: string
  idempotency_key: string
  parameters?: Record<string, unknown>
}

export interface ActionResponse {
  audit_id: number
  outcome: ActionOutcome
  failure_reason?: string | null
  diagnostic_run?: DiagnosticRun | null
  incident?: RouteIncident | null
  response?: Record<string, unknown>
  idempotent: boolean
}

export interface DiagnosticRun {
  id: string
  incident_id: string
  tenant_id: string
  kind: ActionKind
  state: DiagnosticRunState
  route_key: Record<string, unknown>
  parameters: Record<string, unknown>
  started_at: string
  finished_at?: string | null
  result: Record<string, unknown>
  audit_log_id?: number | null
  created_at: string
  updated_at: string
}

export interface AuditLogEntry {
  id: number
  incident_id?: string | null
  tenant_id: string
  action: ActionKind
  actor: string
  reason: string
  idempotency_key: string
  outcome: ActionOutcome
  failure_reason?: string | null
  diagnostic_run_id?: string | null
  pre_snapshot: Record<string, unknown>
  post_snapshot: Record<string, unknown>
  response_payload: Record<string, unknown>
  created_at: string
}

export interface IntegrityChecksum {
  algorithm: string
  value: string
}

export interface EvidenceExport {
  run: DiagnosticRun
  incident: RouteIncident
  events: RouteIncidentEvent[]
  timeline: RouteIncidentTimelinePoint[]
  integrity: IntegrityChecksum
  generated_at: string
  exporter: string
}

export const MUTATING_ACTIONS: ActionKind[] = [
  'direct_upstream_test',
  'through_gateway_test',
  'reprobe',
  'release_slot',
  'reset_slots',
  'reset_availability',
  'recover',
]

export const NON_DESTRUCTIVE_ACTIONS: ActionKind[] = [
  'direct_upstream_test',
  'through_gateway_test',
  'reprobe',
]

// Action labels for the dashboard. i18n keys map onto these.
export const ACTION_LABEL: Record<ActionKind, { title: string; hint: string }> = {
  direct_upstream_test: {
    title: '直连上游测试',
    hint: '绕过网关直接对供应商发起一次安全探测请求',
  },
  through_gateway_test: {
    title: '经网关测试',
    hint: '经过完整 IR 转换 + 凭据选择 + 重试路径的合成探测',
  },
  reprobe: {
    title: '重新探测',
    hint: '记录一次重探意图，下游凭据状态原语是真实信号来源',
  },
  release_slot: {
    title: '释放槽位',
    hint: '指定一个 slot 主动释放，参数 slot_id 必填',
  },
  reset_slots: {
    title: '重置所有槽位',
    hint: '清空该凭据下的全部 slot 占用',
  },
  reset_availability: {
    title: '重置可用性',
    hint: '把凭据可用性状态重置为 available',
  },
  recover: {
    title: '标记恢复',
    hint: '把事件状态变为 recovered 并加版本号；不等同绕过健康检查',
  },
  evidence_export: {
    title: '证据导出',
    hint: '从一次已完成的诊断运行生成脱敏证据包',
  },
}
