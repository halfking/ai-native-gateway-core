import { req, type RequestOptions } from './client'
import {
  unwrapCorrectionStat,
  unwrapTaskProfileSuggestion,
  type CorrectionStat,
  type TaskProfileSuggestion,
} from './taskProfile'

/**
 * taskTypeCorrectionStats.ts — 人工修正统计 + 修正明细 + 修正驱动的分层建议（2026-10-08，第九十九批）。
 *
 * GET /api/admin/task-profile/corrections/stats
 *   · `taskprofile/handler.go:250-299`（`handleCorrectionStats`）
 *   · `taskprofile/corrections.go:131-161`（`Stats`）/`:167-196`（`Recent`）
 *   · `taskprofile/suggest.go:45-93`（`Suggest`）/`:96-105`（`escalateTier`）
 *   · `taskprofile/corrections.go:27-39`（`Correction`）/ sql/migrations/startup/724_task_type_corrections.sql
 *
 * - **注册**：`taskprofile/handler.go:131` `mux.HandleFunc("GET /api/admin/task-profile/corrections/stats", wrap(h.handleCorrectionStats))`
 *   ⇒ **第七种注册形态：方法内嵌路由模式**（引号紧贴 `GET `，不是紧贴 `/api`）。
 *   由 `admin/handler.go:1413` 挂载 ⇒ **admin 档** ⇒ 抽屉席不设 `requiresRole`。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 * - **桌面调用方**：`web/src/api/taskProfile.ts:95-100`（`getTaskTypeCorrectionStats(sinceDays = 30)`）——
 *   ★ 它只传 `since_days`，**从不传 `recent_limit`**。
 * - **移动端**：批 98 之前 0 处实际调用（本端点串只在 `taskProfile.ts:14` 的注释里出现过）。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十三件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **★★ 同一个死值 / 活值，在两个端点上完全相反 —— 本批最重要的发现。**
 *     `Suggest` 的三个 `tier_source`（`suggest.go:34-36` 注释）里，
 *     `confidence_escalation` 在**批 98 的 `/api/admin/task-profile`** 上是**死值**
 *     （那里传 `confidence = 1.0`，而 `minConf` 被 cap 到 1 ⇒ `1.0 < minConf` 永不成立）；
 *     ★★★ **在本端点上它是活值** —— `handleCorrectionStats:290` 传的是 `confidence = 0.75`
 *     ⇒ ⇒ 判据式 `confidence < minConf`（`suggest.go:77`）在 `minConf > 0.75` 时**成立**。
 *     ⇒ ⇒ 注册表里 `MinConfidence ∈ {0.65, 0.70, 0.75, 0.80, 0.85}`（`registry.go:39-70`）
 *       ⇒ `devops`(0.80) / `chat`(0.80) / `documentation`(0.85) / `summary`(0.85) 会命中。
 *     ⇒ ⇒ ★★★★ **可达性结论必须绑定到「哪个端点的哪个 confidence」，不能沿用到同族另一个端点。**
 *
 * (2) ★★★★★ **★ 升级过的 `min_confidence` 会把同一个类型推过 0.75 这条线。**
 *     `suggest.go:72-74` 在命中升级规则时 `minConf += 0.05`（cap 1）。
 *     ⇒ `min_confidence = 0.75` 的类型（`coding` / `testing` / `dependency` / `code` / `creative`）
 *       升级后变成 `0.80` ⇒ **`0.75 < 0.80` 成立** ⇒ 命中 `confidence_escalation`。
 *     ⇒ ⇒ ★★ 边界格是 **`0.75 + 0.05 = 0.80` 严格大于 `0.75`**：
 *       而 `0.70 + 0.05 = 0.75` **不成立**（`<` 不是 `<=`）⇒ ★ 这条边界可观测。
 *     ⇒ ⇒ ★★★ 因此本端点上会出现一个「**既被修正升级过、又被置信度覆盖**」的建议：
 *       `min_confidence = 0.80`（带着 +0.05 的痕迹）而 `tier_source = confidence_escalation`
 *       —— **`tier_source` 被覆盖，但 `min_confidence` 保留了升级的证据。**
 *
 * (3) ★★★★★ **`suggest.go:80-82` 的「空 fallback 补 tier-b」分支在本端点上是活的。**
 *     `documentation` / `summary` 的 `FallbackTiers: []string{}`（`registry.go:46-47`）
 *     ⇒ 命中 (1) 的 `confidence_escalation` ⇒ `fallbacks` 被补成 `["tier-b"]`。
 *     ⇒ ★ 在批 98 那个端点上（confidence 1.0）这个分支是死的 —— 又一条「两端点相反」。
 *
 * (4) ★★★★★ **错误体是 `text/plain`（`http.Error`），与批 98 同族但文案不同 —— 本系列第二次。**
 *     `ensurePool` → 503 `Database not available`（`handler.go:437`）；
 *     `Stats` 失败 → 500 `query stats failed`（`:276`）；`Recent` 失败 → 500 `query recent failed`（`:282`）。
 *     ⇒ ★★ `query stats failed` 与批 98 的 `query correction stats failed` **不是同一条**，别串用。
 *
 * (5) ★★★★★ **★★ 400 文案是本系列首次出现的「区间型」**：
 *     `since_days must be an integer in [1,365]`（`:258`）、
 *     `recent_limit must be an integer in [1,500]`（`:267`）。
 *     ⇒ ★★ 与之前所有「定长文案」都不同 ⇒ 文案不可跨端点套用，更不该拿来判档位。
 *
 * (6) ★★★★★ **校验是「解析失败 ∨ 越界」的合取**（`:257` `err != nil || days <= 0 || days > 365`）：
 *     `abc`（解析失败）、`0`、`366`（越界）**三种触发同一条 400** ⇒ 客户端不可区分是哪一种。
 *
 * (7) ★★★★★ **★ `Recent` 内部那个夹取是死代码**（`corrections.go:171-173`
 *     `if limit <= 0 || limit > 500 { limit = 100 }`）——
 *     handler 已经在 `:266-269` 先把越界值 **拒掉了**，`Recent` 永远只收到 `[1,500]` 内的整数。
 *     ⇒ ⇒ 与批 98 的 `confidence_escalation` 死值同型：**兜底分支被上游守卫折叠掉。**
 *
 * (8) ★★★★★ **`since` 是绝对时间戳字符串，非确定性**：`since.UTC().Format(time.RFC3339)`（`:294`）。
 *     ⇒ ★★ 客户端**只能校验格式**（`YYYY-MM-DDTHH:MM:SSZ`，秒级精度、无小数秒），
 *       **不能校验具体值**，也不能断言「缺省和显式 `since_days=30` 结果相同」。
 *
 * (9) ★★★★★ **★★ 缺省 `since_days` 与显式 `since_days=30` 的 `since` 不逐字相等。**
 *     缺省走 `time.Now().Add(-30*24*time.Hour)`（`:254`），显式走 `time.Now().Add(-time.Duration(days)*24*time.Hour)`（`:261`）
 *     ⇒ 两次 `time.Now()` 是**两个不同时刻** ⇒ 相差毫秒，落到 RFC3339 的**秒**上通常相同、跨秒则不同。
 *     ⇒ ⇒ ★★ **语义等价但字节不必相等**：客户端拿它做快照比对会假报失败。
 *
 * (10) ★★★★★ **顶层是手写 `map[string]any`，恒 4 键**（`:293-298`）：
 *      `since` / `stats` / `suggestions` / `recent`。
 *      ⇒ ★★ 桌面把 `suggestions` 标成**可选**（`web/src/api/taskProfile.ts:67` `suggestions?`）是**过度防御**：
 *        键是 map 字面量里的硬编码字符串 ⇒ **恒在**。客户端的可选性判断不能抄桌面。
 *
 * (11) ★★★★★ **三个容器字段全部非 nil**（与批 98 的 `correction_stats` 相反）：
 *      `Stats` 返回 `make(map[string]CorrectionStat)`（`corrections.go:149`）、
 *      handler 里 `suggestions := make(map[string]Suggestion, len(stats))`（`:288`）、
 *      `Recent` 返回 `make([]Correction, 0, limit)`（`corrections.go:186`）
 *      ⇒ ⇒ **零行时是 `{}` / `{}` / `[]`，没有一格是 `null`。**
 *
 * (12) ★★★★★ **`stats` 与 `suggestions` 是同一个键集**（`handler.go:289-291`
 *      `for taskType := range stats { suggestions[taskType] = Suggest(taskType, 0.75, stats) }`）
 *      ⇒ ⇒ 同集合可断言。★ 与 (10)(11) 合起来给出「`suggestions` 是 `stats` 的逐项派生」这一条。
 *
 * (13) ★★★★★ **`Correction` 是 10 键全恒在，其中两键**值可 null**（`corrections.go:28-39`）：
 *      `ClassifierConfidence *float64 \`json:"classifier_confidence"\``、
 *      `Profile *string \`json:"profile"\`` —— ★★ **指针但没有 `omitempty`** ⇒ **键恒在、值可为 `null`**。
 *      建表 `724_task_type_corrections.sql:33-34` 两列可空，且 `Record` 把从
 *      `auto_route_selections_all` 扫出来的 `*float64` / `*string` **原样透传**进 INSERT（`corrections.go:111`）
 *      ⇒ ⇒ **null 在真实数据上完全可达**（分类器没记置信度时就是 null）。
 *      ★ 对照：`Suggestion.CorrectionStats` 是指针**且**有 `omitempty`（`suggest.go:40`）⇒ **缺席**，不是 null。
 *
 * (14) ★★★★★ **★ 升级用的 `+ 0.05` 在 float64 上不总是得到「好看的两位小数」。**
 *      `suggest.go:72` 是 `minConf += escalationMinConfidenceBump`，IEEE 双精度实测：
 *      `0.70 + 0.05 === 0.75` ✓ · `0.75 + 0.05 === 0.80` ✓ · `0.85 + 0.05 === 0.90` ✓
 *      但 ★★ **`0.80 + 0.05 === 0.8500000000000001`（≠ `0.85`）**、
 *      **`0.65 + 0.05 === 0.7000000000000001`（≠ `0.70`）**
 *      ⇒ ⇒ ★★★ **夹具里写 `0.7 + 0.05` 这样的表达式，绝不能写四舍五入后的字面量** ——
 *        严格 `===` 会假报失败，而 Go 端算出来的就是那个「长尾」值。
 *      ⇒ ★★ 这也把 (2) 的边界钉死了：`0.75 < (0.70 + 0.05)` 是 **false**（正好等于 0.75），
 *        而 `0.75 < (0.75 + 0.05)` 是 **true** ⇒ 边界在 base 0.70 与 0.75 之间，不是「≤ 都是」。
 *
 * ★★ 另注：`recent` 的 `ORDER BY created_at DESC`（`corrections.go:178`）只保证**非递增**，
 *   **不保证严格递减** —— 同一事务内的两行 `created_at` 可以相等 ⇒ 判据用 `≥` 而不是 `>`。
 *
 * ★★ 另注：`writeJSON` 用 `json.NewEncoder(w).Encode(payload)`（`handler.go:443-446`）
 *   ⇒ **响应体末尾多一个 `\n`**。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验顶层 4 键、`since` 的字符串类型、`stats`/`suggestions` 的对象类型与其逐项值、
 *   `recent` 的数组类型与其逐项 10 键；为 (1)(2)(3)(5)(6)(7)(8)(9)(12) 提供判据或决策函数。
 *   ★ **不校验** `tier_source` 的枚举取值（(1) 已证明三个值都可达）。
 *   ★ **不提供** 「非 GET 由 handler 回 405」相关的判据（是 `ServeMux` 自己回的，见 (1) 的注册形态）。
 */

// ── 常量 ─────────────────────────────────────────────────────────────────────

export const TASK_TYPE_CORRECTION_STATS_PATH = '/api/admin/task-profile/corrections/stats'

/** ★★ (10) 顶层手写 map 的 4 个键 —— 全部恒在（桌面把 `suggestions` 标成可选是过度防御）。 */
export const CORRECTION_STATS_ENVELOPE_KEYS = ['since', 'stats', 'suggestions', 'recent'] as const

/** ★★ (13) `Correction` 的 10 个键，**全部恒在**（两个指针字段也没有 `omitempty`）。 */
export const TASK_TYPE_CORRECTION_KEYS = [
  'id',
  'request_id',
  'auto_task_type',
  'human_task_type',
  'agrees',
  'classifier_confidence',
  'profile',
  'annotator',
  'reason',
  'created_at',
] as const

/** ★★ (13) 这两个键**值可为 `null`** —— 键本身恒在。 */
export const TASK_TYPE_CORRECTION_NULLABLE_KEYS = ['classifier_confidence', 'profile'] as const

/** ★★ (1) `handleCorrectionStats:290` 传给 `Suggest` 的 `confidence` —— 本端点的关键常量。 */
export const CORRECTION_STATS_SUGGEST_CONFIDENCE = 0.75

/** ★★ (5) 两个查询参数的合法区间（`handler.go:258 :267` 的文案里写死）。 */
export const CORRECTION_STATS_SINCE_DAYS_MIN = 1
export const CORRECTION_STATS_SINCE_DAYS_MAX = 365
export const CORRECTION_STATS_RECENT_LIMIT_MIN = 1
export const CORRECTION_STATS_RECENT_LIMIT_MAX = 500

/** ★★ 缺省值（`handler.go:254 :263`）。 */
export const CORRECTION_STATS_DEFAULT_SINCE_DAYS = 30
export const CORRECTION_STATS_DEFAULT_RECENT_LIMIT = 100

/** ★★ (8) `time.RFC3339` 在 `.UTC()` 之后的样子：秒级精度、固定 `Z`、无小数秒。 */
export const CORRECTION_STATS_SINCE_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/

// ── 类型 ─────────────────────────────────────────────────────────────────────

/**
 * ★★ (13) 一条人工修正记录 —— `corrections.go:28-39` 的 10 键扁平结构。
 * ★★ `classifier_confidence` 与 `profile` 是**键恒在、值可 `null`**。
 */
export interface TaskTypeCorrection {
  id: number
  request_id: string
  auto_task_type: string
  human_task_type: string
  agrees: boolean
  /** ★★ (13) 分类器没记置信度时为 `null`（不是缺席）。 */
  classifier_confidence: number | null
  /** ★★ (13) 同上。 */
  profile: string | null
  annotator: string
  reason: string
  created_at: string
}

export interface CorrectionStatsResponse {
  /** ★★ (8) `since.UTC().Format(time.RFC3339)` ⇒ `YYYY-MM-DDTHH:MM:SSZ`，**非确定性**。 */
  since: string
  /** ★★ (11) `make(map[string]CorrectionStat)` ⇒ 零行是 `{}` 不是 `null`。 */
  stats: Record<string, CorrectionStat>
  /** ★★ (10)(12) 恒在；键集 === `stats` 的键集。 */
  suggestions: Record<string, TaskProfileSuggestion>
  /** ★★ (11) `make([]Correction, 0, limit)` ⇒ 零行是 `[]` 不是 `null`。 */
  recent: TaskTypeCorrection[]
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/admin/task-profile/corrections/stats`。
 * ★★ 错误体是 `text/plain`（`http.Error`），**不要**跑 JSON 错误解包器。
 *   · 503 `Database not available`（`ensurePool`，`handler.go:437`）
 *   · 500 `query stats failed`（`:276`）· 500 `query recent failed`（`:282`）
 *   · 400 `since_days must be an integer in [1,365]`（`:258`）
 *   · 400 `recent_limit must be an integer in [1,500]`（`:267`）
 * ★ 非 GET ⇒ **`ServeMux` 自己回 405**（`"GET /api/admin/task-profile/corrections/stats"` 模式），
 *   带 `Allow: GET`，体是 Go 标准库的 `Method Not Allowed`，**不是** handler 里的任何文案。
 */
export function fetchTaskTypeCorrectionStats(
  options?: RequestOptions,
  params?: { sinceDays?: number; recentLimit?: number },
): Promise<CorrectionStatsResponse> {
  const path = buildCorrectionStatsPath(params)
  return req<unknown>('GET', path, undefined, options).then((r) => unwrapCorrectionStatsResponse(r, '修正统计'))
}

/**
 * 拼查询串。`since_days` / `recent_limit` **只在显式给出时才拼**
 * —— ★ 桌面 `web/src/api/taskProfile.ts:95-100` 只传 `since_days`，
 *   而**缺省与显式 `since_days=30` 在后端走的是两条 `time.Now()`**（见 (9)），
 *   所以「不传」与「传 30」**不是**同一个请求。
 */
export function buildCorrectionStatsPath(params?: { sinceDays?: number; recentLimit?: number }): string {
  const parts: string[] = []
  if (params?.sinceDays !== undefined) parts.push(`since_days=${params.sinceDays}`)
  if (params?.recentLimit !== undefined) parts.push(`recent_limit=${params.recentLimit}`)
  if (parts.length === 0) return TASK_TYPE_CORRECTION_STATS_PATH
  return `${TASK_TYPE_CORRECTION_STATS_PATH}?${parts.join('&')}`
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

/** ★★ (10) 4 个键全部必查 —— 尤其 `suggestions`（桌面误标成可选）。 */
export function unwrapCorrectionStatsResponse(v: unknown, where: string): CorrectionStatsResponse {
  const d = requireObject(v, where)
  requireKeys(d, CORRECTION_STATS_ENVELOPE_KEYS, where)
  if (typeof d['since'] !== 'string') throw new Error(`${where} 的 since 不是字符串`)
  const stats = d['stats']
  if (!stats || typeof stats !== 'object' || Array.isArray(stats)) {
    throw new Error(`${where} 的 stats 不是对象`)
  }
  for (const k of Object.keys(stats)) {
    unwrapCorrectionStat((stats as Record<string, unknown>)[k], `${where} 的 stats[${k}]`)
  }
  const suggestions = d['suggestions']
  if (!suggestions || typeof suggestions !== 'object' || Array.isArray(suggestions)) {
    throw new Error(`${where} 的 suggestions 不是对象`)
  }
  for (const k of Object.keys(suggestions)) {
    unwrapTaskProfileSuggestion((suggestions as Record<string, unknown>)[k], `${where} 的 suggestions[${k}]`)
  }
  const recent = d['recent']
  if (!Array.isArray(recent)) throw new Error(`${where} 的 recent 不是数组`)
  for (let i = 0; i < recent.length; i++) {
    unwrapTaskTypeCorrection(recent[i], `${where} 的 recent[${i}]`)
  }
  return d as unknown as CorrectionStatsResponse
}

/**
 * ★★ (13) 10 个键全部必查；其中 `classifier_confidence` 与 `profile`
 * **接受 `number | string | null`，但拒绝 `undefined`**（缺键由 `requireKeys` 先拦）。
 */
export function unwrapTaskTypeCorrection(v: unknown, where: string): TaskTypeCorrection {
  const o = requireObject(v, where)
  requireKeys(o, TASK_TYPE_CORRECTION_KEYS, where)
  if (typeof o['id'] !== 'number') throw new Error(`${where} 的 id 不是数字`)
  for (const k of ['request_id', 'auto_task_type', 'human_task_type', 'annotator', 'reason', 'created_at'] as const) {
    if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if (typeof o['agrees'] !== 'boolean') throw new Error(`${where} 的 agrees 不是布尔`)
  const cc = o['classifier_confidence']
  if (cc !== null && typeof cc !== 'number') throw new Error(`${where} 的 classifier_confidence 不是数字也不是 null`)
  const pf = o['profile']
  if (pf !== null && typeof pf !== 'string') throw new Error(`${where} 的 profile 不是字符串也不是 null`)
  return o as unknown as TaskTypeCorrection
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

// ── (5)(6) 查询参数 ──

/**
 * ★★ (5)(6) 镜像 handler 的 400 判定（`:257` 与 `:266`）：
 * `err != nil || v <= 0 || v > 上界`。
 * ⇒ ★★ **`0` 与 `366`（`since_days`）与 `abc` 触发的是同一条 400**，
 *   客户端**不能**靠状态码区分是哪一种 —— 本函数只回答「会不会被拒」。
 */
export function correctionStatsQueryIsValid(params?: {
  sinceDays?: number
  recentLimit?: number
}): boolean {
  if (params === undefined) return true
  if (params.sinceDays !== undefined) {
    if (!Number.isInteger(params.sinceDays)) return false
    if (params.sinceDays < CORRECTION_STATS_SINCE_DAYS_MIN) return false
    if (params.sinceDays > CORRECTION_STATS_SINCE_DAYS_MAX) return false
  }
  if (params.recentLimit !== undefined) {
    if (!Number.isInteger(params.recentLimit)) return false
    if (params.recentLimit < CORRECTION_STATS_RECENT_LIMIT_MIN) return false
    if (params.recentLimit > CORRECTION_STATS_RECENT_LIMIT_MAX) return false
  }
  return true
}

// ── (8)(9) since ──

/** ★★ (8) `since` 是否是 `.UTC().Format(time.RFC3339)` 的形状（秒级、固定 `Z`）。 */
export function sinceMatchesRfc3339Utc(since: string): boolean {
  return CORRECTION_STATS_SINCE_PATTERN.test(since)
}

/**
 * ★★ (9) **缺省 `since_days` 与显式 `since_days=30` 的 `since` 不保证逐字相等。**
 * 两条路径各自调用 `time.Now()`（`handler.go:254` 与 `:261`）⇒ 相隔若干毫秒 ⇒
 * 落在秒级格式上「通常相同、跨秒则不同」。
 * ⇒ ⇒ ★★ 把它当**快照比对**的基线会假报失败；本函数是给「两次请求的 since 是否真的相同」用的
 * 客户端决策助手，不参与响应合法性判定。
 */
export function sinceEquals(a: string, b: string): boolean {
  return a === b
}

// ── (12) 同集合 ──

/**
 * ★★★★★ (12) **`suggestions` 的键集 === `stats` 的键集。**
 * handler 是同一个 `for taskType := range stats` 循环（`handler.go:289-291`）
 * ⇒ ⇒ 两个 map 的键**逐个相同**，个数也相同。
 * ★ 同为**客户端一致性自检**而非响应校验（对真实响应恒成立）——
 *   它防的是网关版本漂移或中间层改 body。
 */
export function suggestionKeysMatchStats(r: CorrectionStatsResponse): boolean {
  const a = Object.keys(r.stats)
  const b = Object.keys(r.suggestions)
  if (a.length !== b.length) return false
  for (const k of a) {
    if (!(k in r.suggestions)) return false
  }
  return true
}

// ── (1)(2)(3) 置信度升级 ──

/**
 * ★★★★★ (1) `suggest.go:77` 的客户端镜像：`confidence < minConf` ⇒ `tier = tier-a`
 * 且 `tier_source = confidence_escalation`。
 * ⇒ ★★ **`<` 不是 `<=`** ⇒ `minConf === confidence` 时**不**命中；
 *   `minConf = 0.75`、`confidence = 0.75` 就是这条边界。
 */
export function isConfidenceEscalated(confidence: number, minConf: number): boolean {
  return confidence < minConf
}

/** ★★ (3) `suggest.go:80-82`：命中置信度升级且 `fallbacks` 为空 ⇒ 补成 `['tier-b']`。 */
export function confidenceEscalationFallbacks(fallbacks: readonly string[]): string[] {
  return fallbacks.length === 0 ? ['tier-b'] : [...fallbacks]
}

/**
 * ★★★★★★ (1)(2)(3) **`Suggest(taskType, 0.75, stats)` 的完整客户端镜像。**
 *
 * 这是本批最要紧的判据，因为它**三个 `tier_source` 分支全都覆盖到**：
 * 1. 修正升级（`suggest.go:69-75`）：`total ≥ 5` 且 `correction_rate ≥ 0.30`
 *    ⇒ `tier = escalatedTier(preferred_tier)`、`tier_source = correction_escalation`、
 *      `min_confidence += 0.05`（cap 1）。
 * 2. 置信度升级（`suggest.go:77-83`）：`0.75 < min_confidence`（**升级后的值**）
 *    ⇒ `tier = tier-a`、`tier_source = confidence_escalation`、
 *      空 `fallback_tiers` 被补成 `['tier-b']`。
 * 3. ★★ **第 2 步会覆盖第 1 步写下的 `tier_source`，但 `min_confidence` 保留 +0.05 的痕迹**
 *    —— 这正是 (2) 说的「既被修正升级、又被告Confidence 覆盖」那一格。
 *
 * ★ 同为**客户端一致性自检**而非响应校验。
 */
export function suggestionMatchesConfidence075(
  profile: {
    task_type: string
    preferred_tier: string
    fallback_tiers: string[]
    min_confidence: number
  },
  stat: CorrectionStat | undefined,
  actual: TaskProfileSuggestion,
): boolean {
  let tier = profile.preferred_tier
  let fallbacks = [...profile.fallback_tiers]
  let minConf = profile.min_confidence
  let source = 'registry'

  if (stat !== undefined && stat.total >= 5 && stat.correction_rate >= 0.3) {
    tier = escalatedTierAt(tier)
    source = 'correction_escalation'
    minConf = Math.min(minConf + 0.05, 1)
  }

  if (isConfidenceEscalated(CORRECTION_STATS_SUGGEST_CONFIDENCE, minConf)) {
    tier = 'tier-a'
    source = 'confidence_escalation'
    fallbacks = confidenceEscalationFallbacks(fallbacks)
  }

  return (
    actual.tier === tier &&
    actual.tier_source === source &&
    actual.min_confidence === minConf &&
    actual.fallback_tiers.length === fallbacks.length &&
    actual.fallback_tiers.every((t, i) => t === fallbacks[i])
  )
}

/** ★★ `escalateTier`（`suggest.go:96-105`）：`c→b`、`b→a`、**其余（含 `tier-a`）→ `a`**。 */
export function escalatedTierAt(tier: string): string {
  if (tier === 'tier-c') return 'tier-b'
  if (tier === 'tier-b') return 'tier-a'
  return 'tier-a'
}

// ── recent 排序 ──

/**
 * ★★ `recent` 的 `ORDER BY created_at DESC`（`corrections.go:178`）只保证**非递增**。
 * ⇒ ★★ **同一事务里的两行 `created_at` 可以相等** ⇒ 判据用 `≥` 而不是 `>`，
 *   断言「严格递减」会在有并列时假报失败。
 */
export function recentIsNonIncreasing(r: CorrectionStatsResponse): boolean {
  for (let i = 1; i < r.recent.length; i++) {
    const prev = (r.recent[i - 1] as TaskTypeCorrection).created_at
    const cur = (r.recent[i] as TaskTypeCorrection).created_at
    if (!(prev >= cur)) return false
  }
  return true
}

// ── (11)(12) 容器非 nil ──

/**
 * ★★ (11) 三个容器字段都**不是** `null` —— 后端一律 `make` 过
 * （`corrections.go:149 :186` · `handler.go:288`）。
 * ⇒ ⇒ 零行响应是 `{}` / `{}` / `[]`，不是 `null` / `null` / `null`。
 */
export function correctionStatsContainersAreNotNull(r: CorrectionStatsResponse): boolean {
  return r.stats !== null && r.suggestions !== null && r.recent !== null
}