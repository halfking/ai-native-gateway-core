// 客户端与任务列表读取同一自然日范围的请求投影。
// 成本缺失时返回 null，列表长度与总数分别表示当前页和筛选总数。

import { req, type RequestOptions } from './client'

// ── 常量（照抄代码，非推断） ───────────────────────────────────────────────

/** ★ `queryInt(r, "limit", 50)` 的默认值。 */
export const ANALYTICS_LIMIT_DEFAULT = 50
/**
 * ★ 越界是**回落 50**（`if limit < 1 || limit > 200 { limit = 50 }`），
 * **不是 clamp 到 200**。见坑 5。
 */
export const ANALYTICS_LIMIT_MAX = 200

/**
 * ★ 三个列表的 `order_by` **各自不同**，且 `switch` 无 default 分支。
 * 未列出的值会**静默落回该端点的默认排序**，所以这里就是**全部**可发值。
 */
export const ANALYTICS_CLIENT_ORDER_BYS = ['cost', 'sessions', 'health'] as const
export const ANALYTICS_TASK_ORDER_BYS = ['cost', 'sessions', 'health'] as const
export type AnalyticsOrderBy = (typeof ANALYTICS_CLIENT_ORDER_BYS)[number]

/** ★ Go 零值 `time.Time` 的 JSON 形态——「这一页一行都没扫到」的判据。见坑 2。 */
export const ANALYTICS_ZERO_TIME = '0001-01-01T00:00:00Z'

/** ★ `refreshed_at` 是 Go 零值 ⇒ 这一页是空的，且拿不到刷新时间。见坑 2。 */
export function analyticsRefreshedAtIsZero(refreshedAt: string | null | undefined): boolean {
  if (typeof refreshedAt !== 'string') return true
  const v = refreshedAt.trim()
  if (v === '') return true
  return v === ANALYTICS_ZERO_TIME || v.startsWith('0001-01-01')
}

// ── 形状 ──────────────────────────────────────────────────────────────────

/**
 * ★★ 只有 5 个键，**没有 `total`、没有 `*_percent`**。见坑 9。
 * 注意这与 `admin/dashboardapi` 里那个**同名**类型形状不同。
 */
export interface AnalyticsHealthDistribution {
  a: number
  b: number
  c: number
  d: number
  f: number
}

export interface AnalyticsClientSummary {
  client_id: string
  session_count: number
  active_sessions_24h: number
  total_requests: number
  total_cost_usd: number | null
  avg_cost_per_session: number | null
  avg_health_score?: number | null
  health_distribution: AnalyticsHealthDistribution
  total_success: number
  total_errors: number
  avg_latency_ms?: number | null
  first_seen_at: string
  last_seen_at: string
  models_used: string[]
}

export interface AnalyticsTaskSummary extends AnalyticsClientSummary {
  task_id: string
  /** ★ tasks 比 clients **多这一个数组**。 */
  clients_used: string[]
}

export interface AnalyticsClientListResponse {
  clients: AnalyticsClientSummary[]
  total: number
  limit: number
  offset: number
  /** ★ 空列表时是 Go 零值。见坑 2。 */
  refreshed_at: string | null
  pending_since?: string | null
  start_date?: string
  end_date?: string
}

export interface AnalyticsTaskListResponse {
  tasks: AnalyticsTaskSummary[]
  total: number
  limit: number
  offset: number
  refreshed_at: string | null
  pending_since?: string | null
  start_date?: string
  end_date?: string
}

export interface AnalyticsListParams {
  limit?: number
  offset?: number
  orderBy?: AnalyticsOrderBy
  tenantId?: string
  startDate?: string
  endDate?: string
}

// ── 参数拼装（与 prompt-injection 同款：越界在客户端就回落，不发非法值） ──────

function buildQuery(params: AnalyticsListParams, allowedOrderBys: readonly string[]): string {
  const qs = new URLSearchParams()

  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    // ★ 与后端一致：越界**回落默认 50**，而不是 clamp。宁可回落也不发会被后端改写的值。
    const eff = n < 1 || n > ANALYTICS_LIMIT_MAX ? ANALYTICS_LIMIT_DEFAULT : n
    qs.set('limit', String(eff))
  }
  if (typeof params.offset === 'number' && Number.isFinite(params.offset)) {
    const n = Math.trunc(params.offset)
    if (n > 0) qs.set('offset', String(n))
  }
  // ★ 只发 switch 里**真的认**的值；发别的会被静默落回默认排序且不报错。
  if (params.orderBy && allowedOrderBys.includes(params.orderBy)) {
    qs.set('order_by', params.orderBy)
  }
  if (params.tenantId && params.tenantId.trim()) {
    qs.set('tenant_id', params.tenantId.trim())
  }
  if (params.startDate) qs.set('start_date', params.startDate)
  if (params.endDate) qs.set('end_date', params.endDate)
  const s = qs.toString()
  return s ? `?${s}` : ''
}

// ── clients ──────────────────────────────────────────────────────────────

export function fetchAnalyticsClients(
  params: AnalyticsListParams = {},
  options?: RequestOptions,
): Promise<AnalyticsClientListResponse> {
  const q = buildQuery(params, ANALYTICS_CLIENT_ORDER_BYS)
  return req<unknown>('GET', `/api/admin/session-analytics/v2/clients${q}`, undefined, options).then(
    unwrapAnalyticsClients,
  )
}

/**
 * ★ 后端 `writeJSON(w, 200, &ClientAnalyticsListResponse{…})`（session_analytics_clients.go:203-208）
 * ⇒ 响应键是 **`clients`**，另有 `total/limit/offset/refreshed_at`。
 * 形状不符**抛错**，绝不当成空清单返回。
 */
export function unwrapAnalyticsClients(resp: unknown): AnalyticsClientListResponse {
  if (
    resp &&
    typeof resp === 'object' &&
    Array.isArray((resp as AnalyticsClientListResponse).clients) &&
    typeof (resp as AnalyticsClientListResponse).total === 'number'
  ) {
    return resp as AnalyticsClientListResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `session-analytics/clients 响应形状不符：期望 {clients:[…], total, limit, offset, refreshed_at}，实得 ${actual}`,
  )
}

// ── tasks ────────────────────────────────────────────────────────────────

export function fetchAnalyticsTasks(
  params: AnalyticsListParams = {},
  options?: RequestOptions,
): Promise<AnalyticsTaskListResponse> {
  const q = buildQuery(params, ANALYTICS_TASK_ORDER_BYS)
  return req<unknown>('GET', `/api/admin/session-analytics/v2/tasks${q}`, undefined, options).then(
    unwrapAnalyticsTasks,
  )
}

/**
 * ★ 后端 `writeJSON(w, 200, &TaskAnalyticsListResponse{…})`（session_analytics_tasks.go:191-197）
 * ⇒ 响应键是 **`tasks`**。
 * ★★ 与 clients 的键**不同名**（`clients` vs `tasks`），且默认值也不同（cost vs sessions）。
 */
export function unwrapAnalyticsTasks(resp: unknown): AnalyticsTaskListResponse {
  if (
    resp &&
    typeof resp === 'object' &&
    Array.isArray((resp as AnalyticsTaskListResponse).tasks) &&
    typeof (resp as AnalyticsTaskListResponse).total === 'number'
  ) {
    return resp as AnalyticsTaskListResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `session-analytics/tasks 响应形状不符：期望 {tasks:[…], total, limit, offset, refreshed_at}，实得 ${actual}`,
  )
}

// ── 页面侧共用的小工具 ───────────────────────────────────────────────────

/**
 * ★★ 「本页显示数 < total」不一定是分页 bug：坏行被 `continue` 丢掉了。见坑 3。
 * 返回 true 时页面要显式说明，别让用户以为数据对不上是前端的问题。
 */
export function analyticsRowsWereDropped(shown: number, total: number): boolean {
  return total > shown
}

/**
 * 成功率。分母用 `total_requests`（`SUM(request_count)`，**NOT NULL** ⇒ 必有值），
 * 而 `total_success + total_errors` 与它是**两个独立计数器**，不保证相等
 * ⇒ 所以这里**不用** `success/(success+errors)`，而是按总请求数算。
 * 分母为 0（新建、无请求）时返回 `null`，页面显示「—」而不是 0%。
 */
export function analyticsSuccessRate(row: { total_requests: number; total_success: number }): number | null {
  if (!Number.isFinite(row.total_requests) || row.total_requests <= 0) return null
  return (row.total_success / row.total_requests) * 100
}
