import { req, type RequestOptions } from './client'

/**
 * turnsFilterOptions.ts — 轮次列表页的筛选框可选值（2026-10-08，第九十六批）。
 *
 * GET /api/admin/turns/sessions/filter-options
 *
 * - **注册**：`admin/handler.go:1277`
 *   ```go
 *   mux.HandleFunc("/api/admin/turns/sessions/filter-options", admin(h.handleTurnsFilterOptions))
 *   ```
 *   ⇒ ★★ **admin 档**（`h.admin` = `AdminMiddleware`，`handler.go:880`）⇒ 抽屉席不设 `requiresRole`。
 * - **实现**：`admin/turns_filter_options.go:49-130`（handler）· `:36-46`（`TurnsFilterOptionsResponse`）
 *   · `:136-154`（三段查询拼装）· `:157-181`（`runFilterOptionQueries`）。
 * - **桌面调用方**：`web/src/api/turns.ts:204-206` `listTurnsFilterOptions()` —— **不带任何参数**；
 *   类型 `TurnsFilterOptions`（`:147-157`）；消费方 `web/src/components/turns/TurnsFilterBar.vue:139-184`。
 * - **不在** `cmd/gateway/maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 * - **同族列表端点** `/api/admin/turns/sessions`（`handler.go:1275`）已由
 *   `web-mobile/src/api/turnsSessions.ts` 覆盖 —— 本文件只补 filter-options。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十二件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **★ 桌面类型多声明了一个后端从不返回的键 `api_keys`。**
 *     Go 结构体 `TurnsFilterOptionsResponse`（`:37-46`）**只有 8 个键**，
 *     而桌面 `TurnsFilterOptions`（`turns.ts:147-157`）有 **9 个** —— 多出 `api_keys`。
 *     ⇒ ⇒ `'api_keys' in resp` **恒假** ⇒ 恒真判据，按纪律删除，契约由注释承担。
 *     ⇒ ★★★ **后果是可见的**：桌面 `TurnsFilterBar.vue:171` 写的是
 *     `v-for="opt in filterOptions.api_keys || []"` ——
 *     那句 `|| []` 就是作者撞到这个缺失键后打的补丁，
 *     ★★ **于是那把「API Key」下拉在桌面上永远是空的**。
 *     ⇒ ⇒ 移动端**不要**把它当一个有数据的筛选项渲染。
 *
 * (2) ★★★★★ **8 个键全部恒在、无 `omitempty`**，且**全部被强制非 nil**：
 *     `*targets[i] = []string{}`（`:166`，在取第一行**之前**执行）
 *     ⇒ ⇒ **空维度是 `[]` 不是 `null`** —— 与批 91 的 `model_routes` 三态、
 *       批 92 的 `events` 是同一族，但成因不同（这里是显式赋空切片）。
 *     ⇒ ⇒ 客户端可以直接写 `resp.models.length`。
 *
 * (3) ★★★★★ **★ 文件头注释说「基于近 30 天」，代码里 5 个维度根本没有时间窗。**
 *     头注 `:5` 写「基于近 30 天实际使用数据」、`:19` 写「每个维度按最近活跃倒序取前 20」。
 *     而 `lastActiveSource`（`:136-140`）拼的是
 *     `SELECT <v> AS v, MAX(ss.first_request_at) AS last_at FROM session_summaries ss<…> WHERE <cond><tenantWhere> GROUP BY 1`
 *     —— **`<cond>` 与 `<tenantWhere>` 之间没有任何时间条件**。
 *     ⇒ ★★★ **只有走 `session_turns` 的 3 个维度（models / providers / status_codes）有 30 天窗口**
 *       （`lastActiveTurnQuery:150-153` 里的 `t.ts > NOW() - INTERVAL '30 days'`）。
 *     ⇒ ⇒ **projects / tasks / owners / clients / tags 这 5 个维度是「全历史里最近活跃的」，
 *       不是「近 30 天的」** ⇒ 注释与代码矛盾，**注释不是契约**。
 *
 * (4) ★★★★ **响应里没有任何时间戳** ⇒ 「按最近活跃倒序」这件事**在响应里根本不可观测**。
 *     `last_at` 只出现在 SQL 的 ORDER BY 里，不进 JSON
 *     ⇒ ⇒ 客户端**无法**校验顺序，本模块**不提供**任何顺序判据。
 *
 * (5) ★★★★ **`ORDER BY last_at DESC LIMIT 20` 没有 tiebreak**
 *     （`:144` 与 `:152`）⇒ ★★ 同一 `last_at` 的多个值之间顺序未定义。
 *
 * (6) ★★★★ **每个维度上限恒为 20**（`turnsFilterOptionsLimit = 20`，`:33`）
 *     ⇒ ⇒ 客户端可用它判断「下拉可能被截断、还有更多」，但**这是后端保证，不是可断言的性质**
 *       （恒真 ⇒ 不做校验，只给客户端一个决策函数 `turnsFilterOptionIsTruncated`）。
 *
 * (7) ★★★★ **`status_codes` 的元素是数字串，不是数字** ——
 *     SQL 选的是 `t.status_code::text`（`:122`），而 `session_turns.status_code` 是 **`integer`**
 *     （`deploy/sql/schemas/baseline/01-schema.sql` 的 `session_turns` 建表）
 *     ⇒ ⇒ 元素形如 `"200"`、`"0"`。
 *     ⇒ ★★★ **而桌面的查询参数类型写的是 `number`**（`turns.ts:164`）、
 *       `TurnsFilterBar.vue:155` 把**字符串**直接绑成下拉值 ⇒ **桌面的类型是错的**
 *       （只因为 `String(params.status_code)` 才没出事）。
 *     ⇒ ⇒ 列表端点侧按原样比较 `ft.status_code = $N`（`turns_sessions.go:439-442`），
 *       字符串参数能被 Postgres 推断成整数，所以「原样回传」是安全的。
 *
 * (8) ★★★★ **`status_codes` 是唯一没有 `!= ''` 过滤的维度**
 *     （`:122` 只有 `t.status_code IS NOT NULL`，其余 7 个都是 `IS NOT NULL AND … != ''`）
 *     ⇒ ★★ 对 int 列做 `::text` 后不可能为空串，所以**无害**，但这是个不对称点。
 *
 * (9) ★★★★ **`clients` 是 `client_id ∪ application_code` 的 UNION**（`:95-101`），
 *     不是单一列 ⇒ ⇒ 同一个下拉里混着两种来源的标识，**不能**假定它只是客户端 ID。
 *
 * (10) ★★★★ **`tags` 走 `LATERAL UNNEST(ss.user_tags)`**（`:103-104`），
 *     且它的 `cond` 是 `"1=1"`（唯一一个不带空值过滤的取值条件）⇒ **数组里的空串会原样出现在结果里**。
 *
 * (11) ★★★★ **★ 每条可达路径都有租户过滤，`tenantID != ""` 那个分支是死路。**
 *     ```go
 *     if IsTenantAdmin(r) { tenantID = GetTenantID(r) } else { tenantID = tenantFromQueryOrContext(r) }
 *     if tenantID != "" { tenantWhere = " AND ss.tenant_id = $1" … }
 *     ```
 *     `GetTenantID` 在 auth 缺失时**兜底 `"default"`**（`context.go:37-42`）
 *     ⇒ 恒非空；`tenantFromQueryOrContext`（`session_turns_v2.go:889-905`）的末尾也回落 `GetTenantID`
 *     ⇒ ⇒ **响应永远是租户内的，不会出现跨租户数据。**
 *     ⇒ ★★★ **但 `IsTenantAdmin` 要求 `Role == "tenant_admin"` 精确匹配**（`context.go:45-48`）
 *       ⇒ **super_admin 走 `else` 分支**、才有机会用 `?tenant=` / `?tenant_id=` / `X-Tenant-ID` 覆盖
 *       ⇒ ★★ 而**桌面从不传任何参数**（`turns.ts:205`）⇒ 桌面上即使是 super_admin 也只看自己那一个租户。
 *
 * (12) ★★★ **★ 同名不同形的兄弟端点：`/api/admin/session-analytics/filter-options`。**
 *     `session_analytics_handler.go:728-729` 在一个**分发器**里分派它（第六种注册形态），
 *     而**不是** `mux.HandleFunc` 直挂；它的响应**只有 2 个键** `{models, providers}`
 *     （`session_analytics_top.go:49-50`），租户口径是 `effectiveScopeTenant(r)`，
 *     503 文案是 **`"db not available"`**（本端点是 `"database not configured"`），
 *     超时 10s（本端点 12s）。
 *     ⇒ ⇒ ★★★ **叶名相同、键数差 4 倍、档位来源不同、503 文案不同 —— 不可互相套用。**
 *
 * ★★ **本模块明确声明的校验边界**：
 *   校验顶层 8 个恒在键、每个维度是**字符串数组**（两分支：不是数组 / 元素不是字符串）；
 *   为 (1)(2)(6)(7) 提供判据或决策函数。
 *   ★ **不校验** 任何维度的取值域（由用户数据自由决定）。
 *   ★ **不提供** 「顺序非升序」判据（(4)：响应里没有 `last_at`，顺序不可观测）。
 *   ★ **不提供** 「两槽位…」「键数 ≤ 20」等恒真判据（后端由 `LIMIT` 与结构保证）。
 *   ★ **不提供** `query/target count mismatch` 那条分支的判据（5 vs 5、3 vs 3 是字面量，不可达）。
 */
export const TURNS_FILTER_OPTIONS_PATH = '/api/admin/turns/sessions/filter-options'

/** ★★ (1) 后端结构体 `TurnsFilterOptionsResponse`（`:37-46`）的真实 8 键，**桌面少一个、多一个**。 */
export const TURNS_FILTER_OPTION_KEYS = [
  'projects',
  'tasks',
  'owners',
  'clients',
  'tags',
  'models',
  'providers',
  'status_codes',
] as const

/** ★★ (6) 每个维度的上限，抄自 `turnsFilterOptionsLimit = 20`（`:33`）。 */
export const TURNS_FILTER_OPTION_LIMIT = 20

/**
 * ★★★★ (3) **只有 3 个维度用得到这个窗口**：`models` / `providers` / `status_codes`
 * （`lastActiveTurnQuery:150-153`）。`projects` / `tasks` / `owners` / `clients` / `tags`
 * **没有时间过滤**（`lastActiveSource:136-140`）⇒ ★ **不要**把它当「整个响应的时间窗」。
 */
export const TURNS_TURN_DIMENSION_WINDOW_DAYS = 30

/** ★★ (9) `clients` 维度的两个来源列（`turns_filter_options.go:97` 与 `:100`）。 */
export const TURNS_CLIENT_VALUE_SOURCES = ['client_id', 'application_code'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface TurnsFilterOptions {
  /** ★ (3) **无 30 天窗口** —— 全历史里最近活跃的前 20 个。 */
  projects: string[]
  /** ★ (3) **无 30 天窗口**。 */
  tasks: string[]
  /** ★ (3) **无 30 天窗口**。 */
  owners: string[]
  /** ★★ (9) `client_id` ∪ `application_code`，**两种来源混在一个下拉里**。 */
  clients: string[]
  /** ★ (3)(10) **无 30 天窗口**；`cond` 是 `1=1` ⇒ 空串也会出现。 */
  tags: string[]
  /** ★★ 只有这三个维度有 30 天窗口。 */
  models: string[]
  providers: string[]
  /** ★★★ (7) 元素是 **`integer::text`** 的数字串（`"200"`），**不是数字**。 */
  status_codes: string[]
}

export interface TurnsApiKeyOption {
  id: number
  label: string
}

// ── fetch ───────────────────────────────────────────────────────────────────

/**
 * GET `/api/admin/turns/sessions/filter-options`。
 * ★ 后端 `:50-53` **只接受 GET**，其余方法一律 405（`{"error":{"detail":…}}`）。
 * ★ `h.db == nil` ⇒ **503 `database not configured`**（`:54-57`）——
 *   ★ 注意兄弟端点 `session-analytics/filter-options` 的 503 文案是 `db not available`，**两者不同**。
 * ★ 查询失败 ⇒ **500 `query filter options failed`**（`:107` 与 `:125`）。
 *   `rows.Err()` **是被检查的**（`:176`）⇒ 与批 91 的「半修吞错」不同，这里 5xx 是真的 5xx。
 * ★ 无查询参数。★★ (11) 想跨租户看必须自己加 `?tenant=`（或 `?tenant_id=` / `X-Tenant-ID` 头），
 *   **桌面从不加** ⇒ 它的作用域就是调用者自己那一个租户。
 */
export function fetchTurnsFilterOptions(options?: RequestOptions): Promise<TurnsFilterOptions> {
  return req<unknown>('GET', TURNS_FILTER_OPTIONS_PATH, undefined, options).then(unwrapTurnsFilterOptions)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

export function unwrapTurnsFilterOptions(resp: unknown): TurnsFilterOptions {
  const d = requireObject(resp, '轮次筛选项')
  requireKeys(d, TURNS_FILTER_OPTION_KEYS, '轮次筛选项')
  for (const k of TURNS_FILTER_OPTION_KEYS) {
    requireStringArray(d[k], `轮次筛选项 的 ${k}`)
  }
  // ★ 不拒绝未知键：后端将来加维度是正常演进；
  //   ★★ 但 `api_keys` 是**桌面凭空多出来的**键，见文件头 (1) 与 apiKeyFilterOptions。
  return d as unknown as TurnsFilterOptions
}

function requireStringArray(v: unknown, where: string): void {
  if (!Array.isArray(v)) {
    const actual = v === null ? 'null' : typeof v
    throw new Error(`${where} 不是数组（实得 ${actual}）`)
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

// ── (1) 桌面那个幽灵键 ──

/**
 * ★★★★★ (1) **「API Key」下拉的可选值 —— 恒为 `[]`**。
 *
 * ★★ 后端结构体**没有 `api_keys` 这个键**（`:37-46` 共 8 键），
 *   桌面 `TurnsFilterOptions` 多声明了它（`turns.ts:157`），
 *   并在 `TurnsFilterBar.vue:171` 写 `filterOptions.api_keys || []` 打了补丁。
 * ⇒ ⇒ **移动端应当不渲染这把下拉**，而不是渲染一把永远空的。
 * ⇒ ★ 本函数是**容错取值**：它在响应里没有该键、或该键不是数组、或元素形状不对时，一律给 `[]`。
 *   ⇒ ★★ **它刻意不抛错** —— 因为「键不存在」是后端的常态，不是响应形状错误。
 *   ⇒ ★ 其输入含**后端不可达**的取值（见注释），用例只用来固定容错行为，不是后端契约判据。
 */
export function apiKeyFilterOptions(opts: TurnsFilterOptions): TurnsApiKeyOption[] {
  const raw = (opts as unknown as { api_keys?: unknown }).api_keys
  if (!Array.isArray(raw)) return []
  const out: TurnsApiKeyOption[] = []
  for (const item of raw) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) continue
    const o = item as Record<string, unknown>
    if (typeof o['id'] !== 'number' || typeof o['label'] !== 'string') continue
    out.push({ id: o['id'], label: o['label'] })
  }
  return out
}

// ── (2)(6) 空与截断 ──

/** ★★ (2) 8 个维度全空 ⇒ 这是一个「还没被使用过」的网关（不是故障）。 */
export function turnsFilterOptionsIsEmpty(o: TurnsFilterOptions): boolean {
  return TURNS_FILTER_OPTION_KEYS.every((k) => o[k].length === 0)
}

/** ★★ (6) 任一维度非空。 */
export function turnsFilterOptionsHasAnyValue(o: TurnsFilterOptions): boolean {
  return TURNS_FILTER_OPTION_KEYS.some((k) => o[k].length > 0)
}

/**
 * ★★★★ (6) 该维度**可能被截断**（`LIMIT 20`）。
 * ⇒ ⇒ 客户端据此提示「还有更多，直接搜索吧」；
 *   ⚠️ 它**不能**用来断言「响应合法」—— 后端保证了上限，这是**客户端的决策**，不是校验。
 * ⇒ ★ 区分格在**正好 20**（`>=` 与 `>` 只在这里不同，而 `LIMIT 20` 完全能返回正好 20）。
 */
export function turnsFilterOptionIsTruncated(values: readonly string[]): boolean {
  return values.length >= TURNS_FILTER_OPTION_LIMIT
}

// ── (7) status_code 的类型落差 ──

/**
 * ★★★★ (7) 把下拉里的**数字串**转成列表端点参数 `status_code` 需要的形态。
 *
 * ⇒ ★★ `status_codes` 的元素来自 `t.status_code::text`，而列是 `integer`
 *   ⇒ 元素形如 `"200"`；**桌面却把参数类型写成 `number`**（`turns.ts:164`）。
 * ⇒ ⇒ 这是**客户端侧的显式转换，不是后端契约**：
 *   列表端点按原样比较（`turns_sessions.go:439-442`），传字符串也能被 Postgres 推断成整数，
 *   但让类型显式收敛比「依赖推断」可靠。
 * ⇒ ★ 返回 `null` 表示**这个选项值不是合法状态码**，调用方应当忽略它而不是传 `NaN`。
 * ⇒ ★★★ **只认十进制整数字面量**（`/^-?\d+$/`），先 `trim` 再判。
 *   这样做同时堵掉三个坑，每一个都是「把坏值静默变成好值」：
 *   - `Number('')` 与 `Number('   ')` **都是 `0`**，而 `0` 是合法整数
 *     ⇒ 空选项会被静默转成状态码 `0`；
 *   - `parseInt('200abc')` 是 **200**、`parseInt('1e3')` 是 **1**
 *     ⇒ 前缀截断会把两个非法值变成两个**看起来很合理**的状态码；
 *   - `Number('0x10')` 是 **16**、`Number('200.5')` 是 **200.5**
 *     ⇒ 十六进制与小数都不是 `integer::text` 能产出的形态。
 *   ⇒ ★★ `^-?\d+$` 保留可选负号：列是 `integer` 且**没有** `>= 0` 的过滤，
 *     所以 `-1` 这类取值在真后端上是可达的，不能当成非法挡掉。
 */
export function statusCodeFilterValue(v: string): number | null {
  const t = v.trim()
  if (!/^-?\d+$/.test(t)) return null
  return Number(t)
}