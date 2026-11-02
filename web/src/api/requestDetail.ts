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
// ★ `hotzone_mirror`（2026-11-02）：本地热区请求侧镜像**兜底回源**（PG 会话族
//   正文缺失时）。后端字面量：domains/requestdetail/types.go 的 SourceHotzoneMirror。
// ★ 联合必须**写在同一行**：契约门 web/src/api/source-contract.test.ts 的
//   unionMembers 提取器按 `export type X = <单行>` 取成员（多行写法取不全）⇒
//   拆行会让门误判「桌面端漏值」。
export type DetailSource = 'memory' | 'file' | 'live_stream' | 'request_logs' | 'session_turns' | 'hotzone_mirror'
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
  /**
   * ★ 正文可用性两态（available | unavailable）。缺失 = 未计算。
   *   unavailable 的两种成因要分开看：**该请求本来没正文** vs
   *   **该租户没开录制同意**（后端 recording_consent=false）⇒
   *   前端据此区分「无消息」与「正文未落库」，别把后者读成前者。
   */
  body_status?: 'available' | 'unavailable'
  /** 正文载荷实际来自哪一层（后端 meta.body_source）。 */
  body_source?: DetailSource
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
  /**
   * ★ 该租户的**会话正文录制同意**状态（2026-11-02）。
   *   false = 正文压根没落库（不是「这条请求没正文」）⇒ 前端要显示
   *   中性的「录制未开启」态，而不是「无消息」。
   */
  recording_consent?: boolean
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
