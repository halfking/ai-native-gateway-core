import { req, type RequestOptions } from './client'

/**
 * routingTree.ts — 模型路由树 / 可用模型原始名单 / 熔断健康（2026-10-08）。
 *
 * 三条 admin 档只读端点（注册处 handler.go:931 / :1202-1205，tenant_admin 可用）。
 *
 * ⚠️ `model-tree` 是本批最需要小心的端点：**同一个 URL，按调用者角色返回两种
 * 形状不同的响应**（`admin/routing.go:2261` `hideCredentialDetails := IsTenantAdmin(r)`）。
 * 见下面的两组类型。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/routing/model-tree（admin 档，routing.go:2249）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★★★★★ **形状随角色变** —— 同一端点两种响应结构。
 *
 * | | super_admin | tenant_admin |
 * |---|---|---|
 * | 顶层 | `{featured, series, unmapped}` | `{featured, series, unmapped, **readonly:true**}` |
 * | variant 级 | 只有 `credentials[]`（逐个凭据详情） | `available` + `credential_count`，**无 credentials** |
 * | `available` 含义 | `credentials[].available` = **该单个凭据**是否可用 | `variants[].available` = **全部**凭据都可用（`allAvailable` 遇任一 false 即 break） |
 *
 * ★ `readonly: true` 是**唯一**能区分两侧的标记（routing.go:2516）。
 * ★★ `available` 这个名字在两侧**层级不同、语义也不同**：一个是单凭据，
 *   一个是全称判断。把它当同一个指标读会得到相反的结论。
 */

export interface ModelTreeCredential {
  credential_id: number
  credential_label: string
  credential_status: string
  provider_id: number
  provider_name: string
  available: boolean
  tier: number
  weight: number
  unit_price_in_per_1m: number | null
  unit_price_out_per_1m: number | null
  /** ★ COALESCE(mo.success_rate, 0.9) —— 编造默认值 */
  success_rate: number
  /** ★ COALESCE(mo.p95_latency_ms, 9999) —— 编造默认值 */
  p95_latency_ms: number
  currency: string | null
  availability_state: string
}

/** super_admin 分支的树节点（routing.go:2297-2330）。 */
export interface ModelTreeVariant {
  variant: string
  canonical_name: string
  tags: string[]
  credentials: ModelTreeCredential[]
}

export interface ModelTreeGeneration {
  generation: string
  variants: ModelTreeVariant[]
}

export interface ModelTreeSeries {
  series: string
  generations: ModelTreeGeneration[]
}

/** tenant_admin 分支的节点（routing.go:2462-2471）。 */
export interface ModelTreeSimpleVariant {
  variant: string
  canonical_name: string
  tags: string[]
  /** ★「**全部**凭据都可用」——不是某个凭据的可用性 */
  available: boolean
  credential_count: number
}

export interface ModelTreeSimpleGeneration {
  generation: string
  variants: ModelTreeSimpleVariant[]
}

export interface ModelTreeSimpleSeries {
  series: string
  generations: ModelTreeSimpleGeneration[]
}

/** unmapped 行：未归一化的原始模型名（`[]map[string]any`，逐字取自 :2360-2383）。 */
export interface ModelTreeUnmapped {
  raw_model_name: string
  provider_id: number
  provider_name: string
  credential_id: number
  credential_label: string
  availability_state: string
  [k: string]: unknown
}

export interface ModelTreeFullResponse {
  featured: string[]
  series: ModelTreeSeries[]
  unmapped: ModelTreeUnmapped[]
}

export interface ModelTreeSimpleResponse {
  featured: string[]
  series: ModelTreeSimpleSeries[]
  unmapped: ModelTreeUnmapped[]
  /** ★★ 两侧唯一的区分标记，tenant_admin 分支恒为 true。 */
  readonly: true
}

export const MODEL_TREE_ENVELOPE_KEYS = ['featured', 'series', 'unmapped'] as const

export function fetchModelTree(
  params?: { featuredOnly?: boolean },
  options?: RequestOptions,
): Promise<ModelTreeFullResponse | ModelTreeSimpleResponse> {
  const qs = new URLSearchParams()
  if (params?.featuredOnly) qs.set('featured_only', 'true')
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<unknown>('GET', `/api/routing/model-tree${suffix}`, undefined, options).then(unwrapModelTree)
}

export function unwrapModelTree(resp: unknown): ModelTreeFullResponse | ModelTreeSimpleResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`模型路由树 响应形状不符：期望对象，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  const missing = MODEL_TREE_ENVELOPE_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`模型路由树 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  if (!Array.isArray(d.featured) || !Array.isArray(d.series) || !Array.isArray(d.unmapped)) {
    throw new Error('模型路由树 响应形状不符：featured/series/unmapped 必须是数组')
  }
  return d as unknown as ModelTreeFullResponse | ModelTreeSimpleResponse
}

/** ★★ 是不是 tenant_admin 的裁剪形状（凭据详情被隐藏）。 */
export function modelTreeIsRedacted(r: ModelTreeFullResponse | ModelTreeSimpleResponse): boolean {
  return (r as ModelTreeSimpleResponse).readonly === true
}

/** ★ 取出变体列表，两种形状都能用（消除调用方的分支）。 */
export function modelTreeVariants(
  series: ModelTreeSeries[] | ModelTreeSimpleSeries[],
): Array<ModelTreeVariant | ModelTreeSimpleVariant> {
  // ⚠️ 必须显式 cast：两种 series 的 generations/variants 是**结构不同的类型**，
  //   TS 的 flatMap 在联合类型上推不出共同元素类型（试过，重载不匹配）。
  //   这里 cast 是安全的 —— 调用方按 modelTreeVariantAvailable / isSimpleVariant 分流。
  return (series as Array<{ generations: Array<{ variants: Array<ModelTreeVariant | ModelTreeSimpleVariant> }> }>).flatMap(
    (s) => s.generations.flatMap((g) => g.variants),
  )
}

/** ★★ tenant_admin 形状下，`available` 是**全称判断**；super_admin 形状下压根没有这一位。 */
export function modelTreeVariantAvailable(
  v: ModelTreeVariant | ModelTreeSimpleVariant,
): boolean | null {
  if (isSimpleVariant(v)) return v.available
  // 完整形状：variant 级没有 available —— 要不要算全称，由调用方决定，这里返回 null
  return null
}

export function isSimpleVariant(
  v: ModelTreeVariant | ModelTreeSimpleVariant,
): v is ModelTreeSimpleVariant {
  return (v as ModelTreeSimpleVariant).credential_count !== undefined
}

/** ★ 完整形状下算「全部凭据可用」需自己 fold —— 别与裁剪形状的 available 混用。 */
export function modelTreeAllCredentialsAvailable(v: ModelTreeVariant): boolean {
  return v.credentials.length > 0 && v.credentials.every((c) => c.available)
}

/** ★ 编造默认值（同 overview：0.9 / 9999）。 */
export function modelTreeMetricsMayBePlaceholder(c: ModelTreeCredential): boolean {
  return c.success_rate === 0.9 && c.p95_latency_ms === 9999
}

/**
 * ★★★ `availability_state` 的 NULL 被写成 **"ready"**
 * （SQL `COALESCE(c.availability_state, 'ready')`，routing.go:2276）。
 * ⇒ 一个**状态未知**的凭据在树里显示为「就绪」—— 这是本批最危险的一处编造，
 *   比 0.9/9999 更糟：它直接改变了「能不能路由」的判断。
 * ⚠️ 与之相邻的 `credential_status` 用的是 'unknown'（诚实的兜底），
 *   两者**不一致**，恰恰说明 'ready' 这个兜底是失误而非设计。
 */
export function modelTreeAvailabilityFabricated(c: { credential_status: string; availability_state: string }): boolean {
  return c.credential_status === 'unknown' && c.availability_state === 'ready'
}

/**
 * ★★ `featured_only=true` 但 `featured` 为空 ⇒ 过滤**整个不下发**
 * （routing.go:2387 `if featuredOnly && len(featuredModels) > 0`，过滤在 Go 里
 * 按 rawName/canonicalName 匹配）。⇒ 静默返回全量。
 */
export function modelTreeFeaturedFilterInert(r: ModelTreeFullResponse | ModelTreeSimpleResponse): boolean {
  return r.featured.length === 0
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/routing/available-models/raw（admin 档，routing.go:3178）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★★★ **零行时返回 `null`，不是 `[]`**。
 *
 * 后端 `var names []string`（routing.go:3203）是 **nil 切片**，
 * 而 `json.Marshal` 把 nil slice 编码成 `null`；只有 `make([]string, 0)`
 * 才是 `[]`。⇒ 「一个可用模型都没有」与「契约漂移」在客户端必须分开处理。
 * 仓库里 audit 那条走的是 `make(...)` 所以返 `[]` —— **同一族两种表示**。
 */
export function fetchAvailableModelsRaw(options?: RequestOptions): Promise<string[]> {
  return req<unknown>('GET', '/api/routing/available-models/raw', undefined, options).then(
    unwrapAvailableModelsRaw,
  )
}

export function unwrapAvailableModelsRaw(resp: unknown): string[] {
  // ★ null 是「零行」的合法形态，必须放行成 []
  if (resp === null || resp === undefined) return []
  if (!Array.isArray(resp)) {
    throw new Error(`可用模型原始名单 响应形状不符：期望数组或 null，实得 ${typeof resp}`)
  }
  return resp.filter((x): x is string => typeof x === 'string')
}

/** ★ 后端已 DISTINCT + ORDER BY（:3186-3188），但仍按去重来确认一次无重复。 */
export function availableModelsHasDuplicates(names: string[]): boolean {
  return new Set(names).size !== names.length
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/routing/health（admin 档，routing.go:3378）
 * ═════════════════════════════════════════════════════════════════════════ */

export interface RoutingHealthCredential {
  credential_id: number
  label: string
  status: string
  circuit_state: string
  consecutive_failures: number
  circuit_open_count_window: number
  cooling_until: string | null
  provider_name: string
  catalog_code: string | null
}

export interface RoutingHealthResponse {
  credentials: RoutingHealthCredential[]
  summary: { total: number; open: number; closed: number }
}

export const ROUTING_HEALTH_CRED_KEYS = [
  'credential_id', 'label', 'status', 'circuit_state', 'consecutive_failures',
  'circuit_open_count_window', 'cooling_until', 'provider_name', 'catalog_code',
] as const

export function fetchRoutingHealth(options?: RequestOptions): Promise<RoutingHealthResponse> {
  return req<unknown>('GET', '/api/routing/health', undefined, options).then(unwrapRoutingHealth)
}

export function unwrapRoutingHealth(resp: unknown): RoutingHealthResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`路由健康 响应形状不符：期望 {credentials,summary}，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  if (!Array.isArray(d.credentials) || !d.summary || typeof d.summary !== 'object') {
    throw new Error('路由健康 响应形状不符：缺 credentials/summary')
  }
  const s = d.summary as Record<string, unknown>
  if (typeof s.total !== 'number' || typeof s.open !== 'number' || typeof s.closed !== 'number') {
    throw new Error('路由健康 summary 键类型不对')
  }
  d.credentials.forEach((c, i) => {
    if (!c || typeof c !== 'object' || Array.isArray(c)) {
      throw new Error(`路由健康 credentials[${i}] 不是对象`)
    }
    const missing = ROUTING_HEALTH_CRED_KEYS.filter((k) => !(k in (c as Record<string, unknown>)))
    if (missing.length > 0) {
      throw new Error(`路由健康 credentials[${i}] 缺 ${missing.length} 个键（${missing.join(', ')}）`)
    }
  })
  return d as unknown as RoutingHealthResponse
}

/**
 * ★ summary 的三个数是**客户端可复算的**（`total=len(credentials)`、
 *   `open`=circuit_state=='open' 的条数、`closed=total-open`）。
 *   后端 `:3415-3440` 就是这么算的 ⇒ 不一致即契约漂移或中间层加工过。
 */
export function routingHealthSummaryDisagrees(r: RoutingHealthResponse): boolean {
  const open = r.credentials.filter((c) => c.circuit_state === 'open').length
  return r.summary.total !== r.credentials.length || r.summary.open !== open || r.summary.closed !== r.credentials.length - open
}

/** ★ `cooling_until` 非空但 `circuit_state` 不是 open —— 冷却窗口与状态不同步。 */
export function routingHealthCoolingDesynced(c: RoutingHealthCredential): boolean {
  return c.cooling_until !== null && c.circuit_state !== 'open'
}