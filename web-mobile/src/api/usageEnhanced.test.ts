import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  // 时间窗
  usageTimeRangeMode,
  usageDaysEffective,
  COST_TREND_DAYS_DEFAULT,
  CACHE_ECONOMICS_DAYS_DEFAULT,
  USAGE_DAYS_MIN,
  USAGE_DAYS_MAX,
  PERIOD_FORMAT,
  // cost-trend
  fetchCostTrend,
  unwrapCostTrend,
  costTrendDegraded,
  costTrendDegradedReason,
  costTrendHasMergedOthers,
  costTrendPercentMeaningless,
  costTrendTotalDisagrees,
  costTrendGroupByValid,
  costTrendSwitchesBaseTable,
  costTrendGroupByDefault,
  COST_TREND_GROUP_BYS,
  REQUEST_SIDE_GROUP_BYS,
  // period-compare
  fetchPeriodCompare,
  unwrapPeriodCompare,
  periodCompareDegraded,
  periodCompareDegradedReason,
  periodChangeMeaningless,
  periodByDimensionMissing,
  periodDimensionTruncated,
  trendWithoutSignificance,
  PERIOD_DIMENSION_LIMIT,
  // cache-economics
  fetchCacheEconomics,
  unwrapCacheEconomics,
  cacheEconomicsDegraded,
  cacheEconomicsDegradedReason,
  cacheHitRatioMeaningless,
  effectiveCostRatioIsFakeFull,
  compressedCountMayBeFailed,
  compressionSavedUnreliable,
} from '@/api/usageEnhanced'

/**
 * usage 增强三条端点的不变量（2026-10-08，第七十批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **`degraded` 恒发（不带 omitempty），`degraded_reason` 是条件键**
 *      （usage_enhanced.go:51-52 / :334-336 / :612-613）
 *      ⇒ 降级时返回 200 + 全 0；不读 `degraded` 就会把「花了 1139 美元」
 *      显示成「没花钱」。**判降级只能用值，不能用「键缺失」**。
 *   ② ★★★★★ **四个「节省」数字是按硬编码假设推算的**（:729/:733/:739）
 *      ⇒ 不是实测账单。
 *   ③ ★★★★ `compressed_requests === 0` 分不清「没压缩」与「查询静默失败」（:694-697）。
 *   ④ ★★★★ `group_by` 决定基表（`planCostTrend` :91-126）——
 *      model/provider/api_key 读 usage_ledger，work_type/intent 读 request_logs。
 *   ⑤ ★★★ 三条端点时间窗缺省不同（7 / 30 / 不接受），
 *      且参数名是 **start/end**（不是 from/to），只给一个 ⇒ 400。
 *   ⑥ ★★★ `trend` 阈值 ±5%、`significant` ±20% ⇒ 可能「up 但不 significant」。
 *   ⑦ ★★★ 上期成本为 0 ⇒ `change_pct` 留 0，不是无穷大。
 *   ⑧ ★★ `by_dimension` 只在有结果时放 `"model"` 键，且明细硬编码 `LIMIT 10`。
 *   ⑨ ★★ `effective_cost_ratio` 分母为 0 时留 **1.0**（:746）不是 0。
 *  ⑩ ★★ `savings_rate` 是 0-100，而两个 ratio 是 0-1 ⇒ 同响应两种单位。
 *
 * 夹具说明：字段名逐字取自后端 struct 的 json tag；数值按后端 SQL 的
 * 计算式构造（`percentage = cost/total*100`、`cache_hit_ratio`、
 * `dollars_saved = cacheRead × avgPrice × 0.9` 等）。
 */

vi.mock('@/api/client', () => ({ req: vi.fn() }))

const reqMock = req as unknown as ReturnType<typeof vi.fn>

beforeEach(() => {
  reqMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

function arg(n = 0): { method: string; path: string } {
  const c = reqMock.mock.calls[n]!
  return { method: c[0] as string, path: c[1] as string }
}

function stub(payload: unknown): void {
  reqMock.mockResolvedValue(payload)
}

/* ── 夹具：cost-trend ──────────────────────────────────────────────── */

/**
 * usage_enhanced.go:25-53。
 * 500 + 300 + 200 = 1000；entries 里 500+300+200=1000；other 0
 * ⇒ total_cost 与 entries 之和一致（不矛盾）。
 */
function costTrendFixture() {
  return {
    group_by: 'model',
    date_from: '2026-10-01',
    date_to: '2026-10-08',
    total_cost: 1000,
    entries: [
      {
        dimension_value: 'claude-opus-5', request_count: 500,
        total_cost_usd: 500, input_cost_usd: 200, output_cost_usd: 300,
        prompt_tokens: 1000000, completion_tokens: 200000,
        avg_latency_ms: 1200, error_rate: 0.02, percentage: 50,
      },
      {
        dimension_value: 'gpt-5', request_count: 300,
        total_cost_usd: 300, input_cost_usd: 120, output_cost_usd: 180,
        prompt_tokens: 600000, completion_tokens: 120000,
        avg_latency_ms: 900, error_rate: 0.01, percentage: 30,
      },
      {
        // ★ COALESCE(…, 'unknown') ⇒ 缺失的分组值归到 'unknown'
        dimension_value: 'unknown', request_count: 200,
        total_cost_usd: 200, input_cost_usd: 80, output_cost_usd: 120,
        prompt_tokens: 400000, completion_tokens: 80000,
        avg_latency_ms: 1500, error_rate: 0.05, percentage: 20,
      },
    ],
    other_cost: 0,
    other_count: 0,
    degraded: false,
  }
}

/** ★ 缺表降级：200 + 空 entries + 全 0 + degraded=true（:215-228）。 */
function costTrendDegradedFixture() {
  return {
    group_by: 'model',
    date_from: '2026-10-01',
    date_to: '2026-10-08',
    total_cost: 0,
    entries: [],
    other_cost: 0,
    other_count: 0,
    degraded: true,
    degraded_reason: 'relation "usage_ledger" does not exist',
  }
}

/* ── 夹具：period-compare ──────────────────────────────────────────── */

function periodCompareFixture() {
  return {
    current: {
      period: '2026-10', total_cost_usd: 1139.62, total_requests: 12000,
      total_tokens: 9000000, avg_cost_per_req: 0.0949683, unique_models: 7,
    },
    previous: {
      period: '2026-09', total_cost_usd: 1000, total_requests: 10000,
      total_tokens: 8000000, avg_cost_per_req: 0.1, unique_models: 6,
    },
    change_pct: 13.962,
    change_abs: 139.62,
    trend: 'up',
    significant: false,
    by_dimension: {
      model: [
        { dimension_value: 'claude-opus-5', current_cost: 600, previous_cost: 500, change_pct: 20 },
        { dimension_value: 'gpt-5', current_cost: 539.62, previous_cost: 500, change_pct: 7.924 },
      ],
    },
    degraded: false,
  }
}

/* ── 夹具：cache-economics ────────────────────────────────────────── */

/**
 * cache_read=200000, prompt=300000 ⇒ 可缓存 token 500000，dollars_spent=500
 * ⇒ avgPricePerToken = 500/500000 = 0.001
 * ⇒ dollars_saved = 200000 × 0.001 × 0.9 = 180
 * ⇒ compressed=10 ⇒ compression_saved = 10 × 8000 × 0.001 = 80
 * ⇒ total_saved = 260；potential = 760
 * ⇒ effective_cost_ratio = 500/760 ≈ 0.657895；savings_rate = 260/760×100 ≈ 34.21
 */
function cacheEconomicsFixture() {
  return {
    date_from: '2026-09-09',
    date_to: '2026-10-08',
    total_requests: 8000,
    cache_read_tokens: 200000,
    prompt_tokens: 300000,
    cache_hit_ratio: 0.4,
    dollars_saved: 180,
    dollars_spent: 500,
    effective_cost_ratio: 0.657895,
    compressed_requests: 10,
    compression_saved: 80,
    total_saved: 260,
    savings_rate: 34.2105,
    degraded: false,
  }
}

/* ═══════════════════════════════════════════════════════════════════════
 * ⑤ 时间窗：参数名与缺省
 * ═══════════════════════════════════════════════════════════════════════ */

describe('usage / 时间窗参数', () => {
  it('参数名是 start/end（不是 from/to）', () => {
    expect(usageTimeRangeMode({ start: '2026-10-01', end: '2026-10-08' })).toBe('custom')
  })

  it('都不给 ⇒ days 口径；只给一个也判 custom（后端会 400）', () => {
    expect(usageTimeRangeMode(undefined)).toBe('days')
    expect(usageTimeRangeMode({ days: 7 })).toBe('days')
    expect(usageTimeRangeMode({ start: '2026-10-01' })).toBe('custom')
  })

  it('days 是 **clamp 到 [1,366]**，不是回落缺省', () => {
    expect(usageDaysEffective(0, 7)).toBe(USAGE_DAYS_MIN)
    expect(usageDaysEffective(-3, 7)).toBe(1)
    expect(usageDaysEffective(400, 7)).toBe(USAGE_DAYS_MAX)
    expect(usageDaysEffective(30, 7)).toBe(30)
  })

  it('★ 同族两条端点的 days 缺省不同：cost-trend 7 天、cache-economics 30 天', () => {
    expect(COST_TREND_DAYS_DEFAULT).toBe(7)
    expect(CACHE_ECONOMICS_DAYS_DEFAULT).toBe(30)
  })

  it('不传 days 时两条端点各发各的缺省', async () => {
    stub(costTrendFixture())
    await fetchCostTrend()
    expect(arg().path).toContain('days=7')
    stub(cacheEconomicsFixture())
    await fetchCacheEconomics()
    expect(arg(1).path).toContain('days=30')
  })

  it('★ custom 口径下**不发 days**，只发 start/end', async () => {
    stub(costTrendFixture())
    await fetchCostTrend({ days: 7, start: '2026-10-01', end: '2026-10-08' })
    expect(arg().path).toContain('start=2026-10-01')
    expect(arg().path).toContain('end=2026-10-08')
    expect(arg().path).not.toContain('days=')
  })

  it('★ 只给 start 时照样发出去，让后端 400（不静默补齐 end）', async () => {
    stub(costTrendFixture())
    await fetchCostTrend({ start: '2026-10-01' })
    expect(arg().path).toContain('start=2026-10-01')
    expect(arg().path).not.toContain('end=')
    expect(arg().path).not.toContain('days=')
  })

  it('周期格式常量是 YYYY-MM（与日期的 YYYY-MM-DD 不同）', () => {
    expect(PERIOD_FORMAT).toBe('YYYY-MM')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ①④ cost-trend
 * ═══════════════════════════════════════════════════════════════════════ */

describe('usage / cost-trend', () => {
  it('正常解包三条 entries', () => {
    const m = unwrapCostTrend(costTrendFixture())
    expect(m.entries).toHaveLength(3)
    expect(m.total_cost).toBe(1000)
  })

  it('★ 缺 group_by 时按 model 发（后端缺省，:136-138）', async () => {
    stub(costTrendFixture())
    await fetchCostTrend()
    expect(arg().path).toContain('group_by=model')
    expect(costTrendGroupByDefault()).toBe('model')
  })

  it('合法维度集合与后端一致（五个）', () => {
    expect(COST_TREND_GROUP_BYS).toHaveLength(5)
    expect([...COST_TREND_GROUP_BYS]).toEqual(['model', 'provider', 'intent', 'work_type', 'api_key'])
  })

  it('★ work_type / intent 会**换基表**（走 request_logs 而不是 ledger）', () => {
    expect([...REQUEST_SIDE_GROUP_BYS]).toEqual(['work_type', 'intent'])
    expect(costTrendSwitchesBaseTable('work_type')).toBe(true)
    expect(costTrendSwitchesBaseTable('intent')).toBe(true)
    expect(costTrendSwitchesBaseTable('model')).toBe(false)
    expect(costTrendSwitchesBaseTable('provider')).toBe(false)
    expect(costTrendSwitchesBaseTable('api_key')).toBe(false)
  })

  it('非法维度不是有效值（后端会 400，不静默回落）', () => {
    expect(costTrendGroupByValid('model')).toBe(true)
    expect(costTrendGroupByValid('task_type')).toBe(false)
    expect(costTrendGroupByValid('')).toBe(false)
  })

  it('★ 降级时能读出来（degraded 是恒发布尔，不是缺键）', () => {
    const m = unwrapCostTrend(costTrendDegradedFixture())
    expect(costTrendDegraded(m)).toBe(true)
    expect(costTrendDegradedReason(m)).toContain('does not exist')
  })

  it('健康时 degraded 显式为 false（不是「键缺失」）', () => {
    const m = unwrapCostTrend(costTrendFixture())
    expect('degraded' in m).toBe(true)
    expect(costTrendDegraded(m)).toBe(false)
    expect(costTrendDegradedReason(m)).toBeNull()
  })

  it('★★ 降级与「真的一分没花」形状相同 ⇒ 必须靠 degraded 区分', () => {
    const d = unwrapCostTrend(costTrendDegradedFixture())
    expect(d.total_cost).toBe(0)
    expect(d.entries).toEqual([])
  })

  it('★ other_count>0 ⇒ entries 不是全集', () => {
    const f = { ...costTrendFixture(), other_cost: 50, other_count: 3 }
    const m = unwrapCostTrend(f)
    expect(costTrendHasMergedOthers(m)).toBe(true)
  })

  it('无合并时 other_count=0（负控）', () => {
    expect(costTrendHasMergedOthers(unwrapCostTrend(costTrendFixture()))).toBe(false)
  })

  it('total_cost=0 ⇒ 百分比无意义（不是「都是 0%」）', () => {
    const m = unwrapCostTrend({ ...costTrendFixture(), total_cost: 0 })
    expect(costTrendPercentMeaningless(m)).toBe(true)
    expect(costTrendPercentMeaningless(unwrapCostTrend(costTrendFixture()))).toBe(false)
  })

  it('total_cost 与「entries 之和 + other」对不上 ⇒ 判定矛盾', () => {
    const f = { ...costTrendFixture(), other_cost: 50, other_count: 1, total_cost: 900 }
    expect(costTrendTotalDisagrees(unwrapCostTrend(f))).toBe(true)
  })

  it('一致时判定无矛盾（负控）', () => {
    expect(costTrendTotalDisagrees(unwrapCostTrend(costTrendFixture()))).toBe(false)
  })

  it('entries 缺子键 ⇒ 抛错并带下标', () => {
    const f = JSON.parse(JSON.stringify(costTrendFixture())) as { entries: Record<string, unknown>[] }
    delete f.entries[1]!.percentage
    expect(() => unwrapCostTrend(f)).toThrow(/entries\[1\]/)
  })

  it('degraded 键缺失 ⇒ 抛错（恒发字段缺失就是契约破损）', () => {
    const f = { ...costTrendFixture() } as Record<string, unknown>
    delete f.degraded
    expect(() => unwrapCostTrend(f)).toThrow(/degraded/)
  })

  it('degraded 不是布尔 ⇒ 抛错', () => {
    expect(() => unwrapCostTrend({ ...costTrendFixture(), degraded: 'true' })).toThrow(/不是布尔/)
  })

  it('拿到信封形状 ⇒ 拒绝', () => {
    expect(() => unwrapCostTrend({ success: true, timestamp: 1, data: costTrendFixture() }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('非对象响应 ⇒ 报出实际形状', () => {
    expect(() => unwrapCostTrend([])).toThrow(/实得 array/)
    expect(() => unwrapCostTrend(null)).toThrow(/实得 null/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ⑥⑦⑧ period-compare
 * ═══════════════════════════════════════════════════════════════════════ */

describe('usage / period-compare', () => {
  it('正常解包两个周期与维度明细', () => {
    const m = unwrapPeriodCompare(periodCompareFixture())
    expect(m.current.period).toBe('2026-10')
    expect(m.by_dimension.model).toHaveLength(2)
  })

  it('current / previous 都会原样发（后端两个都必填，缺任一 400）', async () => {
    stub(periodCompareFixture())
    await fetchPeriodCompare({ current: '2026-10', previous: '2026-09' })
    expect(arg().path).toContain('current=2026-10')
    expect(arg().path).toContain('previous=2026-09')
  })

  it('★ trend 阈值是 ±5%：13.962% 判为 up', () => {
    expect(unwrapPeriodCompare(periodCompareFixture()).trend).toBe('up')
  })

  it('★ significant 阈值是 ±20%：13.962% 判为不显著', () => {
    expect(unwrapPeriodCompare(periodCompareFixture()).significant).toBe(false)
  })

  it('★★ up 但不 significant 是可能的（两套阈值不同）', () => {
    expect(trendWithoutSignificance(unwrapPeriodCompare(periodCompareFixture()))).toBe(true)
  })

  it('趋势与显著性都到位时不再提示（负控）', () => {
    const f = { ...periodCompareFixture(), significant: true }
    expect(trendWithoutSignificance(unwrapPeriodCompare(f))).toBe(false)
  })

  it('★ 上期成本为 0 ⇒ change_pct 无意义（后端留 0，不是无穷大）', () => {
    const f = JSON.parse(JSON.stringify(periodCompareFixture())) as Record<string, unknown>
    ;(f.previous as Record<string, unknown>).total_cost_usd = 0
    f.change_pct = 0
    expect(periodChangeMeaningless(unwrapPeriodCompare(f))).toBe(true)
  })

  it('上期有成本时判定有意义（负控）', () => {
    expect(periodChangeMeaningless(unwrapPeriodCompare(periodCompareFixture()))).toBe(false)
  })

  it('★★ by_dimension 缺 model 键 ⇒ 查询失败或无变化，二者同形', () => {
    const f = { ...periodCompareFixture(), by_dimension: {} }
    expect(periodByDimensionMissing(unwrapPeriodCompare(f))).toBe(true)
  })

  it('model 有明细时判定为已查（负控）', () => {
    expect(periodByDimensionMissing(unwrapPeriodCompare(periodCompareFixture()))).toBe(false)
  })

  it('★ 维度明细硬编码 LIMIT 10 ⇒ 满 10 条即视为截断', () => {
    const f = JSON.parse(JSON.stringify(periodCompareFixture())) as { by_dimension: { model: unknown[] } }
    f.by_dimension.model = Array.from({ length: PERIOD_DIMENSION_LIMIT }, (_, i) => ({
      dimension_value: `m${i}`, current_cost: 1, previous_cost: 1, change_pct: 0,
    }))
    expect(periodDimensionTruncated(unwrapPeriodCompare(f))).toBe(true)
    expect(periodDimensionTruncated(unwrapPeriodCompare(periodCompareFixture()))).toBe(false)
  })

  it('降级时能读出来且带原因', () => {
    const f = { ...periodCompareFixture(), degraded: true, degraded_reason: 'column ul.gw_session_id does not exist' }
    const m = unwrapPeriodCompare(f)
    expect(periodCompareDegraded(m)).toBe(true)
    expect(periodCompareDegradedReason(m)).toContain('does not exist')
  })

  it('降级时两个周期都是零值（与「真没花钱」同形）', () => {
    const f = {
      ...periodCompareFixture(), degraded: true, degraded_reason: 'x',
      current: { period: '2026-10', total_cost_usd: 0, total_requests: 0, total_tokens: 0, avg_cost_per_req: 0, unique_models: 0 },
      previous: { period: '2026-09', total_cost_usd: 0, total_requests: 0, total_tokens: 0, avg_cost_per_req: 0, unique_models: 0 },
    }
    const m = unwrapPeriodCompare(f)
    expect(m.current.total_cost_usd).toBe(0)
    expect(periodCompareDegraded(m)).toBe(true)
  })

  it('★ period-compare 的 degraded 键缺失 ⇒ 抛错（恒发字段）', () => {
    const f = { ...periodCompareFixture() } as Record<string, unknown>
    delete f.degraded
    expect(() => unwrapPeriodCompare(f)).toThrow(/degraded/)
  })

  it('period-compare 的 degraded 不是布尔 ⇒ 抛错', () => {
    expect(() => unwrapPeriodCompare({ ...periodCompareFixture(), degraded: 'no' }))
      .toThrow(/不是布尔/)
  })

  it('current 不是对象 ⇒ 抛错', () => {
    expect(() => unwrapPeriodCompare({ ...periodCompareFixture(), current: 1 }))
      .toThrow(/current 不是对象/)
  })

  it('PeriodStats 缺子键 ⇒ 抛错', () => {
    const f = JSON.parse(JSON.stringify(periodCompareFixture())) as { previous: Record<string, unknown> }
    delete f.previous.unique_models
    expect(() => unwrapPeriodCompare(f)).toThrow(/unique_models/)
  })

  it('★ by_dimension 不是对象 ⇒ 抛错', () => {
    expect(() => unwrapPeriodCompare({ ...periodCompareFixture(), by_dimension: [] }))
      .toThrow(/by_dimension 不是对象/)
  })

  it('★ period-compare 缺参时**不静默填默认值**（后端两个都必填，该 400 就 400）', async () => {
    stub(periodCompareFixture())
    await fetchPeriodCompare({})
    // 两个参数都发成空串，让后端按「缺参数」报 400
    expect(arg().path).toBe('/api/admin/usage/period-compare?current=&previous=')
  })

  it('by_dimension 元素缺子键 ⇒ 抛错并带维度名', () => {
    const f = JSON.parse(JSON.stringify(periodCompareFixture())) as { by_dimension: { model: Record<string, unknown>[] } }
    delete f.by_dimension.model[0]!.change_pct
    expect(() => unwrapPeriodCompare(f)).toThrow(/by_dimension\["model"\]\[0\]/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ②③⑨⑩ cache-economics
 * ═══════════════════════════════════════════════════════════════════════ */

describe('usage / cache-economics', () => {
  it('正常解包', () => {
    const m = unwrapCacheEconomics(cacheEconomicsFixture())
    expect(m.cache_read_tokens).toBe(200000)
    expect(m.degraded).toBe(false)
  })

  it('★ 降级时能读出来（200 + 全 0）', () => {
    const f = { ...cacheEconomicsFixture(), degraded: true, degraded_reason: 'relation missing' }
    const m = unwrapCacheEconomics(f)
    expect(cacheEconomicsDegraded(m)).toBe(true)
    expect(cacheEconomicsDegradedReason(m)).toContain('missing')
  })

  it('★ 没有 token 时 cache_hit_ratio 的 0 无意义', () => {
    const f = { ...cacheEconomicsFixture(), cache_read_tokens: 0, prompt_tokens: 0, cache_hit_ratio: 0 }
    expect(cacheHitRatioMeaningless(unwrapCacheEconomics(f))).toBe(true)
    expect(cacheHitRatioMeaningless(unwrapCacheEconomics(cacheEconomicsFixture()))).toBe(false)
  })

  it('★★ 没有请求时 effective_cost_ratio 留 1.0 ⇒ 不能当「成本 100%」', () => {
    const f = { ...cacheEconomicsFixture(), total_requests: 0, effective_cost_ratio: 1 }
    expect(effectiveCostRatioIsFakeFull(unwrapCacheEconomics(f))).toBe(true)
  })

  it('有请求时不是那个假满值（负控）', () => {
    expect(effectiveCostRatioIsFakeFull(unwrapCacheEconomics(cacheEconomicsFixture()))).toBe(false)
  })

  it('★ compressed_requests=0 ⇒ 分不清「没压缩」与「查询静默失败」', () => {
    const f = { ...cacheEconomicsFixture(), compressed_requests: 0, compression_saved: 0 }
    expect(compressedCountMayBeFailed(unwrapCacheEconomics(f))).toBe(true)
  })

  it('★ 压缩计数不可信时整条估算链都不可信', () => {
    const f = { ...cacheEconomicsFixture(), compressed_requests: 0 }
    expect(compressionSavedUnreliable(unwrapCacheEconomics(f))).toBe(true)
  })

  it('计数有值且未降级时判定可信（负控）', () => {
    expect(compressionSavedUnreliable(unwrapCacheEconomics(cacheEconomicsFixture()))).toBe(false)
  })

  it('降级时即使计数有值也判不可信', () => {
    const f = { ...cacheEconomicsFixture(), degraded: true, degraded_reason: 'x' }
    expect(compressionSavedUnreliable(unwrapCacheEconomics(f))).toBe(true)
  })

  it('缺必检键 ⇒ 抛错并点名', () => {
    const f = { ...cacheEconomicsFixture() } as Record<string, unknown>
    delete f.savings_rate
    expect(() => unwrapCacheEconomics(f)).toThrow(/savings_rate/)
  })

  it('degraded 键缺失 ⇒ 抛错（恒发字段）', () => {
    const f = { ...cacheEconomicsFixture() } as Record<string, unknown>
    delete f.degraded
    expect(() => unwrapCacheEconomics(f)).toThrow(/degraded/)
  })

  it('拿到信封形状 ⇒ 拒绝', () => {
    expect(() => unwrapCacheEconomics({ success: true, timestamp: 2, data: cacheEconomicsFixture() }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('fetchCacheEconomics 打对路径且带 days 缺省 30', async () => {
    stub(cacheEconomicsFixture())
    await fetchCacheEconomics()
    expect(arg().method).toBe('GET')
    expect(arg().path).toBe('/api/admin/usage/cache-economics?days=30')
  })
})