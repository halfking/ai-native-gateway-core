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
