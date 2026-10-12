import { req, type RequestOptions } from './client'

/**
 * storageAndLogConfig.ts — 存储配置 + 日志轮转配置（2026-10-08，第九十三批）。
 *
 * GET /api/admin/storage/config · GET /api/admin/logs/config
 *
 * - **注册**：`admin/handler.go:1108` 与 `:1114`，**两行相邻、同一个 mux**：
 *   ```go
 *   mux.HandleFunc("/api/admin/storage/config", h.superAdmin(h.handleStorageConfig))  // :1108
 *   mux.HandleFunc("/api/admin/storage/config/test-path", h.superAdmin(h.handleStorageTestPath)) // :1109
 *   mux.HandleFunc("/api/admin/storage/migration-state", admin(h.handleMigrationState)) // :1111 ★ admin 档
 *   mux.HandleFunc("/api/admin/logs/config", h.superAdmin(h.handleLogConfig))        // :1114
 *   ```
 *   ⇒ ★★★★★ **`:1108` 与 `:1111` 相邻两行却是两种档位**（superAdmin vs admin）
 *   ⇒ ⇒ **整族（storage/config、logs/config）是 `h.superAdmin` 档**
 *     ⇒ tenant_admin 直接 403 ⇒ 抽屉席**必须**设 `requiresRole: 'super_admin'`。
 *   ⇒ ★★ 但同前缀下的 `storage/migration-state` 是 **admin 档**
 *     ⇒ **绝不能按 `/api/admin/storage/` 这个前缀推权限**。
 * - **实现**：`admin/storage_config.go`（570 行）· `admin/log_management.go`（669 行）。
 * - **桌面调用方**：`web/src/api/tuning.ts:798` 与 `:930` —— `req<StorageConfig>` /
 *   `req<LogConfig>` 直接强转，**不做任何校验** ⇒ 全部校验由本模块补上。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十六件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **两族整族 `h.superAdmin` 档**（见上）⇒ tenant_admin 403。
 * (2) ★★★★★ **`storage_config` 有 26 个 json tag：13 恒在 + 13 个 `omitempty`**。
 *     ★ 其中 **`s3_use_ssl` 是 `bool` + `omitempty`** ⇒ ★★★ **`false` 时键整个消失**
 *     ⇒ ⇒ **「键存在」等价于「已启用」**，`false` 与「没配 S3」在响应上不可区分。
 *     ⇒ ★ 与批 91 的 `model_routes`（键消失 / null / 数组三态）、
 *       批 92 的「键恒在但值为裸 `null`」并列为本仓第三种**可选键编码**。
 * (3) ★★★★★ **`log_config` 的 15 个键一个 `omitempty` 都没有**（`log_management.go:41-62`）
 *     ⇒ **恒在 15**。★ 与 (2) 恰好相反：同一个 admin 包、同一种「配置读取」语义，
 *       两个端点对「可选」的表达方式完全不同 ⇒ **解包器不能共用**。
 * (4) ★★★★★ **`config_source` 是「泄漏某一个键的来源」，不是整个响应的来源**。
 *     ```go
 *     resp.ConfigSource = "default"
 *     if storageType := readStringSetting("storage.type"); storageType != "" {
 *         resp.ConfigSource = "db"          // ★ 硬编码 db —— readStringSetting 丢掉了 src
 *     }
 *     if v, src := readIntSetting("storage.attachment_ttl_days"); src != "" {
 *         resp.ConfigSource = src           // ★ 只有这一个键的 src 会最终决定
 *     }
 *     ```
 *     ⇒ ★★★ 三个取值 `default` / `env` / `db` 都真实存在
 *       （`settings.EffectiveValue` 返回值，见 `settings/registry_test.go:126/145/169`），
 *       ★ **但它只描述 `storage.attachment_ttl_days`（或 `storage.type` 的存在性）**
 *     ⇒ ⇒ `ttl_days` 来自 env 而 `storage_type` 来自 db 时，
 *       `config_source` 报的是 **`env`** —— **不要用它给整张表单打「来源」标签**。
 *     ⇒ ★★ `readStringSetting` **返回类型里没有 src**（`:485-499`）⇒ 这条分支只能硬编码。
 * (5) ★★★★★ **`enabled_source` 的注释与代码矛盾**（`log_management.go:55` vs `:143-181`）：
 *     注释写 `"db" | "env" | "default"`，但代码**初值硬编码 `"env"`**，
 *     只有 `log.enabled` 的 `src == "db"` 时才改成 `"db"`
 *     ⇒ ⇒ **实际只有 `env` / `db` 两值，`default` 永不出现**。
 *     ⇒ ★★★ 与批 92 的「注释说三值、代码只给两值」是**同一个陷阱的第二例**；
 *       **注释不是契约，赋值点是。**
 * (6) ★★★★★ **`hot_reloadable` 与 `enabled` 同源但**只有一个**可被 DB 改写**：
 *     `HotReloadable: cur.File != ""`（`:150`）**永不被覆盖**，
 *     `Enabled: cur.File != ""`（`:147`）**可被 `log.enabled` 覆盖**（`:178-181`）。
 *     ⇒ ⇒ **`hot_reloadable === false` ⇒ `log_file === ""` 且 `log_dir === ""`**（可自验）。
 * (7) ★★★★ **`log_dir` 只在 `cur.File != ""` 时才填**（`:152-154`）⇒ 否则是空串
 *     ⇒ ⇒ 与 (6) 同源：`log_dir !== ""` ⇒ `log_file !== ""`。
 * (8) ★★★★ **`file_path` 可能与 `log_file` 不一致**：初值是 `cur.File`（运行时解析值），
 *     但 DB 有 `log.file_path` 覆盖时**只改 `file_path`**（`:174-177`），**不动 `log_file`**
 *     ⇒ ⇒ **`log_file` 才是「运行时真正的文件」，`file_path` 是「配置期望值」**。
 *     ⇒ ★★ 字段注释写的是「DB/环境变量 解析后的**实际生效路径**」⇒ **注释不准**。
 * (9) ★★★★ **敏感值脱敏形态是 `"***" + secret[len(secret)-4:]`**（`:196`、`:212`）
 *     ⇒ ⇒ **凡出现就一定以 `***` 开头**（可自验），且**长度随原长度变化**。
 *     ⇒ ★★★ ⚠️ **`secret` 长度 < 4 时 `secret[len(secret)-4:]` 会切片越界 panic**
 *       （`len(secret)-4` 为负）—— 客户端遇到这种网关应视为 5xx，不是「脱敏失败」。
 * (10) ★★★★ **`current_disk_usage` 的 `0` 是二义的**：`diskUsageAt` 失败时
 *      `resp.CurrentDiskUsage` 保持零值（`:203-207` 只在 `statErr == nil` 时赋值）
 *      ⇒ ⇒ **「0%」既可能是真的 0%，也可能是探测失败**。
 *      客户端**不能**据此弹「磁盘已满/未用」。
 * (11) ★★★★ **`needs_restart` 依赖 `h.attachmentStorage` 是否注入**（`:178-186`）：
 *      未注入时 `NeedsRestart = (AttachmentDirOverride != "")`；
 *      已注入时比较 `filepath.Abs(BaseDir())` 与 `filepath.Abs(EffectiveDir)`。
 *      ⇒ ⇒ **`needs_restart === true` 的含义随运行时状态而变**，不是单一含义。
 * (12) ★★★ **`storage_type` 决定哪些键会被填**（`:158-215` 的 `switch`）：
 *      `local` 填 `attachment_dir_*` / `effective_dir` / `current_disk_usage`；
 *      `oss` 填 5 个 `oss_*`；`s3` 填 7 个 `s3_*`。
 *      ⇒ ⇒ ★★ **`storage_type === "local"` 时那 12 个 oss/s3 键必然全部缺席**，
 *        **恒为 `false`** ⇒ 这是**由 switch 决定的可达性**，不是恒真判据
 *        （别的 `storage_type` 下它们会出现）⇒ **可以断言**。
 * (13) ★★★ `download_url_prefix` 是**硬编码常量** `/api/attachments/`（`:220`），
 *      **与任何配置无关** ⇒ 常量契约（值恒定，可断言取值）。
 * (14) ★★★ **`log_config` 的 PUT/GET 键名不对称**（`:199` vs `:47`）：
 *      GET 返回 `delete_days`，PUT 收的是 **`archive_delete_days`**。
 *      ⇒ ★★ 客户端做双向同步时**必须查这张对照表**，别照着 GET 的键名去 PUT。
 * (15) ★★★ **`archive_days` / `delete_days` 的缺省是硬编码的 7 / 30**（`:151-152`），
 *      **不是注册表里的 spec**，且响应里**没有任何字段说明这是缺省**。
 * (16) ★★★ **方法不匹配返回 405**（`:123`、`:133`，`writeError` ⇒ `{"error":{"detail"}}`）
 *      ⇒ ★ **与批 91 的 work-types（不匹配返 404）相反**。
 *      ⇒ ★★ **`writeJSON` 是中央出口且内部统一调 `applyV1FreezeNotice`**
 *        （`handler.go:1481-1483`）⇒ ★★★ **判「这端点带不带 v1 冻结告示」不必逐端点查**：
 *        看它最终走哪个写出层即可（`writeJSON` / `writeJSONOk` 都带，
 *        手写 `w.Write` 的才不带）。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验两个响应的**全部恒在键与类型**，外加 `omitempty` 键**存在时**的类型；
 *   为 (2)(4)(5)(6)(7)(8)(9)(12)(13) 各提供判据。
 *   ★ **不提供**「`effective_dir` 非空」这类判据 —— `EffectiveAttachmentDir()`
 *     （`:474-482`）三层兜底最后一层是常量 `"./data/attachments"`
 *     ⇒ **恒真**，由常量 + 注释承担（见批 92 的教训）。
 */

export const STORAGE_CONFIG_PATH = '/api/admin/storage/config'
export const LOG_CONFIG_PATH = '/api/admin/logs/config'

/** ★ (16) `writeError` 的文案 ⇒ 错误体是 `{error:{detail}}`。 */
export const ADMIN_CONFIG_ERROR_DETAILS = {
  methodNotAllowed: 'method not allowed',
  settingsStoreUnavailable: 'settings store unavailable',
} as const

/** ★★ (4) `settings.EffectiveValue` 的三个返回值，真实存在于代码里。 */
export const CONFIG_SOURCES = ['default', 'env', 'db'] as const

/**
 * ★★ `switch resp.StorageType` 的三个**分支**（`storage_config.go:162/195/207`）。
 * ★★★ **这不是 `storage_type` 的取值域** —— 它可被 `readStringSetting("storage.type")`
 *   改成任意字符串（`:135-138`），第四种值会让 switch 三个 case 都不进。
 *   见下方「删掉的判据①」的解释。
 */
export const STORAGE_TYPES = ['local', 'oss', 's3'] as const

/** ★ (13) 与任何配置无关的硬编码常量。 */
export const STORAGE_DOWNLOAD_URL_PREFIX = '/api/attachments/'

/** ★★ (15) 硬编码缺省，**响应里没有任何字段说明这是缺省**。 */
export const LOG_CONFIG_DEFAULT_ARCHIVE_DAYS = 7
export const LOG_CONFIG_DEFAULT_DELETE_DAYS = 30

/** ★★★ (5) 注释说三值、代码只给两值 —— `default` 永不出现。见 (5)。 */
export const LOG_ENABLED_SOURCES = ['env', 'db'] as const

/** `StorageConfigResponse` 的 **13 个恒在键**（★ 键数由脚本从 Go 源码数出，不要手数）。 */
export const STORAGE_CONFIG_REQUIRED_KEYS = [
  'storage_type',
  'attachment_dir_override',
  'ttl_days',
  'max_file_size_mb',
  'disk_quota_percent',
  'auto_cleanup_enabled',
  'auto_cleanup_threshold',
  'effective_dir',
  'attachment_dir_env',
  'needs_restart',
  'current_disk_usage',
  'config_source',
  'download_url_prefix',
] as const

/** ★★ (2) `storage_config` 的 **13 个 `omitempty` 键**（含 1 个 bool）。 */
export const STORAGE_CONFIG_OPTIONAL_KEYS = [
  'oss_endpoint',
  'oss_bucket',
  'oss_access_key_id',
  'oss_access_key_secret',
  'oss_base_path',
  's3_endpoint',
  's3_region',
  's3_bucket',
  's3_access_key_id',
  's3_secret_access_key',
  's3_base_path',
  's3_use_ssl',
  'migration_run_id',
] as const

/** ★★ (2) 那 13 个里**唯一是布尔**的一个 —— `false` 时键消失。 */
export const STORAGE_BOOL_OPTIONAL_KEYS = ['s3_use_ssl'] as const

/** ★★ (9) 恒以 `***` 开头的那两个。 */
export const STORAGE_MASKED_KEYS = ['oss_access_key_secret', 's3_secret_access_key'] as const

/** ★★★ (3) `LogConfigResponse` 的 **15 个键，全部恒在**。 */
export const LOG_CONFIG_KEYS = [
  'max_size_mb',
  'max_backups',
  'max_age_days',
  'compress',
  'archive_days',
  'delete_days',
  'file_path',
  'file_path_override',
  'file_path_env',
  'enabled',
  'enabled_source',
  'log_file',
  'log_dir',
  'hot_reloadable',
  'config_source',
] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface StorageConfigResponse {
  /**
   * ★★★ **取值域不是 `STORAGE_TYPES`** —— 初值 `"local"`（`:133`），
   * 可被 `readStringSetting("storage.type")` 改成**任意字符串**（`:135-138`）。
   * 传第四种值时 switch 三个 case 都不进 ⇒ 所有 `oss_*` / `s3_*` 键缺席，
   * 且 `effective_dir` / `current_disk_usage` / `needs_restart` 保持零值。
   */
  storage_type: string
  /** ★ 空串 = 没有 DB 覆盖（不是缺键）。 */
  attachment_dir_override: string
  ttl_days: number
  max_file_size_mb: number
  disk_quota_percent: number
  auto_cleanup_enabled: boolean
  auto_cleanup_threshold: number

  // ★ 以下 12 个键（oss 5 + s3 7）**全部 omitempty** —— 见 (2)(12)。

  oss_endpoint?: string
  oss_bucket?: string
  oss_access_key_id?: string
  /** ★★ (9) 出现即以 `***` 开头。 */
  oss_access_key_secret?: string
  oss_base_path?: string

  s3_endpoint?: string
  s3_region?: string
  s3_bucket?: string
  s3_access_key_id?: string
  /** ★★ (9) 出现即以 `***` 开头。 */
  s3_secret_access_key?: string
  s3_base_path?: string
  /** ★★★ (2) **`bool` + omitempty ⇒ `false` 时键消失 ⇒ 出现即为 `true`。** */
  s3_use_ssl?: boolean

  /** ★ `EffectiveAttachmentDir()` 三层兜底 ⇒ **恒非空**，故不提供判据。 */
  effective_dir: string
  attachment_dir_env: string
  /** ★★ (11) 含义随 `h.attachmentStorage` 是否注入而变。 */
  needs_restart: boolean
  /** ★★ (10) **`0` 是二义的**：探测失败也保持零值。 */
  current_disk_usage: number
  /** ★★ (4) 只描述**某一个键**的来源，别拿来给整张表单打标签。 */
  config_source: string
  /** ★★ (13) 恒为 `/api/attachments/`，与配置无关。 */
  download_url_prefix: string
  /** ★ PUT 触发了目录迁移时的 run_id，非空才出现（`:222-227`）。 */
  migration_run_id?: string
}

export interface LogConfigResponse {
  max_size_mb: number
  max_backups: number
  max_age_days: number
  compress: boolean
  /** ★★ (15) 缺省 7。★ PUT 时这个键叫 `archive_days`。 */
  archive_days: number
  /** ★★ (15) 缺省 30。★★ (14) **PUT 时这个键叫 `archive_delete_days`**，与 GET 不对称。 */
  delete_days: number
  /** ★★ (8) 配置期望值（DB 覆盖优先），**不一定等于 `log_file`**。 */
  file_path: string
  /** ★ 空串 = 没有 DB 覆盖。 */
  file_path_override: string
  /** ★ `os.Getenv("LLM_GATEWAY_LOG_FILE")` 原值。 */
  file_path_env: string
  /** ★ 可被 DB 的 `log.enabled` 覆盖（与 `hot_reloadable` 不同）。 */
  enabled: boolean
  /** ★★ (5) 只有 `env` / `db` 两值，注释里那个 `default` 永不出现。 */
  enabled_source: string
  /** ★★ (8) **运行时真正的日志文件**。 */
  log_file: string
  /** ★★ (7) 只在 `log_file !== ""` 时非空。 */
  log_dir: string
  /** ★★ (6) **永不被 DB 覆盖**，初值就是 `log_file !== ""`。 */
  hot_reloadable: boolean
  /** ★ (4) 同名概念但**独立计算**，只有 `log.max_size_mb` 会写它（`:158-161`）。 */
  config_source: string
}

// ── fetch ───────────────────────────────────────────────────────────────────

export function fetchStorageConfig(options?: RequestOptions): Promise<StorageConfigResponse> {
  return req<unknown>('GET', STORAGE_CONFIG_PATH, undefined, options).then(unwrapStorageConfig)
}

export function fetchLogConfig(options?: RequestOptions): Promise<LogConfigResponse> {
  return req<unknown>('GET', LOG_CONFIG_PATH, undefined, options).then(unwrapLogConfig)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包 —— ★ 两个端点的可选键编码完全不同，各写各的，不抽通用解包器
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapStorageConfig(resp: unknown): StorageConfigResponse {
  const o = requireObject(resp, '存储配置')
  requireKeys(o, STORAGE_CONFIG_REQUIRED_KEYS, '存储配置')
  for (const k of [
    'storage_type', 'attachment_dir_override', 'effective_dir',
    'attachment_dir_env', 'config_source', 'download_url_prefix',
  ] as const) {
    if (typeof o[k] !== 'string') throw new Error(`存储配置 的 ${k} 不是字符串`)
  }
  for (const k of ['ttl_days', 'max_file_size_mb', 'disk_quota_percent', 'auto_cleanup_threshold', 'current_disk_usage'] as const) {
    if (typeof o[k] !== 'number') throw new Error(`存储配置 的 ${k} 不是数字`)
  }
  if (typeof o['needs_restart'] !== 'boolean') throw new Error('存储配置 的 needs_restart 不是布尔')
  if (typeof o['auto_cleanup_enabled'] !== 'boolean') throw new Error('存储配置 的 auto_cleanup_enabled 不是布尔')
  // ★★ (2) omitempty 键：**存在才校验**。
  for (const k of [
    'oss_endpoint', 'oss_bucket', 'oss_access_key_id', 'oss_access_key_secret', 'oss_base_path',
    's3_endpoint', 's3_region', 's3_bucket', 's3_access_key_id', 's3_secret_access_key',
    's3_base_path', 'migration_run_id',
  ] as const) {
    if (k in o && typeof o[k] !== 'string') throw new Error(`存储配置 的 ${k} 不是字符串`)
  }
  // ★★★ (2) 唯一的布尔可选键：出现即为 true，但仍按布尔校验类型。
  if ('s3_use_ssl' in o && typeof o['s3_use_ssl'] !== 'boolean') {
    throw new Error('存储配置 的 s3_use_ssl 不是布尔')
  }
  return o as unknown as StorageConfigResponse
}

export function unwrapLogConfig(resp: unknown): LogConfigResponse {
  const d = requireObject(resp, '日志配置')
  requireKeys(d, LOG_CONFIG_KEYS, '日志配置')
  for (const k of [
    'max_size_mb', 'max_backups', 'max_age_days', 'archive_days', 'delete_days',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`日志配置 的 ${k} 不是数字`)
  }
  for (const k of ['compress', 'enabled', 'hot_reloadable'] as const) {
    if (typeof d[k] !== 'boolean') throw new Error(`日志配置 的 ${k} 不是布尔`)
  }
  for (const k of [
    'file_path', 'file_path_override', 'file_path_env',
    'enabled_source', 'log_file', 'log_dir', 'config_source',
  ] as const) {
    if (typeof d[k] !== 'string') throw new Error(`日志配置 的 ${k} 不是字符串`)
  }
  return d as unknown as LogConfigResponse
}

function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据 —— ★★ 本模块只保留**两个有区分力**的函数
// ═══════════════════════════════════════════════════════════════════════════

// ★★★★ 下面这些**不变量由后端保证、在可达输入域上恒成立**，
//   本模块**刻意不为它们提供任何判据**，一律由注释承担 ——
//   批 93 首轮我写了 9 条，其中 **8 条是恒真的**（变异脚本逐条实测）。
//   ⇒★★★ 一个恒真的断言**区分不了任何两种实现**，保留它只会让人
//     误以为这里有检查；信息一条都不少，只是从代码挪进了注释。

// ── 删掉的判据①：storage_type ⇒ 哪些云厂商键在场（恒真）──
// switch 的三个 case（`:162/:195/:207`）**各只设自己那一组**：
// `local` 只写 attachment_dir_*/effective_dir/current_disk_usage，
// `oss` 只写 5 个 `oss_*`，`s3` 只写 7 个 `s3_*`
// ⇒ ★★★ 「storage_type==='local' ⇒ 12 个云厂商键全缺席」等三条**全都恒真**。
// ★★ 而且它们**不是**「恒等于一个闭集」：`resp.StorageType` 初值 "local"
//   （`:133`），可被 `readStringSetting("storage.type")` 改成**任意字符串**
//   ⇒ ★★★ **storage_type 的取值域不是那三个**！传第四种值（如 "azure"）时
//   switch 三个 case 都不进 ⇒ 所有云厂商键缺席 + local 运行时字段全是零值
//   ⇒ 这是一个真实存在的「未知 storage_type」形状。
// ⇒ ★★ 因此连「校验 storage_type 取值」都不能做 —— STORAGE_TYPES 只是
//   switch 的三个**分支**，不是取值域。

// ── 删掉的判据②：s3_use_ssl 键存在 ⇒ 为 true（恒真）──
// `:216-218` 只在 `if useSSL` 时 `resp.S3UseSSL = true` ⇒ 键要么缺席、要么为 true
// ⇒ ★★★ 「键存在 ⇒ true」恒真；而 `false` **不可达** ⇒ 不能对它写判据。
// ⇒ 客户端要表达「启用了 SSL」直接读 `c.s3_use_ssl === true` 即可。

// ── 删掉的判据③：脱敏值以 *** 开头（恒真）──
// `:196` 与 `:212` 都是 `"***" + secret[len(secret)-4:]` ⇒ 凡出现即带掩码。
// ⇒ 该判据恒真，而「明文 secret」这种输入后端根本产不出。

// ── 删掉的判据④：config_source / enabled_source 的取值（恒真）──
// config_source 初值 "default"（`:134`），只被 `"db"`（`:140`）或
// `readIntSetting` 的 src 覆盖 ⇒ 取值恒 ∈ {default, env, db}（`:506`）。
// enabled_source 初值硬编码 `"env"`（`:149`），只被 `src=="db"` 改写（`:180`）
// ⇒ 取值恒 ∈ {env, db} ⇒ 「注释里的 default 不可达」这件事本身也是恒真的。

// ── 删掉的判据⑤：download_url_prefix 恒为常量（恒真）──
// `:220` 无条件赋值 ⇒ 恒真。（常量本身仍导出，供拼 URL 用。）

// ── 删掉的判据⑥：hot_reloadable / log_file / log_dir 三者同源（恒真）──
// `:141` `HotReloadable: cur.File != ""`、`:146` `LogFile: cur.File`、
// `:152-154` `if cur.File != "" { resp.LogDir = filepath.Dir(cur.File) }`
// ⇒ ★★★ 三个键**全部只依赖 `cur.File`** ⇒ 恒有恒无一起动
// ⇒ ⇒ 「hot_reloadable===false ⇒ 两个路径都空」与「log_dir!=='' ⇒ log_file!==''」
//   **都恒真**，区分格（log_dir 非空而 log_file 为空）**后端产不出**。
// ⇒ ★ 首轮我把两条变异判成「有牙」，其实是**靠一条不可达的夹具**打出来的
//   （`log_dir:'/var/log'` + `log_file:''`）⇒ 那条用例本身也要删。

// ── 留下的两条（可达输入域上真的能分叉）──

/**
 * ★★★★ (8) `file_path` 是**配置期望值**、`log_file` 是**运行时真值**
 * ⇒ DB 覆盖了 `log.file_path` 但尚未热加载时，两者**必然不等**
 * （`:174-177` 只改 `file_path`，不动 `log_file`）
 * ⇒ ⇒ 这是本族**唯一有区分力**的语义判据：
 *   它回答的是「要不要提示用户等热加载」。
 */
export function logConfigPathMatchesRuntime(r: LogConfigResponse): boolean {
  return r.file_path === r.log_file
}

/**
 * ★★★ (14) GET 返回 `delete_days`，而 PUT 收的是 **`archive_delete_days`**
 * （`log_management.go:199` vs `:47`）。
 * ⇒ ★★ 双向同步的前提：照 GET 的键名去 PUT 会被**静默忽略**
 *   （`DeleteDays` 是 `*int` + omitempty，`nil` = 不改）。
 * ⇒ ⇒ 这条**不是**恒真判据：不同输入返回不同输出，是一张真查表。
 */
export function logConfigPutKeyFor(getKey: string): string {
  if (getKey === 'delete_days') return 'archive_delete_days'
  return getKey
}
