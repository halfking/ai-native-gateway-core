// sessions_v2.ts — V2-P4 admin session detail APIs (2026-07-24)
// Cursor-based turns list, single turn detail, snapshot, instant summary,
// and attachment signed-URL helper. All endpoints require super_admin.
//
// Convention: every call goes through `req<T>()` from _core so we get
// unified 401-handling and JSON error parsing for free.

import { req } from './_core'

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

export async function listSessionTurns(
  sessionId: string,
  params: { cursor?: string; limit?: number } = {}
): Promise<TurnsResponse> {
  const q = new URLSearchParams()
  if (params.cursor) q.set('cursor', params.cursor)
  if (params.limit) q.set('limit', String(params.limit))
  const qs = q.toString()
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/turns${qs ? `?${qs}` : ''}`
  return req<TurnsResponse>('GET', path)
}

export async function getSessionTurn(sessionId: string, turnNo: number) {
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/turns/${turnNo}`
  return req<Record<string, unknown>>('GET', path)
}

export async function getSessionSnapshot(sessionId: string) {
  const path = `/api/admin/sessions/${encodeURIComponent(sessionId)}/snapshot`
  return req<Record<string, unknown>>('GET', path)
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
