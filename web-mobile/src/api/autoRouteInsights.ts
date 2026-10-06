import { req, type RequestOptions } from './client'

// autoRouteInsights.ts — 自动路由的「诊断」与「调优建议」两个面。
//   GET /api/admin/auto-route/analytics/funnel   单个模型的请求漏斗
//   GET /api/admin/auto-route/tuning/proposals   调优建议（含人工审核态）
//
// 鉴权：两条都是 **superAdmin**。
//   · funnel 走 `analyticsH.RegisterAnalyticsRoutes(mux, h.superAdmin)`
//     （handler.go:1430）
//   · proposals 走 `tuning.RegisterTuningRoutes(mux, adminWrap)`
//     （auto_route.go:116），而 RegisterAutoRouteRoutes 本身在
//     handler.go:1381 是用 `h.superAdmin` 调用的 ⇒ 里面的 adminWrap 就是 superAdmin。
//   ⇒ tenant_admin 必 403，导航要挡。这是第四条超管线。
//
// 它们与 /overrides 的关系：那一页答「**现在生效的规则**」，
// 这一页答「**规则该怎么调**」（proposals）与「**请求是怎么被筛掉的**」（funnel）。
// 只看规则不知道规则对不对，只看漏斗不知道该怎么改。
//
// ⚠️ 三个坑：
//
// (1) ★ **funnel 的 meta 里有 `approximate` 与 `data_source`**。
//     `approximate = dataSource != "exact"`。样本不足时后端会用采样/估算
//     （dataSource ∈ exact | approximate | mixed），并同时给出
//     `sample_n` / `trace_rows` / `trace_ratio` / `confidence`。
//     ⇒ UI **必须**把 `approximate=true` 显示出来。
//     把采样估算显示成精确统计，就是「近似被当成结论」——本专题
//     §11.20/§11.22 修的「降级被显示成真的一分钱没花」是同一族错误。
//
// (2) **proposals 的 `status` / `category` 走 allowlist 校验**，非法值 400
//     （auto_route_tuning.go:188 / :193 / :200）。空串是合法的（表示不过滤）。
//     ★ 这是本仓库的第**五种**越界语义：400 for unknown enum。
//       已有 400 / 静默 clamp / 静默回落 / 静默 clamp 到上限 / 400 for enum。
//
// (3) `proposal` 与 `evidence` 是 `json.RawMessage`（任意 JSON），
//     **不能**假设它有固定字段。UI 只能按「有一段 JSON 建议」呈现，
//     并做容错的键提取。
//
// (4) ★★ **近似模式下 `meta.blocked` 恒为 0，而 0 有两种完全不同的含义。**
//     analytics.go:1019-1045：只有 `exact` 分支才 `SUM(blocked_candidates)`。
//     `approximate`（RDL 无行）与 `mixed`（RDL 有行但 trace 为空）都直接走
//     `request_logs` 补数，那条查询里**根本没有 blocked 列** ⇒ fr.totalBlocked
//     保持零值。
//     ⇒ 所以「近似 + blocked=0」= **没算过**，不是「没有阻断」。
//     把它渲染成「0 个候选被阻断」，就是在编造一个我们并不知道的结论。
//     见下方 `blockedIsInconclusive`。
//
// (5) ★ **funnel 有 2 分钟服务端缓存**（funnel_cache.go:21-24，key = scope|model|window）。
//     用户改完覆盖规则立刻回来刷新，会看到**没变化**的旧数。
//     UI 必须自曝「数据最多滞后 2 分钟」，否则用户会认定「写入没生效」并重复提交。

/** 后端 allowlist（auto_route_tuning.go:50-52）。空串 = 不过滤。 */
export const TUNING_STATUSES = ['', 'pending', 'approved', 'rejected', 'applied'] as const
export const TUNING_CATEGORIES = ['', 'keyword_add', 'weight_adjust', 'threshold_change'] as const
export type TuningStatus = (typeof TUNING_STATUSES)[number]
export type TuningCategory = (typeof TUNING_CATEGORIES)[number]

/** proposals 的 limit 上限；越界 400 `limit must be 1-500`（auto_route_tuning.go:200）。 */
export const TUNING_PROPOSALS_MAX_LIMIT = 500
/** 后端默认 limit = 50（auto_route_tuning.go:197 `limit := 50`）。 */
export const TUNING_PROPOSALS_DEFAULT_LIMIT = 50

/** funnel 的服务端缓存 TTL（funnel_cache.go:23）。改完规则立刻刷新会读到旧值。 */
export const FUNNEL_CACHE_TTL_MS = 2 * 60 * 1000

// ── funnel ────────────────────────────────────────────────────────────────

/** `window` 合法值。★ 后端 `strings.ToLower` + `TrimSpace` ⇒ 大小写/空格不敏感。 */
export type AnalyticsWindow = '' | '24h' | '7d'
export const ANALYTICS_WINDOWS: readonly Exclude<AnalyticsWindow, ''>[] = ['24h', '7d'] as const

export type FunnelDataSource = 'exact' | 'approximate' | 'mixed' | (string & {})

export interface FunnelStage {
  key: string
  label: string
  value: number
  /** 后端给的中文 hint，直接透出。 */
  hint: string
  [k: string]: unknown
}

export interface FunnelMeta {
  /** ★ true = 数据是采样/估算，不是精确统计。必须透出。 */
  approximate: boolean
  data_source: FunnelDataSource
  blocked: number
  chosen: number
  sample_n: number
  trace_rows: number
  trace_ratio: number
  confidence: string
  confidence_hint: string
  [k: string]: unknown
}

export interface FunnelResponse {
  model: string
  window: string
  requests: number
  stages: FunnelStage[] | null
  meta: FunnelMeta
  [k: string]: unknown
}

export interface FunnelParams {
  /** 必填，空串 ⇒ 400 `model parameter required` */
  model: string
  window?: AnalyticsWindow
}

/**
 * 拉单个模型的请求漏斗。
 *
 * ★ 窗口选项只有 24h / 7d（`window must be 24h or 7d`，非法值 400），
 *   空串 = 7d。**不要**提供 12h / 30d 之类的选项。
 */
export function fetchRouteFunnel(params: FunnelParams, options?: RequestOptions): Promise<FunnelResponse> {
  const qs = new URLSearchParams()
  qs.set('model', params.model)
  if (params?.window) qs.set('window', params.window)
  return req<FunnelResponse>('GET', `/api/admin/auto-route/analytics/funnel?${qs}`, undefined, options)
}

/**
 * 数据是否可信。
 *
 * ★ 判定依据是 `approximate`（后端给的权威位），**不是**我们自己猜 sample_n。
 *   后端可能在 sample_n 很大时仍是 mixed（trace_ratio < 0.8 时降级），
 *   反之也可能数据够但仍标 approximate。只看样本量会判反。
 */
export function isApproximate(meta: Pick<FunnelMeta, 'approximate'> | null | undefined): boolean {
  return meta?.approximate === true
}

/** 阶段之间的转化率。分母为 0 时返回 null —— 不用 0% 冒充「上一阶段没有量」。 */
export function stageRate(from: number | null | undefined, to: number | null | undefined): number | null {
  const a = typeof from === 'number' ? from : 0
  const b = typeof to === 'number' ? to : 0
  if (a <= 0) return null
  return b / a
}

/**
 * ★ `meta.blocked` 是否**不可信**（不是「0」这个数）。
 *
 * 只有 `data_source === 'exact'` 时后端才真的聚合过 `blocked_candidates`。
 * approximate / mixed 走的是 `request_logs` 补数路径，那条 SQL 压根没有
 * blocked 列 ⇒ 字段必然是零值 ⇒ **0 意味着「没算」，不是「一个都没被拦」。**
 *
 * 返回 true 时 UI 必须显示「未知」/「未统计」，
 * 绝不能显示 `blocked = 0`。
 */
export function blockedIsInconclusive(meta: Pick<FunnelMeta, 'approximate' | 'data_source'> | null | undefined): boolean {
  if (!meta) return true
  return meta.data_source !== 'exact' || meta.approximate === true
}

// ── tuning proposals ──────────────────────────────────────────────────────

export interface TuningProposal {
  id: number
  ts: string
  category: string
  task_type?: string | null
  /** json.RawMessage —— 任意 JSON，**不要**假设固定字段 */
  proposal: unknown
  /** json.RawMessage —— 任意 JSON */
  evidence: unknown
  status: string
  reviewed_by?: string | null
  reviewed_at?: string | null
  applied_at?: string | null
  review_note?: string | null
  [k: string]: unknown
}

/** 回显的过滤条件。★ 两个值都是 **string**（本仓库第三次出现同款）。 */
export interface TuningProposalsFilterEcho {
  status: string
  category: string
}

export interface TuningProposalsResponse {
  /** 后端显式初始化为 `[]`（auto_route_tuning.go 的注释专门说明了这点）。 */
  proposals: TuningProposal[] | null
  count: number
  filter: TuningProposalsFilterEcho
}

export interface TuningProposalsParams {
  status?: TuningStatus
  category?: TuningCategory
  limit?: number
}

export function fetchTuningProposals(
  params?: TuningProposalsParams,
  options?: RequestOptions,
): Promise<TuningProposalsResponse> {
  const qs = new URLSearchParams()
  // ★ 空串是合法值（不过滤），但**非法值 400** ⇒ 只能发 allowlist 里的
  if (params?.status) qs.set('status', params.status)
  if (params?.category) qs.set('category', params.category)
  if (params?.limit != null) {
    const n = Math.trunc(params.limit)
    // 越界 400 ⇒ 前端自己夹，别让用户吃 400
    if (Number.isFinite(n) && n > 0) qs.set('limit', String(Math.min(n, TUNING_PROPOSALS_MAX_LIMIT)))
  }
  const s = qs.toString()
  return req<TuningProposalsResponse>('GET', `/api/admin/auto-route/tuning/proposals${s ? '?' + s : ''}`, undefined, options)
}

const PROPOSAL_STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'muted'> = {
  applied: 'success',
  approved: 'warning',
  rejected: 'danger',
  pending: 'muted',
}

/** 状态 → 配色。★ 词表外一律 muted，不给 success（同 probeTasks 的理由）。 */
export function proposalStatusTone(status: string | null | undefined): 'success' | 'warning' | 'danger' | 'muted' {
  return PROPOSAL_STATUS_TONE[(status ?? '').toLowerCase()] ?? 'muted'
}

/**
 * 从 `proposal` / `evidence` 的任意 JSON 里容错地取几个常见键。
 *
 * ★ 这两个字段是 json.RawMessage，**没有**固定 schema（后端会随建议类型变化）。
 *   写死 `p.model` 这类访问在某个建议类型上就是 `undefined`，
 *   而 undefined 渲染出去是「该建议没有模型」——一个我们并不知道的结论。
 *   ⇒ 只在键存在时取，取不到就不显示这一行。
 */
export function pickFields(obj: unknown, keys: readonly string[]): Array<[string, string]> {
  if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return []
  const rec = obj as Record<string, unknown>
  const out: Array<[string, string]> = []
  for (const k of keys) {
    const v = rec[k]
    if (v == null) continue
    if (typeof v === 'string') {
      if (v.trim() === '') continue
      out.push([k, v])
    } else if (typeof v === 'number' || typeof v === 'boolean') {
      out.push([k, String(v)])
    }
  }
  return out
}

export const PROPOSAL_FIELDS = ['model', 'task_type', 'from_weight', 'to_weight', 'keyword', 'threshold', 'mode'] as const
export const EVIDENCE_FIELDS = ['sample_n', 'success_rate', 'reason', 'observed', 'baseline'] as const
