import { req, type RequestOptions } from './client'

/**
 * selfCheck.ts — 系统自检（2026-10-07，第七十六批）。
 *
 * 六个 GET 端点，注册在 `admin/self_check_handlers.go:60-68` 的 `RegisterRoutes`：
 *
 * | 端点 | 档位 | 行号 |
 * |---|---|---|
 * | `GET /api/self-check/runs` | `admin(...)` | `:61` |
 * | `GET /api/self-check/runs/{id}` | `admin(...)` | `:62` |
 * | `GET /api/self-check/settings` | `admin(...)` | `:63` |
 * | `GET /api/self-check/trigger/availability` | `admin(...)` | `:65` |
 * | `GET /api/self-check/stats` | `admin(...)` | `:67` |
 * | `GET /api/self-check/models` | `admin(...)` | `:68` |
 *
 * 全部 **admin 档** ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
 * （同族的 `settings/update`（`:64`）与 `trigger`（`:66`）是 `superAdmin(...)`，写操作，本模块不碰。）
 *
 * ## ★★★★★ 本族最要紧的九件事
 *
 * (1) ★★★★★ **`self_check_runs` 有 `tenant_id` 列，但六个 handler 一个都不用。**
 *      建表：`deploy/sql/schemas/baseline/01-schema.sql`
 *      ```sql
 *      tenant_id text DEFAULT 'default'::text NOT NULL,
 *      ```
 *      而 `handleListRuns` 是 `WHERE 1=1`、`handleGetRun` 是 `WHERE id=$1`、
 *      `handleStats` 三处是 `WHERE started_at >= $1`、`handleModels` 是 `GROUP BY model_name`
 *      ⇒ **全部不带租户条件**。
 *      注册是 `admin(...)` ⇒ **tenant_admin 能读所有租户的自检记录**
 *      （含 `error_detail`、`request_body`、`response_preview` 等正文级内容）。
 *      ⇒ 本仓**第四次**「不隔离 + admin 档」；与前三次不同的是
 *        **列就在表里，只是查询从不引用它** —— 不是「表没有租户概念」。
 *
 * (2) ★★★★★ **`status` 是五值枚举，统计却只数三个。**
 *      建表 CHECK：`('running','success','partial','failed','retrying')`。
 *      而 `handleStats` 的 summary 与 by_model 只 FILTER 三个：
 *      ```sql
 *      COUNT(*) FILTER (WHERE status='success'),
 *      COUNT(*) FILTER (WHERE status='partial'),
 *      COUNT(*) FILTER (WHERE status='failed')
 *      ```
 *      ⇒ ★ `total_runs` 走 `COUNT(*)`，**含 running 与 retrying**
 *        ⇒ `success_runs + partial_runs + failed_runs` **可能小于** `total_runs`
 *        ⇒ 拿三项相加当分母会算错。
 *      ⇒ 桌面 `api-selfcheck.ts:55` 的 `status` 类型只声明了四个值，**漏了 `retrying`**。
 *
 * (3) ★★★★★ **`range` 回显的是请求值，不是生效窗口。**
 *      `:777-794` 用 `rangeParam` 原值算 `since`，未知值静默落 24h；
 *      `:963` 又把**同一个原值**回显：`"range": rangeParam`。
 *      ⇒ `range=xyz` 得到的是 **24h 的数据、标着 `xyz` 的 range**。
 *      ⇒ 客户端**不能**用回显值推断实际窗口。
 *
 * (4) ★★★★★ **`stats` 四个区块有四种失败策略，其中两种会骗人。**
 *
 *      | 区块 | 查询失败时 | 客户端看到的 |
 *      |---|---|---|
 *      | `summary` | **错误被丢弃**（`:805` 的 `Scan` 返回值没接） | 零值 + `success_rate: 0.0`，**HTTP 200** |
 *      | `by_model` | 500（`:826-829`） | 报错 |
 *      | `error_breakdown` | 只 `slog.Warn`（`:872-874`） | `[]` —— **与「没有失败记录」同形** |
 *      | `trend` | 只 `slog.Warn`（`:904-906`） | `[]` —— **与「该窗口没跑过」同形** |
 *      | `probe_system` | 两个查询的错都 `_ =` 丢弃（`:941`/`:949`） | 全零 ⇒ **`healthy` 算成 `true`** |
 *
 *      ⇒ ★★★ 最严重的是最后一行。这个区块的注释（`:926-931`）自陈存在的理由就是
 *        「页面绿灯但探测管线已死」（glm-5.2 事故），而**它的查询失败恰好产出绿灯**：
 *        `queue_ready_unclaimable == 0` 且 `last_activity_at == nil` ⇒ `healthy = true`。
 *      ⇒ 与第六十八批 `compressed_requests === 0`、第七十五批 `checks_last_10m === 0`
 *        同族的二义，但这次**直接落在健康判据上**。
 *
 * (5) ★★★★ **`credential_id` 不是数据库列，是从 `model_name` 推导的。**
 *      `credentialIDFromSelfCheckLabel`（`:75-85`）：
 *      ```go
 *      const prefix = "cred-"
 *      if !strings.HasPrefix(modelName, prefix) { return nil }
 *      id, err := strconv.ParseInt(modelName[len(prefix):], 10, 64)
 *      if err != nil || id <= 0 { return nil }
 *      ```
 *      ⇒ 只有 `cred-<正整数>` 才产出 `credential_id`，**且 `omitempty` ⇒ 键缺失**。
 *      ⇒ 客户端**可以自己验算**，不需要信后端。
 *
 * (6) ★★★★ **`omitempty` 打在 `int` 上 ⇒ 0 毫秒是「键缺失」不是 `0`。**
 *      `UpstreamLatencyMs int \`json:"upstream_latency_ms,omitempty"\``（`:106`）。
 *      同族还有 `error_type`/`error_detail`/`upstream_result`/`upstream_error`/
 *      `selection_strategy`：SQL 里 `COALESCE(...,'')` 成空串，**再被 omitempty 吃掉**
 *      ⇒ 「键在」等价于「非空」。
 *
 * (7) ★★★★ **run 详情把「数据库挂了」说成「记录不存在」。**
 *      `:230-233`：`QueryRow(...).Scan(&err)` 的**任何**错误都走 404 `run not found`
 *      ⇒ ★ 客户端无法区分 404 的两种成因。
 *
 * (8) ★★★ **`settings` 这个 GET 有写副作用。**
 *      `:327-345`：读不到行就 `INSERT ... ON CONFLICT (id) DO NOTHING` 播种默认值再重查一次。
 *      ⇒ 它不是纯只读端点，缓存/预取会真的落库。
 *      播种常量（`:290`）见 `SELF_CHECK_DEFAULT_FEATURED_MODELS`。
 *
 * (9) ★★★ **`GET /api/self-check/models` 没有 `partial` 计数，而 stats 的 by_model 有。**
 *      ⇒ 两个端点的 `total` 口径相同，但 `success + failed` 在 models 里**不等于** `total`。
 *      ⇒ 且 models 是**全时段**（无 range），stats 按窗口。
 */

export const SELF_CHECK_LABEL_PREFIX = 'cred-'

/** admin/self_check_handlers.go:119 `limit := 50`（非法值回落到的默认值）。 */
export const SELF_CHECK_RUNS_DEFAULT_LIMIT = 50
/** :121 `n <= 500`。 */
export const SELF_CHECK_RUNS_MAX_LIMIT = 500

/** stats 的四个窗口（`:783-794`）；未知值静默落 24h。 */
export const SELF_CHECK_STATS_RANGES = ['1h', '6h', '24h', '7d'] as const
export type SelfCheckStatsRange = (typeof SELF_CHECK_STATS_RANGES)[number]
/** :778-779 与 :792-793 的缺省。 */
export const SELF_CHECK_STATS_DEFAULT_RANGE = '24h'

/** :762-768 `LLM_GATEWAY_USE_NEW_PROBE_MODE` 未设置时的缺省。 */
export const SELF_CHECK_NEW_PROBE_MODE_DEFAULT = true
/** :484 / :486。 */
export const SELF_CHECK_TRIGGER_MODE_PROBE_QUEUE = 'probe_queue'
export const SELF_CHECK_TRIGGER_MODE_LEGACY_WORKER = 'legacy_worker'
/** :493 / :496。 */
export const SELF_CHECK_TRIGGER_ERR_NO_PROBE_PATH = 'self_check.trigger.no_probe_path'
export const SELF_CHECK_TRIGGER_ERR_WORKER_UNAVAILABLE = 'self_check.trigger.worker_unavailable'

/** :959-960 判 stale 的阈值。 */
export const SELF_CHECK_PROBE_STALE_MS = 15 * 60 * 1000

/** admin/self_check_handlers.go:290 `defaultFeaturedModelsSC`（逐字）。 */
export const SELF_CHECK_DEFAULT_FEATURED_MODELS = [
  'minimax-m2.7',
  'glm-5.2',
  'mimo-v2.5',
  'claude-sonnet-5',
  'gpt-5.4',
  'gpt-5.6-luna',
  'deepseek-v4-pro',
]

/**
 * ★ 建表 CHECK `self_check_runs_status_check` 的五值（01-schema.sql）。
 * 桌面 `api-selfcheck.ts:55` 只声明了四个，**漏 `retrying`**。
 */
export const SELF_CHECK_RUN_STATUSES = [
  'running',
  'success',
  'partial',
  'failed',
  'retrying',
] as const
export type SelfCheckRunStatus = (typeof SELF_CHECK_RUN_STATUSES)[number]

/**
 * ★ 建表 CHECK `self_check_runs_error_type_check` 的封闭枚举
 *   （01-schema.sql，逐字；另有 `http_%` 模式不在表内）。
 * ⇒ 与第七十四批 `action` 的**开放字符串**不同，这里是 DB 约束 ⇒ **真的封闭**。
 */
export const SELF_CHECK_ERROR_TYPES = [
  'none', 'timeout', 'network', 'transient', 'rate_limit', 'auth', 'auth_revoked',
  'quota', 'quota_periodic', 'quota_balance', 'quota_permanent', 'upstream_down',
  'upstream_overloaded', 'concurrent', 'stream_timeout', 'model_not_found',
  'model_deprecated', 'unsupported_feature', 'context_length_exceeded',
  'content_filter', 'tool_call_id_mismatch', 'empty_response', 'conversion_error',
  'upstream_context_loss', 'no_available_channel', 'canceled', 'client_bug',
  'parse_error', 'internal', 'unattributed', 'upstream_fail',
] as const

/** ★ 建表 CHECK `self_check_runs_selection_strategy_check` 的七值。 */
export const SELF_CHECK_SELECTION_STRATEGIES = [
  'most_used', 'random', 'featured', 'recent', 'common_7d', 'failed_model',
  'no_eligible_model',
] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 键集
 * ═══════════════════════════════════════════════════════════════════════════ */

/** scRun（`:89-110`）里**不带 omitempty** 的键 ⇒ 恒在。 */
export const SELF_CHECK_RUN_ALWAYS_KEYS = [
  'id', 'model_name', 'started_at', 'duration_ms', 'status', 'rounds_total',
  'rounds_success', 'had_tool_call', 'total_tokens', 'avg_latency_ms',
  'upstream_tested',
] as const
/** scRun 里带 `omitempty` 的键 ⇒ 恒在九个之外。 */
export const SELF_CHECK_RUN_OPTIONAL_KEYS = [
  'credential_id', 'completed_at', 'error_type', 'error_detail',
  'upstream_result', 'upstream_latency_ms', 'upstream_error',
  'selection_strategy', 'attempted_models',
] as const
/** ★ 九个条件键里**只有这两个是数字**（其余是字符串，`attempted_models` 是数组）。 */
export const SELF_CHECK_RUN_NUMERIC_OPTIONAL_KEYS = [
  'credential_id', 'upstream_latency_ms',
] as const
/** ★ 其余六个字符串键。写宽成「字符串或数字」会放过 `error_type: 500`。 */
export const SELF_CHECK_RUN_STRING_OPTIONAL_KEYS = [
  'completed_at', 'error_type', 'error_detail', 'upstream_result',
  'upstream_error', 'selection_strategy',
] as const

/** round（`:251-266`）**没有一个 omitempty** ⇒ 14 键恒在。 */
export const SELF_CHECK_ROUND_KEYS = [
  'id', 'round_index', 'is_ping', 'is_tool_call', 'latency_ms', 'prompt_tokens',
  'completion_tokens', 'total_tokens', 'success', 'http_code', 'error_message',
  'request_body', 'response_preview', 'created_at',
] as const

/** settings 裸对象（`:308-318`）的恒在键。 */
export const SELF_CHECK_SETTINGS_ALWAYS_KEYS = [
  'enabled', 'normal_interval_seconds', 'fault_interval_seconds', 'model_source',
  'max_models', 'max_tokens_per_run', 'featured_model_ids', 'updated_at',
] as const
/** `UpdatedBy *string` + omitempty。 */
export const SELF_CHECK_SETTINGS_OPTIONAL_KEYS = ['updated_by'] as const

/** trigger/availability 恒在的两个键。 */
export const SELF_CHECK_AVAILABILITY_ALWAYS_KEYS = ['available', 'new_probe_mode'] as const
/** `mode` 一个 + `reason`/`error_code` 一对 ⇒ 三个条件键。 */
export const SELF_CHECK_AVAILABILITY_OPTIONAL_KEYS = ['mode', 'reason', 'error_code'] as const

/** stats 顶层六键（`:962-969`）。 */
export const SELF_CHECK_STATS_KEYS = [
  'range', 'summary', 'by_model', 'error_breakdown', 'trend', 'probe_system',
] as const
export const SELF_CHECK_SUMMARY_KEYS = [
  'total_runs', 'success_runs', 'partial_runs', 'failed_runs', 'success_rate',
] as const
export const SELF_CHECK_MODEL_STAT_KEYS = [
  'model_name', 'total', 'success', 'partial', 'failed', 'success_rate', 'avg_latency_ms',
] as const
export const SELF_CHECK_TREND_KEYS = ['timestamp', 'success_rate', 'total'] as const
/** ★ `probe_system` 八键，**其中两个可为 null**（map 里塞 `*time.Time`）。 */
export const SELF_CHECK_PROBE_SYSTEM_KEYS = [
  'queue_ready', 'queue_ready_unclaimable', 'queue_running', 'due_states',
  'queue_last_activity_at', 'last_probe_attempt_at', 'executing', 'healthy',
] as const
/** modelInfo（`:993-999`）的四恒在一条件。 */
export const SELF_CHECK_MODEL_INFO_KEYS = ['model_name', 'total', 'success', 'failed'] as const
export const SELF_CHECK_MODEL_INFO_OPTIONAL_KEYS = ['last_run'] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

export interface ScRun {
  id: number
  model_name: string
  started_at: string
  duration_ms: number
  status: SelfCheckRunStatus
  rounds_total: number
  rounds_success: number
  had_tool_call: boolean
  total_tokens: number
  avg_latency_ms: number
  upstream_tested: boolean
  /** ★ 推导值（文件头第 (5) 条），非 DB 列。 */
  credential_id?: number
  /** ★ 条件键。 */
  completed_at?: string
  error_type?: string
  error_detail?: string
  upstream_result?: string
  /** ★ 条件键：**0 毫秒 ⇒ 键缺失**（文件头第 (6) 条）。 */
  upstream_latency_ms?: number
  upstream_error?: string
  selection_strategy?: string
  attempted_models?: unknown[]
}

export interface ScRound {
  id: number
  round_index: number
  is_ping: boolean
  is_tool_call: boolean
  latency_ms: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  success: boolean
  http_code: number
  /** ★ COALESCE('') ⇒ 恒为字符串，可能为空串。 */
  error_message: string
  request_body: string
  response_preview: string
  created_at: string
}

export interface ScRunListResponse {
  /** ★ 恒数组（`:161` 的 `make(...,0)` + 「frontend reads .length」注释）。 */
  items: ScRun[]
  total: number
}

export interface ScRunDetailResponse {
  run: ScRun
  /** ★ 恒数组（`:267`）。 */
  rounds: ScRound[]
}

/** ★ 裸对象，没有信封（`:353` 直接 `writeJSON(w, 200, s)`）。 */
export interface ScSettings {
  enabled: boolean
  normal_interval_seconds: number
  fault_interval_seconds: number
  model_source: string
  max_models: number
  max_tokens_per_run: number
  featured_model_ids: string[]
  updated_at: string
  updated_by?: string
}

export interface ScTriggerAvailability {
  available: boolean
  new_probe_mode: boolean
  mode?: string
  reason?: string
  error_code?: string
}

export interface ScStatsSummary {
  total_runs: number
  success_runs: number
  partial_runs: number
  failed_runs: number
  /** ★ 0-1 比例；`total_runs === 0` 时留 **0.0** ⇒ 与「全失败」同值。 */
  success_rate: number
}

export interface ScModelStat {
  model_name: string
  total: number
  success: number
  partial: number
  failed: number
  success_rate: number
  /** ★ `COALESCE(AVG(...),0)::int` ⇒ **截断**，不是四舍五入。 */
  avg_latency_ms: number
}

export interface ScErrorStat {
  error_type: string
  count: number
}

export interface ScTrendPoint {
  timestamp: string
  success_rate: number
  total: number
}

/** ★ 失败时也是这个形状（全零 + `healthy: true`），见文件头第 (4) 条。 */
export interface ScProbeSystem {
  queue_ready: number
  queue_ready_unclaimable: number
  queue_running: number
  due_states: number
  /** ★ 恒在键但**可为 null**（map 里塞 `*time.Time`）。 */
  queue_last_activity_at: string | null
  last_probe_attempt_at: string | null
  executing: boolean
  healthy: boolean
}

export interface ScStatsResponse {
  /** ★ **请求原值**，不是生效窗口（文件头第 (3) 条）。 */
  range: string
  summary: ScStatsSummary
  by_model: ScModelStat[]
  /** ★ 查询失败时也是 `[]` ⇒ 与「没有失败记录」同形。 */
  error_breakdown: ScErrorStat[]
  /** ★ 同上。 */
  trend: ScTrendPoint[]
  probe_system: ScProbeSystem
}

export interface ScModelInfo {
  model_name: string
  total: number
  success: number
  /** ★ **没有 partial**（文件头第 (9) 条）。 */
  failed: number
  /** ★ 条件键（`*time.Time` + omitempty）。 */
  last_run?: string
}

export interface ScModelsResponse {
  models: ScModelInfo[]
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

export function fetchSelfCheckRuns(
  params?: { model?: string; status?: string; limit?: number },
  options?: RequestOptions,
): Promise<ScRunListResponse> {
  const qs = new URLSearchParams()
  if (params?.model) qs.set('model', params.model)
  if (params?.status) qs.set('status', params.status)
  if (params?.limit != null) qs.set('limit', String(params.limit))
  const q = qs.toString()
  return req<unknown>(
    'GET',
    `/api/self-check/runs${q ? `?${q}` : ''}`,
    undefined,
    options,
  ).then(unwrapSelfCheckRuns)
}

export function fetchSelfCheckRunDetail(
  id: number | string,
  options?: RequestOptions,
): Promise<ScRunDetailResponse> {
  // ★ id 走原样字符串：后端对 `abc` 返 400 `invalid run id`，
  //   对 `0`/负数则**查得到就 200、查不到就 404**（不报非法）。
  return req<unknown>(
    'GET',
    `/api/self-check/runs/${encodeURIComponent(String(id))}`,
    undefined,
    options,
  ).then(unwrapSelfCheckRunDetail)
}

export function fetchSelfCheckSettings(options?: RequestOptions): Promise<ScSettings> {
  // ★ **裸对象**：这一条没有信封。
  return req<unknown>('GET', '/api/self-check/settings', undefined, options).then(
    unwrapSelfCheckSettings,
  )
}

export function fetchSelfCheckTriggerAvailability(
  options?: RequestOptions,
): Promise<ScTriggerAvailability> {
  return req<unknown>(
    'GET',
    '/api/self-check/trigger/availability',
    undefined,
    options,
  ).then(unwrapSelfCheckTriggerAvailability)
}

export function fetchSelfCheckStats(
  range?: string,
  options?: RequestOptions,
): Promise<ScStatsResponse> {
  const qs = new URLSearchParams()
  if (range != null) qs.set('range', range)
  const q = qs.toString()
  return req<unknown>(
    'GET',
    `/api/self-check/stats${q ? `?${q}` : ''}`,
    undefined,
    options,
  ).then(unwrapSelfCheckStats)
}

export function fetchSelfCheckModels(options?: RequestOptions): Promise<ScModelsResponse> {
  return req<unknown>('GET', '/api/self-check/models', undefined, options).then(
    unwrapSelfCheckModels,
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapSelfCheckRuns(resp: unknown): ScRunListResponse {
  const d = requireObject(resp, 'self-check runs')
  requireKeys(d, ['items', 'total'], 'self-check runs')
  if (!Array.isArray(d.items)) throw new Error('self-check runs 的 items 不是数组')
  if (typeof d.total !== 'number') throw new Error('self-check runs 的 total 不是数字')
  const out: ScRun[] = d.items.map((it, i) => requireRun(it, `self-check runs.items[${i}]`))
  return { items: out, total: d.total }
}

export function unwrapSelfCheckRunDetail(resp: unknown): ScRunDetailResponse {
  const d = requireObject(resp, 'self-check run 详情')
  requireKeys(d, ['run', 'rounds'], 'self-check run 详情')
  const run = requireRun(d.run, 'self-check run 详情.run')
  if (!Array.isArray(d.rounds)) throw new Error('self-check run 详情的 rounds 不是数组')
  const rounds = d.rounds.map((r, i) => requireRound(r, `self-check run 详情.rounds[${i}]`))
  return { run, rounds }
}

export function unwrapSelfCheckSettings(resp: unknown): ScSettings {
  const d = requireObject(resp, 'self-check settings')
  requireKeys(d, SELF_CHECK_SETTINGS_ALWAYS_KEYS, 'self-check settings')
  if (typeof d.enabled !== 'boolean') throw new Error('self-check settings 的 enabled 不是布尔值')
  for (const k of [
    'normal_interval_seconds',
    'fault_interval_seconds',
    'max_models',
    'max_tokens_per_run',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`self-check settings 的 ${k} 不是数字`)
  }
  if (typeof d.model_source !== 'string') {
    throw new Error('self-check settings 的 model_source 不是字符串')
  }
  if (!Array.isArray(d.featured_model_ids)) {
    throw new Error('self-check settings 的 featured_model_ids 不是数组')
  }
  if (typeof d.updated_at !== 'string') {
    throw new Error('self-check settings 的 updated_at 不是字符串')
  }
  // `updated_by` 是 `*string` + omitempty ⇒ 键在时必为字符串，键不在时不是 null。
  if ('updated_by' in d && typeof d.updated_by !== 'string') {
    throw new Error('self-check settings 的 updated_by 不是字符串')
  }
  return d as unknown as ScSettings
}

export function unwrapSelfCheckTriggerAvailability(resp: unknown): ScTriggerAvailability {
  const d = requireObject(resp, 'self-check 触发可用性')
  requireKeys(d, SELF_CHECK_AVAILABILITY_ALWAYS_KEYS, 'self-check 触发可用性')
  if (typeof d.available !== 'boolean') {
    throw new Error('self-check 触发可用性的 available 不是布尔值')
  }
  if (typeof d.new_probe_mode !== 'boolean') {
    throw new Error('self-check 触发可用性的 new_probe_mode 不是布尔值')
  }
  // 三个条件键：有则必为字符串（后端只写字符串字面量）。
  for (const k of SELF_CHECK_AVAILABILITY_OPTIONAL_KEYS) {
    if (k in d && typeof d[k] !== 'string') {
      throw new Error(`self-check 触发可用性的 ${k} 不是字符串`)
    }
  }
  return d as unknown as ScTriggerAvailability
}

export function unwrapSelfCheckStats(resp: unknown): ScStatsResponse {
  const d = requireObject(resp, 'self-check stats')
  requireKeys(d, SELF_CHECK_STATS_KEYS, 'self-check stats')
  if (typeof d.range !== 'string') throw new Error('self-check stats 的 range 不是字符串')

  const s = requireObject(d.summary, 'self-check stats.summary')
  requireKeys(s, SELF_CHECK_SUMMARY_KEYS, 'self-check stats.summary')
  for (const k of [
    'total_runs',
    'success_runs',
    'partial_runs',
    'failed_runs',
    'success_rate',
  ] as const) {
    if (typeof s[k] !== 'number') throw new Error(`self-check stats.summary 的 ${k} 不是数字`)
  }

  if (!Array.isArray(d.by_model)) throw new Error('self-check stats 的 by_model 不是数组')
  d.by_model.forEach((m, i) => {
    const o = requireObject(m, `self-check stats.by_model[${i}]`)
    requireKeys(o, SELF_CHECK_MODEL_STAT_KEYS, `self-check stats.by_model[${i}]`)
    if (typeof o.model_name !== 'string') {
      throw new Error(`self-check stats.by_model[${i}] 的 model_name 不是字符串`)
    }
    for (const k of [
      'total', 'success', 'partial', 'failed', 'success_rate', 'avg_latency_ms',
    ] as const) {
      if (typeof o[k] !== 'number') {
        throw new Error(`self-check stats.by_model[${i}] 的 ${k} 不是数字`)
      }
    }
  })

  if (!Array.isArray(d.error_breakdown)) {
    throw new Error('self-check stats 的 error_breakdown 不是数组')
  }
  d.error_breakdown.forEach((e, i) => {
    const o = requireObject(e, `self-check stats.error_breakdown[${i}]`)
    requireKeys(o, ['error_type', 'count'], `self-check stats.error_breakdown[${i}]`)
    if (typeof o.error_type !== 'string') {
      throw new Error(`self-check stats.error_breakdown[${i}] 的 error_type 不是字符串`)
    }
    if (typeof o.count !== 'number') {
      throw new Error(`self-check stats.error_breakdown[${i}] 的 count 不是数字`)
    }
  })

  if (!Array.isArray(d.trend)) throw new Error('self-check stats 的 trend 不是数组')
  d.trend.forEach((p, i) => {
    const o = requireObject(p, `self-check stats.trend[${i}]`)
    requireKeys(o, SELF_CHECK_TREND_KEYS, `self-check stats.trend[${i}]`)
    if (typeof o.timestamp !== 'string') {
      throw new Error(`self-check stats.trend[${i}] 的 timestamp 不是字符串`)
    }
    if (typeof o.success_rate !== 'number') {
      throw new Error(`self-check stats.trend[${i}] 的 success_rate 不是数字`)
    }
    if (typeof o.total !== 'number') {
      throw new Error(`self-check stats.trend[${i}] 的 total 不是数字`)
    }
  })

  const ps = requireObject(d.probe_system, 'self-check stats.probe_system')
  requireKeys(ps, SELF_CHECK_PROBE_SYSTEM_KEYS, 'self-check stats.probe_system')
  for (const k of ['queue_ready', 'queue_ready_unclaimable', 'queue_running', 'due_states'] as const) {
    if (typeof ps[k] !== 'number') {
      throw new Error(`self-check stats.probe_system 的 ${k} 不是数字`)
    }
  }
  // ★ 这两个是 map 里塞 `*time.Time` ⇒ **恒在键**，nil 时是 `null`（不是键缺失）。
  for (const k of ['queue_last_activity_at', 'last_probe_attempt_at'] as const) {
    if (ps[k] !== null && typeof ps[k] !== 'string') {
      throw new Error(`self-check stats.probe_system 的 ${k} 不是字符串也不是 null`)
    }
  }
  for (const k of ['executing', 'healthy'] as const) {
    if (typeof ps[k] !== 'boolean') {
      throw new Error(`self-check stats.probe_system 的 ${k} 不是布尔值`)
    }
  }
  return d as unknown as ScStatsResponse
}

export function unwrapSelfCheckModels(resp: unknown): ScModelsResponse {
  const d = requireObject(resp, 'self-check models')
  requireKeys(d, ['models'], 'self-check models')
  if (!Array.isArray(d.models)) throw new Error('self-check models 的 models 不是数组')
  d.models.forEach((m, i) => {
    const o = requireObject(m, `self-check models.models[${i}]`)
    requireKeys(o, SELF_CHECK_MODEL_INFO_KEYS, `self-check models.models[${i}]`)
    if (typeof o.model_name !== 'string') {
      throw new Error(`self-check models.models[${i}] 的 model_name 不是字符串`)
    }
    for (const k of ['total', 'success', 'failed'] as const) {
      if (typeof o[k] !== 'number') {
        throw new Error(`self-check models.models[${i}] 的 ${k} 不是数字`)
      }
    }
    // ★ `*time.Time` + omitempty ⇒ 有则字符串、无则**键缺失**（不是 null）。
    if ('last_run' in o && typeof o.last_run !== 'string') {
      throw new Error(`self-check models.models[${i}] 的 last_run 不是字符串`)
    }
  })
  return d as unknown as ScModelsResponse
}

function requireRun(v: unknown, where: string): ScRun {
  const d = requireObject(v, where)
  requireKeys(d, SELF_CHECK_RUN_ALWAYS_KEYS, where)
  for (const k of ['id', 'duration_ms', 'rounds_total', 'rounds_success', 'total_tokens', 'avg_latency_ms'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['model_name', 'status', 'started_at'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  for (const k of ['had_tool_call', 'upstream_tested'] as const) {
    if (typeof d[k] !== 'boolean') throw new Error(`${where} 的 ${k} 不是布尔值`)
  }
  // ★ 九个条件键：键在时类型必须对，键不在时不是 null。
  //   ★ 数字键与字符串键必须分开 —— 写宽成「string 或 number」会让 `error_type: 500` 蒙混过关。
  for (const k of SELF_CHECK_RUN_NUMERIC_OPTIONAL_KEYS) {
    if (k in d && typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of SELF_CHECK_RUN_STRING_OPTIONAL_KEYS) {
    if (k in d && typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if ('attempted_models' in d && !Array.isArray(d.attempted_models)) {
    throw new Error(`${where} 的 attempted_models 不是数组`)
  }
  return d as unknown as ScRun
}

function requireRound(v: unknown, where: string): ScRound {
  const d = requireObject(v, where)
  requireKeys(d, SELF_CHECK_ROUND_KEYS, where)
  for (const k of [
    'id', 'round_index', 'latency_ms', 'prompt_tokens', 'completion_tokens',
    'total_tokens', 'http_code',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['error_message', 'request_body', 'response_preview', 'created_at'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  for (const k of ['is_ping', 'is_tool_call', 'success'] as const) {
    if (typeof d[k] !== 'boolean') throw new Error(`${where} 的 ${k} 不是布尔值`)
  }
  return d as unknown as ScRound
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

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * ★★ **客户端可自验**：`credential_id` 完全由 `model_name` 推导（文件头第 (5) 条）。
 * 返回 `null` 表示「后端不会给这个 run 写 `credential_id`」。
 */
export function selfCheckCredentialIdFromLabel(modelName: string): number | null {
  if (!modelName.startsWith(SELF_CHECK_LABEL_PREFIX)) return null
  const rest = modelName.slice(SELF_CHECK_LABEL_PREFIX.length)
  if (!/^\d+$/.test(rest)) return null
  const id = Number(rest)
  // ★ `id <= 0` 时后端返回 nil（`:81`）。
  return id > 0 ? id : null
}

/** ★★ 推导值与后端回写值是否一致（写操作造成的漂移在这里能被抓到）。 */
export function selfCheckCredentialIdMatches(run: ScRun): boolean {
  const derived = selfCheckCredentialIdFromLabel(run.model_name)
  if (derived === null) return !('credential_id' in run)
  return run.credential_id === derived
}

/**
 * ★★ `status` 的**五值**里，`stats` 只数三个 ⇒ 分母含 running/retrying（文件头第 (2) 条）。
 * ⇒ `success_runs + partial_runs + failed_runs` 恒 ≤ `total_runs`。
 */
export function selfCheckCountedStatusTotal(s: ScStatsSummary): number {
  return s.success_runs + s.partial_runs + s.failed_runs
}

/** ★ 三项之和小于 `total_runs` ⇒ 存在 running / retrying 的 run。 */
export function selfCheckHasInFlightRuns(s: ScStatsSummary): boolean {
  return s.total_runs > selfCheckCountedStatusTotal(s)
}

/** ★★ `total_runs === 0` ⇒ `success_rate` 恒 0.0 ⇒ 与「全失败」同值。 */
export function selfCheckSuccessRateIsMeaningless(s: ScStatsSummary): boolean {
  return s.total_runs === 0
}

/**
 * ★★★ `stats` 的四个区块有四种失败策略，`error_breakdown` / `trend` 的
 * **空数组二义**就在这里（文件头第 (4) 条）。
 * @param sectionAbsent 是否已从服务端日志/运维侧得知该区块查询失败。
 *   客户端拿不到这个信息 ⇒ 传 false 时它只能说「看起来是空的」。
 */
export function selfCheckSectionLooksEmpty(
  section: ScErrorStat[] | ScTrendPoint[],
  sectionAbsent = false,
): boolean {
  return section.length === 0 && !sectionAbsent
}

/**
 * ★★★ `probe_system` 的健康判据（`:959-960` 逐字）：
 * ```go
 * healthy := ReadyExpired == 0 &&
 *     (Running > 0 || LastActivity == nil || time.Since(*LastActivity) < 15*time.Minute)
 * ```
 * ⇒ ★★ **两个查询都失败时它算出 `true`**（零值 ⇒ unclaimable==0 且 last_activity==nil）。
 * ⇒ `healthy === true` **不能**直接当成「探测管线活着」。
 */
export function selfCheckProbeSystemHealthy(
  ps: ScProbeSystem,
  nowMs: number,
): boolean {
  if (ps.queue_ready_unclaimable !== 0) return false
  if (ps.queue_running > 0) return true
  if (ps.queue_last_activity_at === null) return true
  const last = Date.parse(ps.queue_last_activity_at)
  if (Number.isNaN(last)) return false
  return nowMs - last < SELF_CHECK_PROBE_STALE_MS
}

/** ★★ 两个探测查询都失败时的形状：`healthy` 会被算成 `true`（文件头第 (4) 条）。 */
export function selfCheckProbeSystemLooksUnqueried(ps: ScProbeSystem): boolean {
  return (
    ps.queue_ready === 0 &&
    ps.queue_running === 0 &&
    ps.due_states === 0 &&
    ps.queue_last_activity_at === null &&
    ps.last_probe_attempt_at === null
  )
}

/** ★★ `range` 回显的是**请求值**（文件头第 (3) 条）⇒ 不能当生效窗口用。 */
export function selfCheckStatsRangeIsEffectiveRange(stats: ScStatsResponse): boolean {
  return (SELF_CHECK_STATS_RANGES as readonly string[]).includes(stats.range)
}

/** ★★ 未知 range 的实际窗口永远是 24h（`:792-793`）。 */
export function selfCheckStatsEffectiveRange(stats: ScStatsResponse): string {
  return selfCheckStatsRangeIsEffectiveRange(stats) ? stats.range : SELF_CHECK_STATS_DEFAULT_RANGE
}

/** ★ `executing` 只是 `queue_running > 0` 的复读（`:958`）。 */
export function selfCheckProbeSystemExecuting(ps: ScProbeSystem): boolean {
  return ps.queue_running > 0
}

/** ★★ `models` 没有 `partial` 计数 ⇒ 残差只能来自 partial（也可能来自其它状态）。 */
export function selfCheckModelCountsLeaveResidual(m: ScModelInfo): number {
  return m.total - m.success - m.failed
}

/** ★★ `models` 是**全时段**，stats 是窗口 ⇒ 两个 total 不可直接比。 */
export function selfCheckModelsAreAllTime(): boolean {
  return true
}

/** ★ `0 毫秒 ⇒ upstream_latency_ms 键缺失`（文件头第 (6) 条）。 */
export function selfCheckUpstreamLatencyMsOrNull(run: ScRun): number | null {
  return run.upstream_latency_ms ?? null
}

/** ★★ `upstream_tested` 恒在；`upstream_result` 缺失与空串同义 ⇒ 看 tested 才能定性。 */
export function selfCheckUpstreamOutcome(
  run: ScRun,
): 'tested' | 'not_tested' | 'result_empty' | 'result_present' {
  if (!run.upstream_tested) return 'not_tested'
  if (run.upstream_result === undefined || run.upstream_result === '') return 'result_empty'
  return 'result_present'
}