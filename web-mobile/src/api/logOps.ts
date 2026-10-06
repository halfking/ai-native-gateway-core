import { req, type RequestOptions } from './client'

/**
 * logOps.ts — 日志运维面的三条只读端点（2026-10-08，第六十二批）。
 *
 * GET /api/admin/logs/body-cache-stats   （handler.go:959，  **admin** 档）
 * GET /api/admin/logs/files              （handler.go:1115， **admin** 档）
 * GET /api/admin/logs/stats              （handler.go:1116， **admin** 档）
 *
 * ## ★★★ 同一个 `/api/admin/logs/*` 前缀下有**两种权限档**
 *
 * ```
 * handler.go:959   body-cache-stats   admin(...)         ← 本批
 * handler.go:1114  config             h.superAdmin(...)  ← 有 PUT 写操作，不碰
 * handler.go:1115  files              admin(...)         ← 本批
 * handler.go:1116  stats              admin(...)         ← 本批
 * handler.go:1117  archive            h.superAdmin(...)  ← 归档（写），不碰
 * handler.go:1118  cleanup            h.superAdmin(...)  ← 删除（写），不碰
 * handler.go:1119  archive/list       admin(...)
 * ```
 *
 * ⇒ **按前缀判权限会判错**。本批三条都是 admin 档 ⇒ 抽屉席不设 `requiresRole`；
 * 但只要往后加进 `config` / `archive` / `cleanup` 任何一条，**整页档位要跟着升**。
 *
 * ## 不碰写操作
 * `PUT /logs/config`（改日志级别）、`POST /logs/archive`、`POST /logs/cleanup`
 * 都有外部副作用（改行为、动文件、**删文件**），按既定纪律本批不碰。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/logs/stats
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/log_management.go:65-76，逐字照抄。 */
export interface LogStatsResponse {
  log_dir: string
  exists: boolean
  total_files: number
  total_size_bytes: number
  total_size_human: string
  archive_files: number
  archive_size: number
  /** ★ `*time.Time` ⇒ 目录里一个文件都没有时是 `null`。 */
  oldest_mtime: string | null
  /** ★ 同上。 */
  newest_mtime: string | null
  /**
   * ★★ **`float64` 值类型，不是指针**：
   * `diskUsageAt(dir)` 出错时后端只是不赋值（log_management.go:367-369，
   * `if pct, _, _, _, err := diskUsageAt(dir); err == nil { … }`）
   * ⇒ 失败时下发 **0**，与「真的是 0%」**不可分**。
   * ⇒ 客户端不能拿它做断言，只能展示。
   */
  disk_usage_pct: number
}

export const LOG_STATS_KEYS = [
  'log_dir', 'exists', 'total_files', 'total_size_bytes', 'total_size_human',
  'archive_files', 'archive_size', 'oldest_mtime', 'newest_mtime', 'disk_usage_pct',
] as const

export function fetchLogStats(options?: RequestOptions): Promise<LogStatsResponse> {
  return req<unknown>('GET', '/api/admin/logs/stats', undefined, options).then(unwrapLogStats)
}

export function unwrapLogStats(resp: unknown): LogStatsResponse {
  const d = requireObject(resp, '日志统计')
  requireKeys(d, LOG_STATS_KEYS, '日志统计')
  return d as unknown as LogStatsResponse
}

/**
 * ★★★ 三种「什么都没有」在响应上长得**不一样**，不能合成一种文案：
 *   1. `log_dir === ''` ⇒ **文件日志根本没启用**（`cur.File == ""`，
 *      log_management.go:326-329 直接回零值结构）—— 此时 `exists` 也是 false。
 *   2. `exists === false` ⇒ 配了路径但**目录不存在**（:332-335）。
 *   3. `exists === true` 但 `total_files === 0` ⇒ 目录在，**真的一个文件都没有**。
 */
export function logStatsNotEnabled(s: LogStatsResponse): boolean {
  return s.log_dir === ''
}

export function logStatsDirMissing(s: LogStatsResponse): boolean {
  return !logStatsNotEnabled(s) && s.exists === false
}

export function logStatsEmpty(s: LogStatsResponse): boolean {
  return s.exists === true && s.total_files === 0
}

/** ★ 时间范围只在真的有文件时才存在。 */
export function logStatsTimeRange(s: LogStatsResponse): { from: string; to: string } | null {
  if (s.oldest_mtime === null || s.newest_mtime === null) return null
  return { from: s.oldest_mtime, to: s.newest_mtime }
}

/**
 * ★ 归档占比：有归档文件时才算得出比例，否则除以 0。
 * 返回 `null` 表示「算不出」，调用方**不要**替换成 0%。
 */
export function logStatsArchiveRatio(s: LogStatsResponse): number | null {
  const total = Number(s.total_size_bytes)
  if (!(total > 0)) return null
  return Number(s.archive_size) / total
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/logs/files
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * ★★ `LogFileInfoExt`（log_management.go:85-88）**内嵌** `logging.LogFileInfo`：
 *
 * ```go
 * type LogFileInfoExt struct {
 *     logging.LogFileInfo          // 内嵌 ⇒ Go 的 json 编码会**扁平化**
 *     SizeHuman string `json:"size_human"`
 * }
 * ```
 *
 * ⇒ 响应里**没有** `log_file_info` 这样的嵌套层，六个字段与 `size_human` 平级。
 * 内层字段逐字来自 internal/logging/logging.go:399-406。
 */
export interface LogFileInfo {
  name: string
  size_bytes: number
  /** `time.Time`（非指针）⇒ 恒有值。 */
  mod_time: string
  is_current: boolean
  is_compressed: boolean
  is_archived: boolean
  /** 来自外层 `LogFileInfoExt`，与内层六个字段**平级**。 */
  size_human: string
}

/** admin/log_management.go:79-83。 */
export interface LogFilesListResponse {
  files: LogFileInfo[]
  total: number
  /** ★ 与 stats 同源（`filepath.Dir(cur.File)`），但**这里没有 `''` 早退**。 */
  dir: string
}

export const LOG_FILES_KEYS = ['files', 'total', 'dir'] as const
export const LOG_FILE_INFO_KEYS = [
  'name', 'size_bytes', 'mod_time', 'is_current', 'is_compressed', 'is_archived', 'size_human',
] as const

export function fetchLogFiles(options?: RequestOptions): Promise<LogFilesListResponse> {
  return req<unknown>('GET', '/api/admin/logs/files', undefined, options).then(unwrapLogFiles)
}

export function unwrapLogFiles(resp: unknown): LogFilesListResponse {
  const d = requireObject(resp, '日志文件')
  requireKeys(d, LOG_FILES_KEYS, '日志文件')
  if (!Array.isArray(d.files)) throw new Error('日志文件 files 不是数组')
  d.files.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`日志文件 files[${i}] 不是对象`)
    requireKeys(row, LOG_FILE_INFO_KEYS, `日志文件 files[${i}]`)
  })
  return d as unknown as LogFilesListResponse
}

/**
 * ★ 文件日志未启用时 `logging.ListFiles()` 返回**空切片 + nil 错误**
 * （internal/logging/logging.go:408-410 注释原文）
 * ⇒ 空列表**既可能是「没启用」也可能是「启用了但没文件」**，
 * 唯一区分信号是 `dir` 是否为空字符串（:310-313）。
 */
export function logFilesNotEnabled(r: LogFilesListResponse): boolean {
  return r.dir === ''
}

export function logFilesEmpty(r: LogFilesListResponse): boolean {
  return r.dir !== '' && r.files.length === 0
}

/** ★ 校验用：文件日志非空 ⇒ `total` 必须与 `files.length` 一致。 */
export function logFilesTotalDisagrees(r: LogFilesListResponse): boolean {
  return r.total !== r.files.length
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/admin/logs/body-cache-stats
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * admin/logs_body_cache.go:143-152，**手写的 `map[string]any`**（不是 struct）
 * ⇒ 键是字面量，没有任何 struct tag 可照抄；六个键恒发，无 omitempty。
 */
export interface BodyCacheStats {
  size: number
  hits: number
  misses: number
  evictions: number
  /**
   * ★ `hits/(hits+misses)`，**分母为 0 时后端留 0.0**（:139-142）
   * ⇒ 与「真的 0% 命中率」不可分，调用方要靠 `hits+misses` 判有没有样本。
   */
  hit_rate: number
  cap: number
}

export const BODY_CACHE_STATS_KEYS = ['size', 'hits', 'misses', 'evictions', 'hit_rate', 'cap'] as const

export function fetchBodyCacheStats(options?: RequestOptions): Promise<BodyCacheStats> {
  return req<unknown>('GET', '/api/admin/logs/body-cache-stats', undefined, options)
    .then(unwrapBodyCacheStats)
}

export function unwrapBodyCacheStats(resp: unknown): BodyCacheStats {
  const d = requireObject(resp, '详情 body 缓存')
  requireKeys(d, BODY_CACHE_STATS_KEYS, '详情 body 缓存')
  return d as unknown as BodyCacheStats
}

/** ★ 没有任何样本 ⇒ `hit_rate` 无意义（不是「0% 命中率」）。 */
export function bodyCacheRateMeaningless(s: BodyCacheStats): boolean {
  return s.hits + s.misses === 0
}

/** ★ 容量用满：缓存全被占用，配合 evictions > 0 才能说「在淘汰」。 */
export function bodyCacheSaturated(s: BodyCacheStats): boolean {
  return s.cap > 0 && s.size >= s.cap
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
  // ★ 反向检测：dashboardapi / annotations 都不是这个形状
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
