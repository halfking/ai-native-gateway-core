import { req, type RequestOptions } from './client'

/**
 * autoRoute.ts — 自动路由读面（2026-10-08，第五十五批）。
 *
 * 六条只读端点，分属三个 Go 文件：
 *   - admin/auto_route.go        index / audit / cost/customer / cost/model
 *   - admin/auto_route_tuning.go tuning/accuracy
 *   - admin/analytics.go         analytics/decision/{request_id}
 *
 * ## ★★★★★★★ 权限档位：**superAdmin，不是 admin**
 *
 * 三个注册函数的形参都叫 `adminWrap`，看起来像 admin 档 —— **名字是假的**：
 *   - `admin/handler.go:1381` `autoH.RegisterAutoRouteRoutes(mux, h.superAdmin)`
 *   - `admin/handler.go:1430` `analyticsH.RegisterAnalyticsRoutes(mux, h.superAdmin)`
 *   - `admin/auto_route.go:116`  `tuning.RegisterTuningRoutes(mux, adminWrap)` —— 转手，
 *     而 `adminWrap` 就是上一行那个形参，值仍是 `h.superAdmin`。
 *
 * `h.superAdmin` = `SuperAdminMiddleware`（handler.go:886）⇒ **tenant_admin 直接 403**。
 * 若按形参名把它当 admin 档挂到抽屉席，tenant_admin 用户点进去只会看到 403。
 * 这是「不能按名字/路径判权限」的一个实例：名字骗人，**要读到实际绑定的那个值**。
 *
 * ## ★★★★★★ 本批最危险的两处形状陷阱
 *
 * 1. **稀疏键 map**：`index` / `cost/customer` / `cost/model` 三条**每一行只有一个键是
 *    无条件的**（index 是 4 个），其余全部由 `*float64` / `*int` 指针的 nil 分支决定
 *    要不要写进去。⇒ 「键不存在」= 该指标无数据，**不是 0**。
 * 2. **l1 的 splat 合并**：`analytics/decision` 把 `auto_decision` 这个 JSON blob
 *    **逐键展开进 l1**（analytics.go:833-839），且展开发生在 `task_type`/`profile`/
 *    `confidence` 赋值**之后** ⇒ blob 里的同名键会**覆盖**数据库列读出来的值。
 *    客户端不能假设 `l1.task_type` 来自 DB。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/auto-route/index（superAdmin 档，auto_route.go:264）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★★★★★ **稀疏键**：只有 4 个键无条件存在（auto_route.go:352-356 + :399），
 * 其余 13 个都在 `if xxx != nil` 里（:357-398）—— DB 里是 NULL 就不写这个键。
 *
 * ⇒ `success_rate` 缺失和 `success_rate: 0` 是**两件事**，UI 不得都渲染成 0%。
 *
 * 另有一处**异构哨兵**：索引全空时后端不返回 `[]`，而是返回
 * `[{"warning": "credential_model_index is empty; awaiting first bg worker refresh…"}]`
 * （auto_route.go:294-297）—— 一个**只有 warning、没有 credential_id** 的元素。
 */
export interface AutoRouteIndexRow {
  /** 无条件。`rowBucket.Format(time.RFC3339)` */
  bucket: string
  /** 无条件 */
  credential_id: number
  /** 无条件 */
  raw_model: string
  /** 无条件。`updatedAt.Format(time.RFC3339)` */
  updated_at: string
  canonical_id?: number
  canonical_name?: string
  billing_mode?: string
  unit_price_in_per_1m?: number
  unit_price_out_per_1m?: number
  context_window?: number
  success_rate?: number
  p95_latency_ms?: number
  active_sessions?: number
  concurrency_limit?: number
  pressure_ratio?: number
  score_smart?: number
  score_speed_first?: number
  score_cost_first?: number
}

export const AUTO_ROUTE_INDEX_REQUIRED_KEYS = [
  'bucket', 'credential_id', 'raw_model', 'updated_at',
] as const

/** 空索引哨兵元素的唯一键（auto_route.go:296）。 */
export const AUTO_ROUTE_INDEX_WARNING_KEY = 'warning'

export interface AutoRouteIndexParams {
  canonicalId?: string
  top?: number
}

export function fetchAutoRouteIndex(
  params?: AutoRouteIndexParams,
  options?: RequestOptions,
): Promise<AutoRouteIndexRow[]> {
  const qs = new URLSearchParams()
  if (params?.canonicalId) qs.set('canonical_id', params.canonicalId)
  if (params?.top !== undefined) qs.set('top', String(params.top))
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<unknown>('GET', `/api/admin/auto-route/index${suffix}`, undefined, options).then(
    unwrapAutoRouteIndex,
  )
}

export function unwrapAutoRouteIndex(resp: unknown): AutoRouteIndexRow[] {
  if (!Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`自动路由索引 响应形状不符：期望数组，实得 ${actual}`)
  }
  resp.forEach((r, i) => {
    if (!r || typeof r !== 'object' || Array.isArray(r)) {
      throw new Error(`自动路由索引 rows[${i}] 不是对象`)
    }
    const d = r as Record<string, unknown>
    // ★ 空索引哨兵：只有 warning、没有 credential_id —— 必须放行，否则空态会抛错
    if (AUTO_ROUTE_INDEX_WARNING_KEY in d) return
    const missing = AUTO_ROUTE_INDEX_REQUIRED_KEYS.filter((k) => !(k in d))
    if (missing.length > 0) {
      throw new Error(`自动路由索引 rows[${i}] 缺 ${missing.length} 个键（${missing.join(', ')}）`)
    }
  })
  return resp as AutoRouteIndexRow[]
}

/** ★ 是不是「还没跑过首轮 bg 刷新」的哨兵（不是真空索引）。 */
export function autoRouteIndexAwaitingFirstRefresh(rows: AutoRouteIndexRow[]): boolean {
  return rows.length > 0 && rows.every((r) => AUTO_ROUTE_INDEX_WARNING_KEY in (r as object))
}

/** ★ 稀疏键：这些指标**缺失**就是无数据，不得当 0 渲染。 */
export const AUTO_ROUTE_INDEX_SPARSE_KEYS = [
  'canonical_id', 'canonical_name', 'billing_mode', 'unit_price_in_per_1m',
  'unit_price_out_per_1m', 'context_window', 'success_rate', 'p95_latency_ms',
  'active_sessions', 'concurrency_limit', 'pressure_ratio', 'score_smart',
  'score_speed_first', 'score_cost_first',
] as const

/**
 * ★ `top` 的越界值被**静默回落**到 100，不是 clamp 到边界、也不是报错。
 * auto_route.go:268 `if v, err := Atoi(...); err == nil && v > 0 && v <= 1000 { top = v }`
 * —— 条件不满足就保持默认 100。
 * ⚠️ 与两条 cost 端点的**默认 50 / 上限 500** 不同，三条各不相同，
 *    **不能跨端点类推**（同一个族里三种口径）。
 */
export function autoRouteIndexTopAccepted(top: number | undefined): boolean {
  if (top === undefined) return true
  return Number.isInteger(top) && top > 0 && top <= 1000
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/auto-route/audit（superAdmin 档，auto_route.go:495）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★★★ **5 个键无条件 + 3 个键条件存在**。
 *
 * 无条件（:571-578 + :761）：total_requests / total_auto_requests /
 *   specified_model_requests / success_rate / outcome_source
 * 条件存在：`task_distribution` / `profile_distribution` / `top_chosen_models`
 *   —— 三处赋值**全在 `if err == nil` 里**（:627、:665、:724/:758）。
 *   ⇒ 对应查询失败时**键直接缺失，且 HTTP 仍是 200**。
 *   「这一块没有数据」与「这一块的查询挂了」在响应里长得一模一样。
 */
export interface AutoRouteAuditResponse {
  total_requests: number
  total_auto_requests: number
  specified_model_requests: number
  /** `total > 0` 时是 successes/total，否则**后端强制写 0.0**（:576-580） */
  success_rate: number
  outcome_source: AutoRouteOutcomeSource
  task_distribution?: Record<string, number>
  profile_distribution?: Record<string, number>
  top_chosen_models?: Array<{ model: string; count: number }>
}

/** auto_route_outcome_freshness.go:70-76，struct tag 逐字照抄。 */
export interface AutoRouteOutcomeSource {
  available: boolean
  /** ★ omitempty：不可用时**没有**这个键 */
  as_of?: string
  /** ★ omitempty：同上 */
  age_seconds?: number
  stale: boolean
  stale_after_seconds: number
  /** live | no_rows | stale | absent | query_failed（:82-88） */
  reason: string
}

/** 无条件键。条件键不在此列 —— 它们缺失是合法形态。 */
export const AUTO_ROUTE_AUDIT_REQUIRED_KEYS = [
  'total_requests', 'total_auto_requests', 'specified_model_requests',
  'success_rate', 'outcome_source',
] as const

export const AUTO_ROUTE_AUDIT_OPTIONAL_KEYS = [
  'task_distribution', 'profile_distribution', 'top_chosen_models',
] as const

/** outcome_source 的无条件键（as_of / age_seconds 是 omitempty，不在其中）。 */
export const AUTO_ROUTE_OUTCOME_KEYS = [
  'available', 'stale', 'stale_after_seconds', 'reason',
] as const

export function fetchAutoRouteAudit(options?: RequestOptions): Promise<AutoRouteAuditResponse> {
  return req<unknown>('GET', '/api/admin/auto-route/audit', undefined, options).then(unwrapAutoRouteAudit)
}

export function unwrapAutoRouteAudit(resp: unknown): AutoRouteAuditResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`自动路由审计 响应形状不符：期望对象，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  const missing = AUTO_ROUTE_AUDIT_REQUIRED_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`自动路由审计 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  for (const k of ['total_requests', 'total_auto_requests', 'specified_model_requests', 'success_rate']) {
    if (typeof d[k] !== 'number') {
      throw new Error(`自动路由审计 ${k} 不是 number`)
    }
  }
  const os = d.outcome_source
  if (!os || typeof os !== 'object' || Array.isArray(os)) {
    throw new Error('自动路由审计 outcome_source 不是对象')
  }
  const osd = os as Record<string, unknown>
  const osMissing = AUTO_ROUTE_OUTCOME_KEYS.filter((k) => !(k in osd))
  if (osMissing.length > 0) {
    throw new Error(`自动路由审计 outcome_source 缺 ${osMissing.length} 个键（${osMissing.join(', ')}）`)
  }
  return d as unknown as AutoRouteAuditResponse
}

/** ★ 哪些块因为查询失败而缺失（UI 要把「缺」和「空」分开显示）。 */
export function autoRouteAuditMissingBlocks(a: AutoRouteAuditResponse): string[] {
  return AUTO_ROUTE_AUDIT_OPTIONAL_KEYS.filter((k) => !(k in (a as unknown as Record<string, unknown>)))
}

/**
 * ★★★ **`total = auto + specified` 在两条路径上都成立**，可当客户端交叉校验：
 *   - 基表（auto_route.go:552-556）：`COUNT(*)` / `FILTER(is_auto_request = TRUE)` /
 *     `SUM(NOT COALESCE(is_auto_request, FALSE))`，WHERE 的准入条件（:559-563）与
 *     `auto_route.go` 的 MV 定义（migration 649:112）**逐字相同**，且对布尔列
 *     `= TRUE` 与 `IS NOT TRUE` 是**互斥且穷尽**的划分（NULL 归后者）。
 *   - MV（migration 649:99-113）：同样的两个 FILTER + 同一条准入 WHERE。
 * ⇒ 不一致即契约漂移或中间层加工过。
 */
export function autoRouteAuditTotalsDisagree(a: AutoRouteAuditResponse): boolean {
  return a.total_requests !== a.total_auto_requests + a.specified_model_requests
}

/**
 * ★ `success_rate` 的 0.0 有**两种来源**：真的全失败，或 `total_requests === 0` 时
 * 后端强制写 0（:577-580）。⇒ 「0% 成功率」和「没有流量」必须分开显示。
 */
export function autoRouteAuditHasNoTraffic(a: AutoRouteAuditResponse): boolean {
  return a.total_requests === 0
}

/**
 * ★★★ `outcome_source.stale === true` ⇒ **这一屏的成功率/奖励/路由数字不可信**。
 * 后端刻意把「数字低」与「数字不再产生」区分开（:755-759 注释）：
 * 这些值由后台 settle worker 回填，不由被统计的请求测出来。
 */
export function autoRouteAuditNumbersStale(a: AutoRouteAuditResponse): boolean {
  return a.outcome_source.stale
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/admin/auto-route/cost/customer（superAdmin 档，auto_route.go:858）
 * ═════════════════════════════════════════════════════════════════════════ */

/** ★★ 稀疏键：**只有 `api_key_id` 无条件**（auto_route.go:914-916），其余 13 个全条件。 */
export interface AutoRouteCustomerCostRow {
  api_key_id: number
  key_alias?: string
  tenant_id?: string
  application_id?: number
  cost_usd_1h?: number
  cost_usd_24h?: number
  cost_usd_7d?: number
  total_auto_requests?: number
  total_auto_success?: number
  active_concurrent?: number
  avg_pressure_1h?: number
  best_score_smart?: number
  best_score_speed_first?: number
  best_score_cost_first?: number
  last_request_at?: string
}

export const AUTO_ROUTE_CUSTOMER_COST_REQUIRED_KEYS = ['api_key_id'] as const

export interface AutoRouteCostParams {
  apiKeyId?: string
  top?: number
}

export function fetchAutoRouteCustomerCost(
  params?: AutoRouteCostParams,
  options?: RequestOptions,
): Promise<AutoRouteCustomerCostRow[]> {
  const qs = new URLSearchParams()
  if (params?.apiKeyId) qs.set('api_key_id', params.apiKeyId)
  if (params?.top !== undefined) qs.set('top', String(params.top))
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<unknown>('GET', `/api/admin/auto-route/cost/customer${suffix}`, undefined, options).then(
    unwrapAutoRouteCustomerCost,
  )
}

export function unwrapAutoRouteCustomerCost(resp: unknown): AutoRouteCustomerCostRow[] {
  if (!Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`客户成本 响应形状不符：期望数组，实得 ${actual}`)
  }
  resp.forEach((r, i) => {
    if (!r || typeof r !== 'object' || Array.isArray(r)) {
      throw new Error(`客户成本 rows[${i}] 不是对象`)
    }
    const missing = AUTO_ROUTE_CUSTOMER_COST_REQUIRED_KEYS.filter((k) => !(k in (r as object)))
    if (missing.length > 0) {
      throw new Error(`客户成本 rows[${i}] 缺 ${missing.length} 个键（${missing.join(', ')}）`)
    }
  })
  return resp as AutoRouteCustomerCostRow[]
}

/**
 * ★ `total_auto_success > total_auto_requests` —— 成本视图的计数器自相矛盾。
 * 两列都是同源指针（:929-935），正常不会发生；发生了说明视图口径变了。
 */
export function autoRouteCustomerCostContradicts(r: AutoRouteCustomerCostRow): boolean {
  if (r.total_auto_requests === undefined || r.total_auto_success === undefined) return false
  return r.total_auto_success > r.total_auto_requests
}

/* ═══════════════════════════════════════════════════════════════════════════
 * D. GET /api/admin/auto-route/cost/model（superAdmin 档，auto_route.go:973）
 * ═════════════════════════════════════════════════════════════════════════ */

/** ★★ 稀疏键：**只有 `raw_model` 无条件**（auto_route.go:1006-1008），其余 7 个全条件。 */
export interface AutoRouteModelCostRow {
  raw_model: string
  canonical_id?: number
  total_cost_usd?: number
  /** ★ int64 —— 大数不会掉精度 */
  total_tokens?: number
  avg_cost_per_1m_usd?: number
  success_rate?: number
  avg_latency_ms?: number
  total_requests?: number
  unique_api_keys?: number
}

export const AUTO_ROUTE_MODEL_COST_REQUIRED_KEYS = ['raw_model'] as const

export interface AutoRouteModelCostParams {
  canonicalId?: string
  top?: number
}

export function fetchAutoRouteModelCost(
  params?: AutoRouteModelCostParams,
  options?: RequestOptions,
): Promise<AutoRouteModelCostRow[]> {
  const qs = new URLSearchParams()
  if (params?.canonicalId) qs.set('canonical_id', params.canonicalId)
  if (params?.top !== undefined) qs.set('top', String(params.top))
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<unknown>('GET', `/api/admin/auto-route/cost/model${suffix}`, undefined, options).then(
    unwrapAutoRouteModelCost,
  )
}

export function unwrapAutoRouteModelCost(resp: unknown): AutoRouteModelCostRow[] {
  if (!Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`模型成本 响应形状不符：期望数组，实得 ${actual}`)
  }
  resp.forEach((r, i) => {
    if (!r || typeof r !== 'object' || Array.isArray(r)) {
      throw new Error(`模型成本 rows[${i}] 不是对象`)
    }
    const missing = AUTO_ROUTE_MODEL_COST_REQUIRED_KEYS.filter((k) => !(k in (r as object)))
    if (missing.length > 0) {
      throw new Error(`模型成本 rows[${i}] 缺 ${missing.length} 个键（${missing.join(', ')}）`)
    }
  })
  return resp as AutoRouteModelCostRow[]
}

/**
 * ★ `total_tokens`/`total_requests` 缺失时，两个成本列的**分母未知** ⇒
 * 不能算「单价」。这是稀疏键最容易被误用的一处：拿 `cost_usd_7d / total_requests`
 * 当单价会得到 NaN 或 Infinity，而 NaN 在 UI 上渲染出来是空白，不是错误。
 */
export function autoRouteModelCostPerRequest(r: AutoRouteModelCostRow): number | null {
  if (r.total_cost_usd === undefined || r.total_requests === undefined) return null
  if (r.total_requests === 0) return null
  return r.total_cost_usd / r.total_requests
}

/** ★ 两条 cost 共用 `top` 口径：默认 50、上限 500、越界**静默回落**（:864-867 / :979-982）。 */
export function autoRouteCostTopAccepted(top: number | undefined): boolean {
  if (top === undefined) return true
  return Number.isInteger(top) && top > 0 && top <= 500
}

/* ═══════════════════════════════════════════════════════════════════════════
 * E. GET /api/admin/auto-route/tuning/accuracy（superAdmin 档，auto_route_tuning.go:741）
 * ═════════════════════════════════════════════════════════════════════════ */

/** ★★ 与本批前四条相反：这里是**全字段无条件**的 typed struct（:791-800，无 omitempty）。 */
export interface AutoRouteAccuracyRow {
  task_type: string
  classifier: string
  total: number
  avg_quality: number
  avg_success: number
  avg_latency: number
  avg_cost: number
  drift_rate: number
}

export interface AutoRouteAccuracyResponse {
  window_days: number
  breakdown: AutoRouteAccuracyRow[]
  /** `time.Now().UTC()` ⇒ RFC3339Nano 字符串 */
  generated_at: string
}

export const AUTO_ROUTE_ACCURACY_ROW_KEYS = [
  'task_type', 'classifier', 'total', 'avg_quality', 'avg_success',
  'avg_latency', 'avg_cost', 'drift_rate',
] as const

export const AUTO_ROUTE_ACCURACY_REQUIRED_KEYS = [
  'window_days', 'breakdown', 'generated_at',
] as const

export function fetchAutoRouteAccuracy(
  params?: { days?: number },
  options?: RequestOptions,
): Promise<AutoRouteAccuracyResponse> {
  const qs = new URLSearchParams()
  if (params?.days !== undefined) qs.set('days', String(params.days))
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<unknown>('GET', `/api/admin/auto-route/tuning/accuracy${suffix}`, undefined, options).then(
    unwrapAutoRouteAccuracy,
  )
}

export function unwrapAutoRouteAccuracy(resp: unknown): AutoRouteAccuracyResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`调优准确率 响应形状不符：期望对象，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  const missing = AUTO_ROUTE_ACCURACY_REQUIRED_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`调优准确率 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  if (typeof d.window_days !== 'number') throw new Error('调优准确率 window_days 不是 number')
  if (!Array.isArray(d.breakdown)) throw new Error('调优准确率 breakdown 不是数组')
  d.breakdown.forEach((r, i) => {
    if (!r || typeof r !== 'object' || Array.isArray(r)) {
      throw new Error(`调优准确率 breakdown[${i}] 不是对象`)
    }
    const rm = AUTO_ROUTE_ACCURACY_ROW_KEYS.filter((k) => !(k in (r as object)))
    if (rm.length > 0) {
      throw new Error(`调优准确率 breakdown[${i}] 缺 ${rm.length} 个键（${rm.join(', ')}）`)
    }
  })
  return d as unknown as AutoRouteAccuracyResponse
}

/**
 * ★★ `days` 越界是**报错**（400 `admin_days_out_of_range`），不是静默回落 ——
 * 与 `index` / 两条 `cost` 的 `top` 处理**完全相反**（auto_route_tuning.go:747-754）。
 * 合法区间 1..90。
 */
export function autoRouteAccuracyDaysAccepted(days: number | undefined): boolean {
  if (days === undefined) return true
  return Number.isInteger(days) && days > 0 && days <= 90
}

/**
 * ★★ 后端**按窗口长度换物化视图**（:757-763）：`days <= 7` 走 5 分钟桶
 * （`tuning_signals_5m`），8..90 走天桶（`tuning_signals_daily`）。
 * ⇒ **同一组 task_type/classifier 在两个窗口下的 `avg_*` 口径并不完全可比**
 * （一个是 5 分钟桶的加权平均，一个是日桶的）。UI 换窗口时不能直接比大小。
 */
export function autoRouteAccuracyBucketGranularity(days: number): '5m' | 'daily' {
  return days <= 7 ? '5m' : 'daily'
}

/**
 * ★★ 五个 `avg_*` 全部 `COALESCE(..., 0)`（:774-778）。⇒ 一行 `total > 0` 但
 * 源列全 NULL 时，`avg_success = 0` 与「真的 0% 成功率」**逐字节相同**。
 * 这是本批第二处编造默认值（第一处是 routing 的 0.9/9999）。
 */
export function autoRouteAccuracyAveragesMayBeZeroPlaceholders(r: AutoRouteAccuracyRow): boolean {
  return (
    r.total > 0 &&
    r.avg_quality === 0 && r.avg_success === 0 && r.avg_latency === 0 &&
    r.avg_cost === 0 && r.drift_rate === 0
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
 * F. GET /api/admin/auto-route/analytics/decision/{request_id}（superAdmin，analytics.go:737）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★★★ **l1 会被 auto_decision 的 JSON blob 逐键覆盖。**
 *
 * analytics.go:830-840 的顺序是：
 *   1. `l1 := {task_type, profile}`（来自 DB 列，经 `nullStringOrEmpty`）
 *   2. `if confidence != nil { l1["confidence"] = ... }`
 *   3. `json.Unmarshal(auto_decision)` 后 **`for k, v := range parsed { l1[k] = v }`**
 *
 * ⇒ 第 3 步在最后，**blob 里的 `task_type` / `profile` / `confidence` 会覆盖前两步**。
 * 这些键是 `Record<string, unknown>` 的一部分，不能当可信的 DB 读数用。
 */
export interface AutoRouteDecisionL1 {
  /** ★ 可能已被 auto_decision blob 覆盖 */
  task_type: string
  /** ★ 同上 */
  profile: string
  confidence?: number
  [k: string]: unknown
}

/** l2 是**条件键**：只在 L2 查询命中时出现（analytics.go:889-916）。 */
export interface AutoRouteDecisionL2 {
  ts: string
  success: boolean
  chosen_credential_id?: number
  chosen_provider_id?: number
  tier?: number
  candidates_tried?: number
  resolution_path?: string
  canonical_model?: string
  /** ★ 只在 decision_trace 非空、非 `"{}"` 且能解析时出现（:908-914） */
  decision_trace?: Record<string, unknown>
}

export interface AutoRouteDecisionResponse {
  request_id: string
  ts: string
  success: boolean
  /** ★ NULL 被 `nullStringOrEmpty` 变成 `""`，不是 null —— 与「模型名为空串」不可分 */
  client_model: string
  /** ★ 同上 */
  outbound_model: string
  l1: AutoRouteDecisionL1
  api_key_id?: number
  credential_id?: number
  latency_ms?: number
  l2?: AutoRouteDecisionL2
}

export const AUTO_ROUTE_DECISION_REQUIRED_KEYS = [
  'request_id', 'ts', 'success', 'client_model', 'outbound_model', 'l1',
] as const

export const AUTO_ROUTE_L1_REQUIRED_KEYS = ['task_type', 'profile'] as const

export const AUTO_ROUTE_L2_REQUIRED_KEYS = ['ts', 'success'] as const

export function fetchAutoRouteDecision(
  requestId: string,
  options?: RequestOptions,
): Promise<AutoRouteDecisionResponse> {
  return req<unknown>(
    'GET',
    `/api/admin/auto-route/analytics/decision/${encodeURIComponent(requestId)}`,
    undefined,
    options,
  ).then(unwrapAutoRouteDecision)
}

export function unwrapAutoRouteDecision(resp: unknown): AutoRouteDecisionResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`决策回放 响应形状不符：期望对象，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  const missing = AUTO_ROUTE_DECISION_REQUIRED_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`决策回放 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  if (typeof d.success !== 'boolean') throw new Error('决策回放 success 不是 boolean')
  if (!d.l1 || typeof d.l1 !== 'object' || Array.isArray(d.l1)) {
    throw new Error('决策回放 l1 不是对象')
  }
  const l1m = AUTO_ROUTE_L1_REQUIRED_KEYS.filter((k) => !(k in (d.l1 as object)))
  if (l1m.length > 0) {
    throw new Error(`决策回放 l1 缺 ${l1m.length} 个键（${l1m.join(', ')}）`)
  }
  if ('l2' in d) {
    const l2 = d.l2
    if (!l2 || typeof l2 !== 'object' || Array.isArray(l2)) {
      throw new Error('决策回放 l2 不是对象')
    }
    const l2m = AUTO_ROUTE_L2_REQUIRED_KEYS.filter((k) => !(k in (l2 as object)))
    if (l2m.length > 0) {
      throw new Error(`决策回放 l2 缺 ${l2m.length} 个键（${l2m.join(', ')}）`)
    }
  }
  return d as unknown as AutoRouteDecisionResponse
}

/**
 * ★★★ **l2 缺失有三种完全不同的原因，响应里区分不了**：
 *   1. 后端根本没查 —— `l2Lookup` 只在 `len(uuidVariants(reqID)) == 2` 时为真
 *      （analytics.go:854-859）。hex32 / 探测 id 生成不出 dashed 变体 ⇒ 不查。
 *   2. 查了但没有决策日志（`pgx.ErrNoRows`）⇒ 仍不写 l2（:917-919）。
 *   3. L2 查询真出错 ⇒ **500**（:919-922），不会走到这里。
 * ⇒ UI 只能说「没有 L2 记录」，**不能说「查询失败」**（那是 500，走错误分支）。
 */
export function autoRouteDecisionHasL2(r: AutoRouteDecisionResponse): boolean {
  return r.l2 !== undefined
}

/**
 * ★ `client_model` / `outbound_model` 的 `""` 可能是 **NULL 也可能是真空串**
 * （`nullStringOrEmpty`，analytics.go:823-824）。⇒ 「没填模型」不能只靠空串判断，
 * 必须结合 `outbound_model === ""` 一起看（两者都空才是真的没模型信息）。
 */
export function autoRouteDecisionModelsBothEmpty(r: AutoRouteDecisionResponse): boolean {
  return r.client_model === '' && r.outbound_model === ''
}

/** ★ l1 里**唯一来自数据库列**的三个键（confidence 可选）。 */
export const AUTO_ROUTE_L1_DB_KEYS = ['task_type', 'profile', 'confidence'] as const

/**
 * ★★★★ **splat 跑过的可判定证据**：l1 里出现了这三个 DB 键之外的键。
 *
 * 合并之后响应里已经分不清「这个键来自 DB 列」还是「来自 auto_decision blob」，
 * 所以「是否被覆盖」**无法**直接判定 —— 但「splat 有没有跑过」可以：
 * 三个 DB 键之外的任何键都只可能是 blob 带进来的。
 *
 * ⚠️ 反过来也成立：**没有**多余键**不能**推出「task_type 一定来自 DB」——
 *   blob 完全可能只带一个同名的 `task_type` 而不带任何新键，此时这个谓词返回空，
 *   而 `l1.task_type` 已经被顶掉了。⇒ 空结果**不构成免责**，只能作为提示。
 */
export function autoRouteDecisionL1SplatKeys(l1: AutoRouteDecisionL1): string[] {
  const db = new Set<string>(AUTO_ROUTE_L1_DB_KEYS)
  return Object.keys(l1).filter((k) => !db.has(k))
}