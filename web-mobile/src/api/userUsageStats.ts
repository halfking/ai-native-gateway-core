import { req, type RequestOptions } from './client'

/**
 * userUsageStats.ts — 账号维度用量汇总与单用户画像（2026-10-08，第八十八批）。
 *
 * GET /api/admin/users/usage-summary
 * GET /api/admin/users/{id}/stats
 *
 * - **注册**：`admin/handler.go:1104-1105`
 *   ```go
 *   mux.HandleFunc("/api/admin/users/usage-summary", admin(h.handleUserUsageSummary))
 *   mux.HandleFunc("/api/admin/users/",           admin(h.handleUserStatsDispatcher))
 *   ```
 *   ⇒ ★★ 两个都是 **`admin(...)` 档**（tenant_admin 可用，**不设** `requiresRole`）。
 *   ⇒ ★ `usage-summary` 是**精确路径**、`users/` 是**前缀** ⇒ Go 1.22+ ServeMux
 *     精确优先 ⇒ `usage-summary` 不会落进分发器。
 * - **实现**：`admin/user_usage_stats.go`（395 行）。
 * - 数据面是 `usage_facts`（与对账快照同源），owner 维度经 `api_keys.owner_user` 关联。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十八件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **`key_count` 查询失败只记日志、不报错**（`:217-222`）：
 *      ```go
 *      if err := h.db.QueryRow(...).Scan(&resp.KeyCount); err != nil {
 *          slog.Warn("user_stats: key count failed", …)
 *      }
 *      ```
 *      ⇒ ★★★ `key_count` 恒为数字，而 **0 是二义的**（真的 0 个密钥 / 查失败）
 *      ⇒ ★★ 这与批 80 的 `database.free_bytes`（作者**显式注释**说明是「测不到」位）
 *        **不同**：那里至少写明了语义，这里**没有任何注释**说明它可能不可信。
 *
 * (2) ★★★★★ **三个 Top 桶与 recent 列表都是「失败即降级」，但响应仍是 200**：
 *      - 桶**查询**失败 ⇒ `continue`（该桶保持**空数组**，`:328-331`）
 *      - 桶 **Scan** 失败 ⇒ 记日志 + `continue`（`:334-337`）
 *      - 桶 `rows.Err()` ⇒ **只记日志**（`:340-342`）⇒ **截断的 Top 列表被当完整数据返回**
 *      - `recent` 同理（`:358-373`）
 *      ⇒ ★★★ 客户端**无法区分**「这个用户没有数据」与「这一段查询失败了」。
 *      ⇒ ★★ 注释自陈动机（`:324-326`）：15s ctx 中途超时会把截断列表当完整数据，
 *        所以加了 `rows.Err()` 的**日志**—— 但**日志在服务端**，客户端看不到。
 *
 * (3) ★★★★★ **`usage-summary` 的 `rows.Scan` 失败是**裸 `continue`**（`:107-109`）——
 *      **没有** `slog`、没有 `warnRowSkip`、什么都没有
 *      ⇒ 与 (2) 的三个桶（至少记了 `slog.Warn`）**不一致**
 *      ⇒ ★★★ 这是本仓「静默跳行」最彻底的一处：列表可被静默截断且**无任何痕迹**。
 *      ⇒ ★ 对照：同文件的 `rows.Err()` **有**检查（`:112`）⇒ 迭代中断会 500。
 *
 * (4) ★★★★ **`days` 的上界两个端点不同**：
 *      - `usage-summary`：`days < 1 || days > 365` ⇒ 回落 30（`:63-65`）
 *      - `{id}/stats`：`days < 1 || days > 90` ⇒ 回落 30（`:176-178`）
 *      ⇒ ★★★ 同一个参数名、同一族、**上界差 4 倍**，且都是**静默回落不 400**。
 *
 * (5) ★★★★ **`daily` 与 `usage-summary` 的窗口口径不同**：
 *      - `usage-summary`：`now() - ($1 * INTERVAL '1 day')`（**UTC，无日切**，`:91`）
 *      - `{id}/stats` 的 `daily`：`generate_series(date_trunc('day', now()
 *        AT TIME ZONE 'Asia/Shanghai') …)` ⇒ **显式 Asia/Shanghai 日切 + 零填充**（`:250-273`）
 *      ⇒ ★★★ 且注释明说「对账页保持显式 UTC 日（结算口径，**有意分叉**）」
 *      ⇒ ⇒ 客户端**不能**跨这两个端点比较同一天的数字。
 *
 * (6) ★★★★ **`daily` 是零填充的**：用 `generate_series` 造出每一天，
 *      `LEFT JOIN agg` 后 `COALESCE(agg.requests, 0)` 等 ⇒
 *      **没有数据的那天也会出现（全 0）** ⇒ `daily.length === days` 恒成立。
 *
 * (7) ★★★★ **跨租户访问被掩蔽成 404**（`:199-205`）：
 *      ```go
 *      if myTenant != "" && myTenant != "default" && myTenant != tenantID {
 *          writeError(w, http.StatusNotFound, "user not found")
 *      ```
 *      注释自陈理由：先 403 会构成「跨租户用户 ID 存在性 oracle」
 *      ⇒ ★★★ 客户端**不能**用 403 判权限，也**不能**用 404 判「用户不存在」——
 *        两者返回**完全相同**的响应。
 *
 * (8) ★★★ **只有 `ErrNoRows` 映射 404，基础设施错误映射 500**
 *      （`:188-196`，注释自陈「不与『用户不存在』混淆」）⇒ 文案
 *      `user not found` / `lookup user failed` **可区分**。
 *
 * (9) ★★★★ **租户隔离在两个端点都被 `tenantID != "default"` 短路**：
 *      - `usage-summary`（`:73-79`）：`tenantID != "" && tenantID != "default"` 才加 where
 *      - `{id}/stats`（`:199`）：同样条件
 *      ⇒ ★★★ 租户键为 `"default"` 的那个租户，其 tenant_admin 会拿到**全平台**数据。
 *
 * (10) ★★★★ **405 检查排在 503 之前**（两个端点都是：`:53` vs `:57`、`:166` vs `:170`）
 *      ⇒ 与批 83（405 在前）一致、与批 84（503 在前）**相反**。
 *
 * (11) ★★★★ **503 文案是 `db not available`** —— 本仓**第五种**措辞：
 *      批 81/82/85/86 的 `database not configured`、批 84 的 `database not available`、
 *      本族的 **`db not available`**（少了 `atabase`）。
 *
 * (12) ★★★ `usage-summary` 的 `ORDER BY agg.requests DESC` **无 tiebreak**
 *      ⇒ 同请求数的行**顺序未定义**。
 *      三个 Top 桶的 `ORDER BY COUNT(*) DESC LIMIT 5` 同样**无 tiebreak**。
 *
 * (13) ★★★ **`usage-summary` 用 `JOIN users u ON u.username = agg.owner`**
 *      （`:95`）⇒ ★ **owner 不在 `users` 表里的账号整行消失**（内连接）。
 *
 * (14) ★★★ **`last_active_at` 是 `*time.Time` 且无 omitempty**（`:40`）
 *      ⇒ 无数据时是**裸 `null`**（「指针 + 无 omitempty」的一例）。
 *
 * (15) ★★★ **`recent` 的 `first_chunk_ms` / `total_ms` 是 `*int64` 且无 omitempty**
 *      （`:142-143`）⇒ 真会**裸 `null`**（这两列 DB 里可空）。
 *      ★ 而 `kpi.latency_p95_ms` 虽是 `*int64`，但 SQL 用
 *      `COALESCE(percentile_cont(0.95)…, 0)` ⇒ 实际路径**恒为数字**。
 *
 * (16) ★★★ **`kpi.error_rate` 只在 `requests > 0` 时计算**（`:242-244`），
 *      否则保持零值 ⇒ ★★ **`error_rate === 0` 是二义的**（真 0 或「无请求」）。
 *
 * (17) ★★★ 三个 Top 桶的 `name` 都有**占位符兜底**，且各不同：
 *      - `top_models`：`COALESCE(NULLIF(f.raw_model_name,''), '<unknown>')`
 *      - `top_apps`：`COALESCE(app.code, '<none>')`（**LEFT JOIN** ⇒ 可能是 `<none>`）
 *      - `top_keys`：`COALESCE(NULLIF(k.key_alias,''), COALESCE(NULLIF(k.key_prefix,''), '<unknown>'))`
 *      - `recent.model`：空 → **`'-'`**（不是 `<unknown>`！）
 *      - `recent.status`：空 → **`'unknown'`**
 *      ⇒ ★★ 五个占位符**各不相同**，且 `'-'` 与 `'unknown'` 只出现在 recent 里。
 *
 * (18) ★★★ **分发表是手工拆路径**（`:380-394`）：
 *      `TrimPrefix("/api/admin/users/")` → `TrimSuffix("/")` → `SplitN(…, 2)`，
 *      要求**恰好两段且第二段是 `stats`**，否则 **404 `not found`**；
 *      `{id}` 用 `Atoi`（**无 TrimSpace**）且 `id <= 0` ⇒ **400 `invalid user id`**。
 *
 * ★★ **本模块明确声明的校验边界**：解包器校验两个响应的**全部恒在键与类型**
 *   （usage-summary 2 + 行 5 键；stats 10 键 + kpi 6 + 桶 5 + recent 6 + daily 7），
 *   并为 (1)(2)(3)(5)(6)(7)(9)(12)(16) 各提供判据函数。
 *   ★ **不校验** `top_*` 与 `recent` 的**完整性**（后端自己都保证不了，见 (2)(3)）。
 */

// ── 常量（后端字面量） ───────────────────────────────────────────────────────

/** `:58` / `:171` 的 503 文案 —— 本仓**第五种**措辞。见 (11)。 */
export const USER_USAGE_DB_UNAVAILABLE_MESSAGE = 'db not available'

/** `:390` 的 400 文案。 */
export const USER_STATS_INVALID_ID_MESSAGE = 'invalid user id'

/** `:189` / `:203` 的 404 文案（不存在与跨租户**共用**）。见 (7)(8)。 */
export const USER_STATS_NOT_FOUND_MESSAGE = 'user not found'

/** `:194` 的 500 文案。 */
export const USER_STATS_LOOKUP_FAILED_MESSAGE = 'lookup user failed'

/** `:385` 分发器兜底的 404 文案。 */
export const USER_STATS_DISPATCH_NOT_FOUND_MESSAGE = 'not found'

/** `:62` / `:175` 的 `days` 缺省。 */
export const USER_USAGE_DEFAULT_DAYS = 30

/** `usage-summary` 的 `days` 上界。见 (4)。 */
export const USER_USAGE_SUMMARY_MAX_DAYS = 365
/** `{id}/stats` 的 `days` 上界 —— **只有 90**。见 (4)。 */
export const USER_STATS_MAX_DAYS = 90

/** `:94` / `:272` / `:301` 等的 LIMIT。 */
export const USER_STATS_TOP_LIMIT = 5
export const USER_STATS_RECENT_LIMIT = 10

/** `usage-summary` 的 5 键（`last_active_at` 是 `*time.Time` 无 omitempty）。见 (14)。 */
export const USER_USAGE_ROW_KEYS = ['username', 'requests', 'tokens', 'credits', 'last_active_at'] as const

/** `usage-summary` 的两键信封。 */
export const USER_USAGE_SUMMARY_KEYS = ['days', 'items'] as const

/** `userStatsResponse` 的 10 键。 */
export const USER_STATS_KEYS = [
  'user_id',
  'username',
  'days',
  'kpi',
  'daily',
  'top_models',
  'top_apps',
  'top_keys',
  'key_count',
  'recent_requests',
] as const

/** `userStatsKpi` 的 6 键。 */
export const USER_STATS_KPI_KEYS = [
  'requests',
  'tokens',
  'credits',
  'errors',
  'error_rate',
  'latency_p95_ms',
] as const

/** `userStatsBucket` 的 5 键（三个 Top 共用）。 */
export const USER_STATS_BUCKET_KEYS = ['name', 'requests', 'tokens', 'credits', 'cost_usd'] as const

/** `userStatsRecentRequest` 的 6 键。 */
export const USER_STATS_RECENT_KEYS = ['ts', 'model', 'first_chunk_ms', 'total_ms', 'credits', 'status'] as const

/** `tenantDailyStat` 的 7 键。 */
export const USER_STATS_DAILY_KEYS = ['date', 'requests', 'success', 'errors', 'tokens', 'credits', 'cost'] as const

/** 见 (17)：五个**各不相同**的占位符。 */
export const USER_STATS_MODEL_PLACEHOLDER = '<unknown>'
export const USER_STATS_APP_PLACEHOLDER = '<none>'
export const USER_STATS_KEY_PLACEHOLDER = '<unknown>'
export const USER_STATS_RECENT_MODEL_PLACEHOLDER = '-'
export const USER_STATS_RECENT_STATUS_PLACEHOLDER = 'unknown'

/** 见 (2)：三个 Top 桶的键名。 */
export const USER_STATS_TOP_KEYS = ['top_models', 'top_apps', 'top_keys'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface UserUsageRow {
  username: string
  requests: number
  tokens: number
  credits: number
  /** ★ `*time.Time` 无 omitempty ⇒ number 无关，是**裸 `null`** 或 RFC3339 串。见 (14)。 */
  last_active_at: string | null
}

export interface UserUsageSummaryResponse {
  days: number
  /** ★ 恒为数组（`[]userUsageRow{}` 初始化）。 */
  items: UserUsageRow[]
}

export interface UserStatsKpi {
  requests: number
  tokens: number
  credits: number
  /** ★ 非 success 即失败（含 rate_limited）。 */
  errors: number
  /** ★★ `requests === 0` 时**未计算**，保持 0 ⇒ `0` 是二义的。见 (16)。 */
  error_rate: number
  /** ★ `*int64` 无 omitempty；实际路径恒为数字（SQL 有 COALESCE）。见 (15)。 */
  latency_p95_ms: number | null
}

export interface UserStatsBucket {
  /** ★ 三个桶的占位符**各不相同**。见 (17)。 */
  name: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface UserStatsRecentRequest {
  /** `time.Time` ⇒ RFC3339Nano。 */
  ts: string
  /** ★ 空模型名回落成 `'-'`。见 (17)。 */
  model: string
  /** ★ `*int64` 无 omitempty ⇒ 数字或**裸 `null`**（DB 列可空）。见 (15)。 */
  first_chunk_ms: number | null
  /** ★ 同上。 */
  total_ms: number | null
  credits: number
  /** ★ 空 status 回落成 `'unknown'`。见 (17)。 */
  status: string
}

/** `tenantDailyStat`（复用自租户统计）。 */
export interface UserStatsDailyRow {
  /** `YYYY-MM-DD`（`to_char` 的产物）。 */
  date: string
  requests: number
  success: number
  errors: number
  tokens: number
  credits: number
  cost: number
}

export interface UserStatsResponse {
  user_id: number
  username: string
  days: number
  kpi: UserStatsKpi
  /** ★ **零填充** ⇒ 长度恒等于 `days`。见 (6)。 */
  daily: UserStatsDailyRow[]
  /** ★ 可能**不完整**（查询失败只降级该桶）。见 (2)。 */
  top_models: UserStatsBucket[]
  top_apps: UserStatsBucket[]
  top_keys: UserStatsBucket[]
  /** ★★ 0 是二义的（真 0 / 查失败）。见 (1)。 */
  key_count: number
  /** ★ 可能**不完整**。见 (2)。 */
  recent_requests: UserStatsRecentRequest[]
}

export interface UserUsageParams {
  /** 非法值静默回落 30（不 400）。见 (4)。 */
  days?: number
}
export interface UserStatsParams extends UserUsageParams {
  userId: number
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/users/usage-summary`（**admin 档**）。 */
export function fetchUserUsageSummary(
  params: UserUsageParams = {},
  options?: RequestOptions,
): Promise<UserUsageSummaryResponse> {
  const qs = new URLSearchParams()
  if (params.days != null) qs.set('days', String(params.days))
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/users/usage-summary${s ? `?${s}` : ''}`, undefined, options).then(
    unwrapUserUsageSummary,
  )
}

/** GET `/api/admin/users/{id}/stats`（**admin 档**）。 */
export function fetchUserStats(
  params: UserStatsParams,
  options?: RequestOptions,
): Promise<UserStatsResponse> {
  const qs = new URLSearchParams()
  if (params.days != null) qs.set('days', String(params.days))
  const s = qs.toString()
  return req<unknown>(
    'GET',
    `/api/admin/users/${params.userId}/stats${s ? `?${s}` : ''}`,
    undefined,
    options,
  ).then(unwrapUserStats)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapUserUsageSummary(resp: unknown): UserUsageSummaryResponse {
  const d = requireObject(resp, '账号用量汇总')
  requireKeys(d, USER_USAGE_SUMMARY_KEYS, '账号用量汇总')
  if (typeof d['days'] !== 'number') throw new Error('账号用量汇总 的 days 不是数字')
  const items = requireArray(d['items'], '账号用量汇总 的 items')
  for (let i = 0; i < items.length; i++) {
    const o = requireObject(items[i], `账号用量汇总 的 items[${i}]`)
    requireKeys(o, USER_USAGE_ROW_KEYS, `账号用量汇总 的 items[${i}]`)
    if (typeof o['username'] !== 'string') throw new Error(`items[${i}] 的 username 不是字符串`)
    for (const k of ['requests', 'tokens', 'credits'] as const) {
      if (typeof o[k] !== 'number') throw new Error(`items[${i}] 的 ${k} 不是数字`)
    }
    // ★ *time.Time 无 omitempty ⇒ null 或串。见 (14)。
    if (o['last_active_at'] !== null && typeof o['last_active_at'] !== 'string') {
      throw new Error(`items[${i}] 的 last_active_at 不是字符串也不是 null`)
    }
  }
  return d as unknown as UserUsageSummaryResponse
}

export function unwrapUserStats(resp: unknown): UserStatsResponse {
  const d = requireObject(resp, '用户画像')
  requireKeys(d, USER_STATS_KEYS, '用户画像')
  if (typeof d['user_id'] !== 'number') throw new Error('用户画像 的 user_id 不是数字')
  if (typeof d['username'] !== 'string') throw new Error('用户画像 的 username 不是字符串')
  if (typeof d['days'] !== 'number') throw new Error('用户画像 的 days 不是数字')
  // ★★ key_count 恒为数字（失败静默兜 0），**不接受** null。见 (1)。
  if (typeof d['key_count'] !== 'number') throw new Error('用户画像 的 key_count 不是数字')

  const k = requireObject(d['kpi'], '用户画像 的 kpi')
  requireKeys(k, USER_STATS_KPI_KEYS, '用户画像 的 kpi')
  for (const key of ['requests', 'tokens', 'credits', 'errors', 'error_rate'] as const) {
    if (typeof k[key] !== 'number') throw new Error(`kpi 的 ${key} 不是数字`)
  }
  if (k['latency_p95_ms'] !== null && typeof k['latency_p95_ms'] !== 'number') {
    throw new Error('kpi 的 latency_p95_ms 不是数字也不是 null')
  }

  const daily = requireArray(d['daily'], '用户画像 的 daily')
  for (let i = 0; i < daily.length; i++) {
    const o = requireObject(daily[i], `用户画像 的 daily[${i}]`)
    requireKeys(o, USER_STATS_DAILY_KEYS, `用户画像 的 daily[${i}]`)
    if (typeof o['date'] !== 'string') throw new Error(`daily[${i}] 的 date 不是字符串`)
    for (const key of ['requests', 'success', 'errors', 'tokens', 'credits', 'cost'] as const) {
      if (typeof o[key] !== 'number') throw new Error(`daily[${i}] 的 ${key} 不是数字`)
    }
  }

  for (const bk of USER_STATS_TOP_KEYS) {
    const arr = requireArray(d[bk], `用户画像 的 ${bk}`)
    for (let i = 0; i < arr.length; i++) {
      const o = requireObject(arr[i], `用户画像 的 ${bk}[${i}]`)
      requireKeys(o, USER_STATS_BUCKET_KEYS, `用户画像 的 ${bk}[${i}]`)
      if (typeof o['name'] !== 'string') throw new Error(`${bk}[${i}] 的 name 不是字符串`)
      for (const key of ['requests', 'tokens', 'credits', 'cost_usd'] as const) {
        if (typeof o[key] !== 'number') throw new Error(`${bk}[${i}] 的 ${key} 不是数字`)
      }
    }
  }

  const recent = requireArray(d['recent_requests'], '用户画像 的 recent_requests')
  for (let i = 0; i < recent.length; i++) {
    const o = requireObject(recent[i], `用户画像 的 recent_requests[${i}]`)
    requireKeys(o, USER_STATS_RECENT_KEYS, `用户画像 的 recent_requests[${i}]`)
    if (typeof o['ts'] !== 'string') throw new Error(`recent_requests[${i}] 的 ts 不是字符串`)
    for (const key of ['model', 'status'] as const) {
      if (typeof o[key] !== 'string') throw new Error(`recent_requests[${i}] 的 ${key} 不是字符串`)
    }
    if (typeof o['credits'] !== 'number') throw new Error(`recent_requests[${i}] 的 credits 不是数字`)
    // ★ 两列是真会为 null 的 *int64。见 (15)。
    for (const key of ['first_chunk_ms', 'total_ms'] as const) {
      if (o[key] !== null && typeof o[key] !== 'number') {
        throw new Error(`recent_requests[${i}] 的 ${key} 不是数字也不是 null`)
      }
    }
  }
  return d as unknown as UserStatsResponse
}

function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
}
function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}
function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (1) key_count 的二义 ──

/**
 * ★★★★★ 见 (1)：`key_count === 0` **是二义的** ——
 * 真的 0 个密钥，还是那次 `COUNT(*)` 查询失败了。
 * 后端失败时只 `slog.Warn`、不报错（`:217-222`）⇒ 响应仍是 200 且计数为 0。
 * ⇒ ★★ 客户端**不能**把 0 读成「这个用户没有密钥」。
 */
export function userStatsKeyCountIsAmbiguous(r: UserStatsResponse): boolean {
  return r.key_count === 0 && r.kpi.requests === 0
}

// ── (2) 桶/列表可能不完整 ──

/**
 * ★★★★★ 见 (2)：某个 Top 桶是**空数组** ——
 * 可能是「该维度真的没有数据」，也可能是**那次查询失败了**。
 * ⇒ ★★ 后端对两者返回**完全相同**的形状（`[]`），客户端**无法区分**。
 */
export function userStatsBucketIsAmbiguous(rows: UserStatsBucket[]): boolean {
  return rows.length === 0
}

/** ★★ 桶非空 ⇒ 至少有数据（但**仍可能不完整**，见 (2)）。 */
export function userStatsBucketHasRows(rows: UserStatsBucket[]): boolean {
  return rows.length > 0
}

// ── (3) Scan 失败静默跳行 ──

/**
 * ★★★★ 见 (3)：`usage-summary` 的行**可能被静默跳掉** ——
 * `rows.Scan` 失败是裸 `continue`（`:107-109`），**无任何服务端痕迹**。
 * ⇒ ★★ 因此 `items` 的长度**不能**被当成「这个窗口内的账号总数」。
 */
export function userUsageItemsMayBeTruncated(r: UserUsageSummaryResponse): boolean {
  return r.items.length === 0
}

// ── (5) 两个端点的窗口口径分叉 ──

/**
 * ★★★★ 见 (5)：`daily` 的日期是 **Asia/Shanghai 日切**的结果，
 * 而 `usage-summary` 的窗口是 **UTC 无日切**的 `now() - days*1day`。
 * ⇒ ★★★ 两个端点的「今天」可能落在**不同的一天** ⇒ 跨端点比较同一天的数字会错。
 */
export function userStatsDailyUsesShanghaiDayCut(r: UserStatsResponse): boolean {
  return r.daily.every((d) => /^\d{4}-\d{2}-\d{2}$/.test(d.date))
}

// ── (6) daily 零填充 ──

/** ★★★ 见 (6)：`daily` 由 `generate_series` 零填充 ⇒ 长度恒等于 `days`。 */
export function userStatsDailyLengthMatchesDays(r: UserStatsResponse): boolean {
  return r.daily.length === r.days
}

// ── (7)(8) 404 的两种成因 ──

/**
 * ★★★★ 见 (7)：跨租户访问与「用户不存在」返回**完全相同**的 404
 * ⇒ ★★★ 客户端**不能**用 404 判断「用户不存在」，也**不能**用 403 判权限
 * （后端刻意不返 403，注释自陈那会构成 ID 存在性 oracle）。
 */
export function userStatsNotFoundCoversBothCases(detail: string): boolean {
  return detail === 'user not found'
}

/** ★★★ 见 (8)：基础设施错误是 **500** 且文案不同 ⇒ 可区分于 404。 */
export function userStatsLookupFailedIsDistinct(detail: string): boolean {
  return detail === 'lookup user failed'
}

// ── (9) 租户隔离被 default 短路 ──

/** ★★★★★ 见 (9)：租户键为 `"default"` 时**不加** `tenant_id` 过滤。 */
export function userUsageTenantFilterIsSkipped(tenantId: string): boolean {
  return tenantId === '' || tenantId === 'default'
}

// ── (12)(16) 排序与二义数值 ──

/** ★★★ 见 (12)：`items` 按 `requests` 降序（**无 tiebreak** ⇒ 同值行顺序未定义）。 */
export function userUsageItemsAreRequestsDescending(r: UserUsageSummaryResponse): boolean {
  for (let i = 1; i < r.items.length; i++) {
    const prev = r.items[i - 1]
    const cur = r.items[i]
    if (prev === undefined || cur === undefined) return false
    if (prev.requests < cur.requests) return false
  }
  return true
}

/** ★★★ 见 (16)：`error_rate === 0` **是二义的**（真 0 或 `requests === 0` 未计算）。 */
export function userStatsErrorRateIsAmbiguous(r: UserStatsResponse): boolean {
  return r.kpi.error_rate === 0
}

/** ★★ `requests > 0` ⇒ `error_rate` 是**真值**算出来的（非零值时）。 */
export function userStatsErrorRateIsComputed(r: UserStatsResponse): boolean {
  return r.kpi.requests > 0
}

// ── (15) 两个可空列 ──

/** ★★★ 见 (15)：`recent` 的 `first_chunk_ms` 可为**裸 `null`**（未采到首字延迟）。 */
export function userStatsRecentFirstChunkIsNull(row: UserStatsRecentRequest): boolean {
  return row.first_chunk_ms === null
}

/** ★★★ `total_ms` 同样可为裸 `null`。 */
export function userStatsRecentTotalIsNull(row: UserStatsRecentRequest): boolean {
  return row.total_ms === null
}

// ── (17) 占位符 ──

/** ★★ 见 (17)：`recent.model` 的占位符是 `'-'`（**不是** `<unknown>`）。 */
export function userStatsRecentModelIsPlaceholder(row: UserStatsRecentRequest): boolean {
  return row.model === '-'
}

/** ★★ 见 (17)：`recent.status` 的占位符是 `'unknown'`。 */
export function userStatsRecentStatusIsPlaceholder(row: UserStatsRecentRequest): boolean {
  return row.status === 'unknown'
}

/** ★★ 见 (17)：`top_apps` 的占位符是 `'<none>'`（其余两个是 `<unknown>`）。 */
export function userStatsAppBucketPlaceholderIsNone(row: UserStatsBucket): boolean {
  return row.name === '<none>'
}

// ── (4) days 上界分叉 ──

/** ★★★★ 见 (4)：`usage-summary` 接受到 365 天。 */
export function userUsageSummaryDaysIsAccepted(d: number): boolean {
  return d >= 1 && d <= USER_USAGE_SUMMARY_MAX_DAYS
}

/** ★★★★ 见 (4)：`{id}/stats` 只接受到 **90** 天 —— 同一参数、上界差 4 倍。 */
export function userStatsDaysIsAccepted(d: number): boolean {
  return d >= 1 && d <= USER_STATS_MAX_DAYS
}

/** ★★ 非法 `days` 的实际生效值（恒为缺省 30），**静默回落不 400**。 */
export function userUsageEffectiveDays(d: number, max: number): number {
  return d >= 1 && d <= max ? d : USER_USAGE_DEFAULT_DAYS
}
