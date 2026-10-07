// credentialState.ts — 凭据 × 模型实时状态读面（第四十九批，**superAdmin 档**，1 条只读端点）。
//   GET /api/credentials/{id}/models/{model}/state
//
// 后端逐条对应：
//   admin/credential_state_handlers.go:174-180  registerStateRoutes（★wrap := h.superAdmin）
//   admin/credential_state_handlers.go:136-168  handleCredentialStateQuery
//   admin/credential_state_handlers.go:182-189  parseCredentialID（Atoi 失败**或** <= 0 ⇒ 400）
//   admin/credential_state_handlers.go:197-199  writeStateServiceUnavailable ⇒ 503
//   domains/credentialstate/manager.go:738-765  GetState（★三层 miss ⇒ (nil, nil)）
//   domains/credentialstate/cache.go:59-78      getFromRedis（★err 被外层静默吞掉）
//   domains/credentialstate/cache.go:105-142    getFromDB 的 node_probe_state 支
//   domains/credentialstate/cache.go:144-202    getLegacyStateFromDB（model_probe_state 支）
//   domains/credentialstate/state.go:11-28      State 结构体（12 恒存在 + 4 omitempty）
//   domains/credentialstate/manager.go:899-901  Enabled() = m != nil && m.db != nil
//
// ★★★★★★★★★★ 头号陷阱一：**错误响应是 `text/plain`，不是本仓惯用的 JSON 信封**。
//   这四个 handler 全部用 `http.Error(w, msg, code)`（:143 / :158 / :185 / :198），
//   而本仓其它 400 多条端点用 `writeError(w, code, msg)` ⇒ `{"error":{"detail":msg}}`。
//   ⇒ ★★★★ 响应体是纯文本 `msg`，**`error.detail` 根本不存在**。
//     移动端 `client.ts` 的 `errorMessage()` 走 `JSON.parse` 失败分支 ⇒ **原样返回整段文本**。
//   ⇒ ★★★★★★ 而且 `http.Error` 会**追加一个换行符**（Go 源码 `fmt.Fprintln(w, error)`）
//     ⇒ 移动端拿到的报文是 `"state service not available\n"`
//     ⇒ **任何带 `$` 锚点的正则都匹配不上**，任何 `toBe(原文)` 都失败。
//   ⇒ 本模块所有错误判据一律**不做 `$` 锚点**，并另给一个显式去尾换行的 helper。
//   ⇒ ★★★ 本仓 admin 包里 `http.Error` 出现 **165 处**、横跨 20+ 文件 ⇒ 不是孤例。
//
// ★★★★★★★★★★ 头号陷阱二：`state` **可以是 `null`** —— 三层缓存全 miss。
//   `manager.go:738-765`：
//       L1 内存 → L2 Redis → L3 DB；L3 返回 `(nil, nil)` 时**不报错**，
//       `GetState` 直接 `return state, nil`（state 是 nil）
//   handler `:152-167` 只判 `err != nil`，`state == nil` 时照样序列化：
//       {"credential_id":N,"model":"M","state":null}   ← 200
//   ⇒ ★★★★ 「从没探测过」返回 **200 + `state: null`**，**不是** 404。
//   ⇒ 按 `state.available === boolean` 之类校验会**拒掉合法形状**。
//
// ★★★★★★★★★★ 头号陷阱三：**五条指标在两条 DB 分支里根本没被赋值**。
//   `getFromDB` 的 node_probe 支（cache.go:118-134）只填 6 个字段：
//       CredentialID / Model / Available / ConsecutiveFails / LastUpdatedAt / RecoverAt / Source
//   `getLegacyStateFromDB`（cache.go:144-202）只填 5 个：
//       CredentialID / Model / HealthStatus / ConsecutiveFails / LastUpdatedAt / RecoverAt / Source
//   ⇒ ★★★★ `success_rate` / `avg_latency_ms` / `p95_latency_ms` / `active_sessions` /
//     `concurrency_limit` 在**两条 DB 分支里都是 Go 零值** ⇒ 序列化成 `0`。
//   ⇒ ★★★ 而缓存命中时（由探测写入的完整 State）这些字段**有真值**。
//     ⇒ **同一个 (凭据, 模型) 第一次查是 0、缓存后再查可能是 0.87**，
//       两次都是 200，**没有任何字段说明差异来自哪一层**。
//
// ★★★★★★★★ 头号陷阱四：`source` 记的是「**谁写的**」，不是「从哪读的」。
//   `state.go:27` 的注释列了 5 个取值：
//       request, probe_v2, model_probe, passive, manual
//   但两条 DB 分支写进去的是：
//       cache.go:133  Source: "node_probe_db"
//       cache.go:199  Source: "db"
//   ⇒ ★★★★ **实际取值集合 ⊋ 注释集合**，多出两种「来自数据库」的值。
//   ⇒ ★★ 而 `source` 与「答案是从内存/Redis/DB 哪一层来的」**正交** ⇒
//     **客户端无法判断命中了哪一层**。
//
// ★★★★★★★★ 头号陷阱五：`health_status` 的实际取值也超出注释。
//   `state.go:15` 注释：`healthy, warning, degraded, unreachable`
//   但 legacy 分支 `cache.go:184-192` 只在下列取值时把 `Available` 置 true：
//       healthy_confirmed / probing / available / healthy
//   ⇒ ★★★★ 至少多出 `healthy_confirmed` / `probing` / `available` 三种。
//   ⇒ ★★★ node_probe 支（cache.go:131-133）**只在不可达时**赋 `"unreachable"`，
//     其余情况**保持 Go 零值空串** ⇒ ★★ **`health_status === ''` 不等于「健康」**。
//
// ★★★★ 静默降级两处：
//   · `manager.go:748` `if state, err := m.getFromRedis(...); err == nil && state != nil`
//     ⇒ ★★★ **Redis 挂掉/超时/反序列化失败全部被吞掉**，直接落到 DB，无任何信号。
//   · `cache.go:138` `isUndefinedTable(nodeErr)`（PG 错误码 42P01，表不存在）
//     ⇒ ★★★ **表不存在被当作「这一支没数据」**，静默落到 legacy 分支。
//
// ★★★ `LastUpdatedAt` 的两种来源：
//   · node_probe 支：`LastUpdatedAt: time.Now()`（cache.go:128）⇒ **是查询时刻，不是探测时刻**
//   · legacy 支：`lastAttemptAt` 为 NULL 时**保持 Go 零值**
//     ⇒ ★★★ `last_updated_at` 可能是 `"0001-01-01T00:00:00Z"`
//       （本仓第 **2** 处 Go 零值时间；第 1 处是 free-discovery `ListTasks` 的 `updated_at`）
//
// ★★ `RecoverAt` 在两条 DB 分支里**都恒非 nil**（node_probe 取 `&nextRetryAt`；
//   legacy 直接 `state.RecoverAt = nextRetryAt` —— 后者是**值类型** `time.Time`，
//   即使是零值也会赋）⇒ 该 `omitempty` 键在这两条分支里**必存在**，且可能是 Go 零值。
//
// ★★ 成功响应用 `json.NewEncoder(w).Encode(...)` ⇒ **末尾带一个换行符**（`Encode` 会 append `\n`）。
//   JSON 解析无害，但字节级对账要注意。
//
// ★★ `model == ""` 那条 400（:142-145）**实际不可达**：Go 1.22 的 `{model}` 不匹配空段，
//   而 ServeMux 会先把 `//` 清理掉 ⇒ `/models//state` 会被重定向成 `/models/state`
//   （model 变成字面量 `"state"`）⇒ 客户端永远不用处理这一态。
//
// ★★ 同族三条 POST（`/credentials/{id}/test`、`/credentials/test-batch`、
//   `/credentials/{id}/models/{model}/test`）**都是 superAdmin 且有外部副作用**
//   （真的触发一次探测）⇒ 本页一律不提供入口。

import type { RequestOptions } from './client'
import { req } from './client'

// ── 枚举（★ 两组都要给：注释集合 ⊂ 实际集合）─────────────────────────

/** `state.go:27` **注释**里列的 source 取值。 */
export const CRED_STATE_SOURCE_DOCUMENTED = ['request', 'probe_v2', 'model_probe', 'passive', 'manual'] as const

/** ★ 两条 DB 分支实际写入的另两个值（`cache.go:133` / `cache.go:199`）。 */
export const CRED_STATE_SOURCE_FROM_DB = ['node_probe_db', 'db'] as const

/** 实际出现过的完整集合。 */
export const CRED_STATE_SOURCE_OBSERVED = [
  ...CRED_STATE_SOURCE_DOCUMENTED,
  ...CRED_STATE_SOURCE_FROM_DB,
] as const

/** `state.go:15` **注释**里列的 health_status 取值。 */
export const CRED_STATE_HEALTH_DOCUMENTED = ['healthy', 'warning', 'degraded', 'unreachable'] as const

/**
 * ★ legacy 分支（`cache.go:184-187`）还认这三种，注释里没有；
 *   node_probe 分支未赋值时是**空串** ⇒ 实际集合含 `''`。
 */
export const CRED_STATE_HEALTH_UNDOCUMENTED = ['healthy_confirmed', 'probing', 'available', ''] as const

export const CRED_STATE_HEALTH_OBSERVED = [
  ...CRED_STATE_HEALTH_DOCUMENTED,
  ...CRED_STATE_HEALTH_UNDOCUMENTED,
] as const

// ── 键集合 ────────────────────────────────────────────────────────────

/** ★ 12 个**恒存在**键（`state.go:12-27`，无 omitempty）。 */
export const CRED_STATE_REQUIRED_KEYS = [
  'credential_id',
  'model',
  'available',
  'health_status',
  'success_rate',
  'avg_latency_ms',
  'p95_latency_ms',
  'active_sessions',
  'concurrency_limit',
  'last_updated_at',
  'consecutive_fails',
  'source',
] as const

/** ★ 4 个 `omitempty` 键 ⇒ **条件存在**，缺失 ≠ null。 */
export const CRED_STATE_OPTIONAL_KEYS = ['last_success_at', 'last_failure_at', 'recover_at', 'last_error'] as const

// ── 类型 ───────────────────────────────────────────────────────────────

/** `domains/credentialstate/state.go:11-28` 逐字段照抄。 */
export interface CredState {
  credential_id: number
  model: string
  available: boolean
  /** ★ 可能是 `''`（未赋值），也可能是注释里没有的取值。 */
  health_status: string
  /** ★ 两条 DB 分支都**没赋值** ⇒ 缓存未命中时恒为 0。 */
  success_rate: number
  /** ★ 同上。 */
  avg_latency_ms: number
  /** ★ 同上。 */
  p95_latency_ms: number
  /** ★ 同上。 */
  active_sessions: number
  /** ★ 同上。 */
  concurrency_limit: number
  /** ★ node_probe 支写的是**查询时刻**；legacy 支可能是 Go 零值。 */
  last_updated_at: string
  consecutive_fails: number
  /** ★ 实际取值比注释多两种「来自数据库」的值。 */
  source: string
  /** ★ 条件存在（omitempty）。 */
  last_success_at?: string
  /** ★ 条件存在（omitempty）。 */
  last_failure_at?: string
  /** ★ 条件存在，但两条 DB 分支里**都恒存在**。 */
  recover_at?: string
  /** ★ 条件存在（omitempty）。 */
  last_error?: string
}

/** ★★ `state` 可为 `null`（三层缓存全 miss ⇒ `(nil, nil)` ⇒ 200）。 */
export interface CredStateEnvelope {
  credential_id: number
  model: string
  state: CredState | null
}

// ── 路径 ───────────────────────────────────────────────────────────────

export const CRED_STATE_PATH_PREFIX = '/api/credentials'

export function credStatePath(credId: string | number, model: string): string {
  return `${CRED_STATE_PATH_PREFIX}/${encodeURIComponent(String(credId))}/models/${encodeURIComponent(model)}/state`
}

// ── 形状判据 ───────────────────────────────────────────────────────────

function shapeFail(expect: string, actual: string): Error {
  return new Error(`凭据×模型状态 响应形状不符：期望 ${expect}，实得 ${actual}`)
}

function actualKind(v: unknown): string {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  if (typeof v === 'object') return 'object'
  return typeof v
}

function isObj(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function validateCredState(v: unknown, where: string): CredState {
  if (!isObj(v)) throw shapeFail(`${where} 是状态对象`, actualKind(v))
  const missing = CRED_STATE_REQUIRED_KEYS.filter((k) => !(k in v))
  if (missing.length > 0) {
    throw shapeFail(`${where} 的 12 个必填键齐全`, `缺 ${missing.length} 个（${missing.join(', ')}）`)
  }
  if (typeof v.credential_id !== 'number' || typeof v.model !== 'string') {
    throw shapeFail(`${where} 的 credential_id:number / model:string`, actualKind(v.credential_id))
  }
  if (typeof v.available !== 'boolean') throw shapeFail(`${where} 的 available:boolean`, actualKind(v.available))
  if (typeof v.health_status !== 'string') throw shapeFail(`${where} 的 health_status:string`, actualKind(v.health_status))
  if (typeof v.source !== 'string') throw shapeFail(`${where} 的 source:string`, actualKind(v.source))
  // ★★★ 五条指标：两条 DB 分支都没赋值 ⇒ 恒 0，但**键必须在**
  for (const k of ['success_rate', 'avg_latency_ms', 'p95_latency_ms', 'active_sessions', 'concurrency_limit'] as const) {
    if (typeof v[k] !== 'number') throw shapeFail(`${where} 的 ${k}:number`, actualKind(v[k]))
  }
  if (typeof v.consecutive_fails !== 'number') {
    throw shapeFail(`${where} 的 consecutive_fails:number`, actualKind(v.consecutive_fails))
  }
  if (typeof v.last_updated_at !== 'string') {
    throw shapeFail(`${where} 的 last_updated_at:string`, actualKind(v.last_updated_at))
  }
  // ★ 4 个 omitempty 键：若出现必须是字符串
  for (const k of CRED_STATE_OPTIONAL_KEYS) {
    if (k in v && typeof v[k] !== 'string') throw shapeFail(`${where} 的 ${k}:string（若存在）`, actualKind(v[k]))
  }
  return v as unknown as CredState
}

/**
 * ★★ `{credential_id, model, state}`：`state` **可为 `null`**（三层缓存全 miss）。
 *   解包判据只能钉「两个路径键的类型正确 + `state` 是对象或 null」，
 *   **不能**要求 12 个指标键（`null` 时它们根本不存在）。
 */
export function unwrapCredState(resp: unknown): CredStateEnvelope {
  if (isObj(resp)) {
    if (typeof resp.credential_id === 'number' && typeof resp.model === 'string') {
      if (resp.state === null) return { credential_id: resp.credential_id, model: resp.model, state: null }
      if (isObj(resp.state)) {
        return {
          credential_id: resp.credential_id,
          model: resp.model,
          state: validateCredState(resp.state, 'state'),
        }
      }
      throw shapeFail('state 是状态对象或 null', actualKind(resp.state))
    }
    if ('credential_id' in resp || 'state' in resp) throw shapeFail('{credential_id, model, state}', actualKind(resp))
  }
  throw shapeFail('{credential_id, model, state}', actualKind(resp))
}

export function fetchCredState(
  credId: string | number,
  model: string,
  options?: RequestOptions,
): Promise<CredStateEnvelope> {
  return req<unknown>('GET', credStatePath(credId, model), undefined, options).then(unwrapCredState)
}

// ── 请求侧判读 ─────────────────────────────────────────────────────────

/** ★★ 后端 `strconv.Atoi` 失败**或** `<= 0` ⇒ 同一句 400（`credential_state_handlers.go:183-186`）。 */
export function parseCredentialIdRejects(raw: string | number): boolean {
  const s = String(raw).trim()
  if (!/^[+-]?\d+$/.test(s)) return true
  return Number.parseInt(s, 10) <= 0
}

/** ★★ 路径段要做 encode：模型名里有 `/` 时不 encode 会打到别的路由上。 */
export function modelNeedsEncoding(model: string): boolean {
  return model !== encodeURIComponent(model)
}

/** ★ `{model}` 不匹配空段 ⇒ `/models//state` 会被 ServeMux 清理成 `/models/state`。 */
export function emptyModelSegmentIsUnreachable(model: string): boolean {
  return model === ''
}

/** ★★ 真实 scheduler 意义上的「探测从未发生」＝ `state === null`（**不是** 404）。 */
export function credStateNeverProbed(env: CredStateEnvelope): boolean {
  return env.state === null
}

/** ★★ 缺失的 omitempty 键（缺失 ≠ null）。 */
export function credStateMissingOptionalKeys(s: CredState): string[] {
  return CRED_STATE_OPTIONAL_KEYS.filter((k) => !(k in s))
}

/** ★★ `recover_at` 在两条 DB 分支里恒存在 ⇒ 它缺失只可能来自缓存里写的旧结构。 */
export function credStateRecoverAtAbsent(s: CredState): boolean {
  return !('recover_at' in s)
}

// ── 二义与不二义 ───────────────────────────────────────────────────────

/** Go 零值 `time.Time` 的 JSON 形态。★ 本仓第 2 处（第 1 处见 `freeDiscovery.ts`）。 */
export const GO_ZERO_TIME = '0001-01-01T00:00:00Z'

export function isGoZeroTime(v: string | null | undefined): boolean {
  return v === GO_ZERO_TIME
}

/** ★★ legacy 支的 `last_attempt_at` 为 NULL ⇒ `last_updated_at` 是 Go 零值。 */
export function credStateUpdatedAtIsUnreadable(s: CredState): boolean {
  return isGoZeroTime(s.last_updated_at)
}

/** ★★★ node_probe 支把 `LastUpdatedAt` 写成 `time.Now()` ⇒ **是查询时刻，不是探测时刻**。 */
export function credStateUpdatedAtIsQueryTime(s: CredState): boolean {
  return s.source === 'node_probe_db' && !isGoZeroTime(s.last_updated_at)
}

/** ★ `health_status === ''` 表示「没被判为不可达」，**不等于**「健康」。 */
export function credStateHealthUnset(s: CredState): boolean {
  return s.health_status === ''
}

/** ★★ 实际取值超出 `state.go:15` 注释的那些（客户端不能按注释建枚举）。 */
export function credStateHealthUndocumented(s: CredState): boolean {
  return !(CRED_STATE_HEALTH_DOCUMENTED as readonly string[]).includes(s.health_status)
}

/** ★★ 同上：注释集合 ⊂ 实际集合，`source` 也一样。 */
export function credStateSourceUndocumented(s: CredState): boolean {
  return !(CRED_STATE_SOURCE_DOCUMENTED as readonly string[]).includes(s.source)
}

/** ★ `source` 是「谁写的」，**不是**「从哪一层读的」⇒ 命中层不可判。 */
export function credStateSourceSaysStorageLayer(s: CredState): boolean {
  return (CRED_STATE_SOURCE_FROM_DB as readonly string[]).includes(s.source)
}

/**
 * ★★ 缓存层**不可见**：Redis 挂掉被静默吞掉（`manager.go:748`）、表不存在也静默回退
 *   （`cache.go:138`）⇒ 响应里**没有任何字段**记录命中了内存/Redis/DB 哪一层。
 *   这里**不提供**谓词：能表达这件事的谓词必然是恒真或恒假，两者是假判据。
 *   要表达「答案来自哪一层不可判」，只能在页面上写死一条说明文案。
 */

/** ★★ 五条指标在两条 DB 分支里都没赋值。 */
export const CRED_STATE_METRIC_PLACEHOLDER_FIELDS = [
  'success_rate',
  'avg_latency_ms',
  'p95_latency_ms',
  'active_sessions',
  'concurrency_limit',
] as const

/** ★★ 五条全是 0 —— 在 DB 直查时是「**没实现**」，在缓存命中时可能是真值。 */
export function credStateMetricsAllZero(s: CredState): boolean {
  return CRED_STATE_METRIC_PLACEHOLDER_FIELDS.every((k) => s[k] === 0)
}

/**
 * ★★★★ `success_rate === 0` 有**三种**成因，分不开：
 *   1. 两条 DB 分支**根本没赋值** ⇒ Go 零值
 *   2. 缓存命中但真的是 0% 成功率
 *   3. 从没成功调用过
 */
export function credStateSuccessRateIsIndeterminate(s: CredState): boolean {
  return s.success_rate === 0
}

/**
 * ★★ `available` 与 `health_status` 是**两个独立**信号，但代码里有一处**必须一致**：
 *   `cache.go:131-133` 在不可达时**同时**设 `Available=false` 与 `HealthStatus="unreachable"`。
 *   ⇒ 「标了 unreachable 却说可用」是自相矛盾的形状。
 */
export function credStateSignalsDisagree(s: CredState): boolean {
  return s.health_status === 'unreachable' && s.available === true
}

/**
 * ★★ 反向约束（`cache.go:184-192`）：legacy 分支只在 health_status **不属于**
 *   `healthy_confirmed` / `probing` / `available` / `healthy` 时才不动 `Available`
 *   —— 注意该分支**从不**把 `Available` 置 false，它保持 Go 零值 false。
 *   ⇒ `available === false` 且 health_status 是那四个之一 ⇒ 不可能由该分支产生。
 */
export function credStateAvailableFalseWithHealthyLabel(s: CredState): boolean {
  const healthyLabels = ['healthy_confirmed', 'probing', 'available', 'healthy']
  return s.available === false && healthyLabels.includes(s.health_status)
}

/** ★ `consecutive_fails > 0` 与 `available === false` 是两件事。 */
export function credStateHasFails(s: CredState): boolean {
  return s.consecutive_fails > 0
}

/** ★ `last_error` 是 omitempty ⇒ 键缺失与空串同义（都没有错误记录）。 */
export function credStateLastErrorMissing(s: CredState): boolean {
  return !('last_error' in s) || s.last_error === ''
}

// ── 错误文案（★ text/plain，且带尾换行）────────────────────────────────

/** ★★★ `http.Error` 追加的换行符（Go 源码 `fmt.Fprintln(w, error)`）。 */
export const HTTP_ERROR_TRAILING_NEWLINE = '\n'

/** ★ 去尾换行（仅去掉 `http.Error` 追加的那一个）。 */
export function stripHttpErrorNewline(msg: string): string {
  return msg.endsWith(HTTP_ERROR_TRAILING_NEWLINE) ? msg.slice(0, -1) : msg
}

/**
 * ★★★ 这个端点系列的错误体**不是 JSON** ⇒ `error.detail` 不存在，
 * 移动端拿到的是**带尾换行的纯文本**。
 */
export function isPlainTextErrorBody(text: string): boolean {
  const t = text.trim()
  if (t === '') return false
  return !(t.startsWith('{') || t.startsWith('['))
}

/** ★★ `Atoi` 失败**或** `<= 0` ⇒ 同一句 400。★ 不加 `$`：报文带尾换行。 */
export function invalidCredentialIdMessage(msg: string): boolean {
  return /^invalid credential ID/i.test(stripHttpErrorNewline(msg.trim()))
}

/** ★★ 503：manager 或 db 为 nil（`Enabled() = m != nil && m.db != nil`）。 */
export function stateServiceMissingMessage(msg: string): boolean {
  return /^state service not available/i.test(stripHttpErrorNewline(msg.trim()))
}

/** ★★ 500：`GetState` 出错（DB / Redis 之外的真实故障）。 */
export function getStateFailedMessage(msg: string): boolean {
  return /^failed to get state/i.test(stripHttpErrorNewline(msg.trim()))
}

/** ★★ 同路径的三个写端点是 superAdmin 档 ⇒ 租户管理员在本页全部拿 403。 */
export function superAdminOnlyMessage(msg: string): boolean {
  return /super_admin/i.test(msg) || /forbidden/i.test(msg)
}

/** ★ `model == ""` 那条 400 不可达（Go 1.22 `{model}` 不匹配空段）。 */
export function modelRequiredMessage(msg: string): boolean {
  return /^model is required/i.test(stripHttpErrorNewline(msg.trim()))
}

/**
 * ★★ 三个 503/500 判据必须**互斥**（文案不同，可靠）；
 *   但「层不可见」这一条无法从报文区分。
 */
export function credStateErrorKind(
  msg: string,
): 'invalid-id' | 'service-unavailable' | 'internal' | 'super-admin' | 'unknown' {
  if (invalidCredentialIdMessage(msg)) return 'invalid-id'
  if (stateServiceMissingMessage(msg)) return 'service-unavailable'
  if (getStateFailedMessage(msg)) return 'internal'
  if (superAdminOnlyMessage(msg)) return 'super-admin'
  return 'unknown'
}

/** ★ Redis 挂掉与「这一层没数据」**报文完全一样** ⇒ 分不开。 */
export function credStateRedisFailureIsInvisible(msg: string): boolean {
  // `manager.go:748` 吞掉 Redis 错误，所以「500 failed to get state」**不会**由 Redis 挂掉引起，
  // 而客户端看到的是一次成功的 DB 查询结果。
  return !stateServiceMissingMessage(msg) && !getStateFailedMessage(msg)
}
