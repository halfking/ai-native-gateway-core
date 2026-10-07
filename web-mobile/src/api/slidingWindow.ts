import { req, type RequestOptions } from './client'

/**
 * slidingWindow.ts — 凭据模型的滑窗调用明细（2026-10-08，第九十四批）。
 *
 * GET /api/credentials/sliding-window?credential_id=X&model=Y&minutes=60&limit=50
 *
 * - **注册**：**第五种注册形态**，`mux.HandleFunc` 在**另一个文件的方法**里
 *   （`admin/credential_monitor.go:156` 的 `RegisterMonitorRoutes`），
 *   由 `admin/handler.go:1453` 现构造并挂载：
 *   ```go
 *   monitorH.RegisterMonitorRoutes(mux, h.admin)   // ★ admin 档
 *   ```
 *   ⇒ ★ 与批 92 的 model-history **同一注册处、同一档位**。
 * - **实现**：`admin/credential_monitor.go:697-788`（handler）·
 *   `:790-809`（回退查询 `slidingWindowQuery`）· `credentialhealth/recorder.go:16-22`
 *   （`CallEntry`）· `:155-177`（`ComputeStats`）。
 * - **桌面调用方**：`web/src/api/credential-monitor.ts:149-168` —— `req<{…}>`
 *   直接强转，**不做任何校验** ⇒ 全部校验由本模块补上。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十四件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **整族是 `h.admin` 档**（`handler.go:1453`）⇒ tenant_admin **可用**
 *     ⇒ 抽屉席**不设** `requiresRole`（与批 91 的 `work-types` 相反）。
 *
 * (2) ★★★★★ **响应是 `map[string]any` 手写的，恒有 **8 个键**
 *     （`:769-779`），★ **桌面类型只声明了 6 个** —— 漏掉 `limit` 与 `total_returned`
 *     （`web/src/api/credential-monitor.ts:151-165`）⇒ 照桌面类型写的客户端
 *     **看不到 `total_returned`**，也就无从发现下面 (3) 那组互锁关系。
 *
 * (3) ★★★★★ **四个数字完全互锁**（全部可自验）：
 *     ```go
 *     if len(entries) > limit { entries = entries[:limit] }   // :764-766 先截断
 *     stats := credentialhealth.ComputeStats(entries)          // :768 再统计
 *     … "total_returned": len(entries),                        // :775
 *     ```
 *     而 `ComputeStats` 第一行就是 `Total: len(entries)`（`recorder.go:157`）
 *     ⇒ ⇒ **`total_returned === entries.length === stats.total`**
 *     ⇒ 且 `ComputeStats` 用 `if e.Success { Success++ } else { Failed++ }`
 *       （`:162-165`，**完备二分**）⇒ ⇒ **`stats.success + stats.failed === stats.total`**
 *     ⇒ ⇒ 四个数字锁在一起，任何一个错都会被另外三个抓住。
 *
 * (4) ★★★★★ **`error_kinds` 的求和「小于等于」`failed`，而不是相等**：
 *     `if e.ErrorKind != "" { stats.ErrorKinds[e.ErrorKind]++ }`（`recorder.go:166-168`）
 *     ⇒ ★★★ **失败的条目若 `err` 是空串，就完全不进 `error_kinds`**
 *     ⇒ ⇒ `sum(error_kinds) <= failed`；等号成立**当且仅当**每个失败条目都有非空 `err`。
 *     ⇒ ★★ 「失败原因分布」与「失败数」对不上时，**不是 bug，是那些失败没有归类**。
 *
 * (5) ★★★★★ **`failure_rate` 是派生值，且 `total === 0` 时恒为 `0`**：
 *     `if stats.Total > 0 { stats.FailureRate = float64(stats.Failed)/float64(stats.Total) }`
 *     （`:172-174`）⇒ ⇒ `total === 0 ⟹ failure_rate === 0`（**不是 NaN、不是 null**）
 *     ⇒ ⇒ `total > 0 ⟹ failure_rate === failed / total`。
 *
 * (6) ★★★★★ **`source` 的语义：只有一条方向可断言**：
 *     ```go
 *     source := "redis"
 *     entries := make([]CallEntry, 0)
 *     if m.recorder != nil && m.recorder.Enabled() { entries, _ = m.recorder.GetRecent(…) }
 *     if len(entries) == 0 { source = "request_logs"; … }     // ← 空必回退
 *     ```
 *     ⇒ ⇒ **`source === 'redis'` ⟹ `entries.length > 0`**（这是一条可断言的不变式）
 *     ⇒ ⇒ ★★ **`source === 'request_logs'` 是二义的**：Redis recorder 不可用
 *       **或** Redis 可用但窗口内无数据，**两种成因同形**。
 *
 * (7) ★★★★★ **Redis 的错误被整个丢弃**：`entries, _ = m.recorder.GetRecent(…)`（`:741`）
 *     ⇒ ★★★ Redis 挂掉 ⇒ 静默回退 request_logs ⇒ **响应里看不出 Redis 故障**。
 *     ⇒ ★ 与批 93 的 `enabled_source`（`default` 永不可达）、批 91 的 `count` 全 0
 *       同族：**「降级/兜底」的成因在响应里查不到。**
 *
 * (8) ★★★★ **`entries` 被强制非 nil**（`:757-759`，注释自陈「否则前端
 *     `windowEntries.length` 会抛 Cannot read properties of null」）
 *     ⇒ ⇒ **空时是 `[]` 不是 `null`**。
 *
 * (9) ★★★★ **`minutes` 完全没有校验**（`:715` `queryInt(r,"minutes",60)`），
 *     而 **`limit` 有 `1..500` 的 400**（`:725-728`）
 *     ⇒ ★★★ **同一族的两个查询参数，一个校验一个不校验**
 *     ⇒ ⇒ `minutes=0` / 负数 / 超大值**全部放行**，落到 SQL 的
 *       `ts > NOW() - ($3 || ' minutes')::interval`。
 *     ⇒ ★★ 且 `minutes=abc` 会被 `queryInt` **静默回落成 60**（`handler.go:1539-1542`）
 *       —— 与批 92 的 `limit=abc` 同一形态。
 *
 * (10) ★★★★ **`credential_id == 0` 才判 400**（`:719-722`）—— **不是 `< 1`**
 *     ⇒ ★★★ **负数能过这一关并进 SQL**（与批 92 完全同形）。
 *
 * (11) ★★★★ **`model` 是大小写不敏感匹配**：
 *     `AND lower(COALESCE(outbound_model, client_model)) = lower($2)`（`:804`）
 *     ⇒ ⇒ 客户端**不需要**自己 lowercase
 *     ⇒ ★★ 但**回显的 `model` 是原样传入的那一个**（`:770` 直接回显，不做规范化）
 *     ⇒ ⇒ **`model` 的回显可能与实际命中的 `outbound_model` 大小写不同。**
 *
 * (12) ★★★★ **两源的「历史深度」完全不同**：
 *     Redis recorder 是 **2 小时窗口 / 100 条**（`handler.go:713`，
 *     `credentialhealth.NewRecorder(client, 2*time.Hour, 100)`），
 *     回退源 `request_logs_with_current_month` 是**当月视图**（`:797`，
 *     注释称「视图冻结不可改投影（R37 定案）」）
 *     ⇒ ⇒ 切到 `request_logs` 后能看到的范围**突然变大**，不是「补齐了缺失的数据」。
 *
 * (13) ★★★★ **`CallEntry` 的 json tag 是缩写，与 Go 字段名完全不同**：
 *     `RequestID→rid`、`Timestamp→ts`、`Success→ok`、`LatencyMs→lat`、`ErrorKind→err`
 *     （`recorder.go:16-22`）⇒ ★★★ **照 Go 字段名写客户端必然全错**。
 *     - `rid` 是**恒在键但可能为空串**（`COALESCE(request_id,'')`，`:798`）
 *     - `ts` 是 **unix 毫秒**（`EXTRACT(EPOCH FROM ts)::bigint * 1000`）
 *     - `err` 是**唯一带 `omitempty`** 的键 ⇒ 成功条目里**键消失**
 *
 * (14) ★★★★ **回退查询 `ORDER BY ts DESC` 无 tiebreak**（`:807`）
 *     ⇒ ★★★ **同毫秒时间戳的条目顺序未定义** ⇒ 只能断言**非升序**。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验 envelope 的 **8 个恒在键与类型**、每个 `CallEntry` 的 5 个键、
 *   `stats` 的 5 个键；为 (3)(4)(5)(6)(14) 各提供判据。
 *   ★ **不校验** `source` / 各 `err` 取值的**取值域** —— 它们由 Redis / 日志自由文本决定。
 *   ★ **不提供**「`rid` 非空」判据 —— `COALESCE(…,'')` 表明空串是合法值（见 13）。
 */
export const SLIDING_WINDOW_PATH = '/api/credentials/sliding-window'

/** ★★ (9) `queryInt` 的缺省与 (10) 的边界。 */
export const SLIDING_WINDOW_DEFAULT_MINUTES = 60
export const SLIDING_WINDOW_DEFAULT_LIMIT = 50
export const SLIDING_WINDOW_LIMIT_MIN = 1
export const SLIDING_WINDOW_LIMIT_MAX = 500

/** ★★ (6) 两个取值；★ 注意 `request_logs` 那一侧是**二义的**。 */
export const SLIDING_WINDOW_SOURCES = ['redis', 'request_logs'] as const

/** ★★ (2) `writeJSON` 手写的 8 个键 —— ★ 桌面类型只声明了 6 个。 */
export const SLIDING_WINDOW_KEYS = [
  'credential_id',
  'model',
  'window_minutes',
  'limit',
  'source',
  'total_returned',
  'entries',
  'stats',
] as const

/** ★★ (13) `CallEntry` 的 5 个 json tag（★ 全是缩写，与 Go 字段名不同）。 */
export const CALL_ENTRY_KEYS = ['rid', 'ts', 'ok', 'lat', 'err'] as const

/** 其中唯一带 `omitempty` 的那个（成功条目里键消失）。 */
export const CALL_ENTRY_OPTIONAL_KEYS = ['err'] as const

/** `stats` 的 5 个键（全部恒在）。 */
export const WINDOW_STATS_KEYS = ['total', 'success', 'failed', 'failure_rate', 'error_kinds'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface CallEntry {
  /** ★★ (13) 恒在键，★ 但**可能为空串**（`COALESCE(request_id,'')`）。 */
  rid: string
  /** ★★ (13) unix **毫秒**。 */
  ts: number
  ok: boolean
  /** 毫秒。 */
  lat: number
  /** ★★ (13) 唯一带 `omitempty` 的键 —— 成功条目里**键不存在**。 */
  err?: string
}

export interface WindowStats {
  /** ★★★ (3) 恒等于 `entries.length`。 */
  total: number
  success: number
  failed: number
  /** ★★★ (5) `total > 0` 时 === `failed / total`；`total === 0` 时恒为 0。 */
  failure_rate: number
  /** ★★★ (4) 求和 **≤ `failed`**（失败的条目若 `err` 空串则不进这里）。 */
  error_kinds: Record<string, number>
}

export interface SlidingWindowResponse {
  credential_id: number
  /** ★★ (11) 原样回显，★ **不是**规范化后的实际命中值。 */
  model: string
  window_minutes: number
  /** ★★ (2) 桌面类型漏了这个键。 */
  limit: number
  /** ★★ (6)(7) `request_logs` 那一侧是**二义的**。 */
  source: string
  /** ★★★ (3) 恒等于 `entries.length` 与 `stats.total`。★ 桌面类型漏了这个键。 */
  total_returned: number
  /** ★★ (8) 恒为数组，空时是 `[]` 不是 `null`。 */
  entries: CallEntry[]
  stats: WindowStats
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/credentials/sliding-window`。
 * ★ (9) `minutes` 后端**不校验**；`limit` 越界 400（1–500）。
 * ★ (9) 两者传非数字都会被 `queryInt` **静默回落**成缺省值。
 */
export function fetchSlidingWindow(
  params: { credentialId: number; model: string; minutes?: number; limit?: number },
  options?: RequestOptions,
): Promise<SlidingWindowResponse> {
  const q = new URLSearchParams()
  q.set('credential_id', String(params.credentialId))
  q.set('model', params.model)
  q.set('minutes', String(params.minutes ?? SLIDING_WINDOW_DEFAULT_MINUTES))
  q.set('limit', String(params.limit ?? SLIDING_WINDOW_DEFAULT_LIMIT))
  return req<unknown>('GET', `${SLIDING_WINDOW_PATH}?${q.toString()}`, undefined, options).then(unwrapSlidingWindow)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapSlidingWindow(resp: unknown): SlidingWindowResponse {
  const d = requireObject(resp, '滑窗')
  requireKeys(d, SLIDING_WINDOW_KEYS, '滑窗')
  for (const k of ['credential_id', 'window_minutes', 'limit', 'total_returned'] as const) {
    if (typeof d[k] !== 'number') throw new Error(`滑窗 的 ${k} 不是数字`)
  }
  for (const k of ['model', 'source'] as const) {
    if (typeof d[k] !== 'string') throw new Error(`滑窗 的 ${k} 不是字符串`)
  }
  const entries = d['entries']
  if (!Array.isArray(entries)) throw new Error('滑窗 的 entries 不是数组')
  for (let i = 0; i < entries.length; i++) {
    unwrapCallEntry(entries[i], `滑窗 的 entries[${i}]`)
  }

  const s = requireObject(d['stats'], '滑窗 的 stats')
  requireKeys(s, WINDOW_STATS_KEYS, '滑窗 的 stats')
  for (const k of ['total', 'success', 'failed', 'failure_rate'] as const) {
    if (typeof s[k] !== 'number') throw new Error(`滑窗 的 stats 的 ${k} 不是数字`)
  }
  requireRecord(s['error_kinds'], '滑窗 的 stats 的 error_kinds')
  return d as unknown as SlidingWindowResponse
}

/** ★★ (13) 五个键全用缩写；`err` 带 `omitempty` ⇒ **存在才校验**。 */
export function unwrapCallEntry(v: unknown, where: string): CallEntry {
  const o = requireObject(v, where)
  requireKeys(o, ['rid', 'ts', 'ok', 'lat'], where)
  if (typeof o['rid'] !== 'string') throw new Error(`${where} 的 rid 不是字符串`)
  if (typeof o['ts'] !== 'number') throw new Error(`${where} 的 ts 不是数字`)
  if (typeof o['lat'] !== 'number') throw new Error(`${where} 的 lat 不是数字`)
  if (typeof o['ok'] !== 'boolean') throw new Error(`${where} 的 ok 不是布尔`)
  if ('err' in o && typeof o['err'] !== 'string') throw new Error(`${where} 的 err 不是字符串`)
  return o as unknown as CallEntry
}

function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
}

function requireRecord(v: unknown, where: string): Record<string, number> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error(`${where} 不是对象`)
  const rec = v as Record<string, unknown>
  for (const k of Object.keys(rec)) {
    if (typeof rec[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  return rec as Record<string, number>
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (3) 四个数字互锁 ──

/** ★★★ (3) `total_returned === entries.length`。 */
export function slidingWindowTotalMatchesEntries(r: SlidingWindowResponse): boolean {
  return r.total_returned === r.entries.length
}

/** ★★★ (3) `stats.total === entries.length`（`ComputeStats` 的第一行）。 */
export function slidingWindowStatsTotalMatchesEntries(r: SlidingWindowResponse): boolean {
  return r.stats.total === r.entries.length
}

/** ★★★ (3) `success + failed === total`（`if/else` 是完备二分，不是有重叠的三项和）。 */
export function slidingWindowSuccessPlusFailedEqualsTotal(r: SlidingWindowResponse): boolean {
  return r.stats.success + r.stats.failed === r.stats.total
}

// ── (4) error_kinds 的和 ≤ failed ──

/** `error_kinds` 全部计数之和。 */
export function slidingWindowErrorKindSum(s: WindowStats): number {
  return Object.values(s.error_kinds).reduce((a, b) => a + b, 0)
}

/**
 * ★★★★★ (4) **`sum(error_kinds) ≤ failed`**，而不是相等 ——
 * 失败的条目若 `err` 是空串就**完全不进** `error_kinds`（`recorder.go:166-168`）。
 * ⇒ ⇒ 「失败原因分布」条数少于「失败数」是**正常的**，差值就是没归类的那些。
 */
export function slidingWindowErrorKindsAtMostFailed(s: WindowStats): boolean {
  return slidingWindowErrorKindSum(s) <= s.failed
}

/**
 * ★★★★ (4) 的**反面**：等号成立**当且仅当每个失败条目都有非空 `err`**。
 * ⇒ ⇒ 客户端用它判断「失败原因分布是否完整」。
 */
export function slidingWindowErrorKindsCoverEveryFailure(entries: CallEntry[]): boolean {
  let failed = 0
  let classified = 0
  for (const e of entries) {
    if (e.ok) continue
    failed++
    if (e.err !== undefined && e.err !== '') classified++
  }
  return classified === failed
}

// ── (5) failure_rate 是派生值 ──

/**
 * ★★★★★ (5) `total === 0` 时 `failure_rate` **恒为 0**（后端 `if Total > 0` 才计算），
 * `total > 0` 时恒等于 `failed / total`。
 * ⇒ ★★ 客户端**不能**直接写 `rate === failed / total` —— `total === 0` 时那是 `NaN`。
 */
export function slidingWindowFailureRateMatches(s: WindowStats): boolean {
  if (s.total === 0) return s.failure_rate === 0
  return s.failure_rate === s.failed / s.total
}

// ── (6) source 的语义 ──

/**
 * ★★★★ (6) **`source === 'redis'` ⟹ `entries.length > 0`** ——
 * 因为 `len(entries) == 0` 会触发回退并把 `source` 改成 `request_logs`（`:743-746`）。
 * ⇒ ★★ 这是 `source` 唯一**可以断言**的方向。
 */
export function slidingWindowRedisSourceImpliesEntries(r: SlidingWindowResponse): boolean {
  if (r.source !== 'redis') return true
  return r.entries.length > 0
}

/**
 * ★★★★ (6) 的反面：**`source === 'request_logs'` 不代表「entries 为空」** ——
 * Redis 窗口内无数据、但 request_logs 有数据时，source 也是 `request_logs`。
 * ⇒ ★★★ 客户端**不能**用 `source` 推断「是不是降级失败了」。
 */
export function slidingWindowFallbackSourceStillHasEntries(r: SlidingWindowResponse): boolean {
  if (r.source !== 'request_logs') return false
  return r.entries.length > 0
}

// ── (14) 时间序无 tiebreak ──

/**
 * ★★★★ (14) 回退查询 `ORDER BY ts DESC` **无 tiebreak** ⇒ 同毫秒顺序未定义
 * ⇒ 只能断言**非升序**，**不能**断言严格降序。
 */
export function slidingWindowEntriesAreNonAscending(entries: CallEntry[]): boolean {
  for (let i = 1; i < entries.length; i++) {
    if ((entries[i - 1] as CallEntry).ts < (entries[i] as CallEntry).ts) return false
  }
  return true
}

// ── (9)(10) 查询参数边界 ──

/**
 * ★★★ (9) `limit` 是否落在后端接受的 1–500 内。
 * ★ **`minutes` 刻意没有对应判据** —— 后端**根本不校验它**，
 *   任何 `minutes`（0、负数、超大值）都会被放行并进入 SQL 的 interval 表达式。
 */
export function slidingWindowLimitIsInRange(limit: number): boolean {
  return limit >= SLIDING_WINDOW_LIMIT_MIN && limit <= SLIDING_WINDOW_LIMIT_MAX
}

/** ★★ (10) `credential_id == 0` 才 400（不是 `< 1`）⇒ 负数能过这一关。 */
export function slidingWindowCredentialIdIsRejected(credentialId: number): boolean {
  return credentialId === 0
}
