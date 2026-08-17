import { req } from './_core'

export type RequestJourneyQueueView = 'total' | 'models' | 'nodes'
export type RequestJourneyQueueScope = 'all'
export type RequestJourneyObservationStatus = 'complete' | 'observation_degraded'
export type RequestJourneyOutcome = 'success' | 'failure' | 'canceled'
export type RequestJourneyStage =
  | 'received'
  | 'routing'
  | 'model_queue'
  | 'credential_queue'
  | 'node_selection'
  | 'upstream'
  | 'streaming'
  | 'retrying'
  | 'terminal'

export type RequestJourneyEventType =
  | 'request_received'
  | 'route_resolved'
  | 'model_enqueued'
  | 'credential_selected'
  | 'node_enqueued'
  | 'node_selected'
  | 'attempt_started'
  | 'first_byte'
  | 'attempt_succeeded'
  | 'attempt_failed'
  | 'retry_scheduled'
  | 'node_switched'
  | 'model_switched'
  | 'request_succeeded'
  | 'request_failed'
  | 'request_canceled'
  | 'observation_degraded'

export type RequestJourneyNodeHealthStatus =
  | 'unknown'
  | 'healthy'
  | 'suspect'
  | 'degraded'
  | 'cooling'
  | 'probing'
  | 'recovering'
  | 'quarantined'
  | 'disabled'
  | 'unhealthy'

export interface RequestJourneyAttemptRef {
  attempt_id: string
  attempt_no: number
  model?: string
  provider_id?: number
  provider?: string
  credential_id?: number
}

export interface RequestJourneyEvent {
  tenant_id: string
  gateway_instance_id: string
  request_id: string
  seq: number
  event_type: RequestJourneyEventType
  stage: RequestJourneyStage
  requested_model?: string
  resolved_model?: string
  model?: string
  provider_id?: number
  provider?: string
  credential_id?: number
  from_model?: string
  to_model?: string
  from_credential_id?: number
  to_credential_id?: number
  attempt?: RequestJourneyAttemptRef
  outcome?: RequestJourneyOutcome
  error_kind?: string
  http_status?: number
  retry_reason?: string
  switch_reason?: string
  node_health_status?: RequestJourneyNodeHealthStatus
  observation_status: RequestJourneyObservationStatus
  occurred_at: string
}

export interface RequestJourney {
  tenant_id: string
  gateway_instance_id: string
  request_id: string
  observation_status: RequestJourneyObservationStatus
  started_at: string
  updated_at: string
  events: RequestJourneyEvent[]
}

export interface RequestJourneySnapshot {
  tenant_id?: string
  gateway_instance_id?: string
  request_id: string
  requested_model?: string
  resolved_model?: string
  current_stage: RequestJourneyStage
  last_seq?: number
  last_event_type?: RequestJourneyEventType
  attempt?: RequestJourneyAttemptRef
  outcome?: RequestJourneyOutcome
  error_kind?: string
  http_status?: number
  retry_reason?: string
  switch_reason?: string
  node_health_status?: RequestJourneyNodeHealthStatus
  observation_status?: RequestJourneyObservationStatus
  started_at?: string
  updated_at: string
  completed_at?: string
}

export interface RequestIngressSnapshot {
  request_id: string
  gateway_instance_id: string
  protocol: 'chat' | 'messages' | 'responses' | 'gemini'
  path_class: 'chat_completions' | 'messages' | 'responses' | 'gemini_models'
  arrived_at: string
  updated_at: string
  status: 'arrived' | 'succeeded' | 'failed' | 'canceled'
  error_kind?: string
  http_status?: number
}

export interface TotalRequestFIFOSnapshot {
  capacity: number
  requests: RequestJourneySnapshot[]
}

export interface IngressRequestFIFOSnapshot {
  capacity: number
  requests: RequestIngressSnapshot[]
}

export interface ModelRequestFIFOSnapshot {
  model: string
  capacity: number
  requests: RequestJourneySnapshot[]
}

export interface NodeRequestFIFOSnapshot {
  model: string
  provider_id?: number
  credential_id: number
  capacity: number
  requests: RequestJourneySnapshot[]
}

export interface RequestJourneyQueuesResponse {
  view: RequestJourneyQueueView
  scope?: RequestJourneyQueueScope
  observation_status?: RequestJourneyObservationStatus
  observation_scope?: 'shared_redis' | 'instance_local'
  total_snapshot?: TotalRequestFIFOSnapshot | IngressRequestFIFOSnapshot
  model_snapshots?: ModelRequestFIFOSnapshot[]
  node_snapshots?: NodeRequestFIFOSnapshot[]
}

export interface RequestJourneyDetailResponse {
  observation_status: RequestJourneyObservationStatus
  journey?: RequestJourney
}

export type RequestJourneyQueuesPayload =
  | RequestJourneyQueuesResponse
  | TotalRequestFIFOSnapshot
  | ModelRequestFIFOSnapshot[]
  | NodeRequestFIFOSnapshot[]

export function getRequestJourneyQueues(
  view: RequestJourneyQueueView,
  scope?: RequestJourneyQueueScope,
): Promise<RequestJourneyQueuesPayload> {
  const query = new URLSearchParams({ view })
  if (scope) query.set('scope', scope)
  return req<RequestJourneyQueuesPayload>(
    'GET',
    `/api/admin/request-journeys/queues?${query.toString()}`,
  )
}

export async function getRequestJourney(requestId: string): Promise<RequestJourney> {
  const payload = await req<RequestJourney | RequestJourneyDetailResponse>(
    'GET',
    `/api/admin/request-journeys/${encodeURIComponent(requestId)}`,
  )
  if ('journey' in payload) {
    if (payload.journey) return payload.journey
    throw new Error('request journey not found')
  }
  return payload as RequestJourney
}
