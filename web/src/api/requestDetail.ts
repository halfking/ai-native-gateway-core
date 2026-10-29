// requestDetail.ts — unified request detail facade client.
// GET /api/admin/request-detail/{id} — memory/file → request_logs → session_turns
import { req } from './_core'

// DetailSource 是统一详情接口的 `source` 字段取值集合。
//
// ★ R182 轮修正：★ 旧联合漏了 **`live_stream`** ⇒ ★
//   后端 `domains/requestdetail/types.go` 有 **5** 个取值（memory / file /
//   live_stream / request_logs / session_turns），而前端只列了 4 个
//   ⇒ ⇒ `live_stream` 落进渲染侧的兜底分支 ⇒ ⇒ **裸串 `live_stream` 上屏**。
// ★ 移动端 `web-mobile/src/api/requestDetail.ts` 的 REQUEST_DETAIL_SOURCES
//   早已是 5 个 ⇒ ★ 桌面端这次是**向移动端对齐**。
// ★ 由 web/src/api/__tests__ 之外的
//   `web/src/api/credential-monitor.source-contract.test.ts` 里的
//   「request_detail 域」判据钉住（后端字面量集合 ↔ 前端联合逐字相等）。
export type DetailSource = 'memory' | 'file' | 'live_stream' | 'request_logs' | 'session_turns'
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
