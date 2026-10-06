import { req, type RequestOptions } from './client'

// systemMonitor.ts — 系统监控面。
//   GET /api/admin/system-monitor/stats         队列/并发/近 1 小时计数
//   GET /api/admin/system-monitor/recent-runs    最近结束的探针运行明细
//
// 鉴权：两条都挂 `adminWrap`（admin/systemmonitor_handlers.go:636-637）
// ⇒ **admin 档，tenant_admin 可用**。
//
// ★ 同族的其它端点**档位不一样**，照抄会 403：
//   · superAdmin 档：submit / start-all / stop-all / by-credential/{id} /
//     by-provider/{id} / by-model/{model} / **concurrency**
//   · `concurrency` 还是 **PATCH**（改 `self_check_settings.monitor_concurrency`）
//     ⇒ 写操作，本模块**不做**。
//
// ⚠️★★★ 六个坑，逐条实读源码：
//
// (1) ★★★ **`stats` 里每个 0 都可能是「不知道」。**
//     · `h.systemMonitor == nil` ⇒ `queue_size` / `running_size` 直接留 0、
//       `in_fallback` 留 false（handler 里的 `if h.systemMonitor != nil` 整块跳过）
//       ⇒ 「队列 0 条、没在降级」是**没接线**时的假象，不是「一切正常」。
//     · `monitor_concurrency`：从 `self_check_settings WHERE id=1` 读，
//       **读失败或 ≤0 一律用 5**（`if err == nil && monitorConcurrency > 0`）。
//     · ★★ 最严重的一处：近 1 小时的四个计数用的是
//       `_ = h.db.QueryRow(...).Scan(&completed, &failed, &skipped, &tokens)`
//       —— **错误被显式丢弃**。查询失败时四个值全是 0，
//       界面上与「一小时零次运行」**完全无法区分**。
//     ⇒ 前端方向刻意偏向「宁可说可能没取到」：这三个成组的 0 一律加说明。
//
// (2) ★★★ **`completed + failed + skipped ≠ 总运行数`。**
//     SQL 口径（handler 内逐字）：
//       completed := COUNT(*) FILTER (WHERE status = 'success')
//       failed    := COUNT(*) FILTER (WHERE status IN ('failed','timeout','network_error'))
//       skipped   := COUNT(*) FILTER (WHERE status = 'skipped')
//     而 `status` 的 CHECK 约束允许 **6 个**值
//     （`system_probe_runs_status_check`）：
//       success / failed / expired / skipped / timeout / network_error
//     ★ `expired` **既不在成功、也不在失败、更不在跳过** ⇒ 三者相加漏掉它。
//     ⇒ 页面**不得**把三者相加当「一小时总运行数」展示。
//
// (3) ★★ `recent-runs` 的行是**静默跳过的**，而 `total` 取 `len(out)`。
//     扫描出错走 `warnRowSkip(...)` + `continue`（不是上抛）。
//     handler 自己的注释就写明：
//       「探针运行记录少一截 = 失败/跳过的探针被静默抹掉，
//         "系统监控全绿"是假象。total 取自 len(out)，静默截断不会体现为数字异常。」
//     ⇒ `total` 是**本页返回行数**，不是数据库计数。
//
// (4) ★★ **`recent-runs` 只含**已结束**的运行。**
//     两处写入方（`bg/credential_selfcheck.go:908`、
//     `bg/systemmonitor/audit.go:122`）都在**运行结束后**才 INSERT，
//     且 `finished_at` 列是 `NOT NULL`、两处都传 `time.Now()`。
//     ⇒ **进行中的探测根本不会出现在这张清单里**，
//       `finished_at` 也永远是真实时间（不存在零值时间 `0001-01-01`）。
//     ⇒ 页面不能说「这是最新的探针运行」，要说「最近**已结束**的运行」。
//
// (5) ★ `limit` 默认 50、越界静默回落 50（上限 200）—— 与 `sessions/list`
//     （50/500）、`online`（20/静默 clamp 100）同族但**数值都不同**，别照抄。
//     但 `limit` 同样被**回显** ⇒ `total >= limit` 是**精确**的截断信号。
//
// (6) ★ 两个端点的错误信封不同：
//     `stats` / `recent-runs` 走 `writeError`（嵌套 `{"error":{"detail":…}}`），
//     而同文件的 `migration-metrics` 走 `http.Error`（**text/plain**）。
//     ★ 同一个 URL 前缀下形状不一致 ⇒ 各端点独立解包，不抽通用解包器。

/** `stats` 的 1 小时计数口径：`expired` **不在其中任何一项**。见 (2)。 */
export const STATS_FAILED_STATUSES = ['failed', 'timeout', 'network_error'] as const
/** `status` 的 CHECK 约束允许的 6 个值（权威来源：baseline DDL）。 */
export const PROBE_RUN_STATUSES = [
  'success',
  'failed',
  'expired',
  'skipped',
  'timeout',
  'network_error',
] as const
export type ProbeRunStatus = (typeof PROBE_RUN_STATUSES)[number]

/** `monitor_concurrency` 在读不到 settings 行时的兜底值。见 (1)。 */
export const STATS_CONCURRENCY_FALLBACK = 5
/** ★ `systemMonitor == nil` 时队列/降级字段全是 0/false。见 (1)。 */
export const STATS_MAY_NOT_BE_WIRED = true
/** ★ 1 小时四个计数的 Scan **错误被丢弃** ⇒ 全 0 可能是查询失败。见 (1)。 */
export const STATS_1H_COUNTS_MAY_BE_UNAVAILABLE = true

export interface SystemMonitorStats {
  /** ★ 监控器未接线时**恒为 0**。见 (1)。 */
  queue_size: number
  /** ★ 同上。 */
  running_size: number
  /** ★ 未接线时**恒为 false**（不是「没在降级」）。见 (1)。 */
  in_fallback: boolean
  /** ★ 读不到 settings 行时为 **5**。见 (1)。 */
  monitor_concurrency: number
  /** ★ 与下面三个**全部**可能因查询失败而为 0。见 (1)。 */
  completed_total_1h: number
  failed_total_1h: number
  skipped_total_1h: number
  total_tokens_1h: number
  snapshot_at: string
}

export interface SystemProbeRun {
  id: number
  task_id: number
  task_type: string
  automaticity: string
  credential_id: number
  raw_model: string
  source: string
  /** `COALESCE(worker_id,'')` ⇒ 空串意味着源列为 NULL。 */
  worker_id: string
  status: string
  attempt: number
  /** 可空 + `omitempty` ⇒ **键可能整个不存在**。 */
  http_status?: number
  /** 可空 + `omitempty` ⇒ **键可能整个不存在**。 */
  latency_ms?: number
  err_code: string
  skip_reason: string
  started_at: string
  /** ★ 恒为真实时间（列 NOT NULL + 两处写入方都传 `time.Now()`）。见 (4)。 */
  finished_at: string
  recent_request_id: string
}

export interface RecentRunsResponse {
  /** ★ 本页**返回行数**（跳行后），不是数据库计数。见 (3)。 */
  total: number
  /** 后端回显的生效 limit ⇒ 截断判定用它。见 (5)。 */
  limit: number
  runs: SystemProbeRun[]
}

export function fetchSystemMonitorStats(options?: RequestOptions): Promise<SystemMonitorStats> {
  return req<SystemMonitorStats>('GET', '/api/admin/system-monitor/stats', undefined, options)
}

export const RECENT_RUNS_LIMIT_DEFAULT = 50
export const RECENT_RUNS_LIMIT_MAX = 200

export interface RecentRunsParams {
  limit?: number
}

export function fetchRecentProbeRuns(
  params: RecentRunsParams = {},
  options?: RequestOptions,
): Promise<RecentRunsResponse> {
  const qs = new URLSearchParams()
  // ★ 只发 1..200：越界静默回落 50（不是 400）。见 (5)。
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    if (n >= 1 && n <= RECENT_RUNS_LIMIT_MAX) qs.set('limit', String(n))
  }
  const s = qs.toString()
  return req<RecentRunsResponse>('GET', `/api/admin/system-monitor/recent-runs${s ? '?' + s : ''}`, undefined, options)
}

/** 截断判定：用**回显**的 limit ⇒ 命中就是「确实被填满」。见 (5)。 */
export function recentRunsTruncated(resp: RecentRunsResponse | null | undefined): boolean {
  if (!resp) return false
  const n = resp.total ?? 0
  const lim = resp.limit
  return typeof lim === 'number' && lim > 0 && n >= lim
}

/**
 * ★ **不能**用三个计数之和当「一小时总运行数」。
 * `expired` 不被任何一项统计（见 (2)）。这个函数只用于显示
 * 「已统计到 N 次」，语义上永远**不是**总数。
 */
export function statsAccountedRuns(s: SystemMonitorStats | null | undefined): number {
  if (!s) return 0
  return (s.completed_total_1h ?? 0) + (s.failed_total_1h ?? 0) + (s.skipped_total_1h ?? 0)
}

/** `expired` 是唯一「后端三个口径都没统计」的状态。见 (2)。 */
export function statusIsUnaccounted(status: string | null | undefined): boolean {
  return (status ?? '').toLowerCase() === 'expired'
}

/** 是否属于后端 `failed_total_1h` 的口径（`expired` **不在内**）。 */
export function statusCountsAsFailed(status: string | null | undefined): boolean {
  return (STATS_FAILED_STATUSES as readonly string[]).includes((status ?? '').toLowerCase())
}

export function runTone(run: SystemProbeRun | null | undefined): 'success' | 'warning' | 'danger' | 'muted' {
  const s = (run?.status ?? '').toLowerCase()
  if (s === 'success') return 'success'
  if (statusCountsAsFailed(s)) return 'danger'
  // ★ `expired` 必须单独一档：它既没被算成功、也没被算失败
  if (s === 'expired') return 'warning'
  return 'muted'
}

/** ★ 清单里**没有**进行中的运行（只在结束后才落库）。见 (4)。 */
export const PROBE_RUNS_ONLY_SETTLED = true

/** 运行时长（毫秒）。缺列时返回 `null` 而不是 0。 */
export function runDurationMs(run: SystemProbeRun | null | undefined): number | null {
  if (!run) return null
  const s = Date.parse(run.started_at)
  const e = Date.parse(run.finished_at)
  if (Number.isNaN(s) || Number.isNaN(e)) return null
  const d = e - s
  return d >= 0 ? d : null
}