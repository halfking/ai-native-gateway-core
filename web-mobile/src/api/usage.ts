import { req, type RequestOptions } from './client'

// usage.ts — /api/usage/summary + by-model（形状对齐 web/src/api/usage.ts）。
// by-model 可能被 DegradedListEnvelope 包装：{degraded, missing_view, items}。

export interface UsageSummary {
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd: number
  total_credits_charged?: number
  avg_latency_ms: number
  success_rate: number
  degraded?: boolean
  missing_view?: string
  hint?: string
}

export interface ModelUsage {
  model: string
  provider_code: string
  total_requests: number
  total_tokens: number
  total_cost_usd: number
}

export interface DegradedListEnvelope<T> {
  degraded: boolean
  missing_view?: string
  items: T[]
}

export function fetchUsageSummary(days = 7, options?: RequestOptions): Promise<UsageSummary> {
  return req<UsageSummary>('GET', `/api/usage/summary?days=${days}`, undefined, options)
}

export function fetchUsageByModel(days = 7, options?: RequestOptions): Promise<DegradedListEnvelope<ModelUsage> | ModelUsage[]> {
  return req<DegradedListEnvelope<ModelUsage> | ModelUsage[]>('GET', `/api/usage/by-model?days=${days}`, undefined, options)
}

/** 兼容两种形态（envelope 或裸数组）。 */
export function unwrapModelUsage(payload: DegradedListEnvelope<ModelUsage> | ModelUsage[]): { items: ModelUsage[]; degraded: boolean } {
  if (Array.isArray(payload)) return { items: payload, degraded: false }
  return { items: payload.items ?? [], degraded: !!payload.degraded }
}

// ── 成本趋势（cost-trend）：本仓最容易被误读的一个端点 ────────────────────

/**
 * `GET /api/usage/cost-trend?group_by=&days=`
 * 路由 admin/usage.go:57-58 → handler admin/usage_enhanced.go:130+。
 *
 * ★★ **降级语义是这个端点的全部难点**（后端 usage_enhanced.go:48-52 原注释）：
 *   当可选视图未迁移时（`IsMissingRelationError`，:211），后端返回
 *   **HTTP 200 + 空 entries + total_cost 0** 并附 `degraded:true` +
 *   `degraded_reason`。
 *   **若不读 degraded 字段，这个响应与「这段时间真的一分钱没花」完全同形。**
 *   ⇒ 移动端必须显式判 degraded，绝不能把 `total_cost: 0` 当成「零花费」显示。
 *
 * 后端那行注释还记了一个真实教训：同文件的 PeriodCompare / CacheEconomics 先加了
 * degraded，CostTrend 漏了 ⇒「本轮已修降级载荷」这句话对自己文件都不成立。
 * 移动端同理：**新增一个读同一族降级载荷的端点时，要一并核对它带不带标记。**
 */
export interface CostTrendEntry {
  dimension_value: string
  request_count: number
  total_cost_usd: number
  input_cost_usd: number
  output_cost_usd: number
  prompt_tokens: number
  completion_tokens: number
  avg_latency_ms: number
  error_rate: number
  percentage: number
}

export interface CostTrendResponse {
  group_by: string
  date_from: string
  date_to: string
  total_cost: number
  entries: CostTrendEntry[]
  other_cost: number
  other_count: number
  degraded: boolean
  degraded_reason?: string
}

/** 后端默认 group_by='model'（:135-138）；未知维度会走 planCostTrend 的 not-ok 分支。 */
export const COST_TREND_DIMENSIONS = ['model', 'provider', 'key', 'application', 'tenant'] as const
export type CostTrendDimension = (typeof COST_TREND_DIMENSIONS)[number]

export function fetchCostTrend(
  groupBy: CostTrendDimension = 'model',
  days = 7,
  options?: RequestOptions,
): Promise<CostTrendResponse> {
  const qs = new URLSearchParams({ group_by: groupBy, days: String(Math.max(1, Math.trunc(days) || 7)) })
  return req<CostTrendResponse>('GET', `/api/usage/cost-trend?${qs}`, undefined, options)
}

/**
 * 把降级响应**显式抬成一种结论**，而不是让调用方各自记得判 degraded。
 *
 * 三态刻意分开（这是本函数存在的理由）：
 *   · 'ok'       —— 有数据
 *   · 'degraded' —— 后端视图缺失，`total_cost: 0` **不是真实零花费**，不可作为结论
 *   · 'empty'    —— 没降级且确实零条数 ⇒ 这次是真的没有
 * ★ `degraded` 与 `empty` 必须互斥：把 degraded 显示成「暂无数据」，
 *   就是在用一句没有依据的话解释一次系统降级。
 */
export interface CostTrendReading {
  kind: 'ok' | 'degraded' | 'empty'
  totalCost: number
  entries: CostTrendEntry[]
  reason?: string
  /** 仅 kind==='ok' 时可信；degraded 时是 0，但**不可当零花费读**。 */
  costIsMeaningful: boolean
}

export function readCostTrend(r: CostTrendResponse): CostTrendReading {
  if (r.degraded) {
    return {
      kind: 'degraded',
      // ⚠️ 透传后端给的值，但用 costIsMeaningful=false 让调用方无法误当真实数字
      totalCost: r.total_cost ?? 0,
      entries: r.entries ?? [],
      reason: r.degraded_reason,
      costIsMeaningful: false,
    }
  }
  const entries = r.entries ?? []
  if (entries.length === 0) {
    return { kind: 'empty', totalCost: 0, entries, costIsMeaningful: true }
  }
  return { kind: 'ok', totalCost: r.total_cost ?? 0, entries, costIsMeaningful: true }
}
