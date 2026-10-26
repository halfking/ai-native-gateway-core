import {
  getCacheEconomics,
  getCostTrend,
  getHotApiKeys,
  getPeriodCompare,
  getUsageSummary,
  type CacheEconomicsResponse,
  type CostTrendResponse,
  type HotApiKeyEntry,
  type PeriodCompareResponse,
  type UsageSummary,
} from './usage'

// usageEnhanced.ts — 批 158b（2026-10-17）初版；2026-10-21 契约对齐重写。
//
// ## 为什么重写（初版整体是错的）
//
// 初版照一份**从未存在过**的契约，另写了一套类型并自己拼 URL：
//
//   · period-compare 传 `current=last_7d&previous=previous_7d`
//     ⇒ 后端 admin/usage_enhanced.go:parsePeriod 只收 `YYYY-MM` ⇒ **恒 400**
//   · hot-keys 打成 `/api/admin/usage/hot-api-keys`
//     ⇒ 后端没有这个路由（真实路由是 `/api/usage/hot-keys`）⇒ **恒 404**
//   · 5 个端点的返回形状全部对不上：
//     total_cost ↔ total_cost_usd、data ↔ entries、delta_cost_pct ↔ change_pct、
//     cache_hit_rate ↔ cache_hit_ratio、{keys:[…]} ↔ 裸数组 / 降级信封。
//
// 更糟的是初版自带的 `usageEnhanced.test.ts` 用**同一套错形状**做夹具，
// 于是测试恒绿而真实页面在视图读 `data.cost_trend.data.length` 时抛 TypeError
// ⇒ 整页白屏。**用前端自己声明的错形状做 fixture ⇒ 假绿。**
//
// ## 这一版的规矩
//
// 端点、路径、参数、返回形状**一律以 api/usage.ts 为正典**，本模块只做组合
// （并发 5 个请求 + 归一化 hot-keys 的降级信封）。后端契约因此只有一处定义，
// 前端再产生第二个猜测时必然对不上 —— 而对不上的那一刻会被测试抓住。
//
// 真值来自本机 8782 实测（2026-10-21）：
//   GET /api/usage/summary?days=7
//       → {total_requests, total_prompt_tokens, total_completion_tokens,
//          total_cost_usd, total_credits_charged?, avg_latency_ms, success_rate,
//          degraded?}          ← **没有 days 字段**（days 是请求参数，不是响应字段）
//   GET /api/admin/usage/cost-trend?group_by=model
//       → {group_by, date_from, date_to, total_cost, entries[], other_cost, other_count, degraded?}
//   GET /api/admin/usage/period-compare?current=YYYY-MM&previous=YYYY-MM
//       → {current:PeriodStats, previous:PeriodStats, change_pct, change_abs,
//          trend, significant, by_dimension, degraded?}
//   GET /api/admin/usage/cache-economics
//       → {date_from, date_to, total_requests, cache_read_tokens, prompt_tokens,
//          cache_hit_ratio, dollars_saved, ..., degraded?}
//   GET /api/usage/hot-keys?days=7&limit=10
//       → HotApiKeyEntry[] | {items, degraded, ...}（两种形状都要认）

/** 组合视图的数据形状。字段名与 api/usage.ts 的响应字段逐字一致 —— 不做改名。 */
export interface UsageEnhancedData {
  /** 请求窗口天数。**summary 响应里没有这个字段**，它是请求参数，故由本模块带上。 */
  days: number
  summary: UsageSummary
  cost_trend: CostTrendResponse
  period_compare: PeriodCompareResponse
  cache_economics: CacheEconomicsResponse
  hot_api_keys: HotApiKeyEntry[]
  /** hot-keys 端点自身的降级标记（裸数组形状下为 false）。 */
  hot_keys_degraded: boolean
  hot_keys_degraded_reason: string
  refreshedAt: string
}

export interface UsageEnhancedParams {
  /** summary / hot-keys 的窗口天数，默认 7。 */
  days?: number
  /** hot-keys 的条数上限，默认 10。 */
  topN?: number
  /** cost-trend / cache-economics 的显式窗口，默认由后端自选。 */
  dateFrom?: string
  dateTo?: string
  /** 同期对比的当期，格式必须 `YYYY-MM`（后端只认这个）。 */
  currentPeriod?: string
  /** 同期对比的对比期，格式必须 `YYYY-MM`。 */
  previousPeriod?: string
}

/** 当月 `YYYY-MM`（后端 period-compare 的唯一可接受格式）。 */
export function currentMonthPeriod(now: Date = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
}

/** 上月 `YYYY-MM`。跨年时 `new Date(y, m-1, 1)` 会自动退到上一年 12 月。 */
export function previousMonthPeriod(now: Date = new Date()): string {
  const prev = new Date(now.getFullYear(), now.getMonth() - 1, 1)
  return `${prev.getFullYear()}-${String(prev.getMonth() + 1).padStart(2, '0')}`
}

/**
 * 组合 5 个 usage 端点的综合视图。
 *
 * 5 个请求并发发出；任一失败整体失败（调用方看到异常，不看到半截数据）。
 */
export async function getUsageEnhancedData(
  params: UsageEnhancedParams = {},
): Promise<UsageEnhancedData> {
  const days = params.days ?? 7
  const topN = params.topN ?? 10
  const current = params.currentPeriod ?? currentMonthPeriod()
  const previous = params.previousPeriod ?? previousMonthPeriod()

  const [summary, costTrend, periodCompare, cacheEcon, hotKeys] = await Promise.all([
    getUsageSummary(days),
    getCostTrend('model', { date_from: params.dateFrom, date_to: params.dateTo }),
    getPeriodCompare(current, previous),
    getCacheEconomics({ date_from: params.dateFrom, date_to: params.dateTo }),
    getHotApiKeys(days, topN),
  ])

  return {
    days,
    summary,
    cost_trend: costTrend,
    period_compare: periodCompare,
    cache_economics: cacheEcon,
    hot_api_keys: hotKeys.items,
    hot_keys_degraded: hotKeys.degraded,
    hot_keys_degraded_reason: hotKeys.reason,
    refreshedAt: new Date().toISOString(),
  }
}
