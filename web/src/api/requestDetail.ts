// requestDetail.ts — unified request detail facade client.
// GET /api/admin/request-detail/{id} — memory/file → request_logs → session_turns
import { req } from './_core'

export type DetailSource = 'memory' | 'file' | 'request_logs' | 'session_turns'
export type DetailPersistence = 'in_flight' | 'persisted'

export interface UnifiedDetailMeta {
  request_id: string
  tenant_id?: string
  gw_session_id?: string | null
  gw_task_id?: string | null
  client_model?: string | null
  request_status?: string | null
  success?: boolean | null
  latency_ms?: number | null
  turn_number?: number | null
}

export interface UnifiedDetailBodies {
  request_body?: unknown
  response_body?: unknown
  outbound_body?: unknown
}

export interface UnifiedRequestDetail {
  source: DetailSource
  persistence: DetailPersistence
  meta: UnifiedDetailMeta
  bodies?: UnifiedDetailBodies | null
  warning?: string
}

export function getUnifiedRequestDetail(
  requestId: string,
  opts?: { omitBody?: boolean },
) {
  const qs = opts?.omitBody ? '?omit_body=1' : ''
  return req<UnifiedRequestDetail>(
    'GET',
    `/api/admin/request-detail/${encodeURIComponent(requestId)}${qs}`,
  )
}
