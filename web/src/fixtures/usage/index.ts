// fixtures/usage/index.ts —— 5 个 usage 端点的**真端点原始响应**（2026-10-21 采集）
//
// ## 来源（可复现）
//
//   C=$(cat /tmp/kx-admin-cookie.txt)
//   curl -s -H "Cookie: llmgw_session=$C" 'http://127.0.0.1:8782/api/usage/summary?days=7'
//   curl -s -H "Cookie: llmgw_session=$C" 'http://127.0.0.1:8782/api/admin/usage/cost-trend?group_by=model'
//   curl -s -H "Cookie: llmgw_session=$C" 'http://127.0.0.1:8782/api/admin/usage/period-compare?current=2026-10&previous=2026-09'
//   curl -s -H "Cookie: llmgw_session=$C" 'http://127.0.0.1:8782/api/admin/usage/cache-economics'
//   curl -s -H "Cookie: llmgw_session=$C" 'http://127.0.0.1:8782/api/usage/hot-keys?days=7&limit=10'
//
// ⚠️ **不要手写这些形状。** 2026-10-21 的审计发现：/admin/usage-enhanced 整页白屏，
// 根因是前端照一份**从未存在过**的契约另写了一套类型（total_cost / data /
// delta_cost_pct / cache_hit_rate / {keys:[…]}），而当时的单测**用同一套错形状
// 做夹具** ⇒ 测试恒绿、线上恒炸。形状只有一处真源：这里。
//
// 已知与后端不一致的历史包袱（**故意保留原样**，用于钉住真实行为）：
//   · summary **没有** days 字段（days 是请求参数，不是响应字段）
//   · period_compare 的 change_pct 已经是**百分数**（后端 `(Δ/prev)*100`），
//     而 cache_hit_ratio 是 0~1 的**比例** —— 两者不能共用同一个格式化器
//   · hot-keys 返回**裸数组**（不是 {items,…} 信封），后端两种形状都可能出现
import realSummary from './real_usage_summary.json'
import realCostTrend from './real_usage_cost_trend.json'
import realPeriodCompare from './real_usage_period_compare.json'
import realCacheEconomics from './real_usage_cache_economics.json'
import realHotKeys from './real_usage_hot_keys.json'

export const realUsageFixtures = {
  summary: realSummary,
  costTrend: realCostTrend,
  periodCompare: realPeriodCompare,
  cacheEconomics: realCacheEconomics,
  hotKeys: realHotKeys,
} as const

/** 5 个端点的真实路径。测试用它断言「请求打到了哪些 URL」。 */
export const REAL_USAGE_PATHS = [
  '/api/usage/summary',
  '/api/admin/usage/cost-trend',
  '/api/admin/usage/period-compare',
  '/api/admin/usage/cache-economics',
  '/api/usage/hot-keys',
] as const

/**
 * 按**真实路径**派发真实响应。
 *
 * 刻意**不**按「URL 开头包含某段」派发：那正是初版能把自己那套错端点喂饱的原因
 * （断言 `current=last_7d` 通过，而后端只收 YYYY-MM；断言
 * `/api/admin/usage/hot-api-keys` 通过，而后端没有这条路由 ⇒ 线上恒 404）。
 * 这里遇到任何未知路径一律抛错 ⇒ 打到错端点立刻暴露。
 */
export function realUsageResponse(rawUrl: string): unknown {
  const path = new URL(rawUrl, 'http://localhost').pathname
  switch (path) {
    case '/api/usage/summary':
      return realUsageFixtures.summary
    case '/api/admin/usage/cost-trend':
      return realUsageFixtures.costTrend
    case '/api/admin/usage/period-compare':
      return realUsageFixtures.periodCompare
    case '/api/admin/usage/cache-economics':
      return realUsageFixtures.cacheEconomics
    case '/api/usage/hot-keys':
      return realUsageFixtures.hotKeys
    default:
      throw new Error(`unexpected endpoint (no such route on the gateway): ${rawUrl}`)
  }
}
