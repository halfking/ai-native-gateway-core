import { req, type RequestOptions } from './client'

/**
 * storageMigrationState.ts — 附件存储目录迁移进度（2026-10-08，第九十五批）。
 *
 * GET /api/admin/storage/migration-state
 *
 * - **注册**：`mux.HandleFunc` 直挂在 `admin/handler.go:1111`
 *   ```go
 *   mux.HandleFunc("/api/admin/storage/migration-state", admin(h.handleMigrationState))
 *   ```
 *   ⇒ ★★ **admin 档**（`h.admin` = `AdminMiddleware`，`handler.go:880`）
 *   ⇒ ★★★ **与批 93 的 `storage/config`（`:1108`，**superAdmin** 档）同前缀、
 *     相邻两行、两种档位** ⇒ 再次印证「不能按前缀推权限」。
 * - **实现**：`admin/storage_migration.go:401-421`（handler）· `:425-427`（分发入口）·
 *   `:99-112`（`getMigration`）· `:48-64`（`migrationRun`）· `:40-45`（状态枚举）。
 * - **桌面调用方**：`web/src/api/tuning.ts:838-840` —— `req<MigrationStateResponse>`
 *   直接强转，**不做任何校验** ⇒ 全部校验由本模块补上。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 * - **上游**：`PUT /api/admin/storage/config` 改附件目录后触发迁移，
 *   并在响应里附带 `migration_run_id`（`storage_config.go:344-347`）引导前端来轮询本端点。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十一件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **顶层是一个「2 键、两个值都可为裸 `null`」的裸对象**
 *     （`:409-412`）：
 *     ```go
 *     resp := map[string]any{ "running": nil, "latest": nil }
 *     if run != nil { … resp["running"|"latest"] = run }
 *     ```
 *     ⇒ ⇒ 这不是 envelope、不是裸数组、也不是「单个主键恒在但值为 null」
 *       —— 而是**第四种形状：两个并列槽位，键恒在、值二选一被填**。
 *     ⇒ ★★ Go 的 `encoding/json` 序列化 map **按键名排序** ⇒ 线上 JSON 的键序恒为
 *       `latest` 在前、`running` 在后（夹具逐字照抄这一点）。
 *
 * (2) ★★★★★ **「至多一个槽位非 null」是构造保证，不是数据属性** ——
 *     `getMigration()`（`:99-112`）**只返回一个** run（`running` 优先于 `latest`，返回值拷贝）
 *     ⇒ ⇒ **`toBeTruthy(running) && toBeTruthy(latest)` 在真后端上恒假** ⇒ **不提供判据**。
 *
 * (3) ★★★★★ **`status` 枚举声明 4 个值，但 `idle` 是死值**：
 *     ```go
 *     migrationIdle migrationStatus = "idle"   // :41 —— 全仓仅此一处，无任何赋值点
 *     ```
 *     `startStorageMigration` 只会建 `migrationRunning` 的 run（`:133`），
 *     `finishMigration` 只会写 `succeeded`/`failed` ⇒ ⇒
 *     **响应里 `status` 的可达取值只有 3 个**。
 *     ⇒ ★★★ **桌面 `MigrationStatus`（`tuning.ts:813`）把 `idle` 声明成了成员** ——
 *       照它写的客户端会为一条**永不出现的分支**写代码。
 *     ⇒ 本模块的 `MIGRATION_STATUSES` 只列 3 个；**但不提供「status ∈ 枚举」判据**（恒真，见文末）。
 *
 * (4) ★★★★★ **`migrationRun` 是 15 键 = 11 恒在 + 4 带 `omitempty`**
 *     （脚本按 `` json:"…" `` 扫出，不是手数）：
 *     - 恒在：`run_id` `status` `from_dir` `to_dir` `started_at`
 *       `files_total` `files_copied` `bytes_total` `bytes_copied`
 *       `files_deleted` `old_dir_purged`
 *     - `omitempty`：`finished_at` `heartbeat_at` `errors` `message`
 *     ⇒ ★ 本批是**桌面类型没写错**的少数情况（`tuning.ts:815-831` 15 个键全对），
 *       与批 94（桌面少声明 2 键）对照。
 *
 * (5) ★★★★★ **`failed` 的 run 可能根本没有 `errors` 键** ——
 *     `run.Errors = append(…)` 只发生在 4 处：panic（`:160`）、`filepath.Rel` 失败（`:219`）、
 *     复制失败（`:226`）、删旧目录失败（`:274`）。
 *     而 8 条 `finishMigration(run, migrationFailed, …)` 分支（`:169 :179 :189 :213 :229
 *     :248 :257 :265`）**只写 `Message`、从不动 `Errors`** ⇒ ⇒
 *     **「收集文件失败 / 校验失败 / 切换 BaseDir 失败 / 空间不足 / 超时」这些失败，
 *     唯一的细节只在 `message` 里，`errors` 是缺失的。**
 *     ⇒ ⇒ 客户端**不能**用「`errors` 为空」推断「没有失败」。
 *
 * (6) ★★★★ **`message` 是自由文本，共有 14 种形态**（8 处 `run.Message = …` 加
 *     `finishMigration` 的 8 个实参），中文前缀 + 英文错误串拼接
 *     （如 `"收集文件失败: " + err.Error()`）⇒ ⇒
 *     **没有稳定错误码，客户端不得对 `message` 做 `startsWith` 判定。**
 *
 * (7) ★★★★ **`files_deleted` 赋的是 `run.FilesTotal`，不是实际删除数**（`:277`），
 *     且 `purged` 为 false 时**保持 0**（从不赋值）⇒ ⇒
 *     「`files_deleted ∈ {0, files_total}`」「`files_deleted > 0` ⟹ `old_dir_purged === true`」
 *     **都是恒真的** ⇒ 不提供判据，写进注释。
 *     ⇒ ★★★ **反过来却是错的**：空目录 + 迁移期间有新写入 ⇒ 跳过删除（`:193-196`）
 *     ⇒ **`files_deleted === 0`、`files_total === 0`、`old_dir_purged === false`**
 *     ⇒ ★★★ **`files_deleted === files_total` 推不出「已清理」**（0 === 0），
 *       见下面 `migrationPurgeLooksComplete`。
 *
 * (8) ★★★★ **`files_copied` 是「已处理到第几个」不是「成功复制了几个」** ——
 *     `run.FilesCopied = i + 1`（`:239`）在**复制成功之后**执行，但 `i` 遍历的是
 *     `files`（含此前失败的条目）⇒ ⇒ 某文件失败时 `FilesCopied` **不前进**，
 *     下一个成功时一步跨过 ⇒ **可越过失败条目**。
 *     ⇒ ⇒ `files_copied ≤ files_total` 恒真（不提供判据）；
 *       **客户端不能把 `files_total - files_copied` 当「还剩几个失败」**。
 *
 * (9) ★★★★ **空目录分支是一个「成功但零进度」的可达形状**（`:182-201`）：
 *     `FilesTotal = 0`、`BytesTotal = 0`、`FilesCopied` **从未赋值**（保持 0），
 *     终态仍是 `succeeded` ⇒ ⇒ **`status === 'succeeded' && files_total === 0 &&
 *     files_copied === 0` 是合法的成功态。**
 *     ⇒ ⇒ 进度比**必须**处理除零（`total === 0` 时给 0，**不是 NaN/Infinity**），
 *       见 `migrationFileProgress` / `migrationByteProgress`。
 *
 * (10) ★★★ **`heartbeat_at` 恒在**：`startStorageMigration:137` 建 run 时就赋了
 *     `&hb`，复制循环 `:241` 只是更新 ⇒ ★ `'heartbeat_at' in run` 恒真，不提供判据。
 *     ⇒ ★★ 相应地，**心跳停更在本响应里查不出来** —— 迁移 goroutine 卡死时
 *       `status` 仍是 `running`、计数不再变，**没有「卡住」信号**。
 *
 * (11) ★★★ **v1 冻结告示对本端点是噪音**：`writeJSON` 是中央出口、无条件调
 *     `applyV1FreezeNotice`（`handler.go:1483`），而告示判据是
 *     `request_logs` 写门（`v1_freeze_notice.go:228-229`）⇒ ⇒
 *     响应头 `X-LLM-Gateway-V1-Data-Frozen` 可能出现，但**迁移状态与 request_logs 无关**
 *     ⇒ ⇒ ★★ **移动端不要在本页挂「v1 数据已停更」横幅**（告示在**响应头**里，不在 body）。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验顶层 2 键、每个槽位「null 或 run 对象」、`migrationRun` 的 11 恒在键与类型、
 *   4 个 `omitempty` 键的「存在才校验」两个分支；为 (1)(2)(3)(7)(9) 提供判据。
 *   ★ **不校验** `status` 的取值域（Go 常量封闭域 ⇒ 恒真判据，按纪律删除，契约由注释承担）。
 *   ★ **不校验** `message` 的取值（同 (6)）。
 *   ★ **不校验** `errors` 是否存在（`failed` 也可以没有，见 (5)）。
 *   ★ **不提供**「两槽位不同时非 null」判据（恒真，见 (2)）。
 *   ★ **不提供** `files_copied ≤ files_total` 判据（恒真，见 (8)）。
 */
export const MIGRATION_STATE_PATH = '/api/admin/storage/migration-state'

/** ★★ (1) 顶层恒有的 2 个槽位键。 */
export const MIGRATION_STATE_KEYS = ['running', 'latest'] as const

/** ★★ (4) `migrationRun` 恒在的 11 个键。 */
export const MIGRATION_RUN_KEYS = [
  'run_id',
  'status',
  'from_dir',
  'to_dir',
  'started_at',
  'files_total',
  'files_copied',
  'bytes_total',
  'bytes_copied',
  'files_deleted',
  'old_dir_purged',
] as const

/** ★★ (4) 4 个带 `omitempty` 的键。 */
export const MIGRATION_RUN_OPTIONAL_KEYS = ['finished_at', 'heartbeat_at', 'errors', 'message'] as const

/**
 * ★★★★★ (3) `status` 的**可达**取值 —— 只有 3 个。
 * ★ `idle` 在 `:41` 声明但**全仓无赋值点**，故不列入；
 *   桌面 `MigrationStatus`（`tuning.ts:813`）把它列进去了，那是死分支。
 */
export const MIGRATION_STATUSES = ['running', 'succeeded', 'failed'] as const

/** ★★ (3) 终态（≠ `running`）的取值。 */
export const MIGRATION_TERMINAL_STATUSES = ['succeeded', 'failed'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface MigrationRun {
  /** 形如 `migration-<UnixNano>`（`:132`），全局单调唯一到纳秒。 */
  run_id: string
  /** ★★ (3) 可达域只有 running / succeeded / failed，**没有 `idle`**。 */
  status: (typeof MIGRATION_STATUSES)[number]
  /** 绝对路径。 */
  from_dir: string
  /** 绝对路径。 */
  to_dir: string
  /** RFC3339（Go `time.Time` 默认序列化，**带纳秒**）。 */
  started_at: string
  /** 终态必有；`running` 时键**不存在**（`finishMigration:292` / panic 分支 `:162` 才写）。 */
  finished_at?: string
  /** ★★★ (10) **恒在**（建 run 时就赋值，`:137`）。 */
  heartbeat_at?: string
  files_total: number
  /** ★★ (8) 「已处理到第几个」，可越过失败条目 —— 不是成功数。 */
  files_copied: number
  bytes_total: number
  bytes_copied: number
  /** ★★★ (7) 赋的是 `files_total`，**不是实际删除数**；未清理时恒为 0。 */
  files_deleted: number
  /** ★★★ (7) 唯一权威的「旧目录已删」标志。 */
  old_dir_purged: boolean
  /** ★★ (5) 失败时**也可能缺失**（8 条 finishMigration 分支只写 `message`）。 */
  errors?: string[]
  /** ★★ (6) 自由文本，14 种形态，**不得**做 `startsWith` 判定。 */
  message?: string
}

export interface MigrationStateResponse {
  /** ★★ (1) 键恒在；值可能是裸 `null`。有值时其 `status` 必为 `running`。 */
  running: MigrationRun | null
  /** ★★ (1) 键恒在；值可能是裸 `null`。有值时其 `status` 必为终态。 */
  latest: MigrationRun | null
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/admin/storage/migration-state`。
 * ★ 后端 `:404-407` **只接受 GET**，其余方法一律 405（错误体 `{"error":{"detail":…}}`）。
 * ★ 无查询参数。
 */
export function fetchStorageMigrationState(
  options?: RequestOptions,
): Promise<MigrationStateResponse> {
  return req<unknown>('GET', MIGRATION_STATE_PATH, undefined, options).then(unwrapMigrationState)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapMigrationState(resp: unknown): MigrationStateResponse {
  const d = requireObject(resp, '迁移状态')
  requireKeys(d, MIGRATION_STATE_KEYS, '迁移状态')
  // ★★ (1) 两个槽位各自独立判定：null 或 run 对象，**二选一**。
  //   逐槽位判定 ⇒ `running` 坏掉时错误消息含 `running`，`latest` 坏掉时含 `latest`，
  //   两条判据在变异测试里才分得开（只靠 `where` 标签区分的用例分不出任何变异）。
  if (d['running'] !== null) unwrapMigrationRun(d['running'], '迁移状态 的 running')
  if (d['latest'] !== null) unwrapMigrationRun(d['latest'], '迁移状态 的 latest')
  return d as unknown as MigrationStateResponse
}

/** ★★ (4) 11 恒在键必查；4 个 `omitempty` 键**存在才校验**。 */
export function unwrapMigrationRun(v: unknown, where: string): MigrationRun {
  const o = requireObject(v, where)
  requireKeys(o, MIGRATION_RUN_KEYS, where)
  for (const k of ['run_id', 'status', 'from_dir', 'to_dir', 'started_at'] as const) {
    if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  for (const k of ['files_total', 'files_copied', 'bytes_total', 'bytes_copied', 'files_deleted'] as const) {
    if (typeof o[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  if (typeof o['old_dir_purged'] !== 'boolean') throw new Error(`${where} 的 old_dir_purged 不是布尔`)
  // ★ 两个分支各留一条判据：键**不存在**（omitempty）与键**存在但类型错**。
  if ('finished_at' in o && typeof o['finished_at'] !== 'string') {
    throw new Error(`${where} 的 finished_at 不是字符串`)
  }
  if ('heartbeat_at' in o && typeof o['heartbeat_at'] !== 'string') {
    throw new Error(`${where} 的 heartbeat_at 不是字符串`)
  }
  if ('errors' in o) {
    const errs = o['errors']
    if (!Array.isArray(errs)) throw new Error(`${where} 的 errors 不是数组`)
    for (let i = 0; i < errs.length; i++) {
      if (typeof errs[i] !== 'string') throw new Error(`${where} 的 errors[${i}] 不是字符串`)
    }
  }
  if ('message' in o && typeof o['message'] !== 'string') {
    throw new Error(`${where} 的 message 不是字符串`)
  }
  return o as unknown as MigrationRun
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
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (1)(2) 槽位选择 ──

/**
 * ★★★★★ (1) **两个槽位里取「当前有效的那一个」** ——
 * `getMigration()` 的读取顺序是 `running` 优先（`:103-110`），本函数与后端同序。
 * ⇒ ★★★ 只读 `latest` 的客户端会**漏掉正在跑的迁移**；
 *   只读 `running` 的客户端会**在迁移刚结束时显示「没有迁移」**。
 */
export function effectiveMigrationRun(s: MigrationStateResponse): MigrationRun | null {
  return s.running ?? s.latest
}

/** ★★ (1) 两个槽位都为 `null` ⇒ **从未有过迁移**（或状态刚被重启清空）。 */
export function migrationStateIsEmpty(s: MigrationStateResponse): boolean {
  return s.running === null && s.latest === null
}

// ── 轮询策略 ──

/**
 * ★★★★ 是否该继续轮询。
 * ⇒ ★★★ **两个槽位都为空时必须停** —— 否则在一个从未迁移过的网关上会**永远转圈**。
 * ⇒ ★★★ `failed` 也停：重试循环不会让 `latest` 变回 `running`
 *   （只有 `startStorageMigration` 能写 `running`，而它是**另一个请求**触发的）。
 */
export function shouldPollMigration(s: MigrationStateResponse): boolean {
  const run = effectiveMigrationRun(s)
  if (run === null) return false
  return run.status === 'running'
}

/** ★★ (3) 是否终态。 */
export function migrationRunIsTerminal(run: MigrationRun): boolean {
  return run.status !== 'running'
}

// ── (9) 进度比必须处理除零 ──

/**
 * ★★★★★ (9) **文件进度比**。`files_total === 0` 时返回 **0**，
 * ★ **不是 `files_total > 0 ? copied/total : copied`** 那种写法 ——
 *   后者在空目录成功迁移（`files_total === 0`）时得到 `0/0 = NaN`，
 *   而 NaN 喂给进度条组件会渲染成空白或 `NaN%`。
 */
export function migrationFileProgress(run: MigrationRun): number {
  if (run.files_total === 0) return 0
  return run.files_copied / run.files_total
}

/** ★★★★★ (9) 字节进度比，除零处理同 `migrationFileProgress`。 */
export function migrationByteProgress(run: MigrationRun): number {
  if (run.bytes_total === 0) return 0
  return run.bytes_copied / run.bytes_total
}

// ── (7) 清理完成的判断 ──

/**
 * ★★★★ (7) 「旧目录看起来已清理」的**弱信号**。
 * ⇒ ★★ **不能用 `files_deleted === files_total` 代替**：
 *   空目录 + 迁移期间有新写入 ⇒ 跳过删除（`:193-196`）⇒
 *   `files_total === 0`、`files_deleted === 0`、`old_dir_purged === false`
 *   ⇒ **`0 === 0` 成立但目录没删。**
 * ⇒ ★★ 本函数对「空目录且已删除」也返回 `false`
 *   （`files_deleted > 0` 不成立）—— 这**是刻意的**：
 *   那条路径下 `files_deleted` 恒为 0，计数器给不出任何信息，
 *   只有 `old_dir_purged` 能证明。权威答案永远是 `old_dir_purged`。
 */
export function migrationPurgeLooksComplete(run: MigrationRun): boolean {
  return run.files_deleted > 0 && run.old_dir_purged
}

// ── (5) 失败细节的可用性 ──

/**
 * ★★★★★ (5) **「这次失败有没有结构化细节」**。
 * ⇒ ★★★ 8 条 `finishMigration(…, migrationFailed, …)` 分支只写 `message`，
 *   `errors` 仍缺失 ⇒ ⇒ **返回 `false` 不代表「没有失败」，只代表「细节只在 message 里」。**
 */
export function migrationRunHasErrorDetail(run: MigrationRun): boolean {
  return run.errors !== undefined && run.errors.length > 0
}

// ── 上游 `migration_run_id` 关联 ──

/**
 * ★★★ 与 `PUT /api/admin/storage/config` 响应里的 `migration_run_id`
 * （`storage_config.go:344-347`）关联：state 里**任一槽位**的 `run_id` 命中即为同一次迁移。
 * ⇒ ★★ 注意上游那个 id 存在**跨请求共享的 `atomic.Value`** 里、`:349` 用完即清空，
 *   两次并发 PUT 之间可能被对方读到 ⇒ **关联不上不等于「没迁移过」，先看本端点自己的槽位。**
 */
export function migrationStateContainsRunId(s: MigrationStateResponse, runId: string): boolean {
  return effectiveMigrationRun(s)?.run_id === runId
}
