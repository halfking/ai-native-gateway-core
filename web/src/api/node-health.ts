// node-health.ts — 节点恢复时间线 admin API
//
// GET /api/admin/node-health/{credential_id}/timeline?since=24h
// 真源：后端 node_probe_runs 投影（非 mock）。

import { req } from './_core'

export type NodeRecoveryEventType =
  | 'reconnected'
  | 'recovered'
  | 'failed'
  | 'degraded'
  | 'quarantined'
  | 'probing'

export interface NodeRecoveryEvent {
  credential_id: string
  event_type: NodeRecoveryEventType
  occurred_at: string
  duration_ms?: number
  reason_code?: string
  note?: string
}

export interface NodeRecoveryTimelineResponse {
  credential_id: string
  observation_status: 'complete' | 'observation_degraded'
  events: NodeRecoveryEvent[]
}

export async function fetchNodeRecoveryTimeline(
  credentialId: string,
  since = '24h',
): Promise<NodeRecoveryTimelineResponse> {
  const qs = since ? `?since=${encodeURIComponent(since)}` : ''
  return req<NodeRecoveryTimelineResponse>(
    'GET',
    `/api/admin/node-health/${encodeURIComponent(credentialId)}/timeline${qs}`,
  )
}
