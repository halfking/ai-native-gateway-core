import { req, type RequestOptions } from './client'

/**
 * dispatchJournal.ts — 调度轨迹快照（2026-10-08，第八十九批）。
 *
 * GET /api/admin/dispatch/journal/{tenant}/{request_id}
 *
 * - **注册**：**第四种注册形态** —— 既不在 `admin/handler.go` 的 mux 清单里，
 *   也不在 `cmd/gateway/main.go:7070` 的 requestJourney 直挂清单里，而是
 *   `cmd/gateway/main.go:7074-7075` 现构造 API 对象、现调 `RegisterRoutes`：
 *   ```go
 *   journalSnapshotAPI := admin.NewJournalSnapshotAPI(journalSnapshotStore)
 *   journalSnapshotAPI.RegisterRoutes(mux, requestJourneyWrapAdmin)
 *   ```
 *   → `admin/journal_handlers.go:30-35`（`mux.HandleFunc("/api/admin/dispatch/journal/", wrap(api.ServeHTTP))`）
 *   ⇒ ★★★ 只 grep `mux.HandleFunc` 或 `main.go` 的路由清单都会判成「死端点」。
 * - **实现**：`admin/journal_handlers.go`（126 行）+ `domains/dispatch/journal_consumer.go`
 *   + `domains/dispatch/journal.go`（条目结构）+ `domains/dispatch/pipeline.go:1852-1893`（发射）。
 * - **桌面调用方**：`web/src/api/dispatchJournal.ts:47` —— ★ **它不做任何校验**，
 *   直接 `req<DispatchJournalSnapshot>` 强转 ⇒ 全部校验由本模块补上。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十五件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **`not found` 的七种成因返回的是**完全相同**的响应**：
 *     - `:55` 认证上下文缺失 / tenant 为空
 *     - `:61` 角色不是 `super_admin`/`admin_key`/`tenant_admin`
 *     - `:65` 非特权调用方跨租户
 *     - `:77` 快照不存在（`ErrJournalNotFound`）
 *     - `:84` 快照里的 `TenantID`/`RequestID` 与路径段不符
 *     - `journal_consumer.go:99` 三字段任一为空（认证/目标/请求 id）
 *     - `journal_consumer.go:102` 非特权跨租户（第二道闸，与 `:65` 重复）
 *     ⇒ ★★★ 客户端**不能**用 404 判「快照不存在」，也**不能**用 404 判权限。
 *
 * (2) ★★★★★ **中间件不做角色校验** —— `AdminMiddleware`（`admin/auth.go:76-130`）
 *     只验 JWT 有效性，**不检查 role**，任何有效 JWT 都会被放进 handler
 *     ⇒ ⇒ **普通用户拿到的也是 404**，不是 403
 *     ⇒ ★ 与批 88 的「403/404 可区分」正好相反：本族**根本没有 403 这条路**。
 *
 * (3) ★★★★★ **两种错误体形状并存，且取决于「哪一层写的」**：
 *     - handler 层 `writeRequestJourneyError`（`request_journey.go:287-291`）：
 *       `{"error":{"message":msg,"type":"admin_error"}}`
 *     - 中间件层 `writeError`（`handler.go:1494-1498`）：`{"error":{"detail":msg}}`
 *       （401 `authentication required`、403 `password change required` 走这条）
 *     ⇒ ★★ **同一个 admin 包里就有两种**，键名互斥（`message` vs `detail`）
 *     ⇒ ⇒ 错误映射必须按（**写出层**）分支，不能只按包分支。
 *     ⇒ ★★ 还有第三种 `writeErrorWithCode`（`:1501`，带 `code`），本族未用到。
 *
 * (4) ★★★★★ **快照是进程内 LRU 内存缓冲，不是持久化存储**：
 *     `cmd/gateway/main.go:1080` `dispatch.NewInMemoryJournalStore(10000)`
 *     - 进程重启 ⇒ 全部快照消失 ⇒ 404
 *     - 容量 10000，满了淘汰最久未用的 ⇒ 404
 *     - ★★ **`ConsumeSnapshot` 会 `moveToFrontLocked`（`:109`）⇒ 读操作有副作用**：
 *       读一次就把这条快照「续命」，客户端**不能**假设「读它不影响它能活多久」。
 *     ⇒ ★★ 而 `journal_consumer.go:16-17` 的注释自称「for testing and demonstration
 *       purposes / 生产实现应当用持久化存储」，**实际却接在生产路径上** ⇒ 注释与接线不符。
 *
 * (5) ★★★★★ **截断是**两段式**的，而只有第二段被报告**：
 *     - 第一段（环形，`journal.go:137-140`）：每请求的 ring 上限 `journalCapacity = 128`，
 *       满了从**头部**丢最旧的 ⇒ ★★★ **这个丢弃没有任何标记**，响应里看不出来
 *     - 第二段（快照，`pipeline.go:1874-1879`）：`len(entries) > maxJournalSnapshotEvents(50)`
 *       ⇒ 保留**最近** 50 条，`truncated = true`，`truncated_count = 原长度 - 50`
 *     ⇒ ★★★ **「`truncated === false`」不等于「轨迹完整」** —— 第一段的丢弃是隐形的
 *       ⇒ 这是本族最锋利的「二义」：`truncated` 只覆盖两段里的一段。
 *
 * (6) ★★★★ **`truncated === true` ⇒ `entries.length === 50`**（`pipeline.go:1876-1879`
 *     是精确切片 `entries = entries[truncatedCount:]`）
 *     ⇒ 等价地 **`truncated_count === 0` ⟺ `!truncated`** ⇒ 这条可自验。
 *
 * (7) ★★★★ **末尾条目恒为终态** —— `emitJournalSnapshot` 只在 `complete()`
 *     的终态路径被调（`pipeline.go:1784`），而终态条目由 `complete()` 写、
 *     「no journal entry follows a terminal one」（`notice.go:63-64`）
 *     ⇒ ⇒ 客户端**可自验**：最后一条 `action ∈ {completed, failed, canceled}`。
 *     ★ 而截断从**头部**丢，终态条目恒在最末 ⇒ 截断不影响这条不变量。
 *
 * (8) ★★★★ **`snapshot_version === 最后一条的 seq`** ——
 *     `SnapshotVersion = int64(qr.journalSeq)`（`pipeline.go:1880`），
 *     而 `journalSeq` 的最后一次自增就发生在写终态条目时（`journal.go:118-119`）
 *     ⇒ ⇒ 又一条可自验的强不变量。
 *
 * (9) ★★★★ **`seq` 首条为 1 且相邻差 1** —— `qr.journalSeq++; entry.Seq = qr.journalSeq`
 *     （`journal.go:118-119`）⇒ 环形丢弃与快照截断都只砍**头部窗口**，
 *     保留下来的窗口内部**不会出现空洞** ⇒ 客户端可自验「严格递增且步长为 1」。
 *
 * (10) ★★★★ **`counts` 是累计快照、只增不减** ——
 *      `recordDecision` 先 `qr.Counts.Retries++` 等折叠再 `entry.Counts = qr.Counts`
 *      （`journal.go:118-135`）⇒ 逐条非递减，末条即总量。
 *      ★ 语义注释自陈：计数是**分类视图**，权威的尝试上限是 `AttemptCount`
 *      （`journal.go:60-62`）⇒ **`counts` 之和 ≠ 尝试次数**。
 *
 * (11) ★★★★ **条目有 5 个恒在键 + 12 个 `omitempty` 可选键**：
 *      恒在：`seq`/`at`/`action`/`attempt`/`counts`
 *      可选：`model`/`credential_id`/`provider_id`/`vendor`/`error_kind`/`http_status`
 *      /`from_model`/`to_model`/`from_credential_id`/`to_credential_id`
 *      /`from_provider_id`/`to_provider_id`
 *      ⇒ ★★★ 12 个 `omitempty` 都是**值类型 + 无指针** ⇒ ★★ **`0` 与空串会被 `omitempty` 吃掉**
 *      ⇒ 「`credential_id` 是 0」与「`credential_id` 不存在」**在响应里不可区分**。
 *
 * (12) ★★★ **`counts` 五键无 `omitempty`**（`journal.go:63-69`）⇒ 恒在、恒为数字。
 *
 * (13) ★★★★ **权限是「角色 × 路径里的租户」二维的**（`:59-67`）：
 *      ```go
 *      privileged := auth.Role == "super_admin" || auth.Role == "admin_key"
 *      if !privileged && auth.Role != "tenant_admin" { 404 }
 *      if !privileged && callerTenant != tenantID     { 404 }
 *      ```
 *      ⇒ ★★★ `super_admin` 与 `admin_key` **可读任意租户**；
 *      `tenant_admin` **只能读自己租户**（且租户键取自 JWT，不是 URL）
 *      ⇒ ⇒ 抽屉席**不设** `requiresRole`（tenant_admin 也够用）。
 *      ⇒ ★ 三种角色集见 `admin/context.go:16`：`super_admin | tenant_admin | admin_key`。
 *
 * (14) ★★★ **路径解析：先切分、后解码**（`:97-117`）
 *      `strings.Split(rest, "/")` 要求**恰好两段且都非空**，随后 `url.PathUnescape`
 *      再 `TrimSpace`，最后拒掉含 `/` `\` `\x00` 的段以及 `.` / `..`
 *      ⇒ ★★ 编码过的 `%2F` **不会**穿越路径（切分发生在解码之前），
 *      但会在解码后被 `ContainsAny` 拒掉 ⇒ **400 `invalid journal path`**。
 *
 * (15) ★★★ **405 检查排在最前**（`:38-42`，且会写 `Allow: GET` 响应头），
 *      甚至早于 `consumer == nil` 与路径解析
 *      ⇒ 与批 88（405 在 503 之前）一致。
 *
 * ★★ **本模块刻意不做的事**：
 *   - **不硬拒未知的 `action`**：后端 `NextActionKind`（`notice.go:55-68`）可能新增取值，
 *     解包期硬拒会让客户端在网关升级后直接崩 ⇒ 只校验 `action` 是字符串，
 *     取值集合以常量 + 注释承担。
 *   - **不提供「快照是否完整」判据**：后端自己都保证不了（见 (5)）。
 */

export const DISPATCH_JOURNAL_PATH_PREFIX = '/api/admin/dispatch/journal/'

/** `:50` 的 400 文案。 */
export const DISPATCH_JOURNAL_BAD_PATH_MESSAGE = 'invalid journal path'
/** `:40` 的 405 文案。 */
export const DISPATCH_JOURNAL_METHOD_MESSAGE = 'method not allowed'
/** `:44` / `:80` 的 503 文案（两种成因共用一句）。 */
export const DISPATCH_JOURNAL_UNAVAILABLE_MESSAGE = 'journal unavailable'
/** `:55` / `:61` / `:65` / `:77` / `:84` 的 404 文案（**五种成因共用一句**）。见 (1)。 */
export const DISPATCH_JOURNAL_NOT_FOUND_MESSAGE = 'not found'
/** 中间件层的 401 文案（走的是 `detail` 形状，不是 `message`，见 (3)）。 */
export const DISPATCH_JOURNAL_UNAUTHORIZED_MESSAGE = 'authentication required'

/** `notice.go:57-67` 的九个动作取值。★ 后端可新增，故解包期**不硬拒**未知取值。 */
export const DISPATCH_JOURNAL_ACTIONS = [
  'retry_same_cred',
  'switch_cred',
  'switch_model',
  'capacity_wait',
  'scheduled_wait',
  'completed',
  'failed',
  'canceled',
] as const

/** 终态三值 —— 见 (7) 的「末尾条目恒为终态」。 */
export const DISPATCH_JOURNAL_TERMINAL_ACTIONS = ['completed', 'failed', 'canceled'] as const

/** 特权两值：可读任意租户。见 (13)。 */
export const DISPATCH_JOURNAL_PRIVILEGED_ROLES = ['super_admin', 'admin_key'] as const
/** 租户档：只能读自己租户。 */
export const DISPATCH_JOURNAL_TENANT_ROLE = 'tenant_admin'

/** `pipeline.go:1874` 的 `maxJournalSnapshotEvents`。见 (6)。 */
export const DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES = 50
/** `journal.go:74` 的 `journalCapacity` —— 环形上限，**它的丢弃不会被报告**。见 (5)。 */
export const DISPATCH_JOURNAL_RING_CAPACITY = 128
/** `main.go:1080` 的 LRU 容量 —— 满了就淘汰 ⇒ 404。见 (4)。 */
export const DISPATCH_JOURNAL_STORE_CAPACITY = 10000

/** `journalSnapshotResponse` 的 6 键（都无 omitempty）。 */
export const DISPATCH_JOURNAL_SNAPSHOT_KEYS = [
  'tenant_id',
  'request_id',
  'entries',
  'truncated',
  'truncated_count',
  'snapshot_version',
] as const

/** `JournalEntry` 的 5 个恒在键（无 omitempty）。见 (11)。 */
export const DISPATCH_JOURNAL_ENTRY_REQUIRED_KEYS = ['seq', 'at', 'action', 'attempt', 'counts'] as const

/** `JournalEntry` 的 12 个 `omitempty` 可选键。★ **存在 0 不代表键会存在**。见 (11)。 */
export const DISPATCH_JOURNAL_ENTRY_OPTIONAL_KEYS = [
  'model',
  'credential_id',
  'provider_id',
  'vendor',
  'error_kind',
  'http_status',
  'from_model',
  'to_model',
  'from_credential_id',
  'to_credential_id',
  'from_provider_id',
  'to_provider_id',
] as const

/** `ActionCounts` 的 5 键（无 omitempty）。见 (12)。 */
export const DISPATCH_JOURNAL_COUNTS_KEYS = [
  'retries',
  'node_switches',
  'model_switches',
  'capacity_waits',
  'scheduled_waits',
] as const

/** `JournalEntry` 的 6 个字符串型可选键。 */
const OPTIONAL_STRING_KEYS = [
  'model',
  'vendor',
  'error_kind',
  'from_model',
  'to_model',
] as const
/** `JournalEntry` 的 6 个数字型可选键（★ `omitempty` 会吃掉 0，见 (11)）。 */
const OPTIONAL_NUMBER_KEYS = [
  'credential_id',
  'provider_id',
  'http_status',
  'from_credential_id',
  'to_credential_id',
  'from_provider_id',
] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface DispatchJournalCounts {
  /** 累计重试次数（非递减）。 */
  retries: number
  node_switches: number
  model_switches: number
  capacity_waits: number
  scheduled_waits: number
}

export interface DispatchJournalEntry {
  /** ★ 首条为 1，相邻差 1（见 (9)）。 */
  seq: number
  /** `time.Time` ⇒ RFC3339Nano。 */
  at: string
  /** ★ 字符串即可，**不硬拒**未知取值（后端可新增）。 */
  action: string
  /** 该事件之后的累计 AttemptCount。 */
  attempt: number
  /** 该事件之后的累计分类计数快照（★ **不是**权威尝试次数，见 (10)）。 */
  counts: DispatchJournalCounts
  model?: string
  /** ★★ `int` + `omitempty` ⇒ **值为 0 时键不存在**，0 与「没有」不可区分。见 (11)。 */
  credential_id?: number
  provider_id?: number
  vendor?: string
  error_kind?: string
  http_status?: number
  from_model?: string
  to_model?: string
  from_credential_id?: number
  to_credential_id?: number
  from_provider_id?: number
  to_provider_id?: number
}

export interface DispatchJournalSnapshot {
  /** ★ 恒等于请求路径里那一段（后端 `:83-86` 会校验，不符就 404）。 */
  tenant_id: string
  request_id: string
  /** ★ handler 显式把 nil 补成 `[]`（`:87-89`）⇒ **恒为数组、永不为 `null`**。 */
  entries: DispatchJournalEntry[]
  /** ★★ 只覆盖**第二段**截断；第一段（环形 128）的丢弃它看不见。见 (5)。 */
  truncated: boolean
  /** ★ `truncated === true` ⇒ 本值 ≥ 1；`=== 0` ⟺ `!truncated`。见 (6)。 */
  truncated_count: number
  /** ★ 恒等于最后一条的 `seq`。见 (8)。 */
  snapshot_version: number
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * 拼路径（**两段都要 `encodeURIComponent`** —— 后端按 `EscapedPath()` 先切分后解码，
 * 所以不编码的 `/` 会把路径切错，而编码过的 `%2F` 会被后端解码后拒掉。见 (14)）。
 */
export function dispatchJournalPath(tenantId: string, requestId: string): string {
  return `${DISPATCH_JOURNAL_PATH_PREFIX}${encodeURIComponent(tenantId)}/${encodeURIComponent(requestId)}`
}

/** GET `/api/admin/dispatch/journal/{tenant}/{request_id}`（admin 档，见 (13)）。 */
export function fetchDispatchJournal(
  params: { tenantId: string; requestId: string },
  options?: RequestOptions,
): Promise<DispatchJournalSnapshot> {
  const path = dispatchJournalPath(params.tenantId, params.requestId)
  return req<unknown>('GET', path, undefined, options).then(unwrapDispatchJournalSnapshot)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapDispatchJournalSnapshot(resp: unknown): DispatchJournalSnapshot {
  const d = requireObject(resp, '调度轨迹快照')
  requireKeys(d, DISPATCH_JOURNAL_SNAPSHOT_KEYS, '调度轨迹快照')
  if (typeof d['tenant_id'] !== 'string') throw new Error('调度轨迹快照 的 tenant_id 不是字符串')
  if (typeof d['request_id'] !== 'string') throw new Error('调度轨迹快照 的 request_id 不是字符串')
  if (d['tenant_id'] === '') throw new Error('调度轨迹快照 的 tenant_id 是空串')
  if (d['request_id'] === '') throw new Error('调度轨迹快照 的 request_id 是空串')
  if (typeof d['truncated'] !== 'boolean') throw new Error('调度轨迹快照 的 truncated 不是布尔')
  if (typeof d['truncated_count'] !== 'number') throw new Error('调度轨迹快照 的 truncated_count 不是数字')
  if (typeof d['snapshot_version'] !== 'number') throw new Error('调度轨迹快照 的 snapshot_version 不是数字')

  const entries = requireArray(d['entries'], '调度轨迹快照 的 entries')
  for (let i = 0; i < entries.length; i++) {
    const o = requireObject(entries[i], `调度轨迹快照 的 entries[${i}]`)
    requireKeys(o, DISPATCH_JOURNAL_ENTRY_REQUIRED_KEYS, `调度轨迹快照 的 entries[${i}]`)
    if (typeof o['seq'] !== 'number') throw new Error(`entries[${i}] 的 seq 不是数字`)
    if (typeof o['at'] !== 'string') throw new Error(`entries[${i}] 的 at 不是字符串`)
    // ★ 只校验是字符串：后端可能新增动作取值，硬拒会让客户端在网关升级后崩。
    if (typeof o['action'] !== 'string') throw new Error(`entries[${i}] 的 action 不是字符串`)
    if (typeof o['attempt'] !== 'number') throw new Error(`entries[${i}] 的 attempt 不是数字`)

    const c = requireObject(o['counts'], `entries[${i}] 的 counts`)
    requireKeys(c, DISPATCH_JOURNAL_COUNTS_KEYS, `entries[${i}] 的 counts`)
    for (const k of DISPATCH_JOURNAL_COUNTS_KEYS) {
      if (typeof c[k] !== 'number') throw new Error(`entries[${i}] 的 counts 的 ${k} 不是数字`)
    }

    // ★ 12 个 omitempty 键：**存在才校验类型**，不存在是合法形状。
    for (const k of OPTIONAL_STRING_KEYS) {
      if (k in o && typeof o[k] !== 'string') throw new Error(`entries[${i}] 的 ${k} 不是字符串`)
    }
    for (const k of OPTIONAL_NUMBER_KEYS) {
      if (k in o && typeof o[k] !== 'number') throw new Error(`entries[${i}] 的 ${k} 不是数字`)
    }
  }
  return d as unknown as DispatchJournalSnapshot
}

function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
}
function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}
function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (6) 截断的两段式与自洽 ──

/**
 * ★★★★ 见 (6)：`truncated === true` ⇒ `entries.length === 50`
 * （`pipeline.go:1876-1879` 是精确切片）。⇒ 这条判据把「截断计数」与「窗口长度」互锁。
 */
export function dispatchJournalTruncatedFillsWindow(r: DispatchJournalSnapshot): boolean {
  return !r.truncated || r.entries.length === DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES
}

/**
 * ★★★★ 见 (6)：`truncated_count === 0` ⟺ `!truncated`
 * ⇒ 与 `dispatchJournalTruncatedFillsWindow` 互补（一个看长度、一个看计数）。
 */
export function dispatchJournalTruncatedCountMatchesFlag(r: DispatchJournalSnapshot): boolean {
  return (r.truncated_count === 0) === !r.truncated
}

// ── (5) 「没截断」不等于「完整」 ──

/**
 * ★★★★★ 见 (5)：**`truncated === false` 不能被当成「轨迹完整」** ——
 * 第一段（环形 128，`journal.go:137-140`）的丢弃是**隐形的**：
 * 只要首条 `seq > 1`，就说明那里已经丢过东西，而响应里**没有任何标记**。
 * ⇒ ★★★ 因此本模块**刻意不提供**「是否完整」这条布尔判据 —— 写它只能返回恒真或误导。
 * 正确用法是：自己拿 `entries[0].seq` 与 1 的差值判断第一段丢了多少。
 */
export function dispatchJournalLeadingEntriesWereDropped(r: DispatchJournalSnapshot): boolean {
  return r.entries.length > 0 && (r.entries[0] as DispatchJournalEntry).seq > 1
}

// ── (7) 末尾条目恒为终态 ──

/** ★★★★ 见 (7)：这条目的 `action` 属于终态三值。 */
export function dispatchJournalEntryIsTerminal(row: DispatchJournalEntry): boolean {
  return (DISPATCH_JOURNAL_TERMINAL_ACTIONS as readonly string[]).includes(row.action)
}

/**
 * ★★★★ 见 (7)：**最后一条恒为终态** —— 快照只在 `complete()` 的终态路径发射，
 * 且截断只砍头部 ⇒ 终态条目恒在最末。
 * ⇒ ★★ 空 `entries` 在真实响应里**不可达**（`emitJournalSnapshot` 遇空直接 return），
 *   但 handler 仍把 nil 补成 `[]`（`:87-89`）⇒ 解包放行、判据返回 false。
 */
export function dispatchJournalEndsWithTerminal(r: DispatchJournalSnapshot): boolean {
  const n = r.entries.length
  if (n === 0) return false
  return dispatchJournalEntryIsTerminal(r.entries[n - 1] as DispatchJournalEntry)
}

// ── (8)(9) seq 与 snapshot_version 的自洽 ──

/** ★★★★ 见 (9)：`seq` 首条为 1、严格递增且步长为 1（残留窗口内无空洞）。 */
export function dispatchJournalSeqIsContiguous(r: DispatchJournalSnapshot): boolean {
  if (r.entries.length === 0) return false
  for (let i = 1; i < r.entries.length; i++) {
    const prev = r.entries[i - 1] as DispatchJournalEntry
    const cur = r.entries[i] as DispatchJournalEntry
    if (cur.seq !== prev.seq + 1) return false
  }
  return true
}

/** ★★★★ 见 (9)：首条 `seq` 必为 1 —— 除非第一段已经丢过东西（见 (5)）。 */
export function dispatchJournalSeqStartsAtOne(r: DispatchJournalSnapshot): boolean {
  return r.entries.length > 0 && (r.entries[0] as DispatchJournalEntry).seq === 1
}

/** ★★★★ 见 (8)：`snapshot_version` 恒等于最后一条的 `seq`。 */
export function dispatchJournalVersionMatchesLastSeq(r: DispatchJournalSnapshot): boolean {
  const n = r.entries.length
  if (n === 0) return false
  return r.snapshot_version === (r.entries[n - 1] as DispatchJournalEntry).seq
}

// ── (10) attempt 与 counts 都是累计量 ──

/** ★★★★ 见 (10)：`attempt` 逐条非递减。 */
export function dispatchJournalAttemptIsNonDecreasing(r: DispatchJournalSnapshot): boolean {
  for (let i = 1; i < r.entries.length; i++) {
    if ((r.entries[i] as DispatchJournalEntry).attempt < (r.entries[i - 1] as DispatchJournalEntry).attempt) {
      return false
    }
  }
  return true
}

/** ★★★★ 见 (10)：`counts` 五个计数器逐条非递减。 */
export function dispatchJournalCountsAreCumulative(r: DispatchJournalSnapshot): boolean {
  for (let i = 1; i < r.entries.length; i++) {
    const prev = (r.entries[i - 1] as DispatchJournalEntry).counts
    const cur = (r.entries[i] as DispatchJournalEntry).counts
    for (const k of DISPATCH_JOURNAL_COUNTS_KEYS) {
      if (cur[k] < prev[k]) return false
    }
  }
  return true
}

/**
 * ★★★ 见 (10) 的语义注释（`journal.go:60-62`）：`counts` 是**分类视图**，
 * 权威的尝试上限是 `AttemptCount`；而且 **wait 类动作不计入 AttemptCount**
 * ⇒ 「五个计数器之和 == 尝试次数」这个等式**后端没有承诺**，
 * 而要判它成立需要一个本族**没有暴露**的口径
 * ⇒ ★★★ 因此**刻意不提供**这条判据（写出来只能是恒真或臆断），由本注释承担契约。
 */

// ── 时戳形状 ──

/**
 * ★★★ `At` 是 `time.Time` ⇒ JSON 恒为 **RFC3339Nano**（零值也序列化成
 * `0001-01-01T00:00:00Z`，**从不为 `null`**）。
 * ⇒ ★★ 刻意做成**判据**而不是解包期硬抛：Go 的 RFC3339Nano 会**裁掉小数末尾的零**，
 * 任何正则都只能取一个**包含**子集的方向 ⇒ 宁可漏判也不误拒。
 */
export function dispatchJournalAtIsRFC3339(row: DispatchJournalEntry): boolean {
  return /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.test(row.at)
}

// ── (11) omitempty 把 0 吃掉 ──
/**
 * ★★★ 见 (11)：某个 `omitempty` 数字键**值为 0 时整个键不存在** ——
 * 所以「键不存在」既可能是「没填」，也可能是「填了 0」，**不可区分**。
 * ⇒ ★★ 这条判据回答的是「这个 0 有没有落进响应」，不是「值是不是 0」。
 */
export function dispatchJournalOptionalNumberIsPresent(row: DispatchJournalEntry, key: string): boolean {
  return (row as unknown as Record<string, unknown>)[key] !== undefined
}

// ── (13) 权限：角色 × 路径租户 ──

/** ★★★★ 见 (13)：`super_admin` / `admin_key` 是特权角色，可读任意租户。 */
export function dispatchJournalRoleIsPrivileged(role: string): boolean {
  return (DISPATCH_JOURNAL_PRIVILEGED_ROLES as readonly string[]).includes(role)
}

/**
 * ★★★★ 见 (13)：给定角色与调用方租户，能否读 `pathTenant` 的快照。
 * 特权 ⇒ 恒可读；`tenant_admin` ⇒ 只能读**自己**租户；其它角色 ⇒ 一律不可。
 */
export function dispatchJournalRoleCanReadTenant(role: string, callerTenant: string, pathTenant: string): boolean {
  if (dispatchJournalRoleIsPrivileged(role)) return true
  if (role !== DISPATCH_JOURNAL_TENANT_ROLE) return false
  return callerTenant === pathTenant
}

// ── (14) 路径段合法性 ──

/**
 * ★★★★ 见 (14)：一个路径段要被后端接受，必须
 * **trim 后非空**、**不含 `/` `\` NUL**、且**不是 `.` 或 `..`**。
 * ⇒ ★★ 编码过的 `%2F` 不会穿越路径（切分在解码之前），但解码后会被拒。
 */
export function dispatchJournalSegmentIsAcceptable(segment: string): boolean {
  const t = segment.trim()
  if (t === '') return false
  if (t.includes('/') || t.includes('\\') || t.includes('\u0000')) return false
  if (t === '.' || t === '..') return false
  return true
}

/** ★★★ 两段都合法才值得发请求 —— 否则必然是 400 `invalid journal path`。 */
export function dispatchJournalParamsAreAcceptable(tenantId: string, requestId: string): boolean {
  return dispatchJournalSegmentIsAcceptable(tenantId) && dispatchJournalSegmentIsAcceptable(requestId)
}

// ── (5) 快照是易失的进程内缓冲 ──

/**
 * ★★★★★ 见 (4)：`entries` 为空 ⇒ 这个快照**要么**根本没这条轨迹，
 * **要么**它已经被 LRU 淘汰 / 进程重启冲掉了 —— 后端返回的是**同一个 200 空快照**。
 * ⇒ ★★★ 客户端**不能**把空 `entries` 读成「这个请求没有重试或切换」。
 */
export function dispatchJournalEmptyEntriesAreAmbiguous(r: DispatchJournalSnapshot): boolean {
  return r.entries.length === 0
}
