import { req, type RequestOptions } from './client'

// turnsSessions.ts — 会话 / 轮次维度。
//   GET /api/admin/turns/sessions          会话列表（游标分页）
//   GET /api/admin/sessions/{id}/turns      单会话的轮次树
//
// 鉴权：两条都走 `admin(...)` = AdminMiddleware（admin/handler.go:1187 / :1275），
// 只做认证不判角色 ⇒ tenant_admin 可用。
//
// 它和排障线的位置：会话是「一次对话」这一层，比单条请求粗、比模型/凭据细。
// 排障时「用户说结果不对」的入口通常在这一层——一轮里 3 次尝试、
// 2 次压缩、1 次注入拦截，只有 sessions/turns 看得到。
//
// ⚠️★ 三个坑：
//
// (1) 分页是**游标**（`cursor` + `next_cursor` + `has_more`），不是 page/offset。
//     游标是**会话签名**的：换会话传错游标 ⇒ 400 "cursor mismatch"。
//     所以游标只能原样回传给同一个端点的下一页，不能缓存复用。
//
// (2) `limit` 越界**静默回落默认值 20**（turns_sessions.go:244-249），
//     不是 400。所以发 999 用户不会看到错误，只会拿到 20 条且毫无提示。
//     ⇒ 前端自己夹到 1..50。
//
// (3) ★★ 同一族数据在两个端点里**延迟字段名不一样**：
//       sessions 列表的 TurnGroupItem  是 `latency_ms,omitempty`（缺省即未知）
//       turns 树  的 SessionTurnTreeItem 是 `latency`（**无 omitempty，可为 null**）
//     见 session_turns_tree.go:49 `LatencyMs *int \`json:"latency"\`` ——
//     Go 字段名是 LatencyMs，JSON 键却是 latency。照抄任一边到另一边都取不到值，
//     而取不到值的表现是「延迟永远是 undefined」而不是报错。
//
// ⚠️ 另有 `/api/admin/turns`（v2 扁平列表，handler.go:1273）**不要用**：
//   该端点 SQL 不扫 request_id / child_requests 两列，恒返回
//   `"request_id": ""` 和 `"child_requests": null`。用它会得到一屏空 ID。

/** 服务端硬边界（越界静默回落 20，所以前端必须自己夹）。 */
export const TURNS_SESSIONS_MIN_LIMIT = 1
export const TURNS_SESSIONS_MAX_LIMIT = 50
export const TURNS_SESSIONS_DEFAULT_LIMIT = 20

/** 轮次树 limit 上限：由 NormalizePaginationParams 施加（session_online_pagination.go:126-128）。 */
export const SESSION_TURNS_MAX_LIMIT = 100

export interface TurnGroupItem {
  turn_no: number
  ts: string
  /** ⚠️ omitempty —— 无关联请求时字段**不存在**（不是 null） */
  request_id?: string
  title?: string
  summary?: string
  request_tokens: number
  response_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number | string
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
  /** ★ 本端点是 `latency_ms`（omitempty）；turns 树那边叫 `latency`。见文件头 (3)。 */
  latency_ms?: number
  [k: string]: unknown
}

export interface TurnsCompression {
  applied_count: number
  tokens_saved: number
  strategies: string[] | null
  [k: string]: unknown
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
  total_cost_usd: number | string
  last_turn_no?: number
  last_model?: string
  last_provider?: string
  models_used: string[] | null
  failover_count: number
  error_count: number
  duration_ms: number
  compression: TurnsCompression
  turns: TurnGroupItem[] | null
  project_id?: string
  task_id?: string
  owner_user?: string
  client_id?: string
  application_code?: string
  end_user_id?: string
  user_tags: string[] | null
  start_time?: string
  api_key_id?: number
  api_key_label?: string
  parent_session_id?: string
  parent_relation?: string
  session_analysis?: unknown
  [k: string]: unknown
}

export interface TurnsSessionsResponse {
  items: TurnsSessionGroup[]
  has_more: boolean
  next_cursor: string
}

export interface TurnsSessionsParams {
  cursor?: string
  limit?: number
  model?: string
  provider?: string
  status_code?: number
  project_id?: string
  task_id?: string
  search?: string
  /** 逗号分隔，后端任一命中即保留（turns_sessions.go:13-29）。 */
  tags?: string
  client?: string
  owner_user?: string
  api_key_id?: number
  status?: string
  /** RFC3339，作用于会话 updated_at */
  ts_from?: string
  ts_to?: string
  /** 仅 super_admin 生效；移动端不传。 */
  tenant?: string
}

function clampLimit(n: number, min: number, max: number): number {
  if (!Number.isFinite(n)) return min
  return Math.max(min, Math.min(Math.trunc(n), max))
}

export function fetchTurnsSessions(
  params?: TurnsSessionsParams,
  options?: RequestOptions,
): Promise<TurnsSessionsResponse> {
  const qs = new URLSearchParams()
  // ★ 游标只在该端点内部逐页传；换筛选条件必须丢掉（否则 400 cursor mismatch）
  if (params?.cursor) qs.set('cursor', params.cursor)
  if (params?.limit != null) {
    qs.set('limit', String(clampLimit(params.limit, TURNS_SESSIONS_MIN_LIMIT, TURNS_SESSIONS_MAX_LIMIT)))
  }
  if (params?.model) qs.set('model', params.model)
  if (params?.provider) qs.set('provider', params.provider)
  if (params?.status_code != null && Number.isFinite(params.status_code)) {
    qs.set('status_code', String(Math.trunc(params.status_code)))
  }
  if (params?.project_id) qs.set('project_id', params.project_id)
  if (params?.task_id) qs.set('task_id', params.task_id)
  if (params?.search) qs.set('search', params.search)
  if (params?.tags) qs.set('tags', params.tags)
  if (params?.client) qs.set('client', params.client)
  if (params?.owner_user) qs.set('owner_user', params.owner_user)
  if (params?.api_key_id != null) qs.set('api_key_id', String(params.api_key_id))
  if (params?.status) qs.set('status', params.status)
  if (params?.ts_from) qs.set('ts_from', params.ts_from)
  if (params?.ts_to) qs.set('ts_to', params.ts_to)
  if (params?.tenant) qs.set('tenant', params.tenant)
  const s = qs.toString()
  return req<TurnsSessionsResponse>('GET', `/api/admin/turns/sessions${s ? '?' + s : ''}`, undefined, options)
}

/** 轮次树条目（session_turns_tree.go:44-57）。 */
export interface SessionTurnChild {
  request_id: string
  /** title | summary | sensitive_word | compression | other */
  request_type: string
  status: string
  /** ⚠️ 可为 null —— 未知。禁止用 0 冒充。 */
  latency: number | null
  id_kind?: string
  primary_key?: string
  [k: string]: unknown
}

export interface SessionTurnTreeItem {
  turn_number: number
  request_id: string
  status: string
  model?: string
  /** ★ 键名是 `latency`（**不是** latency_ms），且无 omitempty ⇒ 可能显式为 null */
  latency: number | null
  child_requests: SessionTurnChild[] | null
  body_status: 'available' | 'unavailable' | (string & {})
  v2_shadow?: unknown
  [k: string]: unknown
}

export interface SessionTurnsResponse {
  session_id: string
  turns: SessionTurnTreeItem[] | null
  count: number
  has_more: boolean
  next_cursor: string
  /** v2 路径会附 source: 'v2' | 'tree_fallback' */
  source?: string
  [k: string]: unknown
}

export function fetchSessionTurns(
  sessionId: string,
  params?: { limit?: number; cursor?: string },
  options?: RequestOptions,
): Promise<SessionTurnsResponse> {
  const id = encodeURIComponent(sessionId ?? '')
  const qs = new URLSearchParams()
  if (params?.limit != null) {
    qs.set('limit', String(clampLimit(params.limit, 1, SESSION_TURNS_MAX_LIMIT)))
  }
  if (params?.cursor) qs.set('cursor', params.cursor)
  const s = qs.toString()
  return req<SessionTurnsResponse>('GET', `/api/admin/sessions/${id}/turns${s ? '?' + s : ''}`, undefined, options)
}

/**
 * 延迟归一。**两个端点的键名不同**，这里统一收口。
 *
 * ⚠️ 返回 null 表示「未知」，不是 0。把未知渲染成 0ms 会让
 * 「这一轮慢」被误读成「这一轮很快」。
 */
export function latencyOf(
  item: (Pick<TurnGroupItem, 'latency_ms'> & Record<string, unknown>) | (Pick<SessionTurnTreeItem, 'latency'> & Record<string, unknown>) | null | undefined,
): number | null {
  if (!item) return null
  const a = item.latency_ms
  if (typeof a === 'number' && Number.isFinite(a)) return a
  // turns 树那条：latency 可为 null，也可能是数字
  const b = (item as { latency?: unknown }).latency
  if (typeof b === 'number' && Number.isFinite(b)) return b
  return null
}

/** 轮次是否算「有问题」：失败、限流、注入拦截都算。 */
export function turnIsProblem(turn: TurnGroupItem): boolean {
  return turn.success === false || !!turn.error_kind
}

/** 成本归一。与 requestLogs 的 costNumber 同理：空串必须先挡掉。 */
export function costNumber(v: number | string | null | undefined): number | null {
  if (v == null) return null
  if (typeof v === 'string' && v.trim() === '') return null
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : null
}
