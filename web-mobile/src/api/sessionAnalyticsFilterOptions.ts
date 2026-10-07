import { req, type RequestOptions } from './client'

/**
 * sessionAnalyticsFilterOptions.ts — 会话分析页的模型/提供商筛选项（2026-10-08，第九十七批）。
 *
 * GET /api/admin/session-analytics/filter-options
 *
 * - **注册**：**第六种注册形态 —— 前缀 catch-all + 方法内分发器**，
 *   `cmd/gateway/main.go:7186`
 *   ```go
 *   mux.HandleFunc("/api/admin/session-analytics/", wrapSessionAnalytics(adminHandler.RouteSessionAnalytics))
 *   ```
 *   分派点是 `admin/session_analytics_handler.go:728-729` 的
 *   `case parts[0] == "filter-options" && len(parts) == 1: h.HandleFilterOptions(w, r)`
 *   ⇒ ★★ 注意它**不在** `admin/handler.go` 的 `mux.HandleFunc` 里，
 *   只查 `admin/handler.go` 会判「端点不存在」。
 * - **实现**：`admin/session_analytics_top.go:190-272`（handler）
 *   · `:47-50`（`FilterOptionsResponse`）· `admin/session_tenant.go:126-131`（`effectiveScopeTenant`）
 *   · `cmd/gateway/main_admin_wrappers.go:42-64`（鉴权包装）。
 * - **桌面调用方**：**没有**。全仓 `web/src/` 只出现 `/api/admin/turns/sessions/filter-options`
 *   ⇒ ★★★ **移动端会是第一个调用方**。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 与批 96 的「同叶名兄弟」有六处正好相反 —— 抄错就是全错
 * ══════════════════════════════════════════════════════════════════════════
 *
 * 兄弟端点是 `/api/admin/turns/sessions/filter-options`
 * （`web-mobile/src/api/turnsFilterOptions.ts`）。**叶名相同、键数差 4 倍、语义不同。**
 *
 *  | 维度 | 本端点（session-analytics） | 兄弟端点（turns/sessions） |
 *  |---|---|---|
 *  | 响应键数 | **2** | 8 |
 *  | **空时编码** | ★★★★★ **`null`** | ★★★★★ **`[]`** |
 *  | **租户作用域** | ★★★★★ **super_admin 看全部租户** | ★★★★★ **永远租户内** |
 *  | **能否 `?tenant=` 收窄** | ★★★★ **不能** | 能（`tenantFromQueryOrContext`） |
 *  | **无 DB 时** | ★★★★ **404（整棵树不注册）** | 503（恒注册） |
 *  | **scan 失败** | ★★★★ **静默跳过该行** | 500 |
 *
 * (1) ★★★★★ **响应只有 2 个键**（`FilterOptionsResponse`，`session_analytics_top.go:47-50`），
 *     两个都是 `[]string`，**无 `omitempty`** ⇒ 键恒在。
 *
 * (2) ★★★★★ **★ 空时是 `null` 不是 `[]`** —— 与兄弟端点正好相反：
 *     ```go
 *     var ( models []string; providers []string )   // :232-234 从 nil 开始
 *     for modelRows.Next() {
 *         if err := modelRows.Scan(&m); err == nil { models = append(models, m) }   // :243-245
 *     }
 *     ```
 *     **从头到尾没有任何一处给它们赋空切片** ⇒ 零行 ⇒ 仍是 `nil` ⇒ JSON **`null`**。
 *     ⇒ ⇒ 对照兄弟端点 `*targets[i] = []string{}`（`turns_filter_options.go:166`）**显式赋空**。
 *     ⇒ ★★★ **这两个端点的解包器绝不能共用。**
 *
 * (3) ★★★★★ **两个槽位互相独立** ⇒ **半空形状可达**：
 *     `{models: [...], providers: null}` 与 `{models: null, providers: [...]}` 都合法
 *     （`models_used text[] DEFAULT '{}' NOT NULL`、`providers text[]` 可空，
 *     `UNNEST` 空数组/NULL 都只是零行而不是报错）。
 *
 * (4) ★★★★★ **★ 租户作用域与兄弟端点方向相反**：
 *     ```go
 *     func effectiveScopeTenant(r *http.Request) string {   // session_tenant.go:126-131
 *         if IsSuperAdminOrLegacy(r) { return "" }
 *         return GetTenantID(r)
 *     }
 *     ```
 *     super_admin / `admin_key` 拿到 **`""`** ⇒ `if tenantID != ""`（`:212`、`:225`）**不进**
 *     ⇒ ⇒ **SQL 里没有租户条件** ⇒ **super_admin 看到的是全部租户的模型与提供商聚合。**
 *     ⇒ ★★★ 对照兄弟端点：那里的 `GetTenantID` 兜底 `"default"`（`context.go:37-42`）⇒ 恒非空
 *       ⇒ **永远租户内**。**两个同叶名端点的数据可见范围是相反的。**
 *
 * (5) ★★★★ **★ 而且这里没有 `?tenant=` 收窄的手段** ——
 *     `effectiveScopeTenant` **不读查询参数**（对照 `tenantFromQueryOrContext` 会读
 *     `?tenant=` / `?tenant_id=` / `X-Tenant-ID`）⇒ ⇒
 *     **super_admin 无法把这个端点收窄到某一个租户**，只能看到全量聚合。
 *     ⇒ ★★ 想要单租户视图，唯一办法是**用那个租户的账号登录**。
 *
 * (6) ★★★★ **★ 整条 `/api/admin/session-analytics/` 树只在 `dbConn != nil` 时注册**
 *     （`cmd/gateway/main.go:7085-7089`：`wrapSessionAnalytics` 只在有 DB 时才被赋值）
 *     ⇒ ⇒ **没有数据库时这个路径是 404，而不是 503** ——
 *       对照兄弟端点它恒注册（`admin/handler.go:1277`）才在 handler 内判 `h.db == nil` 返 503。
 *
 * (7) ★★★★ **鉴权是双模式的**（`cmd/gateway/main_admin_wrappers.go:42-64`）：
 *     - `session_service_auth.enabled` 为假，或 `LLM_GATEWAY_SESSION_SERVICE_JWT_SECRET` 为空
 *       ⇒ 走 `admin.AdminMiddleware`（与兄弟端点同一套）
 *     - 否则走 `admin.SessionAnalyticsMiddleware(...)`，issuer 默认 `ai-session-manager`、
 *       audience 固定 `llm-gateway-session-analytics`
 *     ⇒ ⇒ ★★★ **这个端点可以被 session-service JWT 调用，不只是管理员会话。**
 *     ⇒ ★★ 且「enabled 但 secret 缺失」只 `slog.Warn` 一句就**回落到 admin 鉴权**
 *       —— 配错时的行为是**降级**，不是 fail-closed。
 *
 * (8) ★★★★ **★ per-row 的 scan 失败被静默跳过**：
 *     `if err := modelRows.Scan(&m); err == nil { models = append(models, m) }`（`:243-245`）
 *     与 providers 侧同形（`:258-260`）⇒ ⇒ **某一行取不出值就整行消失，无任何信号。**
 *     ⇒ ★★★ 与兄弟端点相反：`turns_filter_options.go:169-173` 的 scan 失败是 **return err ⇒ 500**。
 *     ⇒ ★★ 但**迭代级错误是被检查的**（`modelRows.Err()` `:247`、`providerRows.Err()` `:262`）
 *       ⇒ 精确说法是「**行扫描吞、迭代不吞**」。
 *     ⇒ ⇒ 客户端**不能**假定列表长度与 `DISTINCT` 结果一致。
 *
 * (9) ★★★★ **★ 顺序与去重在响应里是可观测的** ——
 *     `ORDER BY model` / `ORDER BY provider`（`:216`、`:229`）配 `DISTINCT`（`:207`、`:220`）
 *     ⇒ ⇒ **按值升序、无重复**。
 *     ⇒ ★★★ 这是本系列第一个**顺序能被客户端校验**的筛选项端点：
 *       兄弟端点的 `last_at` 只进 SQL 的 ORDER BY、不进 JSON（批 96 (4)）⇒ 不可观测。
 *
 * (10) ★★★ **这次头注与代码相符**：`last_request_at > NOW() - INTERVAL '30 days'`
 *      （`:210`、`:223`）真的是 30 天窗口（对照批 96 那 8 个维度里有 5 个根本没有窗口）。
 *
 * (11) ★★★ **没有任何桌面调用方** ⇒ 这段代码**从未被现有前端消费过** ⇒
 *      移动端是第一个调用方，也意味着**它的行为没有任何既有消费者验证过**。
 *
 * (12) ★★★ 错误面：非 GET ⇒ **405**（`:191-194`）；`h.db == nil` ⇒
 *      **503 `db not available`**（`:196`，★ 兄弟端点是 `database not configured`）；
 *      查询失败 ⇒ **500 `query failed`**（`writeInternalErr` → `writeError`，`internal_error.go:46-49`）。
 *      错误体一律 `{"error":{"detail":…}}`。超时 **10s**（`:200`，兄弟端点 12s）。
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验顶层 2 个恒在键、每个键是 **`null` 或字符串数组**（三个分支：null / 不是数组 / 元素不是字符串）；
 *   为 (2)(3)(4)(9) 提供判据或决策函数。
 *   ★ **不校验** 值的取值域（用户数据自由决定）；★ **不校验** 两个列表是否有交集（后端无从保证）。
 *   ★ **不提供** 「`models_used` 为空数组时响应非 null」这类后端不变式的判据。
 */
export const SESSION_ANALYTICS_FILTER_OPTIONS_PATH = '/api/admin/session-analytics/filter-options'

/** ★★ (1) 响应恒有的 2 个键（`session_analytics_top.go:48-49`）。 */
export const SESSION_ANALYTICS_FILTER_OPTION_KEYS = ['models', 'providers'] as const

/** ★★ (10) 真实的 30 天窗口（`:210`、`:223`）。 */
export const SESSION_ANALYTICS_FILTER_WINDOW_DAYS = 30

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface SessionAnalyticsFilterOptions {
  /**
   * ★★★★★ (2) **无数据时是 `null`，不是 `[]`** —— 类型必须诚实地带上 `null`，
   * ★ 这样每个消费点都被迫处理它；`[]` 只在「查到了、但一行都没匹配」时才出现。
   */
  models: string[] | null
  /** ★★★★★ (2)(3) 同上，且**与 `models` 相互独立** ⇒ 半空形状可达。 */
  providers: string[] | null
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/admin/session-analytics/filter-options`。
 * ★ (6) 无数据库时**这个路径压根没注册** ⇒ 404，不是 503。
 * ★ (7) 鉴权双模式：admin 会话，或 session-service JWT（audience `llm-gateway-session-analytics`）。
 * ★ (4)(5) 无查询参数，**也无法用参数收窄租户** —— super_admin 只能拿全量聚合。
 */
export function fetchSessionAnalyticsFilterOptions(
  options?: RequestOptions,
): Promise<SessionAnalyticsFilterOptions> {
  return req<unknown>('GET', SESSION_ANALYTICS_FILTER_OPTIONS_PATH, undefined, options).then(
    unwrapSessionAnalyticsFilterOptions,
  )
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapSessionAnalyticsFilterOptions(resp: unknown): SessionAnalyticsFilterOptions {
  const d = requireObject(resp, '会话筛选项')
  requireKeys(d, SESSION_ANALYTICS_FILTER_OPTION_KEYS, '会话筛选项')
  // ★ (2) 三态都要放行：null / 字符串数组 / 其余一切报错。
  //   ★★ 若这里把 null 判成错，移动端在「网关刚装好、还没跑过流量」时**整个页面报错** ——
  //   而那是一个完全正常的状态。
  requireNullableStringArray(d['models'], '会话筛选项 的 models')
  requireNullableStringArray(d['providers'], '会话筛选项 的 providers')
  return d as unknown as SessionAnalyticsFilterOptions
}

function requireNullableStringArray(v: unknown, where: string): void {
  if (v === null) return
  if (!Array.isArray(v)) {
    const actual = typeof v
    throw new Error(`${where} 不是数组也不是 null（实得 ${actual}）`)
  }
  for (let i = 0; i < v.length; i++) {
    if (typeof v[i] !== 'string') throw new Error(`${where} 的第 ${i} 项不是字符串`)
  }
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

/**
 * ★★★★★ (2) 把 `null` 归一成 `[]` —— **这是本模块最该被复用的那一个函数**。
 * ⇒ ⇒ 凡是要 `.length` / `.map` / 迭代下拉项的地方都必须先过它，
 *   否则「网关还没跑过流量」就会让移动端崩在 `Cannot read properties of null` 上
 *   —— ★ 与批 96 注释里那句「否则前端 `windowEntries.length` 会抛」是同一类事故，
 *   ★★ 但**成因相反**：那边是后端没兜住，这边是后端**故意**发 null。
 */
export function filterOptionList(v: string[] | null): string[] {
  return v ?? []
}

/**
 * ★★★★★ (3) 两个列表全空（**null 与 `[]` 都算空**）。
 * ⇒ ★★ 半空形状下它返回 `false` —— 调用方据此知道「有的下拉有值、有的没有」，
 *   而不是笼统地报「没有筛选项」。
 */
export function sessionAnalyticsFilterOptionsIsEmpty(o: SessionAnalyticsFilterOptions): boolean {
  return filterOptionList(o.models).length === 0 && filterOptionList(o.providers).length === 0
}

/** ★★ (3) 任一列表非空。 */
export function sessionAnalyticsFilterOptionsHasAnyValue(o: SessionAnalyticsFilterOptions): boolean {
  return filterOptionList(o.models).length > 0 || filterOptionList(o.providers).length > 0
}

/**
 * ★★★★ (9) 该列表是否**按值升序且无重复**（`ORDER BY model` + `DISTINCT`）。
 *
 * ⇒ ⇒ 这是**客户端决策**（要不要在渲染前自己再排一遍），**不是响应校验**：
 *   对每一个真实后端响应它都成立，所以它永远不会在生产里变红；
 *   它防的是「网关版本漂移 / 中间层改了 body」。
 * ⇒ ★★ 与兄弟端点的对照很关键：那边的排序键 `last_at` **不进 JSON** ⇒ 不可观测 ⇒
 *   所以那一侧**不能**提供任何顺序判据；这一侧可以。
 */
export function filterOptionValuesAreSortedUnique(values: readonly string[]): boolean {
  for (let i = 1; i < values.length; i++) {
    const prev = values[i - 1] as string
    const cur = values[i] as string
    if (!(prev < cur)) return false
  }
  return true
}