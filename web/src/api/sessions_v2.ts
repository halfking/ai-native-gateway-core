// sessions_v2.ts — V2-P4 admin session detail APIs (2026-07-24)
// Cursor-based turns list, single turn detail, snapshot, instant summary,
// and attachment signed-URL helper. All endpoints require super_admin.
//
// Convention: every call goes through `req<T>()` from _core so we get
// unified 401-handling and JSON error parsing for free.

import { req, type RequestOptions } from './_core'

export interface TurnDigest {
  user_input: string
  assistant_output: string
  metrics: TurnDigestMetrics
  events?: TurnDigestEvent[]
  tool_usage?: TurnDigestToolUsage
}

export interface TurnDigestMetrics {
  tokens_used: number
  cost: number
  latency_ms: number
  cache_hit_rate?: number
  compression_rate?: number
}

export interface TurnDigestEvent {
  type: 'error' | 'warning' | 'info' | string
  category: string
  message: string
}

export interface TurnDigestToolUsage {
  tool_call_count: number
  tools_used: string[]
}

export interface TurnAttachment {
  att_id: string
  name: string
  size: number
  mime?: string
  object?: string
}

export interface TurnDetail {
  request?: unknown
  response?: unknown
  compression?: Record<string, unknown>
  meta?: Record<string, unknown>
  governance?: Record<string, unknown>
  attachments?: TurnAttachment[]
  title?: string
  summary?: string
  digest?: TurnDigest | null
  model?: string
  cost_usd?: number
}

export interface TurnListItem {
  turn_no: number
  ts: string
  title?: string
  summary?: string
  request_tokens: number
  response_tokens: number
  cost_usd: number
  model: string
  provider: string
  status_code: number
  submit_mode: string
  injection_verdict: string
  output_verdict: string
  attachment_count: number
}

export interface TurnsResponse {
  session_id: string
  turns: TurnListItem[]
  has_more: boolean
  next_cursor: string
}

export interface SessionTurnBodyItem {
  turn_no: number
  request_id: string
  /** 本轮新增的请求消息数组（session_bodies.request_delta） */
  request_delta: unknown
  /** 本轮回复消息数组（session_bodies.response_delta） */
  response_delta: unknown
  /** 实际发往 LLM 的 outbound 正文（session_bodies.outbound_body） */
  outbound_body: unknown
}

export interface SessionTurnBodiesResponse {
  session_id: string
  turns: SessionTurnBodyItem[]
  has_more: boolean
}

export async function listSessionTurns(
  sessionId: string,
  params: { cursor?: string; limit?: number } = {},
  options?: RequestOptions
): Promise<TurnsResponse> {
  const q = new URLSearchParams()
  if (params.cursor) q.set('cursor', params.cursor)
  if (params.limit) q.set('limit', String(params.limit))
  const qs = q.toString()
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/turns${qs ? `?${qs}` : ''}`
  return req<TurnsResponse>('GET', path, undefined, options)
}

/** GET /api/admin/sessions/{id}/turns/bodies — 批量取会话每轮正文（V2 增量存储）。 */
export async function fetchSessionTurnsBodies(
  sessionId: string,
  params: { limit?: number } = {},
  options?: RequestOptions
): Promise<SessionTurnBodiesResponse> {
  const q = new URLSearchParams()
  if (params.limit) q.set('limit', String(params.limit))
  const qs = q.toString()
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/turns/bodies${qs ? `?${qs}` : ''}`
  return req<SessionTurnBodiesResponse>('GET', path, undefined, options)
}

export async function getSessionTurn(
  sessionId: string,
  turnNo: number,
  options?: RequestOptions
): Promise<TurnDetail> {
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/turns/${turnNo}`
  return req<TurnDetail>('GET', path, undefined, options)
}

export async function getSessionSnapshot(sessionId: string, options?: RequestOptions) {
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/snapshot`
  return req<Record<string, unknown>>('GET', path, undefined, options)
}

export async function triggerInstantSummary(sessionId: string) {
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/instant-summary`
  return req<Record<string, unknown>>('POST', path, {})
}

export async function getAttachmentSignedUrl(
  sessionId: string,
  turnNo: number,
  attId: string
): Promise<{ url: string; expires_at: number }> {
  const path =
    `/api/admin/sessions/${encodeURIComponent(sessionId)}/turns/${turnNo}` +
    `/attachments/${encodeURIComponent(attId)}/url`
  return req<{ url: string; expires_at: number }>('GET', path)
}
