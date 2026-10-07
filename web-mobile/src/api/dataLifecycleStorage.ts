import { req, type RequestOptions } from './client'
import { STORAGE_TABLES_LIMIT_DEFAULT, STORAGE_TABLES_LIMIT_MAX } from './dataLifecycle'

/**
 * dataLifecycleStorage.ts — 存储总览 + 表级大小（2026-10-08，第八十批）。
 *
 * - **注册**（`admin/handler.go`）：
 *   - `:982` `admin(h.handleDataLifecycleStorage)` ⇒ GET `/api/admin/data-lifecycle/storage`
 *   - `:983` `admin(h.handleDataLifecycleTableSizes)` ⇒ GET `/api/admin/data-lifecycle/storage/tables`
 *   ⇒ 两个都是 **admin 档**（tenant_admin 可用）⇒ 抽屉席**不设** `requiresRole`。
 *   ⇒ ★ 同族 `:986-992` 的 vacuum / vacuum-full / reindex（六个）**全是 `h.superAdmin`**
 *     且**全是写操作**（锁表风险）⇒ 本模块**一条都不碰**。
 * - **实现**：`admin/data_lifecycle_storage.go`（`:131-229` 两个 handler，`:232-561` helpers）。
 * - **桌面调用方**：`web/src/api/tuning.ts:447`（overview）与 `:470`（tables）。
 *   ★ 桌面 `TableSizeInfo`（`tuning.ts:456-468`）**漏了后端的 `toast_human`**
 *     —— 本模块按后端 struct `data_lifecycle_storage.go:107` 逐字带上。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十三件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★ **`database` 是值类型字段，查询失败时留下的是「全零整块」，不是缺键。**
 *      `:140-152`，`storageOverview.Database` 是 `databaseStorageInfo`（值类型，无 omitempty）
 *      ```go
 *      if dbErr != nil { resp.Warnings = append(resp.Warnings, "数据库大小查询失败: "+dbErr.Error()) }
 *      else             { resp.Database = *dbInfo }
 *      ```
 *      ⇒ 失败时 `database` 键**照常存在**，`database_bytes/database_human/total_bytes/
 *        total_human/tables_bytes/indexes_bytes/toast_bytes/free_bytes` 全 0 / 全 `""`，
 *        且 `server_version` 因 omitempty **被省略**。
 *      ⇒ ★★ 判据：`database_human === ''` ⇔ 数据库查询失败
 *        （成功路径必然来自 `pg_size_pretty`，非空）。
 *      ⇒ ★ 与 `columnar` 的失败表达**不同构**：columnar 用 `note` 说明原因，
 *        database 只留零值 + `warnings` 一条。
 *
 * (2) ★★★★ **`database.free_bytes` 恒为 0、`database.free_human` 恒为 `""` —— 不是「空闲 0 字节」，是「测不到」。**
 *      `queryDatabaseStorage`（`:242-290`）**从未给这两个字段赋过值**，作者在 `:54` 自陈：
 *      ```go
 *      FreeBytes int64 `json:"free_bytes"` // 当前 connection 看不到 PG server 端 fs 剩余（仅客户端视角）
 *      ```
 *      ⇒ ★★ 这是**第十种 nil 编码：作者显式声明的「测不到」位**。
 *      ⇒ `free_human` 连 omitempty 都没有 ⇒ **恒发空串**。
 *      ⇒ ⇒ 客户端**禁止**把 `free_bytes` 渲染成「DB 剩余 0 B」；
 *        本模块**不提供**任何读它的判据函数（那是恒真判据，按纪律删），
 *        只用常量 `STORAGE_DB_FREE_IS_UNMEASURED` 把这条契约显式钉住。
 *
 * (3) ★★★★★ **同一个响应里有两套 humanize，单位字符串不重叠 ⇒ 可自验哪个是哪个。**
 *      | 字段 | 来源 | 实现 |
 *      |---|---|---|
 *      | `database_human` | `pg_size_pretty(pg_database_size(current_database()))`（`:249`） | **PostgreSQL 自带** |
 *      | 其余全部 `*_human` | Go `humanBytes()`（`:502-518`） | 本仓 1024 进制 |
 *      `humanBytes` 的单位串是 `"B","KB","MB","GB","TB","PB"`（**大写 K**）；
 *      PostgreSQL `pg_size_pretty` 的是 `"bytes","kB","MB","GB","TB"`（**小写 k**，且字节级是 `bytes`）。
 *      ⇒ ★★★ 于是判据 `storageDatabaseHumanIsGoStyle(db)` 能把「这个响应不是本端点的」挑出来。
 *      ⇒ `humanBytesGo()` 是**逐字复刻**（含两级 TrimRight 的顺序：先去尾 `0` 再去尾 `.`），
 *        用来核对除 `database_human` 外的每一个 human 串。
 *
 * (4) ★★★★ **`queryColumnarStorageSafe` 恒返回 `nil` error ⇒ `warnings` 里那条「列存统计查询失败」不可达。**
 *      `:296-303`：
 *      ```go
 *      if h == nil || h.db == nil { out.Note = "数据库连接未就绪"; return out, nil }
 *      return queryColumnarStorage(ctx, h), nil   // ← 恒 nil，内部失败只体现在 out.Note 上
 *      ```
 *      handler `:155-161` 的 `if colErr != nil` **永假** ⇒ 那是**不可达代码**。
 *      ⇒ ★★★ 列存失败的真实表达是 `columnar.note` 带 `"列存统计查询失败"` 前缀，
 *        **永远不会**出现在 `warnings` 里。
 *      ⇒ 与第七十六批 `probe_system`（`err` 被 `_ =` 丢弃 ⇒ 失败被算成 `healthy: true`）同型：
 *        那次是**静默成好**，这次是**静默留 note**。
 *
 * (5) ★★★★ `columnar.note` 是四个封闭取值 + 一个前缀，与 `available`/`total_human` 有可验关系。
 *      `queryColumnarStorage`（`:310-348`）四个出口：
 *      | 条件 | `available` | `total_human` | `note` |
 *      |---|---|---|---|
 *      | `h == nil \|\| h.db == nil` | false | `""` | `"数据库连接未就绪"` |
 *      | 扩展不存在 / ext 查询报错 | false | `""` | `"citus_columnar 扩展未安装"` |
 *      | 扫描报错 | **true** | `""` | `"列存统计查询失败: "+err` |
 *      | 扫描成功且 `table_count == 0` | true | `"0 B"` | `"尚无表使用列存（citus_columnar 已加载）"` |
 *      | 扫描成功且 `table_count > 0` | true | `humanBytes(total)` | **键被省略** |
 *      ⇒ ★★★ **`total_human !== ''` ⇔ 统计查询成功**（`:343` 的赋值在扫描成功之后）。
 *      ⇒ ★★★ 反向不成立：`available === true` **不**保证 `total_human` 非空
 *        （扫描失败时 `Available` 已被 `:323` 置 true，`:341` 直接 return，`TotalHuman` 留空）。
 *      ⇒ `total_human` **无 omitempty** ⇒ 恒发键，只是值可能为空串（**第十种编码的变体**）。
 *
 * (6) ★★★★ `warnings` 六个取值，三条是查询失败、三条是阈值判断；其中一条有**三个必要条件**。
 *      | 文案 | 触发条件 |
 *      |---|---|
 *      | `"数据库大小查询失败: "+err` | `queryDatabaseStorage` 返错（**含 `err.Error()`**） |
 *      | `"列存统计查询失败: "+err` | —— **★ 不可达**，见 (4) |
 *      | `"本机磁盘查询失败: "+err` | `queryFilesystem` 返错（**含 `err.Error()`**） |
 *      | `"数据库总大小超过本机磁盘容量的 5 倍 — 表明 DB 部署在独立节点"` | `db.TotalBytes>0 && fs.TotalBytes>0 && ratio>5.0` |
 *      | `"本机磁盘已用 ≥ 90%，请检查日志/缓存/附件"` | `fs.UsedPercent >= 90` |
 *      | `"本机日志目录已超过 5GB，建议调小 log.max_size_mb / log.max_age_days"` | `local_logs!=nil && exists && size_bytes > 5<<30` |
 *      ⇒ ★ 5 倍那条是**严格大于**（ratio 恰为 5.0 不触发），且 `db>0` 与 `fs>0` 缺一不可
 *        （任一为 0 时 `ratio` 无意义或被 `:180` 的短路挡掉）⇒ 判据**三个必要条件各一条用例**。
 *      ⇒ ★ 5GB 那条是 `5<<30` = **5368709120** 严格大于。
 *      ⇒ ★ `warnings` 在 `:141` 显式 `[]string{}` ⇒ **恒数组**，零条时是 `[]` 不是 `null`。
 *
 * (7) ★★★★ `filesystem.free_bytes` 与 `used_bytes` **取自 statfs 的不同字段** ⇒ 两者不互补。
 *      `:356-362`：
 *      ```go
 *      totalU, availU, freeU := statfsBytes(abs)  // (Blocks*Bsize, Bavail*Bsize, Bfree*Bsize)
 *      free := int64(availU)                      // ← free_bytes = Bavail（对非特权进程可用）
 *      used := total - int64(freeU)               // ← used_bytes = total − Bfree（含 root 保留块）
 *      ```
 *      `diskusage_unix.go:14` 证实返回的是 `Bavail` / `Bfree` 两套数。
 *      ⇒ ★★★ **`used_bytes + free_bytes !== total_bytes`**，差额正是「预留给 root 的块」
 *        `(Bfree − Bavail) * Bsize`，由 `storageFilesystemReservedBytes()` 读出。
 *      ⇒ ★ 与 (2) 的 database 块「恒 0」是**两个不同性质**：这一个是**口径不齐**，
 *        那一个是**根本测不到**。两者都不许当成「剩余空间」。
 *
 * (8) ★★★ `local_logs` 是**指针 + omitempty**；`directoryInfo` 九个键恒在但有两个可空串。
 *      `:117` `LocalLogs *directoryInfo json:"local_logs,omitempty"` ⇒ `resolveLogDir` 失败时**键被省略**。
 *      ⇒ ★ `resolveLogDir`（`:473-499`）三条路径都成功，兜底是 `cwd` ⇒ **实际恒在**，
 *        但解包器仍按**条件键**处理（struct tag 声明它可缺）。
 *      ⇒ ★★ `queryDirectory`（`:380-409`）：`os.Stat` 失败或不是目录 ⇒ 提前 return，
 *        此时 `size_human` **保持 `""`**（`:407` 的赋值在其后）
 *        ⇒ **`size_human === ''` ⇔ `exists === false`**。
 *      ⇒ ★ 同函数 `:381` 是 `abs, _ := filepath.Abs(path)` —— **错误被丢弃** ⇒ 极端情况下 `path` 可能是 `""`。
 *      ⇒ ★ `exists: true` 但目录为空 ⇒ `size_human: "0 B"`、`files: 0`、两个 mtime 都是 `0`
 *        ⇒ **`oldest_mtime === 0` ⇔ `files === 0`**；且 `oldest_mtime <= newest_mtime` 恒成立。
 *
 * (9) ★★★ `database.total_bytes` 有一处**静默兜底**，而注释里的不等式方向其实是错的。
 *      `:259-269`：SUM 查询失败 ⇒ `out.TotalBytes = out.DatabaseBytes`。
 *      ⇒ ★★ 这是**第十一种编码：静默兜底** ⇒ `total_bytes === database_bytes` 时**不能**断定「整库就等于关系和」。
 *      ⇒ ★★★ `:46` 的注释写「通常 DatabaseBytes ≤ TotalBytes」，**代码不保证这个方向**：
 *        `pg_database_size` 算**全库**，而 SUM 的 WHERE（`:263`）把 `pg_catalog` /
 *        `information_schema` **排除**了 ⇒ **`database_bytes` 可以大于 `total_bytes`**。
 *        由 `storageDatabaseExceedsRelationSum()` 读出，UI 才不会显示成「关系和 > 整库」的怪数。
 *
 * (10) ★★ `collected_at` 是 `time.Now().UTC()`（`:142` / `:227`）⇒ RFC3339Nano 且**以 `Z` 结尾**。
 *      ⇒ 本地时区绝不会出现 `+08:00` ⇒ `storageOverviewIsUtc()` 是可自验的。
 *      ⇒ 桌面 `dataLifecycleTableSizes` 忽略 `collected_at`；移动端用它判数据新鲜度。
 *
 * (11) ★★ `limit` 用 `strconv.Atoi` ⇒ **静默回落 20，不是 400**。
 *      `:206-211`：
 *      ```go
 *      limit := 20
 *      if v := r.URL.Query().Get("limit"); v != "" {
 *          if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 { limit = n }
 *      }
 *      ```
 *      ⇒ ★ 三个必要条件：`Atoi` 不报错 **且** `n > 0` **且** `n <= 200`。
 *      ⇒ ★★ `strconv.Atoi` ≡ `ParseInt(s, 10, 0)` ⇒ **`" 20"`（前导空格）报错**、`"0x14"` 报错、
 *        `"20.0"` 报错 ⇒ 都回落 20；`"+20"` 接受。
 *        **不能**用 JS `Number()` 直译（第七十六批已踩过：`" 7"` 在 Go 报错、在 JS 得 7）。
 *      ⇒ ★ 与第七十八批 `/api/admin/probe/tasks`（非法 limit ⇒ **400**）是同仓**两种风格并存**的又一例；
 *        与第七十六批 `/api/self-check/runs`（静默回落 50）同款。
 *      ⇒ ★ 响应**不回显 limit** ⇒ 截断只能靠 `storageAtCap`（既有模块）反推。
 *
 * (12) ★★★ `/storage/tables` 只有**一条**错误路径，且两条子路径文案完全相同。
 *      `:216-221`：`writeInternalErr(w, "查询表大小失败", err)`
 *      `internal_error.go:46-49` ⇒ 500 + `writeError` ⇒ 形状是**嵌套**信封
 *      `{"error":{"detail":"查询表大小失败"}}`（**无 `code` 键**），
 *      且 `internal_error.go` 头注释明确「客户端只见到 op …**绝不携带 `err.Error()`**」。
 *      ⇒ ★★ 两条子路径（`Query` 失败 `:432-434` / `rows.Err()` `:455-457`）**文案逐字相同**
 *        ⇒ 客户端**不可区分**。
 *      ⇒ ★ 方法不是 GET ⇒ 405 `{"error":{"detail":"method not allowed"}}`（`:132-135` / `:201-204`）。
 *      ⇒ ★ `writeJSON` 的 marshal 兜底体（`handler.go:1487`）也是同一嵌套形状
 *        （`{"error":{"detail":"json marshal failed"}}`）。
 *
 * (13) ★★★★ `tables` 是恒数组；**行扫描失败静默跳过** ⇒ `total_bytes` 恒等于**留存行之和**。
 *      `:437` `out := make([]tableSizeInfo, 0, limit)` ⇒ 零行时是 `[]`，**绝不是 `null`**。
 *      `:441-448`：
 *      ```go
 *      if err := rows.Scan(...); err != nil { warnRowSkip("dataLifecycle.storage.tableSizes", err); continue }
 *      ```
 *      ⇒ ★★★ 整行从 `tables` 里消失，但 `totalBytes` 只在**留存**时累加（`:451`）
 *        ⇒ **强不变量 `total_bytes === Σ tables[].total_bytes` 恒成立**（无论跳过多少行）。
 *      ⇒ ★ `percent_of_db` 同样只对留存行计算（`:460-464`）⇒ 重算也恒吻合。
 *      ⇒ ★ 排序是 `ORDER BY pg_total_relation_size(c.oid) DESC`（`:429`）⇒ **按 `total_bytes` 非增**；
 *        并列行的次序由 PG 决定 ⇒ 客户端**只能**断非增，不能断「严格递减」。
 *      ⇒ ★ 与 (7)/(9) 同族的一处口径差：这里的 schema 排除列表只有
 *        `('pg_catalog','information_schema')`，而 `queryColumnarStorage`（`:335-337`）**还额外排除**
 *        `citus` / `citus_internal` / `columnar` / `columnar_internal` ⇒ **同族两个查询的排除列表不同**。
 */

// ── 常量（后端字面量） ───────────────────────────────────────────────────────

/** `:206` `limit := 20` */
export const STORAGE_TABLES_LIMIT_FALLBACK = STORAGE_TABLES_LIMIT_DEFAULT
/** `:208` `n > 0 && n <= 200` */
export const STORAGE_TABLES_LIMIT_SEND_MIN = 1
/** `:208` `n <= 200` */
export const STORAGE_TABLES_LIMIT_SEND_MAX = STORAGE_TABLES_LIMIT_MAX
/** `:182` `ratio > 5.0` 的阈值 5 */
export const STORAGE_DB_OVER_DISK_RATIO = 5
/** `:187` `fs.UsedPercent >= 90` */
export const STORAGE_FS_DISK_ALERT_PERCENT = 90
/** `:190` `SizeBytes > 5<<30` */
export const STORAGE_LOGS_ALERT_BYTES = 5 * 1024 * 1024 * 1024 // 5368709120

/** ★ 见 (2)：`database.free_*` 是「测不到」，**恒 0 / 恒空串**。禁止渲染成真实值。 */
export const STORAGE_DB_FREE_IS_UNMEASURED = true
/** ★ 见 (4)：「列存统计查询失败」这条 warning 分支**不可达**。 */
export const STORAGE_WARNING_COLUMNAR_FAILED_IS_UNREACHABLE = true

/** `columnar.note` 的四个封闭取值（`noteKind` 分类用）。 */
export const STORAGE_COLUMNAR_NOTE_DB_NOT_READY = '数据库连接未就绪'
export const STORAGE_COLUMNAR_NOTE_EXT_MISSING = 'citus_columnar 扩展未安装'
export const STORAGE_COLUMNAR_NOTE_NO_TABLES = '尚无表使用列存（citus_columnar 已加载）'
/** `note` 的「查询失败」形态是**前缀** + `err.Error()`（`:340`），不是封闭取值。 */
export const STORAGE_COLUMNAR_NOTE_QUERY_FAILED_PREFIX = '列存统计查询失败: '

export const STORAGE_WARNING_DB_FAILED_PREFIX = '数据库大小查询失败: '
export const STORAGE_WARNING_FS_FAILED_PREFIX = '本机磁盘查询失败: '
export const STORAGE_WARNING_DB_OVER_DISK =
  '数据库总大小超过本机磁盘容量的 5 倍 — 表明 DB 部署在独立节点'
export const STORAGE_WARNING_FS_DISK_ALERT = '本机磁盘已用 ≥ 90%，请检查日志/缓存/附件'
export const STORAGE_WARNING_LOGS_ALERT =
  '本机日志目录已超过 5GB，建议调小 log.max_size_mb / log.max_age_days'

/** 见 (1)：`database` 整块全零 ⇔ 查询失败。 */
export const STORAGE_DATABASE_KEYS = [
  'database_bytes',
  'database_human',
  'total_bytes',
  'total_human',
  'tables_bytes',
  'indexes_bytes',
  'toast_bytes',
  'free_bytes',
  'free_human',
] as const

/** 见 (5)：`columnar` 五个键恒在（`note` 除外，omitempty）。 */
export const STORAGE_COLUMNAR_KEYS = [
  'available',
  'table_count',
  'total_columns',
  'total_bytes',
  'total_human',
] as const
export const STORAGE_COLUMNAR_OPTIONAL_KEYS = ['note'] as const

export const STORAGE_FILESYSTEM_KEYS = [
  'path',
  'total_bytes',
  'total_human',
  'used_bytes',
  'used_human',
  'free_bytes',
  'free_human',
  'used_percent',
] as const

export const STORAGE_LOCAL_DIR_KEYS = [
  'path',
  'exists',
  'files',
  'size_bytes',
  'size_human',
  'oldest_mtime',
  'newest_mtime',
] as const

export const STORAGE_OVERVIEW_KEYS = [
  'database',
  'columnar',
  'filesystem',
  'warnings',
  'collected_at',
] as const
export const STORAGE_OVERVIEW_OPTIONAL_KEYS = ['local_logs'] as const

/**
 * `tableSizeInfo`（`:99-110`）的十个键 —— **恒在**，
 * 含桌面 `tuning.ts` 漏掉的 `toast_human`（`:107`）。
 */
export const STORAGE_TABLE_ROW_KEYS = [
  'table',
  'schema',
  'rows',
  'total_bytes',
  'total_human',
  'index_bytes',
  'toast_bytes',
  'toast_human',
  'percent_of_db',
  'is_partitioned',
] as const

export const STORAGE_TABLE_SIZES_KEYS = ['tables', 'total_bytes', 'total_human', 'collected_at'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface DatabaseStorageInfo {
  database_bytes: number
  /** ★ PostgreSQL `pg_size_pretty` 风格（`"1 kB"` / `"512 bytes"`），**不是**本仓 humanBytes。见 (3)。 */
  database_human: string
  total_bytes: number
  total_human: string
  tables_bytes: number
  indexes_bytes: number
  toast_bytes: number
  /** ★ **恒 0** —— 「测不到」，不是空闲 0 字节。见 (2)。 */
  free_bytes: number
  /** ★ **恒 `""`** —— 恒发但无值。见 (2)。 */
  free_human: string
  /** `current_setting('server_version')`，非空 ⇒ 恒在；查询失败时**键被省略**。见 (1)。 */
  server_version?: string
}

export type ColumnarNoteKind =
  | 'none'
  | 'db_not_ready'
  | 'ext_missing'
  | 'query_failed'
  | 'no_tables'
  /** ★ 不可达：后端只写那四个字面量。真见到时**不**静默归 `none`（会伪装成「统计成功」）。 */
  | 'unknown'

export interface ColumnarStorageInfo {
  available: boolean
  table_count: number
  total_columns: number
  total_bytes: number
  /** ★ 恒发键；统计查询失败时为 `""`。见 (5)。 */
  total_human: string
  note?: string
}

export interface FilesystemInfo {
  path: string
  total_bytes: number
  total_human: string
  used_bytes: number
  used_human: string
  /** ★ `Bavail`（非特权可用），与 `used_bytes` **不互补**。见 (7)。 */
  free_bytes: number
  free_human: string
  /** ★ `int(used*100/total)`，**整数除法截断**。见 (7)。 */
  used_percent: number
}

export interface LocalDirInfo {
  path: string
  exists: boolean
  files: number
  size_bytes: number
  /** ★ `''` ⇔ `exists === false`。见 (8)。 */
  size_human: string
  /** ★ unix 秒；`0` ⇔ `files === 0`。见 (8)。 */
  oldest_mtime: number
  /** ★ unix 秒。 */
  newest_mtime: number
}

export interface StorageOverview {
  database: DatabaseStorageInfo
  columnar: ColumnarStorageInfo
  filesystem: FilesystemInfo
  /** ★ 指针 + omitempty ⇒ `resolveLogDir` 失败时**键被省略**。见 (8)。 */
  local_logs?: LocalDirInfo
  /** ★ 恒数组（`:141` `[]string{}`）。 */
  warnings: string[]
  collected_at: string
}

export interface StorageTableRow {
  table: string
  schema: string
  rows: number
  total_bytes: number
  total_human: string
  index_bytes: number
  toast_bytes: number
  /** ★ 桌面 `tuning.ts` 漏掉这个键（后端 `:107` 有）。 */
  toast_human: string
  /** ★ 榜内占比，分母是**返回行之和**（`:462`）。 */
  percent_of_db: number
  is_partitioned: boolean
}

export interface StorageTableSizes {
  tables: StorageTableRow[]
  /** ★ 返回行之和（`:451`），不是整库。 */
  total_bytes: number
  total_human: string
  collected_at: string
}

// ── humanBytes 逐字复刻（`:502-518`） ───────────────────────────────────────

const STORAGE_HUMAN_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const

/** `:524-544` formatInt：手写十进制，n=0 ⇒ "0"，负数带 `-`。 */
function formatIntGo(n: number): string {
  // ★★ 这里的 `if (n === 0) return '0'` 在 JS 里是**可证冗余**的：
  //   `String(Math.abs(Math.trunc(0)))` 天然就是 `'0'`。
  //   Go 那边必须有它（buf 是固定长度数组，循环 `for n > 0` 一次都不进 ⇒ 切出空串）。
  // ⇒ 保留是**为了与 Go 逐字对齐**，不是为了让判据有牙；
  //   删掉它不改变任何输出（变异 #27 实测 STILL_GREEN 属可证等价）。
  if (n === 0) return '0'
  const neg = n < 0
  const abs = neg ? -n : n
  const digits = String(Math.abs(Math.trunc(abs)))
  return neg ? '-' + digits : digits
}

/** `:546-554` formatFloat：保留 1 位小数，frac 是 `int64((v-int)*10)` ⇒ **截断不是四舍五入**。 */
function formatFloatGo(v: number): string {
  const intPart = Math.trunc(v)
  let frac = Math.trunc((v - intPart) * 10)
  if (frac < 0) frac = -frac
  return formatIntGo(intPart) + '.' + formatIntGo(frac)
}

/**
 * Go `humanBytes` 的逐字复刻（`data_lifecycle_storage.go:502-518`）。
 *
 * ★ 两级 `TrimRight` 的**顺序**是关键（`:516-517`）：
 * ```go
 * strings.TrimRight(strings.TrimRight(formatFloat(v), "0"), ".")
 * ```
 * ⇒ `1.0` ⇒ `"1.0"` ⇒ 去尾 `0` ⇒ `"1."` ⇒ 去尾 `.` ⇒ `"1"` ⇒ `"1 KB"`（**无小数点**）
 * ⇒ `1.25` ⇒ `"1.2 KB"`（**截断**，不是 1.3）
 */
export function humanBytesGo(n: number): string {
  if (n < 0) return '0 B'
  let v = n
  let i = 0
  while (v >= 1024 && i < STORAGE_HUMAN_UNITS.length - 1) {
    v /= 1024
    i++
  }
  if (i === 0) return formatIntGo(Math.trunc(v)) + ' ' + STORAGE_HUMAN_UNITS[i]
  const trimmed = (formatFloatGo(v).replace(/0+$/, '') as string).replace(/\.$/, '')
  return trimmed + ' ' + STORAGE_HUMAN_UNITS[i]
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/data-lifecycle/storage`（`handler.go:982`，admin 档）。 */
export function fetchStorageOverview(options?: RequestOptions): Promise<StorageOverview> {
  return req<unknown>('GET', '/api/admin/data-lifecycle/storage', undefined, options).then(
    unwrapStorageOverview,
  )
}

/**
 * GET `/api/admin/data-lifecycle/storage/tables`（`handler.go:983`，admin 档）的**强校验**版。
 *
 * ★ 既有 `dataLifecycle.ts` 的 `fetchStorageTableSizes` 走 `req<T>` 直接信任响应；
 *   本函数额外做形状校验（十键逐项 + 类型），形状不符**抛错**而不是把坏数据交给 UI。
 * ★ `limit` 只在 `1..200` 时发出（`:208` 的三个必要条件）⇒ 越界干脆不发，
 *   由后端静默回落到 20（见 (11)）。
 */
export function fetchStorageTableSizesChecked(
  params: { limit?: number } = {},
  options?: RequestOptions,
): Promise<StorageTableSizes> {
  const qs = new URLSearchParams()
  if (storageTablesLimitIsSendable(params.limit)) qs.set('limit', String(Math.trunc(params.limit!)))
  const s = qs.toString()
  return req<unknown>(
    'GET',
    `/api/admin/data-lifecycle/storage/tables${s ? '?' + s : ''}`,
    undefined,
    options,
  ).then(unwrapStorageTableSizes)
}

/**
 * 见 (11)：`strconv.Atoi` 不报错 **且** `n > 0` **且** `n <= 200` 才发得出去。
 *
 * ★ `Number.isFinite` 这一项在当前实现里是**可证冗余**的（变异 #60 实测 STILL_GREEN）：
 *   `NaN` 被 `n >= 1` 挡住（NaN 参与的比较恒假），`±Infinity` 被 `n <= 200` 挡住。
 *   保留它是因为它是**可读的显式条件**，且 Go 侧 `strconv.Atoi` 的确有溢出报错语义
 *   （`ErrRange`）——将来若换成透传原始字符串，这条会立刻变得必要。
 */
export function storageTablesLimitIsSendable(limit: number | null | undefined): boolean {
  if (typeof limit !== 'number' || !Number.isFinite(limit)) return false
  const n = Math.trunc(limit)
  return n >= STORAGE_TABLES_LIMIT_SEND_MIN && n <= STORAGE_TABLES_LIMIT_SEND_MAX
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapStorageOverview(resp: unknown): StorageOverview {
  const d = requireObject(resp, '存储总览')
  requireKeys(d, STORAGE_OVERVIEW_KEYS, '存储总览')
  requireDatabase(d['database'])
  requireColumnar(d['columnar'])
  requireFilesystem(d['filesystem'])
  // ★ 指针 + omitempty ⇒ 条件键；键在时才校验。
  if ('local_logs' in d) requireLocalDir(d['local_logs'], '存储总览 的 local_logs')
  if (!Array.isArray(d['warnings'])) throw new Error('存储总览 的 warnings 不是数组')
  if (typeof d['collected_at'] !== 'string') throw new Error('存储总览 的 collected_at 不是字符串')
  return d as unknown as StorageOverview
}

export function unwrapStorageTableSizes(resp: unknown): StorageTableSizes {
  const d = requireObject(resp, '表级大小')
  requireKeys(d, STORAGE_TABLE_SIZES_KEYS, '表级大小')
  if (!Array.isArray(d['tables'])) throw new Error('表级大小 的 tables 不是数组')
  d['tables'].forEach((row, i) => requireTableRow(row, `表级大小 的 tables[${i}]`))
  if (typeof d['total_bytes'] !== 'number') throw new Error('表级大小 的 total_bytes 不是数字')
  if (typeof d['total_human'] !== 'string') throw new Error('表级大小 的 total_human 不是字符串')
  if (typeof d['collected_at'] !== 'string') throw new Error('表级大小 的 collected_at 不是字符串')
  return d as unknown as StorageTableSizes
}

function requireDatabase(v: unknown): void {
  const where = '存储总览 的 database'
  const d = requireObject(v, where)
  requireKeys(d, STORAGE_DATABASE_KEYS, where)
  for (const k of [
    'database_bytes',
    'total_bytes',
    'tables_bytes',
    'indexes_bytes',
    'toast_bytes',
    'free_bytes',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['database_human', 'total_human', 'free_human'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  // ★ `server_version` 是 omitempty 条件键；非空字面量 ⇒ 存在时必为字符串。
  if ('server_version' in d && typeof d['server_version'] !== 'string') {
    throw new Error(`${where} 的 server_version 不是字符串`)
  }
}

function requireColumnar(v: unknown): void {
  const where = '存储总览 的 columnar'
  const d = requireObject(v, where)
  requireKeys(d, STORAGE_COLUMNAR_KEYS, where)
  if (typeof d['available'] !== 'boolean') throw new Error(`${where} 的 available 不是布尔`)
  for (const k of ['table_count', 'total_columns', 'total_bytes'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  if (typeof d['total_human'] !== 'string') throw new Error(`${where} 的 total_human 不是字符串`)
  // ★ `note` 是 omitempty 条件键。
  if ('note' in d && typeof d['note'] !== 'string') throw new Error(`${where} 的 note 不是字符串`)
}

function requireFilesystem(v: unknown): void {
  const where = '存储总览 的 filesystem'
  const d = requireObject(v, where)
  requireKeys(d, STORAGE_FILESYSTEM_KEYS, where)
  if (typeof d['path'] !== 'string') throw new Error(`${where} 的 path 不是字符串`)
  for (const k of ['total_bytes', 'used_bytes', 'free_bytes', 'used_percent'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['total_human', 'used_human', 'free_human'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
}

function requireLocalDir(v: unknown, where: string): void {
  const d = requireObject(v, where)
  requireKeys(d, STORAGE_LOCAL_DIR_KEYS, where)
  if (typeof d['path'] !== 'string') throw new Error(`${where} 的 path 不是字符串`)
  if (typeof d['exists'] !== 'boolean') throw new Error(`${where} 的 exists 不是布尔`)
  for (const k of ['files', 'size_bytes', 'oldest_mtime', 'newest_mtime'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  if (typeof d['size_human'] !== 'string') throw new Error(`${where} 的 size_human 不是字符串`)
}

function requireTableRow(v: unknown, where: string): void {
  const d = requireObject(v, where)
  requireKeys(d, STORAGE_TABLE_ROW_KEYS, where)
  for (const k of ['table', 'schema'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  for (const k of ['rows', 'total_bytes', 'index_bytes', 'toast_bytes', 'percent_of_db'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['total_human', 'toast_human'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if (typeof d['is_partitioned'] !== 'boolean') throw new Error(`${where} 的 is_partitioned 不是布尔`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

/** 见 (1)：`database_human === ''` ⇔ 数据库查询失败（成功路径必然非空）。 */
export function storageDatabaseQueryFailed(r: StorageOverview): boolean {
  return r.database.database_human === ''
}

/** 见 (1)：整块全零（含 `server_version` 缺键）——「失败」的完整形状。 */
export function storageDatabaseBlockIsAllZero(db: DatabaseStorageInfo): boolean {
  return (
    db.database_bytes === 0 &&
    db.database_human === '' &&
    db.total_bytes === 0 &&
    db.total_human === '' &&
    db.tables_bytes === 0 &&
    db.indexes_bytes === 0 &&
    db.toast_bytes === 0 &&
    db.free_bytes === 0 &&
    db.free_human === '' &&
    db.server_version === undefined
  )
}

/** 见 (9)：`database_bytes` 可以**大于** `total_bytes`（`pg_catalog` 被 SUM 排除）。 */
export function storageDatabaseExceedsRelationSum(db: DatabaseStorageInfo): boolean {
  return db.database_bytes > db.total_bytes
}

/** 见 (9)：SUM 兜底时两者必然相等（`:268`）。 */
export function storageTotalFellBackToDatabaseSize(db: DatabaseStorageInfo): boolean {
  return db.total_bytes !== 0 && db.total_bytes === db.database_bytes
}

/** 见 (5)：`note` 分类。`none` ⇔ 键被省略。 */
export function storageColumnarNoteKind(c: ColumnarStorageInfo): ColumnarNoteKind {
  const note = c.note
  if (note === undefined) return 'none'
  if (note === STORAGE_COLUMNAR_NOTE_DB_NOT_READY) return 'db_not_ready'
  if (note === STORAGE_COLUMNAR_NOTE_EXT_MISSING) return 'ext_missing'
  if (note.startsWith(STORAGE_COLUMNAR_NOTE_QUERY_FAILED_PREFIX)) return 'query_failed'
  if (note === STORAGE_COLUMNAR_NOTE_NO_TABLES) return 'no_tables'
  // ★ 不可达分支（后端只写这四个）：真见到别的字面量时**不**静默归 `none`，
  //   否则 UI 会以为「统计成功且无 note」⇒ 返回 `unknown` 让调用方显式处理。
  return 'unknown'
}

/** 见 (5)：`total_human !== ''` ⇔ 统计查询成功（`:343` 的赋值在扫描成功之后）。 */
export function storageColumnarQuerySucceeded(c: ColumnarStorageInfo): boolean {
  return c.total_human !== ''
}

/** 见 (5)：`available === true` **不**保证统计成功（扫描失败时 `Available` 已置 true）。 */
export function storageColumnarAvailableButUnmeasured(c: ColumnarStorageInfo): boolean {
  return c.available === true && c.total_human === ''
}

/** 见 (6)：`warnings` 逐条分类。 */
export function storageWarningKind(w: string): string {
  if (w.startsWith(STORAGE_WARNING_DB_FAILED_PREFIX)) return 'db_failed'
  if (w.startsWith(STORAGE_COLUMNAR_NOTE_QUERY_FAILED_PREFIX)) return 'columnar_failed'
  if (w.startsWith(STORAGE_WARNING_FS_FAILED_PREFIX)) return 'fs_failed'
  if (w === STORAGE_WARNING_DB_OVER_DISK) return 'db_over_disk'
  if (w === STORAGE_WARNING_FS_DISK_ALERT) return 'fs_disk_alert'
  if (w === STORAGE_WARNING_LOGS_ALERT) return 'logs_alert'
  return 'unknown'
}

/** 见 (6)：取某一条 warning（按分类）。取不到返回 `undefined`。 */
export function storageWarningOfKind(r: StorageOverview, kind: string): string | undefined {
  return r.warnings.find((w) => storageWarningKind(w) === kind)
}

/**
 * 见 (6)：5 倍告警的判定，`db > 0` 与 `fs > 0` **两个必要条件**缺一不可（`:180` 短路）。
 * 阈值是**严格大于** 5。
 *
 * ★ 两个条件的**作用不对称**（变异 #48 / #49 实测）：
 *   - `fs > 0` 去掉 ⇒ `db/0` = `Infinity` ⇒ 输出彻底错 ⇒ **有牙**。
 *   - `db > 0` 去掉 ⇒ **完全冗余**：db=0 且 fs>0 时 `0/fs` 数学上恒为 0；
 *     db=0 且 fs=0 时又被 `fs > 0` 那一项挡在前面（返回 0，不会漏出 `0/0 = NaN`）。
 *     ⇒ 任何夹具下输出都不变 ⇒ 属**可证等价变异**。
 *     保留它是照抄后端 `:180` 的显式条件（那段 `&&` 两项各写各的，读起来是对称的）。
 */
export function storageDbOverDiskRatio(r: StorageOverview): number {
  if (r.database.total_bytes <= 0 || r.filesystem.total_bytes <= 0) return 0
  return r.database.total_bytes / r.filesystem.total_bytes
}

/** 见 (7)：`total − used − free` = 预留给 root 的块（`Bfree − Bavail`）。**不是 0。** */
export function storageFilesystemReservedBytes(fs: FilesystemInfo): number {
  return fs.total_bytes - fs.used_bytes - fs.free_bytes
}

/**
 * 见 (8)：`size_human === ''` ⇔ `exists === false`（`:384-387` 提前 return，`:407` 未执行）。
 *
 * ★ `local_logs` 是**条件键**（指针 + omitempty）⇒ 这两个判据都吃 `undefined`：
 *   键缺失时 `size_human` 无从谈起，判据返回 `false` 而不是让调用方先判空。
 */
export function storageLocalDirUnavailable(d: LocalDirInfo | null | undefined): boolean {
  return d !== null && d !== undefined && d.size_human === ''
}

/** 见 (8)：`oldest_mtime === 0` ⇔ `files === 0`；有文件时两 mtime 都非零。 */
export function storageLocalDirHasNoFiles(d: LocalDirInfo | null | undefined): boolean {
  return d !== null && d !== undefined && d.oldest_mtime === 0
}

/** 见 (8)：mtime 序不倒置。 */
export function storageLocalDirMtimesOrdered(d: LocalDirInfo | null | undefined): boolean {
  return d !== null && d !== undefined && d.oldest_mtime <= d.newest_mtime
}

/** 见 (10)：`collected_at` 是 `time.Now().UTC()` ⇒ 必以 `Z` 结尾。 */
export function storageOverviewIsUtc(iso: string): boolean {
  return iso.endsWith('Z')
}

/** 见 (3)：`database_human` 若落在本仓 humanBytes 的单位集里，说明这不是本端点的响应。 */
export function storageDatabaseHumanIsGoStyle(db: DatabaseStorageInfo): boolean {
  return /\d (B|KB|MB|GB|TB|PB)$/.test(db.database_human)
}

/** 见 (3)：`humanBytesGo` 与响应里 `total_human` 一致（供 UI 自检换算实现有没有漂）。 */
export function storageHumanMatches(bytes: number, human: string): boolean {
  return humanBytesGo(bytes) === human
}

// ── 表级大小判据 ────────────────────────────────────────────────────────────

/** 见 (13)：`total_bytes === Σ tables[].total_bytes` —— 跳过的行不计入，故恒成立。 */
export function tableSizesTotalMatchesRows(r: StorageTableSizes): boolean {
  return r.total_bytes === sumRows(r, 'total_bytes')
}

function sumRows(r: StorageTableSizes, field: 'total_bytes'): number {
  return r.tables.reduce((acc, row) => acc + row[field], 0)
}

/** 见 (13)：`total_human === humanBytesGo(total_bytes)`。 */
export function tableSizesHumanMatchesTotal(r: StorageTableSizes): boolean {
  return humanBytesGo(r.total_bytes) === r.total_human
}

/** 见 (3)：每行的两个 human 串也要与 `humanBytesGo` 吻合。 */
export function tableRowHumsMatch(r: StorageTableSizes): boolean {
  return r.tables.every(
    (row) => humanBytesGo(row.total_bytes) === row.total_human && humanBytesGo(row.toast_bytes) === row.toast_human,
  )
}

/** 见 (13)：按 `total_bytes` 非增（并列顺序由 PG 决定 ⇒ 不断「严格」）。 */
export function tableSizesIsDescending(r: StorageTableSizes): boolean {
  return r.tables.every((row, i) => i === 0 || r.tables[i - 1]!.total_bytes >= row.total_bytes)
}

/** 见 (13)：`percent_of_db` 用榜内和重算（`:462`），逐行吻合。 */
export function tableSizesPercentIsTopNRelative(r: StorageTableSizes): boolean {
  const total = sumRows(r, 'total_bytes')
  if (total <= 0) return r.tables.every((row) => row.percent_of_db === 0)
  return r.tables.every((row) => row.percent_of_db === Math.trunc((row.total_bytes * 100) / total))
}

/** 见 (13)：榜上有分区父表 ⇒ 与分区行体积**重叠**，不可相加（与既有 `dataLifecycle.ts` (4) 同源）。 */
export function tableSizesMayDoubleCount(r: StorageTableSizes): boolean {
  return r.tables.some((row) => row.is_partitioned)
}

// ── 尾部 helper ─────────────────────────────────────────────────────────────

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
