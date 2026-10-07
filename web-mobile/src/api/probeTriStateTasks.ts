import { req, type RequestOptions } from './client'

/**
 * probeTriStateTasks.ts — 三态探测队列（2026-10-07，第七十八批）。
 *
 * GET /api/admin/probe/tasks?status=pending|in_flight|completed&limit=
 *
 * - **注册**：`admin/probe_dashboard.go:1907` 的 `adminWrap(h.handleProbeTaskRoute)`
 *   ⇒ **admin 档**，tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
 * - **路由是方法多路复用**：GET 走 `handleProbeTaskList`（`:1841`，只读），
 *   POST 走 `handleProbeTaskCreate`、DELETE 走 `handleProbeTaskCancel`
 *   （`:1889`/`:1891`）⇒ 本模块**只碰 GET**，写操作不碰。
 * - 桌面调用方：`web/src/api-selfcheck.ts:313`（自检页的三段队列视图），
 *   组件 `web/src/components/probe/ProbeTriStateQueue.vue`。
 *
 * ## ★★★★★ 本族最要紧的八件事
 *
 * (1) ★★★★★ **响应里的 `status` 不是数据库里的 status。**
 *      `queryProbeTriStateTasks`（`:1813-1821`）把六个 DB 状态压成三个：
 *      ```go
 *      switch t.Status {
 *      case "ready":        t.Status = "pending"
 *      case "running":      t.Status = "in_flight"
 *      default:             t.Outcome = t.Status; t.Status = "completed"
 *      }
 *      ```
 *      DB 的 CHECK 是 **6 值**：`('ready','running','success','failed','expired','cancelled')`
 *      （`sql/migrations/domain/343_credential_probe_queue.sql`）
 *      ⇒ ★ **原始状态只在 completed 行以 `outcome` 保留**；
 *        pending / in_flight 行的原值（ready / running）**被丢掉且不可恢复**。
 *
 * (2) ★★★★★ `outcome` 与 `status` 是**互斥且可验**的一对。
 *      ⇒ `status === 'completed'` ⇒ `outcome` 必在且是四值之一；
 *        `status !== 'completed'` ⇒ `outcome` 必**不在**（omitempty）。
 *      ⇒ 客户端可以自验这条不变量，不需要信后端。
 *
 * (3) ★★★★★ `next_retry_at_ms` 的存在性 ⇔ `status === 'pending'`。
 *      `:1822-1824`：`if t.Status == "pending" && !nextRunAt.IsZero() { ... }`
 *      而 `next_run_at TIMESTAMPTZ NOT NULL DEFAULT now()` ⇒ pending 行必然非零
 *      ⇒ ★ **pending 行必有这个键，另两条腿必没有**。
 *      ⇒ 退避信息只在 pending 腿有意义。
 *
 * (4) ★★★★ ★★ **`*int` + omitempty 与 `int` + omitempty 的行为相反。**
 *      `HTTPStatus *int` / `LatencyMs *int`（`:1733-1734`）是**指针**：
 *      omitempty 只在 **nil** 时省略 ⇒ 数据库里的 **0 会原样出现**（`http_status: 0`）。
 *      对照第七十六批 `upstream_latency_ms int` ⇒ 0 被 omitempty **吃掉**变成键缺失。
 *      ⇒ ★★ 同一个 API 层里这两种编码都出现过，解包器必须**分别**处理。
 *
 * (5) ★★★★ `provider_id` / `provider_name` / `provider_code` 三个都带 omitempty。
 *      provider_id 是 `*int64`（LEFT JOIN 可能为 NULL）；
 *      name/code 走 `COALESCE(NULLIF(...), NULLIF(...), NULLIF(...), '')`（`:1781-1782`）
 *      ⇒ 三者皆空时 SQL 给 **`''`**，再被 omitempty 吃掉
 *      ⇒ ★ 「供应商未知」表现为**键缺失**，不是空串、也不是 null。
 *
 * (6) ★★★★ `origin` 是从 `source` 推出来的三值枚举（`probeTriStateOrigin`，`:1743-1751`）：
 *      ```go
 *      case "request_failure", "no_candidates": return "error"
 *      case "admin", "external_async":          return "manual"
 *      default:                                  return "scheduled"
 *      ```
 *      ⇒ ★★ **`no_candidates` 不在 `source` 的 CHECK 约束里**
 *      （CHECK 只有 `'request_failure','periodic','external_async','admin'`）
 *      ⇒ 那个分支项**不可达**（恒真守卫，见下）。
 *      ⇒ 但 `origin` 与 `source` 的对应关系**客户端可自验**。
 *
 * (7) ★★★ 参数是**严格校验**（不是静默回落）：
 *      - `status` 不在 `pending|in_flight|completed` ⇒ **400** `status must be pending|in_flight|completed`；
 *      - `limit` 不在 `1..200` ⇒ **400** `limit must be 1..200`；
 *      - 缺省 `status=pending`、`limit=50`。
 *      ⇒ ★ 与第七十六批 `/self-check/runs` 的 `limit` **静默回落**形成直接对照。
 *
 * (8) ★★★ `count` 是**本页长度**，不是总数。
 *      `:1879` 的 `"count": len(tasks)` ⇒ `count === tasks.length` **恒成立**
 *      ⇒ ★ 「取满了吗」只能靠 `tasks.length < limit` 判断，`count` 没有额外信息。
 */

/** `probe_dashboard.go:1750-1751` 的 default 分支。 */
export const PROBE_TASK_ORIGIN_SCHEDULED = 'scheduled'
export const PROBE_TASK_ORIGIN_ERROR = 'error'
export const PROBE_TASK_ORIGIN_MANUAL = 'manual'
export const PROBE_TASK_ORIGINS = [
  PROBE_TASK_ORIGIN_SCHEDULED,
  PROBE_TASK_ORIGIN_ERROR,
  PROBE_TASK_ORIGIN_MANUAL,
] as const
export type ProbeTaskOrigin = (typeof PROBE_TASK_ORIGINS)[number]

/** `:1851` 的三值白名单。 */
export const PROBE_TASK_STATUSES = ['pending', 'in_flight', 'completed'] as const
export type ProbeTaskStatus = (typeof PROBE_TASK_STATUSES)[number]

/** DB 的六值 CHECK ⇒ 响应里只有 completed 腿会把它放进 `outcome`（`:1819`）。 */
export const PROBE_TASK_OUTCOMES = ['success', 'failed', 'expired', 'cancelled'] as const
export type ProbeTaskOutcome = (typeof PROBE_TASK_OUTCOMES)[number]

/** `credential_probe_queue_status_check` 的六值。 */
export const PROBE_QUEUE_DB_STATUSES = [
  'ready', 'running', 'success', 'failed', 'expired', 'cancelled',
] as const

/** `credential_probe_queue_source_check` 的四值。 */
export const PROBE_QUEUE_DB_SOURCES = [
  'request_failure', 'periodic', 'external_async', 'admin',
] as const

/** `:1847-1848` 的缺省。 */
export const PROBE_TASK_DEFAULT_STATUS: ProbeTaskStatus = 'pending'
/** `:1856`。 */
export const PROBE_TASK_DEFAULT_LIMIT = 50
/** `:1755` 的 `probeCompletedWindow`，同时也是 `:1859` 报的上界。 */
export const PROBE_TASK_MAX_LIMIT = 200

export const PROBE_TASK_BAD_STATUS_MESSAGE = 'status must be pending|in_flight|completed'
export const PROBE_TASK_BAD_LIMIT_MESSAGE = 'limit must be 1..200'
export const PROBE_DB_NOT_CONFIGURED_MESSAGE = 'database not configured'
export const PROBE_TASK_INTERNAL_ERROR_MESSAGE = 'internal server error'

/** ProbeTriStateTask（`:1711-1738`）里**不带 omitempty** 的十三键。 */
export const PROBE_TASK_ALWAYS_KEYS = [
  'id', 'dedup_key', 'credential_id', 'raw_model', 'command', 'source',
  'origin', 'status', 'attempt', 'max_attempts', 'priority',
  'created_at', 'updated_at',
] as const
/** 带 `omitempty` 的九键。 */
export const PROBE_TASK_OPTIONAL_KEYS = [
  'provider_id', 'provider_name', 'provider_code', 'outcome',
  'next_retry_at_ms', 'reason_code', 'http_status', 'latency_ms', 'finished_at',
] as const

/** 响应顶层三键（`:1876-1880`）。 */
export const PROBE_TRI_STATE_KEYS = ['status', 'tasks', 'count'] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

export interface ProbeTriStateTask {
  id: number
  dedup_key: string
  credential_id: number
  raw_model: string
  command: string
  /** DB 原值（CHECK 四值之一）。 */
  source: string
  /** ★ 由 `source` 推导的三值（文件头第 (6) 条）。 */
  origin: ProbeTaskOrigin
  /** ★ 三个之一，**不是** DB 的六值之一（文件头第 (1) 条）。 */
  status: ProbeTaskStatus
  attempt: number
  max_attempts: number
  priority: number
  created_at: string
  updated_at: string
  /** ★ 条件键：`provider_id` 是 `*int64` + omitempty。 */
  provider_id?: number
  /** ★ 条件键：全空串时 SQL 给 `''`，再被 omitempty 吃掉（文件头第 (5) 条）。 */
  provider_name?: string
  provider_code?: string
  /** ★ 条件键：**只在 completed 行**（文件头第 (2) 条）。 */
  outcome?: ProbeTaskOutcome
  /** ★ 条件键：**只在 pending 行**（文件头第 (3) 条）。 */
  next_retry_at_ms?: number
  reason_code?: string
  /** ★★ `*int` + omitempty ⇒ **0 会出现**，不像 int+omitempty 那样被吃掉。 */
  http_status?: number
  latency_ms?: number
  finished_at?: string
}

export interface ProbeTriStateResponse {
  /** ★ 回显请求的 status（`:1877`），三个合法值之一。 */
  status: ProbeTaskStatus
  /** ★ 恒数组（`:1797` 的 `[]ProbeTriStateTask{}`），不会是 null。 */
  tasks: ProbeTriStateTask[]
  /** ★ 本页长度，**不是总数**（文件头第 (8) 条）。 */
  count: number
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

export function fetchProbeTriStateTasks(
  status?: string,
  limit?: number,
  options?: RequestOptions,
): Promise<ProbeTriStateResponse> {
  // ★ 不传 status 时**不发**这个参数：后端会补 `pending`（`:1847-1848`），
  //   但补出来的值不会出现在 URL 里，反过来也会让 400 的判定更难读。
  const qs = new URLSearchParams()
  if (status != null) qs.set('status', status)
  if (limit != null) qs.set('limit', String(limit))
  const q = qs.toString()
  return req<unknown>(
    'GET',
    `/api/admin/probe/tasks${q ? `?${q}` : ''}`,
    undefined,
    options,
  ).then(unwrapProbeTriStateTasks)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapProbeTriStateTasks(resp: unknown): ProbeTriStateResponse {
  const d = requireObject(resp, '三态探测队列')
  requireKeys(d, PROBE_TRI_STATE_KEYS, '三态探测队列')
  if (typeof d.status !== 'string') throw new Error('三态探测队列 的 status 不是字符串')
  // ★ 这个值来自 `:1851` 的白名单 ⇒ 真的是封闭枚举，可以校验取值
  if (!(PROBE_TASK_STATUSES as readonly string[]).includes(d.status)) {
    throw new Error(`三态探测队列 的 status 不是已知状态（${String(d.status)}）`)
  }
  if (!Array.isArray(d.tasks)) throw new Error('三态探测队列 的 tasks 不是数组')
  if (typeof d.count !== 'number') throw new Error('三态探测队列 的 count 不是数字')
  d.tasks.forEach((t, i) => requireTask(t, `三态探测队列 的 tasks[${i}]`))
  return d as unknown as ProbeTriStateResponse
}

function requireTask(v: unknown, where: string): ProbeTriStateTask {
  const d = requireObject(v, where)
  requireKeys(d, PROBE_TASK_ALWAYS_KEYS, where)
  for (const k of ['id', 'credential_id', 'attempt', 'max_attempts', 'priority'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of [
    'dedup_key', 'raw_model', 'command', 'source', 'origin', 'status',
    'created_at', 'updated_at',
  ] as const) {
    if (typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  // ★ `origin` 由两个字面量分支加 default 决定 ⇒ 封闭三值，可以校验取值
  //   （上面的循环已经把非字符串拦掉了；这里 `String()` 只是为了满足 `unknown` 的类型）
  if (!(PROBE_TASK_ORIGINS as readonly string[]).includes(String(d.origin))) {
    throw new Error(`${where} 的 origin 不是已知来源（${String(d.origin)}）`)
  }
  if (!(PROBE_TASK_STATUSES as readonly string[]).includes(String(d.status))) {
    throw new Error(`${where} 的 status 不是已知状态（${String(d.status)}）`)
  }
  // ★ 数字类条件键：指针 + omitempty ⇒ 键在时必为数字（含 0）
  for (const k of ['provider_id', 'next_retry_at_ms', 'http_status', 'latency_ms'] as const) {
    if (k in d && typeof d[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  for (const k of ['provider_name', 'provider_code', 'reason_code', 'finished_at'] as const) {
    if (k in d && typeof d[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if ('outcome' in d && !(PROBE_TASK_OUTCOMES as readonly string[]).includes(d.outcome as string)) {
    throw new Error(`${where} 的 outcome 不是已知结果（${String(d.outcome)}）`)
  }
  return d as unknown as ProbeTriStateTask
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * ★ `probeTriStateOrigin`（`:1743-1751`）的客户端复刻。
 * @param source DB 的 `source` 原值。
 */
export function probeTaskOriginFromSource(source: string): ProbeTaskOrigin {
  if (source === 'request_failure' || source === 'no_candidates') {
    return PROBE_TASK_ORIGIN_ERROR
  }
  if (source === 'admin' || source === 'external_async') {
    return PROBE_TASK_ORIGIN_MANUAL
  }
  return PROBE_TASK_ORIGIN_SCHEDULED
}

/**
 * ★★ 后端给的 `origin` 与 `source` 对不上时为 true（契约漂移）。
 * 客户端**不需要信** `origin`，可以自己算。
 */
export function probeTaskOriginMismatch(t: ProbeTriStateTask): boolean {
  return t.origin !== probeTaskOriginFromSource(t.source)
}

/** ★★ `outcome` 与 `status` 的互斥不变量（文件头第 (2) 条）。 */
export function probeTaskOutcomeIsConsistent(t: ProbeTriStateTask): boolean {
  const has = t.outcome !== undefined
  if (t.status === 'completed') return has
  return !has
}

/** ★★ `next_retry_at_ms` 的存在性 ⇔ `status === 'pending'`（文件头第 (3) 条）。 */
export function probeTaskNextRetryIsConsistent(t: ProbeTriStateTask): boolean {
  return t.next_retry_at_ms !== undefined === (t.status === 'pending')
}

/** ★ `next_retry_at_ms` 的可空取值（只有 pending 行有）。 */
export function probeTaskNextRetryAtMsOrNull(t: ProbeTriStateTask): number | null {
  return t.next_retry_at_ms ?? null
}

/** ★★ 三个 provider 键**全缺** ⇒ 供应商未知（不是空串、不是 null）。 */
export function probeTaskProviderIsUnknown(t: ProbeTriStateTask): boolean {
  return (
    t.provider_id === undefined &&
    t.provider_name === undefined &&
    t.provider_code === undefined
  )
}

/** ★ 供应商显示名的可空取值：键缺失与空串在本端点不可能同现（omitempty 已吃掉 `''`）。 */
export function probeTaskProviderNameOrNull(t: ProbeTriStateTask): string | null {
  return t.provider_name ?? null
}

/**
 * ★★ `count` 恒等于 `tasks.length`（`:1879`）⇒ 「取满了吗」只能靠长度与 limit 比。
 * @param limit 本次请求的 limit；请求没带 limit 时后端用 50。
 */
export function probeTaskPageIsFull(r: ProbeTriStateResponse, limit?: number): boolean {
  const n = limit ?? PROBE_TASK_DEFAULT_LIMIT
  return r.tasks.length >= n
}

/** ★★ 「可能还有下一页」的判据；`count` 提供不了额外信息（文件头第 (8) 条）。 */
export function probeTaskMayHaveMore(r: ProbeTriStateResponse, limit?: number): boolean {
  return probeTaskPageIsFull(r, limit)
}

/** ★ 重试已用尽：`attempt >= max_attempts` 且还没进终态。 */
export function probeTaskRetriesExhausted(t: ProbeTriStateTask): boolean {
  return t.status !== 'completed' && t.attempt >= t.max_attempts
}

/** ★ 在途（已领到 lease、正在跑）。 */
export function probeTaskIsInFlight(t: ProbeTriStateTask): boolean {
  return t.status === 'in_flight'
}

/** ★ 终态且成功。 */
export function probeTaskSucceeded(t: ProbeTriStateTask): boolean {
  return t.status === 'completed' && t.outcome === 'success'
}

/** ★ 终态但不是成功（含 failed / expired / cancelled）。 */
export function probeTaskFailedOrAbandoned(t: ProbeTriStateTask): boolean {
  return t.status === 'completed' && t.outcome !== undefined && t.outcome !== 'success'
}

/**
 * ★★ `http_status` 的 0 是**真实存在**的（指针 + omitempty 不会吃掉 0）。
 * ⇒ 用 `if (t.http_status)` 判「有没有测出状态码」会把 0 误判成「没有」。
 */
export function probeTaskHasHttpStatus(t: ProbeTriStateTask): boolean {
  return t.http_status !== undefined
}

/** ★★ 同上：0 毫秒也是真实值。 */
export function probeTaskLatencyMsOrNull(t: ProbeTriStateTask): number | null {
  return t.latency_ms ?? null
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 内部工具
 * ═══════════════════════════════════════════════════════════════════════════ */

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