import { req, type RequestOptions } from './client'

/**
 * usageEnhanced.ts — 用量成本增强面的三条**只读**端点（2026-10-08，第七十批）。
 *
 * GET /api/admin/usage/cost-trend       （handler.go:1267 `h.admin` 子路由）
 * GET /api/admin/usage/period-compare
 * GET /api/admin/usage/cache-economics
 *
 * ★ 三条都挂在 `h.admin(h.HandleUsageAdmin)`（`admin/usage.go:49-68`）
 *   这个**前缀子路由**上 ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
 *   （同族的 `trend-series` / `trend-models` 也是 admin 档，留待下一批。）
 *
 * ## ★★★★★ 本族最要紧的五件事
 *
 * (1) ★★★★★ **`degraded` 是恒发字段（不带 omitempty），而 `degraded_reason` 是条件键。**
 *      三个响应都是（`usage_enhanced.go:51-52` / `:334-336` / `:612-613`）：
 *      ```go
 *      Degraded       bool   `json:"degraded"`                 // 恒发
 *      DegradedReason string `json:"degraded_reason,omitempty"` // 仅降级时下发
 *      ```
 *      注释写明了理由：字段缺失与 `false` 在 API 语义上无法区分，
 *      那正是这个字段要消灭的歧义 ⇒ **客户端可以断言「服务端确认过它是好的」**。
 *      ★ 这是本仓少见的「故意不省略」写法，与 compression/data-lifecycle
 *      族的 omitempty 条件键**正好相反** ⇒ 不能照抄别族的判据。
 *      ⇒ 降级时返回 **200 + 全 0**，若不读 `degraded`，
 *      「本月花了 1139 美元」会被显示成「本月没花钱」（注释里的实测记录）。
 *
 * (2) ★★★★★ **`cache-economics` 的四个「节省」数字全是按硬编码假设推出来的**：
 *      ```go
 *      avgPricePerToken = dollarsSpent / (cacheReadTokens + promptTokens) // :729
 *      dollarsSaved     = cacheReadTokens * avgPricePerToken * 0.9         // :733 ← 假设缓存价是 10%
 *      compressionSaved = compressedRequests * 8000 * avgPricePerToken     // :739 ← 假设每次省 8000 token
 *      totalSaved       = dollarsSaved + compressionSaved                  // :743
 *      ```
 *      ⇒ `dollars_saved` / `compression_saved` / `total_saved` / `savings_rate`
 *      **不是实测账单**，是「三条写死的假设」的推论 ⇒ UI 必须标「估算」。
 *
 * (3) ★★★★ **`compressed_requests` 的查询失败是静默的**（:694-697 只有 `slog.Warn`），
 *      而它是 `compression_saved` 的**唯一输入**（:737-740）
 *      ⇒ 它是 0 时**分不清**「真的没有压缩请求」与「压缩计数查询失败」，
 *      连锁着 `compression_saved` / `total_saved` / `savings_rate` 一起不可分。
 *
 * (4) ★★★★ **`group_by` 决定读哪张表**：ledger 侧与 request 侧是**两张不同的表**
 *      （`planCostTrend`，:91-126）
 *      - `model` / `provider` / `api_key` → `usage_ledger_with_current_month ul`
 *      - `work_type` / `intent`           → `request_logs_with_current_month rl`
 *      ⇒ 同一个响应里换 `group_by` 就换了**基表**，成本口径虽经注释核对一致（:660），
 *      但那是某一天的一次实测，不是契约保证。
 *      ⇒ UI 换维度时要说明「换了基表」。
 *
 * (5) ★★★ **同族三条端点的时间窗缺省各不相同，且参数名也不同**：
 *      - `cost-trend`       缺省 **7 天**
 *      - `cache-economics`  缺省 **30 天**
 *      - `period-compare`   **不接受时间窗**，只收 `current` / `previous` 两个 `YYYY-MM`
 *      且 `cost-trend` / `cache-economics` 用的是 **`start` / `end`**（不是 `from`/`to`）：
 *      两者**必须同时给**，只给一个 ⇒ **400**；格式错 ⇒ **400**；
 *      `end < start` ⇒ 400。`days` 是 **clamp [1,366]**，
 *      且走 days 口径时起点被 `.Truncate(24*time.Hour)` **对齐到 UTC 零点**（:1445）。
 *
 * 另：`by_dimension` 只在 `len(modelChanges) > 0` 时才放 `"model"` 键（:447-449），
 * 查询失败时是 `{}`，与「查了但该维度没变化」同形（R68 注释自陈，只留了 slog.Warn）。
 * 且维度明细 SQL **硬编码 `LIMIT 10`**（:563）。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 公共：时间窗
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/usage_enhanced.go:148 `resolveUsageTimeRange(r, 7)`。 */
export const COST_TREND_DAYS_DEFAULT = 7
/** admin/usage_enhanced.go:623 `resolveUsageTimeRange(r, 30)`。 */
export const CACHE_ECONOMICS_DAYS_DEFAULT = 30
/** admin/usage.go:1439-1444：**clamp** 到 [1,366]。 */
export const USAGE_DAYS_MIN = 1
export const USAGE_DAYS_MAX = 366

/** admin/usage.go:1452/1456 的 `time.Parse("2006-01-02", …)` 布局。 */
export const USAGE_DATE_FORMAT = 'YYYY-MM-DD'
/** admin/usage_enhanced.go:466 `time.Parse("2006-01", period)` 布局。 */
export const PERIOD_FORMAT = 'YYYY-MM'

export interface UsageRangeQuery {
  /** clamp 到 [1,366]。★ 只在 `start`/`end` 都不给时才生效。 */
  days?: number
  /** `YYYY-MM-DD`。与 `end` **必须同时给**，只给一个 ⇒ 400。 */
  start?: string
  end?: string
}

/** ★ 与 compression 族的 `hours` 是**不同的参数名**（这里是 start/end），不可套用。 */
export function usageTimeRangeMode(q: UsageRangeQuery | undefined): 'days' | 'custom' {
  if (!q) return 'days'
  if (q.start !== undefined && q.start !== '') return 'custom'
  if (q.end !== undefined && q.end !== '') return 'custom'
  return 'days'
}

/** 后端是 **clamp**，不是回落缺省（:1439-1444）。 */
export function usageDaysEffective(days: number | undefined, def: number): number {
  if (days === undefined || !Number.isInteger(days)) return def
  if (days < USAGE_DAYS_MIN) return USAGE_DAYS_MIN
  if (days > USAGE_DAYS_MAX) return USAGE_DAYS_MAX
  return days
}

function usageRangeQuery(q: UsageRangeQuery | undefined, def: number): string {
  const parts: string[] = []
  if (usageTimeRangeMode(q) === 'custom') {
    // ★ 只给一个时后端直接 400，这里照样发出去让后端报错（不静默补齐）
    if (q?.start) parts.push(`start=${encodeURIComponent(q.start)}`)
    if (q?.end) parts.push(`end=${encodeURIComponent(q.end)}`)
  } else {
    parts.push(`days=${usageDaysEffective(q?.days, def)}`)
  }
  return `?${parts.join('&')}`
}

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/usage/cost-trend
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * admin/usage_enhanced.go:25-36。
 * ★ `dimension_value` 的来源列是 `COALESCE(…, 'unknown')`（:165）
 * ⇒ 分组值为空时**归到 `'unknown'`**，不是缺失。
 * ★ `percentage` 是 **0-100**（:205 `* 100.0`），分母为 0 时留 0。
 */
export interface CostTrendEntry {
  dimension_value: string
  request_count: number
  total_cost_usd: number
  input_cost_usd: number
  output_cost_usd: number
  prompt_tokens: number
  completion_tokens: number
  avg_latency_ms: number
  /** ★ 0-1 比例（:187 `1.0 - AVG(...)`），与 `percentage` 的单位**不同**。 */
  error_rate: number
  /** 0-100 百分数。 */
  percentage: number
}

/** admin/usage_enhanced.go:39-53。 */
export interface CostTrendResponse {
  group_by: string
  /** `startTime.Format("2006-01-02")` ⇒ **日期字符串**，不是时间戳。 */
  date_from: string
  date_to: string
  total_cost: number
  /**
   * ★★ **不是全集**：占比 <2% 且已有 10 条的条目被合并进 `other`（:262-267）。
   * ⇒ 「共 N 个维度」是错的说法，要说「列出 N 条 + 其他 M 条」。
   */
  entries: CostTrendEntry[]
  other_cost: number
  other_count: number
  /** ★ 恒发（不带 omitempty）。`false` = 服务端确认过它是好的。 */
  degraded: boolean
  /** ★ 仅降级时下发（omitempty）。 */
  degraded_reason?: string
}

// ★ `degraded` **不在必检键里**：它由下面的 `typeof !== 'boolean'` 单独把关，
//   而那条检查同时覆盖「缺键」与「类型错」两件事。
//   列进必检键是**冗余**（变异 #11 实测：删掉它用例照样全绿）。
export const COST_TREND_KEYS = [
  'group_by', 'date_from', 'date_to', 'total_cost',
  'entries', 'other_cost', 'other_count',
] as const
export const COST_TREND_ENTRY_KEYS = [
  'dimension_value', 'request_count', 'total_cost_usd', 'input_cost_usd',
  'output_cost_usd', 'prompt_tokens', 'completion_tokens',
  'avg_latency_ms', 'error_rate', 'percentage',
] as const

/**
 * admin/usage_enhanced.go:91-126 的五个合法维度。
 * ★ 非法值 ⇒ **400**（:142），不是静默回落 —— 与 `group_by` 缺省 `model` 不同。
 */
export const COST_TREND_GROUP_BYS = ['model', 'provider', 'intent', 'work_type', 'api_key'] as const
export type CostTrendGroupBy = (typeof COST_TREND_GROUP_BYS)[number]

/** ★ 走 `request_logs_with_current_month rl` 的两个维度（换基表）。 */
export const REQUEST_SIDE_GROUP_BYS: readonly CostTrendGroupBy[] = ['work_type', 'intent']

/** 缺省是 `model`（:136-138）。 */
export function costTrendGroupByDefault(): CostTrendGroupBy {
  return 'model'
}

export function costTrendGroupByValid(v: string): v is CostTrendGroupBy {
  return (COST_TREND_GROUP_BYS as readonly string[]).includes(v)
}

/** ★ 换到这两个维度时后端会**换基表**（`planCostTrend`），数字口径需重新交代。 */
export function costTrendSwitchesBaseTable(g: CostTrendGroupBy): boolean {
  return REQUEST_SIDE_GROUP_BYS.includes(g)
}

export interface CostTrendQuery extends UsageRangeQuery {
  /** 缺省 `model`；非法值 ⇒ 400。 */
  group_by?: CostTrendGroupBy
}

export function fetchCostTrend(
  q?: CostTrendQuery,
  options?: RequestOptions,
): Promise<CostTrendResponse> {
  const gb = q?.group_by ?? costTrendGroupByDefault()
  return req<unknown>(
    'GET',
    `/api/admin/usage/cost-trend${usageRangeQuery(q, COST_TREND_DAYS_DEFAULT)}&group_by=${gb}`,
    undefined,
    options,
  ).then(unwrapCostTrend)
}

export function unwrapCostTrend(resp: unknown): CostTrendResponse {
  const d = requireObject(resp, '成本趋势')
  requireKeys(d, COST_TREND_KEYS, '成本趋势')
  if (typeof d.degraded !== 'boolean') throw new Error('degraded 不是布尔值')
  const entries = requireArray(d.entries, '成本趋势 entries')
  entries.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`entries[${i}] 不是对象`)
    requireKeys(row, COST_TREND_ENTRY_KEYS, `entries[${i}]`)
  })
  return d as unknown as CostTrendResponse
}

/** ★ 降级时后端返回 200 + 空 entries + total 0，**必须**先读这个标志。 */
export function costTrendDegraded(m: CostTrendResponse): boolean {
  return m.degraded === true
}

/** ★ 恒发布尔，**不能**用「键缺失」判降级（与本仓 omitempty 族相反）。 */
export function costTrendDegradedReason(m: CostTrendResponse): string | null {
  return m.degraded_reason !== undefined && m.degraded_reason !== '' ? m.degraded_reason : null
}

/** ★★ 有条目被合并进「其他」⇒ `entries` **不是全集**。 */
export function costTrendHasMergedOthers(m: CostTrendResponse): boolean {
  return Number(m.other_count) > 0
}

/** ★ 分母为 0 时所有 `percentage` 都留 0（:205 的 CASE WHEN），无意义。 */
export function costTrendPercentMeaningless(m: CostTrendResponse): boolean {
  return Number(m.total_cost) === 0
}

/**
 * ★ `total_cost` 是**所有分组之和**（:259 在合并前累加），
 * ⇒ 应等于 `Σ entries.total_cost_usd + other_cost`。
 */
export function costTrendTotalDisagrees(m: CostTrendResponse): boolean {
  const listed = m.entries.reduce((a, e) => a + Number(e.total_cost_usd), 0)
  const other = Number(m.other_cost)
  return Math.abs(Number(m.total_cost) - (listed + other)) > 0.01
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/usage/period-compare
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * admin/usage_enhanced.go:292-313。
 * ★★ `unique_sessions` 已被**删除**（2026-10-03），注释写明理由：
 * 「一个无消费者的指标算不出来时，返回 0 与『真的是 0』在报告上无法区分」。
 * ⇒ 客户端**不要**去读这个键，它不存在。
 */
export interface PeriodStats {
  /** 原样回显请求里的周期串，不做规范化。 */
  period: string
  total_cost_usd: number
  total_requests: number
  total_tokens: number
  avg_cost_per_req: number
  unique_models: number
}

/** admin/usage_enhanced.go:340-345。 */
export interface DimChange {
  dimension_value: string
  current_cost: number
  previous_cost: number
  /**
   * ★ 分母 `previous_cost` 为 0 时留 **0**（:554-558 的 CASE WHEN）
   * ⇒ 「新出现的模型」change_pct 是 0，不是无穷大，也不是 null。
   */
  change_pct: number
}

/** admin/usage_enhanced.go:316-337。 */
export interface PeriodCompareResponse {
  current: PeriodStats
  previous: PeriodStats
  /** 分母 `previous.total_cost_usd > 0`，否则留 0（:421-423）。 */
  change_pct: number
  change_abs: number
  /** ★ 阈值是 **±5%**（:426-430），不是 ±1%。 */
  trend: 'up' | 'down' | 'flat'
  /** ★ 阈值是 **±20%**（:433-435）。 */
  significant: boolean
  /**
   * ★ 只在 `len(modelChanges) > 0` 时才放 `"model"` 键（:447-449）
   * ⇒ 查询失败时是 `{}`，与「查了但无变化」**同形**。
   * ★ 维度明细 SQL 硬编码 `LIMIT 10`（:563）。
   */
  by_dimension: Record<string, DimChange[]>
  /** ★ 恒发（不带 omitempty）。 */
  degraded: boolean
  degraded_reason?: string
}

// ★ 同上：`degraded` 由类型校验单独把关，不重复列入。
export const PERIOD_COMPARE_KEYS = [
  'current', 'previous', 'change_pct', 'change_abs',
  'trend', 'significant', 'by_dimension',
] as const
export const PERIOD_STATS_KEYS = [
  'period', 'total_cost_usd', 'total_requests',
  'total_tokens', 'avg_cost_per_req', 'unique_models',
] as const
export const DIM_CHANGE_KEYS = [
  'dimension_value', 'current_cost', 'previous_cost', 'change_pct',
] as const

/** admin/usage_enhanced.go:426/433 的阈值。 */
export const TREND_THRESHOLD_PCT = 5
export const SIGNIFICANT_THRESHOLD_PCT = 20

export interface PeriodCompareQuery {
  /** ★ **必填**，`YYYY-MM`；缺任一 ⇒ 400（:357-360）。 */
  current?: string
  previous?: string
}

export function fetchPeriodCompare(
  q: PeriodCompareQuery,
  options?: RequestOptions,
): Promise<PeriodCompareResponse> {
  const current = q.current ?? ''
  const previous = q.previous ?? ''
  return req<unknown>(
    'GET',
    `/api/admin/usage/period-compare?current=${encodeURIComponent(current)}&previous=${encodeURIComponent(previous)}`,
    undefined,
    options,
  ).then(unwrapPeriodCompare)
}

export function unwrapPeriodCompare(resp: unknown): PeriodCompareResponse {
  const d = requireObject(resp, '周期对比')
  requireKeys(d, PERIOD_COMPARE_KEYS, '周期对比')
  if (typeof d.degraded !== 'boolean') throw new Error('degraded 不是布尔值')
  for (const key of ['current', 'previous'] as const) {
    const v = d[key]
    if (!isPlainObject(v)) throw new Error(`${key} 不是对象`)
    requireKeys(v, PERIOD_STATS_KEYS, `周期对比 ${key}`)
  }
  const byDim = d.by_dimension
  if (!isPlainObject(byDim)) throw new Error('by_dimension 不是对象')
  for (const [dim, list] of Object.entries(byDim)) {
    const arr = requireArray(list, `by_dimension["${dim}"]`)
    arr.forEach((row, i) => {
      if (!isPlainObject(row)) throw new Error(`by_dimension["${dim}"][${i}] 不是对象`)
      requireKeys(row, DIM_CHANGE_KEYS, `by_dimension["${dim}"][${i}]`)
    })
  }
  return d as unknown as PeriodCompareResponse
}

export function periodCompareDegraded(m: PeriodCompareResponse): boolean {
  return m.degraded === true
}

export function periodCompareDegradedReason(m: PeriodCompareResponse): string | null {
  return m.degraded_reason !== undefined && m.degraded_reason !== '' ? m.degraded_reason : null
}

/** ★ 上期为 0 ⇒ `change_pct` 留 0（:421-423），**不是**无穷大也不是 null。 */
export function periodChangeMeaningless(m: PeriodCompareResponse): boolean {
  return Number(m.previous.total_cost_usd) === 0
}

/** ★★ 缺 `"model"` 键 ⇒ **查询失败或无变化，二者同形**（:443-449 只留 slog.Warn）。 */
export function periodByDimensionMissing(m: PeriodCompareResponse): boolean {
  return !('model' in m.by_dimension) || (m.by_dimension.model?.length ?? 0) === 0
}

/** ★ 维度明细硬编码 `LIMIT 10`（:563）⇒ 满 10 条即可能被截断。 */
export const PERIOD_DIMENSION_LIMIT = 10

export function periodDimensionTruncated(m: PeriodCompareResponse): boolean {
  return (m.by_dimension.model?.length ?? 0) >= PERIOD_DIMENSION_LIMIT
}

/** ★ `significant` 与 `trend` 是**两套阈值**（±20% / ±5%）⇒ 可能「up 但不 significant」。 */
export function trendWithoutSignificance(m: PeriodCompareResponse): boolean {
  return m.trend !== 'flat' && m.significant === false
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/admin/usage/cache-economics
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * admin/usage_enhanced.go:593-614。
 *
 * ★★ **单位混在一处**：`cache_hit_ratio` / `effective_cost_ratio` 是 **0-1 比例**，
 * 而 `savings_rate` 是 **0-100 百分数**（:755 `* 100.0`）。
 * ★★ 四个「节省」字段是**按硬编码假设推算**的（见文件头 (2)）⇒ 必须标「估算」。
 */
export interface CacheEconomicsResponse {
  date_from: string
  date_to: string
  total_requests: number
  cache_read_tokens: number
  prompt_tokens: number
  /** 0-1 比例。分母为 0 时留 0（:719-722）。 */
  cache_hit_ratio: number
  /** ★ 估算：`cacheRead × avgPrice × 0.9`。 */
  dollars_saved: number
  dollars_spent: number
  /** 0-1 比例。★ 分母为 0 时**留 1.0**（:746 初始化），不是 0。 */
  effective_cost_ratio: number
  /** ★★ 查询失败时也是 0（:694-697 静默）⇒ 与「真的没压缩」**不可分**。 */
  compressed_requests: number
  /** ★ 估算：`compressed × 8000 × avgPrice`。 */
  compression_saved: number
  /** ★ 估算：上面两项之和。 */
  total_saved: number
  /** ★ 估算：0-100 百分数。 */
  savings_rate: number
  /** ★ 恒发（不带 omitempty）。 */
  degraded: boolean
  degraded_reason?: string
}

export const CACHE_ECONOMICS_KEYS = [
  'date_from', 'date_to', 'total_requests', 'cache_read_tokens', 'prompt_tokens',
  'cache_hit_ratio', 'dollars_saved', 'dollars_spent', 'effective_cost_ratio',
  'compressed_requests', 'compression_saved', 'total_saved', 'savings_rate',
] as const

export function fetchCacheEconomics(
  q?: UsageRangeQuery,
  options?: RequestOptions,
): Promise<CacheEconomicsResponse> {
  return req<unknown>(
    'GET',
    `/api/admin/usage/cache-economics${usageRangeQuery(q, CACHE_ECONOMICS_DAYS_DEFAULT)}`,
    undefined,
    options,
  ).then(unwrapCacheEconomics)
}

export function unwrapCacheEconomics(resp: unknown): CacheEconomicsResponse {
  const d = requireObject(resp, '缓存经济')
  requireKeys(d, CACHE_ECONOMICS_KEYS, '缓存经济')
  if (typeof d.degraded !== 'boolean') throw new Error('degraded 不是布尔值')
  return d as unknown as CacheEconomicsResponse
}

export function cacheEconomicsDegraded(m: CacheEconomicsResponse): boolean {
  return m.degraded === true
}

export function cacheEconomicsDegradedReason(m: CacheEconomicsResponse): string | null {
  return m.degraded_reason !== undefined && m.degraded_reason !== '' ? m.degraded_reason : null
}

/** 分母 `cache_read + prompt` 为 0 ⇒ 那个 0 无意义（不是「命中率 0%」）。 */
export function cacheHitRatioMeaningless(m: CacheEconomicsResponse): boolean {
  return Number(m.cache_read_tokens) + Number(m.prompt_tokens) === 0
}

/** ★★ 没有 token 时 `effective_cost_ratio` 留 **1.0**（:746）⇒ 显示 100% 会误导。 */
export function effectiveCostRatioIsFakeFull(m: CacheEconomicsResponse): boolean {
  return Number(m.total_requests) === 0
}

/** ★ `compressed_requests === 0` ⇒ 分不清「没压缩」与「压缩计数查询失败」。 */
export function compressedCountMayBeFailed(m: CacheEconomicsResponse): boolean {
  return Number(m.compressed_requests) === 0
}

/** ★★ 压缩节省依赖一个可能静默失败的计数 ⇒ 它不可信时整条估算链都不可信。 */
export function compressionSavedUnreliable(m: CacheEconomicsResponse): boolean {
  return compressedCountMayBeFailed(m) || cacheEconomicsDegraded(m)
}

/* ── 内部工具 ──────────────────────────────────────────────────────────── */

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  if ('success' in resp && 'timestamp' in resp) {
    throw new Error(`${where} 拿到的是 dashboardapi 信封形状，本族应为裸 JSON`)
  }
  return resp
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}

function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}