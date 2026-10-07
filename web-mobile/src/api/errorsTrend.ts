import { req, type RequestOptions } from './client'

/**
 * errorsTrend.ts — 供应商错误趋势（2026-10-07，第七十九批）。
 *
 * GET /api/errors/trend
 *
 * - **注册**：`admin/handler.go:1249` 的 `admin(h.errorsTrendHandlers.getErrorsTrend)`
 *   ⇒ **admin 档**，tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
 * - **实现**：`admin/errors_trend.go`（350 行）。
 * - **桌面调用方**：`web/src/api/*.ts` 的 errors trend 面板。
 *
 * ## ★★★★★ 本族最要紧的八件事
 *
 * (1) ★★★★★ **两个数据源，`source` 告诉你是哪个。**
 *      | source | 读的是 | 何时被选中 |
 *      |---|---|---|
 *      | `stats` | `supplier_error_stats`（预聚合，分钟桶由后台聚合器每 5 分钟 UPSERT） | 该窗口该粒度**有行** |
 *      | `fallback` | `supplier_errors_unified`（明细） | stats 返回**零行**（`:122-133`） |
 *      ⇒ ★ `fallback` 只说明「**预聚合表在这个粒度上没有行**」，
 *        不一定是「没有错误」—— 粒度不匹配也会走到这里。
 *      ⇒ ★ 但 `fallback` + `time_series` 为空是**确定的**：
 *        明细表也没有行 ⇒ 窗口内确实没有错误（`fallback` 分支总是被真的执行一遍）。
 *
 * (2) ★★★★★ **读路径刻意绕过 RLS。**
 *      `withTrendReadTx`（`:171-196`）在只读事务里
 *      `set_config('app.bypass_rls','true',true)`（`is_local=true`）。
 *      注释自陈原因（`:163-170`）：`supplier_errors_hot` / `supplier_errors`
 *      是 **FORCE RLS + 租户隔离**，而网关应用角色**不是 superuser**，
 *      直连读会被**静默过滤到 0 行** ⇒「趋势数据闭环断裂」。
 *      ⇒ ★★ 于是**「0 行」本身就是一个被文档化的失败模式**，
 *        客户端看到的数字是 **bypass 之后**的，租户隔离靠 WHERE 参数而不是 RLS。
 *
 * (3) ★★★★★ `summary.unique_requests` 是**各桶相加**，不是去重计数。**
 *      `loadFromStats`（`:221-222`）/ `loadFromDetail`（`:263-264`）只做
 *      ```go
 *      resp.Summary.TotalErrors += p.ErrorCount
 *      resp.Summary.UniqueRequests += p.UniqueRequests
 *      ```
 *      ⇒ ★★ 一个跨桶的 request_id 会被数两次 ⇒ 汇总值**上界偏大**。
 *      ⇒ 这是「桶求和 ≠ 全局去重」与第七十六批 `summary` 三项之和的同类陷阱。
 *
 * (4) ★★★★ `by_supplier` / `by_error_type` 是 `map[string]int` + `omitempty`。**
 *      `:42-43` 两个键都带 omitempty，而 SQL 侧是
 *      `jsonb_object_agg(...) FILTER (WHERE supplier <> '')`
 *      ⇒ 全部被 FILTER 掉时聚合返回 NULL，扫描得到空字节，map 保持 nil
 *      ⇒ ★ **空 map 被整个键省略**（不是 `{}`，不是 `null`）——
 *        本仓第**九**种 nil 编码，也是**第一次出现 map 类型**。
 *
 * (5) ★★★★ `top_error_types` / `top_suppliers` 显式初始化成空数组。**
 *      `loadBreakdowns`（`:308-309`）：
 *      ```go
 *      resp.Summary.TopErrorTypes = []errorsTrendBreakdownRow{}
 *      resp.Summary.TopSuppliers = []errorsTrendBreakdownRow{}
 *      ```
 *      ⇒ ★ 恒数组，**不会是 null**（与第 (4) 条的 map 恰好相反，同一个响应里并存）。
 *      ⇒ ★★ 两者都被**截到 10 条**（`:319`/`:323`），**不回显被丢掉的数量**。
 *
 * (6) ★★★★ `hours` 是严格三值枚举，`granularity` 的缺省由它推导。**
 *      `parseVendorErrorHours`（`vendor_credential_error_handlers.go:182-191`）：
 *      只接受 `1 / 24 / 168`，其余（含 `0`、负数、`abc`）⇒ **400** `invalid_hours`。
 *      `granularity` 缺省（`:81-90`）：
 *      ```go
 *      case hours <= 1:  granularity = "minute"
 *      case hours <= 24: granularity = "hour"
 *      default:          granularity = "day"
 *      ```
 *      显式传值则严格校验（`:91-94`）⇒ **400** `invalid_granularity`。
 *      ⇒ ★ `hours=1 → minute`、`24 → hour`、`168 → day`。
 *
 * (7) ★★★ 错误信封是**嵌套**的，与 `writeError` 的扁平形不同。**
 *      `writeErrorWithCode`（`handler.go:1501-1508`）：
 *      ```go
 *      writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "detail": msg}})
 *      ```
 *      ⇒ 形状是 `{"error":{"code":"…","detail":"…"}}`。
 *      ⇒ 六个 code：`db_not_configured`（503，`database is not configured`——
 *      ★ 与别处 `database not configured` **措辞不同**）、
 *      `invalid_hours`、`invalid_granularity`、`invalid_credential_id`（三个 400）、
 *      `trend_stats_query_failed` / `trend_fallback_query_failed`（两个 500）。
 *
 * (8) ★★★ `supplier` / `error_type` 的 `'all'` 是魔法值。**
 *      三处 SQL 一致：
 *      ```sql
 *      AND ($3 = '' OR $3 = 'all' OR supplier = $3)
 AND ($4 = 0 OR credential_id = $4)
 AND ($5 = '' OR $5 = 'all' OR error_type = $5)
      ```
 *      ⇒ ★ 空串与字面量 `'all'` 都表示「不过滤」；
 *        而 `credential_id` 只能用 `0` 表示不过滤，
 *        但入口校验又要求它 `> 0`（`:97-103`）⇒ **一旦传了 credential_id 就一定是过滤**。
 */

/** `parseVendorErrorHours`（`:183-185`）的缺省。 */
export const ERRORS_TREND_DEFAULT_HOURS = 24
/** `:187` 的三值白名单。 */
export const ERRORS_TREND_HOURS = [1, 24, 168] as const
export type ErrorsTrendHours = (typeof ERRORS_TREND_HOURS)[number]

/** `:83-89` 的三档粒度。 */
export const ERRORS_TREND_GRANULARITIES = ['minute', 'hour', 'day'] as const
export type ErrorsTrendGranularity = (typeof ERRORS_TREND_GRANULARITIES)[number]

/** `loadFromStats:200` 与 `loadFromDetail:238` 两个字面量。 */
export const ERRORS_TREND_SOURCES = ['stats', 'fallback'] as const
export type ErrorsTrendSource = (typeof ERRORS_TREND_SOURCES)[number]

/** ★ `''` 与 `'all'` 都表示不过滤（见文件头第 (8) 条）。 */
export const ERRORS_TREND_FILTER_WILDCARDS = ['', 'all'] as const

/** `loadBreakdowns:319`/`:323` 的截断上限。 */
export const ERRORS_TREND_BREAKDOWN_LIMIT = 10
/** `:105` 的查询超时。 */
export const ERRORS_TREND_QUERY_TIMEOUT_MS = 8000

export const ERRORS_TREND_CODE_DB_NOT_CONFIGURED = 'db_not_configured'
export const ERRORS_TREND_CODE_INVALID_HOURS = 'invalid_hours'
export const ERRORS_TREND_CODE_INVALID_GRANULARITY = 'invalid_granularity'
export const ERRORS_TREND_CODE_INVALID_CREDENTIAL_ID = 'invalid_credential_id'
export const ERRORS_TREND_CODE_STATS_QUERY_FAILED = 'trend_stats_query_failed'
export const ERRORS_TREND_CODE_FALLBACK_QUERY_FAILED = 'trend_fallback_query_failed'
export const ERRORS_TREND_CODES = [
  ERRORS_TREND_CODE_DB_NOT_CONFIGURED,
  ERRORS_TREND_CODE_INVALID_HOURS,
  ERRORS_TREND_CODE_INVALID_GRANULARITY,
  ERRORS_TREND_CODE_INVALID_CREDENTIAL_ID,
  ERRORS_TREND_CODE_STATS_QUERY_FAILED,
  ERRORS_TREND_CODE_FALLBACK_QUERY_FAILED,
] as const

/** `:71` 的 503 文案 —— ★ 与别处的 `database not configured` 措辞不同。 */
export const ERRORS_TREND_DB_NOT_CONFIGURED_MESSAGE = 'database is not configured'
/** `:188` 的 400 文案。 */
export const ERRORS_TREND_BAD_HOURS_MESSAGE = 'hours must be 1, 24, or 168'
/** `:92` 的 400 文案。 */
export const ERRORS_TREND_BAD_GRANULARITY_MESSAGE = 'granularity must be minute, hour, or day'
/** `:100` 的 400 文案。 */
export const ERRORS_TREND_BAD_CREDENTIAL_ID_MESSAGE = 'credential_id must be a positive integer'
export const ERRORS_TREND_STATS_FAILED_MESSAGE = 'failed to load error trend'
export const ERRORS_TREND_FALLBACK_FAILED_MESSAGE = 'failed to load error trend'

/** 趋势点（`:37-44`）：四个恒在键 + 两个 **map + omitempty** 条件键。 */
export const ERRORS_TREND_POINT_ALWAYS_KEYS = [
  'timestamp',
  'error_count',
  'unique_requests',
  'affected_users',
] as const
export const ERRORS_TREND_POINT_OPTIONAL_KEYS = ['by_supplier', 'by_error_type'] as const

/** summary（`:51-57`）：五个键**全部无 omitempty**。 */
export const ERRORS_TREND_SUMMARY_KEYS = [
  'total_errors',
  'unique_requests',
  'top_error_types',
  'top_suppliers',
  'affected_credentials',
] as const

/** 响应顶层七键（`:59-67`）。 */
export const ERRORS_TREND_KEYS = [
  'source',
  'granularity',
  'hours',
  'since',
  'until',
  'time_series',
  'summary',
] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

export interface ErrorsTrendPoint {
  timestamp: string
  error_count: number
  unique_requests: number
  affected_users: number
  /** ★ 空 map 时**整个键省略**（map + omitempty，文件头第 (4) 条）。 */
  by_supplier?: Record<string, number>
  by_error_type?: Record<string, number>
}

export interface ErrorsTrendBreakdownRow {
  key: string
  count: number
}

export interface ErrorsTrendSummary {
  total_errors: number
  /** ★★ 各桶**相加**，不是去重（文件头第 (3) 条）。 */
  unique_requests: number
  /** ★ 恒数组，≤10（文件头第 (5) 条）。 */
  top_error_types: ErrorsTrendBreakdownRow[]
  /** ★ 恒数组，≤10。 */
  top_suppliers: ErrorsTrendBreakdownRow[]
  affected_credentials: number
}

export interface ErrorsTrendResponse {
  source: ErrorsTrendSource
  granularity: ErrorsTrendGranularity
  hours: number
  /** ★ 服务端算的时间边界（`:107-108`），客户端回显即可。 */
  since: string
  until: string
  /** ★ 恒数组（两条 load 函数都显式初始化）。 */
  time_series: ErrorsTrendPoint[]
  summary: ErrorsTrendSummary
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

export interface ErrorsTrendParams {
  hours?: number
  granularity?: string
  supplier?: string
  credentialId?: number
  errorType?: string
}

export function fetchErrorsTrend(
  params?: ErrorsTrendParams,
  options?: RequestOptions,
): Promise<ErrorsTrendResponse> {
  const qs = new URLSearchParams()
  const p = params ?? {}
  if (p.hours != null) qs.set('hours', String(p.hours))
  if (p.granularity) qs.set('granularity', p.granularity)
  // ★ `''` 与 `'all'` 都是「不过滤」的合法写法 ⇒ 空串也照发，由后端判定。
  if (p.supplier != null) qs.set('supplier', p.supplier)
  // ★ credentialId 传了就一定是过滤：后端要求 > 0，0 只在内部表示不过滤。
  if (p.credentialId != null) qs.set('credential_id', String(p.credentialId))
  if (p.errorType != null) qs.set('error_type', p.errorType)
  const q = qs.toString()
  return req<unknown>(
    'GET',
    `/api/errors/trend${q ? `?${q}` : ''}`,
    undefined,
    options,
  ).then(unwrapErrorsTrend)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapErrorsTrend(resp: unknown): ErrorsTrendResponse {
  const d = requireObject(resp, '错误趋势')
  requireKeys(d, ERRORS_TREND_KEYS, '错误趋势')
  // ★ 两个都是两个字面量决定的封闭枚举 ⇒ 可以校验取值
  if (typeof d.source !== 'string') throw new Error('错误趋势 的 source 不是字符串')
  if (!(ERRORS_TREND_SOURCES as readonly string[]).includes(d.source)) {
    throw new Error(`错误趋势 的 source 不是已知来源（${String(d.source)}）`)
  }
  if (typeof d.granularity !== 'string') throw new Error('错误趋势 的 granularity 不是字符串')
  if (!(ERRORS_TREND_GRANULARITIES as readonly string[]).includes(d.granularity)) {
    throw new Error(`错误趋势 的 granularity 不是已知粒度（${String(d.granularity)}）`)
  }
  if (typeof d.hours !== 'number') throw new Error('错误趋势 的 hours 不是数字')
  for (const k of ['since', 'until'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`错误趋势 的 ${k} 不是字符串`)
  }
  if (!Array.isArray(d.time_series)) throw new Error('错误趋势 的 time_series 不是数组')
  d.time_series.forEach((p, i) => requirePoint(p, `错误趋势 的 time_series[${i}]`))
  requireSummary(d.summary)
  return d as unknown as ErrorsTrendResponse
}

function requirePoint(v: unknown, where: string): ErrorsTrendPoint {
  const d = requireObject(v, where)
  requireKeys(d, ERRORS_TREND_POINT_ALWAYS_KEYS, where)
  for (const k of [
    'error_count',
    'unique_requests',
    'affected_users',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  if (typeof d.timestamp !== 'string') throw new Error(`${where} 的 timestamp 不是字符串`)
  // ★ 两个 map 条件键：键在时必须是「字符串 → 数字」的对象；键不在是正常的（空 map 被省略）
  for (const k of ERRORS_TREND_POINT_OPTIONAL_KEYS) {
    if (!(k in d)) continue
    const m = d[k]
    if (!isPlainObject(m)) throw new Error(`${where} 的 ${k} 不是对象`)
    for (const [kk, vv] of Object.entries(m)) {
      if (typeof vv !== 'number') throw new Error(`${where} 的 ${k}.${kk} 不是数字`)
    }
  }
  return d as unknown as ErrorsTrendPoint
}

function requireSummary(v: unknown): void {
  const s = requireObject(v, '错误趋势 的 summary')
  requireKeys(s, ERRORS_TREND_SUMMARY_KEYS, '错误趋势 的 summary')
  for (const k of [
    'total_errors',
    'unique_requests',
    'affected_credentials',
  ] as const) {
    if (typeof s[k] !== 'number') throw new Error(`错误趋势.summary 的 ${k} 不是数字`)
  }
  for (const k of ['top_error_types', 'top_suppliers'] as const) {
    if (!Array.isArray(s[k])) throw new Error(`错误趋势.summary 的 ${k} 不是数组`)
    s[k].forEach((r, i) => {
      const o = requireObject(r, `错误趋势.summary.${k}[${i}]`)
      requireKeys(o, ['key', 'count'], `错误趋势.summary.${k}[${i}]`)
      if (typeof o.key !== 'string') {
        throw new Error(`错误趋势.summary.${k}[${i}].key 不是字符串`)
      }
      if (typeof o.count !== 'number') {
        throw new Error(`错误趋势.summary.${k}[${i}].count 不是数字`)
      }
    })
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * ★ `granularity` 的缺省推导（`:81-90`）的客户端复刻。
 * 客户端可以核对服务端回显的 `granularity` 是否符合「没显式指定时的值」。
 */
export function errorsTrendDefaultGranularity(hours: number): ErrorsTrendGranularity {
  if (hours <= 1) return 'minute'
  if (hours <= 24) return 'hour'
  return 'day'
}

/** ★ 显式传了 granularity 时，缺省推导**不适用** ⇒ 这个判定恒假。 */
export function errorsTrendGranularityIsDefault(
  r: ErrorsTrendResponse,
  granularityWasSent: boolean,
): boolean {
  return !granularityWasSent && r.granularity === errorsTrendDefaultGranularity(r.hours)
}

/**
 * ★★ `source === 'fallback'` 只说明「预聚合表在这个粒度上没有行」，
 * 既可能是真的没错误，也可能是**粒度不匹配**（聚合器只写了别的粒度）。
 */
export function errorsTrendPreAggregationMissed(r: ErrorsTrendResponse): boolean {
  return r.source === 'fallback'
}

/**
 * ★★ `source === 'fallback'` **且** `time_series` 为空是**确定的**：
 * fallback 分支总是被真的执行一遍，明细表也为空 ⇒ 窗口内确实没有错误。
 */
export function errorsTrendWindowIsGenuinelyEmpty(r: ErrorsTrendResponse): boolean {
  return r.source === 'fallback' && r.time_series.length === 0
}

/** ★ 数据来自后台聚合器（stats 源）还是明细兜底。 */
export function errorsTrendSourceIsAggregated(r: ErrorsTrendResponse): boolean {
  return r.source === 'stats'
}

/**
 * ★★ `summary.unique_requests` 是桶求和，**不是**去重计数
 * （文件头第 (3) 条）⇒ 它是一个**上界**。
 */
export function errorsTrendUniqueRequestsIsUpperBound(r: ErrorsTrendResponse): boolean {
  return r.summary.unique_requests >= sumSeriesField(r, 'unique_requests')
}

/** ★ 汇总值与序列求和是否自洽（后端就是这么算的，客户端可以核对）。 */
export function errorsTrendSummaryMatchesSeries(r: ErrorsTrendResponse): boolean {
  return r.summary.total_errors === sumSeriesField(r, 'error_count')
}

/** ★★ 两个 breakdown 都被截到 10 条 ⇒ 满 10 条时**不知道还剩多少**。 */
export function errorsTrendBreakdownIsTruncated(r: ErrorsTrendResponse): boolean {
  return (
    r.summary.top_error_types.length >= ERRORS_TREND_BREAKDOWN_LIMIT ||
    r.summary.top_suppliers.length >= ERRORS_TREND_BREAKDOWN_LIMIT
  )
}

/** ★ 地图维度缺失 ⇔ 那一列全被 `FILTER (WHERE supplier <> '')` 滤掉或为空 map。 */
export function errorsTrendPointHasSupplierDim(p: ErrorsTrendPoint): boolean {
  return p.by_supplier !== undefined && Object.keys(p.by_supplier).length > 0
}

/** ★ 错误类型维度缺失同理。 */
export function errorsTrendPointHasErrorTypeDim(p: ErrorsTrendPoint): boolean {
  return p.by_error_type !== undefined && Object.keys(p.by_error_type).length > 0
}

/** ★ `''` 与 `'all'` 都是不过滤的通配值。 */
export function errorsTrendFilterIsWildcard(v: string): boolean {
  return (ERRORS_TREND_FILTER_WILDCARDS as readonly string[]).includes(v)
}

/** ★ 序列是否按时间升序（两条 load 都是 `ORDER BY bucket/stat_time`）。 */
export function errorsTrendSeriesIsAscending(r: ErrorsTrendResponse): boolean {
  for (let i = 1; i < r.time_series.length; i++) {
    if (Date.parse(r.time_series[i]!.timestamp) < Date.parse(r.time_series[i - 1]!.timestamp)) {
      return false
    }
  }
  return true
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 内部工具
 * ═══════════════════════════════════════════════════════════════════════════ */

function sumSeriesField(
  r: ErrorsTrendResponse,
  field: 'error_count' | 'unique_requests',
): number {
  return r.time_series.reduce((acc, p) => acc + p[field], 0)
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
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