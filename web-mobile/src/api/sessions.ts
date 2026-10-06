import { req, type RequestOptions } from './client'
import { costMayBeNoData } from './modelTaskIndex'

// sessions.ts — 会话运维面。
//   GET /api/admin/sessions/list                 最近会话审计清单
//   GET /api/admin/sessions/online               当前活跃会话（游标分页）
//   GET /api/admin/sessions/{id}/timeline        单会话轮次树
//
// 鉴权：三条都挂 `wrapAdmin` / `admin(...)`（cmd/gateway/main.go:7268-7275、
// admin/handler.go:1175-1183）⇒ **admin 档，tenant_admin 可用**。
// ★ 但 `summary` 那个端点是 **POST-only**（admin/session_summary_v2.go:89），
//   同族端点方法不一致，别照抄 GET。
//
// ⚠️★★★ 八个坑，逐条实读源码：
//
// (1) ★★★ **同族三端的「没登录」表现是两种状态码。**
//     `list`：`GetAuthContext(r) == nil` ⇒ **404**（handler 内钉死，
//           admin/session_list_v2.go:127-130，注释明说是为了让 legacy default
//           掩盖不了缺失身份）。
//     `online` / `timeline`：⇒ **401** `authentication required`。
//     ⇒ 客户端**不能**用「404 ⇒ 没权限」推断，反之亦然；
//       只能在拿到响应后按实际 status 分别映射文案。
//
// (2) ★★★ `list` 的四个布尔/计数字段是**恒定值**，不是测量。**
//     逐条追到构造处（admin/session_list_v2.go:182-186 →
//     sessionforensics.SessionAudit 字面量）：
//       · `has_session_id`  —— 循环里 `if sid == nil || *sid == "" { continue }`
//         已把空/NULL 行全部跳过 ⇒ 进到字面量的行**必然**是 true。
//       · `missing_session_ids`（`missing_sid` 计数）—— 同一个 `continue`
//         让「gw_session_id 为 NULL 的行」自成一组又被整组跳过
//         ⇒ 这个计数**恒为 0**。
//       · `has_title` / `has_summary` —— SQL 的 SELECT 列表里**根本没有**
//         session_titles / session_summaries 这两张表 ⇒ 字面量不设这两个字段
//         ⇒ Go 零值 ⇒ **恒为 false**。
//     ⇒ 显示成「无标题」「没有会话 ID」就是在编造一个我们并不知道的结论。
//       见 `SESSION_LIST_TITLE_ALWAYS_FALSE` 等常量。
//
// (3) ★★ **`models_used` 可能是 `null` 而不是 `[]`。**
//     SQL 是 `array_agg(DISTINCT client_model) FILTER (WHERE client_model IS NOT NULL)`；
//     当一组内**所有**行的 client_model 都为 NULL 时，聚合结果是 NULL
//     ⇒ 扫进 `[]string` 得到 nil ⇒ 序列化成 `null`。
//     （对比 `sessions` 本身是 `make([]map[string]any, 0, len(audits))` ⇒ 永远 `[]`。）
//
// (4) ★★★ **`total_cost_usd` 的 0 是生产者 `COALESCE` 兜底值。**
//     `COALESCE(SUM(cost_usd), 0)` ⇒ **0 = 没有成本数据**，不是「免费」。
//     与 `model_task_index.avg_cost_per_1k_usd`（`SUM(total_tokens)>0 ? … : 0`）
//     是**同一个家族**，此处复用 `costMayBeNoData`（同族收口，不重写一份）。
//
// (5) ★★ **`limit` 回显在响应里 ⇒ 截断是精确信号。**
//     `limit` 默认 50；`strconv.Atoi` 失败 / ≤0 / >500 ⇒ **静默回落 50**
//     （admin/session_list_v2.go:137-141）。
//     响应含 `"limit": limit` ⇒ `sessions.length >= limit` 就是「后端填满了」
//     ⇒ 无需说「可能」，它**确实**被截断了。
//
// (6) ★★★ **`online` 的 superAdmin 会看到**全部租户**。**
//     SQL 条件是 `if !IsSuperAdminOrLegacy(r) { … AND rl.tenant_id = $1 }`
//     （admin/session_online.go）⇒ superAdmin / legacy 角色**不加租户过滤**。
//     ⇒ 页面必须说明当前作用域，否则同一屏数据在两种角色下含义完全不同。
//
// (7) ★★ `online` 的 `limit` 是**静默 clamp**，与 `list` 的静默回落不同。**
//     `NormalizePaginationParams`（admin/session_online_pagination.go:122-130）：
//       ≤0 → 20；**>100 → 100**（不发错、不提示）。
//     ⇒ 发 `limit=500` 静默变成 100 ⇒ 客户端只发 1..100。
//     分页用 `LIMIT limit+1` 多取一条判定 `has_more`，
//     所以**没有**截断标记问题：`truncated` 由 `has_more` 精确给出。
//
// (8) ★★★ `timeline` 的 `{id}` 有**三种身份**，响应会告诉你它最终按哪种解析。**
//     仓库里有 5 类 ID 的互查表；这个端点接受其中两种入口：
//       · `{id}` 按**文本** `gw_session_id` 解释（`session_id_source=path_gw_session_id`）
//       · `?session_pk=<sessions.id 数值>` 显式按数值代理键解析
//         （`session_pk_resolved`）；非正整数 ⇒ **400**
//         `session_pk must be a positive numeric sessions.id`
//       · 兜底：`{id}` 是**纯数字**且按文本查不到任何行 ⇒ 再按 `sessions.id` 解析一次
//         （`numeric_fallback_resolved`）
//     ★ `numeric_fallback_resolved` 意味着「你传的 id 我是**猜**的」
//       ⇒ 页面必须把它标出来，否则用户会以为在看自己指定的那个会话。
//     ★ 这个端点**自带** `truncated` 字段（= `has_more`）
//       —— 与 `availability-timeline`（写死 500 无标记）、
//         `cache-state`（ScanKeys 4096 无标记）不同，**有**标记就别再自己猜。

// ── 会话清单 ───────────────────────────────────────────────────────────────

/** 后端默认 limit。 */
export const SESSION_LIST_LIMIT_DEFAULT = 50
/** 后端接受的最大 limit（`n <= 500`，超出静默回落 50）。 */
export const SESSION_LIST_LIMIT_MAX = 500

export interface SessionAudit {
  session_id: string
  has_compression: boolean
  compression_hits: number
  total_turns: number
  /** ★ **恒为 true**：空/NULL 的行在构造前已被 `continue` 跳过。见 (2)。 */
  has_session_id: boolean
  /** ★ **恒为 0**：同上。见 (2)。 */
  missing_session_ids: number
  /** ★ **恒为 false**：SQL 没查标题表。见 (2)。 */
  has_title: boolean
  /** ★ **恒为 false**：SQL 没查摘要表。见 (2)。 */
  has_summary: boolean
  /** ★ 可能是 `null`（组内 client_model 全为 NULL）。见 (3)。 */
  models_used: string[] | null
  total_prompt_tokens: number
  total_resp_tokens: number
  /** ★ **0 = 没有成本数据**（COALESCE 兜底），不是免费。见 (4)。 */
  total_cost_usd: number
  earliest_at?: string
  latest_at?: string
  audit_at: string
}

export interface SessionListItem {
  /** 后端固定回显 `"gw_session_id"`。 */
  id_kind: string
  /** ★ 是 `request_logs.gw_session_id`（**文本**客户端标识），
   *  **不是** `sessions.id`（数值代理键）。见 (8)。 */
  primary_key: string
  audit: SessionAudit
}

export interface SessionListResponse {
  tenant: string
  /** 后端回显的生效 limit ⇒ 截断判定用它。见 (5)。 */
  limit: number
  sessions: SessionListItem[]
  id_kind: string
}

export interface SessionListParams {
  limit?: number
}

export function fetchSessionList(
  params: SessionListParams = {},
  options?: RequestOptions,
): Promise<SessionListResponse> {
  const qs = new URLSearchParams()
  // ★ 只发 1..500：越界不报错，而是**静默变成 50**（见 (5)）。
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    if (n >= 1 && n <= SESSION_LIST_LIMIT_MAX) qs.set('limit', String(n))
  }
  const s = qs.toString()
  return req<SessionListResponse>('GET', `/api/admin/sessions/list${s ? '?' + s : ''}`, undefined, options)
}

/** ★ 后端把标题/摘要两个字段恒置 false（SQL 没查那两张表）。见 (2)。 */
export const SESSION_LIST_TITLE_ALWAYS_FALSE = true
export const SESSION_LIST_SUMMARY_ALWAYS_FALSE = true
/** ★ `missing_session_ids` 恒为 0（空/NULL 行在构造前已被跳过）。见 (2)。 */
export const SESSION_LIST_MISSING_ID_ALWAYS_ZERO = true
/** ★ `has_session_id` 恒为 true（同上）。 */
export const SESSION_LIST_HAS_ID_ALWAYS_TRUE = true

/**
 * 清单是否被截断。
 *
 * ★ 与 `availability-timeline` / `cache-state` 不同：这里 `limit` 被**回显**，
 *   所以 `sessions.length >= limit` 不是「可能」而是「确实被后端填满」。
 */
export function sessionListTruncated(resp: SessionListResponse | null | undefined): boolean {
  if (!resp) return false
  const n = resp.sessions?.length ?? 0
  const lim = resp.limit
  return typeof lim === 'number' && lim > 0 && n >= lim
}

/** `models_used` 的 null ⇒ 空数组（渲染时永远拿得到数组）。见 (3)。 */
export function modelsUsedOf(audit: SessionAudit | null | undefined): string[] {
  return audit?.models_used ?? []
}

// ── 在线会话 ───────────────────────────────────────────────────────────────

/** 后端默认 limit（≤0 时回落到这里）。 */
export const ONLINE_LIMIT_DEFAULT = 20
/** 后端**静默 clamp** 到的上限（`> 100 → 100`）。见 (7)。 */
export const ONLINE_LIMIT_MAX = 100

export interface FreshnessInfo {
  /** hot / merged / v2_archive */
  data_source: string
  freshness_ms: number
  stale: boolean
}

export interface OnlineSession {
  session_id: string
  title?: string
  last_request_status?: string
  /** `COALESCE(slr.last_model,'')` ⇒ 空串意味着源列是 NULL。 */
  last_model?: string
  last_provider_id?: number
  last_latency_ms?: number
  last_active_at?: string
  device_count?: number
  freshness?: FreshnessInfo
}

export interface OnlineSessionsResponse {
  sessions: OnlineSession[]
  count: number
  next_cursor?: string | null
  has_more: boolean
}

export interface OnlineSessionsParams {
  cursor?: string | null
  limit?: number
}

export function fetchOnlineSessions(
  params: OnlineSessionsParams = {},
  options?: RequestOptions,
): Promise<OnlineSessionsResponse> {
  const qs = new URLSearchParams()
  // ★ 游标**不透明**：后端自己 base64 编码/解码（`parseOnlineSessionCursor`），
  //   客户端**不得**自己拼或解析。非法游标 ⇒ 400
  //   `session.pagination_invalid_cursor`。
  if (params.cursor) qs.set('cursor', params.cursor)
  // ★ 只发 1..100：超出会被**静默 clamp 到 100**（见 (7)）。
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    if (n >= 1 && n <= ONLINE_LIMIT_MAX) qs.set('limit', String(n))
  }
  const s = qs.toString()
  return req<OnlineSessionsResponse>('GET', `/api/admin/sessions/online${s ? '?' + s : ''}`, undefined, options)
}

/** 是否还有下一页（后端多取一条判定，不存在「静默截断」）。 */
export function onlineHasMore(resp: OnlineSessionsResponse | null | undefined): boolean {
  return resp?.has_more === true
}

export { costMayBeNoData }

// ── 单会话轮次树 ───────────────────────────────────────────────────────────

/** 后端回告 `{id}` 最终按哪种身份解析。见 (8)。 */
export const SESSION_ID_SOURCES = ['path_gw_session_id', 'session_pk_resolved', 'numeric_fallback_resolved'] as const
export type SessionIdSource = (typeof SESSION_ID_SOURCES)[number]

export interface SessionTurn {
  request_id: string
  request_type: string
  status: string
  model?: string
  latency_ms?: number
  started_at?: string
  children?: SessionTurn[]
  is_final_success?: boolean
  outcome?: string
  outcome_reason?: string
  error_kind?: string
  failure_stage?: string
}

export interface SessionTimelineResponse {
  /** 生效的**文本** session id。 */
  session_id: string
  /** 已知时才是数值代理键；走文本路径时为 `null`。 */
  session_pk: number | null
  session_id_source: string
  turns: SessionTurn[]
  count: number
  has_more: boolean
  /** ★ 后端**自带**的截断标记（= `has_more`）。见 (8)。 */
  truncated: boolean
}

export interface SessionTimelineParams {
  /** 显式按数值 `sessions.id` 解析。非正整数后端返 400 ⇒ 客户端先拦。 */
  sessionPk?: number
}

export function fetchSessionTimeline(
  sessionId: string,
  params: SessionTimelineParams = {},
  options?: RequestOptions,
): Promise<SessionTimelineResponse> {
  const sid = String(sessionId ?? '').trim()
  // ★ 空 id 后端 400 `session id required` ⇒ 本地拦下。
  if (sid === '') throw new Error('session id required')
  const qs = new URLSearchParams()
  // ★ 只发正整数：`session_pk=0` / 负数 / 小数都会被后端 400。
  if (typeof params.sessionPk === 'number' && Number.isFinite(params.sessionPk)) {
    const pk = Math.trunc(params.sessionPk)
    if (pk >= 1) qs.set('session_pk', String(pk))
  }
  const s = qs.toString()
  const path = `/api/admin/sessions/${encodeURIComponent(sid)}/timeline`
  return req<SessionTimelineResponse>('GET', `${path}${s ? '?' + s : ''}`, undefined, options)
}

/** ★ 是否走了「纯数字 → 按 sessions.id 再解析一次」的兜底（说明 id 是**猜**的）。 */
export function sessionIdSourceIsGuess(source: string | null | undefined): boolean {
  return source === 'numeric_fallback_resolved'
}

/** 是否是已知的三种来源之一（未知来源要如实显示，不能当正常路径）。 */
export function sessionIdSourceKnown(source: string | null | undefined): boolean {
  return (SESSION_ID_SOURCES as readonly string[]).includes(String(source ?? ''))
}

/** 递归统计轮次总数（顶层 + children）。 */
export function countTurns(turns: SessionTurn[] | null | undefined): number {
  let n = 0
  for (const t of turns ?? []) {
    n += 1 + countTurns(t.children)
  }
  return n
}

/** 拍平轮次树（渲染用），保留层级深度。 */
export function flattenTurns(
  turns: SessionTurn[] | null | undefined,
  depth = 0,
): Array<{ turn: SessionTurn; depth: number }> {
  const out: Array<{ turn: SessionTurn; depth: number }> = []
  for (const t of turns ?? []) {
    out.push({ turn: t, depth })
    out.push(...flattenTurns(t.children, depth + 1))
  }
  return out
}