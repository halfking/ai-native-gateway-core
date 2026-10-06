// tenants.ts — 租户名录与租户统计（**superAdmin 档**，只读）。
//   GET /api/admin/tenants?status=
//   GET /api/admin/tenants/{code}
//   GET /api/admin/tenants/{code}/users
//   GET /api/admin/tenants/{code}/keys
//   GET /api/admin/tenants/{code}/stats?days=
//
// 鉴权：`admin/handler.go:926-927` 两条注册都是 `h.superAdmin(...)`
// ⇒ **superAdmin 档**，抽屉席须设 `requiresRole: 'super_admin'`。
// ★ 而且 handler 内部**还有一道**角色校验：
//     if auth := GetAuthContext(r); auth != nil &&
//        auth.Role != "super_admin" && auth.Role != "admin_key" { 403 }
//   ⇒ ★ `admin_key` 这个角色**也放行**，它是中间件那道 `requiresRole`
//     没有建模的 ⇒ 移动端**无法**只用角色字符串判权限，只能靠后端的 403。
//
// ⚠️⚠️⚠️⚠️ 这五条端点**全部返回裸结构**，不是 `{items: […]}`：
//     list / users / keys ⇒ **裸数组**；get / stats ⇒ **裸对象**。
// ⇒ 与 MaaS 族（`{items}`）和本仓多数 admin 端点**都不同**。
//     误当成 `{items}` 解包会让四条端点 100% 抛错。
//
// (1) ★★★★★★ `attachTenantUsage7d` **静默降级成 0**：它有**独立的 1.5s 预算**
//     （`usageCtx`，派生自 `r.Context()` 以免被列表主查询的 cancel 掐掉），
//     查询失败/超时时 `slog.Warn` 然后 **直接 return**，四个字段留在 0。
//     ⇒ ★★★ 客户端**分辨不出**「这个租户真没用量」与「富化没在 1.5s 内跑完」。
//     2026-10-03 的注释记录了根因：这条聚合在真实数据量下要 20s+
//     （request_logs_with_current_month 7 天 32 万行），而原先与主查询共用 5s。
//
// (2) ★★★★★ `tenantInfo` 的 **6 个聚合字段全是 `omitempty`**：
//     `user_count` / `api_key_count` / `requests_7d` / `tokens_7d` /
//     `credits_7d` / `cost_7d_usd` / `total_requests`
//     ⇒ ★★★ 「键不存在」至少有三种成因：真的为 0、被 omitempty 省掉、
//       富化降级留下的 0。**绝不能**把缺键渲染成「没有用量」当成结论。
//
// (3) ★★★★ `getTenant`（详情）的五个聚合计数全是 `_ = h.db.QueryRow(...)`
//     ⇒ **连一行日志都不留**地吞掉错误，比列表侧更糟。
//     ⇒ 列表和详情的「0」**可靠性不同**，页面不能一视同仁。
//
// (4) ★★★★ `GET /{code}/stats` 会返回 **504 Gateway Timeout**
//     （`writeTenantStatsError`：`retry with a smaller days window`）
//     ⇒ ★★ 这是本仓**第一次**出现 504：不是 500、不是「查不到」。
//     页面必须把它单列，提示「调小 days 重试」。
//
// (5) ★★★★ 同一个响应里，**成本与积分来自两张不同的表**：
//     `total_requests` / `total_tokens` / `total_cost_usd` /
//     `unique_keys` / `unique_models` / `unique_apps`
//         ← `usage_ledger_with_current_month`
//     `total_credits` / `input_tokens` / `output_tokens` /
//     `cache_read_tokens` / `cache_write_tokens` / `avg_latency_ms`
//         ← `logsTable`（`request_logs_hot` ∪ `request_logs` 的 UNION ALL 子查询）
//     ⇒ 「收入侧」和「上游成本侧」口径不同，**两者对不上是可能的**，不是 bug。
//
// (6) ★★★★ 日切**有意分叉**：stats 的 `daily` 用
//     `date_trunc('day', ts AT TIME ZONE 'Asia/Shanghai')`（R36-A3 显式钉死），
//     而对账/结算页保持**显式 UTC 日**。
//     ⇒ 同一个产品里两个页面的「一天」边界不同，看到跨零点的差异**不是数据错**。
//     且 `daily` 用 `generate_series` **补零** ⇒ 永远有 `days` 条连续日期，
//     不会因为某天没数据而缺格。
//
// (7) ★★★ `days`：`< 1 ⇒ 7`、`> 365 ⇒ 365`（`strconv.Atoi` 失败时 `days=0`
//     ⇒ 落进 `< 1` ⇒ 7）⇒ 又一套限幅（本仓第八种）。
//
// (8) ★★★ 三处聚合都用 `warnRowSkip` + `continue` ⇒ **静默丢行**：
//     `tenants.list` / `tenants.listUsers` / `tenants.listKeys` /
//     `tenants.stats.byModel` / `byApplication` / `daily`
//     ⇒ 返回条数**可能**少于库里真实行数，页面不能把条数当「全量」。
//
// (9) ★★ `userInfo.last_login_at` 是 `*time.Time` 但**无** omitempty
//     ⇒ 键一定在，值为 `null` = **从未登录**（不是「键缺失」）。
//     而 `tenantKeyInfo` 的 `key_alias` / `owner_user` / `application_code` /
//     `expires_at` 都**带** omitempty ⇒ 键可能整个不存在。
//     ⇒ **同一个响应族里两种语义并存**。
//
// (10) ★★ 子资源路由有历史坑：`sub` 是 `SplitN(path,"/",2)` 的尾部，
//     所以 `/model-policies/audit` 的 `sub` 是 `"model-policies/audit"`。
//     2026-06-23 曾因此对 `/check` 和 `/audit` 都报
//     `unknown sub-resource`，现已由 `isModelPoliciesSubResource` 按前缀匹配修掉。
//     未知子资源 ⇒ **404 `unknown sub-resource: <sub>`**。
//
// (11) ★ `h.db == nil` ⇒ **503 `database not configured`**。
//
// ★★ 写操作本页一律不碰：POST /tenants（创建）、PATCH /tenants/{code}、
//     model-policies 的 POST/PATCH/DELETE/undelete。

import type { RequestOptions } from './client'
import { req } from './client'

/** ★ `getTenantStats` 的限幅（`admin/tenants.go`）：<1 ⇒ 7、>365 ⇒ 365。 */
export const TENANT_STATS_DAYS_DEFAULT = 7
export const TENANT_STATS_DAYS_MAX = 365

/** ★★ `tenantInfo` 里那 7 个带 `omitempty` 的聚合键。 */
export const TENANT_OPTIONAL_AGG_KEYS = [
  'user_count',
  'api_key_count',
  'requests_7d',
  'tokens_7d',
  'credits_7d',
  'cost_7d_usd',
  'total_requests',
] as const
export type TenantOptionalAggKey = (typeof TENANT_OPTIONAL_AGG_KEYS)[number]

/** ★★ `daily` 的日切时区（R36-A3 显式钉死）；对账页刻意用 UTC，**有意分叉**。 */
export const TENANT_STATS_DAY_TZ = 'Asia/Shanghai'

export interface TenantInfo {
  code: string
  name: string
  status: string
  description: string
  contact_email: string
  created_at: string
  updated_at: string
  /** ★★ 以下 7 个**带 omitempty** ⇒ 为 0 时键整个不存在。 */
  user_count?: number
  api_key_count?: number
  requests_7d?: number
  tokens_7d?: number
  credits_7d?: number
  cost_7d_usd?: number
  total_requests?: number
}

export function fetchTenants(
  params: { status?: string } = {},
  options?: RequestOptions,
): Promise<TenantInfo[]> {
  const qs = new URLSearchParams()
  if (params.status) qs.set('status', params.status)
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/tenants${s ? '?' + s : ''}`, undefined, options).then(
    unwrapTenants,
  )
}

/** ★★ 响应是**裸数组**（`writeJSON(w, 200, tenants)`），**没有** `{items}`。 */
export function unwrapTenants(resp: unknown): TenantInfo[] {
  if (Array.isArray(resp)) return resp as TenantInfo[]
  const actual = resp === null ? 'null' : typeof resp
  throw new Error(`admin/tenants 响应形状不符：期望裸数组 [...]，实得 ${actual}`)
}

function tenantPath(code: string, sub?: string): string {
  // ★ 租户码必须 encode：后端 `SplitN(path,"/",2)` 会按斜杠切
  return `/api/admin/tenants/${encodeURIComponent(code)}${sub ? '/' + sub : ''}`
}

export function fetchTenant(code: string, options?: RequestOptions): Promise<TenantInfo> {
  return req<unknown>('GET', tenantPath(code), undefined, options).then(unwrapTenant)
}

/** ★ 响应是**裸对象**（`writeJSON(w, 200, t)`）。 */
export function unwrapTenant(resp: unknown): TenantInfo {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const t = resp as Record<string, unknown>
    if (typeof t.code === 'string' && typeof t.status === 'string') {
      return t as unknown as TenantInfo
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/tenants/{code} 响应形状不符：期望裸租户对象 {code, status, …}，实得 ${actual}`)
}

export interface TenantUser {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  must_change_password: boolean
  /** ★ 指针但**无** omitempty ⇒ 键一定在；`null` = **从未登录**。 */
  last_login_at: string | null
  created_at: string
}

export function fetchTenantUsers(code: string, options?: RequestOptions): Promise<TenantUser[]> {
  return req<unknown>('GET', tenantPath(code, 'users'), undefined, options).then(unwrapTenantUsers)
}

/** ★★ 裸数组。 */
export function unwrapTenantUsers(resp: unknown): TenantUser[] {
  if (Array.isArray(resp)) return resp as TenantUser[]
  const actual = resp === null ? 'null' : typeof resp
  throw new Error(`admin/tenants/{code}/users 响应形状不符：期望裸数组 [...]，实得 ${actual}`)
}

export interface TenantKey {
  id: number
  tenant_id: string
  key_prefix: string
  /** ★ 以下 4 个**带** omitempty ⇒ 键可能整个不存在。 */
  key_alias?: string
  owner_user?: string
  application_code?: string
  expires_at?: string
  enabled: boolean
  status: string
  application_id: number
  total_requests: number
  total_cost_usd: number
  created_at: string
}

export function fetchTenantKeys(code: string, options?: RequestOptions): Promise<TenantKey[]> {
  return req<unknown>('GET', tenantPath(code, 'keys'), undefined, options).then(unwrapTenantKeys)
}

/** ★★ 裸数组（且**按 id DESC** 排序，与 users 的 `ORDER BY id` 正相反）。 */
export function unwrapTenantKeys(resp: unknown): TenantKey[] {
  if (Array.isArray(resp)) return resp as TenantKey[]
  const actual = resp === null ? 'null' : typeof resp
  throw new Error(`admin/tenants/{code}/keys 响应形状不符：期望裸数组 [...]，实得 ${actual}`)
}

export interface TenantModelBreakdown {
  model: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface TenantAppBreakdown {
  application_code: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface TenantDailyStat {
  /** ★ `YYYY-MM-DD`，日切按 **Asia/Shanghai**。 */
  date: string
  requests: number
  success: number
  errors: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface TenantStats {
  /** ★ clamp **之后**回显。 */
  days: number
  // ← usage_ledger_with_current_month
  total_requests: number
  total_tokens: number
  total_cost_usd: number
  unique_keys: number
  unique_models: number
  unique_apps: number
  // ← logsTable（hot ∪ 母表 的 UNION ALL）
  total_credits: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  avg_latency_ms: number
  by_model: TenantModelBreakdown[]
  by_application: TenantAppBreakdown[]
  /** ★ `generate_series` 补零 ⇒ 恒有 `days` 条连续日期。 */
  daily: TenantDailyStat[]
}

export function fetchTenantStats(
  code: string,
  params: { days?: number } = {},
  options?: RequestOptions,
): Promise<TenantStats> {
  const qs = new URLSearchParams()
  if (typeof params.days === 'number' && Number.isFinite(params.days)) {
    qs.set('days', String(Math.trunc(params.days)))
  }
  const s = qs.toString()
  return req<unknown>('GET', `${tenantPath(code, 'stats')}${s ? '?' + s : ''}`, undefined, options).then(
    unwrapTenantStats,
  )
}

/** ★ 响应是**裸对象**；`by_model` / `by_application` / `daily` 后端已 nil→`[]`。 */
export function unwrapTenantStats(resp: unknown): TenantStats {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const s = resp as Record<string, unknown>
    if (
      typeof s.days === 'number' &&
      Array.isArray(s.by_model) &&
      Array.isArray(s.by_application) &&
      Array.isArray(s.daily)
    ) {
      return s as unknown as TenantStats
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`admin/tenants/{code}/stats 响应形状不符：期望裸统计对象 {days, by_model, by_application, daily}，实得 ${actual}`)
}

// ── 页面侧判读 ─────────────────────────────────────────────────────────

/** ★ `getTenantStats` 限幅：<1 ⇒ **回落 7**、>365 ⇒ clamp 365。 */
export function tenantStatsDaysClamped(days: number): number {
  if (!Number.isFinite(days)) return TENANT_STATS_DAYS_DEFAULT
  const n = Math.trunc(days)
  if (n < 1) return TENANT_STATS_DAYS_DEFAULT
  if (n > TENANT_STATS_DAYS_MAX) return TENANT_STATS_DAYS_MAX
  return n
}

/**
 * ★★★ **哪些聚合键缺失**。缺失的成因**至少**有三种：
 * 真的为 0（被 omitempty 省掉）／ 富化超时降级留下的 0 ／ 从未被写入。
 * ⇒ 页面拿到这个列表就知道「哪几格不能下结论」。
 */
export function tenantMissingAggKeys(t: TenantInfo): TenantOptionalAggKey[] {
  const rec = t as unknown as Record<string, unknown>
  return TENANT_OPTIONAL_AGG_KEYS.filter((k) => !(k in rec))
}

/**
 * ★★★ 「没有 7 天用量」这个结论**不可靠**：`requests_7d` 缺失既可能是真的没用量，
 * 也可能是 `attachTenantUsage7d` 的 **1.5s 预算没跑完**（只在服务端 slog.Warn）。
 * 返回 true 时页面必须说「**读数不可信**」，而不是「这段时间没用量」。
 */
export function tenantUsageMayBeDegraded(t: TenantInfo): boolean {
  return !('requests_7d' in t) || !('credits_7d' in t) || !('cost_7d_usd' in t)
}

/** ★ 列表与详情的聚合可靠性**不同**：详情是 `_ =` 吞错，连日志都没有。 */
export function tenantAggSourceNote(fromList: boolean): string {
  return fromList ? 'list' : 'detail'
}

/** ★ `last_login_at` 是**无** omitempty 的指针 ⇒ 键在值为 `null` = 从未登录。 */
export function tenantUserNeverLoggedIn(u: TenantUser): boolean {
  return u.last_login_at === null
}

/** ★★ 「从未登录」与「键缺失」是两回事（互为对照用）。 */
export function tenantUserHasLastLogin(u: TenantUser): boolean {
  return typeof u.last_login_at === 'string' && u.last_login_at.length > 0
}

/** ★ 被要求改密码的用户数（运维安全视角的硬指标）。 */
export function tenantUsersNeedingPasswordChange(users: TenantUser[]): number {
  return users.filter((u) => u.must_change_password).length
}

/** ★ 停用用户数。 */
export function tenantUsersDisabled(users: TenantUser[]): number {
  return users.filter((u) => !u.enabled).length
}

/** ★ `expires_at` 带 omitempty ⇒ 键不存在 = **永不过期**，不是「没查到」。 */
export function tenantKeyNeverExpires(k: TenantKey): boolean {
  return !('expires_at' in k)
}

/** ★ 空的 `key_alias` / `owner_user` / `application_code` 也是键不存在（都是指针 omitempty）。 */
export function tenantKeyHasAlias(k: TenantKey): boolean {
  return 'key_alias' in k
}

export function tenantKeyHasOwner(k: TenantKey): boolean {
  return 'owner_user' in k
}

/**
 * ★★ `daily` 用 `generate_series` **补零** ⇒ 条数恒等于 `days`。
 * 页面据此画连续的折线；若实际条数少于 days，说明后端降级过，不能画。
 */
export function tenantDailyIsComplete(s: TenantStats): boolean {
  return s.daily.length === s.days
}

/** ★ 日切按 **Asia/Shanghai**；对账页按 UTC —— **有意分叉**，跨零点差异不是数据错。 */
export function tenantDailyTzNote(): string {
  return TENANT_STATS_DAY_TZ
}