// usageEnhanced.test.ts —— 组合层的「端点 / 参数 / 形状」契约（2026-10-21 重写）
//
// ## 挡住的是什么
//
// 2026-10-21 逐页浏览器验收发现 /admin/usage-enhanced **整页白屏**。根因不是渲染，
// 而是这份文件对应的实现（初版 usageEnhanced.ts）在 2026-10-17 照一份**从未存在过**
// 的契约另写了一套类型和端点：
//
//   · period-compare 传 `current=last_7d&previous=previous_7d`
//     ⇒ 后端 admin/usage_enhanced.go:parsePeriod 只收 `YYYY-MM` ⇒ **恒 400**
//   · hot-keys 打成 `/api/admin/usage/hot-api-keys`
//     ⇒ 后端没有这条路由（真实路由 `/api/usage/hot-keys`）⇒ **恒 404**
//   · 5 个端点的返回形状全部对不上（total_cost↔total_cost_usd、data↔entries、
//     delta_cost_pct↔change_pct、cache_hit_rate↔cache_hit_ratio、{keys:[…]}↔裸数组）
//
// 而**初版这个测试文件用同一套错形状做夹具**，于是：
//   · `expect(urls.some(u => u.includes('current=last_7d'))).toBe(true)` 通过
//   · `expect(urls.some(u => u.includes('/api/admin/usage/hot-api-keys'))).toBe(true)` 通过
// ⇒ **把错误的契约写进夹具，再断言实现符合这个错契约** —— 假绿的经典形态。
//
// ## 这一版的规矩
//
// 夹具是**真端点的原始响应**（src/fixtures/usage/，采集命令见该文件注释），
// 派发器按**真实路径**匹配，未知路径直接抛错 ⇒ 打到错端点立刻暴露。
// 判据直接钉住「请求打到了哪些 URL、带什么参数、拿回来的形状是不是后端的形状」。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { req } from './_core'
import { getUsageEnhancedData, currentMonthPeriod, previousMonthPeriod } from './usageEnhanced'
import { REAL_USAGE_PATHS, realUsageFixtures, realUsageResponse } from '../fixtures/usage'

vi.mock('./_core', () => ({ req: vi.fn() }))

const mockedReq = vi.mocked(req)

/** 记录下来的请求 URL（按发生顺序）。 */
function requestedUrls(): string[] {
  return mockedReq.mock.calls.map((c) => c[1] as string)
}

beforeEach(() => {
  vi.clearAllMocks()
  mockedReq.mockImplementation(async (_method, url) => realUsageResponse(url))
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('getUsageEnhancedData —— 端点与参数', () => {
  it('打到 5 个真实端点，且一个不多一个不少', async () => {
    await getUsageEnhancedData({ days: 7, topN: 10 })

    const paths = requestedUrls().map((u) => new URL(u, 'http://localhost').pathname)
    expect(paths.slice().sort()).toEqual([...REAL_USAGE_PATHS].sort())
    // 反例（初版）：打成 /api/admin/usage/hot-api-keys ⇒ 后端无此路由 ⇒ 线上恒 404。
    // 上面的精确集合断言已经覆盖；这里再钉一句「不得出现这个历史错路径」，
    // 让失败信息直接指向根因而不是让人自己去比对集合。
    expect(paths).not.toContain('/api/admin/usage/hot-api-keys')
  })

  it('period-compare 的 current/previous 必须是 YYYY-MM（后端只认这个格式）', async () => {
    await getUsageEnhancedData({ days: 7, topN: 10, currentPeriod: '2026-10', previousPeriod: '2026-09' })

    const url = requestedUrls().find((u) => u.includes('/api/admin/usage/period-compare'))
    expect(url, 'period-compare 请求必须发出').toBeTruthy()
    const q = new URL(url!, 'http://localhost').searchParams
    expect(q.get('current')).toBe('2026-10')
    expect(q.get('previous')).toBe('2026-09')
    // 形状断言：后端 parsePeriod 用 time.Parse("2006-01", …) ⇒ 任何非 YYYY-MM 恒 400。
    expect(q.get('current')).toMatch(/^\d{4}-\d{2}$/)
    expect(q.get('previous')).toMatch(/^\d{4}-\d{2}$/)
    // 反例（初版）：'last_7d' / 'previous_7d' ⇒ 后端 400 ⇒ 整个 Promise.all 拒绝 ⇒ 整页报错。
    expect([...q.values()]).not.toContain('last_7d')
    expect([...q.values()]).not.toContain('previous_7d')
  })

  it('默认周期 = 当月 / 上月（YYYY-MM）', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-20T12:00:00Z'))
    expect(currentMonthPeriod()).toBe('2026-10')
    expect(previousMonthPeriod()).toBe('2026-09')

    await getUsageEnhancedData()
    const url = requestedUrls().find((u) => u.includes('/api/admin/usage/period-compare'))
    const q = new URL(url!, 'http://localhost').searchParams
    expect(q.get('current')).toBe('2026-10')
    expect(q.get('previous')).toBe('2026-09')
  })

  it('summary 带 days、hot-keys 带 days+limit、cost-trend 带 group_by', async () => {
    await getUsageEnhancedData({ days: 7, topN: 10 })

    const summary = new URL(requestedUrls().find((u) => u.includes('/api/usage/summary'))!, 'http://localhost')
    expect(summary.searchParams.get('days')).toBe('7')

    const hot = new URL(requestedUrls().find((u) => u.includes('/api/usage/hot-keys'))!, 'http://localhost')
    expect(hot.searchParams.get('days')).toBe('7')
    expect(hot.searchParams.get('limit')).toBe('10')

    const trend = new URL(requestedUrls().find((u) => u.includes('/cost-trend'))!, 'http://localhost')
    expect(trend.searchParams.get('group_by')).toBe('model')
  })

  it('显式 date_from/date_to 会带到 cost-trend 与 cache-economics', async () => {
    await getUsageEnhancedData({ dateFrom: '2026-09-01', dateTo: '2026-09-30' })

    const trend = new URL(requestedUrls().find((u) => u.includes('/cost-trend'))!, 'http://localhost')
    expect(trend.searchParams.get('date_from')).toBe('2026-09-01')
    expect(trend.searchParams.get('date_to')).toBe('2026-09-30')

    const cache = new URL(requestedUrls().find((u) => u.includes('/cache-economics'))!, 'http://localhost')
    expect(cache.searchParams.get('date_from')).toBe('2026-09-01')
    expect(cache.searchParams.get('date_to')).toBe('2026-09-30')
  })
})

describe('getUsageEnhancedData —— 返回形状', () => {
  it('把真端点的响应**原样**带出去，不改字段名', async () => {
    const out = await getUsageEnhancedData({ days: 7, topN: 10 })

    // summary：total_cost_usd / total_requests（**没有** total_cost，也没有 days）
    expect(out.summary.total_requests).toBe(realUsageFixtures.summary.total_requests)
    expect(out.summary.total_cost_usd).toBe(realUsageFixtures.summary.total_cost_usd)
    expect(out.summary).not.toHaveProperty('total_cost')
    expect(out.summary).not.toHaveProperty('days')

    // cost-trend：entries（**不是** data）
    expect(out.cost_trend.entries).toHaveLength(realUsageFixtures.costTrend.entries.length)
    expect(out.cost_trend.entries[0].dimension_value).toBe(
      realUsageFixtures.costTrend.entries[0].dimension_value,
    )
    expect(out.cost_trend).not.toHaveProperty('data')

    // period-compare：change_pct / change_abs（**不是** delta_cost_pct）
    expect(out.period_compare.change_pct).toBe(realUsageFixtures.periodCompare.change_pct)
    expect(out.period_compare.change_abs).toBe(realUsageFixtures.periodCompare.change_abs)
    expect(out.period_compare).not.toHaveProperty('delta_cost_pct')

    // cache-economics：cache_hit_ratio（**不是** cache_hit_rate）
    expect(out.cache_economics.cache_hit_ratio).toBe(
      realUsageFixtures.cacheEconomics.cache_hit_ratio,
    )
    expect(out.cache_economics).not.toHaveProperty('cache_hit_rate')

    // hot-keys：api_key_id / request_count / total_cost_usd（**不是** key_id / key_name）
    expect(out.hot_api_keys).toHaveLength(realUsageFixtures.hotKeys.length)
    expect(out.hot_api_keys[0].api_key_id).toBe(realUsageFixtures.hotKeys[0].api_key_id)
    expect(out.hot_api_keys[0].request_count).toBe(realUsageFixtures.hotKeys[0].request_count)
    expect(out.hot_api_keys[0]).not.toHaveProperty('key_id')

    // days 是请求参数（summary 响应里没有），故由本模块补上
    expect(out.days).toBe(7)
    expect(typeof out.refreshedAt).toBe('string')
  })

  it('hot-keys 返回裸数组时解包成条目列表，且不误报降级', async () => {
    const out = await getUsageEnhancedData()
    expect(Array.isArray(out.hot_api_keys)).toBe(true)
    expect(out.hot_api_keys[0].api_key_id).toBeTruthy()
    expect(out.hot_keys_degraded).toBe(false)
  })

  it('hot-keys 返回降级信封时，条目取自 items 且降级标记透出', async () => {
    mockedReq.mockImplementation(async (_method, url) => {
      const path = new URL(url, 'http://localhost').pathname
      if (path === '/api/usage/hot-keys') {
        return { items: [], degraded: true, degraded_reason: 'usage_ledger_with_current_month' }
      }
      return realUsageResponse(url)
    })

    const out = await getUsageEnhancedData()
    expect(out.hot_api_keys).toEqual([])
    expect(out.hot_keys_degraded).toBe(true)
    expect(out.hot_keys_degraded_reason).toBe('usage_ledger_with_current_month')
  })
})
