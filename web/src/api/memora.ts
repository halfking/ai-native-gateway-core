import { req } from './_core'

// memora.ts — v6.0 audit T12 (2026-06-22)
// Memora session-memory plumbing: sink liveness, session browse, per-
// session context (facts + readable blocks), session messages, and the
// "no-topic" session endpoints (sessions that don't have a gw_task_id
// because the client never set one — the prefix-based variant).
//
// The endpoints live under /api/system/memora-* and /api/system/session-*.
// All are admin-only (or admin-impersonating); user-facing memora
// ingestion happens via the gateway's per-request hot path, not here.

export interface MemoraSinkStats {
  enqueued: number
  dropped: number
  processed: number
  errored: number
  queue_len: number
  queue_cap: number
  consecutive_errors: number
  last_error: string | null
  last_error_at: string | null
  paused?: boolean
}

export interface MemoraStatus {
  enabled: boolean
  base_url: string | null
  connected: boolean
  last_error: string | null
  last_error_at: string | null
  ping_latency_ms: number | null
  sink_paused?: boolean
  sink: MemoraSinkStats | null
}

export function getMemoraStatus(): Promise<MemoraStatus> {
  return req<MemoraStatus>('GET', '/api/system/memora-status')
}

export function pingMemora(): Promise<{ connected: boolean; latency_ms: number; error: string | null }> {
  return req('POST', '/api/system/memora-ping')
}

export function controlMemoraSink(action: 'pause' | 'resume'): Promise<{ paused: boolean; sink: MemoraSinkStats }> {
  return req('POST', '/api/system/memora-sink', { action })
}

export interface MemoraSession {
  task_id: string | null
  session_id: string | null
  title?: string | null
  no_topic: boolean
  no_topic_label: string | null
  hour_start?: string | null
  api_key_prefix: string | null
  api_key_owner_user: string | null
  application_code: string | null
  request_count: number
  ok_count: number
  fail_count: number
  first_activity: string
  last_activity: string
  latest_model: string | null
  memora_preview?: string | null
  memora_status?: 'ok' | 'empty' | 'error' | 'skipped' | null
}

export interface MemoraSessionsResponse {
  sessions: MemoraSession[]
  hours: number
  no_topic_window: number
  topic_count: number
  no_topic_count: number
}

export function getMemoraSessions(params: {
  q?: string
  hours?: number
  limit?: number
  owner_user?: string
  key_prefix?: string
  no_topic_window?: number
  include_no_topic?: boolean
  include_memora?: boolean
} = {}): Promise<MemoraSessionsResponse> {
  const qs = new URLSearchParams()
  if (params.q) qs.set('q', params.q)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.owner_user) qs.set('owner_user', params.owner_user)
  if (params.key_prefix) qs.set('key_prefix', params.key_prefix)
  if (params.no_topic_window != null) qs.set('no_topic_window', String(params.no_topic_window))
  if (params.include_no_topic === false) qs.set('include_no_topic', '0')
  if (params.include_memora) qs.set('include_memora', '1')
  const s = qs.toString()
  return req<MemoraSessionsResponse>('GET', `/api/system/memora-sessions${s ? '?' + s : ''}`)
}

export interface MemoraFact {
  id: string
  memory: string
  score: number
  tags: string[] | null
  kind?: 'text' | 'json'
  source?: 'task' | 'gw-session'
}

export interface ReadableBlock {
  id: string
  text: string
  kind: 'text' | 'json'
  source: 'task' | 'gw-session'
  tags?: string[] | null
  score?: number
}

export interface MemoraContextResponse {
  task_id: string
  user_id: string
  request_count: number
  latest_model?: string
  facts: MemoraFact[]
  readable_blocks?: ReadableBlock[]
  facts_visible?: number
  facts_written?: number
  hours?: number
  scoped_session_id?: string | null
  extracted_at?: string
  facts_search_error?: string
  title: string
}

export interface SessionScopeParams {
  hours?: number
  session_id?: string
}

function appendSessionScopeQS(qs: URLSearchParams, scope?: SessionScopeParams) {
  if (!scope) return
  if (scope.hours != null) qs.set('hours', String(scope.hours))
  if (scope.session_id) qs.set('session_id', scope.session_id)
}

export function getMemoraContext(taskId: string, scope?: SessionScopeParams): Promise<MemoraContextResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  const s = qs.toString()
  return req<MemoraContextResponse>(
    'GET',
    `/api/system/memora-context/${encodeURIComponent(taskId)}${s ? '?' + s : ''}`,
  )
}

export interface RequestMessage {
  ts: string
  request_id: string
  seq: number
  direction: 'user' | 'assistant'
  client_model: string | null
  outbound_model: string | null
  prompt_preview: string | null
  response_preview: string | null
  user_turn?: string | null
  assistant_text?: string | null
  tool_summary?: string | null
  prompt_tokens: number
  completion_tokens: number
  latency_ms: number
  cost_usd: number
  status: string | null
  error_kind?: string
}

export interface SessionMessagesResponse {
  task_id: string
  session_id: string | null
  messages: RequestMessage[]
  message_count?: number
  request_count?: number
  hours?: number
  hour_start?: string
  api_key_prefix?: string
  title?: string
  scoped_session_id?: string | null
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
}

export function getSessionMessages(
  taskId: string,
  scope?: SessionScopeParams,
  limit?: number,
): Promise<SessionMessagesResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  if (limit != null) qs.set('limit', String(limit))
  const s = qs.toString()
  return req<SessionMessagesResponse>(
    'GET',
    `/api/system/session-messages/${encodeURIComponent(taskId)}${s ? '?' + s : ''}`,
  )
}

export interface SessionExtractToMemoraResponse {
  task_id: string
  user_id: string
  project_id: string
  written: number
  skipped_noise: number
  skipped_duplicate: number
  memora_message_ids: string[]
  extracted_at: string
  samples: string[]
  error?: string
}

export interface SessionExtractionStatusResponse {
  task_id: string
  extracted: boolean
  extracted_at?: string
  written?: number
  skipped_noise?: number
  skipped_duplicate?: number
  status?: string
}

export function extractSessionToMemora(
  taskId: string,
  scope?: SessionScopeParams,
  dryRun = false,
): Promise<SessionExtractToMemoraResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  const s = qs.toString()
  return req<SessionExtractToMemoraResponse>(
    'POST',
    `/api/system/session-context/${encodeURIComponent(taskId)}/extract-to-memora${s ? '?' + s : ''}`,
    { dry_run: dryRun },
  )
}

export function getSessionExtractionStatus(taskId: string): Promise<SessionExtractionStatusResponse> {
  return req<SessionExtractionStatusResponse>(
    'GET',
    `/api/system/session-context/${encodeURIComponent(taskId)}/extraction-status`,
  )
}

export interface SessionTitleMeta {
  task_id: string
  scoped_session_id?: string
  log_count: number
  generated_at: string
  api_key_id: number
  model: string
}

export interface SessionTitleResponse {
  title: string
  meta: SessionTitleMeta
}

export function summarizeSessionTitle(
  taskId: string,
  scope?: SessionScopeParams,
): Promise<SessionTitleResponse> {
  const qs = new URLSearchParams()
  appendSessionScopeQS(qs, scope)
  const s = qs.toString()
  return req<SessionTitleResponse>(
    'POST',
    `/api/system/session-context/${encodeURIComponent(taskId)}/summarize-title${s ? '?' + s : ''}`,
    {},
  )
}

// ── No-topic session API (aggregated sessions without gw_task_id) ─────

export interface NoTopicSessionParams {
  prefix: string
  hours?: number
  hour_start?: string
  limit?: number
}

export function getNoTopicSessionMessages(params: NoTopicSessionParams): Promise<SessionMessagesResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  if (params.limit != null) qs.set('limit', String(params.limit))
  return req<SessionMessagesResponse>('GET', `/api/system/no-topic-session/messages?${qs.toString()}`)
}

export function summarizeNoTopicSessionTitle(params: NoTopicSessionParams): Promise<SessionTitleResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  return req<SessionTitleResponse>('POST', `/api/system/no-topic-session/summarize-title?${qs.toString()}`, {})
}

export function extractNoTopicSessionToMemora(params: NoTopicSessionParams): Promise<SessionExtractToMemoraResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  return req<SessionExtractToMemoraResponse>('POST', `/api/system/no-topic-session/extract-to-memora?${qs.toString()}`, {})
}

export function getNoTopicSessionExtractionStatus(params: NoTopicSessionParams): Promise<SessionExtractionStatusResponse> {
  const qs = new URLSearchParams()
  qs.set('prefix', params.prefix)
  if (params.hours != null) qs.set('hours', String(params.hours))
  if (params.hour_start) qs.set('hour_start', params.hour_start)
  return req<SessionExtractionStatusResponse>('GET', `/api/system/no-topic-session/extraction-status?${qs.toString()}`)
}

// ── Session title — manual override + bulk lookup (2026-08-06) ────────

/** Body for PUT /api/system/session-context/{taskId}/title. */
export interface SessionTitleUpdateRequest {
  title: string
  scoped_session_id?: string
}

/** Response for PUT/DELETE on .../title. */
export interface SessionTitleUpdateResponse {
  task_id: string
  scoped_session_id: string
  title: string
  model: string
  updated_at: string
}

/** Body for POST /api/system/session-context/titles/batch. */
export interface SessionTitlesBatchRequest {
  keys: Array<{ task_id: string; scoped_session_id?: string }>
}

/** Response for the batch lookup. Keys are `task_id + "\x00" + scoped_session_id`. */
export interface SessionTitlesBatchResponse {
  titles: Record<string, string>
}

/**
 * Manually override the auto-generated session title for a task.
 * Stamps model="manual" so future summarize-title calls can still refresh
 * the row without losing the human intent (the LLM call simply wins).
 */
export function updateSessionTitle(
  taskId: string,
  body: SessionTitleUpdateRequest,
): Promise<SessionTitleUpdateResponse> {
  return req<SessionTitleUpdateResponse>(
    'PUT',
    `/api/system/session-context/${encodeURIComponent(taskId)}/title`,
    body,
  )
}

/**
 * Clear the stored session title so the UI falls back to the short-id
 * display and a fresh summarize-title can run unblocked. Idempotent:
 * returns `deleted:false` if no row existed.
 */
export function deleteSessionTitle(
  taskId: string,
  scopedSessionID = '',
): Promise<{ task_id: string; scoped_session_id: string; deleted: boolean }> {
  const qs = scopedSessionID ? `?scoped_session_id=${encodeURIComponent(scopedSessionID)}` : ''
  return req(
    'DELETE',
    `/api/system/session-context/${encodeURIComponent(taskId)}/title${qs}`,
  )
}

/**
 * Bulk lookup of session titles keyed by (task_id, scoped_session_id).
 * Used by the request-logs list to enrich each row in a single
 * round-trip. Server-side caps at 500 keys per call.
 *
 * KEEP: 前端暂未直接调用（RequestLogsView 通过后端 SQL LEFT JOIN 实现），
 *       保留作未来 list 预加载 / 自定义视图批量预热入口。
 *       对应后端 handleSessionTitlesBatch (admin/session_title.go)。
 *       若 90 天内仍无引用，触发 rule 09 §5.2.4 评估保留或下线。
 */
export function batchGetSessionTitles(
  keys: Array<{ task_id: string; scoped_session_id?: string }>,
): Promise<SessionTitlesBatchResponse> {
  return req<SessionTitlesBatchResponse>(
    'POST',
    '/api/system/session-context/titles/batch',
    { keys },
  )
}
