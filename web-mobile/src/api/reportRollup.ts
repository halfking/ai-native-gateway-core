import { req, type RequestOptions } from './client'

/**
 * reportRollup.ts — 对账汇总与筛选栏候选（2026-10-08，第八十四批）。
 *
 * GET /api/admin/report-rollup/summary
 * GET /api/admin/report-rollup/dimensions
 *
 * - **注册**：`admin/handler.go:1067`
 *   `mux.HandleFunc("/api/admin/report-rollup/", h.superAdmin(h.handleReportRollup))`
 *   ⇒ ★★ **整个前缀（含尾斜杠）都是 `h.superAdmin`** ⇒ 抽屉席须设
 *     `requiresRole: 'super_admin'`，并同步 `AppDrawer.spec.ts` 白名单。
 *   ⇒ ★ 前缀**带尾斜杠** ⇒ 请求 `/api/admin/report-rollup`（无尾斜杠）不匹配
 *     （Go 1.22+ ServeMux 会 301 到带尾斜杠的版本，**但客户端必须自己拼对**）。
 * - **分发**：`admin/report_rollup.go:94-118` 的 `strings.HasSuffix` switch
 *   ⇒ `summary` / `export` / `dimensions` / `run`（POST，**不碰**）/ 404。
 * - **实现**：`admin/report_rollup.go`；类型在 `domains/reportrollup/`。
 * - ★ `export` 是 **xlsx 二进制**（`Content-Type: …spreadsheetml.sheet`）⇒
 *   **不是 JSON** ⇒ 本模块**不提供** export 的解包器。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十五件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **「降级」是 200 + 一个两键信封，**不是错误** —— 主键整个消失。**
 *      `:317-319`（summary）与 `:354-356`（dimensions）走同一条：
 *      ```go
 *      if reportDegraded(err) {
 *          writeJSON(w, http.StatusOK, map[string]any{"degraded": true, "error_code": "REPORT_SNAPSHOTS_NOT_MIGRATED"})
 *          return
 *      }
 *      ```
 *      `reportDegraded`（`:291-293`）= **错误串里含 `report_snapshots`**（小写后 `Contains`）
 *      ⇒ 是**字符串匹配**，不是哨兵错误类型。
 *      ⇒ ★★★ 降级响应里**没有** `report` / **没有** `dimensions` ⇒
 *        **解包器必须接受「只有降级两键、没有主键」的形状**。
 *
 * (2) ★★★ 503 文案是 **`database not available`** —— 本仓**第三种**措辞。
 *      `:98-101`（`h.db == nil`）。
 *      对照：批 81/82 的 `database not configured`、批 79 的 `database **is** not configured`。
 *
 * (3) ★★★ **503 检查排在 405 检查之前** ⇒ 「非 GET **且** db 未配置」得 503。
 *      `:98` 在 `:102` 之前 ⇒ 与批 82 一致、**与批 83 相反**。
 *
 * (4) ★★★ 子路由用 **`strings.HasSuffix`** 分发，**不是路径段精确匹配**。
 *      `:107-114`。⇒ `/api/admin/report-rollup/任意/summary` 也匹配 ⇒
 *      所以「路径看起来对」不代表进了 summary 分支。
 *
 * (5) ★★★★ 缺省窗口是「**昨日往前 7 天**」，闭区间，且**不含今日**。
 *      `reportRange`（`:122-147`）：`end = 昨日`、`start = 昨日 − 6 天`。
 *      ⇒ ★★ 注释自陈原因：「聚合语义是 **T+1** 凌晨出昨日报表」⇒ **今日快照尚不存在**。
 *      ⇒ ⇒ 客户端算「今天的数据」时必须知道：**这个端点天生给不了今天**。
 *
 * (6) ★★★★ 区间只接受 `YYYY-MM-DD`，四种 400 文案。
 *      `:127` `time.ParseInLocation("2006-01-02", raw, time.UTC)`
 *      ⇒ 带时分秒、带时区偏移都会**解析失败**。
 *      四条文案（逐字）：
 *      `invalid start (want YYYY-MM-DD)` / `invalid end (want YYYY-MM-DD)`
 *      / `end before start` / `range cannot exceed 366 days`。
 *      ⇒ ★★ 上界是 `end.Sub(start) > 366*24h`（`:143-145`）⇒ **`start == end` 合法**（单日）。
 *
 * (7) ★★★★ `view` 是**四值枚举**，但**大小写与空格都不敏感**。
 *      `:157-163`：`strings.ToLower(strings.TrimSpace(q.Get("view")))`，
 *      空则缺省 `provider`，再 `view.Valid()`（`report.go:35-37`）。
 *      ⇒ ★★★ 可达值 `provider` / `internal` / `credential` / `key`
 *        ⇒ ★ **规范化之后可以严格校验取值**（响应里的 `report.view` 已是小写规范化值）。
 *
 * (8) ★★★★ 三个数字维度用 `strconv.ParseInt`，但**前面先 `TrimSpace`** ⇒ 带空格也接受。
 *      `:174-182`：`strings.TrimSpace(q.Get(f.param))` 之后才 `ParseInt(raw, 10, 64)`。
 *      ⇒ ★★★ **同仓两种参数校验风格并存的又一例**：
 *        `/storage/tables` 与 `/errors/trend` 的 `strconv.Atoi` **没有** TrimSpace
 *        （`" 20"` 报错），这里的 `ParseInt` **有**（`" 7"` 接受）。
 *      ⇒ 两种都**没有**：只有一个数字维度存在时，两个解析器的判定集不同。
 *
 * (9) ★★★★★ `detail` 的缺省在 summary 与 export 里**正好相反**。
 *      - `reportDailyDetail`（summary 用，`:194-200`）：`"1"/"true"/"yes"/"daily"` ⇒ true，**其余 false**
 *      - `reportExportDetail`（export 用，`:207-213`）：`"0"/"false"/"no"/"summary"` ⇒ false，**其余 true**
 *      ⇒ ★★★ 于是 `detail=xyz` 在 summary 下是 **false**、在 export 下是 **true**。
 *      ⇒ ⇒ 客户端**不能**用一个共享的「detail 解析」函数描述两者。
 *      ⇒ 两个都做了 `ToLower(TrimSpace(...))` ⇒ 大小写不敏感。
 *
 * (10) ★★★★ `Filter GrainFilter` 标着 `json:"-"` ⇒ **过滤条件不回显**。
 *      `grainreport.go:114`。⇒ ★★★ 客户端**无法从响应里知道服务端实际用了什么过滤**
 *      （`view` 会回显、`start`/`end` 会回显，但六个维度过滤一个都不回显）。
 *
 * (11) ★★★★★ `error_breakdown` 在**父行是裸 `null`、在按天行是键缺失**。
 *      `ProviderRow` / `CredentialRow` / `APIKeyRow`：`json:"error_breakdown"`（**无 omitempty**）
 *      `DailyModelRow` / `DailyGroupRow`：`json:"error_breakdown,omitempty"`（**有**）
 *      ⇒ ★★★ 同一个概念、同一份 SQL、**两种 nil 编码**。
 *      ⇒ ★ 而且 `map` 无 omitempty 时：nil ⇒ `null`，空 map ⇒ `{}` ⇒ **两种都可达**。
 *
 * (12) ★★★★ `Totals.CacheHitRatio` 是 `*float64` 且**无 omitempty** ⇒ **分母为 0 时是裸 `null`**。
 *      `report.go` 的 `json:"cache_hit_ratio"` + 注释「分母 0 → nil」。
 *      ⇒ ★★★ 这是「**指针 + 无 omitempty** ⇒ 裸 `null`」的一例
 *        （对照批 83 的「指针无 omitempty ⇒ 裸 null」在 map 里的形态）。
 *      ⇒ ⇒ 客户端算命中率**必须**处理 `null`，不能 `|| 0`。
 *
 * (13) ★★★ `error_count === request_count - success_count` 是**可自验的不变式**。
 *      `report.go` 的 `Totals` 注释自陈：「ErrorCount = RequestCount − SuccessCount
 *      （终态 success 之外一律计失败，**含 rate_limited**）」。
 *      ⇒ ★★★ 这个等式在**每一层**（总计 / 每个维度行 / 每个按天行）都应成立 ⇒
 *        它是本族最锐利的形状校验。
 *
 * (14) ★★★★★ `source` **不是独立字段，而是从 `coverage` 两个数组派生的**
 *      ⇒ 这是本族最锐利的**可自验不变式**。`grainreport.go:945-951`：
 *      ```go
 *      switch {
 *      case len(rep.Coverage.LegacyDates) > 0 && len(rep.Coverage.GrainDates) > 0:
 *          rep.Source = "mixed"
 *      case len(rep.Coverage.LegacyDates) > 0:
 *          rep.Source = "legacy"
 *      default:
 *          rep.Source = "grain"
 *      }
 *      ```
 *      ⇒ ★★★ `legacy_dates` 为空 ⇒ **`source` 恒为 `grain`**（`default`）；
 *        `legacy_dates` 非空且 `grain_dates` 非空 ⇒ `mixed`；前者非空且后者空 ⇒ `legacy`。
 *      ⇒ ★★ 推论一：**`source === "grain"` 不蕴含 `grain_dates` 非空** ——
 *        两个数组都空时也走 `default`。
 *      ⇒ ★★ 推论二：`source` 是 `grain | mixed | legacy` 的**封闭三值枚举**
 *        （行内注释自陈）⇒ 仍可严格校验取值。见 (14b)。
 *      ⇒ 它披露的是「本次结果里有几层是细粒度快照」：
 *        `grain` 全是新口径、`mixed` 新旧混合、`legacy` 全是旧口径。
 *
 * (14b) ★★★★ **`coverage` 的两个日期数组可以是裸 `null`**，且**都升序**。
 *      `Coverage`（`grainreport.go:100-107`）是两个 `[]string`，**无 omitempty**
 *      ⇒ nil slice ⇒ JSON `null`（不是 `[]`，也不是键缺）。
 *      `:943-944` 对两者各 `sort.Strings` ⇒ **升序**。
 *      ⇒ ★★ 与 (1)(11)(12) 一样，这是**又一种** nil 编码落在同一个响应里。
 *
 * (15a) ★★ 响应里的 `start` / `end` 是 **`time.Time`**，回显为 **RFC3339Nano**，
 *      **不是**发进去的 `YYYY-MM-DD`。
 *      `GrainReport.Start/End` 是 `time.Time`（`grainreport.go:112-113`），
 *      Go 的 `time.Time` 序列化成 RFC3339Nano（如 `2026-10-01T00:00:00Z`）
 *      ⇒ ★★★ 客户端**不能**把响应里的 `start.slice(0,10)` 当成「后端认了这个值」
 *        的凭证（虽然恰好一致），也不能拿它直接当 `YYYY-MM-DD` 传给下一跳。
 *      ⇒ ★ 而**发出去的**参数是 `YYYY-MM-DD`（`reportRange` 用
 *        `time.ParseInLocation("2006-01-02", …)` 解析）⇒ 一进一出**两种形状**。
 *
 * (15) ★★★ `DimensionOption.Name` 是 **omitempty** 且 `fill()` 回查不到就**留空**。
 *      `dimensions.go:21-26` + `report_rollup.go:367-377`：
 *      `strconv.ParseInt(list[i].Key, 10, 64)` 回查名字表，**查不到就 `continue`（留空）**。
 *      ⇒ ★★★ `Name` 键**可能缺失**（键缺，而不是空串）。
 *      ⇒ ★ 而且候选**键统一成字符串**（`Key` 是 `string`，即使 provider/credential/apikey 是数值 id）
 *        ⇒ 「Key 是数字」这件事**在 JSON 里看不出来**，只能靠 `Name` 有没有来间接猜。
 *      ⇒ ★★★★ 更要紧的是：**`fill()` 只回填了 `providers` / `credentials` / `api_keys`**
 *        （`report_rollup.go:378-380` 三行），`models` / `tenants` / `persons`
 *        **连 `names` 都没有传进去** ⇒ ★ 那三个维度的 `name` **恒缺**。
 *      ⇒ ⇒ 客户端不能把「`name` 缺失」一律解释成「回查失败」：
 *        对 models/tenants/persons 而言**永远查不到**。
 *      ⇒ ★ `dimensions` 的候选**不受当前筛选条件影响**（只看日期区间与视角），
 *        注释自陈这是为了避免筛选栏自锁。
 *
 * ★★ **本模块明确声明的校验边界**：解包器校验 **envelope 层**（降级两键、`report`/`dimensions`
 *   主键、顶层数组的形状、`Totals` 的 16 个恒在字段 + `internal_currency` 条件键）。
 *   ★ **`GrainReport` 的九种子行结构**（`DayRow` / `ProviderRow` / `CredentialRow` /
 *   `APIKeyRow` / `ModelRow` / `TenantRow` / `PersonRow` / `DailyModelRow` / `DailyGroupRow`，
 *   每种 5~20 字段）**只校验「是数组 of 对象」，不逐字段校验**。
 *   ⇒ 理由：那会引入 100+ 个字段级断言而其中绝大多数是纯数值透传；
 *     envelope 层的降级/枚举/不变式才是真正会出错的形状。
 *   ⇒ ⇒ 调用方拿到子行时**仍需自己判空**；未校验不等于「一定对」。
 */

// ── 常量（后端字面量） ───────────────────────────────────────────────────────

/** `:318` 降级时的 `error_code`。 */
export const REPORT_ROLLUP_DEGRADED_CODE = 'REPORT_SNAPSHOTS_NOT_MIGRATED'

/** `:99` 503 文案 —— 本仓第三种措辞。见 (2)。 */
export const REPORT_ROLLUP_DB_UNAVAILABLE_MESSAGE = 'database not available'

/** `report.go:27-33` 的四个视角值（已 `ToLower(TrimSpace())` 规范化）。见 (7)。 */
export const REPORT_ROLLUP_VIEWS = ['provider', 'internal', 'credential', 'key'] as const

/** `:159` 的缺省视角。 */
export const REPORT_ROLLUP_DEFAULT_VIEW = 'provider'

/** `grainreport.go` 行内注释的三个来源值。见 (14)。 */
export const REPORT_ROLLUP_SOURCES = ['grain', 'mixed', 'legacy'] as const

/** `:144` `range cannot exceed 366 days`。见 (6)。 */
export const REPORT_ROLLUP_MAX_RANGE_DAYS = 366

/** `:126-125` 缺省窗口：昨日往前 7 天（闭区间）。见 (5)。 */
export const REPORT_ROLLUP_DEFAULT_WINDOW_DAYS = 7

/** `:196` `reportDailyDetail`（summary 用）认作真的四个字面量。见 (9)。 */
export const REPORT_ROLLUP_DETAIL_ON_TOKENS = ['1', 'true', 'yes', 'daily'] as const
/** `:209` `reportExportDetail`（export 用）认作假的四个字面量 —— **与上面互补**。 */
export const REPORT_ROLLUP_EXPORT_OFF_TOKENS = ['0', 'false', 'no', 'summary'] as const

/** 降级信封的两个键。 */
export const REPORT_ROLLUP_DEGRADED_KEYS = ['degraded', 'error_code'] as const

/** `Totals` 里**恒在**的 16 个键（`cache_hit_ratio` 也在内 —— 它是裸 `null` 不是键缺）。 */
export const REPORT_TOTALS_KEYS = [
  'request_count',
  'success_count',
  'error_count',
  'error_rate',
  'input_tokens',
  'output_tokens',
  'cache_read_tokens',
  'cache_write_tokens',
  'total_tokens',
  'estimated_cost_cents',
  'currency',
  'credits_charged',
  'internal_cost_cents',
  'cache_hit_ratio',
  'latency_p50_ms',
  'latency_p95_ms',
] as const

/** `GrainReport` 里**恒在**（无 omitempty）的键。 */
export const REPORT_GRAIN_ALWAYS_KEYS = [
  'start',
  'end',
  'view',
  'totals',
  'error_breakdown',
  'days',
  'models',
  'snapshot_dates',
  'coverage',
  'source',
] as const

/** `GrainReport` 里**条件**（omitempty）的顶层键。 */
export const REPORT_GRAIN_OPTIONAL_KEYS = [
  'top_error_kind',
  'top_error_count',
  'daily_models',
  'providers',
  'credentials',
  'api_keys',
  'model_totals',
  'tenants',
  'persons',
  'daily_providers',
  'daily_credentials',
  'daily_api_keys',
  'daily_tenants',
  'daily_persons',
] as const

/** `DimensionOptions` 的六个键 —— **全部无 omitempty** ⇒ 恒在。 */
export const REPORT_DIMENSIONS_KEYS = [
  'providers',
  'credentials',
  'api_keys',
  'models',
  'tenants',
  'persons',
] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

/**
 * `Totals`（`domains/reportrollup/report.go`）。
 *
 * ★★ `cache_hit_ratio` 是 `*float64` 且**无 omitempty** ⇒ 分母为 0 时是**裸 `null`**。见 (12)。
 * ★ `internal_currency` 带 omitempty ⇒ 仅 internal 视角有值。
 */
export interface ReportTotals {
  request_count: number
  success_count: number
  /** ★ 恒等于 `request_count − success_count`。见 (13)。 */
  error_count: number
  error_rate: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  estimated_cost_cents: number
  currency: string
  credits_charged: number
  /** ★ 无 omitempty ⇒ internal 视角以外也是 0（恒在键）。 */
  internal_cost_cents: number
  /** ★ 恒在，但**分母为 0 时是 `null`**。见 (12)。 */
  cache_hit_ratio: number | null
  latency_p50_ms: number
  latency_p95_ms: number
  /** ★ 条件键（仅 internal 视角）。 */
  internal_currency?: string
}

/**
 * `GrainReport`（`domains/reportrollup/grainreport.go`）。
 *
 * ★ 九种子行结构**只校验「是数组 of 对象」**，不逐字段校验（见文件头声明）。
 * ★★ `filter` 标着 `json:"-"` ⇒ **永不在响应里**。见 (10)。
 */
export interface ReportGrainReport {
  /** ★ 回显的是 **RFC3339Nano**（`time.Time`），**不是**发进去的 `YYYY-MM-DD`。见 (15a)。 */
  start: string
  /** ★ 同上。 */
  end: string
  /** ★ 四值枚举，已规范化（`ToLower(TrimSpace())` 后写入）。见 (7)。 */
  view: string
  totals: ReportTotals
  /** ★★ 无 omitempty ⇒ nil 时是**裸 `null`**（不是键缺）。见 (11)。 */
  error_breakdown: Record<string, number> | null
  days: unknown[]
  models: unknown[]
  snapshot_dates: unknown[]
  /**
   * ★ `grain_dates` / `legacy_dates` 是 `[]string` 且**无 omitempty**
   * ⇒ nil slice 时是**裸 `null`**（不是 `[]`，也不是键缺）。见 (14b)。
   */
  coverage: { grain_dates?: string[] | null; legacy_dates?: string[] | null }
  /** ★ 封闭三值枚举，**且可从 `coverage` 两个数组重新算出来**。见 (14)。 */
  source: string

  top_error_kind?: string
  top_error_count?: number
  daily_models?: unknown[]
  providers?: unknown[]
  credentials?: unknown[]
  api_keys?: unknown[]
  model_totals?: unknown[]
  tenants?: unknown[]
  persons?: unknown[]
  daily_providers?: unknown[]
  daily_credentials?: unknown[]
  daily_api_keys?: unknown[]
  daily_tenants?: unknown[]
  daily_persons?: unknown[]
}

/** `DimensionOption`：`key` 与 `requests` 恒在，`name` **可能缺键**。见 (15)。 */
export interface ReportDimensionOption {
  /** ★ 统一是**字符串**（即使语义上是数值 id）。见 (15)。 */
  key: string
  requests: number
  /** ★ 键**可能缺失**（omitempty + 回查失败留空）。见 (15)。 */
  name?: string
}

export interface ReportDimensionOptions {
  providers: ReportDimensionOption[]
  credentials: ReportDimensionOption[]
  api_keys: ReportDimensionOption[]
  models: ReportDimensionOption[]
  tenants: ReportDimensionOption[]
  persons: ReportDimensionOption[]
}

/** 正常（未降级）的 summary 响应。 */
export interface ReportRollupSummaryResponse {
  report: ReportGrainReport
  degraded?: false
}
/** 正常（未降级）的 dimensions 响应。 */
export interface ReportRollupDimensionsResponse {
  dimensions: ReportDimensionOptions
  degraded?: false
}
/**
 * ★★★ 降级响应：**只有**这两个键，主键整个消失。见 (1)。
 */
export interface ReportRollupDegradedResponse {
  degraded: true
  error_code: string
}

export type ReportRollupSummaryBody = ReportRollupSummaryResponse | ReportRollupDegradedResponse
export type ReportRollupDimensionsBody =
  | ReportRollupDimensionsResponse
  | ReportRollupDegradedResponse

export interface ReportRollupParams {
  /** `YYYY-MM-DD`，闭区间。缺省 = 昨日往前 7 天。见 (5)(6)。 */
  start?: string
  end?: string
  /** 四值枚举，**大小写与空格不敏感**。见 (7)。 */
  view?: string
  /** 三个数字维度。**带空格也接受**（后端先 TrimSpace）。见 (8)。 */
  providerId?: number | string
  credentialId?: number | string
  apiKeyId?: number | string
  /** ★ 三个文本维度：后端同样先 `TrimSpace`，但**不做大小写折叠**（`:184-186`）。 */
  tenantId?: string
  person?: string
  model?: string
  /** summary 专用：真值是 `1`/`true`/`yes`/`daily`。见 (9)。 */
  detail?: string
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/report-rollup/summary`（**superAdmin 档**）。 */
export function fetchReportRollupSummary(
  params: ReportRollupParams = {},
  options?: RequestOptions,
): Promise<ReportRollupSummaryBody> {
  return req<unknown>(
    'GET',
    `/api/admin/report-rollup/summary${queryOf(params)}`,
    undefined,
    options,
  ).then(unwrapReportRollupSummary)
}

/** GET `/api/admin/report-rollup/dimensions`（**superAdmin 档**）。 */
export function fetchReportRollupDimensions(
  params: ReportRollupParams = {},
  options?: RequestOptions,
): Promise<ReportRollupDimensionsBody> {
  return req<unknown>(
    'GET',
    `/api/admin/report-rollup/dimensions${queryOf(params)}`,
    undefined,
    options,
  ).then(unwrapReportRollupDimensions)
}

function queryOf(p: ReportRollupParams): string {
  const qs = new URLSearchParams()
  // ★ start/end 只在**严格 YYYY-MM-DD** 时才发 —— 非法值会被后端 400 拒掉。
  if (p.start != null) qs.set('start', p.start)
  if (p.end != null) qs.set('end', p.end)
  // ★ view 大小写不敏感 ⇒ 这里直接规范化后再发（发原文后端也会归一）。
  if (p.view != null) qs.set('view', normalizeReportView(p.view))
  for (const [param, v] of [
    ['provider_id', p.providerId],
    ['credential_id', p.credentialId],
    ['api_key_id', p.apiKeyId],
  ] as const) {
    if (v != null) qs.set(param, String(v))
  }
  for (const [param, v] of [
    ['tenant_id', p.tenantId],
    ['person', p.person],
    ['model', p.model],
  ] as const) {
    if (v != null) qs.set(param, v)
  }
  if (p.detail != null) qs.set('detail', p.detail)
  const s = qs.toString()
  return s ? `?${s}` : ''
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★★ 两种形状都要接受（见 (1)）：
 * - 降级：`{degraded: true, error_code: "REPORT_SNAPSHOTS_NOT_MIGRATED"}`（**主键缺失**）
 * - 正常：`{report: {...}}`
 */
export function unwrapReportRollupSummary(resp: unknown): ReportRollupSummaryBody {
  const d = requireObject(resp, '对账汇总')
  if (isDegraded(d)) {
    requireKeys(d, REPORT_ROLLUP_DEGRADED_KEYS, '对账汇总（降级）')
    if (d['error_code'] !== REPORT_ROLLUP_DEGRADED_CODE) {
      throw new Error(
        `对账汇总 的 error_code 不是 ${REPORT_ROLLUP_DEGRADED_CODE}（实得 ${String(d['error_code'])}）`,
      )
    }
    return d as unknown as ReportRollupDegradedResponse
  }
  if (!('report' in d)) throw new Error('对账汇总 缺 1 个键（report）')
  requireGrainReport(d['report'], '对账汇总 的 report')
  return d as unknown as ReportRollupSummaryResponse
}

/** 与 summary 同款降级信封，但主键是 `dimensions`。 */
export function unwrapReportRollupDimensions(resp: unknown): ReportRollupDimensionsBody {
  const d = requireObject(resp, '对账候选')
  if (isDegraded(d)) {
    requireKeys(d, REPORT_ROLLUP_DEGRADED_KEYS, '对账候选（降级）')
    if (d['error_code'] !== REPORT_ROLLUP_DEGRADED_CODE) {
      throw new Error(
        `对账候选 的 error_code 不是 ${REPORT_ROLLUP_DEGRADED_CODE}（实得 ${String(d['error_code'])}）`,
      )
    }
    return d as unknown as ReportRollupDegradedResponse
  }
  if (!('dimensions' in d)) throw new Error('对账候选 缺 1 个键（dimensions）')
  requireDimensionOptions(d['dimensions'])
  return d as unknown as ReportRollupDimensionsResponse
}

function isDegraded(d: Record<string, unknown>): boolean {
  // ★ 保留 `=== true` 而不是 `!!d['degraded']`：后端**只**写 `true` 这个字面量
  //   （`:318` 与 `:355` 两处都是），`degraded` 的可达集合只有 `{true}` 或**键缺**
  //   ⇒ 两种写法在可达集合上可证等价。保留与后端形状对齐的写法，不为它造不可达样本。
  return d['degraded'] === true
}

function requireGrainReport(v: unknown, where: string): void {
  const d = requireObject(v, where)
  requireKeys(d, REPORT_GRAIN_ALWAYS_KEYS, where)
  // ★ 单趟：先窄化成 string，再校验封闭枚举（`view`/`source` 都是封闭域。见 (7)(14)）。
  for (const k of ['start', 'end', 'view', 'source'] as const) {
    const val = d[k]
    if (typeof val !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
    if (k === 'view' && !(REPORT_ROLLUP_VIEWS as readonly string[]).includes(val)) {
      throw new Error(`${where} 的 view 不是已知视角（${val}）`)
    }
    if (k === 'source' && !(REPORT_ROLLUP_SOURCES as readonly string[]).includes(val)) {
      throw new Error(`${where} 的 source 不是已知来源（${val}）`)
    }
  }
  requireTotals(d['totals'], `${where} 的 totals`)
  // ★ `error_breakdown` 无 omitempty ⇒ nil 时是**裸 null** ⇒ 两种都要接受。见 (11)。
  if (d['error_breakdown'] !== null && !isPlainObject(d['error_breakdown'])) {
    throw new Error(`${where} 的 error_breakdown 不是对象也不是 null`)
  }
  if (d['error_breakdown'] !== null) {
    for (const [k, val] of Object.entries(d['error_breakdown'])) {
      if (typeof val !== 'number') throw new Error(`${where} 的 error_breakdown.${k} 不是数字`)
    }
  }
  for (const k of ['days', 'models', 'snapshot_dates'] as const) {
    if (!Array.isArray(d[k])) throw new Error(`${where} 的 ${k} 不是数组`)
  }
  // ★ 条件键：存在时是数组或标量。
  for (const k of REPORT_GRAIN_OPTIONAL_KEYS) {
    if (k in d && Array.isArray(d[k]) === false && typeof d[k] !== 'number' && typeof d[k] !== 'string') {
      throw new Error(`${where} 的 ${k} 类型不对`)
    }
  }
  requireObject(d['coverage'], `${where} 的 coverage`)
}

function requireTotals(v: unknown, where: string): void {
  const d = requireObject(v, where)
  requireKeys(d, REPORT_TOTALS_KEYS, where)
  for (const k of [
    'request_count',
    'success_count',
    'error_count',
    'input_tokens',
    'output_tokens',
    'cache_read_tokens',
    'cache_write_tokens',
    'total_tokens',
    'estimated_cost_cents',
    'credits_charged',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['error_rate', 'internal_cost_cents', 'latency_p50_ms', 'latency_p95_ms'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  if (typeof d['currency'] !== 'string') throw new Error(`${where} 的 currency 不是字符串`)
  // ★★ `cache_hit_ratio` 是指针且**无 omitempty** ⇒ 分母 0 时是**裸 null**。见 (12)。
  if (d['cache_hit_ratio'] !== null && typeof d['cache_hit_ratio'] !== 'number') {
    throw new Error(`${where} 的 cache_hit_ratio 不是数字也不是 null`)
  }
  if ('internal_currency' in d && typeof d['internal_currency'] !== 'string') {
    throw new Error(`${where} 的 internal_currency 不是字符串`)
  }
}

function requireDimensionOptions(v: unknown): void {
  const d = requireObject(v, '对账候选 的 dimensions')
  requireKeys(d, REPORT_DIMENSIONS_KEYS, '对账候选 的 dimensions')
  for (const k of REPORT_DIMENSIONS_KEYS) {
    const arr = requireArray(d[k], `dimensions 的 ${k}`)
    for (let i = 0; i < arr.length; i++) {
      const o = requireObject(arr[i], `dimensions 的 ${k}[${i}]`)
      requireKeys(o, ['key', 'requests'], `dimensions 的 ${k}[${i}]`)
      if (typeof o['key'] !== 'string') throw new Error(`dimensions 的 ${k}[${i}] 的 key 不是字符串`)
      if (typeof o['requests'] !== 'number') throw new Error(`dimensions 的 ${k}[${i}] 的 requests 不是数字`)
      if ('name' in o && typeof o['name'] !== 'string') {
        throw new Error(`dimensions 的 ${k}[${i}] 的 name 不是字符串`)
      }
    }
  }
}

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

function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (1) 降级 ──

/** 见 (1)：这次响应是不是「表没迁移」的降级信封。 */
export function reportRollupIsDegraded(body: ReportRollupSummaryBody | ReportRollupDimensionsBody): boolean {
  return (body as ReportRollupDegradedResponse).degraded === true
}

/** 见 (1)：降级时主键整个消失 ⇒ 用它区分「有数据」与「没迁移」。 */
export function reportRollupHasData(
  body: ReportRollupSummaryBody | ReportRollupDimensionsBody,
): boolean {
  return !reportRollupIsDegraded(body)
}

/** 见 (1)：降级时的 `error_code`（非降级时是 `undefined`）。 */
export function reportRollupDegradedCode(
  body: ReportRollupSummaryBody | ReportRollupDimensionsBody,
): string | undefined {
  return reportRollupIsDegraded(body)
    ? (body as ReportRollupDegradedResponse).error_code
    : undefined
}

// ── (5)(6) 区间 ──

/**
 * 后端缺省窗口是「昨日往前 7 天」⇒ **天然不含今日**。见 (5)。
 *
 * ★★ 这里**只有两个合取项**，不是三个：`start < today` 是**可证冗余**的 ——
 * 由 `start <= yesterday` 与 `yesterday < today` 已经蕴含
 * （字符串日期的传递性）⇒ 加上它不改变任何一条分支的结果。
 * ⇒ 保留两项，冗余项**删掉**而不是留着当恒真项。
 * ⇒ 而这两项**互不冗余**：
 *   - 缺 `start <= yesterday` ⇒ 起点越过终点仍会判「不含今日」
 *   - 缺 `yesterday < today` ⇒ 把「今天」当昨天也能判过
 */
export function reportRollupDefaultWindowExcludesToday(today: Date, yesterday: string, start: string): boolean {
  const todayStr = today.toISOString().slice(0, 10)
  return start <= yesterday && yesterday < todayStr
}

/** 见 (6)：区间是否超上限（`end − start > 366 天` ⇒ 400）。 */
export function reportRollupRangeIsTooLong(days: number): boolean {
  return days > REPORT_ROLLUP_MAX_RANGE_DAYS
}

/** 见 (6)：`start == end` 是**合法的**单日区间（差值 0，不超上限）。 */
export function reportRollupSingleDayIsValid(): boolean {
  return !reportRollupRangeIsTooLong(0)
}

// ── (7) view ──

/** 后端的规范化：`ToLower(TrimSpace(x))`，空则缺省 `provider`。见 (7)。 */
export function normalizeReportView(raw: string | null | undefined): string {
  const v = String(raw ?? '')
    .trim()
    .toLowerCase()
  return v === '' ? REPORT_ROLLUP_DEFAULT_VIEW : v
}

/** 见 (7)：规范化后是否是四个已知视角之一。 */
export function reportRollupViewIsKnown(view: string): boolean {
  return (REPORT_ROLLUP_VIEWS as readonly string[]).includes(view)
}

// ── (10) filter 不回显 ──

/** 见 (10)：响应里**永远没有** `filter` 键（后端标了 `json:"-"`）。 */
export function reportRollupFilterIsNeverEchoed(body: ReportRollupSummaryBody): boolean {
  const r = (body as ReportRollupSummaryResponse).report
  return r !== undefined && !('filter' in (r as unknown as Record<string, unknown>))
}

// ── (12)(13) Totals ──

/** 见 (13)：`error_count === request_count − success_count`（注释自陈的语义）。 */
export function reportTotalsErrorCountMatches(t: ReportTotals): boolean {
  return t.error_count === t.request_count - t.success_count
}

/** 见 (12)：命中率分母为 0 ⇒ `cache_hit_ratio` 是**裸 `null`**。 */
export function reportTotalsCacheHitRatioIsNull(t: ReportTotals): boolean {
  return t.cache_hit_ratio === null
}

/** 见 (12)：「有缓存」与「命中率是 0」是两回事（`0` 不是 `null`）。 */
export function reportTotalsCacheHitRatioIsZero(t: ReportTotals): boolean {
  return t.cache_hit_ratio === 0
}

// ── (11) error_breakdown 的两种编码 ──

/** 见 (11)：父行（providers/credentials/api_keys）的 `error_breakdown` 恒在，nil 时是 `null`。 */
export function reportBreakdownIsNullOnParentRow(row: unknown): boolean {
  const d = row as Record<string, unknown>
  return d !== null && typeof d === 'object' && d['error_breakdown'] === null
}

/** 见 (11)：按天行（daily_*）的 `error_breakdown` 带 omitempty ⇒ nil 时是**键缺**。 */
export function reportBreakdownIsAbsentOnDailyRow(row: unknown): boolean {
  const d = row as Record<string, unknown>
  return d !== null && typeof d === 'object' && !('error_breakdown' in d)
}

// ── (14)(14b) source ⇔ coverage（★ 本族最锐利的可自验不变式） ──

/** 见 (14)：三个来源值是可校验的封闭枚举。 */
export function reportRollupSourceIsKnown(r: ReportGrainReport): boolean {
  return (REPORT_ROLLUP_SOURCES as readonly string[]).includes(r.source)
}

/** 见 (14)：`legacy` 意味着区间内只有旧口径快照 ⇒ 维度分解不可用。 */
export function reportRollupIsLegacyOnly(r: ReportGrainReport): boolean {
  return r.source === 'legacy'
}

/** 见 (14)：`mixed` 意味着区间内新旧两种口径都有。 */
export function reportRollupIsMixed(r: ReportGrainReport): boolean {
  return r.source === 'mixed'
}

/**
 * ★★★★★ 见 (14)：**按后端那个 `switch` 原样重算一遍 `source`**。
 *
 * ```go
 * case legacy>0 && grain>0: "mixed"
 * case legacy>0:            "legacy"
 * default:                  "grain"
 * ```
 *
 * ★★ 注意 `default` 分支：**两个数组都空时也落 `grain`** ⇒
 * `source === "grain"` **不蕴含** `grain_dates` 非空。
 * ★ 且 `legacy_dates` 可能是**裸 `null`**（见 (14b)）⇒ 长度按 0 算。
 */
export function reportCoverageDeriveSource(r: ReportGrainReport): string {
  const legacy = (r.coverage.legacy_dates ?? []).length
  const grain = (r.coverage.grain_dates ?? []).length
  if (legacy > 0 && grain > 0) return 'mixed'
  if (legacy > 0) return 'legacy'
  return 'grain'
}

/**
 * ★★★★★ 见 (14)：`source` 与 `coverage` 一致（后端就是从 coverage 派生的）。
 *
 * ⇒ 这是**自洽性校验**，不是「回显」：`source` 与 `coverage` 由同一段代码算出，
 *   不一致就说明客户端拆解错了形状或响应被截断。
 */
export function reportRollupSourceMatchesCoverage(r: ReportGrainReport): boolean {
  return reportCoverageDeriveSource(r) === r.source
}

/**
 * ★★★★ 见 (14)：`source === "legacy"` **要求** `legacy_dates` 非空。
 *
 * ⇒ 与 `reportRollupIsLegacyOnly` 合起来可推出「`legacy` ⇒ 维度分解不可用」是有依据的。
 */
export function reportLegacyOnlyHasLegacyDates(r: ReportGrainReport): boolean {
  return (r.coverage.legacy_dates ?? []).length > 0
}

/**
 * ★★★ 见 (14b)：`grain_dates` 可能是**裸 `null`**（nil slice，非 omitempty）。
 *
 * ⇒ ★ 与 (11) 的 `error_breakdown` 同族，但**字段不同** ⇒ 判据必须分开写。
 */
export function reportCoverageGrainDatesIsNull(r: ReportGrainReport): boolean {
  return r.coverage.grain_dates === null
}

/** ★★★ 见 (14b)：`legacy_dates` 同样可以是裸 `null`。 */
export function reportCoverageLegacyDatesIsNull(r: ReportGrainReport): boolean {
  return r.coverage.legacy_dates === null
}

/**
 * ★★★ 见 (14b)：两个日期数组都经过 `sort.Strings` ⇒ **升序**（非降序）。
 *
 * ⇒ 判据用「非降序」而非「严格递增」：后端没去重，同值可重复。
 */
export function reportCoverageDatesAreAscending(r: ReportGrainReport): boolean {
  return isNonDecreasing(r.coverage.grain_dates) && isNonDecreasing(r.coverage.legacy_dates)
}

/** `null` 与空数组都算「单调」（长度 ≤ 1 时恒真）。 */
function isNonDecreasing(list: string[] | null | undefined): boolean {
  if (!Array.isArray(list)) return true
  for (let i = 1; i < list.length; i++) {
    const prev = list[i - 1]
    const cur = list[i]
    if (prev === undefined || cur === undefined) return false
    if (prev > cur) return false
  }
  return true
}

// ── (15a) 回显是 RFC3339Nano，不是 YYYY-MM-DD ──

/**
 * ★★★ 见 (15a)：响应里的 `start` / `end` 是 `time.Time` 的 **RFC3339Nano** 串。
 *
 * ⇒ 判据：**含 `T` 且以 `Z` 或 ±HH:MM 结尾**，而不是 10 位的 `YYYY-MM-DD`。
 * ⇒ 客户端要拿它跟别的端点比较日期时，必须自己截断。
 */
export function reportStartIsRfc3339NotDateOnly(raw: string): boolean {
  return raw.includes('T') && raw.length > 10
}

/** ★★★ 见 (15a)：反面对照 —— 10 位 `YYYY-MM-DD`（**回显里不会出现**）。 */
export function reportStartIsDateOnly(raw: string): boolean {
  return !reportStartIsRfc3339NotDateOnly(raw)
}

// ── (9) detail 的缺省相反 ──

/** 见 (9)：summary 侧的真值判定（其余一律 false）。 */
export function reportDetailIsOn(raw: string | null | undefined): boolean {
  const v = String(raw ?? '')
    .trim()
    .toLowerCase()
  return (REPORT_ROLLUP_DETAIL_ON_TOKENS as readonly string[]).includes(v)
}

/** 见 (9)：export 侧的判定 —— **缺省相反**，`xyz` 在这里是 true。 */
export function reportExportDetailIsOff(raw: string | null | undefined): boolean {
  const v = String(raw ?? '')
    .trim()
    .toLowerCase()
  return (REPORT_ROLLUP_EXPORT_OFF_TOKENS as readonly string[]).includes(v)
}

// ── (15) 候选 ──

/** 见 (15)：`name` 是 omitempty 条件键 —— 键**可能缺失**。 */
export function reportDimensionHasName(o: ReportDimensionOption): boolean {
  return 'name' in o
}

/** 见 (15)：候选 `key` 统一是字符串，即使语义上是数值 id。 */
export function reportDimensionKeyLooksNumeric(o: ReportDimensionOption): boolean {
  return /^-?\d+$/.test(o.key)
}