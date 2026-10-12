import { req, type RequestOptions } from './client'

/**
 * routingPolicy.ts — 路由策略配置面（2026-10-07，第七十七批）。
 *
 * 四个 GET 端点，注册在 `admin/handler.go:1200-1216`：
 *
 * | 端点 | 档位 | 行号 |
 * |---|---|---|
 * | `GET /api/routing/policy` | `h.superAdmin(...)` | `:1200` |
 * | `GET /api/routing/featured` | `h.superAdmin(...)` | `:1201` |
 * | `GET /api/routing/scoring-weights` | `h.superAdmin(...)` | `:1215` |
 * | `GET /api/routing/featured-models` | `admin(...)` | `:1216` |
 *
 * ⇒ ★ **一个族里三档一档**：`featured-models` 是 admin 档（tenant_admin 可用，
 *   抽屉席不设 `requiresRole`），其余三个是 **superAdmin** 档（tenant_admin 403）。
 *   抽屉席要放后三个**必须**设 `requiresRole: 'super_admin'`
 *   并同步 `src/components/shell/AppDrawer.spec.ts` 的白名单。
 *   ⇒ ★ 绝不能按「同前缀都是一类」来定档，必须逐条看注册。
 *
 * ## ★★★★★ 本族最要紧的七件事
 *
 * (1) ★★★★★ **`policy` 的响应形状是 `row_to_json(rp)` ⇒ 形状由表决定，不由代码决定。**
 *      `routing.go:2527` 的 `SELECT row_to_json(rp)::text FROM routing_policy rp`
 *      ⇒ **21 个列全是响应键**，加一个 DDL 就要加一个客户端键。
 *      建表见 `deploy/sql/schemas/baseline/01-schema.sql`：
 *      ```sql
 *      CREATE TABLE public.routing_policy (
 *          id smallint DEFAULT 1 NOT NULL,
 *          tenant_id text DEFAULT 'default'::text NOT NULL,
 *          weights_json jsonb DEFAULT '{}'::jsonb NOT NULL,
 *          sticky_ttl_seconds integer DEFAULT 1800 NOT NULL,
 *          local_bonus numeric(4,3) DEFAULT 0.000 NOT NULL,
 *          notes text,                                    -- ← 可空
 *          updated_at timestamptz DEFAULT now() NOT NULL,
 *          algorithm_version smallint DEFAULT 2,           -- ← 可空
 *          ...
 *          CONSTRAINT routing_policy_id_check CHECK ((id = 1)),
 *      );
 *      ```
 *      ⇒ ★ **7 个 NOT NULL + 14 个可空**；`row_to_json` 对可空列输出 **`null` 而不是省略键**
 *      ⇒ 这是本仓第**八**种 nil 编码，且与第七十二批「同一载荷里两个数组键编码相反」同族。
 *
 * (2) ★★★★★ **空对象 `{}` 是三合一语义。**
 *      `:2533`：`if err := row.Scan(&raw); err != nil || raw == "" { writeJSON(w, 200, map[string]any{}); return }`
 *      ⇒ 「没有这一行」「查询失败」「文本为空」**三种情况都回 HTTP 200 + `{}`**。
 *      ⇒ ★ 客户端**不能**把 `{}` 渲染成「策略未配置」的确定结论。
 *
 * (3) ★★★★ **`scoring-weights` 的降级完全不可辨。**
 *      `getScoringWeights`（`:4026-4055`）：
 *      ```go
 *      if err != nil || len(weightsJSON) == 0 { return defaultWeights }
 *      if err := json.Unmarshal(...); err != nil { return defaultWeights }
 *      for k, v := range defaultWeights { if _, ok := weights[k]; !ok { weights[k] = v } }
 *      ```
 *      ⇒ **查询失败、解析失败、缺键**三种都产出同一份默认值，
 *        且响应里**没有任何标记**能说「这是兜底值」。
 *
 * (4) ★★★★ **`scoring-weights` 的数字键是开放形状。**
 *      `scoringWeightsDisplayOnlyPayload`（`:3919-3927`）把 jsonb 里**任意**键摊平，
 *      再加两个披露键：
 *      ```go
 *      out["display_only"] = true
 *      out["note"] = "these weights only affect /api/routing/resolve and /api/routing/score-details previews, not live routing"
 *      ```
 *      ⇒ ★ 只有五个键是**保证存在**的（默认值回填），其余随 DB 内容增减。
 *      ⇒ ★ `display_only` 是**硬编码常量** ⇒ 校验它的取值是**恒真判据**，只校类型。
 *
 * (5) ★★★★ **`policy` / `featured` / `scoring-weights` 三个都硬编码 `tenant_id = 'default'`。**
 *      `routing.go:2529` / `:2592` / `:4038`。
 *      ⇒ ★ 对照第四个端点：`featured-models` 走 `EffectiveTenantIDAll(r)`（`context.go:69`），
 *        tenant_admin 拿自己的租户、super_admin 拿全租户合计 ⇒ **它才是对的**。
 *      ⇒ 「同一个前缀下不同端点有不同隔离口径」—— 本仓第四次出现（对比第七十三批、
 *        第七十五批、第七十六批都是**全族都不隔离**，这里是**同族两个口径**）。
 *
 * (6) ★★★ **`featured_models` 恒为数组，查询失败也回空数组。**
 *      `:2591` 的 `COALESCE(featured_models, ARRAY[]::TEXT[])` 加 `:2598` 的 nil 兜底
 *      ⇒ **不会是 null**；但 `:2594-2597` 查询失败只 `slog.Warn` 然后 `models = []string{}`
 *      ⇒ 「没配精选模型」与「查不出来」**同形**。
 *
 * (7) ★★★ **`featured-models` 的 `standardized_name` 恒等于 `name`。**
 *      `routing.go:4081-4082` 两个字段都取 `p.CanonicalName`
 *      ⇒ 客户端不该把它们渲染成两种不同的东西（也不该拿它做「标准化前/后」的对照）。
 */

/** `routing.go:3912` 的 note 原文。 */
export const SCORING_WEIGHTS_DISPLAY_ONLY_NOTE =
  'these weights only affect /api/routing/resolve and /api/routing/score-details previews, not live routing'

/** `getScoringWeights`（`:4027-4033`）的五个保证键。 */
export const SCORING_WEIGHTS_DEFAULT_KEYS = [
  'price',
  'session_load',
  'failure_penalty',
  'default_price_cny',
  'default_price_usd',
] as const

/** `getScoringWeights`（`:4027-4033`）的默认值。 */
export const SCORING_WEIGHT_DEFAULTS = {
  price: 10,
  session_load: 5,
  failure_penalty: 20,
  default_price_cny: 5.0,
  default_price_usd: 5.0,
} as const

/** ★ routing_policy 里**恒不为 null** 的七列。 */
export const ROUTING_POLICY_REQUIRED_KEYS = [
  'id',
  'tenant_id',
  'weights_json',
  'sticky_ttl_seconds',
  'local_bonus',
  'updated_at',
  'transient_fail_threshold',
] as const

/** ★ routing_policy 里**键恒在、值可为 null** 的十四列（`row_to_json` 不省略可空列）。 */
export const ROUTING_POLICY_NULLABLE_KEYS = [
  'notes',
  'algorithm_version',
  'retry_per_credential',
  'tier_fallback_max',
  'slot_soft_limit_ratio',
  'slot_hard_limit_ratio',
  'slot_wait_max_ms',
  'circuit_open_seconds',
  'circuit_failure_threshold',
  'circuit_max_open_seconds',
  'featured_models',
  'stats_window_minutes',
  'stats_update_interval_seconds',
  'scoring_weights_json',
] as const

/** routing_policy 的全部 21 列（= `row_to_json` 的输出键集）。 */
export const ROUTING_POLICY_KEYS = [
  ...ROUTING_POLICY_REQUIRED_KEYS,
  ...ROUTING_POLICY_NULLABLE_KEYS,
] as const

/** `scoringWeightsDisplayOnlyPayload` 加上的两个披露键。 */
export const SCORING_WEIGHTS_DISCLOSURE_KEYS = ['display_only', 'note'] as const

/** featured-models 的四个元素键（`routing.go:4075-4080`，无 omitempty）。 */
export const FEATURED_MODEL_KEYS = [
  'name',
  'standardized_name',
  'count',
  'source',
] as const

/**
 * ★ `source` 是**两个字面量**决定的封闭枚举
 * （`routing.go:2862` 的 `"policy"` 与 `:2903` 的 `"usage"`），
 * 与第七十四批那种「只判 `!= ""`」的开放字符串不同 ⇒ 这里**可以**校验取值。
 */
export const FEATURED_MODEL_SOURCES = ['policy', 'usage'] as const
export type FeaturedModelSource = (typeof FEATURED_MODEL_SOURCES)[number]

/** `routing.go:4057` 的 limit 实参 20；只作用于 usage 来源（`:2865-2867`）。 */
export const FEATURED_MODELS_USAGE_LIMIT = 20

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * ★ `row_to_json(rp)` 的产物：键集由表决定。
 * 七个必填键 + 十四个「键在但可为 null」的键。
 */
export interface RoutingPolicyRow {
  id: number
  tenant_id: string
  weights_json: Record<string, unknown>
  sticky_ttl_seconds: number
  local_bonus: number
  updated_at: string
  transient_fail_threshold: number
  notes: string | null
  algorithm_version: number | null
  retry_per_credential: number | null
  tier_fallback_max: number | null
  slot_soft_limit_ratio: number | null
  slot_hard_limit_ratio: number | null
  slot_wait_max_ms: number | null
  circuit_open_seconds: number | null
  circuit_failure_threshold: number | null
  circuit_max_open_seconds: number | null
  featured_models: string[] | null
  stats_window_minutes: number | null
  stats_update_interval_seconds: number | null
  scoring_weights_json: Record<string, unknown> | null
}

export interface RoutingFeaturedResponse {
  /** ★ 恒数组，查询失败时是空数组（文件头第 (6) 条）。 */
  featured_models: string[]
}

/** ★ 开放形状：五个保证的数字键 + jsonb 里的任意键 + 两个披露键。 */
export interface RoutingScoringWeights {
  price: number
  session_load: number
  failure_penalty: number
  default_price_cny: number
  default_price_usd: number
  display_only: boolean
  note: string
  /** ★ jsonb 里的其它数字键（PATCH 会忽略它们）。 */
  [extra: string]: number | boolean | string
}

export interface FeaturedModelEntry {
  name: string
  /** ★ 恒等于 `name`（`routing.go:4081-4082`）。 */
  standardized_name: string
  count: number
  source: FeaturedModelSource
}

export interface RoutingFeaturedModelsResponse {
  /** ★ 恒数组（`make([]featuredModel, 0, len(popular))`）。 */
  models: FeaturedModelEntry[]
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

/** ★ **superAdmin 档**（`handler.go:1200`）：tenant_admin 会拿到 403。 */
export function fetchRoutingPolicy(options?: RequestOptions): Promise<RoutingPolicyRow | null> {
  return req<unknown>('GET', '/api/routing/policy', undefined, options).then(unwrapRoutingPolicy)
}

/** ★ **superAdmin 档**（`handler.go:1201`）。 */
export function fetchRoutingFeatured(
  options?: RequestOptions,
): Promise<RoutingFeaturedResponse> {
  return req<unknown>('GET', '/api/routing/featured', undefined, options).then(
    unwrapRoutingFeatured,
  )
}

/** ★ **superAdmin 档**（`handler.go:1215`）。 */
export function fetchRoutingScoringWeights(
  options?: RequestOptions,
): Promise<RoutingScoringWeights> {
  return req<unknown>('GET', '/api/routing/scoring-weights', undefined, options).then(
    unwrapRoutingScoringWeights,
  )
}

/** **admin 档**（`handler.go:1216`）：tenant_admin 可用。 */
export function fetchRoutingFeaturedModels(
  options?: RequestOptions,
): Promise<RoutingFeaturedModelsResponse> {
  return req<unknown>('GET', '/api/routing/featured-models', undefined, options).then(
    unwrapRoutingFeaturedModels,
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * ★★ `{}` 与整行是**两种合法形状**（文件头第 (2) 条）：
 * `routing.go:2533` 把「没有行」「查询失败」「文本为空」都写成 `{}`。
 * ⇒ 这里返回 `null` 表示「服务端给的是空对象」，**不表示「查询失败」** ——
 *   后端根本没给客户端分辨的余地。
 */
export function unwrapRoutingPolicy(resp: unknown): RoutingPolicyRow | null {
  const d = requireObject(resp, '路由策略')
  // ★ 反向检测：同前缀下 `/api/routing/featured` 只回 `featured_models`
  if ('status' in d || ('featured_models' in d && !('tenant_id' in d))) {
    throw new Error('路由策略 拿到的是别的形状（featured 响应？）')
  }
  if (Object.keys(d).length === 0) return null
  requireKeys(d, ROUTING_POLICY_REQUIRED_KEYS, '路由策略')
  if (typeof d.id !== 'number') throw new Error('路由策略 的 id 不是数字')
  if (typeof d.tenant_id !== 'string') throw new Error('路由策略 的 tenant_id 不是字符串')
  for (const k of [
    'sticky_ttl_seconds',
    'local_bonus',
    'transient_fail_threshold',
  ] as const) {
    if (typeof d[k] !== 'number') throw new Error(`路由策略 的 ${k} 不是数字`)
  }
  if (typeof d.updated_at !== 'string') throw new Error('路由策略 的 updated_at 不是字符串')
  requireJsonObject(d.weights_json, '路由策略 的 weights_json')
  // ★ 十四个可空列：键必须**在**，值可以是 null（row_to_json 不省略可空列）
  for (const k of ROUTING_POLICY_NULLABLE_KEYS) {
    if (!(k in d)) throw new Error(`路由策略 缺键 ${k}（可空列也要有键）`)
  }
  requireNullableString(d.notes, '路由策略 的 notes')
  for (const k of [
    'algorithm_version',
    'retry_per_credential',
    'tier_fallback_max',
    'slot_soft_limit_ratio',
    'slot_hard_limit_ratio',
    'slot_wait_max_ms',
    'circuit_open_seconds',
    'circuit_failure_threshold',
    'circuit_max_open_seconds',
    'stats_window_minutes',
    'stats_update_interval_seconds',
  ] as const) {
    requireNullableNumber(d[k], `路由策略 的 ${k}`)
  }
  requireNullableStringArray(d.featured_models, '路由策略 的 featured_models')
  requireNullableJsonObject(d.scoring_weights_json, '路由策略 的 scoring_weights_json')
  return d as unknown as RoutingPolicyRow
}

export function unwrapRoutingFeatured(resp: unknown): RoutingFeaturedResponse {
  const d = requireObject(resp, '路由精选模型')
  requireKeys(d, ['featured_models'], '路由精选模型')
  if (!Array.isArray(d.featured_models)) {
    throw new Error('路由精选模型 的 featured_models 不是数组')
  }
  d.featured_models.forEach((m, i) => {
    if (typeof m !== 'string') {
      throw new Error(`路由精选模型 的 featured_models[${i}] 不是字符串`)
    }
  })
  return d as unknown as RoutingFeaturedResponse
}

export function unwrapRoutingScoringWeights(resp: unknown): RoutingScoringWeights {
  const d = requireObject(resp, '路由打分权重')
  // ★ 五个保证键由默认值回填（`getScoringWeights:4049-4053`）⇒ 恒在
  requireKeys(d, SCORING_WEIGHTS_DEFAULT_KEYS, '路由打分权重')
  for (const k of SCORING_WEIGHTS_DEFAULT_KEYS) {
    if (typeof d[k] !== 'number') throw new Error(`路由打分权重 的 ${k} 不是数字`)
  }
  requireKeys(d, SCORING_WEIGHTS_DISCLOSURE_KEYS, '路由打分权重')
  // ★ `display_only` 是硬编码常量 ⇒ 只校类型，校验取值是恒真判据
  if (typeof d.display_only !== 'boolean') {
    throw new Error('路由打分权重 的 display_only 不是布尔值')
  }
  if (typeof d.note !== 'string') throw new Error('路由打分权重 的 note 不是字符串')
  // ★ jsonb 里的其它键：布尔或字符串或数字都合法（后端只把它们原样摊平）
  for (const [k, v] of Object.entries(d)) {
    if ((SCORING_WEIGHTS_DEFAULT_KEYS as readonly string[]).includes(k)) continue
    if ((SCORING_WEIGHTS_DISCLOSURE_KEYS as readonly string[]).includes(k)) continue
    const t = typeof v
    if (t !== 'number' && t !== 'boolean' && t !== 'string') {
      throw new Error(`路由打分权重 的额外键 ${k} 类型不支持（${t}）`)
    }
  }
  return d as unknown as RoutingScoringWeights
}

export function unwrapRoutingFeaturedModels(resp: unknown): RoutingFeaturedModelsResponse {
  const d = requireObject(resp, '路由精选模型（动态）')
  requireKeys(d, ['models'], '路由精选模型（动态）')
  if (!Array.isArray(d.models)) throw new Error('路由精选模型（动态） 的 models 不是数组')
  d.models.forEach((m, i) => {
    const o = requireObject(m, `路由精选模型（动态） 的 models[${i}]`)
    requireKeys(o, FEATURED_MODEL_KEYS, `路由精选模型（动态） 的 models[${i}]`)
    if (typeof o.name !== 'string') {
      throw new Error(`路由精选模型（动态） 的 models[${i}].name 不是字符串`)
    }
    // ★ `standardized_name` 恒等于 `name`，但仍要校类型（后端两处同源不等于两处同型）
    if (typeof o.standardized_name !== 'string') {
      throw new Error(`路由精选模型（动态） 的 models[${i}].standardized_name 不是字符串`)
    }
    if (typeof o.count !== 'number') {
      throw new Error(`路由精选模型（动态） 的 models[${i}].count 不是数字`)
    }
    // ★ 这里**可以**校验取值：source 由两个字面量决定（对比第七十四批的开放字符串）
    if (typeof o.source !== 'string') {
      throw new Error(`路由精选模型（动态） 的 models[${i}].source 不是字符串`)
    }
    if (!(FEATURED_MODEL_SOURCES as readonly string[]).includes(o.source)) {
      throw new Error(
        `路由精选模型（动态） 的 models[${i}].source 不是已知来源（${String(o.source)}）`,
      )
    }
  })
  return d as unknown as RoutingFeaturedModelsResponse
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * ★★ 空对象 = 「没有这一行」或「查询失败」或「文本为空」三合一
 * （`routing.go:2533`）⇒ 解包器返回 `null`，客户端**只能说「拿不到」**，
 * 不能说「未配置」。
 */

/** ★ `tenant_id` 由 `:2529` 写死成 `'default'`（三处硬编码之一）。 */
export function routingPolicyIsDefaultTenant(row: RoutingPolicyRow | null): boolean {
  return row !== null && row.tenant_id === 'default'
}

/** ★★ 同一族里两个端点的租户口径不同（文件头第 (5) 条）。 */
export type RoutingTenantScope = 'default_only' | 'caller_scoped'

/** `policy` / `featured` / `scoring-weights` 三个都只读 default 租户。 */
export const ROUTING_DEFAULT_TENANT_ONLY_ENDPOINTS = [
  'policy',
  'featured',
  'scoring-weights',
] as const

/** `featured-models` 走 `EffectiveTenantIDAll(r)`。 */
export const ROUTING_CALLER_SCOPED_ENDPOINTS = ['featured-models'] as const

/**
 * ★★ `standardized_name` 与 `name` 同源（`routing.go:4081-4082`）⇒ 永不相等时才可疑。
 * @returns true 表示「两个字段真的对不上」，属契约漂移。
 */
export function featuredModelNameMismatch(m: FeaturedModelEntry): boolean {
  return m.standardized_name !== m.name
}

/** ★ `source === 'policy'` 的条目来自配置表，**不受 usage limit 约束**（`:2865-2867`）。 */
export function featuredModelIsPolicyPinned(m: FeaturedModelEntry): boolean {
  return m.source === 'policy'
}

/**
 * ★★ 五个保证键**全都等于默认值**时，可能是兜底结果也可能是真的配成了默认值
 * （`getScoringWeights:4040-4042`）⇒ 客户端**无法区分**，只能说「可能」。
 *
 * ★ 但**额外键能定案**：兜底分支直接 `return defaultWeights`（`:4041`/`:4046`），
 *   那张 map 只有五个键 ⇒ **响应里只要有一个额外键，就一定来自 DB**。
 */
export function routingScoringWeightsMayBeDefaults(w: RoutingScoringWeights): boolean {
  if (routingScoringWeightsExtraKeyCount(w) > 0) return false
  return SCORING_WEIGHTS_DEFAULT_KEYS.every((k) => w[k] === SCORING_WEIGHT_DEFAULTS[k])
}

/** ★★ 五键全为默认值时，可注入的额外键数量为 0（兜底值不可能带额外键）。 */
export function routingScoringWeightsExtraKeyCount(w: RoutingScoringWeights): number {
  const known = new Set<string>([
    ...SCORING_WEIGHTS_DEFAULT_KEYS,
    ...SCORING_WEIGHTS_DISCLOSURE_KEYS,
  ])
  return Object.keys(w).filter((k) => !known.has(k)).length
}

/**
 * ★★ 「查询失败 ⇒ 空数组」与「真的没配」同形（文件头第 (6) 条）⇒
 * 这个判定**只能**说「看起来是空的」，不能说「确实没配」。
 */
export function routingFeaturedLooksUnconfigured(r: RoutingFeaturedResponse): boolean {
  return r.featured_models.length === 0
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

function requireJsonObject(v: unknown, where: string): void {
  if (!isPlainObject(v)) throw new Error(`${where} 不是对象`)
}

function requireNullableJsonObject(v: unknown, where: string): void {
  if (v !== null && !isPlainObject(v)) throw new Error(`${where} 不是对象也不是 null`)
}

function requireNullableString(v: unknown, where: string): void {
  if (v !== null && typeof v !== 'string') throw new Error(`${where} 不是字符串也不是 null`)
}

function requireNullableNumber(v: unknown, where: string): void {
  if (v !== null && typeof v !== 'number') throw new Error(`${where} 不是数字也不是 null`)
}

function requireNullableStringArray(v: unknown, where: string): void {
  if (v === null) return
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组也不是 null`)
  v.forEach((x, i) => {
    if (typeof x !== 'string') throw new Error(`${where}[${i}] 不是字符串`)
  })
}