// sessionTurnsTree.ts — V3.3-OBS (2026-08-15) OBS-FE5
// 会话与统计 tab 下钻链路的 API 封装：
//   GET /api/admin/sessions/online        在线会话列表（admin/session_online.go）
//   GET /api/admin/sessions/{id}/turns    轮次-子请求树（admin/session_turns_tree.go, OBS-BE6）
//
// 字段名以后端响应结构体 json tag 为准：
//   - turns: turn_number / request_id / status / model(omitempty) / latency(ms, null=未知)
//     child_requests: request_id / request_type / status / latency(ms, null=未知)
//   - online: session_id / title? / last_request_status? / last_model? /
//     last_latency_ms?(null=未知) / last_active_at? / freshness?
//
// 硬约束（13 号门禁）：latency 为 null 表示未知，禁止前端用 0 冒充；
// 后端未提供的字段（用户/轮次数/健康度）不读取、不渲染。
//
// 与 _core.req 的差异：req 会把错误压平成 message，丢失状态码；本模块需要
// 区分 404（会话不存在）/ 403（跨租户）/ 401（未认证）/ 网络错误，因此自带
// 保留 status 的轻量封装（不走 req 的 401 重定向，由面板展示未认证态）。

import { BASE, headers } from './_core'

export type SessionChildRequestType =
  | 'title'
  | 'summary'
  | 'sensitive_word'
  | 'compression'
  | 'other'

export interface SessionChildRequest {
  request_id: string
  request_type: SessionChildRequestType | string
  status: string
  /** 毫秒；null = 未知（禁止零值冒充） */
  latency: number | null
}

export interface SessionTurnTreeItem {
  turn_number: number
  request_id: string
  status: string
  model?: string
  /** 毫秒；null = 未知（禁止零值冒充） */
  latency: number | null
  child_requests: SessionChildRequest[]
}

export interface SessionTurnsTreeResponse {
  session_id: string
  turns: SessionTurnTreeItem[]
  count: number
  has_more: boolean
  next_cursor: string
}

export interface OnlineSessionFreshness {
  data_source?: string
  freshness_ms?: number
  stale?: boolean
}

export interface OnlineSessionItem {
  session_id: string
  title?: string
  last_request_status?: string
  last_model?: string
  last_latency_ms?: number | null
  last_active_at?: string
  device_count?: number
  freshness?: OnlineSessionFreshness
}

export interface OnlineSessionsResponse {
  sessions: OnlineSessionItem[]
  count: number
  has_more: boolean
  next_cursor: string
}

export type SessionObsErrorKind =
  | 'unauthorized' // 401
  | 'forbidden' // 403 跨租户
  | 'not_found' // 404 会话不存在
  | 'server' // 5xx / 4xx 其他
  | 'network' // fetch 层失败（断网/超时/JSON 解析失败）

export class SessionObsApiError extends Error {
  readonly status: number // HTTP 状态码；network 时为 0
  readonly kind: SessionObsErrorKind

  constructor(kind: SessionObsErrorKind, status: number, message: string) {
    super(message)
    this.name = 'SessionObsApiError'
    this.kind = kind
    this.status = status
  }
}

function kindFromStatus(status: number): SessionObsErrorKind {
  if (status === 401) return 'unauthorized'
  if (status === 403) return 'forbidden'
  if (status === 404) return 'not_found'
  return 'server'
}

/** 带状态码的 GET（仅本模块使用；401 不重定向，交给面板展示）。 */
async function getJson<T>(path: string): Promise<T> {
  let r: Response
  try {
    r = await fetch(BASE + path, {
      method: 'GET',
      headers: headers('GET'),
      credentials: 'same-origin',
    })
  } catch {
    throw new SessionObsApiError('network', 0, '网络请求失败')
  }
  if (!r.ok) {
    let msg = r.statusText || `HTTP ${r.status}`
    try {
      const text = await r.text()
      if (text) {
        try {
          const j = JSON.parse(text)
          if (j && typeof j.error === 'string') msg = j.error
        } catch {
          msg = text
        }
      }
    } catch {
      /* 保留 statusText */
    }
    throw new SessionObsApiError(kindFromStatus(r.status), r.status, msg)
  }
  try {
    return (await r.json()) as T
  } catch {
    throw new SessionObsApiError('network', 0, '响应解析失败')
  }
}

/** GET /api/admin/sessions/online?limit=&cursor= 在线会话列表。 */
export async function fetchOnlineSessions(
  params: { limit?: number; cursor?: string } = {}
): Promise<OnlineSessionsResponse> {
  const q = new URLSearchParams()
  if (params.limit) q.set('limit', String(params.limit))
  if (params.cursor) q.set('cursor', params.cursor)
  const qs = q.toString()
  return getJson<OnlineSessionsResponse>(`/api/admin/sessions/online${qs ? `?${qs}` : ''}`)
}

/** GET /api/admin/sessions/{id}/turns?limit=&cursor= 轮次-子请求树（OBS-BE6）。 */
export async function fetchSessionTurnsTree(
  sessionId: string,
  params: { limit?: number; cursor?: string } = {}
): Promise<SessionTurnsTreeResponse> {
  const q = new URLSearchParams()
  if (params.limit) q.set('limit', String(params.limit))
  if (params.cursor) q.set('cursor', params.cursor)
  const qs = q.toString()
  const path =
    `/api/admin/sessions/${encodeURIComponent(sessionId)}/turns${qs ? `?${qs}` : ''}`
  return getJson<SessionTurnsTreeResponse>(path)
}
