// turns.ts — 跨会话轮次列表 API（2026-08-09）
// 调用 GET /api/admin/turns，返回所有会话的轮次记录，按时间倒序分页。
// 与 sessions_v2.ts 不同，该端点不限定单一会话，而是跨会话查询。

import { req, type RequestOptions } from './_core'

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

// =============================================================================
// 会话分组轮次列表（2026-08-10）
// 调用 GET /api/admin/turns/sessions，按会话分组返回：
//   外层 = 会话摘要（topic/title/summary/status/totals/压缩汇总），
//   内层 = 该会话的轮次（request/response 摘要 + compression/cache 明细）。
// 供 TurnsListView.vue 分层展示使用。
// =============================================================================

export interface TurnGroupItem {
  turn_no: number
  ts: string
  title?: string
  summary?: string
  request_tokens: number
  response_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
  model: string
  provider: string
  status_code: number
  success: boolean
  error_kind?: string
  submit_mode: string
  compression_applied: boolean
  compression_strategy?: string
  compression_tokens_saved?: number
  injection_verdict: string
  output_verdict: string
  attachment_count: number
  attempt_no: number
  latency_ms?: number
}

export interface TurnsCompressionAgg {
  applied_count: number
  tokens_saved: number
  strategies: string[]
}

export interface TurnsSessionGroup {
  session_id: string
  tenant_id: string
  title?: string
  topic?: string
  intent?: string
  summary?: string
  summary_model?: string
  summary_generated_at?: string
  status: string
  task_type?: string
  client_type?: string
  created_at: string
  updated_at: string
  closed_at?: string
  total_turns: number
  total_tokens: number
  total_cost_usd: number
  last_turn_no?: number
  last_model?: string
  last_provider?: string
  models_used: string[]
  failover_count: number
  error_count: number
  duration_ms: number
  compression: TurnsCompressionAgg
  turns: TurnGroupItem[]
  // 会话级附加信息（来自 session_dim / session_summaries，可能为空）
  project_id?: string
  task_id?: string
  owner_user?: string
  client_id?: string
  application_code?: string
  end_user_id?: string
  user_tags: string[]
  start_time?: string
  // 会话间父子/附属关系：handoff=透明轮换派生，auto_title/auto_summary=回环分支会话
  parent_session_id?: string
  parent_relation?: string
}

export interface TurnsSessionsResponse {
  items: TurnsSessionGroup[]
  has_more: boolean
  next_cursor: string
}

export async function listTurnsSessions(params: {
  cursor?: string
  limit?: number
  model?: string
  provider?: string
  status_code?: number
  ts_from?: string
  ts_to?: string
  tenant?: string
  project_id?: string
  task_id?: string
  search?: string
  tags?: string
  client?: string
  owner_user?: string
}, options?: RequestOptions): Promise<TurnsSessionsResponse> {
  const qs = new URLSearchParams()
  if (params.cursor) qs.set('cursor', params.cursor)
  if (params.limit) qs.set('limit', String(params.limit))
  if (params.model) qs.set('model', params.model)
  if (params.provider) qs.set('provider', params.provider)
  if (params.status_code) qs.set('status_code', String(params.status_code))
  if (params.ts_from) qs.set('ts_from', params.ts_from)
  if (params.ts_to) qs.set('ts_to', params.ts_to)
  if (params.tenant) qs.set('tenant', params.tenant)
  if (params.project_id) qs.set('project_id', params.project_id)
  if (params.task_id) qs.set('task_id', params.task_id)
  if (params.search) qs.set('search', params.search)
  if (params.tags) qs.set('tags', params.tags)
  if (params.client) qs.set('client', params.client)
  if (params.owner_user) qs.set('owner_user', params.owner_user)

  const path = `/api/admin/turns/sessions${qs.toString() ? '?' + qs.toString() : ''}`
  return req<TurnsSessionsResponse>('GET', path, undefined, options)
}

// =============================================================================
// 热门筛选条件（2026-08-10）
// 调用 GET /api/admin/turns/sessions/filter-options，返回近 30 天内各筛选维度
// 实际出现的热门取值，供 TurnsListView.vue 的 filterable 下拉填充。
// =============================================================================

export interface TurnsFilterOptions {
  projects: string[]
  tasks: string[]
  owners: string[]
  clients: string[]
  tags: string[]
  models: string[]
  providers: string[]
  status_codes: string[]
}

export async function listTurnsFilterOptions(): Promise<TurnsFilterOptions> {
  return req<TurnsFilterOptions>('GET', '/api/admin/turns/sessions/filter-options')
}
