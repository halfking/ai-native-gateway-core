import { req, type RequestOptions } from './client'

/**
 * formatAnomalies.ts — 响应格式异常：明细列表 + 按小时汇总（2026-10-08，第八十二批）。
 *
 * GET /api/admin/format-anomalies?limit=50&offset=0&provider=&model=&anomaly_type=&unresolved_only=
 * GET /api/admin/format-anomaly-summary?hours=24
 *
 * - **注册**（`admin/handler.go`）：
 *   - `:913` `h.superAdmin(h.handleFormatAnomalySummary)`
 *   - `:914` `h.superAdmin(h.handleFormatAnomalies)`
 *   - `:915` `h.superAdmin(h.handleFormatAnomalySubrouter)` —— `format-anomalies/{id}/resolve` 是 **POST 写操作**，本模块**不碰**
 *   ⇒ ★★ 两个 GET **都是 `h.superAdmin`** ⇒ 抽屉席须设 `requiresRole: 'super_admin'`，
 *     并同步 `src/components/shell/AppDrawer.spec.ts` 白名单。
 * - **实现**：`admin/format_anomalies.go`（list `:51-192`、summary `:194-267`）。
 * - **数据表**：`response_format_anomalies`（`sql/migrations/startup/454_response_format_anomalies.sql`，
 *   baseline `01-schema.sql:14325-14345`）。
 * ★ 与第八十一批的 `model_integrity_events` **是两张不同的表**：
 *   `admin/model_integrity.go:48-49` 的注释说 `ModelIntegritySummary` "Mirrors the SQL used by
 *   /api/admin/format-anomaly-summary" —— 指的是**聚合口径相似**，**不是同一张表**。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十五件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **`count` 在这个端点是「全表命中数」，不是本页长度 —— 与批 78/81 语义相反。**
 *      `:100-111` 先跑**独立的** `SELECT COUNT(*)` 得到 `total`，
 *      再跑带 `LIMIT/OFFSET` 的 list 查询，`:186-191` 把 `"count": total` 写进去。
 *      ⇒ ★★★ 恒成立的不变式是 **`count >= anomalies.length`**（count 是全量，本页是切片）。
 *      ⇒ ★★ 批 78 的 `count == tasks.length`、批 81 的 `count == events.length`
 *        **在这里都不成立** ⇒ **判据不能跨族照抄**。
 *
 * (2) ★★★★ `limit` 与 `offset` 都被**回显** ⇒ 分页判定可以精确做。
 *      与批 79/80/81（只回显 days、**不回显 limit**）不同 ⇒ 本族能直接判「还有下一页」。
 *      - `limit`：`:61-67`，缺省 50，`<=0 ⇒ 50`，`>500 ⇒ 500`
 *      - `offset`：`:68-71`，缺省 0，`<0 ⇒ 0`，**★ 没有上界**（`offset=999999` 照发）
 *
 * (3) ★★★★ `provider_code` 走 `LEFT JOIN providers` 补值，且**展示与过滤用同一个 COALESCE。**
 *      `:119` `COALESCE(rfa.provider_code, p.code) AS provider_code`，
 *      `:81` 的 WHERE 也是 `COALESCE(rfa.provider_code, p.code) = $1`。
 *      ⇒ ★★★ **可自验的不变式**：响应里若某行 `provider_code` 键缺失（COALESCE 结果为 NULL），
 *        那它**不可能是 provider 过滤的结果** —— 因为 `NULL = 'openai'` 求值为 NULL 而非真，
 *        WHERE 不会放它行。
 *      ⇒ ★ provider 行被删（`p.code` 也为 NULL）⇒ 键缺，而不是「空串」。
 *
 * (4) ★★★★ `response_structure` 是 `map[string]any` + omitempty ⇒ **空 map 整个键被省略。**
 *      `:28` `Structure map[string]any json:"response_structure,omitempty"`，
 *      解码入口 `:177` `jsoncol.Decode(...)`。
 *      ⇒ ★★★ **本仓第十种 nil 编码的第二次出现**（批 79 的 `by_supplier` 同款）。
 *      ⇒ ★★ 推论：**「键在」就意味着 map 非空**，所以「结构为空」这个判据是**恒真的**，
 *        按纪律**删掉**；只提供 `formatHasStructure(row)`（有牙）。
 *
 * (5) ★★★★ `request_id` 是**非指针 string**（`:17`）⇒ 恒在键，**可以是空串**。
 *      建表 `14328` 是 `request_id text NOT NULL`。
 *      ⇒ ★★★ 与批 81 的 `RequestID *string`（条件键）**正好相反**。
 *      ⇒ NOT NULL 只保证不是 NULL，**不保证非空串** ⇒ 解包器按 `string` 校验（可能为空）。
 *
 * (6) ★★★ 字段名与 JSON 键名不一致：`ContentSize` 对应的键是 **`content_size_bytes`**（`:27`）。
 *      ⇒ 客户端写 `content_size_bytes`，**不是** `content_size`。
 *
 * (7) ★★★★ `anomaly_type` 与 `severity` 在这张表都是**开放域**，不能校验封闭枚举。
 *      - `format_anomaly_recorder.go:31-38` 有八个常量（`missing_usage_block` 等），
 *        `data_loss_anomaly.go:22-36` 另有四个（`tools_restore_failed` 等）；
 *      - ★★ 但写入入口 `RecordDataAnomaly(ctx, anomalyType, severity, …)`（`:149`）
 *        **接受任意字符串** ⇒ 封闭枚举**不可能**成立。
 *      - 建表 `:40-41` **也没有 CHECK**。
 *      ⇒ ★★★ 与批 81 正好相反：那里 `anomaly_type` 被 SQL 硬编码（**可**严格校验）、
 *        `severity` 有四值域；这里**两个都是开放域**。
 *      ⇒ ⇒ 只提供「是否落在已知集合内」的判据，**不**在解包器里拒绝未知取值。
 *
 * (8) ★★★★ 两张表的 `severity` **缺省值不同**：
 *      `response_format_anomalies` 是 `DEFAULT 'medium'`（`454:41`），
 *      `model_integrity_events` 是 `DEFAULT 'low'`（`462:52`）。
 *      ⇒ ★ 跨表比较 severity 分布时不能想当然。
 *
 * (9) ★★★★ summary 的三个 AVG 是 `*float64` + omitempty ⇒ **`AVG(...)` 返回 NULL 时键被省略。**
 *      `:223-225` 三个 `AVG(...)` 全都直接进指针字段。
 *      ⇒ ★★★ 若某桶里 `content_size_bytes` 全是 NULL，`avg_content_size` 就是**键缺失**，
 *        不是 `0`、不是 `null`。
 *      ⇒ ⇒ 客户端算「平均」时不能把键缺当成 0 参与求和。
 *
 * (10) ★★★ summary 的聚合是 `COUNT(DISTINCT request_id)`，与明细的逐行计数不同。
 *      `:222` `COUNT(DISTINCT request_id) AS affected_requests`。
 *      ⇒ ★★★ 恒成立：**`affected_requests <= anomaly_count`**（去重数不会超过总数）。
 *      ⇒ 另有 `resolved_count <= anomaly_count`（`:226` 的 `FILTER (WHERE resolved)`）。
 *      ⇒ ★ 注意 `request_id` 是 `text NOT NULL`，但**可以是空串** ⇒ 空串会被算成一个"请求"。
 *
 * (11) ★★★★ summary 的排序是 `ORDER BY hour DESC, anomaly_count DESC`，**不是**整体按计数降序。
 *      `:230`。
 *      ⇒ ★★★ 客户端**不能**断言整张表按 `anomaly_count` 非增；
 *        只能断言「**同一 hour 内**按 `anomaly_count` 非增」。
 *      ⇒ 这是本族最容易被误用的一个不变量。
 *
 * (12) ★★★ summary 的 `LIMIT 200` 是**硬编码在 SQL 里**的（`:231`），**不是参数**。
 *      ⇒ ★★ 响应**不回显这个上限**，客户端只能靠 `count === 200` 反推可能被截断。
 *      ⇒ 与 list 的 `limit` 可调、且会回显，形成鲜明对照。
 *
 * (13) ★★★ `hours` 回显的是**生效值**，上界是 `24*30 = 720` 小时（30 天）。
 *      `:204-210`：`hours <= 0 ⇒ 24`，`hours > 720 ⇒ 720`。
 *      ⇒ ★ 与批 81 的 `days <= 30` 是**两套不同的窗口语义**（小时 vs 天）。
 *
 * (14) ★★★★ **503 检查排在 405 检查之前。**
 *      `:52-55`（`h.db == nil`）在 `:56-59`（方法不是 GET）之前。
 *      ⇒ ★★★ 所以「方法不是 GET **且** db 未配置」时拿到的是 **503 而不是 405**。
 *      ⇒ 与批 81 的 dispatcher 相同，但 list 端点自己也有这道检查（不是只在 dispatcher）。
 *
 * (15) ★★★ 三条 500 的文案**各不相同**，客户端可区分失败发生在哪一步。
 *      - `:109` `"count query failed"`（计数失败）
 *      - `:182` `"list query failed"`（列表失败）
 *      - `:258` `"summary query failed"`（汇总失败）
 *      ⇒ ★★ 与第八十批 `/storage/tables` 的「两条子路径文案**完全相同**」正好相反。
 *
 * ★ 又一次 **`withAllTenantReadOnlyTx`**（`:106`、`:143`、`:213`）
 *   ⇒ **本仓第六次「不隔离」**（前五次：批 75、76、77、81 等）。
 *   两张表都有 `tenant_id` 列且 list 会 SELECT 出来，但 WHERE **从不过滤**。
 *
 * ★ `unresolved_only` 用 `queryBool`（`handler.go:1546-1549`）：
 *   ```go
 *   return strings.EqualFold(s, "true") || s == "1"
 *   ```
 *   ⇒ `TRUE` / `True` / `1` 都为真；**其它任何值（包括 `yes`、`on`、`t`）都为假**
 *   ⇒ ★ 这是「不报错、不回落成缺省」的布尔解析：无法区分「没传」与「传了假值」。
 */

// ── 常量（后端字面量） ───────────────────────────────────────────────────────

/** `:61` `queryInt(r, "limit", 50)` */
export const FORMAT_ANOMALY_LIMIT_DEFAULT = 50
/** `:65` `limit > 500` ⇒ 回落 */
export const FORMAT_ANOMALY_LIMIT_MAX = 500
/** `:68` `queryInt(r, "offset", 0)`；**没有上界** */
export const FORMAT_ANOMALY_OFFSET_DEFAULT = 0
/** `:204` `queryInt(r, "hours", 24)` */
export const FORMAT_SUMMARY_HOURS_DEFAULT = 24
/** `:208-209` `hours > 24*30` ⇒ 回落 */
export const FORMAT_SUMMARY_HOURS_MAX = 720
/** `:231` 硬编码在 SQL 里的 `LIMIT 200`（**不参数化、不回显**）。见 (12)。 */
export const FORMAT_SUMMARY_SQL_LIMIT = 200

/** ★ `ContentSize` 字段对应的 JSON 键是 `content_size_bytes`，不是 `content_size`。见 (6)。 */
export const FORMAT_CONTENT_SIZE_KEY = 'content_size_bytes'

/**
 * 已知 `anomaly_type` 取值（`format_anomaly_recorder.go:31-38` 八个 +
 * `data_loss_anomaly.go:22-36` 四个）。
 *
 * ★★ 这是「已知集合」**不是**封闭枚举：`RecordDataAnomaly` 接受任意字符串，
 *   建表也没有 CHECK ⇒ 解包器**不会**因为未知取值而抛错。见 (7)。
 */
export const FORMAT_ANOMALY_KNOWN_TYPES = [
  'missing_usage_block',
  'zero_completion_tokens',
  'extraction_failed',
  'unexpected_structure',
  'null_usage_values',
  'persistence_failed',
  'json_marshal_failed',
  'log_harvested',
  'tools_restore_failed',
  'request_body_truncated',
  'body_decode_failed',
  'metadata_dropped',
] as const

/**
 * 这张表的 `severity` 建表缺省是 **`medium`**（`454:41`）。
 * ★ 与 `model_integrity_events` 的 `DEFAULT 'low'` **不同** ⇒ 跨表比较时别想当然。见 (8)。
 * ★★ 同样是**开放域**：`RecordDataAnomaly` 的 severity 参数是普通 string。
 */
export const FORMAT_SEVERITY_TABLE_DEFAULT = 'medium'

/** `queryBool`（`handler.go:1548`）认作真的两个形态：`EqualFold(s,"true")` 或 `s == "1"`。 */
export const FORMAT_BOOL_TRUE_LITERALS = ['true', '1'] as const

/** 明细行的七个恒在键。`request_id` 是**非指针 string**，见 (5)。 */
export const FORMAT_ANOMALY_RECORD_ALWAYS_KEYS = [
  'id',
  'detected_at',
  'request_id',
  'anomaly_type',
  'severity',
  'resolved',
  'created_at',
] as const

/** 明细行的十三个条件键（全是指针 + omitempty；`response_structure` 是 map）。 */
export const FORMAT_ANOMALY_RECORD_OPTIONAL_KEYS = [
  'provider_id',
  'provider_code',
  'client_model',
  'outbound_model',
  'usage_source',
  'expected_tokens',
  'actual_tokens',
  'content_size_bytes',
  'response_structure',
  'response_sample',
  'resolved_at',
  'resolution_notes',
  'tenant_id',
] as const

/** 汇总行的六个恒在键。 */
export const FORMAT_SUMMARY_ROW_ALWAYS_KEYS = [
  'hour',
  'anomaly_type',
  'severity',
  'anomaly_count',
  'affected_requests',
  'resolved_count',
] as const

/** 汇总行的五个条件键（两个指针 + 三个 `*float64`）。见 (9)。 */
export const FORMAT_SUMMARY_ROW_OPTIONAL_KEYS = [
  'provider_code',
  'client_model',
  'avg_content_size',
  'avg_expected_tokens',
  'avg_actual_tokens',
] as const

export const FORMAT_ANOMALIES_KEYS = ['anomalies', 'count', 'limit', 'offset'] as const
export const FORMAT_SUMMARY_KEYS = ['summaries', 'count', 'hours'] as const

/** 整型条件键（可空 ⇒ 0 与 NULL 必须靠 `in` 区分）。见 (4) 的同族陷阱。 */
export const FORMAT_ANOMALY_RECORD_NUMERIC_OPTIONAL_KEYS = [
  'provider_id',
  'expected_tokens',
  'actual_tokens',
  'content_size_bytes',
] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface FormatAnomalyRecord {
  id: number
  detected_at: string
  /** ★ 恒在（非指针 + 无 omitempty），**但可能是空串**。见 (5)。 */
  request_id: string
  /** ★ **开放域**，不可当封闭枚举校验。见 (7)。 */
  anomaly_type: string
  severity: string
  resolved: boolean
  created_at: string

  provider_id?: number
  /** ★ `COALESCE(rfa.provider_code, p.code)`；缺失 ⇒ 该行不可能匹配 provider 过滤。见 (3)。 */
  provider_code?: string
  client_model?: string
  outbound_model?: string
  usage_source?: string
  /** ★ **键在时值可能是 0**（指针指向 0）；键缺才是 NULL。 */
  expected_tokens?: number
  /** ★ 同上。 */
  actual_tokens?: number
  /** ★ 键名带 `_bytes` 后缀，与 Go 字段名 `ContentSize` 不一致。见 (6)。 */
  content_size_bytes?: number
  /** ★ `map[string]any` + omitempty ⇒ **键在就意味着非空**。见 (4)。 */
  response_structure?: Record<string, unknown>
  response_sample?: string
  resolved_at?: string
  resolution_notes?: string
  tenant_id?: string
}

export interface FormatAnomaliesResponse {
  /** ★ 恒数组（`make([]…, 0, limit)`）。 */
  anomalies: FormatAnomalyRecord[]
  /** ★★ **全表命中数**，不是本页长度。见 (1)。 */
  count: number
  /** ★ **回显**生效值。见 (2)。 */
  limit: number
  /** ★ 回显生效值（无上界）。见 (2)。 */
  offset: number
}

export interface FormatAnomalySummaryRow {
  hour: string
  anomaly_type: string
  severity: string
  anomaly_count: number
  /** ★ `COUNT(DISTINCT request_id)` ⇒ 恒 `<= anomaly_count`。见 (10)。 */
  affected_requests: number
  /** ★ 恒 `<= anomaly_count`。见 (10)。 */
  resolved_count: number

  provider_code?: string
  client_model?: string
  /** ★ `AVG(...)` 为 NULL 时**键被省略**，不是 0。见 (9)。 */
  avg_content_size?: number
  /** ★ 同上。 */
  avg_expected_tokens?: number
  /** ★ 同上。 */
  avg_actual_tokens?: number
}

export interface FormatAnomalySummaryResponse {
  /** ★ 恒数组（`make([]…, 0)`）。 */
  summaries: FormatAnomalySummaryRow[]
  /** ★ 恒等于 `summaries.length`（后端 `len(summaries)`）。 */
  count: number
  /** ★ 回显**生效**小时数，上界 720。见 (13)。 */
  hours: number
}

export interface FormatAnomaliesParams {
  limit?: number
  offset?: number
  provider?: string
  model?: string
  anomalyType?: string
  unresolvedOnly?: boolean
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/format-anomalies`（`handler.go:914`，**superAdmin 档**）。 */
export function fetchFormatAnomalies(
  params: FormatAnomaliesParams = {},
  options?: RequestOptions,
): Promise<FormatAnomaliesResponse> {
  const qs = new URLSearchParams()
  if (formatLimitIsSendable(params.limit)) qs.set('limit', String(Math.trunc(params.limit!)))
  // ★ offset 没有上界（`:68-71` 只挡负数）⇒ 只要非负整数就照发。
  if (formatOffsetIsSendable(params.offset)) qs.set('offset', String(Math.trunc(params.offset!)))
  // ★ 三个字符串过滤走 `strings.TrimSpace` ⇒ 空串等价于「不过滤」，但仍然照发。
  if (params.provider != null) qs.set('provider', params.provider)
  if (params.model != null) qs.set('model', params.model)
  if (params.anomalyType != null) qs.set('anomaly_type', params.anomalyType)
  if (params.unresolvedOnly != null) qs.set('unresolved_only', params.unresolvedOnly ? 'true' : 'false')
  const s = qs.toString()
  return req<unknown>(
    'GET',
    `/api/admin/format-anomalies${s ? '?' + s : ''}`,
    undefined,
    options,
  ).then(unwrapFormatAnomalies)
}

/**
 * GET `/api/admin/format-anomaly-summary`（`handler.go:913`，**superAdmin 档**）。
 *
 * ★ 上限 200 是**硬编码在 SQL 里**的，不可调也不回显（见 (12)）⇒ 本函数没有 limit 参数。
 */
export function fetchFormatAnomalySummary(
  params: { hours?: number } = {},
  options?: RequestOptions,
): Promise<FormatAnomalySummaryResponse> {
  const qs = new URLSearchParams()
  if (formatHoursIsSendable(params.hours)) qs.set('hours', String(Math.trunc(params.hours!)))
  const s = qs.toString()
  return req<unknown>(
    'GET',
    `/api/admin/format-anomaly-summary${s ? '?' + s : ''}`,
    undefined,
    options,
  ).then(unwrapFormatAnomalySummary)
}

/** 见 (2)：`1..500` 才发得出去（后端 `<=0 ⇒ 50`、`>500 ⇒ 500`）。 */
export function formatLimitIsSendable(limit: number | null | undefined): boolean {
  if (typeof limit !== 'number' || !Number.isFinite(limit)) return false
  const n = Math.trunc(limit)
  return n >= 1 && n <= FORMAT_ANOMALY_LIMIT_MAX
}

/** 见 (2)：offset 只要**非负整数**就发得出去（后端无上界）。 */
export function formatOffsetIsSendable(offset: number | null | undefined): boolean {
  if (typeof offset !== 'number' || !Number.isFinite(offset)) return false
  return Math.trunc(offset) >= FORMAT_ANOMALY_OFFSET_DEFAULT
}

/** 见 (13)：`1..720` 才发得出去（后端 `<=0 ⇒ 24`、`>720 ⇒ 720`）。 */
export function formatHoursIsSendable(hours: number | null | undefined): boolean {
  if (typeof hours !== 'number' || !Number.isFinite(hours)) return false
  const n = Math.trunc(hours)
  return n >= 1 && n <= FORMAT_SUMMARY_HOURS_MAX
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapFormatAnomalies(resp: unknown): FormatAnomaliesResponse {
  const d = requireObject(resp, '格式异常明细')
  requireKeys(d, FORMAT_ANOMALIES_KEYS, '格式异常明细')
  if (!Array.isArray(d['anomalies'])) throw new Error('格式异常明细 的 anomalies 不是数组')
  d['anomalies'].forEach((a, i) => requireAnomalyRow(a, `格式异常明细 的 anomalies[${i}]`))
  for (const k of ['count', 'limit', 'offset'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`格式异常明细 的 ${k} 不是数字`)
  }
  return d as unknown as FormatAnomaliesResponse
}

export function unwrapFormatAnomalySummary(resp: unknown): FormatAnomalySummaryResponse {
  const d = requireObject(resp, '格式异常汇总')
  requireKeys(d, FORMAT_SUMMARY_KEYS, '格式异常汇总')
  if (!Array.isArray(d['summaries'])) throw new Error('格式异常汇总 的 summaries 不是数组')
  d['summaries'].forEach((s, i) => requireSummaryRow(s, `格式异常汇总 的 summaries[${i}]`))
  if (typeof d['count'] !== 'number') throw new Error('格式异常汇总 的 count 不是数字')
  if (typeof d['hours'] !== 'number') throw new Error('格式异常汇总 的 hours 不是数字')
  return d as unknown as FormatAnomalySummaryResponse
}

function requireAnomalyRow(v: unknown, where: string): void {
  const d = requireObject(v, where)
  requireKeys(d, FORMAT_ANOMALY_RECORD_ALWAYS_KEYS, where)
  if (typeof d['id'] !== 'number') throw new Error(`${where} 的 id 不是数字`)
  // ★ 两个 time.Time：无 omitempty ⇒ 恒在 ⇒ 恒为非空串。
  for (const k of ['detected_at', 'created_at'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  // ★★ `request_id` 是非指针 string ⇒ 恒在，**但可以是空串**（NOT NULL ≠ 非空）。见 (5)。
  if (typeof d['request_id'] !== 'string') throw new Error(`${where} 的 request_id 不是字符串`)
  // ★ 两个开放域字符串：只校类型，不校取值。见 (7)。
  for (const k of ['anomaly_type', 'severity'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if (typeof d['resolved'] !== 'boolean') throw new Error(`${where} 的 resolved 不是布尔`)
  // ★ 四个整型条件键：键在时必须是数字（`0` 是合法值，不能用真值判断）。
  for (const k of FORMAT_ANOMALY_RECORD_NUMERIC_OPTIONAL_KEYS) {
    if (k in d && typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of [
    'provider_code',
    'client_model',
    'outbound_model',
    'usage_source',
    'response_sample',
    'resolved_at',
    'resolution_notes',
    'tenant_id',
  ] as const) {
    if (k in d && typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  // ★ `response_structure` 是 `map[string]any`：只可能是对象；键在即非空。见 (4)。
  if ('response_structure' in d && !isPlainObject(d['response_structure'])) {
    throw new Error(`${where} 的 response_structure 不是对象`)
  }
}

function requireSummaryRow(v: unknown, where: string): void {
  const d = requireObject(v, where)
  requireKeys(d, FORMAT_SUMMARY_ROW_ALWAYS_KEYS, where)
  if (typeof d['hour'] !== 'string') throw new Error(`${where} 的 hour 不是字符串`)
  for (const k of ['anomaly_type', 'severity'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  for (const k of ['anomaly_count', 'affected_requests', 'resolved_count'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['provider_code', 'client_model'] as const) {
    if (k in d && typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  // ★ 三个 `*float64` AVG：键在时必须是数字。见 (9)。
  for (const k of ['avg_content_size', 'avg_expected_tokens', 'avg_actual_tokens'] as const) {
    if (k in d && typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

/**
 * 见 (1)：`count` 是**全表命中数** ⇒ 恒 `>= anomalies.length`。
 *
 * ★★★ 这里是 `>=` 而不是 `===` —— 批 78/81 那个 `count == 本页长度` 的判据
 *   在本族**恒假**（除非 offset=0 且 limit 足够大且恰好装完）。
 */
export function formatCountCoversWholeResult(r: FormatAnomaliesResponse): boolean {
  return r.count >= r.anomalies.length
}

/** 见 (2)：本页没到末尾 ⇒ 还有下一页。 */
export function formatHasMorePages(r: FormatAnomaliesResponse): boolean {
  return r.offset + r.anomalies.length < r.count
}

/** 见 (2)：本页就是最后一页。 */
export function formatIsLastPage(r: FormatAnomaliesResponse): boolean {
  return r.offset + r.anomalies.length >= r.count
}

/**
 * 见 (3)：该行的 `provider_code` 缺失 ⇒ 它**匹配不了任何 provider 过滤**。
 *
 * ★ 依据：WHERE 与展示用的是同一个 `COALESCE(rfa.provider_code, p.code)`；
 *   COALESCE 结果为 NULL 时 `NULL = $1` 求值为 NULL（不为真）⇒ 不会被放出来。
 */
export function formatProviderCodeIsUnmatchable(row: FormatAnomalyRecord): boolean {
  return row.provider_code === undefined
}

/** 见 (4)：结构存在（map + omitempty ⇒ 键在即非空）。 */
export function formatHasStructure(row: FormatAnomalyRecord): boolean {
  return 'response_structure' in row
}

/** 见 (4)：整型条件键的「有没有」必须用 `in`，不能用真值。 */
export function formatHasNumericValue(
  row: FormatAnomalyRecord,
  key: 'provider_id' | 'expected_tokens' | 'actual_tokens' | 'content_size_bytes',
): boolean {
  return key in row
}

/**
 * ★★ 条件键的**正确取值入口**。
 *
 * ★★★ 为什么需要它：`content_size_bytes` 是**条件键**，解包器**不要求**它存在
 *   （缺键 = DB 里是 NULL，是合法形状）⇒ 用错键名（例如写成 `content_size`）
 *   **不会抛错**，只会静默拿到 `undefined`。
 *   ⇒ 调用方**不要**直接写 `row.content_size` / `row.contentSize`，
 *     一律走这个函数；它把「键名写错」从静默 undefined 变成可自测的取值。
 */
export function formatNumericValue(
  row: FormatAnomalyRecord,
  key: 'provider_id' | 'expected_tokens' | 'actual_tokens' | 'content_size_bytes',
): number | undefined {
  return key in row ? row[key] : undefined
}

/** ★ `0` 是合法值，不是「没有」。把被否定的写法一起断言出来。 */
export function formatNumericValueIsZero(
  row: FormatAnomalyRecord,
  key: 'provider_id' | 'expected_tokens' | 'actual_tokens' | 'content_size_bytes',
): boolean {
  return key in row && row[key] === 0
}

/** 见 (5)：`request_id` 恒在，但**可以是空串** —— 空串会被 `COUNT(DISTINCT)` 算成一个请求。 */
export function formatRequestIdIsEmpty(row: FormatAnomalyRecord): boolean {
  return row.request_id === ''
}

/** 见 (7)：`anomaly_type` 是否落在已知集合内（**不是**封闭枚举，超出也不代表出错）。 */
export function formatAnomalyTypeIsKnown(row: FormatAnomalyRecord | FormatAnomalySummaryRow): boolean {
  return (FORMAT_ANOMALY_KNOWN_TYPES as readonly string[]).includes(row.anomaly_type)
}

/** 见 (8)：severity 是否等于这张表的**建表缺省** `medium`（另一张表是 `low`）。 */
export function formatSeverityIsTableDefault(row: FormatAnomalyRecord | FormatAnomalySummaryRow): boolean {
  return row.severity === FORMAT_SEVERITY_TABLE_DEFAULT
}

// ── 汇总判据 ────────────────────────────────────────────────────────────────

/** 见 (10)：去重请求数不会超过总数。 */
export function formatSummaryAffectedWithinCount(s: FormatAnomalySummaryRow): boolean {
  return s.affected_requests <= s.anomaly_count
}

/** 见 (10)：已处置数不会超过总数。 */
export function formatSummaryResolvedWithinCount(s: FormatAnomalySummaryRow): boolean {
  return s.resolved_count <= s.anomaly_count
}

/**
 * 见 (11)：`ORDER BY hour DESC, anomaly_count DESC` ⇒ **`hour` 全局非增**。
 * ★ 不能断言整体按 `anomaly_count` 降序 —— 那是第二个排序键。
 */
export function formatSummaryIsHourDesc(r: FormatAnomalySummaryResponse): boolean {
  return r.summaries.every((s, i) => i === 0 || r.summaries[i - 1]!.hour >= s.hour)
}

/** 见 (11)：**同一 hour 内**按 `anomaly_count` 非增（并列允许）。 */
export function formatSummaryIsCountDescWithinHour(r: FormatAnomalySummaryResponse): boolean {
  return r.summaries.every((s, i) => {
    if (i === 0) return true
    const prev = r.summaries[i - 1]!
    if (prev.hour !== s.hour) return true
    return prev.anomaly_count >= s.anomaly_count
  })
}

/** 见 (9)：某个 AVG 键缺失（`AVG` 返回 NULL）。 */
export function formatSummaryAvgIsAbsent(
  s: FormatAnomalySummaryRow,
  key: 'avg_content_size' | 'avg_expected_tokens' | 'avg_actual_tokens',
): boolean {
  return !(key in s)
}

/** 见 (12)：`count` 拿满硬编码上限 ⇒ 可能被 SQL 的 `LIMIT 200` 截断。 */
export function formatSummaryAtSqlCap(r: FormatAnomalySummaryResponse): boolean {
  return r.count >= FORMAT_SUMMARY_SQL_LIMIT
}

/** 见 (13)：`hours` 是**生效**值 ⇒ 请求值被回落的次数即「窗口被改写」。 */
export function formatHoursWasRewritten(
  r: FormatAnomalySummaryResponse,
  requestedHours?: number,
): boolean {
  // ★ 显式收窄成局部常量：`noUncheckedIndexedAccess` 下可选参数仍是 `number | undefined`，
  //   直接 `Math.trunc(requestedHours)` 会被构建门（比 vue-tsc 更严）判 TS2345。
  const asked: number | undefined = requestedHours
  if (typeof asked !== 'number' || !Number.isFinite(asked)) return false
  return Math.trunc(asked) !== r.hours
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