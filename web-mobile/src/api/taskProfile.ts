import { req, type RequestOptions } from './client'

/**
 * taskProfile.ts — 任务类型档案 + 人工修正反馈闭环（2026-10-08，第九十八批）。
 *
 * GET /api/admin/task-profile
 *
 * - **注册**：**第七种注册形态 —— Go 1.22 方法内嵌路由模式 + 另一个包**，
 *   `taskprofile/handler.go:128-136`
 *   ```go
 *   func (h *Handlers) RegisterTaskProfileRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
 *       mux.HandleFunc("GET  /api/admin/task-profile",                    wrap(h.handleProfile))
 *       mux.HandleFunc("POST /api/admin/task-profile/corrections",        wrap(h.handleCreateCorrection))
 *       mux.HandleFunc("GET  /api/admin/task-profile/corrections/stats",  wrap(h.handleCorrectionStats))
 *       …
 *   }
 *   ```
 *   由 `admin/handler.go:1413` 现构造后挂载：
 *   `taskProfileHandlers.RegisterTaskProfileRoutes(mux, admin)` ⇒ ★★ **admin 档**
 *   ⇒ 抽屉席不设 `requiresRole`。
 * - ★★★ **判「端点是否存在」的清单必须加第 7 条**：
 *   **方法内嵌在路由模式里** ⇒ `grep '"/api/admin/task-profile"'`（引号紧贴路径）
 *   **只能命中测试文件**，真正的注册是 `"GET /api/admin/task-profile"`。
 *   ⇒ ★★ 且因为方法由 `ServeMux` 自己匹配，**非 GET 是 mux 直接回 405**
 *   （Go 的标准行为，带 `Allow` 头、`text/plain` 体），**不是 handler 里的 `writeError`**。
 * - **实现**：`taskprofile/handler.go:141-180`（`handleProfile`）· `:155-159`（`profileView`）
 *   · `:435-441`（`ensurePool`）· `taskprofile/suggest.go:45-92`（`Suggest`）
 *   · `taskprofile/corrections.go:131-163`（`Stats`）· `taskprofile/registry.go:98-106`（`Snapshot`）/`:128-136`（`TaskTypes`）。
 * - **桌面调用方**：`web/src/api/taskProfile.ts:91` —— 直接强转，不做校验。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十二件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **★★ 错误体是 `text/plain`（`http.Error`），不是 JSON —— 本系列首次。**
 *     `ensurePool` 用 `http.Error(w, "Database not available", 503)`（`:437`），
 *     查询失败用 `http.Error(w, "query correction stats failed", 500)`（`:151`），
 *     请求体解析失败也是 `http.Error(w, "invalid JSON body: "+err.Error(), 400)`（`:190` 侧）。
 *     ⇒ ⇒ ★★ **对这个端点调用 JSON 错误解包器一定失败**；客户端只能读状态码 +
 *       （若需要）`text` 兜底。与批 93–97 的 `{"error":{"detail":…}}` 是**两套错误契约**。
 *
 * (2) ★★★★ **503 文案是本系列的第三种，而且首字母大写**：
 *     `Database not available`（本端点） ←→ `db not available`（§11.133 session-analytics）
 *     ←→ `database not configured`（§11.132 turns） ⇒ ★★ **文案不可跨端点套用，也不该用来判档位。**
 *
 * (3) ★★★★★ **顶层是手写的 `map[string]any`，恒 4 个键**（`handler.go:174-179`）：
 *     ```go
 *     writeJSON(w, map[string]any{
 *         "registry_version": version, "schema_version": SchemaVersion,
 *         "task_types": TaskTypes(),  "profiles": views,
 *     })
 *     ```
 *     ⇒ ★ `schema_version` 是**编译期常量** `SchemaVersion = 1`（`registry.go:25`）⇒ 值域单点。
 *
 * (4) ★★★★★ **`profiles` 被强制非 nil**（`:160` `make([]profileView, 0, len(profiles))`）
 *     ⇒ ⇒ **空档案集是 `[]` 不是 `null`** —— 与 §11.133 那个「零行即 `null`」正好相反。
 *
 * (5) ★★★★★ **★ `task_types` 与 `profiles[].task_type` 是同一个集合，且两者都升序。**
 *     `TaskTypes()` 取 `registry.Load().profiles` 的键后 `sort.Strings`（`:128-136`）；
 *     `Snapshot()` 取同一张表的值后 `sort.Slice(… TaskType < TaskType)`（`:98-106`）
 *     ⇒ ⇒ ★★★ **顺序在这个响应里是可观测的**（对照 §11.132 的 `last_at` 不可观测），
 *       且 `task_types[i] === profiles[i].task_type` 对每个 `i` 成立。
 *
 * (6) ★★★★★ **`profileView` 是 Go 匿名内嵌 struct ⇒ JSON 是扁平的**：
 *     ```go
 *     type profileView struct {
 *         TaskProfile                                        // 5 个键被摊平
 *         CorrectionStats *CorrectionStat `json:"correction_stats,omitempty"`
 *         Suggestion                       `json:"suggestion"`
 *     }
 *     ```
 *     ⇒ ⇒ 顶层每个 profile 对象是 **5 + 1 + 1 = 最多 7 个键**，而不是嵌套一层。
 *
 * (7) ★★★★★ **★ 同一个键 `correction_stats` 在两个层级各出现一次**：
 *     profile 级（`handler.go:157`）与 `suggestion` 级（`suggest.go:40`）。
 *     两者**出现条件看起来不同**（profile 级只要 map 里有键；suggestion 级还要 `Total > 0`），
 *     ⇒ ★★★ 但 `Stats` 的 SQL 是 `GROUP BY auto_task_type` 配 `COUNT(*)`
 *       （`corrections.go:137-142`）⇒ **每个分组 `Total ≥ 1`** ⇒ **零值行不可达**
 *       ⇒ ⇒ **两种出现条件在真实数据上恒等价** ⇒ **不提供判据，契约由注释承担**
 *       （`Suggest` 里那个 `stat.Total > 0` 守卫在真实数据上是冗余的）。
 *
 * (8) ★★★★★ **★★ `suggestion` 是在 `confidence = 1.0` 下算出来的**（`handler.go:170`
 *     `Suggest(p.TaskType, 1.0, stats)`），而 `minConf` 在升级分支里被 **cap 到 1**（`suggest.go:72-73`）
 *     ⇒ ⇒ `if confidence < minConf`（`:77`）要求 `minConf > 1.0` ⇒ **永不成立**
 *     ⇒ ⇒ ★★★ **`tier_source` 的 `confidence_escalation` 在本响应里是死值** ——
 *       而 `suggest.go:34-36` 的注释把它列为三个合法值之一。
 *       ⇒ **可达值只有 `registry` 与 `correction_escalation`。**
 *       （本系列第三次「注释与代码矛盾」：§11.132 的「近 30 天」、
 *         §11.131 的 `enabled_source`、`task_quality_score` 0–1。）
 *
 * (9) ★★★★★ **升级规则完全可观测**：
 *     `if cs != nil && cs.Total >= correctionMinSamples && cs.CorrectionRate >= correctionEscalationRate`
 *     （`suggest.go:69`，常量 `:20 :22 :24` = `0.30` / `5` / `+0.05`）
 *     ⇒ ⇒ 命中时 `tier = escalateTier(profile.preferred_tier)`、
 *       `tier_source = "correction_escalation"`、`min_confidence += 0.05`（cap 1）；
 *       未命中时 `tier === preferred_tier` 且 `tier_source === "registry"`。
 *
 * (10) ★★★★★ **`escalateTier` 是一张三级表，且 `tier-a` 原地不动**（`suggest.go:96-105`）：
 *      `c→b`、`b→a`、**`default→a`** ⇒ ⇒ 已在 `tier-a` 的类型即使满足升级条件也**留在 `tier-a`**，
 *        但 `tier_source` **仍会变成 `correction_escalation`** ⇒ ★★ **「tier 变了」与「source 变了」不是一回事。**
 *
 * (11) ★★★★★ **`agrees + corrected === total`**：`Stats` 的 SQL 是
 *      `SUM(CASE WHEN agrees THEN 1 ELSE 0 END)` 与 `SUM(CASE WHEN agrees THEN 0 ELSE 1 END)`
 *      （`corrections.go:138-139`）—— **完备二分** ⇒ 每行恰好给两者之一 +1
 *      ⇒ ⇒ 且 `correction_rate = corrected / total`（`:158-160`，`Total ≥ 1` 见 (7)）。
 *
 * (12) ★★★★ **未知任务类型有一条自造档案的分支**（`suggest.go:48-57`）：
 *      不在 registry 里时用 `tier-b` + `[tier-a, tier-c]` + `min_confidence 0.70`
 *      ⇒ ⇒ ★★ `suggestion.task_type` 对任何输入都非空，**但 `description` 只在已知类型上非空**
 *      （自造档案**没有** `Description`）⇒ ⇒ **空 `description` 是「未知类型」的信号。**
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验顶层 4 键、每个 profile 的 6 个恒在键与类型、4 个 `correction_stats` 的
 *   「存在才校验」分支、`suggestion` 的 6 个恒在键；为 (5)(9)(10)(11) 提供判据或决策函数。
 *   ★ **不校验** `status` 类枚举（这里只有 `tier_source`，且死值已由 (8) 排除）；
 *   ★ **不提供** 两层 `correction_stats` 出现条件的差异判据（(7) 恒真）；
 *   ★ **不提供** 「非 GET 由 handler 回 405」相关的任何判据（它根本不是 handler 回的，见 (1)）。
 */
export const TASK_PROFILE_PATH = '/api/admin/task-profile'

/** ★★ (3) 顶层手写 map 的 4 个键。 */
export const TASK_PROFILE_ENVELOPE_KEYS = ['registry_version', 'schema_version', 'task_types', 'profiles'] as const

/** ★★ (6) `profileView` 里被摊平的 5 个 profile 键 + 必有的 `suggestion`。 */
export const TASK_PROFILE_ENTRY_KEYS = [
  'task_type',
  'description',
  'preferred_tier',
  'fallback_tiers',
  'min_confidence',
  'suggestion',
] as const

/** ★★ (6)(7) profile 级那个带 `omitempty` 的键。 */
export const TASK_PROFILE_ENTRY_OPTIONAL_KEYS = ['correction_stats'] as const

/** ★★ `Suggestion` 的 6 个键。 */
export const TASK_PROFILE_SUGGESTION_KEYS = [
  'task_type',
  'tier',
  'fallback_tiers',
  'min_confidence',
  'tier_source',
] as const

/** ★★ `Suggestion` 自己也有一个同名的 `correction_stats`（`:40`）。 */
export const TASK_PROFILE_SUGGESTION_OPTIONAL_KEYS = ['correction_stats'] as const

/** `CorrectionStat` 的 5 个键（全部恒在，`suggest.go`/`types.go` 无 omitempty）。 */
export const CORRECTION_STAT_KEYS = ['task_type', 'total', 'agrees', 'corrected', 'correction_rate'] as const

/** ★★ (10) 三档 tier（`types.go:24-26`）。 */
export const TASK_PROFILE_TIERS = ['tier-a', 'tier-b', 'tier-c'] as const

/** ★★ (9) 升级阈值，抄自 `suggest.go:20 :22 :24`。 */
export const TASK_PROFILE_CORRECTION_ESCALATION_RATE = 0.3
export const TASK_PROFILE_CORRECTION_MIN_SAMPLES = 5
export const TASK_PROFILE_ESCALATION_MIN_CONFIDENCE_BUMP = 0.05

/** ★★ (3) `SchemaVersion = 1`（`registry.go:25`），编译期常量。 */
export const TASK_PROFILE_SCHEMA_VERSION = 1

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface CorrectionStat {
  task_type: string
  total: number
  agrees: number
  corrected: number
  /** ★★ (11) `total ≥ 1` 恒成立 ⇒ 恒等于 `corrected / total`，客户端不必处理除零。 */
  correction_rate: number
}

export interface TaskProfileSuggestion {
  task_type: string
  /** ★★ (9) 命中升级规则时是 `escalateTier(preferred_tier)`，否则等于 `preferred_tier`。 */
  tier: string
  /** ★★ 与顶层 `fallback_tiers` 不是同一份数据；详见文件头 (6) 与 `Suggestion` 的构造。 */
  fallback_tiers: string[]
  min_confidence: number
  /** ★★★ (8) **可达值只有 `registry` 与 `correction_escalation`** —— `confidence_escalation` 是死值。 */
  tier_source: string
  /** ★★ (7) 与 profile 级那个**同名但不同层**；真实数据上两者同时出现或同时缺席。 */
  correction_stats?: CorrectionStat
}

export interface TaskProfileEntry {
  task_type: string
  /** ★★ (12) **未知任务类型时是空串** —— 那是「该类型不在 registry 里」的信号。 */
  description: string
  preferred_tier: string
  fallback_tiers: string[]
  min_confidence: number
  suggestion: TaskProfileSuggestion
  /** ★★ (7) 有该任务类型的修正记录时才出现。 */
  correction_stats?: CorrectionStat
}

export interface TaskProfileResponse {
  registry_version: string
  /** ★★ (3) 恒为 1（编译期常量）。 */
  schema_version: number
  /** ★★ (5) **升序**，且与 `profiles[].task_type` 逐项相同。 */
  task_types: string[]
  /** ★★ (4) 恒为数组（`make(…, 0, …)`），空档案集是 `[]` 不是 `null`。 */
  profiles: TaskProfileEntry[]
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/admin/task-profile`。
 * ★★ 错误体是 **`text/plain`**（`http.Error`），不是 `{"error":{"detail":…}}`
 * —— ★ **不要**对这个端点跑 JSON 错误解包器。
 * ★ `Database not available` ⇒ **503**（`ensurePool`：`store.Pool() == nil`）。
 * ★ 查询修正统计失败 ⇒ **500 `query correction stats failed`**（`handleProfile:151`）。
 * ★ 非 GET ⇒ **mux 自己回 405**（`"GET /api/admin/task-profile"` 模式），带 `Allow: GET`，
 *   体是 Go 标准库的 `Method Not Allowed`，**不是** handler 里的 JSON 错误体。
 */
export function fetchTaskProfile(options?: RequestOptions): Promise<TaskProfileResponse> {
  return req<unknown>('GET', TASK_PROFILE_PATH, undefined, options).then(unwrapTaskProfile)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapTaskProfile(resp: unknown): TaskProfileResponse {
  const d = requireObject(resp, '任务档案')
  requireKeys(d, TASK_PROFILE_ENVELOPE_KEYS, '任务档案')
  if (typeof d['registry_version'] !== 'string') throw new Error('任务档案 的 registry_version 不是字符串')
  if (typeof d['schema_version'] !== 'number') throw new Error('任务档案 的 schema_version 不是数字')
  const taskTypes = d['task_types']
  if (!Array.isArray(taskTypes)) throw new Error('任务档案 的 task_types 不是数组')
  for (let i = 0; i < taskTypes.length; i++) {
    if (typeof taskTypes[i] !== 'string') throw new Error(`任务档案 的 task_types 第 ${i} 项不是字符串`)
  }
  const profiles = d['profiles']
  if (!Array.isArray(profiles)) throw new Error('任务档案 的 profiles 不是数组')
  for (let i = 0; i < profiles.length; i++) {
    unwrapTaskProfileEntry(profiles[i], `任务档案 的 profiles[${i}]`)
  }
  return d as unknown as TaskProfileResponse
}

/** ★★ (6) 6 个恒在键必查；profile 级 `correction_stats` **存在才校验**。 */
export function unwrapTaskProfileEntry(v: unknown, where: string): TaskProfileEntry {
  const o = requireObject(v, where)
  requireKeys(o, TASK_PROFILE_ENTRY_KEYS, where)
  for (const k of ['task_type', 'description', 'preferred_tier'] as const) {
    if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  for (const k of ['fallback_tiers'] as const) {
    if (!Array.isArray(o[k])) throw new Error(`${where} 的 ${k} 不是数组`)
    for (let i = 0; i < (o[k] as unknown[]).length; i++) {
      if (typeof (o[k] as unknown[])[i] !== 'string') throw new Error(`${where} 的 ${k} 第 ${i} 项不是字符串`)
    }
  }
  for (const k of ['min_confidence'] as const) {
    if (typeof o[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  if ('correction_stats' in o) unwrapCorrectionStat(o['correction_stats'], `${where} 的 correction_stats`)
  unwrapTaskProfileSuggestion(o['suggestion'], `${where} 的 suggestion`)
  return o as unknown as TaskProfileEntry
}

/** ★★ `Suggestion` 的 5 个恒在键必查；它自己的 `correction_stats` **存在才校验**。 */
export function unwrapTaskProfileSuggestion(v: unknown, where: string): TaskProfileSuggestion {
  const o = requireObject(v, where)
  requireKeys(o, TASK_PROFILE_SUGGESTION_KEYS, where)
  for (const k of ['task_type', 'tier', 'tier_source'] as const) {
    if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  if (!Array.isArray(o['fallback_tiers'])) throw new Error(`${where} 的 fallback_tiers 不是数组`)
  for (let i = 0; i < (o['fallback_tiers'] as unknown[]).length; i++) {
    if (typeof (o['fallback_tiers'] as unknown[])[i] !== 'string') {
      throw new Error(`${where} 的 fallback_tiers 第 ${i} 项不是字符串`)
    }
  }
  if (typeof o['min_confidence'] !== 'number') throw new Error(`${where} 的 min_confidence 不是数字`)
  if ('correction_stats' in o) {
    unwrapCorrectionStat(o['correction_stats'], `${where} 的 correction_stats`)
  }
  return o as unknown as TaskProfileSuggestion
}

/** ★★ `CorrectionStat` 的 5 个键全部恒在。 */
export function unwrapCorrectionStat(v: unknown, where: string): CorrectionStat {
  const o = requireObject(v, where)
  requireKeys(o, CORRECTION_STAT_KEYS, where)
  if (typeof o['task_type'] !== 'string') throw new Error(`${where} 的 task_type 不是字符串`)
  for (const k of ['total', 'agrees', 'corrected', 'correction_rate'] as const) {
    if (typeof o[k] !== 'number') throw new Error(`${where} 的 ${k} 不是数字`)
  }
  return o as unknown as CorrectionStat
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

// ── (11) 修正统计的自洽 ──

/**
 * ★★★★★ (11) **`agrees + corrected === total`** ——
 * `Stats` 的 SQL 用一对 `CASE WHEN agrees THEN 1 ELSE 0 / 0 ELSE 1`（**完备二分**）
 * ⇒ 每行恰好给两者之一 +1 ⇒ 三个数字锁死。
 */
export function correctionStatSplitsSumToTotal(s: CorrectionStat): boolean {
  return s.agrees + s.corrected === s.total
}

/**
 * ★★★★ (11) **`correction_rate === corrected / total`** ——
 * ★ `Stats` 里是 `if stat.Total > 0 { … }` 守卫，但 `GROUP BY` + `COUNT(*)` 让 `Total ≥ 1` 恒成立
 * （见文件头 (7)）⇒ ⇒ 客户端**不需要**写 `total === 0 ? rate : …` 那种分支。
 */
export function correctionRateMatchesStat(s: CorrectionStat): boolean {
  return s.correction_rate === s.corrected / s.total
}

// ── (10) tier 升级表 ──

/**
 * ★★★★★ (10) `escalateTier` 的客户端镜像（`suggest.go:96-105`）：
 * `tier-c → tier-b`、`tier-b → tier-a`、**其余（含 `tier-a`）→ `tier-a`**。
 * ⇒ ★★ **`tier-a` 已在顶格，升级是原地不动** ——
 *   所以「tier 变了」与「`tier_source` 变了」**不是一回事**（见 `suggestionFollowsEscalationRule`）。
 */
export function escalatedTier(tier: string): string {
  if (tier === 'tier-c') return 'tier-b'
  if (tier === 'tier-b') return 'tier-a'
  return 'tier-a'
}

// ── (9) 升级规则与建议的一致性 ──

/** ★★ (9) 这条修正记录是否满足升级阈值。 */
export function correctionStatsTriggerEscalation(s: CorrectionStat | undefined): boolean {
  if (s === undefined) return false
  if (s.total < TASK_PROFILE_CORRECTION_MIN_SAMPLES) return false
  return s.correction_rate >= TASK_PROFILE_CORRECTION_ESCALATION_RATE
}

/**
 * ★★★★★ (9) **`suggestion` 与「`preferred_tier` + `correction_stats`」自洽**。
 *
 * ⇒ ⇒ 未达阈值 ⇒ `tier === preferred_tier` 且 `tier_source === 'registry'`
 * ⇒ ⇒ 达阈值 ⇒ `tier === escalatedTier(preferred_tier)` 且 `tier_source === 'correction_escalation'`
 * ⇒ ⇒ ★★ **`confidence_escalation` 在本响应里不可达**（见文件头 (8)），
 *   所以本函数**只认那两个取值**；出现第三个就是后端行为变了。
 *
 * ★ 这是**客户端决策 / 一致性自检**，不是响应校验：对每个真实响应它都成立，
 *   它防的是网关版本漂移或中间层改 body。
 */
export function suggestionFollowsEscalationRule(e: TaskProfileEntry): boolean {
  const escalated = correctionStatsTriggerEscalation(e.correction_stats)
  if (escalated) {
    return e.suggestion.tier === escalatedTier(e.preferred_tier) && e.suggestion.tier_source === 'correction_escalation'
  }
  return e.suggestion.tier === e.preferred_tier && e.suggestion.tier_source === 'registry'
}

// ── (5) 两个数组同集合且都升序 ──

/**
 * ★★★★★ (5) **`task_types` 与 `profiles[].task_type` 逐项相同且升序**。
 *
 * ⇒ 两者都来自同一张 `registry.Load().profiles`：一个取键后 `sort.Strings`，
 *   一个取值后 `sort.Slice(TaskType < TaskType)` ⇒ **同集合、同序**。
 * ⇒ ⇒ ★★ 客户端可用它察觉「响应被截断」或「两个数组来自不同版本」——
 *   这是本系列少见的、**顺序完全可观测**的端点。
 * ★ 同为**客户端一致性自检**而非响应校验（对真实响应恒成立）。
 */
export function taskTypesMatchProfiles(r: TaskProfileResponse): boolean {
  if (r.task_types.length !== r.profiles.length) return false
  for (let i = 0; i < r.profiles.length; i++) {
    if (r.task_types[i] !== (r.profiles[i] as TaskProfileEntry).task_type) return false
  }
  return true
}

/** ★★ (5) 该字符串数组是否严格升序（`sort.Strings` 的字节序）。 */
export function stringListIsAscending(values: readonly string[]): boolean {
  for (let i = 1; i < values.length; i++) {
    if (!((values[i - 1] as string) < (values[i] as string))) return false
  }
  return true
}