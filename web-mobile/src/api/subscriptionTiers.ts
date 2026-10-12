import { req, type RequestOptions } from './client'

/**
 * subscriptionTiers.ts — 订阅档与「每档包含哪些模块」（2026-10-08，第八十七批）。
 *
 * GET /api/admin/tiers
 *
 * - ★★★★★ **注册机制与本仓绝大多数端点不同**：它**不在** `admin/handler.go` 的
 *   `mux.HandleFunc` 里，而是在 **echo 的 group** 上：
 *   `cmd/gateway/main.go:6964`  `adminGroup := e.Group("/api/admin", requireSuperAdmin)`
 *   → `cmd/gateway/main.go:6966`  `licensing.RegisterModuleRoutes(adminGroup, licensingStore)`
 *   → `licensing/admin_api.go:33-36` `mh.RegisterRoutes(g)`
 *   → `licensing/module_api.go:22` `g.GET("/tiers", h.ListTiers)`
 *   ⇒ ★★★ **这是本仓第三种注册机制**（前两种：`admin/handler.go` 的 mux、
 *     `cmd/gateway/main.go:7070` 附近的 requestJourney 一族）。
 *     查端点时**只 grep mux 会把「已注册」误判成「死端点」** —— 本批差点就这么判。
 *   ⇒ ★★ 中间件是 `requireSuperAdmin` ⇒ **superAdmin 档**
 *     （tenant_admin 403）⇒ 抽屉席须设 `requiresRole: 'super_admin'`
 *     并同步 `src/components/shell/AppDrawer.spec.ts` 白名单。
 * - **实现**：`licensing/module_api.go:64-98`；类型 `licensing/types.go:149-155`；
 *   查询 `licensing/store_pgx.go:686-691` / `:708-711`。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十三件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **内嵌 struct 的 JSON 是**扁平**的**，不是嵌套。
 *      `:80-83` 是匿名内嵌：
 *      ```go
 *      type tierWithModules struct {
 *          SubscriptionTier            // 内嵌，无 json tag
 *          ModuleKeys []string `json:"module_keys"`
 *      }
 *      ```
 *      ⇒ ★★★ Go 把**匿名内嵌**的字段**平铺**到同一层 ⇒ 响应是
 *      `{code, name, description, price_cents, sort_order, module_keys}` **六键同层**，
 *      **不是** `{tier:{…}, module_keys:[…]}`。
 *      ⇒ ★ 这是 Go JSON 的编码细节，最容易在客户端写成「取 `.tier.code`」而全盘 undefined。
 *
 * (2) ★★★★ **响应是顶层裸数组**（`:97` `c.JSON(200, result)`），
 *      而 `result := make([]tierWithModules, 0, len(tiers))`
 *      ⇒ **无订阅档时是 `[]` 而不是 `null`**。
 *
 * (3) ★★★ 六键**恒在**（`SubscriptionTier` 五字段与 `module_keys` 都无 omitempty）。
 *
 * (4) ★★★★ **`module_keys` 在「该档没有模块」时被显式补成 `[]string{}`**
 *      （`:87-90`）：
 *      ```go
 *      keys := modulesByTier[t.Code]
 *      if keys == nil { keys = []string{} }
 *      ```
 *      ⇒ ★★★ 作者刻意兜底 ⇒ **该键恒为数组，永不为 `null`**。
 *      ⇒ ★ 这与「nil slice ⇒ JSON `null`」（批 84 的 `coverage.grain_dates`）相反。
 *
 * (5) ★★★★★ **`module_keys` 的顺序未定义** ——
 *      `ListTierModuleMaps`（`store_pgx.go:708-711`）的 SQL
 *      **既无 `ORDER BY` 也无 `WHERE`/`LIMIT`** ⇒ PG 不保证返回行序
 *      ⇒ 客户端**绝不能**依赖数组顺序，也不能用「下标 i 对应第 i 个模块」这类假设。
 *      ⇒ ★★ 对照：tiers 主查询**有** `ORDER BY sort_order`（`:690`）
 *        ⇒ **行顺序是定义的，组内顺序不是**。
 *
 * (6) ★★★★ **`module_keys` 内部无重复** ——
 *      表上有 `PRIMARY KEY (tier_code, module_key)`
 *      （`deploy/sql/schemas/baseline/01-schema.sql:22468-22469`）
 *      ⇒ 「无序」不等于「可重复」：**顺序未定义、元素唯一**。
 *
 * (7) ★★★★★ **`enabled` 列存在，但 SELECT 根本没取它** ——
 *      表定义 `enabled boolean DEFAULT true NOT NULL`（`:16835`），
 *      而 `ListSubscriptionTiers` 的 SELECT（`:688`）只取
 *      `code, name, description, price_cents, sort_order`。
 *      ⇒ ★★★ **被停用的订阅档也会出现在响应里**，且客户端**看不到 `enabled`**
 *      ⇒ 这是「**不可见的过滤维度**」。
 *      ⇒ ★★ 与批 85 的 `catalog`（WHERE `status IN (…)` **主动排除** `hidden`）
 *        正好相反：那次的过滤**可见**（status 不在响应里但有文档），
 *        这次的过滤**根本没有发生**（端点没读那一列）。
 *
 * (8) ★★★★ **`max_features` 被 SELECT 了但被丢弃** ——
 *      `ListTierModuleMaps` 取了 `COALESCE(max_features,'')`，Go 侧存进
 *      `TierModuleMap.MaxFeatures`（`json:"max_features,omitempty"`），
 *      而 `tierWithModules` **没有这个字段** ⇒ 响应里**没有** `max_features`。
 *      ⇒ ★★ 客户端拿不到「该模块在该档的配额上限」，这是**信息损失**而非过滤。
 *
 * (9) ★★★★★ **错误体形状与 `admin` 包不同** ——
 *      `licensing/internal_error.go:36-39`：
 *      ```go
 *      return c.JSON(500, map[string]string{"error": op})
 *      ```
 *      ⇒ `error` 是**字符串**；而 `admin` 包的 `writeError` 是
 *      `{"error":{"detail":msg}}`（`error` 是**对象**）。
 *      ⇒ ★★★ 这是本仓**第四种错误体形状** ⇒ 错误映射**不能**跨包复用。
 *
 * (10) ★★ 两条 500 文案各不相同：`list tiers failed`（`:67`）与
 *      `list tier module maps failed`（`:71`）⇒ 客户端**可区分**是哪一步失败。
 *
 * (11) ★★ `description` / `price_cents` / `sort_order` 在表上都是 `NOT NULL`
 *      （`:16832-16834`）⇒ 三键恒非 null；`description` 可为空串。
 *
 * (12) ★★ `code` 有 UNIQUE 约束（`:22353-22356`）⇒ 数组内 `code` 唯一。
 *
 * (13) ★★ 该端点**不按租户隔离**（读的是全局 `subscription_tiers`）。
 *
 * ★★ **本模块明确声明的校验边界**：解包器校验**顶层是数组** + 每项的
 *   **六个恒在键与类型** + `module_keys` 的元素类型；
 *   **不校验** `module_keys` 的**顺序**（后端未定义，校验它反而是错的），
 *   也不校验 `code` 的取值域（表上是自由文本，无 CHECK）。
 */

/** 响应里每项的六个恒在键（内嵌已扁平化）。见 (1)(3)。 */
export const SUBSCRIPTION_TIER_KEYS = [
  'code',
  'name',
  'description',
  'price_cents',
  'sort_order',
  'module_keys',
] as const

/** `SubscriptionTier` 自身的五键（内嵌部分）。见 (1)。 */
export const SUBSCRIPTION_TIER_CORE_KEYS = [
  'code',
  'name',
  'description',
  'price_cents',
  'sort_order',
] as const

/** ★ `:65`/`:71` 的两条 500 文案。见 (10)。 */
export const SUBSCRIPTION_TIER_ERROR_OPS = ['list tiers failed', 'list tier module maps failed'] as const

// ── 类型 ─────────────────────────────────────────────────────────────────────

export interface SubscriptionTierRow {
  /** ★ 与其余五键**同层**（内嵌已扁平化）。见 (1)。 */
  code: string
  name: string
  /** ★ 可为空串，但**不会**是 `null`（表列 NOT NULL）。见 (11)。 */
  description: string
  price_cents: number
  /** ★ 行按它升序排列（`ORDER BY sort_order`）。见 (5)。 */
  sort_order: number
  /** ★ **恒为数组，永不为 `null`**；**顺序未定义**但元素唯一。见 (4)(5)(6)。 */
  module_keys: string[]
  /**
   * ★ 表上有 `enabled` 列，但**端点不返回它** ⇒ 被停用的档也出现在这里。
   * 见 (7)。
   */
  enabled?: never
  /**
   * ★ 后端 SELECT 了 `max_features` 但 `tierWithModules` 没有该字段
   * ⇒ 响应里**没有**它。见 (8)。
   */
  max_features?: never
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/tiers`（**superAdmin 档**，走 echo group）。 */
export function fetchSubscriptionTiers(options?: RequestOptions): Promise<SubscriptionTierRow[]> {
  return req<unknown>('GET', '/api/admin/tiers', undefined, options).then(unwrapSubscriptionTiers)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

/** ★ 顶层必须是**裸数组**（空时是 `[]`）。见 (2)。 */
export function unwrapSubscriptionTiers(resp: unknown): SubscriptionTierRow[] {
  if (!Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`订阅档 响应形状不符：期望顶层裸数组，实得 ${actual}`)
  }
  const arr = resp
  for (let i = 0; i < arr.length; i++) {
    const o = requireObject(arr[i], `订阅档[${i}]`)
    // ★ 内嵌已扁平 ⇒ 六个键都在**同一层**。见 (1)。
    requireKeys(o, SUBSCRIPTION_TIER_KEYS, `订阅档[${i}]`)
    for (const k of ['code', 'name', 'description'] as const) {
      if (typeof o[k] !== 'string') throw new Error(`订阅档[${i}] 的 ${k} 不是字符串`)
    }
    for (const k of ['price_cents', 'sort_order'] as const) {
      if (typeof o[k] !== 'number') throw new Error(`订阅档[${i}] 的 ${k} 不是数字`)
    }
    // ★ module_keys 恒为数组（作者显式补 `[]`）⇒ 不用接受 null。见 (4)。
    const keys = o['module_keys']
    if (!Array.isArray(keys)) throw new Error(`订阅档[${i}] 的 module_keys 不是数组`)
    for (let j = 0; j < keys.length; j++) {
      if (typeof keys[j] !== 'string') throw new Error(`订阅档[${i}] 的 module_keys[${j}] 不是字符串`)
    }
  }
  return arr as SubscriptionTierRow[]
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

// ── (5)(6)(12) 排序与唯一性 ──

/**
 * ★★★★ 见 (5)：**行**按 `sort_order` 升序（SQL 有 `ORDER BY sort_order`）
 * ⇒ **非降序**即可（后端没在 `sort_order` 上做去重语义，同值可并列）。
 */
export function subscriptionTiersAreSortOrdered(rows: SubscriptionTierRow[]): boolean {
  for (let i = 1; i < rows.length; i++) {
    const prev = rows[i - 1]
    const cur = rows[i]
    if (prev === undefined || cur === undefined) return false
    if (prev.sort_order > cur.sort_order) return false
  }
  return true
}

// ★★ 见 (5)：**`module_keys` 的顺序未定义** —— 后端 SQL 没有 `ORDER BY`。
//   ⇒ 因此**刻意不提供**「顺序是否有序」这条判据：
//     写它就是一条**恒真判据**（恒返回 true，不表达任何后端契约）。
//   ⇒ 正确做法是**不做断言**并在调用方注释里写明「顺序未定义」；
//     变异验证要打掉的是「客户端偷偷按顺序取元素」的那种**用法**，
//     而模块层拿不到这类用法 ⇒ 模块层如实留白，注释承担契约。

/**
 * ★★★★ 见 (6)：`module_keys` 内部**元素唯一**（表上复合主键保证）。
 *
 * ⇒ ★ 与「顺序未定义」是**两件事**：无序 ≠ 可重复。
 */
export function subscriptionTierModuleKeysAreUnique(rows: SubscriptionTierRow[]): boolean {
  for (const r of rows) {
    if (new Set(r.module_keys).size !== r.module_keys.length) return false
  }
  return true
}

/** ★★★ 见 (12)：`code` 在数组内唯一（表上有 UNIQUE 约束）。 */
export function subscriptionTierCodesAreUnique(rows: SubscriptionTierRow[]): boolean {
  return new Set(rows.map((r) => r.code)).size === rows.length
}

// ── (4) module_keys 的可辨形态 ──

/** ★★★ 见 (4)：`module_keys` 为**空数组**（该档没有模块）。 */
export function subscriptionTierHasNoModules(row: SubscriptionTierRow): boolean {
  return Array.isArray(row.module_keys) && row.module_keys.length === 0
}

/** ★★ 反向：至少配了一个模块。 */
export function subscriptionTierHasModules(row: SubscriptionTierRow): boolean {
  return Array.isArray(row.module_keys) && row.module_keys.length > 0
}

// ── (7) 不可见的过滤维度 ──

/**
 * ★★★★★ 见 (7)：表上有 `enabled` 列，**但响应里没有它**
 * ⇒ 被停用的订阅档也会出现在数组里，客户端**无法区分**。
 * ⇒ ★★ 客户端因此**不能**把「出现在列表里」读成「该档已启用」。
 */
export function subscriptionTierEnabledIsNotExposed(row: SubscriptionTierRow): boolean {
  return !('enabled' in (row as unknown as Record<string, unknown>))
}

/** ★★★★ 见 (7)：内部层不该嵌套（内嵌已扁平）⇒ 顶层直接就有 `code`。 */
export function subscriptionTierIsFlat(row: SubscriptionTierRow): boolean {
  const d = row as unknown as Record<string, unknown>
  return typeof d['code'] === 'string' && !('tier' in d) && !('SubscriptionTier' in d)
}

// ── (8)(11) 取值与信息损失 ──

/** ★★★ 见 (8)：`max_features` **不在**响应里（后端 SELECT 了但结构体没有）。 */
export function subscriptionTierMaxFeaturesIsAbsent(row: SubscriptionTierRow): boolean {
  return !('max_features' in (row as unknown as Record<string, unknown>))
}

/**
 * ★★★ 见 (11)：`description` **不会是 `null`**（表列 NOT NULL），但**可以**是空串。
 *
 * ★ 保留 `=== ''` 而不是 `!row.description`：`description` 的可达集合是
 *   `{任意非空串, 空串}`（无 null/undefined）⇒ 两种写法在可达集合上**可证等价**
 *   （空串两边都 true，非空串两边都 false）。
 */
export function subscriptionTierDescriptionIsEmptyString(row: SubscriptionTierRow): boolean {
  return row.description === ''
}