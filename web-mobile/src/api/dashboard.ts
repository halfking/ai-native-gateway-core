import { req, type RequestOptions } from './client'

/**
 * dashboard.ts — Dashboard API v2 的七条只读端点（2026-10-08，第五十八批）。
 *
 * session-overview / session-trend / session-health / session-active /
 * module-stats / errors / performance
 *
 * 注册处 `admin/handler.go:1053-1061`，全部 `admin(...)` 档 ⇒
 * **tenant_admin 可用**，与 auto-route 线的 `h.superAdmin` 相反。
 *
 * ## ★★★★★★ 这七条共用一个信封，且降级**长得和真零值一模一样**
 *
 * `admin/dashboardapi/types.go:187` 的 `writeSuccessJSON` 产出：
 *
 * ```json
 * { "success": true, "data": {…}, "metadata": {…}, "timestamp": "…" }
 * ```
 *
 * ★ 而 `writeDegraded`（errors.go:319-334 等）**也是 HTTP 200 + `success:true`**，
 * 只是把 `data` 填成**零值/空数组**，并在 metadata 上打三个键：
 *
 * ```json
 * "metadata": { "degraded": true, "missing_view": "…", "hint": "数据视图尚未初始化，请先执行数据聚合迁移" }
 * ```
 *
 * ⇒ **「聚合迁移没跑」与「真的全是零」在 data 上逐字段相同。**
 *   唯一区分是 `metadata.degraded`。
 *   客户端若只看 data，会在数据管道挂掉时给出一张**全部正常的看板**——
 *   这是本族最危险的一处，也是本批所有判据的重心。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 共用信封（types.go:42-72，逐字照抄 struct tag）
 * ═════════════════════════════════════════════════════════════════════════ */

/** error（types.go:52-56）。`details` 有 omitempty。 */
export interface DashboardErrorInfo {
  code: string
  message: string
  details?: string
}

/** metadata（types.go:59-72）。★ `generated_at` **没有** omitempty ⇒ 必在。 */
export interface DashboardMetadata {
  total?: number
  page?: number
  size?: number
  pages?: number
  cache_hit?: boolean
  generated_at: string
  took_ms?: number
  /** ★★ 缺表降级标记：整个 data 是零值。 */
  degraded?: boolean
  /** 缺的是哪个视图（degraded 时给） */
  missing_view?: string
  /** 人类可读的修复提示（degraded 时给） */
  hint?: string
}

/** 成功信封的顶层键。`code`/`message`/`error` 在成功时**不出现**。 */
export const DASHBOARD_SUCCESS_ENVELOPE_KEYS = ['success', 'data', 'metadata', 'timestamp'] as const
/** 错误信封的顶层键。★ `data` / `metadata` 都**不出现**。 */
export const DASHBOARD_ERROR_ENVELOPE_KEYS = ['success', 'code', 'message', 'error', 'timestamp'] as const
/** metadata 的必填键（只有 generated_at 没有 omitempty）。 */
export const DASHBOARD_METADATA_REQUIRED_KEYS = ['generated_at'] as const

export interface DashboardEnvelope<T> {
  success: boolean
  data?: T
  metadata?: DashboardMetadata
  timestamp: string
  code?: string
  message?: string
  error?: DashboardErrorInfo
}

/**
 * ★ 拆信封。成功信封必须有 `data`（业务数据在 data 上）；错误信封必须有 `code`。
 *
 * 成功但 `data` 缺失 ⇒ `writeSuccessJSON(w, nil, …)` 那种，data 会被 omitempty 吃掉
 * ⇒ 形状不符，抛错而不是当成空对象放行。
 */
export function unwrapDashboardEnvelope<T>(resp: unknown): DashboardEnvelope<T> {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`Dashboard 响应形状不符：期望信封对象，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  if (typeof d.success !== 'boolean') {
    throw new Error('Dashboard 响应形状不符：缺 success（既不是成功信封也不是错误信封）')
  }
  if (typeof d.timestamp !== 'string') {
    throw new Error('Dashboard 响应形状不符：缺 timestamp')
  }
  if (d.success === true) {
    if (!('data' in d)) {
      throw new Error('Dashboard 成功信封缺 data（data 是 omitempty，缺失即形状不符）')
    }
    if ('metadata' in d) {
      const md = d.metadata
      if (!md || typeof md !== 'object' || Array.isArray(md)) {
        throw new Error('Dashboard metadata 不是对象')
      }
      const missing = DASHBOARD_METADATA_REQUIRED_KEYS.filter((k) => !(k in (md as object)))
      if (missing.length > 0) {
        throw new Error(`Dashboard metadata 缺 ${missing.length} 个键（${missing.join(', ')}）`)
      }
    }
  } else {
    if (!('code' in d) || !('error' in d)) {
      throw new Error('Dashboard 错误信封缺 code / error')
    }
  }
  return d as unknown as DashboardEnvelope<T>
}

/**
 * ★★★ **这个信封是不是「降级」** —— 全族唯一能区分「数据管道挂了」与「真的是零」的信号。
 *
 * ⚠️ `degraded` 是 omitempty ⇒ 正常时**键不存在**（不是 `false`）。
 *   所以必须用 `=== true`，不能写成 `if (md.degraded)`（后者恰好也对，
 *   但写成 `=== false` 判「正常」就会把缺键也算成降级）。
 */
export function dashboardIsDegraded(env: DashboardEnvelope<unknown>): boolean {
  return env.metadata?.degraded === true
}

/** 降级时缺的是哪个视图；非降级返回 null（拿不到就说拿不到，不猜）。 */
export function dashboardMissingView(env: DashboardEnvelope<unknown>): string | null {
  return dashboardIsDegraded(env) ? env.metadata?.missing_view ?? null : null
}

/** 降级时的修复提示。 */
export function dashboardHint(env: DashboardEnvelope<unknown>): string | null {
  return dashboardIsDegraded(env) ? env.metadata?.hint ?? null : null
}

/**
 * ★ **非降级时不存在「查缓存」与「数据缺失」的区分**：非降级下 data 一定有值。
 * ⇒ 客户端**不需要**（也不应该）把空数组当成「数据缺失」。
 */
export function dashboardDegradedWithoutMissingView(env: DashboardEnvelope<unknown>): boolean {
  return dashboardIsDegraded(env) && env.metadata?.missing_view === undefined
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 查询参数（types.go:119-148，逐字照抄 ParseQueryParams）
 * ═════════════════════════════════════════════════════════════════════════ */

/** ★★ 全部越界都是**静默回落**，没有一条会 400。 */
export const DASHBOARD_DAYS_DEFAULT = 7
export const DASHBOARD_PAGE_DEFAULT = 1
export const DASHBOARD_SIZE_DEFAULT = 20
export const DASHBOARD_SIZE_MAX = 100
export const DASHBOARD_DAYS_MIN = 1
export const DASHBOARD_DAYS_MAX = 90

export interface DashboardQuery {
  tenantId?: string
  days?: number
  page?: number
  size?: number
  sortBy?: string
  sortDir?: string
  search?: string
  /** ★ 后端是 `q.Get("refresh") == "true"`，**严格相等**。 */
  refresh?: boolean
}

function dashboardSuffix(q: DashboardQuery | undefined): string {
  const qs = new URLSearchParams()
  if (!q) return ''
  if (q.tenantId !== undefined) qs.set('tenant_id', q.tenantId)
  if (q.days !== undefined) qs.set('days', String(q.days))
  if (q.page !== undefined) qs.set('page', String(q.page))
  if (q.size !== undefined) qs.set('size', String(q.size))
  if (q.sortBy !== undefined) qs.set('sort_by', q.sortBy)
  if (q.sortDir !== undefined) qs.set('sort_dir', q.sortDir)
  if (q.search !== undefined) qs.set('search', q.search)
  // ★ 后端判 `== "true"`，所以这里必须发字面量 "true"/"false"
  if (q.refresh !== undefined) qs.set('refresh', q.refresh ? 'true' : 'false')
  return qs.toString() ? `?${qs}` : ''
}

/** ★ days 越界（非整数也 Atoi 失败）⇒ 静默回落 7。 */
export function dashboardDaysEffective(days: number | undefined): number {
  if (days === undefined || !Number.isInteger(days)) return DASHBOARD_DAYS_DEFAULT
  return days < DASHBOARD_DAYS_MIN || days > DASHBOARD_DAYS_MAX ? DASHBOARD_DAYS_DEFAULT : days
}

/** ★ size 越界 ⇒ 静默回落 20。 */
export function dashboardSizeEffective(size: number | undefined): number {
  if (size === undefined || !Number.isInteger(size)) return DASHBOARD_SIZE_DEFAULT
  return size < 1 || size > DASHBOARD_SIZE_MAX ? DASHBOARD_SIZE_DEFAULT : size
}

/** ★ page < 1 ⇒ 静默回落 1。 */
export function dashboardPageEffective(page: number | undefined): number {
  if (page === undefined || !Number.isInteger(page)) return DASHBOARD_PAGE_DEFAULT
  return page < DASHBOARD_PAGE_DEFAULT ? DASHBOARD_PAGE_DEFAULT : page
}

/**
 * ★★★ **tenant_admin 填 `tenant_id` 会被后端静默改写成他自己的租户**
 * （`normalizeDashboardScope`，auth.go:39-45：非 super_admin / admin_key
 * 一律 `params.TenantID = auth.TenantID`）。
 *
 * ⇒ UI **不得**给 tenant_admin 提供这个筛选框：他填什么都会拿到自己租户的数据，
 * 而返回体里没有任何标记告诉他「你填的被忽略了」。
 * 只有 super_admin / admin_key 才真正拿到跨租户能力。
 */
export function dashboardTenantFilterHonored(role: string | null | undefined): boolean {
  return role === 'super_admin' || role === 'admin_key'
}

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/dashboard/session-overview（session_overview.go:29）
 * ═════════════════════════════════════════════════════════════════════════ */

/** health_distribution / compliance_stats / cost_stats 等子对象的形状随各自定义，
 *  本层只钉顶层键（下面用 index signature 承接，避免写死一个我们没验证过的子结构）。 */
export interface SessionOverviewData {
  total_sessions: number
  active_sessions: number
  new_sessions_24h: number
  closed_sessions_24h: number
  health_distribution: Record<string, unknown>
  compliance_stats: Record<string, unknown>
  cost_stats: Record<string, unknown>
  model_usage: unknown[]
  top_clients: unknown[]
  top_tasks: unknown[]
  cost_trend: unknown[]
  session_trend: unknown[]
  generated_at: string
  period_start: string
  period_end: string
}

export const SESSION_OVERVIEW_KEYS = [
  'total_sessions', 'active_sessions', 'new_sessions_24h', 'closed_sessions_24h',
  'health_distribution', 'compliance_stats', 'cost_stats', 'model_usage',
  'top_clients', 'top_tasks', 'cost_trend', 'session_trend',
  'generated_at', 'period_start', 'period_end',
] as const

export function fetchSessionOverview(
  q?: DashboardQuery,
  options?: RequestOptions,
): Promise<DashboardEnvelope<SessionOverviewData>> {
  return req<unknown>('GET', `/api/admin/dashboard/session-overview${dashboardSuffix(q)}`, undefined, options)
    .then((r) => unwrapDashboardEnvelope<SessionOverviewData>(r))
    .then((env) => {
      if (env.data) requireKeys(env.data, SESSION_OVERVIEW_KEYS, 'session-overview')
      return env
    })
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/dashboard/session-trend（session_trend.go:26）
 * ═════════════════════════════════════════════════════════════════════════ */

export interface SessionTrendData {
  trend: unknown[]
  summary: Record<string, unknown>
  period_start: string
  period_end: string
}

export const SESSION_TREND_KEYS = ['trend', 'summary', 'period_start', 'period_end'] as const

export function fetchSessionTrend(
  q?: DashboardQuery,
  options?: RequestOptions,
): Promise<DashboardEnvelope<SessionTrendData>> {
  return req<unknown>('GET', `/api/admin/dashboard/session-trend${dashboardSuffix(q)}`, undefined, options)
    .then((r) => unwrapDashboardEnvelope<SessionTrendData>(r))
    .then((env) => {
      if (env.data) requireKeys(env.data, SESSION_TREND_KEYS, 'session-trend')
      return env
    })
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/admin/dashboard/session-health（session_health.go:25）
 * ═════════════════════════════════════════════════════════════════════════ */

export interface SessionHealthData {
  distribution: Record<string, unknown>
  trend: unknown[]
  top_issues: unknown[]
}

export const SESSION_HEALTH_KEYS = ['distribution', 'trend', 'top_issues'] as const

export function fetchSessionHealth(
  q?: DashboardQuery,
  options?: RequestOptions,
): Promise<DashboardEnvelope<SessionHealthData>> {
  return req<unknown>('GET', `/api/admin/dashboard/session-health${dashboardSuffix(q)}`, undefined, options)
    .then((r) => unwrapDashboardEnvelope<SessionHealthData>(r))
    .then((env) => {
      if (env.data) requireKeys(env.data, SESSION_HEALTH_KEYS, 'session-health')
      return env
    })
}

/* ═══════════════════════════════════════════════════════════════════════════
 * D. GET /api/admin/dashboard/session-active（session_active.go:42）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★ **分页在 data 和 metadata 里各有一份**（`page`/`size`：data :44-45，
 * metadata :62-63）。两者都来自同一组 params，理论上相等；
 * 不相等即契约漂移或中间层加工过 —— 用 `dashboardPaginationDisagrees` 盯着。
 */
export interface SessionActiveData {
  sessions: unknown[]
  total_active: number
  page: number
  size: number
}

export const SESSION_ACTIVE_KEYS = ['sessions', 'total_active', 'page', 'size'] as const

export function fetchSessionActive(
  q?: DashboardQuery,
  options?: RequestOptions,
): Promise<DashboardEnvelope<SessionActiveData>> {
  return req<unknown>('GET', `/api/admin/dashboard/session-active${dashboardSuffix(q)}`, undefined, options)
    .then((r) => unwrapDashboardEnvelope<SessionActiveData>(r))
    .then((env) => {
      if (env.data) requireKeys(env.data, SESSION_ACTIVE_KEYS, 'session-active')
      return env
    })
}

/** ★ data 与 metadata 的 page/size 不一致（两者本应同源）。 */
export function dashboardPaginationDisagrees(env: DashboardEnvelope<SessionActiveData>): boolean {
  if (!env.data || !env.metadata) return false
  const md = env.metadata
  if (md.page !== undefined && md.page !== env.data.page) return true
  if (md.size !== undefined && md.size !== env.data.size) return true
  return false
}

/** ★ 「本页取满 + 还有更多」⇒ 该显示翻页按钮。 */
export function dashboardHasNextPage(env: DashboardEnvelope<SessionActiveData>): boolean {
  if (!env.data) return false
  const md = env.metadata
  if (md?.pages !== undefined) return env.data.page < md.pages
  return env.data.sessions.length >= env.data.size
}

/* ═══════════════════════════════════════════════════════════════════════════
 * E. GET /api/admin/dashboard/module-stats（module_stats.go:43）
 * ═════════════════════════════════════════════════════════════════════════ */

export interface ModuleStatsData {
  modules: unknown[]
  summary: {
    total_modules: number
    total_executions: number
    avg_cache_hit_rate: number
    avg_duration_ms: number
  }
  period_start: string
  period_end: string
}

export const MODULE_STATS_KEYS = ['modules', 'summary', 'period_start', 'period_end'] as const
export const MODULE_STATS_SUMMARY_KEYS = [
  'total_modules', 'total_executions', 'avg_cache_hit_rate', 'avg_duration_ms',
] as const

export function fetchModuleStats(
  q?: DashboardQuery,
  options?: RequestOptions,
): Promise<DashboardEnvelope<ModuleStatsData>> {
  return req<unknown>('GET', `/api/admin/dashboard/module-stats${dashboardSuffix(q)}`, undefined, options)
    .then((r) => unwrapDashboardEnvelope<ModuleStatsData>(r))
    .then((env) => {
      if (env.data) {
        requireKeys(env.data, MODULE_STATS_KEYS, 'module-stats')
        const s = env.data.summary as Record<string, unknown> | undefined
        if (s && typeof s === 'object') requireKeys(s, MODULE_STATS_SUMMARY_KEYS, 'module-stats.summary')
      }
      return env
    })
}

/* ═══════════════════════════════════════════════════════════════════════════
 * F. GET /api/admin/dashboard/errors（errors.go:28）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★ Go 字段叫 `Trend`，**JSON 键却是 `recent_errors`**（errors.go:31）。
 * ⇒ 按字段名去读 JSON 会读到 undefined。契约以 JSON 键为准。
 */
export interface ErrorStatsData {
  summary: {
    total_errors: number
    error_rate: number
    total_requests: number
    avg_error_latency_ms: number
  }
  distribution: unknown[]
  recent_errors: unknown[]
  top_errors: unknown[]
}

export const ERROR_STATS_KEYS = ['summary', 'distribution', 'recent_errors', 'top_errors'] as const

export function fetchDashboardErrors(
  q?: DashboardQuery,
  options?: RequestOptions,
): Promise<DashboardEnvelope<ErrorStatsData>> {
  return req<unknown>('GET', `/api/admin/dashboard/errors${dashboardSuffix(q)}`, undefined, options)
    .then((r) => unwrapDashboardEnvelope<ErrorStatsData>(r))
    .then((env) => {
      if (env.data) requireKeys(env.data, ERROR_STATS_KEYS, 'dashboard/errors')
      return env
    })
}

/**
 * ★★★ `error_rate` 是**比值**（errors / requests），不是百分数。
 * 且 `writeDegraded` 把它填成 0 ⇒ 与「真的 0% 错误率」无法区分，
 * 唯一区分仍是 `metadata.degraded`。
 */
export function dashboardErrorRatePercent(d: ErrorStatsData): number {
  return d.summary.error_rate * 100
}

/** ★ 分母为 0 时比值无意义（后端已给 0）；这里只把「无意义」显式化。 */
export function dashboardErrorRateMeaningless(d: ErrorStatsData): boolean {
  return d.summary.total_requests === 0
}

/* ═══════════════════════════════════════════════════════════════════════════
 * G. GET /api/admin/dashboard/performance（performance.go:28）
 * ═════════════════════════════════════════════════════════════════════════ */

export interface PerformanceData {
  summary: {
    avg_latency_ms: number
    p50_latency_ms: number
    p95_latency_ms: number
    p99_latency_ms: number
  }
  latency_distribution: Record<string, unknown>
  throughput: unknown[]
  slow_queries: unknown[]
}

export const PERFORMANCE_KEYS = [
  'summary', 'latency_distribution', 'throughput', 'slow_queries',
] as const
export const PERFORMANCE_SUMMARY_KEYS = [
  'avg_latency_ms', 'p50_latency_ms', 'p95_latency_ms', 'p99_latency_ms',
] as const

export function fetchDashboardPerformance(
  q?: DashboardQuery,
  options?: RequestOptions,
): Promise<DashboardEnvelope<PerformanceData>> {
  return req<unknown>('GET', `/api/admin/dashboard/performance${dashboardSuffix(q)}`, undefined, options)
    .then((r) => unwrapDashboardEnvelope<PerformanceData>(r))
    .then((env) => {
      if (env.data) {
        requireKeys(env.data, PERFORMANCE_KEYS, 'dashboard/performance')
        const s = env.data.summary as Record<string, unknown> | undefined
        if (s && typeof s === 'object') requireKeys(s, PERFORMANCE_SUMMARY_KEYS, 'dashboard/performance.summary')
      }
      return env
    })
}

/** ★ 分位数必须单调：p50 ≤ p95 ≤ p99。乱序即契约漂移。 */
export function dashboardLatencyQuantilesOutOfOrder(d: PerformanceData): boolean {
  const s = d.summary
  return s.p50_latency_ms > s.p95_latency_ms || s.p95_latency_ms > s.p99_latency_ms
}

/* ── 内部：逐端点钉必填键（不抽通用解包器，各端点契约各自不同） ────────── */

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} data 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}