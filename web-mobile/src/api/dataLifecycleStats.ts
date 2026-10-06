import { req, type RequestOptions } from './client'

/**
 * dataLifecycleStats.ts — 数据生命周期面的四条**只读**端点
 * （2026-10-08，第六十六批）。
 *
 * GET /api/admin/data-lifecycle/stats       （handler.go:960，admin 档）
 * GET /api/admin/data-lifecycle/metrics     （handler.go:962，admin 档）
 * GET /api/admin/data-lifecycle/jobs        （handler.go:979，admin 档）
 * GET /api/admin/data-lifecycle/blobs/top   （handler.go:994，admin 档）
 *
 * ★ 与既有 `api/dataLifecycle.ts` **不重叠**：那个模块只覆盖
 * `storage/tables` 与 `partitions` 两条，本模块是另外四条。
 *
 * ## 不碰的写操作
 * `POST /data-lifecycle/cleanup/preview`（虽叫 preview，body 里带
 * `action ∈ {trim, archive, delete}` 且走 POST）、`POST /blobs/cleanup/*`、
 * `/partitions/*`、`/hot/promote` 全部不碰。同族里
 * `partitions/archive` 起、`hot/*`、`storage/tables/vacuum*|reindex` 是
 * `h.superAdmin` 档（handler.go:963-978），而本模块这四条是 `admin` 档 ——
 * **同前缀下混着两档，不能按前缀判权限**。
 *
 * ## ★★★★★ 本族最要紧的五件事
 *
 * (1) ★★★★ **`metrics` 完全不做租户隔离，而注册是 `admin` 档。**
 *      `data_lifecycle_metrics.go:3-11` 的文件头注释写着
 *      「Currently the endpoint is super-admin only and the SQL is left unscoped」，
 *      但注册处是 `admin(...)`（handler.go:962）⇒ **tenant_admin 实际能调**，
 *      而 SQL 是 `FROM request_logs` **无 WHERE**（:63）
 *      ⇒ 它讲的是**整表**，不是本租户。
 *      与此同时 `stats`（:57-63）和 `blobs/top`（:91-97）都做了
 *      `IsTenantAdmin(r)` 判别 ⇒ **同族隔离口径不一致**。
 *      ⚠️ 注释与注册矛盾时**以注册为准**。
 *
 * (2) ★★★★ **`stats` 的四个分段是指针**（`*dataSegment`），可为 `null`：
 *      逐行 `Scan` 出错走 `warnRowSkip(...)` 后 `continue`
 *      （data_lifecycle.go:146-149）⇒ **该段整个是 `null`**，不是 0。
 *      ⇒ 缺段必须渲染成「查不出来」，不能渲染成「0 行」。
 *
 * (3) ★★★★ **`stats.total_rows` 是租户口径，`total_size_bytes` 是全表口径。**
 *      `total_rows` 走 `COUNT(*) … WHERE 1=1` + `tenantFilter`（:75-80），
 *      而 `total_size_bytes` 是 `pg_total_relation_size('request_logs')`
 *      ——**整张表的物理大小，不带任何过滤**（:76）。
 *      ⇒ 同一个对象里混了两种口径，**不能**并排写成
 *      「本租户 X 行 / Y 字节」。
 *      同理每段 / 每租户的 `size_bytes` 是
 *      `pg_total_relation_size('request_logs') * rows / total_count`
 *      （:105-106 等）——**按行数摊派出来的估算值，含索引，不是实测大小**。
 *
 * (4) ★★★ **一个端点里并存三种「查不出来」的编码。**
 *      - 总量查询失败 ⇒ **500**（:81-85），整条挂
 *      - 分段查询失败 ⇒ **500**（:137-141）；分段行 `Scan` 失败 ⇒ 该段 `null`
 *      - `by_tenant` / `growth_trend` 查询失败 ⇒ **静默 `[]`**（:197-201、
 *        :262-266，注释明写 "non-fatal, continue"），只有一行 `slog.Warn`
 *      ⇒ **`[]` 与「真的没有数据」不可分**，UI 不能说「无数据」。
 *
 * (5) ★★★ **`metrics` 与 `stats` 的 `total_rows` 覆盖的行集不同。**
 *      `metrics` 读裸表 `request_logs`（:63），`stats` 读视图
 *      `request_logs_with_current_month`（:78）——后者按名字与
 *      `sql/objects/views/request_logs_with_current_month.sql` 是**当月**口径。
 *      ⇒ **同名 `total_rows` 不可相减、不可并排比较。**
 *      本模块刻意**不提供**任何跨端点合并/对比的函数。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/data-lifecycle/stats
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * admin/data_lifecycle.go:25-31。
 *
 * ⚠️ `size_bytes` / `size_human` 是**按行数摊派**的估算
 * （`pg_total_relation_size('request_logs') * rows / total_count`），
 * 含索引，**不是**这几行实测占的字节。
 */
export interface DataSegment {
  rows: number
  size_bytes: number
  size_human: string
  /** 后端在 :163-176 按 segment 名写死，见 SEGMENT_DAYS。 */
  days: number
  /**
   * ★ 0-100 的百分数（:153 `rows/total*100`），不是 0-1 的比例。
   * ★ `total_rows === 0` 时后端留 0.0，与「真的是 0%」不可分。
   */
  percent_of_total: number
}

/** admin/data_lifecycle.go:33-38。★ `tenant_id` 可能是 `COALESCE(..., 'default')` 的 'default'（:187）。 */
export interface TenantDataStats {
  tenant_id: string
  rows: number
  size_bytes: number
  size_human: string
}

/**
 * admin/data_lifecycle.go:40-45。
 * ★ `compression_rate` 在 :280-282 被**夹到 100**，
 * 且 `requests === 0` 时留 0.0（:276）。
 */
export interface DailyGrowth {
  date: string
  requests: number
  compressed: number
  compression_rate: number
}

/** admin/data_lifecycle.go:13-23。★ 四个分段是**指针**，可为 null。 */
export interface DataLifecycleStats {
  /** ★ 租户口径（:75-79 带 tenantFilter）。 */
  total_rows: number
  /** ★★ **全表口径**：`pg_total_relation_size('request_logs')`，不带任何过滤（:76）。 */
  total_size_bytes: number
  total_size_human: string
  /** 0-7 天（SQL 是 `ts > NOW() - 7 days`，`days` 写死 7）。 */
  hot_data: DataSegment | null
  /** 7-30 天（`ts BETWEEN 30d AND 7d`，`days` 写死 **23**，不是 30）。 */
  warm_data: DataSegment | null
  /** 30-90 天（`days` 写死 **60**）。 */
  cold_data: DataSegment | null
  /** >90 天（`days` 写死 **999**，不是 91 也不是 90）。 */
  expired_data: DataSegment | null
  by_tenant: TenantDataStats[]
  /** ★ 最多 7 天，且**新的一天在前**（:245/:258 `ORDER BY day DESC LIMIT 7`）。 */
  growth_trend: DailyGrowth[]
}

export const DATA_LIFECYCLE_STATS_KEYS = [
  'total_rows', 'total_size_bytes', 'total_size_human',
  'hot_data', 'warm_data', 'cold_data', 'expired_data',
  'by_tenant', 'growth_trend',
] as const
export const DATA_SEGMENT_KEYS = ['rows', 'size_bytes', 'size_human', 'days', 'percent_of_total'] as const
export const TENANT_DATA_STATS_KEYS = ['tenant_id', 'rows', 'size_bytes', 'size_human'] as const
export const DAILY_GROWTH_KEYS = ['date', 'requests', 'compressed', 'compression_rate'] as const

const SEGMENT_KEYS = ['hot_data', 'warm_data', 'cold_data', 'expired_data'] as const
export type SegmentKey = (typeof SEGMENT_KEYS)[number]

/** data_lifecycle.go:165/168/171/174 —— `days` 是**标注口径**后端写死的，不是区间上界。 */
export const SEGMENT_DAYS: Record<SegmentKey, number> = {
  hot_data: 7,
  warm_data: 23,
  cold_data: 60,
  expired_data: 999,
}

/** data_lifecycle.go:195 `LIMIT 10`。 */
export const BY_TENANT_LIMIT = 10
/** data_lifecycle.go:246 / :259 `LIMIT 7`。 */
export const GROWTH_TREND_DAYS = 7

export function fetchDataLifecycleStats(options?: RequestOptions): Promise<DataLifecycleStats> {
  return req<unknown>('GET', '/api/admin/data-lifecycle/stats', undefined, options)
    .then(unwrapDataLifecycleStats)
}

export function unwrapDataLifecycleStats(resp: unknown): DataLifecycleStats {
  const d = requireObject(resp, '数据生命周期统计')
  requireKeys(d, DATA_LIFECYCLE_STATS_KEYS, '数据生命周期统计')
  for (const key of SEGMENT_KEYS) {
    const seg = d[key]
    // ★ null 是合法值（该段行被 warnRowSkip 跳过）；非 null 时必须是完整对象
    if (seg === null || seg === undefined) continue
    if (!isPlainObject(seg)) throw new Error(`数据生命周期统计 ${key} 不是对象也不是 null`)
    requireKeys(seg, DATA_SEGMENT_KEYS, `数据生命周期统计 ${key}`)
  }
  const byTenant = requireArray(d.by_tenant, 'by_tenant')
  byTenant.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`by_tenant[${i}] 不是对象`)
    requireKeys(row, TENANT_DATA_STATS_KEYS, `by_tenant[${i}]`)
  })
  const trend = requireArray(d.growth_trend, 'growth_trend')
  trend.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`growth_trend[${i}] 不是对象`)
    requireKeys(row, DAILY_GROWTH_KEYS, `growth_trend[${i}]`)
  })
  return d as unknown as DataLifecycleStats
}

/** ★★ 某段是 `null` ⇒ **该段查不出来**（不是「0 行」）。 */
export function segmentUnavailable(s: DataLifecycleStats, key: SegmentKey): boolean {
  return s[key] === null || s[key] === undefined
}

/** ★ 至少一段缺失 ⇒ 四段之和不能代表总量，也不能说「占比合计 100%」。 */
export function anySegmentUnavailable(s: DataLifecycleStats): boolean {
  return SEGMENT_KEYS.some((k) => segmentUnavailable(s, k))
}

/** ★ 实际可展示的分段（供 UI 决定要画几根柱子）。 */
export function availableSegments(s: DataLifecycleStats): SegmentKey[] {
  return SEGMENT_KEYS.filter((k) => !segmentUnavailable(s, k))
}

/** ★ 占比合计：四段齐全时才有意义。缺段返回 `null`，不给残缺的百分数。 */
export function segmentPercentTotal(s: DataLifecycleStats): number | null {
  if (anySegmentUnavailable(s)) return null
  return SEGMENT_KEYS.reduce((acc, k) => acc + (s[k] as DataSegment).percent_of_total, 0)
}

/** ★ `percent_of_total` 的分母是 `total_rows`，为 0 时四个占比都留 0（:151-154）。 */
export function percentMeaningless(s: DataLifecycleStats): boolean {
  return s.total_rows === 0
}

/**
 * ★ 四段行数之和**超过** `total_rows`。
 *
 * 后端 SQL 的 30 天边界是**双侧闭区间**
 * （`warm`: `BETWEEN 30d AND 7d`，`cold`: `BETWEEN 90d AND 30d`，
 *  data_lifecycle.go:116/124），落在 `NOW()-30d` 那一瞬间的行**会被数两次**
 * （Postgres 的 `NOW()` 在一个语句内是同一个事务时间，两侧都含端点）。
 * ⇒ 这个差值不是「不可能」，看到它要按「边界重复计数」解读，
 * 而不是按「数据错了」解读。
 */
export function segmentRowsDisagree(s: DataLifecycleStats): boolean {
  if (anySegmentUnavailable(s)) return false
  const sum = SEGMENT_KEYS.reduce((acc, k) => acc + Number((s[k] as DataSegment).rows), 0)
  return sum > Number(s.total_rows)
}

/**
 * ★★ `by_tenant` 撞到后端 `LIMIT 10` ⇒ 名单**被截断**，
 * 不能说「共 N 个租户」。
 */
export function byTenantTruncated(s: DataLifecycleStats): boolean {
  return s.by_tenant.length >= BY_TENANT_LIMIT
}

/** ★★ `growth_trend` 撞到后端 `LIMIT 7` ⇒ 天数**被截断**。 */
export function growthTrendTruncated(s: DataLifecycleStats): boolean {
  return s.growth_trend.length >= GROWTH_TREND_DAYS
}

/**
 * ★ 后端是 `ORDER BY day DESC`（:245/:258）⇒ **新的一天在前**。
 * 折线图若按返回序直接连线，画出来是**倒着走**的。
 */
export function growthTrendIsNewestFirst(s: DataLifecycleStats): boolean {
  const rows = s.growth_trend
  for (let i = 1; i < rows.length; i += 1) {
    const cur = rows[i]!
    const prev = rows[i - 1]!
    if (cur.date > prev.date) return false
  }
  return true
}

/**
 * ★★★ **`[]` 是二义的**，不能直接渲染成「无数据」。
 *
 * `by_tenant` / `growth_trend` 的查询失败是**非致命**的
 * （:197-201 / :262-266 注释明写 "non-fatal, continue"），
 * 失败时后端仍把预置的空切片编进响应 ⇒ 客户端拿到的 `[]`
 * 与「查询成功且确实没有记录」**完全一样**。
 * ⇒ UI 措辞只能是「没有可展示的记录」，不能断言「无数据」。
 */
export function listEmptyIsAmbiguous(s: DataLifecycleStats): boolean {
  return s.by_tenant.length === 0 || s.growth_trend.length === 0
}

/** ★ `compression_rate` 被后端夹到 100（:280-282），出现 100 即说明被夹过。 */
export function compressionRateSaturated(d: DailyGrowth): boolean {
  return d.compression_rate >= 100 && d.compressed > d.requests
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/data-lifecycle/metrics
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * data_lifecycle_metrics.go:22-35。
 *
 * ★★ **`last_cleanup_at` / `last_archive_at` 是死字段。**
 * 二者在结构体里声明（:33-34，`*string` + omitempty），
 * 但 `handleDataLifecycleMetrics`（:39-86）**从头到尾没有给它们赋值** ——
 * 全仓 grep 也只有这两行声明、零个赋值点。
 * ⇒ 这两个键**永远不存在**，无论清理/归档是否真的发生过。
 * ⇒ 键缺失**不能**说成「从未清理过」；只能说「本端点不提供这个时间」。
 * 类型保留为可选，是为了将来后端真填上时客户端不必改。
 */
export interface DataLifecycleMetrics {
  /** ★★ **全表口径**：`FROM request_logs`（:63），无 WHERE，不做租户隔离。 */
  total_rows: number
  total_size_bytes: number
  hot_data_rows: number
  hot_data_size_bytes: number
  warm_data_rows: number
  warm_data_size_bytes: number
  cold_data_rows: number
  cold_data_size_bytes: number
  expired_data_rows: number
  expired_data_size_bytes: number
  /** ★ 恒不存在（后端从不赋值），保留仅为前向兼容。 */
  last_cleanup_at?: string
  /** ★ 恒不存在（同上）。 */
  last_archive_at?: string
}

/** ★ 只有这十个键是必有的；末尾两个时间键**不在必检列表**里。 */
export const DATA_LIFECYCLE_METRICS_KEYS = [
  'total_rows', 'total_size_bytes',
  'hot_data_rows', 'hot_data_size_bytes',
  'warm_data_rows', 'warm_data_size_bytes',
  'cold_data_rows', 'cold_data_size_bytes',
  'expired_data_rows', 'expired_data_size_bytes',
] as const

export function fetchDataLifecycleMetrics(options?: RequestOptions): Promise<DataLifecycleMetrics> {
  return req<unknown>('GET', '/api/admin/data-lifecycle/metrics', undefined, options)
    .then(unwrapDataLifecycleMetrics)
}

export function unwrapDataLifecycleMetrics(resp: unknown): DataLifecycleMetrics {
  const d = requireObject(resp, '数据生命周期指标')
  requireKeys(d, DATA_LIFECYCLE_METRICS_KEYS, '数据生命周期指标')
  return d as unknown as DataLifecycleMetrics
}

/**
 * ★★ 后端**从不赋值** `last_cleanup_at` ⇒ 真实响应里这个键恒不存在。
 *
 * 这是**前向兼容开关**，不是数据判断：今天恒为 `true`，
 * 将来后端真填上时 UI 就能切到「显示时间」的措辞。
 * ⚠️ UI **不可**据此说「从未清理过」—— 清理可能早就跑过了，
 * 只是这个端点不报。
 */
export function metricsCleanupTimeAbsent(m: DataLifecycleMetrics): boolean {
  return !('last_cleanup_at' in m)
}

/** ★★ 同 `metricsCleanupTimeAbsent`，`last_archive_at` 亦恒不存在。 */
export function metricsArchiveTimeAbsent(m: DataLifecycleMetrics): boolean {
  return !('last_archive_at' in m)
}

/**
 * ★ 四段行数之和 > `total_rows` ⇒ 计数器矛盾。
 * 边界成因与 `segmentRowsDisagree` 同源（30 天双侧闭区间）。
 */
export function metricsRowsDisagree(m: DataLifecycleMetrics): boolean {
  return (
    m.hot_data_rows + m.warm_data_rows + m.cold_data_rows + m.expired_data_rows >
    m.total_rows
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/admin/data-lifecycle/jobs
 * ═══════════════════════════════════════════════════════════════════════════ */

/** data_lifecycle_jobs.go:56-62。 */
export interface JobProgress {
  done: number
  total: number
  /** ★ `float64` 零值：`total <= 0` 时后端留 0（:127-130）。 */
  percent: number
  batches: number
  message?: string
}

/**
 * data_lifecycle_jobs.go:39-54。除 `run_id`/`op`/`status`/`duration_ms`
 * 外**十个字段都带 omitempty** ⇒ 键会缺失，不是 null。
 */
export interface JobRun {
  run_id: string
  /** JobType：`promote_hot` | `drop_partition` | `vacuum` | `vacuum_full` | `reindex`（:21-27）。 */
  op: string
  /** JobStatus：`queued` | `running` | `succeeded` | `failed` | `cancelled`（:31-37）。 */
  status: string
  params?: Record<string, unknown>
  progress?: JobProgress
  result?: Record<string, unknown>
  error?: string
  message?: string
  started_at?: string
  heartbeat_at?: string
  /** ★ 运行中/排队中没有这一键（指针 + omitempty）。 */
  finished_at?: string
  /** ★ 恒存在（无 omitempty），未结束时是 0。 */
  duration_ms: number
  operator?: string
}

/**
 * 手写 `map[string]any`（data_lifecycle_jobs.go:324-327），两键。
 * ★ `running` 空时是 **`null`**，`history` 空时是 **`[]`** —— 原因见 `unwrapLifecycleJobs`。
 */
export interface LifecycleJobsResponse {
  running: JobRun[]
  history: JobRun[]
}

export const LIFECYCLE_JOBS_KEYS = ['running', 'history'] as const
export const JOB_RUN_REQUIRED_KEYS = ['run_id', 'op', 'status', 'duration_ms'] as const
export const JOB_PROGRESS_KEYS = ['done', 'total', 'percent', 'batches'] as const

/**
 * ★★ `listJobs` 被**硬编码传 50**（data_lifecycle_jobs.go:322），
 * 而 `listJobs` 内部又把 limit 钳到 `registry.maxKeep`（:244-246）
 * ⇒ 前端传任何 `limit` 都被忽略，且上限恒为 50。
 */
export const LIFECYCLE_JOBS_HISTORY_LIMIT = 50

/**
 * ★★ 两个键的**空态编码不同**，这是 Go 切片零值造成的：
 * - `running`：`var running []*JobRun`（:237）是 **nil 切片**，
 *   没有任务时 `append` 一次都不执行 ⇒ `json.Encode(nil 切片)` ⇒ **`null`**。
 *   而「刚重启、一个任务都没起过」是常态 ⇒ **`running: null` 极常见**。
 * - `history`：`hs := make([]*JobRun, 0, …)`（:247）**非 nil** ⇒ 恒为 `[]`。
 *
 * ⇒ `null` 在这里是**合法值**（归一为 `[]`），
 * 但非 null 且非数组仍然是形状错误（抛错）。
 */
export function unwrapLifecycleJobs(resp: unknown): LifecycleJobsResponse {
  const d = requireObject(resp, '生命周期任务')
  requireKeys(d, LIFECYCLE_JOBS_KEYS, '生命周期任务')
  for (const key of ['running', 'history'] as const) {
    const rows = nullableArray(d[key], key)
    rows.forEach((row, i) => {
      if (!isPlainObject(row)) throw new Error(`${key}[${i}] 不是对象`)
      requireKeys(row, JOB_RUN_REQUIRED_KEYS, `${key}[${i}]`)
    })
    if (d[key] === null) d[key] = rows
  }
  return d as unknown as LifecycleJobsResponse
}

export function fetchLifecycleJobs(options?: RequestOptions): Promise<LifecycleJobsResponse> {
  // ★ 刻意**不发** limit：后端忽略它，发了会让人以为生效了
  return req<unknown>('GET', '/api/admin/data-lifecycle/jobs', undefined, options)
    .then(unwrapLifecycleJobs)
}

/** ★ `running` 列表里的任务没有 `finished_at`；`history` 里的都有（:208 在入历史前必赋值）。 */
export function jobRunning(j: JobRun): boolean {
  return !('finished_at' in j)
}

/** ★ 失败任务带非空 `error`（:180）；成功的不带这一键。 */
export function jobFailed(j: JobRun): boolean {
  return 'error' in j && j.error !== ''
}

/** ★ 进度分母 <= 0 ⇒ `percent` 无意义（不是「完成 0%」）。 */
export function jobProgressMeaningless(j: JobRun): boolean {
  return j.progress === undefined || j.progress.total === 0
}

/* ═══════════════════════════════════════════════════════════════════════════
 * D. GET /api/admin/data-lifecycle/blobs/top
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * data_lifecycle_blobs.go:32-42。
 * ★ `total_bytes` 是**后端在 Go 里现算的** `request+outbound`（:133），
 * 不是 SQL 聚合 ⇒ 结构上恒等（见 `unwrapBlobTop` 里的防御性校验）。
 */
export interface BlobRow {
  request_id: string
  /** `COALESCE(rl.gw_session_id, '')`（:102）⇒ 无会话时是**空串**，不是 null。 */
  session_key: string
  /** `COALESCE(rl.tenant_id, '')`（:103）⇒ 无租户时是**空串**。 */
  tenant_id: string
  /** ★ `ts.UTC().Format(time.RFC3339)`（:132）⇒ **秒级**精度，以 `Z` 结尾。 */
  occurred_at: string
  /** ★ `pg_column_size(request_body)`，COALESCE 到 0 ⇒ 没有 body 时是 **0**。 */
  request_body_bytes: number
  outbound_body_bytes: number
  total_bytes: number
  total_human: string
  /** ★ `COALESCE(outbound_model, '')` + omitempty ⇒ 无模型时**键不存在**。 */
  model?: string
}

/** data_lifecycle_blobs.go:45-49。 */
export interface BlobTopResponse {
  rows: BlobRow[]
  /**
   * ★★ 是**这 N 行的合计**（:135 逐行累加），**不是全表总量**。
   * ⇒ 永远不能说「这些大字段一共占了 X」（表里可能还有没进 Top-N 的）。
   */
  total_bytes: number
  total_human: string
  /** ★ Go `time.Time` 直编 ⇒ RFC3339 **纳秒**级（与 `occurred_at` 的秒级不同）。 */
  collected_at: string
}

export const BLOB_TOP_KEYS = ['rows', 'total_bytes', 'total_human', 'collected_at'] as const
export const BLOB_ROW_KEYS = [
  'request_id', 'session_key', 'tenant_id', 'occurred_at',
  'request_body_bytes', 'outbound_body_bytes', 'total_bytes', 'total_human',
] as const

/** data_lifecycle_blobs.go:81 默认 20。 */
export const BLOB_TOP_LIMIT_DEFAULT = 20
/** data_lifecycle_blobs.go:83 `n <= 200`。 */
export const BLOB_TOP_LIMIT_MAX = 200

export interface BlobTopQuery {
  /**
   * ★ `err == nil && n > 0 && n <= 200` 才生效，否则**静默回落 20**（:82-86）。
   * ⚠️ 与 approvals 的 `page_size` 不是一种口径：那个是「>200 ⇒ 回落 50」，
   * 这个是「不在 (0,200] ⇒ 回落 20」（0 与负数也回落）。
   * ⚠️ 非整数（`Atoi` 失败）同样静默回落 20。
   */
  limit?: number
}

/** 后端实际会采用的 limit（与上面的静默回落规则逐条对齐）。 */
export function blobsTopLimitEffective(limit: number | undefined): number {
  if (limit === undefined || !Number.isInteger(limit)) return BLOB_TOP_LIMIT_DEFAULT
  if (limit <= 0 || limit > BLOB_TOP_LIMIT_MAX) return BLOB_TOP_LIMIT_DEFAULT
  return limit
}

export function fetchBlobTop(
  q?: BlobTopQuery,
  options?: RequestOptions,
): Promise<BlobTopResponse> {
  const suffix = q?.limit !== undefined ? `?limit=${q.limit}` : ''
  return req<unknown>('GET', `/api/admin/data-lifecycle/blobs/top${suffix}`, undefined, options)
    .then(unwrapBlobTop)
}

export function unwrapBlobTop(resp: unknown): BlobTopResponse {
  const d = requireObject(resp, '大字段清单')
  requireKeys(d, BLOB_TOP_KEYS, '大字段清单')
  const rows = requireArray(d.rows, '大字段清单 rows')
  rows.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`大字段清单 rows[${i}] 不是对象`)
    requireKeys(row, BLOB_ROW_KEYS, `大字段清单 rows[${i}]`)
    // 防御性校验：后端 :133 在 Go 里现算，对真实响应**结构上恒等**，
    // 抓不到后端逻辑错，抓的是**形状不符/传输损坏**（例如被别的响应顶替）。
    const a = Number(row.request_body_bytes)
    const b = Number(row.outbound_body_bytes)
    if (Number(row.total_bytes) !== a + b) {
      throw new Error(`大字段清单 rows[${i}] total_bytes 与两个分量之和不符`)
    }
  })
  return d as unknown as BlobTopResponse
}

/**
 * ★★ 撞到请求的 limit ⇒ **Top-N 被截断**，`total_bytes` 一定只是部分和。
 * 读法：即便 `rows.length < limit` 也**不能**断言「表里没有更大的」，
 * 只能说「本次取到的这些合计是 X」。
 */
export function blobTopLikelyTruncated(r: BlobTopResponse, requestedLimit?: number): boolean {
  const n = blobsTopLimitEffective(requestedLimit)
  return r.rows.length >= n
}

/** ★ `model` 带 omitempty ⇒ 没模型名时**键不存在**（或被 COALESCE 成空串）。 */
export function blobModelUnknown(b: BlobRow): boolean {
  return !('model' in b) || b.model === ''
}

/** ★ 没有关联 body 的行两个分量都是 0 ⇒ 这行其实**不是**大字段。 */
export function blobRowHasNoBody(b: BlobRow): boolean {
  return Number(b.request_body_bytes) === 0 && Number(b.outbound_body_bytes) === 0
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

/** `null` 合法（Go nil 切片），非 null 非数组仍抛错。 */
function nullableArray(v: unknown, where: string): unknown[] {
  if (v === null || v === undefined) return []
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组也不是 null`)
  return v
}
