// logsAdmin.ts — 日志管理读面（**admin 档**，四条只读端点）。
//   GET /api/admin/logs/body-cache-stats
//   GET /api/admin/logs/files
//   GET /api/admin/logs/stats
//   GET /api/admin/logs/archive/list
//
// 档位（`admin/handler.go:959,1115,1116,1119`）：上移的四条**全是 `admin(...)`**
//   ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//   ★ 但**同一前缀下混着三条 superAdmin**：
//       /config  (PUT 改轮转配置，热加载)  h.superAdmin
//       /archive (POST 归档)               h.superAdmin
//       /cleanup (POST 删除)               h.superAdmin
//   ⇒ 移动端**不能按前缀判权限**。
//
// ★★★★★★ 头号陷阱：**同一个「文件日志没启用」在三个端点上是三个不同判据**。
//   后端只有一个开关 `logging.ActiveConfig().File == ""`，但三处各自表达：
//     · files         ⇒ `dir === ''`（结构体字段，恒存在，可能为空串）
//     · stats         ⇒ `log_dir === ''`
//     · archive/list  ⇒ **`dir` 键整个不存在**（map 字面量分三支写的！）
//   ⇒ 「看起来都是目录字段」，但**判据形态不一样**，不能抽一个通用 helper。
//
// ★★★★★★ `archive/list` 是**异形端点**：`dir` / `exists` 是**条件存在的键**。
//   （`admin/log_management.go:574 / 580 / 609-614`）
//       ① 未启用        ⇒ {"archives": [], "total": 0}                      ← **没有 dir、没有 exists**
//       ② 归档目录读不到 ⇒ {"archives": [], "total": 0, "dir": D, "exists": false}
//       ③ 正常          ⇒ {"archives": [...], "total": N, "dir": D, "exists": true}
//   ⇒ ★ TS 接口里 `dir` / `exists` 必须是**可选**字段，写成必填就是错的。
//   ⇒ ★ ① 与 ② 的 `archives`/`total` **完全一样**，只有「键在不在」能分开。
//   ⇒ `archives` 三态都是**非 nil 空切片**（`[]any{}` / `make([]archiveItem, 0)`）⇒ 永不为 null。
//
// ★★★★★★ `files` 里的 `is_archived` **永远是 false** —— 硬编码字面量：
//   `internal/logging/logging.go:449`  `IsArchived: false,`
//   而 `ListFiles` 的 doc 注释说「含轮转备份和**归档**」—— **注释是错的**：
//   `scanLogDir` 收的是**顶层目录**且 `if e.IsDir() { continue }`（不递归）
//   ⇒ ★ `files` **永远不包含** `archive/` 里的东西。归档要看 `/archive/list`。
//   ⇒ ★★ 客户端**不许**用 `is_archived === false` 推出「没有归档」。
//
// ★★★★ `body-cache-stats` 的 `hit_rate: 0` 是**二义**的：
//   （`admin/logs_body_cache.go:137-141`）`total := hits + misses; if total > 0 {…}`
//   ⇒ 命中率为 0% **和** 一次流量都没有，**都是 0**，分不开。
//   ★ 而这个端点的 doc 注释说「前端仅在 super admin 视图展示」—— 与 `admin(...)`
//     的实际档位**矛盾**（注释过时，与第四十二轮 model-policies 的头注释同一类错）。
//
// ★★★★ `stats` 的 `disk_usage_pct: 0` 也是**二义**的：
//   `if pct, _, _, _, err := diskUsageAt(dir); err == nil { resp.DiskUsagePct = pct }`
//   ⇒ 探测失败时**静默**留 0，不报错、不留标记 ⇒ 与「真的 0%」分不开。
//
// ★★★ `stats` 的 `exists: false` 同样**二义**，且三个响应的 `exists` 形态：
//   ① `cur.File == ""`      ⇒ 整个零值响应：`log_dir:""` + `exists:false` + 全 0/null
//   ② `!dirExists(dir)`     ⇒ `log_dir:D` + `exists:false` + 全 0/null
//   ③ 目录存在             ⇒ 全量数字
//   ⇒ ① 与 ② 只能靠 **`log_dir` 空不空**分开，`exists` 帮不上忙。
//
// ★★★ `oldest_mtime` / `newest_mtime` 是 `*time.Time` **且没有 omitempty**
//   ⇒ 这两个键**永远在**，没有文件时是 **`null`**（不是缺键、不是零值时间串）。
//   ⇒ 「null」= 这个目录下**一个非归档日志文件都没有**。
//
// ★★★ `total_files` 与 `archive_files` 是**分开的两栏**：
//   walk 命中 `.../archive/...` 就 `ArchiveFiles++` 并 `return`（不计入 TotalFiles）
//   ⇒ 「总文件数」**不含归档**。别把两栏加起来当总数。
//   ★★ 判定是 `strings.Contains(p, sep+"archive"+sep)` **或**
//      `filepath.Base(filepath.Dir(p)) == "archive"`
//      ⇒ 任意层级里名叫 `archive` 的目录都算，不只是顶层那个。
//
// ★★ 遍历错误**静默跳过**：`if err != nil || fi.IsDir() { return nil }`
//   ⇒ 读不了的条目在统计里**直接消失**，不报错、不留日志。
//   ⇒ 而 `files` 走 `os.ReadDir`，**失败会 500**（`writeInternalErr`）—— 同族两种错误风格。
//
// ★★ 四条**全有方法门** ⇒ 非 GET 一律 405 `method not allowed`。
//
// ★ 排序：`files`（`scanLogDir`）与 `archives` 都是 **ModTime 倒序**（最新在前）。
// ★ `size_human` / `total_size_human` 是**派生值**（`humanBytes`，1024 进制、
//   整数字节原样、其余去尾零），客户端可复算 ⇒ 复算不一致就是异常。
// ★ 503 只有 `body-cache-stats` 有：`body cache not initialized`
//   （⚠️ handler 的 doc 注释把它写成 "not initialised"，**代码里是 "not initialized"**）。

import type { RequestOptions } from './client'
import { req } from './client'

// ── body-cache-stats（`admin/logs_body_cache.go:123-146`）────────────────

/** ★★ 这 6 个键是 map 字面量里**逐个无条件赋值**的 ⇒ 永远都在。 */
export const BODY_CACHE_STAT_KEYS = ['size', 'hits', 'misses', 'evictions', 'hit_rate', 'cap'] as const

export interface BodyCacheStats {
  size: number
  hits: number
  misses: number
  evictions: number
  /** ★★ 0 是二义的：0% 命中率 **或** 一次流量都没有。 */
  hit_rate: number
  cap: number
}

/** ★ `hit_rate` 的真身：`hits / (hits + misses)`，`total == 0` 时留 0。 */
export function bodyCacheHitRate(hits: number, misses: number): number {
  const total = hits + misses
  return total > 0 ? hits / total : 0
}

/** ★★ `hit_rate === 0` 且**一次流量都没有** ⇒ 不能说「命中率 0%」。 */
export function bodyCacheHasNoTraffic(s: BodyCacheStats): boolean {
  return s.hits === 0 && s.misses === 0
}

/** ★★ `hit_rate === 0` 但**有流量** ⇒ 这才是真的「全未命中」。 */
export function bodyCacheIsGenuinelyZeroRate(s: BodyCacheStats): boolean {
  return s.hits === 0 && s.misses > 0
}

/** ★★ `hit_rate` 是派生值，客户端可复算；对不上就是异常。 */
export function bodyCacheHitRateMatches(s: BodyCacheStats): boolean {
  return Math.abs(s.hit_rate - bodyCacheHitRate(s.hits, s.misses)) < 1e-9
}

/**
 * ★★ `size` 来自 `c.lru.Len()`，`cap` 是构造时定的容量。
 *   正常 LRU 下 `size ≤ cap`；**超过**就是后端异常，值得上报。
 */
export function bodyCacheSizeExceedsCap(s: BodyCacheStats): boolean {
  return s.size > s.cap
}

/** ★ 淘汰数不应超过累计「未命中 + 命中」总数（可粗筛）。 */
export function bodyCacheEvictionsArePlausible(s: BodyCacheStats): boolean {
  return s.evictions <= s.hits + s.misses
}

// ── files（`admin/log_management.go:293-315`）────────────────────────

/** 抄自 `logging.LogFileInfo`（`internal/logging/logging.go:399-406`）。 */
export interface LogFileInfo {
  name: string
  size_bytes: number
  mod_time: string
  is_current: boolean
  /** ★★ 压缩判定**是真算的**：`strings.HasSuffix(name, ".gz")`。 */
  is_compressed: boolean
  /**
   * ★★★★★★ **恒为 false** —— `internal/logging/logging.go:449` 是硬编码字面量。
   *   保留这个字段只是为了与后端响应对齐；**不许**用它推断「没有归档」。
   */
  is_archived: boolean
}

/** 抄自 `LogFileInfoExt`（`admin/log_management.go:86-89`）：内嵌 + 一个派生字段。 */
export interface LogFileInfoExt extends LogFileInfo {
  size_human: string
}

/** 抄自 `LogFilesListResponse`（`admin/log_management.go:79-83`）。三个键**恒存在**。 */
export interface LogFilesList {
  /** ★ `make([]LogFileInfoExt, 0, len(files))` ⇒ **永不为 null**。 */
  files: LogFileInfoExt[]
  total: number
  /** ★★ 为空串 ⇒ **文件日志未启用**（不是「没有文件」）。 */
  dir: string
}

export function logsAdminPath(sub: string): string {
  return `/api/admin/logs/${sub}`
}

// ── stats（`admin/log_management.go:320-368`）─────────────────────────

/** 抄自 `LogStatsResponse`（`admin/log_management.go:65-76`）。10 个键**全部恒存在**。 */
export interface LogStats {
  /** ★★ 为空串 ⇒ **文件日志未启用**（此时 `exists` 也是 false，两者要合看）。 */
  log_dir: string
  /** ★★★ 与「未启用」二义，**必须**和 `log_dir` 合看才能分。 */
  exists: boolean
  /** ★★★ **不含** `archive/` 子目录里的文件。 */
  total_files: number
  total_size_bytes: number
  total_size_human: string
  archive_files: number
  archive_size: number
  /** ★ 没有 omitempty 的 `*time.Time` ⇒ 键恒在，无文件时是 **null**。 */
  oldest_mtime: string | null
  newest_mtime: string | null
  /** ★★ 0 是二义的：真的 0% **或** `diskUsageAt` 探测失败（静默）。 */
  disk_usage_pct: number
}

/** ★★ 这 10 个键一个都不会少（结构体无 omitempty）。 */
export const LOG_STATS_KEYS = [
  'log_dir',
  'exists',
  'total_files',
  'total_size_bytes',
  'total_size_human',
  'archive_files',
  'archive_size',
  'oldest_mtime',
  'newest_mtime',
  'disk_usage_pct',
] as const

/** ★★ 三态之一：文件日志**压根没启用**（`cur.File == ""`，整个零值响应）。 */
export function logStatsLoggingDisabled(s: LogStats): boolean {
  return s.log_dir === ''
}

/** ★★ 三态之二：启用了，但**目录不存在**。 */
export function logStatsDirMissing(s: LogStats): boolean {
  return s.log_dir !== '' && !s.exists
}

/** ★★ 三态之三：一切正常。 */
export function logStatsOk(s: LogStats): boolean {
  return s.log_dir !== '' && s.exists
}

/** ★★ `exists:false` 本身**分不出**①和② ⇒ 判「未启用」只能看 `log_dir`。 */
export function logStatsExistsIsAmbiguous(s: LogStats): boolean {
  return !s.exists
}

/** ★ `oldest_mtime === null` ⇒ 这个目录下**一个非归档日志文件都没有**。 */
export function logStatsHasNoFiles(s: LogStats): boolean {
  return s.oldest_mtime === null || s.newest_mtime === null
}

/** ★★ 两栏**分开**：「总文件数」不含归档，别相加。 */
export function logStatsTotalIncludingArchive(s: LogStats): number {
  return s.total_files + s.archive_files
}

/** ★ `disk_usage_pct` 是 0 时不可分辨「0%」与「探测失败」。 */
export function logStatsDiskUsageIsAmbiguous(s: LogStats): boolean {
  return s.disk_usage_pct === 0
}

// ── archive/list（`admin/log_management.go:567-615`）───────────────────

export interface LogArchiveItem {
  name: string
  size_bytes: number
  size_human: string
  mod_time: string
}

/**
 * ★★★★★★ `dir` / `exists` 是**条件存在的键**，不是必填。
 *   ① 未启用时后端**根本没写**这两个键。
 */
export interface LogArchiveList {
  /** ★ 三态都非 nil ⇒ **永不为 null**。 */
  archives: LogArchiveItem[]
  total: number
  /** ★★ **不存在这个键** ⇒ 文件日志未启用（与 `files` 的空串判据不同！）。 */
  dir?: string
  /** ★★ **不存在这个键** ⇒ 同上；存在且 false ⇒ 归档目录读不到。 */
  exists?: boolean
}

/** ★★ 判「未启用」只能看**键在不在**（不是看值）。 */
export function logArchiveListIsDisabled(r: LogArchiveList): boolean {
  return !('dir' in r)
}

/** ★ 归档目录存在但读不到（`exists: false`）。 */
export function logArchiveListDirMissing(r: LogArchiveList): boolean {
  return 'dir' in r && r.exists === false
}

/** ★★ 只有 `exists === true` 才是「读到了归档列表」。 */
export function logArchiveListOk(r: LogArchiveList): boolean {
  return r.exists === true
}

/** ★ 三态判读，页面必须三分。 */
export function logArchiveListState(r: LogArchiveList): 'disabled' | 'dir-missing' | 'ok' {
  if (!('dir' in r)) return 'disabled'
  return r.exists === true ? 'ok' : 'dir-missing'
}

/** ★★ ①与②的 `archives`/`total` **完全一样** ⇒ 只有「键在不在」能分开。 */
export function logArchiveListAmbiguousZero(r: LogArchiveList): boolean {
  return r.archives.length === 0 && r.total === 0
}

// ── 通用：humanBytes 复算 ────────────────────────────────────────────

/**
 * ★ 复算 `humanBytes`（`admin/data_lifecycle_storage.go:502-518`）：
 *   1024 进制，units B/KB/MB/GB/TB/PB；整数字节原样（`formatInt`），
 *   其余保留两位再 `TrimRight` 掉尾零；`n < 0` ⇒ `"0 B"`。
 * ★ 只用来做**一致性核对**（派生值对不上就是异常），页面渲染用后端给的串。
 */
export function humanBytesMatches(n: number, human: string): boolean {
  if (n < 0) return human === '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v = v / 1024
    i++
  }
  if (i === 0) return human === `${v} ${units[i]}`
  const fixed = v.toFixed(2)
  return human === `${trimTrailingZeros(fixed)} ${units[i]}`
}

function trimTrailingZeros(s: string): string {
  if (!s.includes('.')) return s
  return s.replace(/0+$/, '').replace(/\.$/, '')
}

/** ★ 503（只有 body-cache-stats 有）。⚠️ 代码是 "not initialized"，注释写成 "not initialised"。 */
export function bodyCacheNotInitialised(msg: string): boolean {
  return /body cache not initialized|body cache not initialised/i.test(msg)
}

// ── 四条 fetch（各自独立解包，不抽通用解包器）─────────────────────────

export function fetchBodyCacheStats(options?: RequestOptions): Promise<BodyCacheStats> {
  return req<unknown>('GET', logsAdminPath('body-cache-stats'), undefined, options).then(unwrapBodyCacheStats)
}

/** ★★ 扁平 6 键对象，没有信封。少任一键即抛错，不返 `{}`。 */
export function unwrapBodyCacheStats(resp: unknown): BodyCacheStats {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    const missing = BODY_CACHE_STAT_KEYS.filter((k) => !(k in m))
    if (missing.length === 0) return m as unknown as BodyCacheStats
    throw new Error(`admin/logs/body-cache-stats 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/logs/body-cache-stats 响应形状不符：期望 6 键扁平对象，实得 ${actual}`)
}

export function fetchLogFiles(options?: RequestOptions): Promise<LogFilesList> {
  return req<unknown>('GET', logsAdminPath('files'), undefined, options).then(unwrapLogFiles)
}

/** ★ `{files, total, dir}`；三个键都是结构体字段 ⇒ **恒存在**。 */
export function unwrapLogFiles(resp: unknown): LogFilesList {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (Array.isArray(m.files) && typeof m.total === 'number' && typeof m.dir === 'string') {
      return m as unknown as LogFilesList
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/logs/files 响应形状不符：期望 {files:[…], total, dir}，实得 ${actual}`)
}

export function fetchLogStats(options?: RequestOptions): Promise<LogStats> {
  return req<unknown>('GET', logsAdminPath('stats'), undefined, options).then(unwrapLogStats)
}

/** ★★ 扁平 10 键对象。三种状态共用同一形状 ⇒ 形状判据**抓不到**状态差异。 */
export function unwrapLogStats(resp: unknown): LogStats {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    const missing = LOG_STATS_KEYS.filter((k) => !(k in m))
    if (missing.length === 0) return m as unknown as LogStats
    throw new Error(`admin/logs/stats 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/logs/stats 响应形状不符：期望 10 键扁平对象，实得 ${actual}`)
}

export function fetchLogArchiveList(options?: RequestOptions): Promise<LogArchiveList> {
  return req<unknown>('GET', logsAdminPath('archive/list'), undefined, options).then(unwrapLogArchiveList)
}

/**
 * ★★★★★★ 异形端点：`dir` / `exists` **可能整个不存在**（未启用那一支）。
 *   ⇒ 形状判据只能钉「`archives` 是数组且 `total` 是数字」，
 *     **不能**要求 `dir` / `exists` 在场 —— 要求了就永远收不到状态①。
 */
export function unwrapLogArchiveList(resp: unknown): LogArchiveList {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const m = resp as Record<string, unknown>
    if (Array.isArray(m.archives) && typeof m.total === 'number') {
      return m as unknown as LogArchiveList
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/logs/archive/list 响应形状不符：期望 {archives:[…], total}（dir/exists 可缺），实得 ${actual}`)
}

