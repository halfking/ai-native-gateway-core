// connection-registry.ts — 流式连接注册表 admin API（对接后端真实投影）
//
// 数据来源：进程内 domains/streaming.ConnectionRegistry（live + closed 审计环）。
// 仅元数据，不含 SSE 帧正文或凭据。

import { req } from './_core'

/** 与后端 streaming.ConnectionSnapshot JSON 对齐 */
export interface ConnectionSnapshot {
  request_id: string
  protocol?: string
  client_type?: string
  tenant_id?: string
  registered_at?: string
  last_frame_at?: string
  frames_written?: number
  bytes_written?: number
  close_reason?: string
  closed?: boolean
}

export interface ConnectionRegistryListResponse {
  live: ConnectionSnapshot[]
  live_count: number
  capacity: number
  closed: ConnectionSnapshot[]
}

export async function fetchConnectionRegistry(): Promise<ConnectionRegistryListResponse> {
  return req<ConnectionRegistryListResponse>('GET', '/api/admin/connection-registry')
}

export async function fetchConnectionByRequestId(requestId: string): Promise<ConnectionSnapshot> {
  return req<ConnectionSnapshot>(
    'GET',
    `/api/admin/connection-registry/${encodeURIComponent(requestId)}`,
  )
}

// ── 节点恢复时间线（仍由 NodeHealthTimelineView 使用，待 node-health API 落地） ──

export interface NodeRecoveryEvent {
  credential_id: string
  event_type: 'reconnected' | 'recovered' | 'failed' | 'degraded' | 'quarantined' | 'probing'
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

/** mock 节点恢复时间线 — node-health API 落地前保留 */
export async function fetchNodeRecoveryTimeline(credentialId: string): Promise<NodeRecoveryTimelineResponse> {
  const now = Date.now()
  const events: NodeRecoveryEvent[] = [
    {
      credential_id: credentialId,
      event_type: 'failed',
      occurred_at: new Date(now - 24 * 3_600_000).toISOString(),
      reason_code: 'upstream_timeout',
    },
    {
      credential_id: credentialId,
      event_type: 'recovered',
      occurred_at: new Date(now - 24 * 3_600_000 + 90_000).toISOString(),
      duration_ms: 90_000,
    },
  ]
  return {
    credential_id: credentialId,
    observation_status: 'complete',
    events,
  }
}
