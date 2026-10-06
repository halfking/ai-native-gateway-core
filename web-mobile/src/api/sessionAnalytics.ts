// sessionAnalytics.ts — 会话分析（session-analytics）族的只读面。
//   GET /api/admin/session-analytics/clients   客户端维度列表
//   GET /api/admin/session-analytics/tasks     任务维度列表
//
// 鉴权：`admin/handler.go:1091-1094` 用 `admin(...)` 注册 ⇒ **admin 档**。
//   ★ 但**三个列表都显式挡普通用户**（见坑 1），比注册档位更严。
//
// ⚠️⚠️⚠️ 这一族最要紧的一条：**数据源是物化视图，而没有任何东西在刷新它。**
//
// (1) ★★★★★★ `session_client_stats` / `session_task_stats` /
//     `session_client_task_matrix` / `session_owner_stats` 是**物化视图**
//     （357_session_analytics_aggregation_views.sql:13 / 358_session_ownership.sql）。
//     刷新函数 `refresh_session_analytics_views()` 自己的注释写着
//     「建议每小时或每日执行」，**但调用点只有两处，都在 migration 末尾的「初始刷新」**
//     （357:143 / 358:396）。本仓唯一的周期性刷新器
//     `bg.MaterializedViewRefresher`（`RefreshInterval = 10 * time.Minute`）
//     硬编码管理的只有 `routing_analytics_7d` 与 `routing_audit_summary_7d`；
//     也没有针对它们的 pg_cron。
//     ⇒ **响应里的 `refreshed_at` 是唯一的「数据有多旧」的线索，页面必须显示。**
//     ⚠️ 未验证：本文件写这些注释时**没有真库**，所以「线上现在是否冻结」**未实测**；
//       能确定的只是「代码与迁移里没有周期刷新路径」。
//
// (2) ★★★★★ `refreshed_at` 在**空列表**时是 Go 零值。
//     `var refreshedAt time.Time` 声明在**循环外**，被每行 `rows.Scan(…, &refreshedAt)`
//     覆盖；循环不执行就保持零值 ⇒ 序列化成 `0001-01-01T00:00:00Z`。
//     它**不是**查询时刻、也**不是**视图创建时刻，只是**这一页最后扫到的那一行**的刷新戳。
//
// (3) ★★★★★ 扫描失败是 `continue`（**静默丢行**），与 `output_compliance_handler.go`
//     的「一行为空 ⇒ 整个端点 500」**方向完全相反**：
//         err := rows.Scan(…)
//         if err != nil { warnRowSkip(…); continue }   // ← 跳过这一行，列表照常 200
//     ⇒ `total` 来自独立的 `COUNT(*)`，而列表可能因坏行而**少于** `total`。
//     ⇒ 客户端必须允许「本页显示数 < total」并说明原因，**不能**当成自己的分页 bug。
//
// (4) ★★★★★ **唯一**会让整行消失的是 `first_seen_at` / `last_seen_at` 两列。
//     物化视图里它们是 `MIN(first_request_at)` / `MAX(last_request_at)`（357:31-32），
//     而 `session_summaries.first_request_at` / `last_request_at` 是
//     `timestamp with time zone` 且**没有 NOT NULL**
//     （655_session_summaries_schema_reconcile.sql:48-49）
//     ⇒ 一组里全为空 ⇒ `MIN()` 为 NULL ⇒ 该行被裸扫进 `time.Time` 失败
//       ⇒ 上一条的 `continue` 把整行丢掉，而 `total` 仍然把它算进去。
//     ⇒ **页面据此必须允许「显示数 < total」并说明。**
//
//     ★ 追到 schema 后**排除了**我原本怀疑的几列（别把它们也写进页面说明，会误导）：
//     `session_summaries` 的 `request_count` / `success_count` / `error_count`
//     / `total_cost_usd` 全是 **`NOT NULL DEFAULT 0`**（655:52-55、677:40-43）
//     ⇒ 它们的 `SUM`/`AVG` **不可能为 NULL**，不是丢行来源。
//     `avg_health_score` / `avg_latency_ms` 已用 `sql.NullInt64` **兜住**；
//     `models_used` 的 nil 也被改写成 `[]`。
//
// (5) ★★★★ 分页是**本仓第五种**越界语义：
//         limit := queryInt(r, "limit", 50)
//         if limit < 1 || limit > 200 { limit = 50 }
//     ⇒ 越界是**回落 50**，**既不是 clamp 也不是回落 20**
//     （pending 50/500 clamp、request-anomalies 50/500 clamp、
//     output-compliance 20→200 clamp、prompt-injection 20 **回落**）。
//
// (6) ★★★ **三个列表的默认排序各不相同**：
//         端点        默认 order_by   switch 认的值       未命中时落回
//         clients     `cost`         sessions / health   total_cost_usd DESC
//         tasks       `sessions`     cost / health       session_count DESC
//     `switch` **没有 default 分支** ⇒ 传 `order_by=xxx` **静默落回默认、不报错**。
//
// (7) ★★★ 租户隔离比注册档位细：非 superAdmin 且显式传了别的 `tenant_id`
//     ⇒ 403 `cross-tenant access denied`；非 superAdmin 且**不传**
//     ⇒ **自动填成 `callerTenant`**（不是报错、也不是全库）。
//
// (8) ★★★ 错误分类单列一档（`writeAnalyticsDetailErr`，由 mv_guard_test 固化）：
//     `42P01`（物化视图不存在）⇒ **503** + 含 `analytics_view_missing` 的引导载荷；
//     其余（含 `ErrNoRows`）⇒ **404** 原文案。
//     ⇒ 与本仓其它族的 `{"error":"…"}` 字符串信封并存，**这一族多一个结构化引导码**。
//
// (9) ★★★★ ★★ `health_distribution` 只有 **5 个键**（a/b/c/d/f），
//     **没有 `total`、没有 `*_percent`**。
//     ★★ 陷阱：`admin` 包与 `admin/dashboardapi` 包**各有一个同名 `HealthDistribution`**：
//         admin.HealthDistribution（dashboard_session_stats.go:25）= 5 个计数键
//         dashboardapi.HealthDistribution（session_overview.go:63）= total + 5 计数 + 5 百分比
//       后者的百分比在 `session_overview.go:369` / `session_health.go:139` 里计算，
//       **而本族用的是前一个** ⇒ **不要把 dashboard 的形状套过来**。
//
// (10) ★★★ `avg_health_score` / `avg_latency_ms` 是 `*int` 且 JSON tag 带 `omitempty`
//     ⇒ **键可能整个不存在**（不是「值为 null」）。与 output-compliance 那种
//     「键一定存在、值为 null」正好相反。
//
// (11) ★ `tenant_id` 在响应里**不返回**（SELECT 列表里没有它），
//     但过滤**确实做了** —— 与 pending 的 `TenantID json:"-"` 同款。

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
  total_cost_usd: number
  avg_cost_per_session: number
  /** ★ `*int` + `omitempty` ⇒ **键可能整个不存在**。见坑 10。 */
  avg_health_score?: number
  health_distribution: AnalyticsHealthDistribution
  total_success: number
  total_errors: number
  /** ★ 同上，键可能不存在。 */
  avg_latency_ms?: number
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
  refreshed_at: string
}

export interface AnalyticsTaskListResponse {
  tasks: AnalyticsTaskSummary[]
  total: number
  limit: number
  offset: number
  refreshed_at: string
}

export interface AnalyticsListParams {
  limit?: number
  offset?: number
  orderBy?: AnalyticsOrderBy
  tenantId?: string
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
  const s = qs.toString()
  return s ? `?${s}` : ''
}

// ── clients ──────────────────────────────────────────────────────────────

export function fetchAnalyticsClients(
  params: AnalyticsListParams = {},
  options?: RequestOptions,
): Promise<AnalyticsClientListResponse> {
  const q = buildQuery(params, ANALYTICS_CLIENT_ORDER_BYS)
  return req<unknown>('GET', `/api/admin/session-analytics/clients${q}`, undefined, options).then(
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
  return req<unknown>('GET', `/api/admin/session-analytics/tasks${q}`, undefined, options).then(
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
