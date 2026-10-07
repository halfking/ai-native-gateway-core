import { req, type RequestOptions } from './client'

/**
 * modelIntegrityDrift.ts — 指纹漂移事件（2026-10-08，第八十一批）。
 *
 * GET /api/admin/model-integrity/fingerprint-drift?days=7&limit=200
 *
 * - **注册**：`admin/handler.go:924-925`
 *   ```go
 *   mux.HandleFunc("/api/admin/model-integrity", h.superAdmin(h.handleModelIntegrity))
 *   mux.HandleFunc("/api/admin/model-integrity/", h.superAdmin(h.handleModelIntegrity))
 *   ```
 *   ⇒ ★ **整个前缀都是 `h.superAdmin`**，不是 admin 档。
 *   ⇒ ⇒ 接抽屉席时**必须**设 `requiresRole: 'super_admin'`，
 *     并同步 `src/components/shell/AppDrawer.spec.ts` 的白名单。
 * - **分发**：`admin/model_integrity.go:62-84` 的 switch（`summary` / `events` /
 *   `events/{id}/resolve` / `fingerprint-drift`；未命中 ⇒ `http.NotFound`）。
 * - **实现**：`admin/model_integrity.go:314-388`。
 * - **桌面调用方**：`web/src/api/integrity.ts:96-102`（`getModelIntegrityFingerprintDrift(days = 7)`），
 *   视图 `web/src/views/ModelIntegrityView.vue` 的 fingerprint-drift 标签页。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的九件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **十九个键里十四个是指针 + omitempty ⇒ 零值是「键缺失」，不是 `null`、不是 `0`。**
 *      `ModelIntegrityRecord`（`model_integrity.go:19-40`）里**所有**可选字段都是
 *      `*string` / `*int` / `*time.Time` / `any`，**没有一个**是可空值类型：
 *      ```go
 *      RequestID     *string `json:"request_id,omitempty"`
 *      ProviderID    *int    `json:"provider_id,omitempty"`
 *      ...
 *      Context       any     `json:"context,omitempty"`
 *      ```
 *      ⇒ ★★★ **五键恒在 / 十四键条件**：
 *      | 恒在（5） | `id`(int64) `detected_at`(time.Time) `anomaly_type` `severity` `resolved`(bool) |
 *      | 条件（14） | `request_id` `provider_id` `provider_code` `credential_id` `client_model`
 *      `outbound_model` `raw_model_name` `expected_value` `actual_value` `sample`
 *      `context` `resolved_at` `resolution_notes` `tenant_id` |
 *      对应建表（`sql/migrations/startup/462_model_integrity_events.sql:40-62`）全部可空。
 *
 * (2) ★★★★★ **★ 指针 + omitempty 与批七十八批的 `http_status` 同款陷阱：零值指针会原样出现。**
 *      `ProviderID *int`：DB 里 `provider_id = 0` ⇒ 扫成**非 nil 指针指向 0**
 *      ⇒ JSON 是 `provider_id: 0`（**键在**）；DB 里 `provider_id IS NULL` ⇒ 键被**省略**。
 *      ⇒ ⇒ ★★ 所以 `if (row.provider_id)` 会把**真实的 0** 误判成「没有值」。
 *      ⇒ 判据必须写 `'provider_id' in row`，**绝不能**用真值判断。
 *      ⇒ 这与第八十批 database 块的「值类型 + omitempty 会吃掉 0」正好相反，
 *        两个方向都在本仓出现过 ⇒ **先看 struct tag，再看声明类型**。
 *
 * (3) ★★★★★ **读路径跨全部租户：SELECT 出 `tenant_id` 却从不过滤。**
 *      `:328` 用 `withAllTenantReadOnlyTx(r.Context(), h.db, …)`，SQL 选了
 *      `tenant_id` 列并扫描进 `item.TenantID`，但 WHERE 只有
 *      `anomaly_type = 'fingerprint_drift'` 与时间窗，**没有任何租户条件**。
 *      ⇒ ★★★ **本仓第五次「不隔离」**（前四次：批 75 operational、批 76 self-check
 *        两处、批 77 routing 三处），且这次**档位是 superAdmin**。
 *      ⇒ tenant_admin 根本够不着这条路由，所以危害面比前几次小；
 *        但**平台超管看到的是所有租户的指纹漂移**（含 `sample` 与 `context`）。
 *
 * (4) ★★★★ **`anomaly_type` 在这个端点上是硬编码常量 ⇒ 可校验取值。**
 *      `:336` 的 WHERE 写死 `anomaly_type = 'fingerprint_drift'`，
 *      而 SELECT 又把该列扫进 `item.AnomalyType`（非指针、无 omitempty ⇒ 恒在）。
 *      ⇒ ★★★ 每一行的 `anomaly_type` **必然**等于 `'fingerprint_drift'`。
 *      ⇒ ⇒ 这是本族**唯一**可以严格校验取值的字符串键；
 *        `severity` 相反（见 (5)）。
 *
 * (5) ★★★★ `severity` 有注释声明的四值域，但**表上没有任何 CHECK 约束**。
 *      建表（`462:52`）写的是 `severity TEXT NOT NULL DEFAULT 'low'  -- low | medium | high | critical`，
 *      `baseline/01-schema.sql:9654` 同样**没有 CHECK**。
 *      ⇒ ★★★ 取值域只存在于**两处注释**里：
 *      `integrity/signals.go:80` 的 `type Severity string` 常量族，与
 *      `bg/integrity_probe_sink.go:49-51` 的硬编码 `"high"` / `"low"`。
 *      两条写入路径都在四值域内 ⇒ 可校验，但**依据是代码不是约束**。
 *      ⇒ ★ 与批七十八批的 `no_candidates`（不在 CHECK 内 ⇒ 那个分支项不可达）同族：
 *        **注释里的取值域不等于数据库约束。**
 *
 * (6) ★★★★ `days` 与 `limit` 都是**静默回落**，不是 400。
 *      ```go
 *      days := queryInt(r, "days", 7)
 *      if days <= 0 || days > 30 { days = 7 }
 *      limit := queryInt(r, "limit", 200)
 *      if limit <= 0 || limit > 500 { limit = 200 }
 *      ```
 *      而 `queryInt`（`handler.go:1534-1544`）用 `strconv.Atoi`，**解析失败直接返回缺省**。
 *      ⇒ `days=31` / `days=0` / `days=abc` / `days=" 7"` **四种写法都拿到 `days=7` 的数据**。
 *      ⇒ ★ 与批八十批的 `/storage/tables` 静默回落同款；
 *        与批七十八批 `/api/admin/probe/tasks` 的 **400** 仍是同仓两种风格。
 *
 * (7) ★★★ `days` 回显的是**生效窗口**（`"days": days` 写在 `:384`），**不是**请求值。
 *      ⇒ 请求 `days=31` 会拿到 `days: 7` 的数据并标着 7。
 *      ★★ 这与批七十六批 `errors/trend` 的 `range` 回显**请求值**正好相反
 *        （那个是回显 `xyz` 却给 24h 数据）⇒ **同一仓两种回显口径，必须逐端点读。**
 *      ⇒ 移动端可以直接拿 `resp.days` 当窗口长度，**不要**另存请求值。
 *
 * (8) ★★★ `count` 恒等于 `events.length`，且 `events` 恒数组。
 *      `:322` `items := make([]ModelIntegrityRecord, 0)` ⇒ 零行时是 `[]`，不是 `null`；
 *      `:383-385` `"count": len(items)` ⇒ 是**本页长度**，不是窗口内总数。
 *      ⇒ ★★ 响应**不回显 limit**（只回显 `days`）⇒ 截断只能靠
 *        「`count` 拿满请求上限」反推，见 `modelIntegrityDriftAtCap`。
 *
 * (9) ★★ 错误信封是**嵌套**的 `{"error":{"detail":"…"}}`，三条固定文案。
 *      - 非 GET ⇒ 405 `"method not allowed"`（`:315-318`）
 *      - `h.db == nil` ⇒ 503 `"database not configured"`（**dispatcher 层** `:56-59`，
 *        在子路径分发之前 ⇒ `summary` / `events` / `fingerprint-drift` 共享它）
 *      - 查询失败 ⇒ 500 `"drift query failed"`（`:379-382`）
 *      ⇒ ★ 与批七十九批 `errors/trend` 的 503 措辞 `database not configured` **逐字相同**，
 *        但 `errors/trend` 的 `db_not_configured` 那条是 `database is not configured`（多一个 is）。
 */

// ── 常量（后端字面量） ───────────────────────────────────────────────────────

/** `:320` `queryInt(r, "days", 7)` */
export const DRIFT_DAYS_DEFAULT = 7
/** `:321` `days > 30` ⇒ 回落 */
export const DRIFT_DAYS_MAX = 30
/** `:323` `queryInt(r, "limit", 200)` */
export const DRIFT_LIMIT_DEFAULT = 200
/** `:325` `limit > 500` ⇒ 回落 */
export const DRIFT_LIMIT_MAX = 500

/** `:336` WHERE 里硬编码的 `anomaly_type`。见 (4)。 */
export const DRIFT_ANOMALY_TYPE = 'fingerprint_drift'

/**
 * `severity` 的四值域。**依据是代码注释与写入端常量，不是 DB CHECK**（表上没有 CHECK）。见 (5)。
 * `integrity/signals.go:77-80` 的 `Severity` 常量族 + `bg/integrity_probe_sink.go:49-51` 的 `"high"`/`"low"`。
 */
export const DRIFT_SEVERITIES = ['low', 'medium', 'high', 'critical'] as const

/** 五个恒在键。 */
export const DRIFT_RECORD_ALWAYS_KEYS = [
  'id',
  'detected_at',
  'anomaly_type',
  'severity',
  'resolved',
] as const

/** 十四个指针 + omitempty 的条件键。 */
export const DRIFT_RECORD_OPTIONAL_KEYS = [
  'request_id',
  'provider_id',
  'provider_code',
  'credential_id',
  'client_model',
  'outbound_model',
  'raw_model_name',
  'expected_value',
  'actual_value',
  'sample',
  'context',
  'resolved_at',
  'resolution_notes',
  'tenant_id',
] as const

/** 两个数字键（`provider_id` / `credential_id`）：键在时值可能是真实的 **0**。见 (2)。 */
export const DRIFT_RECORD_NUMERIC_OPTIONAL_KEYS = ['provider_id', 'credential_id'] as const

export const DRIFT_KEYS = ['events', 'count', 'days'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface ModelIntegrityDriftRecord {
  id: number
  detected_at: string
  /** ★ 本端点恒为 `'fingerprint_drift'`（SQL 硬编码）。见 (4)。 */
  anomaly_type: string
  severity: string
  resolved: boolean

  request_id?: string
  /** ★ **键在时值可能是 0**（指针指向 0）；键缺失才是「真的没有」。见 (2)。 */
  provider_id?: number
  provider_code?: string
  /** ★ 同上，`0` 是合法值。 */
  credential_id?: number
  client_model?: string
  outbound_model?: string
  raw_model_name?: string
  expected_value?: string
  actual_value?: string
  sample?: string
  /** ★ 键在时**可能是空对象 `{}`**（`any` + omitempty 只挡 nil 接口）。 */
  context?: unknown
  resolved_at?: string
  resolution_notes?: string
  /** ★ 存在即说明这条记录**不属于当前租户**。见 (3)。 */
  tenant_id?: string
}

export interface ModelIntegrityDriftResponse {
  /** ★ 恒数组（`make([]…, 0)`），零行时是 `[]`。见 (8)。 */
  events: ModelIntegrityDriftRecord[]
  /** ★ 恒等于 `events.length`（后端 `len(items)`），不是窗口内总数。见 (8)。 */
  count: number
  /** ★ **生效窗口**，不是请求值。见 (7)。 */
  days: number
}

export interface ModelIntegrityDriftParams {
  days?: number
  limit?: number
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/admin/model-integrity/fingerprint-drift`（`handler.go:924-925` → **superAdmin 档**）。
 *
 * ★ `days` / `limit` 只在合法区间内才发得出去（见 (6)）⇒ 越界干脆不发，
 *   交给后端静默回落，省掉「发了才知道被改写」的一层不确定性。
 */
export function fetchModelIntegrityFingerprintDrift(
  params: ModelIntegrityDriftParams = {},
  options?: RequestOptions,
): Promise<ModelIntegrityDriftResponse> {
  const qs = new URLSearchParams()
  if (driftDaysIsSendable(params.days)) qs.set('days', String(Math.trunc(params.days!)))
  if (driftLimitIsSendable(params.limit)) qs.set('limit', String(Math.trunc(params.limit!)))
  const s = qs.toString()
  return req<unknown>(
    'GET',
    `/api/admin/model-integrity/fingerprint-drift${s ? '?' + s : ''}`,
    undefined,
    options,
  ).then(unwrapModelIntegrityDrift)
}

/** 见 (6)：`1..30` 才发得出去（后端 `days <= 0 || days > 30` ⇒ 回落 7）。 */
export function driftDaysIsSendable(days: number | null | undefined): boolean {
  // ★ 这两个守卫在当前实现里**都不可证有牙**（变异 #16 / #17 实测 STILL_GREEN 属可证等价）：
  //   - 去掉 `Number.isFinite`：`Infinity` 被上界 `<= 30` 挡住、`NaN` 被下界 `>= 1` 挡住；
  //   - 去掉 `typeof`：JS 里 `Number.isFinite(undefined)` 与 `Number.isFinite(null)`
  //     **本来就返回 false** ⇒ 这一项恒等于去掉后仍是同一结果。
  //   两者都保留：它们是**可读的显式条件**，且换掉任一项都不改变行为。
  if (typeof days !== 'number' || !Number.isFinite(days)) return false
  const n = Math.trunc(days)
  return n >= 1 && n <= DRIFT_DAYS_MAX
}

/** 见 (6)：`1..500` 才发得出去（后端 `limit <= 0 || limit > 500` ⇒ 回落 200）。 */
export function driftLimitIsSendable(limit: number | null | undefined): boolean {
  if (typeof limit !== 'number' || !Number.isFinite(limit)) return false
  const n = Math.trunc(limit)
  return n >= 1 && n <= DRIFT_LIMIT_MAX
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapModelIntegrityDrift(resp: unknown): ModelIntegrityDriftResponse {
  const d = requireObject(resp, '指纹漂移')
  requireKeys(d, DRIFT_KEYS, '指纹漂移')
  if (!Array.isArray(d['events'])) throw new Error('指纹漂移 的 events 不是数组')
  d['events'].forEach((e, i) => requireRecord(e, `指纹漂移 的 events[${i}]`))
  if (typeof d['count'] !== 'number') throw new Error('指纹漂移 的 count 不是数字')
  if (typeof d['days'] !== 'number') throw new Error('指纹漂移 的 days 不是数字')
  return d as unknown as ModelIntegrityDriftResponse
}

function requireRecord(v: unknown, where: string): void {
  const d = requireObject(v, where)
  // ★ 只校验五个恒在键；十四个条件键缺键是**合法**的（NULL 列被 omitempty 掉）。
  requireKeys(d, DRIFT_RECORD_ALWAYS_KEYS, where)
  if (typeof d['id'] !== 'number') throw new Error(`${where} 的 id 不是数字`)
  if (typeof d['detected_at'] !== 'string') throw new Error(`${where} 的 detected_at 不是字符串`)
  if (typeof d['anomaly_type'] !== 'string') throw new Error(`${where} 的 anomaly_type 不是字符串`)
  if (typeof d['severity'] !== 'string') throw new Error(`${where} 的 severity 不是字符串`)
  if (typeof d['resolved'] !== 'boolean') throw new Error(`${where} 的 resolved 不是布尔`)
  // ★★ `anomaly_type` 在本端点上是 SQL 硬编码的常量 ⇒ 可以**严格校验取值**。见 (4)。
  if (d['anomaly_type'] !== DRIFT_ANOMALY_TYPE) {
    throw new Error(`${where} 的 anomaly_type 不是 ${DRIFT_ANOMALY_TYPE}（实得 ${String(d['anomaly_type'])}）`)
  }
  // ★ 数字型条件键：键在时必须是数字（`provider_id` 可以是 0，**不能**用真值判断）。见 (2)。
  for (const k of DRIFT_RECORD_NUMERIC_OPTIONAL_KEYS) {
    if (k in d && typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  // ★ 字符串型条件键。
  for (const k of [
    'request_id',
    'provider_code',
    'client_model',
    'outbound_model',
    'raw_model_name',
    'expected_value',
    'actual_value',
    'sample',
    'resolved_at',
    'resolution_notes',
    'tenant_id',
  ] as const) {
    if (k in d && typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  // ★ `context` 是 `any`：可以是对象、数组、字符串、数字……**不能**按对象校验。
  //   Go 的 omitempty 对接口只在 nil 时省略 ⇒ 键在时可能是 `{}`、也可能是 `"abc"`。见 (1)。
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

/** 见 (8)：`count` 恒等于本页行数。 */
export function driftCountMatchesEvents(r: ModelIntegrityDriftResponse): boolean {
  return r.count === r.events.length
}

/** 见 (8)：响应**不回显 limit** ⇒ 拿满请求上限才算「可能截断」。 */
export function modelIntegrityDriftAtCap(
  r: ModelIntegrityDriftResponse,
  requestedLimit?: number,
): boolean {
  const lim =
    typeof requestedLimit === 'number' && Number.isFinite(requestedLimit)
      ? Math.trunc(requestedLimit)
      : DRIFT_LIMIT_DEFAULT
  if (lim < 1) return false
  return r.events.length >= lim
}

/** 见 (7)：响应里的 `days` 是**生效窗口**。请求值被回落的次数 ⇒ 判定为「窗口被改写」。 */
export function driftDaysWasRewritten(
  r: ModelIntegrityDriftResponse,
  requestedDays?: number,
): boolean {
  if (typeof requestedDays !== 'number' || !Number.isFinite(requestedDays)) return false
  const asked = Math.trunc(requestedDays)
  return asked !== r.days
}

/**
 * 见 (3)：响应里出现了**别的租户**的记录。
 * 读路径跨全部租户 ⇒ `tenant_id` 键在即说明这条不属于调用方租户。
 * ⚠️ 它同时**说明不了**「全都属于别的租户」—— 单租户部署时每个键都在。
 */
export function driftHasForeignTenants(r: ModelIntegrityDriftResponse): boolean {
  return r.events.some((e) => e.tenant_id !== undefined)
}

/** 见 (3)：窗口内是否**一个租户都没有**（部署形态异常时的信号）。 */
export function driftHasNoTenantTags(r: ModelIntegrityDriftResponse): boolean {
  return r.events.length > 0 && r.events.every((e) => e.tenant_id === undefined)
}

/**
 * 见 (2)：**指针型数字键的「有没有」必须用 `in` 判断，不能用真值。**
 *
 * - 键在（哪怕值是 `0`）⇒ 后端确实拿到了这一列的值
 * - 键缺失 ⇒ DB 里是 NULL
 */
export function driftHasNumericValue(
  row: ModelIntegrityDriftRecord,
  key: 'provider_id' | 'credential_id',
): boolean {
  return key in row
}

/**
 * ★★ `driftHasNumericValue` 的**反面**：`0` 是合法值，不是「没有」。
 * 变异脚本里把 `key in row` 换成 `!!row[key]` 会被这条打红。
 */
export function driftNumericValueIsZero(
  row: ModelIntegrityDriftRecord,
  key: 'provider_id' | 'credential_id',
): boolean {
  // ★ `key in row` 这一项是**可证冗余**的（变异 #33 实测 STILL_GREEN 属可证等价）：
  //   在 JS 里 `k in obj && obj[k] === 0` ≡ `obj[key] === 0` —— `in` 多出来的那部分
  //   只在值是 `undefined` 时才有区别，而 `undefined === 0` 本来就恒假。
  //   保留 `key in row` 是为了把「键在」这件事写出来（与 driftHasNumericValue 对称）。
  return key in row && row[key] === 0
}

/** 见 (1)：`context` 键在时**可能是空对象** —— 那是「解码出空 JSON」，不是「没有上下文」。 */
export function driftHasContextKey(row: ModelIntegrityDriftRecord): boolean {
  return 'context' in row
}

/** `context` 是空对象 / 空数组时的判定（键在但没内容）。 */
export function driftContextIsEmpty(row: ModelIntegrityDriftRecord): boolean {
  // ★ 键缺守卫同样是**可证冗余**的（变异 #37 实测 STILL_GREEN 属可证等价）：
  //   键缺时 `c` 是 `undefined` ⇒ `Array.isArray(undefined)` 为 false、
  //   `typeof undefined !== 'object'` 为 false ⇒ 落到最后一行 `return false`，
  //   与守卫的返回值**完全一致**。
  //   ★ 与「判据被上游检查截胡」相反：这里是**守卫被下游兜住**，两种形态都是等价变异。
  if (!('context' in row)) return false
  const c = row.context
  // ★ 数组分支是**可证冗余**的（变异 #36 实测 STILL_GREEN 属可证等价）：
  //   JSON 不产生稀疏数组，而对任意非稀疏数组 `A`：`Object.keys(A).length === 0` ⇔ `A.length === 0`
  //   ⇒ 落到下面的对象分支会得到同一答案。
  //   保留它是为了让「数组看 length、对象看 keys」这件事显式（也可读性优先）。
  if (Array.isArray(c)) return c.length === 0
  if (c !== null && typeof c === 'object') return Object.keys(c as object).length === 0
  return false
}

/**
 * 有处置痕迹：`resolved === true` **或** `resolved_at` 键在。
 *
 * ★ 两个方向都**不能**单独断言：
 *   `resolved=true ⇒ resolved_at 键在` 不成立（DB 里 resolved 可以为真而 resolved_at 仍 NULL）；
 *   `resolved_at 在 ⇒ resolved=true` 同样没有 DB 约束保证。
 * ⇒ 本判据把两个信号取并集，**任一**存在就算「有痕迹」，两个方向都被覆盖。
 */
export function driftHasResolutionTrace(row: ModelIntegrityDriftRecord): boolean {
  return row.resolved === true || 'resolved_at' in row
}

/** 处置痕迹的完整形态：两者都齐（调用方真正展示「何时处置」时用）。 */
export function driftHasFullResolution(row: ModelIntegrityDriftRecord): boolean {
  return row.resolved === true && 'resolved_at' in row && 'resolution_notes' in row
}

/** 未处置的精确口径：`resolved === false` 且两个痕迹键都缺。 */
export function driftIsUnresolved(row: ModelIntegrityDriftRecord): boolean {
  return row.resolved === false && !('resolved_at' in row) && !('resolution_notes' in row)
}

/** 见 (5)：`severity` 是否落在代码声明的四值域内（**不是** DB CHECK 保证的）。 */
export function driftSeverityIsKnown(row: ModelIntegrityDriftRecord): boolean {
  return (DRIFT_SEVERITIES as readonly string[]).includes(row.severity)
}

/** 见 (5)：四值域外的取值（表上没有 CHECK ⇒ 理论可达）。 */
export function driftSeverityIsUnknown(row: ModelIntegrityDriftRecord): boolean {
  return !driftSeverityIsKnown(row)
}

/** 见 (4)：本端点的 `anomaly_type` 恒定，故 `resolved_at` 与 `id` 之外不必再校验。 */
export function driftEventsAreAllFingerprintDrift(r: ModelIntegrityDriftResponse): boolean {
  return r.events.every((e) => e.anomaly_type === DRIFT_ANOMALY_TYPE)
}

/** 排序是 `ORDER BY ts DESC` ⇒ `detected_at` 非增。 */
export function driftIsNewestFirst(r: ModelIntegrityDriftResponse): boolean {
  return r.events.every((e, i) => i === 0 || r.events[i - 1]!.detected_at >= e.detected_at)
}

/** 见 (8)：零行窗口（返回的是 `[]` 不是 `null`）。 */
export function driftIsEmptyWindow(r: ModelIntegrityDriftResponse): boolean {
  return r.events.length === 0 && r.count === 0
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