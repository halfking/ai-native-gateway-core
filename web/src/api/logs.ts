import { req } from './_core'
import type { ProbeResult } from './routing'

// logs.ts — v6.0 audit T12 (2026-06-22)
// /api/logs/* endpoints: paged request log list, log detail, top-models
// summary, LLM-driven session summary, and the Memora writer bridge.
// The session summary endpoints were added on 2026-06-20 (see
// admin/logs_summary.go in the Go backend) and only fire when the
// caller has a gw_session_id filter on the request-logs view.

export interface RequestLogRow {
  ts: string
  request_id: string
  api_key_id: number | null
  end_user_id: string | null
  client_model: string | null
  outbound_model: string | null
  credential_id: number | null
  credential_label: string | null
  provider_id: number | null
  provider_name: string | null
  provider_code: string | null
  client_profile: string | null
  request_mode: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  total_tokens: number | null
  cost_usd: number | string | null
  cost_display: number | string | null
  cost_currency: string | null
  latency_ms: number | null
  success: boolean
  request_status: 'in_progress' | 'success' | 'failure'
  error_kind: string | null
  search_text: string | null
  identity_hash: string | null
  virtual_client_id: string | null
  virtual_ip: string | null
  virtual_mac: string | null
  affinity_hit: boolean | null
  stream_first_chunk_ms: number | null
  stream_chunk_count: number | null
  stream_interrupted: boolean | null
  stream_done_sent: boolean | null
  usage_source: 'llm' | 'estimated' | null
  gw_session_id: string | null
  gw_task_id: string | null
  api_key_prefix: string | null
  api_key_owner_user: string | null
  application_code: string | null
  canonical_name: string | null
  provider_model: string | null
  trace_seq: number | null
  credits_charged: number | null

  // Round 47 compression v7: parent-child chain tracking.
  // Populated when a request was rewritten by the compressor (either
  // pre-request mode=1 auto_threshold or post-error mode=2 on_4xx).
  // parent_request_id points to the pre-compression request_id;
  // compression_* describe the strategy + payload.
  parent_request_id: string | null
  compression_reason: 'mode_1_auto_threshold' | 'mode_2_on_4xx' | null
  compression_strategy:
    | 'mechanical_trim'
    | 'memora_l1_inject'
    | 'llm_summary'
    | 'noop'
    // v3 (2026-06-19) session-level strategies.
    | 'delta_append'
    | 'sliding_window_token'
    | 'sliding_window_count'
    | 'sliding_window_idle'
    | null
  compression_meta: any | null
  // v3 (2026-06-19) session-level outbound body. NULL when v3 did not
  // rewrite the body (outbound == client request_body).
  outbound_body: any | null
  outbound_msg_count: number | null
  outbound_token_est: number | null
  outbound_msg_hashes: any | null

  // 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
  // `upstream_finish_reason` is the SOLE home for the upstream finish_reason
  // (stop, tool_calls, length, end_turn, function_call, max_tokens, …). It is
  // populated for BOTH success and failure rows and should NOT be displayed
  // as a "失败详情" / failure label. `failure_detail_code` and
  // `failure_stage` are now reserved for actual failure / interruption codes.
  upstream_finish_reason: string | null
  failure_detail_code: string | null
  failure_stage: string | null

  // 2026-07-01 (migration 325): 附件数量。列表接口返回，0/undefined 表示无附件。
  // 前端据此在"状态"列后渲染附件角标 (📎 N)。详情接口额外返回完整 attachments 数组。
  attachment_count?: number
}

// AttachmentInfo 描述单个附件的元数据 (migration 325, request_logs.attachments 元素)。
// 附件实体文件由网关落盘，前端通过 GET /api/attachments/{path} 下载/预览。
export interface AttachmentInfo {
  type: 'image' | 'file'            // 附件类型：图片 / 其他文件
  content_type: string              // MIME 类型，如 image/png
  size: number                      // 解码后字节数
  path: string                      // 文件系统相对路径 (相对存储根目录)，如 2026/07/req_xxx/abc.png
  hash: string                      // 内容 SHA256（用于去重/完整性校验）
  original_url?: string             // 原始引用 (data URI 截断至200字符；HTTP URL 原样)
  message_index?: number            // 所在 message 的索引
  block_index?: number              // 在该 message content 数组中的块索引
  created_at?: string               // 保存时间
}

export interface RequestLogDetail extends RequestLogRow {
  request_body: any | null
  response_body: any | null
  // 2026-07-01: 完整附件元数据数组。仅详情接口返回；为空/undefined 表示无附件。
  attachments?: AttachmentInfo[] | null
}

export interface RequestLogsResponse {
  items: RequestLogRow[]
  count: number
}

export interface SessionSummaryMeta {
  session_id: string
  log_count: number
  data_from: string
  data_to: string
  generated_at: string
  api_key_id: number
  model: string
}

export interface SessionSummaryResponse {
  summary: string
  key_points: string[]
  meta: SessionSummaryMeta
}

export interface MemoraWriteResult {
  written: number
  user_id: string
  project_id: string
  status: string
  error?: string
}

export interface SessionSummaryToMemoraResponse {
  summary: string
  key_points: string[]
  meta: SessionSummaryMeta
  memora: MemoraWriteResult
}

export interface TopRequestModel {
  canonical_id: number | null
  canonical_name: string
  display_name: string
  request_count: number
}

export function getRequestLogs(params: {
  api_key_id?: number
  provider_id?: number
  credential_id?: number
  identity_hash?: string
  from?: string
  to?: string
  q?: string
  model?: string
  error_kind?: string
  request_status?: 'in_progress' | 'success' | 'failure'
  success?: boolean
  canonical_id?: number
  usage_source?: 'llm' | 'estimated'
  gw_session_id?: string
  gw_task_id?: string
  chrono?: boolean
  page?: number
  page_size?: number
} = {}) {
  const qs = new URLSearchParams()
  if (params.api_key_id != null) qs.set('api_key_id', String(params.api_key_id))
  if (params.provider_id != null) qs.set('provider_id', String(params.provider_id))
  if (params.credential_id != null) qs.set('credential_id', String(params.credential_id))
  if (params.identity_hash) qs.set('identity_hash', params.identity_hash)
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.q) qs.set('q', params.q)
  if (params.model) qs.set('model', params.model)
  if (params.error_kind) qs.set('error_kind', params.error_kind)
  if (params.request_status) qs.set('request_status', params.request_status)
  if (params.success != null) qs.set('success', String(params.success))
  if (params.canonical_id != null) qs.set('canonical_id', String(params.canonical_id))
  if (params.usage_source) qs.set('usage_source', params.usage_source)
  if (params.gw_session_id) qs.set('gw_session_id', params.gw_session_id)
  if (params.gw_task_id) qs.set('gw_task_id', params.gw_task_id)
  if (params.chrono) qs.set('chrono', '1')
  if (params.page != null) qs.set('page', String(params.page))
  if (params.page_size != null) qs.set('page_size', String(params.page_size))
  const s = qs.toString()
  return req<RequestLogsResponse>('GET', `/api/logs${s ? '?' + s : ''}`)
}

export function getRequestLogDetail(requestId: string) {
  return req<RequestLogDetail>('GET', `/api/logs/${encodeURIComponent(requestId)}`)
}

// 2026-07-01 (migration 325): 附件相关辅助。
//
// 附件实体文件由网关落盘 (LLM_GATEWAY_ATTACHMENT_DIR)，通过
// GET /api/attachments/{path...} 访问。path 是相对存储根目录的路径
// (如 2026/07/req_xxx/abc.png)，即 AttachmentInfo.path。
// 该端点经 admin 鉴权，浏览器 <img>/fetch 需带同源 cookie (credentials:same-origin)。

// attachmentURL 返回附件的下载/预览 URL。注意 path 已是相对路径，无需再 encode
// 其内的 /；只对最末段做编码以兼容特殊字符。这里整体 encode 后将 %2F 还原为 /，
// 保证目录层级不被破坏。
export function attachmentURL(path: string): string {
  if (!path) return ''
  // 整体编码后还原分隔符，保留目录结构
  return '/api/attachments/' + encodeURIComponent(path).replace(/%2F/gi, '/')
}

// getRequestAttachments 列出某请求的附件元数据。
// GET /api/logs/{request_id}/attachments → { request_id, attachments[], count }
export function getRequestAttachments(requestId: string) {
  return req<{ request_id: string; attachments: AttachmentInfo[]; count: number }>(
    'GET',
    `/api/logs/${encodeURIComponent(requestId)}/attachments`,
  )
}

export function getRequestLogTopModels(params: { from?: string; to?: string; limit?: number } = {}) {
  const qs = new URLSearchParams()
  if (params.from) qs.set('from', params.from)
  if (params.to) qs.set('to', params.to)
  if (params.limit != null) qs.set('limit', String(params.limit))
  const s = qs.toString()
  return req<{ items: TopRequestModel[] }>('GET', `/api/logs/top-models${s ? '?' + s : ''}`)
}

export function getSessionSummary(gwSessionId: string) {
  return req<SessionSummaryResponse>('POST', '/api/logs/session-summary', {
    gw_session_id: gwSessionId,
  })
}

export function sessionSummaryToMemora(gwSessionId: string) {
  return req<SessionSummaryToMemoraResponse>('POST', '/api/logs/session-summary-to-memora', {
    gw_session_id: gwSessionId,
  })
}

export function probeModel(model: string, messages?: Array<{role: string; content: string}>, maxTokens = 20, clientProfile = 'roocode') {
  return req<ProbeResult>('POST', '/api/routing/probe', {
    model,
    messages: messages ?? [{ role: 'user', content: 'Hello, please reply with one word: OK' }],
    max_tokens: maxTokens,
    client_profile: clientProfile,
    request_mode: 'chat',
  })
}