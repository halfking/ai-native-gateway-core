import { req, type RequestOptions } from './client'

/**
 * routingRead.ts — 路由决策面三条只读端点（UI规范 17 §2 desktopOnly 让位轮，2026-10-08）。
 *
 * 后端实读 `admin/routing.go`，三个端点的**响应形状各不相同**，这也是本文件
 * 不抽通用解包器的原因：
 *
 * | 端点 | 权限档 | 形状 |
 * |---|---|---|
 * | `GET /api/routing/overview`  | admin（tenant_admin 可用） | **信封** `{featured, rows}` |
 * | `GET /api/routing/decisions` | admin（tenant_admin 限本租户）| **信封** `{total, offset, limit, decisions}` |
 * | `GET /api/routing/audit`     | **superAdmin**            | **裸数组** `[]` |
 *
 * ⚠️ 注册处：handler.go:930 / :1204 / :1206。
 *   同一族里 admin 档与 superAdmin 档混排，**不能按路径前缀判权限**。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/routing/overview（admin 档，handler.go:930 → routing.go:2075）
 * ═════════════════════════════════════════════════════════════════════════ */

/**
 * ★ 一行 = 一个 (模型, 供应商, 凭据) 组合的路由可路由性。
 *
 * ★★★ 两个**编造**的默认值（SQL 里的 COALESCE，routing.go:2138-2141）：
 *   `COALESCE(mo.success_rate, 0.9)` 与 `COALESCE(mo.p95_latency_ms, 9999)`
 *   ⇒ 一个**从没探测过**的凭据会以「成功率 90%、延迟 9999ms」出现在表里。
 *   客户端**无法**把它们与真值区分（后端没给任何标记），
 *   所以 UI 必须给这两列加「可能未探测」的免责，而不是当实测值展示。
 */
export interface RoutingOverviewRow {
  model_name: string
  provider_id: number
  provider_name: string
  catalog_code: string
  protocol: string
  base_url: string
  provider_enabled: boolean
  credential_id: number
  credential_label: string
  credential_status: string
  lifecycle_status: string
  availability_state: string
  availability_recover_at: string | null
  quota_state: string
  quota_recover_at: string | null
  balance_usd: number | null
  effective_at: string | null
  expires_at: string | null
  circuit_state: string
  cooling_until: string | null
  available: boolean
  /** COALESCE(..., 2) */
  tier: number
  /** COALESCE(..., 100) */
  weight: number
  unit_price_in_per_1m: number | null
  unit_price_out_per_1m: number | null
  currency: string | null
  /** ★ COALESCE(..., 0.9) —— 编造默认值，见类型注释 */
  success_rate: number
  /** ★ COALESCE(..., 9999) —— 编造默认值，见类型注释 */
  p95_latency_ms: number
  standardized_name: string | null
  runtime_routable: boolean
  /**
   * ★★ 与 `runtime_routable` **是同一个变量**（routing.go:2199-2200 两个键
   *   都赋 `runtimeRoutable`）⇒ 永远不可能不相等。保留是为了兼容旧客户端。
   */
  routable: boolean
  /**
   * ★ **条件存在**：只在 `!runtime_routable` 时写入（routing.go:2202-2205）。
   * ⇒ 不能写 `row.runtime_block_reason === ''` 当「可路由」的判据。
   */
  runtime_block_reason?: string
}

export interface RoutingOverviewResponse {
  /**
   * ★★ `routing_policy` 查询的 error 被**丢弃**（`_ = h.db.QueryRow(...)`，
   *   routing.go:2084）⇒ DB 出错时这里就是空数组，**没有信号**。
   *   而 `featured_only=true` 且这里为空时，过滤条件**整个不加**（:2133）
   *   ⇒ 「只看精选」会退化成「看全部」，且用户看不到任何提示。
   */
  featured: string[]
  rows: RoutingOverviewRow[]
}

/** 信封 2 键 + 行内 31 个恒存在键（`runtime_block_reason` 条件存在，不在内）。 */
export const ROUTING_OVERVIEW_REQUIRED_KEYS = ['featured', 'rows'] as const
export const ROUTING_OVERVIEW_ROW_REQUIRED_KEYS = [
  'model_name', 'provider_id', 'provider_name', 'catalog_code', 'protocol', 'base_url',
  'provider_enabled', 'credential_id', 'credential_label', 'credential_status',
  'lifecycle_status', 'availability_state', 'availability_recover_at', 'quota_state',
  'quota_recover_at', 'balance_usd', 'effective_at', 'expires_at', 'circuit_state',
  'cooling_until', 'available', 'tier', 'weight', 'unit_price_in_per_1m',
  'unit_price_out_per_1m', 'currency', 'success_rate', 'p95_latency_ms',
  'standardized_name', 'runtime_routable', 'routable',
] as const

export function fetchRoutingOverview(
  params?: { featuredOnly?: boolean },
  options?: RequestOptions,
): Promise<RoutingOverviewResponse> {
  const qs = new URLSearchParams()
  if (params?.featuredOnly) qs.set('featured_only', 'true')
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<unknown>('GET', `/api/routing/overview${suffix}`, undefined, options).then(unwrapRoutingOverview)
}

export function unwrapRoutingOverview(resp: unknown): RoutingOverviewResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`路由总览 响应形状不符：期望 {featured, rows}，实得 ${actual}`)
  }
  const o = resp as Record<string, unknown>
  const missing = ROUTING_OVERVIEW_REQUIRED_KEYS.filter((k) => !(k in o))
  if (missing.length > 0) {
    throw new Error(`路由总览 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  if (!Array.isArray(o.featured) || !Array.isArray(o.rows)) {
    throw new Error('路由总览 响应形状不符：featured/rows 必须是数组')
  }
  o.rows.forEach((r, i) => {
    if (!r || typeof r !== 'object' || Array.isArray(r)) {
      throw new Error(`路由总览 rows[${i}] 不是对象`)
    }
    const rm = r as Record<string, unknown>
    const rMissing = ROUTING_OVERVIEW_ROW_REQUIRED_KEYS.filter((k) => !(k in rm))
    if (rMissing.length > 0) {
      throw new Error(`路由总览 rows[${i}] 缺 ${rMissing.length} 个键（${rMissing.join(', ')}）`)
    }
  })
  return o as unknown as RoutingOverviewResponse
}

/**
 * ★★★ 编造的默认值（`0.9` / `9999`）——命中即说明这两个数**可能是 COALESCE 兜的**，
 * 不是实测。⚠️ 真值恰好等于这两个数时**无法区分**（后端没给标记）。
 * ⇒ UI 必须给这两列加免责，而不是当实测指标展示。
 */
export function overviewMetricsMayBePlaceholder(r: RoutingOverviewRow): boolean {
  return r.success_rate === 0.9 && r.p95_latency_ms === 9999
}

/** ★ 可路由的行**没有** `runtime_block_reason` 键（后端条件写入）。 */
export function overviewIsRoutable(r: RoutingOverviewRow): boolean {
  return r.runtime_routable === true
}

/** ★ 被阻塞时**才有**原因；键不存在 ⇔ 可路由。 */
export function overviewBlockReason(r: RoutingOverviewRow): string | null {
  return overviewIsRoutable(r) ? null : (r.runtime_block_reason ?? null)
}

/**
 * ★★ `routable` 与 `runtime_routable` 是同一个变量 ⇒ 恒相等。
 * 若不等，说明后端改了其中一个键 —— 这是**契约漂移信号**，不是数据问题。
 */
export function overviewRoutableKeysDisagree(r: RoutingOverviewRow): boolean {
  return r.routable !== r.runtime_routable
}

/**
 * ★★ `featured_only=true` 但 `featured` 为空 ⇒ 过滤条件**整个不下发**
 * （routing.go:2133 `if featuredOnly && len(featured) > 0`）⇒ 返回全部行。
 * 用户点了「只看精选」却看到全量，且没有任何提示。
 */
export function overviewFeaturedFilterInert(resp: RoutingOverviewResponse): boolean {
  return resp.featured.length === 0
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/routing/decisions（admin 档，handler.go:1204 → routing.go:3215）
 * ═════════════════════════════════════════════════════════════════════════ */

/** 一条路由决策。33 个键，与 routing.go:3375-3407 的 map 字面量逐字对应。 */
export interface RoutingDecisionRow {
  ts: string
  request_id: string
  idempotency_key: string | null
  tenant_id: string
  api_key_id: number | null
  model: string
  chosen_credential_id: number | null
  chosen_provider_id: number | null
  tier: number | null
  candidates_tried: number | null
  latency_ms: number | null
  success: boolean
  error_class: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
  /** ★ SQL `COALESCE(cost_usd, 0)` ⇒ NULL 成本显示成 0 元，与「免费」不可区分 */
  cost_usd: number
  request_bytes: number | null
  response_bytes: number | null
  client_model: string | null
  resolved_raw_model: string | null
  outbound_model: string | null
  sticky_hit: boolean | null
  client_profile: string | null
  request_mode: string | null
  identity_hash: string | null
  transform_rule_id: number | null
  egress_protocol: string | null
  failure_stage: string | null
  failure_detail_code: string | null
  resolution_path: string | null
  canonical_model: string | null
  /**
   * ★ SQL `COALESCE(..., '[]'::jsonb)`；解析失败时 `jsoncol.Decode` 保留零值
   *   ⇒ 损坏数据与真的空数组**都**是 `[]`（调用方忽略了 Decode 的返回值）。
   */
  resolution_raw_models: string[]
  /** ★★ 同上：损坏的 jsonb 渲染成 `{}`，与真的空对象不可区分。 */
  decision_trace: Record<string, unknown>
}

export interface RoutingDecisionsResponse {
  total: number
  offset: number
  limit: number
  decisions: RoutingDecisionRow[]
}

export const ROUTING_DECISIONS_REQUIRED_KEYS = ['total', 'offset', 'limit', 'decisions'] as const

/**
 * `limit` 默认 100，>500 **clamp 到 500**（routing.go:3222-3224，是 clamp 不是回落）。
 * `offset` < 0 ⇒ 0（:3225-3227）。`since_minutes` 默认 30。
 */
export const ROUTING_DECISIONS_MAX_LIMIT = 500
export const ROUTING_DECISIONS_DEFAULT_LIMIT = 100
export const ROUTING_DECISIONS_DEFAULT_SINCE_MINUTES = 30

export function fetchRoutingDecisions(
  params?: {
    sinceMinutes?: number
    limit?: number
    offset?: number
    model?: string
    canonical?: string
    success?: boolean
  },
  options?: RequestOptions,
): Promise<RoutingDecisionsResponse> {
  const qs = new URLSearchParams()
  if (params?.sinceMinutes != null) qs.set('since_minutes', String(params.sinceMinutes))
  if (params?.limit != null) qs.set('limit', String(params.limit))
  if (params?.offset != null) qs.set('offset', String(params.offset))
  if (params?.model) qs.set('model', params.model)
  if (params?.canonical) qs.set('canonical', params.canonical)
  if (params?.success != null) qs.set('success', String(params.success))
  const suffix = qs.toString() ? `?${qs}` : ''
  return req<unknown>('GET', `/api/routing/decisions${suffix}`, undefined, options).then(
    unwrapRoutingDecisions,
  )
}

export function unwrapRoutingDecisions(resp: unknown): RoutingDecisionsResponse {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`路由决策 响应形状不符：期望 {total,offset,limit,decisions}，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  const missing = ROUTING_DECISIONS_REQUIRED_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`路由决策 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  if (
    typeof d.total !== 'number' ||
    typeof d.offset !== 'number' ||
    typeof d.limit !== 'number' ||
    !Array.isArray(d.decisions)
  ) {
    throw new Error('路由决策 响应形状不符：键类型不对')
  }
  return d as unknown as RoutingDecisionsResponse
}

/** ★ 分页不一致：`offset + 返回条数 <= total` 不成立时，说明还有没拿到的页。 */
export function routingDecisionsHasMore(resp: RoutingDecisionsResponse): boolean {
  return resp.offset + resp.decisions.length < resp.total
}

/** ★★★ `total` 被计数查询的 error 吞成 0（routing.go:3259-3262）⇒ 可能假 0。 */
export function routingDecisionsTotalUnreliable(resp: RoutingDecisionsResponse): boolean {
  return resp.total === 0 && resp.decisions.length > 0
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/routing/audit（superAdmin 档，handler.go:1206 → routing.go:3443）
 * ═════════════════════════════════════════════════════════════════════════ */

export interface RoutingAuditRow {
  id: number
  ts: string
  /** SQL `COALESCE(actor,'')` ⇒ NULL 变成空串 */
  actor: string
  /** SQL `COALESCE(action,'')` */
  action: string
  /** 可空指针 ⇒ 响应里可能是 `null` */
  target_type: string | null
  target_id: number | null
  /**
   * ★★ `jsoncol.Decode` 的**返回值被忽略**（routing.go:3480-3481）
   *   ⇒ 损坏的 jsonb 与 SQL NULL **都**解成 `null`，客户端无法区分。
   */
  before_json: unknown
  /** 同上 */
  after_json: unknown
}

export const ROUTING_AUDIT_REQUIRED_KEYS = [
  'id', 'ts', 'actor', 'action', 'target_type', 'target_id', 'before_json', 'after_json',
] as const

/** `limit` 默认 50，>500 clamp 到 500（routing.go:3452-3454）。 */
export const ROUTING_AUDIT_DEFAULT_LIMIT = 50
export const ROUTING_AUDIT_MAX_LIMIT = 500

/**
 * ★★★ **命名防撞**：本函数刻意叫 `fetchRoutingAuditLog` 而不是 `fetchRoutingAudit`
 * —— 仓里已有 `@/api/routingAudit` 导出同名的 `fetchRoutingAudit`，
 * 但它打的是**另一个端点** `/api/admin/routing/overrides/audit`（覆盖规则审计）。
 * 两个同名函数分处两个模块，import 时极易拿错。
 * ⇒ 同名不同端点时，给其中一个加限定词，别让调用方靠记忆区分。
 */
export function fetchRoutingAuditLog(
  params?: { limit?: number },
  options?: RequestOptions,
): Promise<RoutingAuditRow[]> {
  const qs = new URLSearchParams()
  if (params?.limit != null) qs.set('limit', String(params.limit))
  const suffix = qs.toString() ? `?${qs}` : ''
  // ★ 这个端点回的是**裸数组**，不是信封 —— 与同族的 decisions 相反。
  return req<unknown>('GET', `/api/routing/audit${suffix}`, undefined, options).then(unwrapRoutingAudit)
}

export function unwrapRoutingAudit(resp: unknown): RoutingAuditRow[] {
  if (!Array.isArray(resp)) {
    const actual = resp === null ? 'null' : typeof resp
    throw new Error(`路由审计 响应形状不符：期望裸数组，实得 ${actual}`)
  }
  resp.forEach((r, i) => {
    if (!r || typeof r !== 'object' || Array.isArray(r)) {
      throw new Error(`路由审计 [${i}] 不是对象`)
    }
    // ⚠️ 括号不能省：`!(k) in obj` 会被解析成 `(!(k)) in obj`，
    //   返回值类型就变成 boolean 而不是「键不存在」，整段判据静默失效。
    const missing = ROUTING_AUDIT_REQUIRED_KEYS.filter((k) => !(k in (r as Record<string, unknown>)))
    if (missing.length > 0) {
      throw new Error(`路由审计 [${i}] 缺 ${missing.length} 个键（${missing.join(', ')}）`)
    }
  })
  return resp as RoutingAuditRow[]
}

/**
 * ★★★★ **查询失败回的是 200 + 空数组**，不是错误（routing.go:3460-3463）。
 *
 *   ```go
 *   rows, err := h.db.Query(ctx, `...`)
 *   if err != nil { writeJSON(w, http.StatusOK, []any{}); return }
 *   ```
 *
 * ⇒ 客户端拿到的「没有审计记录」与「数据库查询失败」**完全一致**。
 * 对一个审计端点，这意味着故障期间运维会看到「最近没人动过配置」——
 * **方向完全错**。这是本批最该被显式提示的一条。
 */
export function routingAuditEmptyIsUnreliable(rows: RoutingAuditRow[], httpStatus: number): boolean {
  return httpStatus === 200 && rows.length === 0
}

/** ★ `actor` / `action` 的空串 ⇔ SQL 里是 NULL（被 COALESCE 过），不是「真叫空」。 */
export function routingAuditActorUnknown(r: RoutingAuditRow): boolean {
  return r.actor === ''
}
