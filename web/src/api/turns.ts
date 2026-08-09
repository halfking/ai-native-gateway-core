// turns.ts — 跨会话轮次列表 API（2026-08-09）
// 调用 GET /api/admin/turns，返回所有会话的轮次记录，按时间倒序分页。
// 与 sessions_v2.ts 不同，该端点不限定单一会话，而是跨会话查询。

import { req } from './_core'

export interface TurnInList {
  session_id: string
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

export interface TurnsListResponse {
  items: TurnInList[]
  has_more: boolean
  next_cursor: string
}

export async function listTurns(params: {
  cursor?: string
  limit?: number
  model?: string
  provider?: string
  status_code?: number
  ts_from?: string
  ts_to?: string
  tenant?: string
}): Promise<TurnsListResponse> {
  const qs = new URLSearchParams()
  if (params.cursor) qs.set('cursor', params.cursor)
  if (params.limit) qs.set('limit', String(params.limit))
  if (params.model) qs.set('model', params.model)
  if (params.provider) qs.set('provider', params.provider)
  if (params.status_code) qs.set('status_code', String(params.status_code))
  if (params.ts_from) qs.set('ts_from', params.ts_from)
  if (params.ts_to) qs.set('ts_to', params.ts_to)
  if (params.tenant) qs.set('tenant', params.tenant)

  const path = `/api/admin/turns${qs.toString() ? '?' + qs.toString() : ''}`
  return req<TurnsListResponse>('GET', path)
}
