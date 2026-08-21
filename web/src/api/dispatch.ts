import { req } from './_core'

export interface WaterfallAttempt {
  attempt_id: string
  attempt_no: number
  model?: string
  provider_id?: number
  credential_id: number
  vendor?: string
  started_at?: string
  first_byte_at?: string
  ended_at?: string
  outcome?: string
  error_kind?: string
}

export interface WaterfallRequest {
  request_id: string
  tenant_id?: string
  session_id?: string
  model?: string
  credential_id?: number
  result: string
  vendor?: string
  attempts?: WaterfallAttempt[]
  arrived_at?: string
  total_enqueued_at?: string
  total_dequeued_at?: string
  model_enqueued_at?: string
  model_dequeued_at?: string
  cred_enqueued_at?: string
  cred_dequeued_at?: string
  forward_start_at?: string
  response_start_at?: string
  response_end_at?: string
  waiting_in_total_ms: number
  waiting_in_model_ms: number
  waiting_in_node_ms: number
  routing_ms: number
  acquire_ms: number
  upstream_latency_ms: number
  streaming_duration_ms: number
  queue_wait_ms: number
  total_ms: number
}

export interface BottleneckDiagnosis {
  bottleneck: 'none' | 'routing' | 'model_capacity' | 'node_concurrency' | string
  message: string
  suggestion?: string
}

export interface WaterfallSnapshot {
  requests: WaterfallRequest[]
  time_range?: { start: string; end: string }
  bottleneck_diagnosis: BottleneckDiagnosis
  enabled: boolean
  wired: boolean
  /** memory | memory+db | db | none */
  source?: string
}

export interface QueueSnapshot {
  model?: string
  credential?: number
  mode?: string
  depth: number
}

export interface DispatchQueuesSnapshot {
  enabled: boolean
  wired: boolean
  models: QueueSnapshot[]
  credentials: QueueSnapshot[]
}

export interface WaterfallQuery {
  limit?: number
  model?: string
  credential_id?: number
}

function buildQuery(params?: Record<string, unknown>): string {
  if (!params) return ''
  const usp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    usp.set(k, String(v))
  }
  const qs = usp.toString()
  return qs ? `?${qs}` : ''
}

export function fetchDispatchWaterfall(query: WaterfallQuery = {}): Promise<WaterfallSnapshot> {
  return req<WaterfallSnapshot>('GET', '/api/admin/dispatch/waterfall' + buildQuery({
    limit: query.limit ?? 50,
    model: query.model,
    credential_id: query.credential_id,
  }))
}

export function fetchDispatchQueues(): Promise<DispatchQueuesSnapshot> {
  return req<DispatchQueuesSnapshot>('GET', '/api/admin/dispatch/queues')
}
