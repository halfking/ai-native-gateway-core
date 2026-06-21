import { req } from './_core'

// session.ts — v6.0 audit T12 (2026-06-22)
// v4 session APIs added 2026-06-21:
//
//  - getSessionCompare: shows original vs compressed messages for a
//    session, the strategy used, and the cache info (L1/L2 hits).
//  - executeHandoff: "compress + summarize + start a new session" —
//    used when a session gets too long and the user wants to keep
//    going without resetting context.
//  - getSessionList: browse sessions with filter (tenant, q, hours).

export interface MessageView {
  index: number
  role: string
  content: string
  tool_calls?: string
  token_count: number
}

export interface CacheInfo {
  l1_hit: boolean
  l2_hit: boolean
  l3_fallback: boolean
  last_refresh: string
}

export interface SessionStats {
  original_tokens: number
  compressed_tokens: number
  saved_tokens: number
  saved_percent: number
  compression_strategy: string
  compression_timestamp: string
}

export interface SessionCompareData {
  session_id: string
  tenant_id: string
  original_msgs: MessageView[]
  compressed_msgs: MessageView[]
  response_msgs: MessageView[]
  cache_info: CacheInfo
  stats: SessionStats
  is_compressed: boolean
  context_usage: number
  context_window: number
  model_used: string
  msg_count: number
}

export interface HandoffRequest {
  session_id: string
  tenant_id?: string
  create_new: boolean
}

export interface HandoffResponse {
  status: string
  session_id: string
  handoff_summary: string
  new_session_id?: string
  new_session_hint?: string
  completed_tasks: number
}

/** Get session compare data. */
export async function getSessionCompare(sessionId: string, tenantId?: string): Promise<SessionCompareData> {
  let path = `/api/admin/session-compare?session_id=${encodeURIComponent(sessionId)}`
  if (tenantId) path += `&tenant_id=${encodeURIComponent(tenantId)}`
  return req<SessionCompareData>('GET', path)
}

/** Execute session handoff. */
export async function executeHandoff(reqData: HandoffRequest): Promise<HandoffResponse> {
  return req<HandoffResponse>('POST', '/api/admin/session-handoff', reqData)
}

export interface SessionSummary {
  session_id: string
  tenant_id: string
  msg_count: number
  request_count: number
  token_total: number
  model_used: string
  time_start: string
  time_end: string
  duration: string
  is_compressed: boolean
  compression_strategy?: string
  first_user_msg?: string
  last_response?: string
  error_count: number
  success_rate: number
}

export interface SessionListResponse {
  sessions: SessionSummary[]
  total: number
  page: number
  size: number
  pages: number
}

/** Get session list. */
export async function getSessionList(params: {
  tenant_id?: string; page?: number; size?: number; hours?: number; q?: string
}): Promise<SessionListResponse> {
  const q = new URLSearchParams()
  if (params.tenant_id) q.set('tenant_id', params.tenant_id)
  if (params.page) q.set('page', String(params.page))
  if (params.size) q.set('size', String(params.size))
  if (params.hours) q.set('hours', String(params.hours))
  if (params.q) q.set('q', params.q)
  return req<SessionListResponse>('GET', '/api/admin/sessions?' + q.toString())
}