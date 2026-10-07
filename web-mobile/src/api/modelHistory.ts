import { req, type RequestOptions } from './client'

/**
 * modelHistory.ts — 凭据模型状态变更历史（2026-10-08，第九十二批）。
 *
 * GET /api/credentials/model-history?credential_id=X&raw_model_name=Y&limit=50
 *
 * - **注册**：**第五种注册形态**，`mux.HandleFunc` 在**另一个文件的方法**里
 *   （`admin/credential_monitor.go:153-163` 的 `RegisterMonitorRoutes`），
 *   由 `admin/handler.go:1453` 现构造并挂载：
 *   ```go
 *   monitorH.RegisterMonitorRoutes(mux, h.admin)   // ★ admin 档，不是 superAdmin
 *   ```
 *   ★ 与批 91 的 `work-types`（`h.superAdmin`）同属 `admin` 包、两种档位。
 * - **实现**：`admin/credential_monitor.go:1433-1478`（handler）
 *   + `:1304-1318`（`ModelHistoryEvent`）+ `:1329-1430`（`runModelHistory` 的 SQL）。
 * - **桌面调用方**：`web/src/api/credential-monitor.ts:347-358` —— `req<ModelHistoryResponse>`
 *   直接强转，**不做任何校验** ⇒ 全部校验由本模块补上。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十四件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **整族是 `h.admin` 档**（`handler.go:1453` 传 `h.admin`）
 *     ⇒ tenant_admin **可用** ⇒ 移动端抽屉席**不设** `requiresRole`。
 *     ⇒ ★ 与批 91 的 `work-types`（`h.superAdmin`）正好相反，两族都在 admin 包里。
 *
 * (2) ★★★★★ **事件对象是「十个键全部恒在，其中六个可为裸 `null`」的形状**。
 *     `ModelHistoryEvent`（`:1307-1318`）**十个字段一个 `omitempty` 都没有**：
 *     ```go
 *     TriggeredBy  *string `json:"triggered_by"`
 *     ProbeStatus  *string `json:"probe_status"`
 *     HTTPStatus   *int    `json:"http_status"`
 *     ErrorCode    *string `json:"error_code"`
 *     ErrorMessage *string `json:"error_message"`
 *     Actor        *string `json:"actor"`
 *     Reason       *string `json:"reason"`
 *     ```
 *     `nullableString` / `nullableInt` 返回 `nil` 时**指针被写成 JSON `null`，键仍在**。
 *     ⇒ ★★★ ⇒ **`'probe_status' in ev` 这种存在性判断永远为真**；
 *       要判「有没有值」必须判 `ev.probe_status !== null`。
 *     ⇒ ★★★ **桌面类型把这六个键声明成了 `probe_status?: string | null`（可选）**
 *       （`web/src/api/credential-monitor.ts:330-337`）⇒ **桌面比现实宽松**：
 *       照它写出来的客户端会一直以为「键可能不存在」。
 *
 * (3) ★★★★★ **`reason` 的「空」有两种编码，且与其他六个指针键相反**（`:1416-1418`）：
 *     ```go
 *     ev.Actor = nullableString(actor)                          // 只看 Valid ⇒ 空串原样保留
 *     if reason.Valid && reason.String != "" { ev.Reason = &reason.String }  // 多一个非空判据
 *     ```
 *     SQL 侧是 `COALESCE(al.after_json->>'reason', '')`（`:1361`）
 *     ⇒ **空串会塌成 `null`**，而 `triggered_by` / `actor` 等只看 `Valid`。
 *     ⇒ ⇒ **`actor` 可以是空串，`reason` 不可能是空串**。
 *     ⇒ ★★★ ⇒ **「reason 不为空串」是一条恒真判据**（取值域是 {null, 非空串}），
 *       变异实测无法在可达输入域上打掉它 ⇒ **本模块刻意不提供该判据**，由本条注释承担。
 *
 * (4) ★★★★★ **两段数据的字段形状互补，由 `source` 区分**：
 *     | 字段 | `source==='auto'` | `source==='manual'` |
 *     |---|---|---|
 *     | `triggered_by` | `mpr.triggered_by` | **恒 `null`**（`NULL::text`，`:1351`） |
 *     | `event` | `recovered` / `broke` | `online` / `offline` |
 *     | `probe_status` | `mpr.status` | **恒 `null`**（`:1355`） |
 *     | `http_status` | `mpr.http_status` | **恒 `null`**（`:1356`） |
 *     | `error_code` / `error_message` | 有值 | **恒 `null`**（`:1357-1358`） |
 *     | `actor` | **恒 `null`**（`:1345`） | `al.actor` |
 *     | `reason` | **恒 `null`**（`:1346`） | `after_json->>'reason'`（空串塌 null） |
 *     ⇒ ⇒ **`auto` 行恰好四个键恒 `null`，`manual` 行恰好六个键恒 `null`**
 *     ⇒ ⇒ **这是本族最强的自验判据来源**。
 *
 * (5) ★★★★★ **`event` 的值域随 `source` 变，不能全局枚举**：
 *     - auto：`mpr.state_change IN ('recovered','broke')`（`:1344`）⇒ 只有两值
 *     - manual：`CASE al.action WHEN '…toggle_online' THEN 'online'
 *       WHEN '…toggle_offline' THEN 'offline' END`（`:1353-1356`）**没有 ELSE**
 *     ⇒ ★★ `CASE` 无 `ELSE` 意味着「不匹配就是 `NULL`」，
 *       而 `Event` 是**非指针 `string`** ⇒ Scan 进 NULL 会失败 ⇒ 落进下面的 (7)。
 *     （实际不触发：`WHERE al.action IN (...)` 已经把两值之外的全排除了。）
 *
 * (6) ★★★★ **`ORDER BY ts DESC`（`:1378`）无 tiebreak**
 *     ⇒ ★★★ **同时间戳事件的顺序未定义** ⇒ 客户端**只能断言「非升序」**，
 *       **不能**断言严格降序（与批 88/89/90 那些无 tiebreak 的排序同族）。
 *
 * (7) ★★★★★ **scan 失败被 `continue` 静默跳过，响应里查不到**（`:1405-1408`）：
 *     ```go
 *     if scanErr := rows.Scan(…); scanErr != nil {
 *         scanFailures++
 *         slog.Warn("model history scan failed", …)
 *         continue
 *     }
 *     ```
 *     ⇒ ★★ 响应结构**没有 `scan_failures` 字段** ⇒ 用户与客户端都看不出这批数据是否完整
 *     ⇒ ★★ 好在有 `slog.Warn`（批 91 的 `work_types.go` 连 warn 都没有，只是裸 continue）。
 *
 * (8) ★★★★★ **`rows.Err()` 不吞，直接 5xx**（`:1422-1426` `return events, fmt.Errorf(...)`）
 *     ⇒ handler 转 `writeInternalErr`（`:1470`）。
 *     ⇒ ★★★ 注释自陈这是审计保证：「incomplete payloads never reach the UI」
 *     ⇒ ⇒ **与批 91 `work_types.go` 的「半修吞错」正好构成对照**：
 *       **同一个 admin 包里，一处选择不吞、一处吞掉三处。**
 *
 * (9) ★★★★★ **`limit` 越界 400，范围 1–200，默认 50**（`:1448-1451`）。
 *     ⇒ 但 ★★ **`queryInt` 解析失败时静默回落默认值**（`admin/handler.go:1534-1544`）：
 *     ```go
 *     v, err := strconv.Atoi(s)
 *     if err != nil { return def }   // ← 不报错
 *     ```
 *     ⇒ `limit=abc` ⇒ **静默当 50 用，不报 400**
 *     ⇒ `credential_id=abc` ⇒ 回落 0 ⇒ 400，但错误文案说的是 **required**（不是格式错）
 *     ⇒ ⇒ **客户端不能假设「400 就是参数非法」**，400 有两种成因。
 *
 * (10) ★★★★ **`credential_id == 0` 判 400**（`:1451-1454`）——
 *      **不是 `< 1`** ⇒ ★★★ **负数 credential_id 能通过这一关**并进 SQL。
 *
 * (11) ★★★★ **租户过滤是两段各自做的，但形状一致**：
 *      auto 侧 `AND ($4 = '' OR mpr.tenant_id = $4)`（`:1343`）、
 *      manual 侧 `AND ($4 = '' OR al.tenant_id = $4)`（`:1369`）⇒ **两侧都过滤**。
 *      `tenantID` 只在 `IsTenantAdmin(r)` 时取 `GetTenantID(r)`（`:1464-1467`），
 *      否则是 `""` ⇒ 那个 `$4 = ''` 分支放行全部租户。
 *      ⇒ ⇒ **tenant_admin 只看自己租户；super_admin 看全部**。
 *
 * (12) ★★★ **超时 5s**（`:1461` `context.WithTimeout(r.Context(), 5*time.Second)`）。
 *
 * (13) ★★★ **错误体是 `{"error":{"detail"}}`**（`writeError`，`handler.go:1494-1498`）
 *      ⇒ 与批 91 的 `writeJSONErrCtx`（`{message, code, type}`）**不是同一种**
 *      ⇒ ★★ 同一个 admin 包里至少有三种错误体（`detail` / `message+code+type` /
 *        `code+detail`），**按「写出层」分支，不按包**。
 *      503 的文案是 `database not configured`（`:1441`）——
 *      ★ **与批 90 的两种 503 文案都不同**，别按字符串跨族匹配。
 *
 * (14) ★★ **`events` 是 `make([]ModelHistoryEvent, 0)`（`:1389`）⇒ 空时是 `[]` 不是 `null`**
 *      ⇒ 且 ★★ **`count` 就是 `len(events)`**（`:1476`）⇒ 可自验 `count === events.length`。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验 envelope 的 4 个恒在键与类型、事件的 10 个**恒在**键与类型
 *   （6 个指针键接受 `null`），并为 (3)(4)(6)(9)(14) 各提供判据。
 *   ★ **不校验** `source` / `event` 的**取值**以外的东西——但这两项
 *     **确实校验取值**，因为它们是**有限枚举且由 SQL 的 IN/CASE 锁死**（与 (5) 同源）；
 *   ★ **不校验** `ts` 是合法 RFC3339（后端自己格式化出来的，不需要客户端再解一次），
 *     只校验它是字符串。
 */

export const MODEL_HISTORY_PATH = '/api/credentials/model-history'

/** ★★ (9) `queryInt(r,"limit",50)` 的缺省值。 */
export const MODEL_HISTORY_DEFAULT_LIMIT = 50
/** ★ (9) 越界即 400 的上下界（`limit < 1 || limit > 200`）。 */
export const MODEL_HISTORY_LIMIT_MIN = 1
export const MODEL_HISTORY_LIMIT_MAX = 200

/** ★★ (13) 三种错误体并存，本族用 `writeError` ⇒ `{error:{detail}}`。 */
export const MODEL_HISTORY_ERROR_DETAILS = {
  methodNotAllowed: 'method not allowed',
  dbNotConfigured: 'database not configured',
  credentialIdRequired: 'credential_id required',
  rawModelNameRequired: 'raw_model_name required',
  limitOutOfRange: 'limit must be 1-200',
} as const

/** (4)(5) `source` 的两值。 */
export const MODEL_HISTORY_SOURCES = ['auto', 'manual'] as const
/** ★ (5) `auto` 侧 `state_change IN ('recovered','broke')`。 */
export const MODEL_HISTORY_AUTO_EVENTS = ['recovered', 'broke'] as const
/** ★ (5) `manual` 侧 `CASE al.action` 的两个分支。 */
export const MODEL_HISTORY_MANUAL_EVENTS = ['online', 'offline'] as const

/** `ModelHistoryEvent` 的 **10 个 json tag**（★ 一个 `omitempty` 都没有，见 (2)）。 */
export const MODEL_HISTORY_EVENT_KEYS = [
  'ts',
  'source',
  'triggered_by',
  'event',
  'probe_status',
  'http_status',
  'error_code',
  'error_message',
  'actor',
  'reason',
] as const

/** 其中**恒为 `null` 会随 `source` 变化**的那几个（见 (4)）。 */
export const MODEL_HISTORY_AUTOFILLED_KEYS = [
  'triggered_by',
  'probe_status',
  'http_status',
  'error_code',
  'error_message',
] as const

/** `ModelHistoryResponse` 的 **4 个键**（全部恒在）。 */
export const MODEL_HISTORY_RESPONSE_KEYS = ['credential_id', 'raw_model_name', 'events', 'count'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface ModelHistoryEvent {
  /** ★ 后端 `ts.UTC().Format(time.RFC3339)`（`:1413`）⇒ UTC、秒精度。 */
  ts: string
  /** ★★ 取值见 `MODEL_HISTORY_SOURCES`；(4) 决定其余键的 null 分布。 */
  source: string
  /** ★★ **键恒在**，可为 `null`（manual 恒 null）。桌面把它声明成可选，见 (2)。 */
  triggered_by: string | null
  /** ★ 取值随 `source` 变，见 (5)。 */
  event: string
  /** ★★ 键恒在，可为 `null`（manual 恒 null）。 */
  probe_status: string | null
  /** ★★ 键恒在，可为 `null`（manual 恒 null）。 */
  http_status: number | null
  /** ★★ 键恒在，可为 `null`（manual 恒 null）。 */
  error_code: string | null
  /** ★★ 键恒在，可为 `null`（manual 恒 null）。 */
  error_message: string | null
  /** ★★ 键恒在，可为 `null`；★ 但**可以是空串**，见 (3)。 */
  actor: string | null
  /** ★★ 键恒在，可为 `null`；★ **永不为空串**（空串被折叠），见 (3)。 */
  reason: string | null
}

export interface ModelHistoryResponse {
  credential_id: number
  raw_model_name: string
  /** ★ (14) 恒为数组，空时是 `[]` 不是 `null`。 */
  events: ModelHistoryEvent[]
  /** ★ (14) 恒等于 `events.length`。 */
  count: number
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/credentials/model-history`。
 * ★ (9) 三个参数缺一即 400；`limit` 越界也 400，但**传非数字会静默当默认值**。
 */
export function fetchModelHistory(
  params: { credentialId: number; rawModelName: string; limit?: number },
  options?: RequestOptions,
): Promise<ModelHistoryResponse> {
  const q = new URLSearchParams()
  q.set('credential_id', String(params.credentialId))
  q.set('raw_model_name', params.rawModelName)
  q.set('limit', String(params.limit ?? MODEL_HISTORY_DEFAULT_LIMIT))
  return req<unknown>('GET', `${MODEL_HISTORY_PATH}?${q.toString()}`, undefined, options).then(unwrapModelHistory)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapModelHistory(resp: unknown): ModelHistoryResponse {
  const d = requireObject(resp, '模型历史')
  requireKeys(d, MODEL_HISTORY_RESPONSE_KEYS, '模型历史')
  if (typeof d['credential_id'] !== 'number') throw new Error('模型历史 的 credential_id 不是数字')
  if (typeof d['raw_model_name'] !== 'string') throw new Error('模型历史 的 raw_model_name 不是字符串')
  if (typeof d['count'] !== 'number') throw new Error('模型历史 的 count 不是数字')

  const events = d['events']
  if (!Array.isArray(events)) throw new Error('模型历史 的 events 不是数组')
  for (let i = 0; i < events.length; i++) {
    unwrapModelHistoryEvent(events[i], `模型历史 的 events[${i}]`)
  }
  return d as unknown as ModelHistoryResponse
}

/** ★★ (2) 十个键**全部必须存在**，其中六个可为 `null` —— 缺键抛、`null` 放行。 */
export function unwrapModelHistoryEvent(v: unknown, where: string): ModelHistoryEvent {
  const o = requireObject(v, where)
  requireKeys(o, MODEL_HISTORY_EVENT_KEYS, where)
  for (const k of ['ts', 'source', 'event'] as const) {
    if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  // ★★ 键恒在 + 可为 null ⇒ 用 requireNullable：**缺键抛错，null 放行**。
  for (const k of ['triggered_by', 'probe_status', 'error_code', 'error_message', 'actor', 'reason'] as const) {
    requireNullable(o[k], `${where} 的 ${k}`)
  }
  requireNullableNumber(o['http_status'], `${where} 的 http_status`)
  return o as unknown as ModelHistoryEvent
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

/** ★★ 键必须存在（由 `requireKeys` 兜住），值只能是 `string` 或 `null`。 */
function requireNullable(v: unknown, where: string): void {
  if (v === null) return
  if (typeof v !== 'string') throw new Error(`${where} 不是字符串也不是 null`)
}

function requireNullableNumber(v: unknown, where: string): void {
  if (v === null) return
  if (typeof v !== 'number') throw new Error(`${where} 不是数字也不是 null`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (14) count 就是 len(events) ──

/** ★★★ (14) `count === events.length` —— 后端直接 `len(events)`。 */
export function modelHistoryCountMatchesEvents(r: ModelHistoryResponse): boolean {
  return r.count === r.events.length
}

// ── (3) reason 的空串折叠 vs actor 的保留 ──
//
// ★★★ **这里刻意不提供任何判据。**
// `:1416-1418` 只在 `reason.Valid && reason.String != ""` 时才给 ev.Reason 赋值
// ⇒ `reason` 的取值域是 **{null, 非空串}**，`reason === ''` 在后端产出里**不可达**
// ⇒ ⇒ 「reason 永不为空串」这条**恒真判据**（`every(e => e.reason !== '')`）
//   **在可达输入域上任何一种写法都成立**，变异脚本实测：把它改成 `some(...)`、
//   改成 `=== ''`，**唯一能区分的输入全是后端产不出的**。
// ⇒ 按「恒真判据一律不提供、用注释承担契约」，该函数已从源码删除，
//   连同它的 4 条用例。★ 与批 90 的「判据可证冗余 ⇒ 删代码而不是补用例」同族。
//
// ★ 对照：**`actor` 只看 `sql.NullString.Valid`（`:1414`）⇒ `actor` 可以是空串**，
//   所以「actor 永不为空串」是**假**的 —— 解包器因此必须接受 `''`。

// ── (4) 两段数据的字段形状互补 ──

/**
 * ★★★★★ (4) `source === 'auto'` ⇒ `actor` 与 `reason` **恒 `null`**
 * （SQL 侧写死 `NULL::text`，`:1345-1346`）。
 */
export function modelHistoryAutoEventHasNullActorAndReason(e: ModelHistoryEvent): boolean {
  if (e.source !== 'auto') return false
  return e.actor === null && e.reason === null
}

/**
 * ★★★★★ (4) `source === 'manual'` ⇒ `triggered_by` / `probe_status` / `http_status`
 * / `error_code` / `error_message` **恒 `null`**（`:1351-1358`）。
 */
export function modelHistoryManualEventHasNullProbeFields(e: ModelHistoryEvent): boolean {
  if (e.source !== 'manual') return false
  return (
    e.triggered_by === null &&
    e.probe_status === null &&
    e.http_status === null &&
    e.error_code === null &&
    e.error_message === null
  )
}

/** ★★★★ (4) 的反面：**两段都不满足就是形状破了**（`source` 值域外时恒为 false）。 */
export function modelHistoryEventMatchesItsSource(e: ModelHistoryEvent): boolean {
  return modelHistoryAutoEventHasNullActorAndReason(e) || modelHistoryManualEventHasNullProbeFields(e)
}

// ── (5) event 取值随 source 变 ──

/** ★★ (5) `auto` 行的 `event` 只能是 `recovered` / `broke`。 */
export function modelHistoryAutoEventKindIsKnown(e: ModelHistoryEvent): boolean {
  if (e.source !== 'auto') return false
  return (MODEL_HISTORY_AUTO_EVENTS as readonly string[]).includes(e.event)
}

/** ★★ (5) `manual` 行的 `event` 只能是 `online` / `offline`。 */
export function modelHistoryManualEventKindIsKnown(e: ModelHistoryEvent): boolean {
  if (e.source !== 'manual') return false
  return (MODEL_HISTORY_MANUAL_EVENTS as readonly string[]).includes(e.event)
}

// ── (6) 排序无 tiebreak ──

/**
 * ★★★ (6) `ORDER BY ts DESC` **无 tiebreak** ⇒ 同时间戳顺序未定义
 * ⇒ 只能断言**非升序**，**不能**断言严格降序。
 */
export function modelHistoryEventsAreNonAscending(events: ModelHistoryEvent[]): boolean {
  for (let i = 1; i < events.length; i++) {
    if ((events[i - 1] as ModelHistoryEvent).ts < (events[i] as ModelHistoryEvent).ts) return false
  }
  return true
}

// ── (9)(10) 查询参数 ──

/**
 * ★★★ (9) `limit` 是否落在后端接受的 1–200 内。
 * ★ 提醒：这**只能**告诉调用方「这个值不会被 400」，
 *   传非数字时 `queryInt` 会静默回落成 50 —— 见 `modelHistoryLimitFallsBackOnNonNumeric`。
 */
export function modelHistoryLimitIsInRange(limit: number): boolean {
  return limit >= MODEL_HISTORY_LIMIT_MIN && limit <= MODEL_HISTORY_LIMIT_MAX
}

/**
 * ★★ (10) `credential_id == 0` 才 400（不是 `< 1`）⇒ 负数能过这一关。
 * ⇒ 这条判据只覆盖后端**实际拦下的那一格**。
 */
export function modelHistoryCredentialIdIsRejected(credentialId: number): boolean {
  return credentialId === 0
}
