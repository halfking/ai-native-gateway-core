import { req, type RequestOptions } from './client'

// dataLifecycle.ts — 数据生命周期面（分区 / 存储）。
//   GET /api/admin/data-lifecycle/partitions        分区表状态（每张分区表 + 每个分区）
//   GET /api/admin/data-lifecycle/storage/tables    按体积排序的表榜
//
// 鉴权：两条都是 `admin(...)`（admin/handler.go:964、:983）⇒ **admin 档**。
//
// ★ 同族其余端点**全是 superAdmin 且多数是写操作**
//   （archive / archive-batch / drop / vacuum / vacuum-full / reindex /
//    promote / hot/* / blobs/cleanup/execute / degradation/control / recover）
//   ⇒ 本模块**一条都不碰**。
//   （唯一例外是 superAdmin 的只读 `hot/cron/stats`，本轮不做。）
//
// ⚠️★★★ 七个坑，逐条实读源码：
//
// (1) ★★★★ **`storage/tables` 的 `total_bytes` 不是整库大小，是「返回的前 N 张之和」。**
//     `queryTableSizes` 只累加**本次返回的行**：
//       `totalBytes += t.TotalBytes`  ← 在 LIMIT 之后的循环里
//     响应里的 `total_bytes` / `total_human` 就是这个值。
//     handler 自己的注释写明：「占比对 Top-N 求和，截断会让 Top-N 大表榜 +
//     DB 占用同时偏小」。
//     ⇒ 显示成「本库共 12.3 GB」是**错的**，只能说「上榜这 N 张合计 12.3 GB」。
//
// (2) ★★★★ **`percent_of_db` 的分母是 Top-N 之和，不是数据库。**
//     `out[i].PercentOfDB = int(out[i].TotalBytes * 100 / totalBytes)`
//     ⇒ 这一列恒以「榜内占比」为语义，加起来约等于 100%，
//       与「占全库百分比」无关。
//     ⇒ 字段名在骗人：UI 必须改口径说明，不能照着字面渲染。
//
// (3) ★★★ **`rows` 是 planner 估计值，不是 `COUNT(*)`。**
//     SQL 取的是 `COALESCE(s.n_live_tup, 0) AS row_estimate`
//     （来自 `pg_stat_user_tables`，**需要 analyze 才有意义**，
//      写入后未统计时会明显偏低）⇒ 字段名却是 `rows`。
//     ⇒ 页面标注「估计行数」。
//
// (4) ★★★ **分区父表与其分区可能**同时出现在榜上**，体积**重复计入**。
//     `WHERE c.relkind IN ('r','p')` 同时包含普通表与分区父表；
//     而 `pg_total_relation_size(父表)` **本身已经包含所有子分区**。
//     ⇒ 榜上出现 `request_logs` 与 `request_logs_2026_10` 时，
//       两行的体积是**重叠**的，累加会偏大。
//     ⇒ 榜里逐行标出「分区父表」，提示与分区行不可相加。
//
// (5) ★★ **`partitions` 里某张表**整个消失**是查不出来的。**
//     handler 遍历固定表清单，某张表的状态查询出错就 `slog.Warn` + `continue`
//     ⇒ 响应少一张表，客户端**无法区分**「这张表查不到」与「配置里就没有它」。
//     ⇒ 页面必须列出「本次返回了几张表」，而不是假装是全集。
//
// (6) ★★★ `row_count = -1` 是后端**明确的「未知」哨兵**，不是负数行数。
//     每个分区单独 `SELECT COUNT(*)`；失败时 handler 显式写
//     `pinfo.RowCount = -1 // Indicate unknown`。
//     ⇒ 渲染成「未知」而不是「-1 行」也不是「0 行」。
//
// (7) ★★ **`archived_count` 把 columnar 的也算成「已归档」。**
//     计数条件是 `if pinfo.IsArchived || pinfo.IsColumnar { … }`
//     ⇒ 「已归档 N」≠「归档表里真有 N 个分区」。
//     ⇒ 另外 `IsArchived` 本身是**按分区名是否含 `_archive_` 推断**的
//       （`containsSubstring(pinfo.PartitionName, "_archive_")`），不是查目录。
//
// ★★ 另有一条**静默丢行**：分区边界解析失败
//   （`parsePartitionBounds`，例如 DEFAULT 分区）会 `continue`，
//   **整个分区从清单里消失**（不只是缺日期）。
//   handler 注释自陈：「少列一个分区就等于让管理员以为该分区不存在」。
//
// ★ 关于日期：这里 `start_date` / `end_date` 是**非指针 `time.Time`**，
//   看起来会有「零值时间 `0001-01-01`」的风险 —— 但**实测不存在**：
//   解析失败的那一行会先 `continue`，所以凡是出现在 `partitions[]` 里的
//   分区都带真实日期。⇒ 客户端可以直接用，不必写零值兜底。
//   ★ 这是先推断、再追到写入方、最后确认「假设不成立」的又一处；
//     与 §11.57 的 `finished_at` 同款。**推出来的坑必须追到代码才能写进契约。**

// ── storage/tables ─────────────────────────────────────────────────────────

export const STORAGE_TABLES_LIMIT_DEFAULT = 20
export const STORAGE_TABLES_LIMIT_MAX = 200

export interface TableSizeInfo {
  table: string
  schema: string
  /** ★ **planner 估计值**（`n_live_tup`），不是精确 COUNT。见 (3)。 */
  rows: number
  total_bytes: number
  total_human: string
  index_bytes: number
  toast_bytes: number
  toast_human: string
  /** ★ **榜内占比**（分母是返回行之和），不是占全库。见 (2)。 */
  percent_of_db: number
  /** ★ true = 分区父表，其体积**已包含**子分区。见 (4)。 */
  is_partitioned: boolean
}

export interface TableSizesResponse {
  tables: TableSizeInfo[]
  /** ★ **返回行之和**，不是整库大小。见 (1)。 */
  total_bytes: number
  total_human: string
  collected_at: string
}

export interface StorageTablesParams {
  limit?: number
}

export function fetchStorageTableSizes(
  params: StorageTablesParams = {},
  options?: RequestOptions,
): Promise<TableSizesResponse> {
  const qs = new URLSearchParams()
  // ★ 只发 1..200：越界静默回落 20（不是 400）。
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    if (n >= 1 && n <= STORAGE_TABLES_LIMIT_MAX) qs.set('limit', String(n))
  }
  const s = qs.toString()
  return req<TableSizesResponse>('GET', `/api/admin/data-lifecycle/storage/tables${s ? '?' + s : ''}`, undefined, options)
}

/** ★ `rows` 是统计估计值。 */
export const STORAGE_ROWS_ARE_ESTIMATES = true
/** ★ `total_bytes` / `total_human` 是**榜内之和**，不是整库。 */
export const STORAGE_TOTAL_IS_TOPN_SUM = true
/** ★ `percent_of_db` 的分母是榜内之和。 */
export const STORAGE_PERCENT_IS_TOPN_RELATIVE = true
/** ★ 分区父表与分区行体积重叠，不可相加。 */
export const STORAGE_MAY_DOUBLE_COUNT_PARTITIONS = true

/** 榜是否被截断（响应**不回显 limit**，只能用「拿满上限」反推）。 */
export function storageAtCap(resp: TableSizesResponse | null | undefined, requestedLimit?: number): boolean {
  if (!resp) return false
  const lim =
    typeof requestedLimit === 'number' && Number.isFinite(requestedLimit)
      ? Math.trunc(requestedLimit)
      : STORAGE_TABLES_LIMIT_DEFAULT
  if (lim < 1) return false
  return (resp.tables?.length ?? 0) >= lim
}

// ── partitions ─────────────────────────────────────────────────────────────

/** ★ 后端用 `-1` 表示「行数未知」，不是负数行数。见 (6)。 */
export const PARTITION_ROW_COUNT_UNKNOWN = -1

export interface PartitionInfo {
  partition_name: string
  /** 由分区名是否含 `_archive_` **推断**，不是查目录。见 (7)。 */
  parent_table: string
  start_date: string
  end_date: string
  /** ★ `-1` = 未知。见 (6)。 */
  row_count: number
  size_bytes: number
  size_human: string
  is_archived: boolean
  is_columnar: boolean
  can_archive: boolean
}

export interface PartitionTableStatus {
  table_name: string
  description: string
  /** ★ 解析边界失败的分区会**整条消失**，所以这个数未必是真实分区总数。见「另有一条静默丢行」。 */
  total_partitions: number
  /** ★ 把 columnar 的也算成「已归档」。见 (7)。 */
  archived_count: number
  archivable_count: number
  total_rows: number
  total_size_bytes: number
  total_size_human: string
  partitions: PartitionInfo[]
  has_archive_func: boolean
  archive_table_name: string
}
export function fetchPartitionStatuses(options?: RequestOptions): Promise<PartitionTableStatus[]> {
  return req<PartitionTableStatus[]>('GET', '/api/admin/data-lifecycle/partitions', undefined, options)
}

/** ★ 行数是否未知（后端哨兵 -1）。见 (6)。 */
export function partitionRowCountUnknown(v: number | null | undefined): boolean {
  return v === PARTITION_ROW_COUNT_UNKNOWN
}

/** ★ `archived_count` 含 columnar，不等于归档表里的真实成员数。见 (7)。 */
export const PARTITION_ARCHIVED_COUNT_INCLUDES_COLUMNAR = true

/**
 * 分区是否计入 `archived_count`。
 * 与后端 `if IsArchived || IsColumnar` **逐字同口径**（同族收口，不各写一份）。
 */
export function partitionCountsAsArchived(p: PartitionInfo | null | undefined): boolean {
  return !!p && (p.is_archived === true || p.is_columnar === true)
}

/** 可归档分区数（后端口径：非归档 且 结束日期早于两个月前）。 */
export function partitionArchivable(p: PartitionInfo | null | undefined): boolean {
  return !!p && p.can_archive === true
}

/** 父表与其分区体积是否**重叠**（不能相加）。见 (4)。 */
export function tableOverlapsItsPartitions(t: TableSizeInfo | null | undefined): boolean {
  return t?.is_partitioned === true
}