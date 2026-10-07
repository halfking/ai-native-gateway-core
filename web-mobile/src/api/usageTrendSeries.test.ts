import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  // 常量
  TREND_DAYS_DEFAULT,
  TREND_DAYS_MIN,
  TREND_DAYS_MAX,
  TREND_TOP_DEFAULT,
  TREND_TOP_MIN,
  TREND_TOP_MAX,
  TREND_MAX_MODELS,
  TREND_MODELS_SQL_LIMIT,
  TREND_OTHERS_KEY,
  TREND_UNKNOWN_MODEL,
  TREND_BUCKET_5M,
  TREND_BUCKET_15M,
  TREND_BUCKET_60M,
  TREND_SOURCES,
  TREND_SOURCE_DETAIL,
  TREND_SOURCE_PROVIDER,
  TREND_SOURCE_DIM,
  TREND_SERIES_KEYS,
  TREND_SERIES_MODEL_KEYS,
  TREND_POINT_KEYS,
  TREND_MODELS_KEYS,
  TREND_MODEL_ENTRY_KEYS,
  // 时间窗
  trendDaysEffective,
  trendTopEffective,
  trendBucketMinutesForDays,
  trendBucketMinutesForSpan,
  trendPresetStartUtc,
  trendCustomRange,
  trendQuery,
  // 过滤
  trendSourceForFilters,
  trendSourceRealTable,
  trendSourceValid,
  trendFilterIdInvalid,
  trendFilterIgnored,
  trendModelsNormalize,
  trendModelsDroppedCount,
  // 取数与解包
  fetchTrendSeries,
  fetchTrendModels,
  unwrapTrendSeries,
  unwrapTrendModels,
  // 降级语义
  trendDegraded,
  trendDegradedReason,
  trendMissingView,
  trendSeriesIsGenuinelyEmpty,
  trendBucketMinutesKnown,
  trendTopEchoed,
  // 折叠
  trendWasFolded,
  trendFoldSuppressedByModelSelection,
  trendOthersLine,
  trendOthersIsLast,
  trendSeriesNonIncreasing,
  // trend-models 特有
  trendModelsPossiblyTruncated,
  trendModelsListIsConstrained,
  // hint
  trendHintLocal,
  trendHintDisagrees,
  // 时间窗形态
  trendEndIsNextMidnight,
  trendEndLooksLikeNow,
  trendSelectedEndDay,
} from '@/api/usageTrendSeries'
import type {
  TrendSeriesResponse,
  TrendModelsResponse,
  TrendModelSeries,
} from '@/api/usageTrendSeries'

/**
 * usage trend-series / trend-models 两条只读端点的不变量（2026-10-08，第七十一批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **`degraded` 是恒发字段（不带 omitempty）**（usage_trend_series.go:77/:97）
 *      —— **第二次遇到**（第七十批 usageEnhanced 同款），且
 *      `dashboard_degrade.go:105` 的注释写明「与 PeriodCompareResponse.Degraded 同理」
 *      ⇒ 这是本仓既定风格。压缩统计（第六十八批）的 omitempty 条件键恰好相反。
 *   ② ★★★★★ **降级时 `top` 与 `bucket_minutes` 仍是解析后的值**（:204-205）
 *      ⇒ 「降级 + 空序列」只能靠 `degraded` 一个键与「零用量」区分。
 *   ③ ★★★★★ **`days` 的 clamp 是 90 不是 366**（`dashboard_board.go:209-211`
 *      的 `boardDays`，不是 `resolveUsageTimeRange`）⇒ 起点减 `days-1` 天，
 *      days=7 实际是**六天前**零点起。
 *   ④ ★★★★★ **自定义区间左闭右开**（`usage.go:1469` `endDay.Add(24*time.Hour)`）
 *      ⇒ 响应 `end` 是**次日零点**；而预设路径 `end = now`（带时分秒）。
 *   ⑤ ★★★★ **分桶两套规则**：预设按 `Days`、自定义按实际 `span`
 *      ⇒ 同样 24 小时跨度，custom 是 5 分钟桶而 days=2 预设是 15 分钟桶。
 *   ⑥ ★★★★ **非法 / 负数 ID 静默换数据档**（`queryInt` 回落 + `> 0` 判据）
 *      ⇒ `provider_id=abc` 与 `provider_id=-5` 都落到 dim 档，不报 400。
 *   ⑦ ★★★★ **`source` 少 `_without_customer_id` 后缀**（:158 vs :407）
 *      ⇒ 不能当真实表名用。
 *   ⑧ ★★★ **折叠只在「没指定 model」且「模型数 > top」时发生**（:439 / :455-457）
 *      ⇒ 判据必须用 `>` 而不是 `>=`；指定 model 时一律不折叠。
 *   ⑨ ★★★ **三个不同的上限，后两个都不回显**：top 回显 clamp 后的值，
 *      model 多选截断到 20、`trend-models` 的 `LIMIT 100` 完全静默。
 *
 * ⚠️ 桌面 `web/src/api/usage.ts:456`/`:474` 把 `degraded` 声明成可选
 *   （后端恒发 ⇒ 桌面「缺键即降级」恒假），
 *   `UsageTrendExplorer.vue:167` 的 `resp.bucket_minutes || 60` 兜底恒不生效
 *   ⇒ **这两处不抄**。
 *
 * 夹具纪律：数字一律硬写后端源码字面量（90 / 20 / 8 / 5 / 15 / 60 / 100），
 * **不用**被测常量去造数据 —— 否则改常量时夹具跟着变，判据恒真。
 */

vi.mock('@/api/client', () => ({ req: vi.fn() }))

const reqMock = req as unknown as ReturnType<typeof vi.fn>

beforeEach(() => {
  reqMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** 让下一次 req 成功返回给定载荷。 */
function ok(payload: unknown): void {
  reqMock.mockResolvedValue(payload)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 夹具：usage_trend_series.go 逐字抄
 * ═══════════════════════════════════════════════════════════════════════════ */

/** :51-57 usageTrendPoint。 */
function pt(bucket: string, requests: number, tokens: number, credits: number, cost: number) {
  return { bucket, requests, tokens, credits, cost_usd: cost }
}

/** :59-66 usageTrendSeries（total 由调用方给全，判据不做推断）。 */
function ser(
  model: string,
  total_requests: number,
  total_tokens: number,
  total_credits: number,
  total_cost_usd: number,
  points: ReturnType<typeof pt>[],
): TrendModelSeries {
  return { model, total_requests, total_tokens, total_credits, total_cost_usd, points }
}

/** :68-81 成功路径（degraded 不带 omitempty ⇒ 恒发 false，三个标记键不发）。 */
function seriesOk(over: Partial<TrendSeriesResponse> = {}): Record<string, unknown> {
  return {
    start: '2026-10-07T00:00:00Z',
    end: '2026-10-08T12:34:56Z',
    bucket_minutes: 5,
    top: 8,
    source: 'request_stats_dim_minute',
    series: [
      ser('gpt-4o', 30, 3000, 12, 1.5, [pt('2026-10-07T00:00:00Z', 30, 3000, 12, 1.5)]),
    ],
    degraded: false,
    ...over,
  }
}

/** :201-212 降级路径：空序列 + degraded=true + 三个标记键，且 top/bucket 仍是解析值。 */
function seriesDegraded(over: Partial<TrendSeriesResponse> = {}): Record<string, unknown> {
  return {
    start: '2026-10-07T00:00:00Z',
    end: '2026-10-08T12:34:56Z',
    bucket_minutes: 15,
    top: 8,
    source: 'request_stats_dim_minute',
    series: [],
    degraded: true,
    degraded_reason: 'request_stats_dim_minute',
    missing_view: 'request_stats_dim_minute',
    hint: '数据视图 request_stats_dim_minute 尚未初始化，请先执行数据聚合迁移',
    ...over,
  }
}

/** :83-89 usageTrendModelEntry。 */
function entry(model: string, requests: number, tokens: number, credits: number, cost: number) {
  return { model, requests, tokens, credits, cost_usd: cost }
}

/** :295-300 trend-models 成功路径。 */
function modelsOk(over: Partial<TrendModelsResponse> = {}): Record<string, unknown> {
  return {
    start: '2026-10-07T00:00:00Z',
    end: '2026-10-08T12:34:56Z',
    source: 'request_stats_dim_minute',
    models: [entry('gpt-4o', 30, 3000, 12, 1.5)],
    degraded: false,
    ...over,
  }
}

/** :277-287 trend-models 降级路径。 */
function modelsDegraded(over: Partial<TrendModelsResponse> = {}): Record<string, unknown> {
  return {
    start: '2026-10-07T00:00:00Z',
    end: '2026-10-08T12:34:56Z',
    source: 'request_stats_dim_minute',
    models: [],
    degraded: true,
    degraded_reason: 'request_stats_dim_minute',
    missing_view: 'request_stats_dim_minute',
    hint: '数据视图 request_stats_dim_minute 尚未初始化，请先执行数据聚合迁移',
    ...over,
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * ① 常量逐字对齐后端字面量（改常量无人发现就是个洞）
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('days clamp 上界是 90 而不是 366', () => {
    // admin/dashboard_board.go:205-211 —— 缺省 1、clamp [1,90]
    expect(TREND_DAYS_DEFAULT).toBe(1)
    expect(TREND_DAYS_MIN).toBe(1)
    expect(TREND_DAYS_MAX).toBe(90)
  })

  it('top 默认 8、上下界 1 与 20', () => {
    // admin/usage_trend_series.go:129 / :144-149
    expect(TREND_TOP_DEFAULT).toBe(8)
    expect(TREND_TOP_MIN).toBe(1)
    expect(TREND_TOP_MAX).toBe(20)
  })

  it('model 多选上限 20', () => {
    // admin/usage_trend_series.go:46 usageTrendMaxModels = 20
    expect(TREND_MAX_MODELS).toBe(20)
  })

  it('trend-models 的 SQL 上限 100', () => {
    // admin/usage_trend_series.go:524 / :554 / :592 —— 三处都写死 LIMIT 100
    expect(TREND_MODELS_SQL_LIMIT).toBe(100)
  })

  it('折叠线名是 __others__ 不是 others', () => {
    // admin/usage_trend_series.go:42 usageTrendOthersKey = "__others__"
    expect(TREND_OTHERS_KEY).toBe('__others__')
  })

  it('明细档模型兜底名是 __unknown__', () => {
    // admin/usage_trend_series.go:49
    expect(TREND_UNKNOWN_MODEL).toBe('__unknown__')
  })

  it('分桶档位只有 5 / 15 / 60', () => {
    // admin/board_time_range.go:47-67
    expect(TREND_BUCKET_5M).toBe(5)
    expect(TREND_BUCKET_15M).toBe(15)
    expect(TREND_BUCKET_60M).toBe(60)
  })

  it('对外 source 只有三个值且不含 _without_customer_id', () => {
    // admin/usage_trend_series.go:155-164 —— 对外少一个后缀
    expect(TREND_SOURCES).toEqual([
      'request_logs_with_current_month',
      'request_stats_minute',
      'request_stats_dim_minute',
    ])
    expect(TREND_SOURCES.some((s) => s.includes('_without_customer_id'))).toBe(false)
  })

  it('degraded 不在必检键里 —— 由类型校验把关', () => {
    // 把 degraded 列进必检键是冗余：typeof 检查已覆盖缺键与类型错
    expect(TREND_SERIES_KEYS as readonly string[]).not.toContain('degraded')
    expect(TREND_MODELS_KEYS as readonly string[]).not.toContain('degraded')
  })

  it('成功路径的三个标记键不在必检键里', () => {
    // degraded_reason / missing_view / hint 都是 omitempty 条件键
    expect(TREND_SERIES_KEYS as readonly string[]).not.toContain('degraded_reason')
    expect(TREND_MODELS_KEYS as readonly string[]).not.toContain('hint')
    expect(TREND_SERIES_MODEL_KEYS as readonly string[]).toContain('points')
    expect(TREND_POINT_KEYS as readonly string[]).toContain('cost_usd')
    expect(TREND_MODEL_ENTRY_KEYS as readonly string[]).toContain('credits')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ③ days clamp —— 上界 90（本族最易被别族的 366 带偏）
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('days 解析口径', () => {
  it('days 缺省回落到 1', () => {
    expect(trendDaysEffective()).toBe(1)
  })

  it('days 下界夹到 1', () => {
    expect(trendDaysEffective(0)).toBe(1)
    expect(trendDaysEffective(-5)).toBe(1)
  })

  it('days 上界夹到 90', () => {
    expect(trendDaysEffective(91)).toBe(90)
    expect(trendDaysEffective(9999)).toBe(90)
  })

  it('days 366 也被夹到 90', () => {
    // ★ 与 usageEnhanced 的 366 上界相反 —— 本族走 boardDays 不是 resolveUsageTimeRange
    expect(trendDaysEffective(366)).toBe(90)
  })

  it('days 边界值原样通过', () => {
    expect(trendDaysEffective(1)).toBe(1)
    expect(trendDaysEffective(90)).toBe(90)
  })

  it('非数字 days 回落缺省', () => {
    expect(trendDaysEffective(NaN)).toBe(1)
    expect(trendDaysEffective(Infinity)).toBe(1)
  })
})

describe('top clamp 回显口径', () => {
  it('top 缺省回落到 8', () => {
    expect(trendTopEffective()).toBe(8)
  })

  it('top 下界夹到 1', () => {
    expect(trendTopEffective(0)).toBe(1)
    expect(trendTopEffective(-3)).toBe(1)
  })

  it('top 上界夹到 20', () => {
    expect(trendTopEffective(21)).toBe(20)
    expect(trendTopEffective(999)).toBe(20)
  })

  it('top 边界值原样通过', () => {
    expect(trendTopEffective(1)).toBe(1)
    expect(trendTopEffective(20)).toBe(20)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑤ 分桶两套规则
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('分桶预设档按 Days', () => {
  it('days 1 落五分钟档', () => {
    expect(trendBucketMinutesForDays(1)).toBe(5)
  })

  it('days 2 到 7 落十五分钟档', () => {
    expect(trendBucketMinutesForDays(2)).toBe(15)
    expect(trendBucketMinutesForDays(7)).toBe(15)
  })

  it('days 8 起落六十分钟档', () => {
    expect(trendBucketMinutesForDays(8)).toBe(60)
    expect(trendBucketMinutesForDays(90)).toBe(60)
  })
})

describe('分桶自定义档按实际跨度', () => {
  it('跨度 24 小时落五分钟档', () => {
    expect(trendBucketMinutesForSpan(24 * 60 * 60 * 1000)).toBe(5)
  })

  it('跨度 48 小时边界仍落五分钟档', () => {
    // board_time_range.go:51 `span <= 48*time.Hour` 是闭区间
    expect(trendBucketMinutesForSpan(48 * 60 * 60 * 1000)).toBe(5)
  })

  it('跨度超过 48 小时落十五分钟档', () => {
    expect(trendBucketMinutesForSpan(48 * 60 * 60 * 1000 + 1)).toBe(15)
  })

  it('跨度 14 天边界仍落十五分钟档', () => {
    // board_time_range.go:53 `span <= 14*24*time.Hour` 是闭区间
    expect(trendBucketMinutesForSpan(14 * 24 * 60 * 60 * 1000)).toBe(15)
  })

  it('跨度超过 14 天落六十分钟档', () => {
    expect(trendBucketMinutesForSpan(14 * 24 * 60 * 60 * 1000 + 1)).toBe(60)
  })

  it('同样 24 小时跨度 custom 是 5 分钟而 days=2 预设是 15 分钟', () => {
    // ★★★ 两套规则并存的核心后果：同一实际跨度落在不同档位
    const span = 24 * 60 * 60 * 1000
    expect(trendBucketMinutesForSpan(span)).toBe(5)
    expect(trendBucketMinutesForDays(2)).toBe(15)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ③ 预设起点减 days-1 天
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('预设窗口起点减 days-1 天', () => {
  const today = new Date('2026-10-08T00:00:00Z')

  it('days 1 起点就是今天零点', () => {
    expect(trendPresetStartUtc(1, today).toISOString()).toBe('2026-10-08T00:00:00.000Z')
  })

  it('days 7 起点是六天前零点而不是七天前', () => {
    // board_time_range.go:120 `Add(-(days-1) * 24h)`
    expect(trendPresetStartUtc(7, today).toISOString()).toBe('2026-10-02T00:00:00.000Z')
  })

  it('days 7 的跨度不足 7 整天', () => {
    const start = trendPresetStartUtc(7, today)
    const days = (today.getTime() - start.getTime()) / (24 * 60 * 60 * 1000)
    expect(days).toBe(6)
  })

  it('days 0 按下界夹到 1 当天处理', () => {
    expect(trendPresetStartUtc(0, today).toISOString()).toBe('2026-10-08T00:00:00.000Z')
  })

  it('days 999 按上界夹到 90 后减 89 天', () => {
    expect(trendPresetStartUtc(999, today).toISOString()).toBe('2026-07-11T00:00:00.000Z')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ④ 自定义区间左闭右开
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('自定义区间左闭右开', () => {
  it('end 是用户所选结束日的次日零点', () => {
    // usage.go:1469 `endDay.Add(24*time.Hour).UTC()`
    const r = trendCustomRange(
      new Date('2026-01-01T00:00:00Z'),
      new Date('2026-01-07T00:00:00Z'),
    )
    expect(r.end.toISOString()).toBe('2026-01-08T00:00:00.000Z')
  })

  it('start 就是所选起始日零点', () => {
    const r = trendCustomRange(
      new Date('2026-01-01T00:00:00Z'),
      new Date('2026-01-07T00:00:00Z'),
    )
    expect(r.start.toISOString()).toBe('2026-01-01T00:00:00.000Z')
  })

  it('同一天区间跨度是 24 小时而不是 0', () => {
    const d = new Date('2026-03-05T00:00:00Z')
    const r = trendCustomRange(d, d)
    expect(r.end.getTime() - r.start.getTime()).toBe(24 * 60 * 60 * 1000)
  })

  it('所选结束日比响应 end 早整整一天', () => {
    const r = trendCustomRange(
      new Date('2026-01-01T00:00:00Z'),
      new Date('2026-01-07T00:00:00Z'),
    )
    const selected = new Date(r.end.getTime() - 24 * 60 * 60 * 1000)
    expect(selected.toISOString()).toBe('2026-01-07T00:00:00.000Z')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑥⑦ 数据档与 source
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('数据档选择', () => {
  it('无过滤落 dim 档', () => {
    expect(trendSourceForFilters(undefined)).toBe(TREND_SOURCE_DIM)
    expect(trendSourceForFilters({})).toBe(TREND_SOURCE_DIM)
  })

  it('仅 provider 过滤落 minute 档', () => {
    expect(trendSourceForFilters({ provider_id: 5 })).toBe(TREND_SOURCE_PROVIDER)
  })

  it('带 api_key 过滤落明细档且优先于 provider', () => {
    expect(trendSourceForFilters({ provider_id: 5, api_key_id: 9 })).toBe(TREND_SOURCE_DETAIL)
  })

  it('provider_id 为负数时静默落回 dim 档', () => {
    // ★ `> 0` 判据（:159）⇒ 负数等价于没传，且不报 400
    expect(trendSourceForFilters({ provider_id: -5 })).toBe(TREND_SOURCE_DIM)
  })

  it('provider_id 为 NaN 时静默落回 dim 档', () => {
    expect(trendSourceForFilters({ provider_id: NaN })).toBe(TREND_SOURCE_DIM)
  })

  it('api_key_id 为 0 时不落明细档', () => {
    expect(trendSourceForFilters({ api_key_id: 0 })).toBe(TREND_SOURCE_DIM)
  })
})

describe('source 映射到真实读面', () => {
  it('明细档对外名少 _without_customer_id 后缀', () => {
    // ★ :158 对外 "request_logs_with_current_month" vs :407 真实表名
    expect(TREND_SOURCE_DETAIL).toBe('request_logs_with_current_month')
    expect(trendSourceRealTable(TREND_SOURCE_DETAIL)).toBe(
      'request_logs_with_current_month_without_customer_id',
    )
  })

  it('minute 档与 dim 档对外名与真实表名一致', () => {
    expect(trendSourceRealTable(TREND_SOURCE_PROVIDER)).toBe('request_stats_minute')
    expect(trendSourceRealTable(TREND_SOURCE_DIM)).toBe('request_stats_dim_minute')
  })

  it('未知 source 映射为 null', () => {
    expect(trendSourceRealTable('some_other_view')).toBeNull()
  })

  it('source 合法性校验只认三个值', () => {
    expect(trendSourceValid('request_stats_dim_minute')).toBe(true)
    expect(trendSourceValid('request_logs_with_current_month_without_customer_id')).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑥ 过滤 ID 静默失效
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('过滤 ID 静默失效检测', () => {
  it('正整数 ID 不算失效', () => {
    expect(trendFilterIdInvalid('provider_id', 5)).toBeNull()
    expect(trendFilterIdInvalid('api_key_id', 1)).toBeNull()
  })

  it('ID 为 0 构成失效', () => {
    expect(trendFilterIdInvalid('provider_id', 0)).not.toBeNull()
  })

  it('ID 为负数构成失效', () => {
    expect(trendFilterIdInvalid('api_key_id', -1)).not.toBeNull()
  })

  it('ID 非数字构成失效', () => {
    expect(trendFilterIdInvalid('provider_id', NaN)).not.toBeNull()
    expect(trendFilterIdInvalid('api_key_id', 'abc')).not.toBeNull()
  })

  it('ID 缺省不算失效', () => {
    expect(trendFilterIdInvalid('provider_id', undefined)).toBeNull()
    expect(trendFilterIdInvalid('api_key_id', null)).toBeNull()
  })

  it('汇总判断 provider 正常时返回 null', () => {
    expect(trendFilterIgnored({ provider_id: 5, api_key_id: 9 })).toBeNull()
  })

  it('汇总判断 api_key 负数时报出失效', () => {
    expect(trendFilterIgnored({ provider_id: 5, api_key_id: -1 })).not.toBeNull()
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑨ model 多选规范化与截断
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('model 多选规范化', () => {
  it('重复值只保留一个', () => {
    expect(trendModelsNormalize(['a', 'a', 'b'])).toEqual(['a', 'b'])
  })

  it('空串被剔除', () => {
    expect(trendModelsNormalize(['a', '', 'b'])).toEqual(['a', 'b'])
  })

  it('未给 model 时是空数组', () => {
    expect(trendModelsNormalize(undefined)).toEqual([])
    expect(trendModelsNormalize([])).toEqual([])
  })

  it('保留传入顺序', () => {
    expect(trendModelsNormalize(['z', 'a', 'm'])).toEqual(['z', 'a', 'm'])
  })

  it('21 个不同模型截断到 20', () => {
    // :140 `if len(f.models) >= 20 { break }` —— 夹具硬写 21 个后端字面量
    const raw = [
      'm01', 'm02', 'm03', 'm04', 'm05', 'm06', 'm07', 'm08', 'm09', 'm10',
      'm11', 'm12', 'm13', 'm14', 'm15', 'm16', 'm17', 'm18', 'm19', 'm20',
      'm21',
    ]
    expect(trendModelsNormalize(raw)).toHaveLength(20)
    expect(trendModelsNormalize(raw)[19]).toBe('m20')
  })

  it('19 个不同模型不截断', () => {
    const raw = [
      'm01', 'm02', 'm03', 'm04', 'm05', 'm06', 'm07', 'm08', 'm09', 'm10',
      'm11', 'm12', 'm13', 'm14', 'm15', 'm16', 'm17', 'm18', 'm19',
    ]
    expect(trendModelsNormalize(raw)).toHaveLength(19)
  })

  it('丢弃计数在超限时为 1', () => {
    const raw = [
      'm01', 'm02', 'm03', 'm04', 'm05', 'm06', 'm07', 'm08', 'm09', 'm10',
      'm11', 'm12', 'm13', 'm14', 'm15', 'm16', 'm17', 'm18', 'm19', 'm20',
      'm21',
    ]
    expect(trendModelsDroppedCount(raw)).toBe(1)
  })

  it('丢弃计数在未超限时为 0', () => {
    expect(trendModelsDroppedCount(['a', 'b'])).toBe(0)
    expect(trendModelsDroppedCount(undefined)).toBe(0)
  })

  it('丢弃计数按去重后计数', () => {
    // 25 个位置但只有 21 个不同值 ⇒ 只丢 1 个（夹具硬写 21 个后端字面量）
    const raw = [
      'm01', 'm01', 'm02', 'm03', 'm04', 'm05', 'm06', 'm07',
      'm08', 'm09', 'm10', 'm11', 'm12', 'm13', 'm14', 'm15',
      'm16', 'm17', 'm18', 'm19', 'm20', 'm21',
      'm03', 'm04', 'm05',
    ]
    expect(trendModelsDistinct(raw)).toBe(21)
    expect(trendModelsDroppedCount(raw)).toBe(1)
  })
})

/** 测试侧独立实现的去重计数（空串剔除 + 去重），刻意不走被测函数。 */
function trendModelsDistinct(raw: readonly string[]): number {
  return new Set(raw.filter((m) => m !== '')).size
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 查询串
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('查询串组装', () => {
  it('start 与 end 都缺省时发 days', () => {
    expect(trendQuery()).toBe('?days=1')
    expect(trendQuery({ days: 7 })).toBe('?days=7')
  })

  it('days 超界时客户端先夹到 90', () => {
    expect(trendQuery({ days: 999 })).toBe('?days=90')
  })

  it('只给 start 时不补 end', () => {
    // ★ 只给一个时后端 400，客户端不静默补齐
    expect(trendQuery({ start: '2026-01-01' })).toBe('?start=2026-01-01')
  })

  it('只给 end 时不补 start', () => {
    expect(trendQuery({ end: '2026-01-07' })).toBe('?end=2026-01-07')
  })

  it('start 与 end 同时给时不发 days', () => {
    expect(trendQuery({ start: '2026-01-01', end: '2026-01-07', days: 7 })).toBe(
      '?start=2026-01-01&end=2026-01-07',
    )
  })

  it('model 多选用重复参数而不是逗号串', () => {
    expect(trendQuery({ model: ['a', 'b'] })).toBe('?days=1&model=a&model=b')
  })

  it('model 值里的特殊字符被转义', () => {
    expect(trendQuery({ model: ['gpt 4/o'] })).toBe('?days=1&model=gpt%204%2Fo')
  })

  it('非正整数 provider_id 不发', () => {
    // ★ 发出去会被后端静默当成 0 并换数据档
    expect(trendQuery({ provider_id: -5 })).toBe('?days=1')
    expect(trendQuery({ provider_id: 0 })).toBe('?days=1')
    expect(trendQuery({ provider_id: NaN })).toBe('?days=1')
  })

  it('非正整数 api_key_id 不发', () => {
    expect(trendQuery({ api_key_id: -1 })).toBe('?days=1')
  })

  it('正整数 provider_id 与 api_key_id 都发', () => {
    expect(trendQuery({ provider_id: 5, api_key_id: 9 })).toBe(
      '?days=1&provider_id=5&api_key_id=9',
    )
  })

  it('非整数 ID 发截断后的值', () => {
    // ★ 夹具只差「是否 Math.trunc」一个条件
    expect(trendQuery({ provider_id: 5.7 })).toBe('?days=1&provider_id=5')
  })

  it('top 发原值由后端 clamp 并回显', () => {
    expect(trendQuery({ top: 999 })).toBe('?days=1&top=999')
  })

  it('tenant_id 被转义后发出', () => {
    expect(trendQuery({ tenant_id: 'a b' })).toBe('?days=1&tenant_id=a%20b')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ①② trend-series 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('trend-series 解包', () => {
  it('成功载荷原样通过', () => {
    const r = unwrapTrendSeries(seriesOk())
    expect(r.degraded).toBe(false)
    expect(r.series).toHaveLength(1)
  })

  it('degraded 缺键时抛错', () => {
    // ★ 恒发字段（:77）⇒ 缺键是契约破坏，不能当 false
    const d = seriesOk()
    delete (d as Record<string, unknown>).degraded
    expect(() => unwrapTrendSeries(d)).toThrow(/degraded/)
  })

  it('degraded 不是布尔值时抛错', () => {
    expect(() => unwrapTrendSeries(seriesOk({ degraded: 'yes' as unknown as boolean }))).toThrow(
      /degraded/,
    )
  })

  it('degraded 为 true 时正常通过', () => {
    const r = unwrapTrendSeries(seriesDegraded())
    expect(trendDegraded(r)).toBe(true)
  })

  it('缺少 series 键时抛错', () => {
    const d = seriesOk()
    delete (d as Record<string, unknown>).series
    expect(() => unwrapTrendSeries(d)).toThrow(/series/)
  })

  it('series 不是数组时抛错', () => {
    expect(() => unwrapTrendSeries(seriesOk({ series: null as unknown as [] }))).toThrow(/不是数组/)
  })

  it('空序列数组合法', () => {
    const r = unwrapTrendSeries(seriesOk({ series: [] }))
    expect(r.series).toEqual([])
  })

  it('bucket_minutes 不是 5/15/60 时抛错', () => {
    expect(() => unwrapTrendSeries(seriesOk({ bucket_minutes: 30 }))).toThrow(/bucket_minutes/)
  })

  it('bucket_minutes 三个合法值都通过', () => {
    for (const m of [5, 15, 60]) {
      expect(unwrapTrendSeries(seriesOk({ bucket_minutes: m })).bucket_minutes).toBe(m)
    }
  })

  it('source 未知值时抛错', () => {
    expect(() => unwrapTrendSeries(seriesOk({ source: 'whatever' }))).toThrow(/source/)
  })

  it('序列项缺 points 键时抛错', () => {
    const d = seriesOk()
    const s = (d.series as Record<string, unknown>[])[0]!
    delete s.points
    expect(() => unwrapTrendSeries(d)).toThrow(/points/)
  })

  it('序列项缺 total_cost_usd 时抛错', () => {
    const d = seriesOk()
    const s = (d.series as Record<string, unknown>[])[0]!
    delete s.total_cost_usd
    expect(() => unwrapTrendSeries(d)).toThrow(/total_cost_usd/)
  })

  it('点缺 bucket 键时抛错', () => {
    const d = seriesOk()
    const s = (d.series as Record<string, unknown>[])[0]!
    ;(s.points as Record<string, unknown>[])[0]! = { requests: 1, tokens: 1, credits: 1, cost_usd: 0 }
    expect(() => unwrapTrendSeries(d)).toThrow(/points\[0\]/)
  })

  it('点不是对象时抛错', () => {
    const d = seriesOk()
    const s = (d.series as Record<string, unknown>[])[0]!
    s.points = [1, 2]
    expect(() => unwrapTrendSeries(d)).toThrow(/不是对象/)
  })

  it('序列项不是对象时抛错', () => {
    expect(() => unwrapTrendSeries(seriesOk({ series: ['x'] as unknown as [] }))).toThrow(/不是对象/)
  })

  it('数组形状抛错而不是返回空序列', () => {
    expect(() => unwrapTrendSeries([])).toThrow(/形状不符/)
  })

  it('null 抛错', () => {
    expect(() => unwrapTrendSeries(null)).toThrow(/形状不符/)
  })

  it('dashboardapi 信封形状抛错', () => {
    expect(() => unwrapTrendSeries({ success: true, timestamp: 1 })).toThrow(/信封/)
  })

  it('拿到 trend-models 的载荷形状时反向报错', () => {
    expect(() => unwrapTrendSeries(modelsOk())).toThrow(/trend-models/)
  })
})

describe('trend-models 解包', () => {
  it('成功载荷原样通过', () => {
    const r = unwrapTrendModels(modelsOk())
    expect(r.models).toHaveLength(1)
  })

  it('degraded 缺键时抛错', () => {
    const d = modelsOk()
    delete (d as Record<string, unknown>).degraded
    expect(() => unwrapTrendModels(d)).toThrow(/degraded/)
  })

  it('degraded 不是布尔值时抛错', () => {
    expect(() => unwrapTrendModels(modelsOk({ degraded: 1 as unknown as boolean }))).toThrow(
      /degraded/,
    )
  })

  it('降级载荷三个标记键齐全时通过', () => {
    const r = unwrapTrendModels(modelsDegraded())
    expect(trendDegraded(r)).toBe(true)
    expect(trendMissingView(r)).toBe('request_stats_dim_minute')
  })

  it('缺少 models 键时抛错', () => {
    const d = modelsOk()
    delete (d as Record<string, unknown>).models
    expect(() => unwrapTrendModels(d)).toThrow(/models/)
  })

  it('空模型数组合法', () => {
    expect(unwrapTrendModels(modelsOk({ models: [] })).models).toEqual([])
  })

  it('模型项缺 credits 时抛错', () => {
    const d = modelsOk()
    const m = (d.models as Record<string, unknown>[])[0]!
    delete m.credits
    expect(() => unwrapTrendModels(d)).toThrow(/credits/)
  })

  it('source 未知值时抛错', () => {
    expect(() => unwrapTrendModels(modelsOk({ source: 'nope' }))).toThrow(/source/)
  })

  it('models 不是数组时抛错', () => {
    expect(() => unwrapTrendModels(modelsOk({ models: {} as unknown as [] }))).toThrow(/不是数组/)
  })

  it('拿到 trend-series 的载荷形状时反向报错', () => {
    expect(() => unwrapTrendModels(seriesOk())).toThrow(/trend-series/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * fetch
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('fetch 打到正确 URL', () => {
  it('trend-series 缺省打到 days=1', async () => {
    ok(seriesOk())
    await fetchTrendSeries()
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/usage/trend-series?days=1')
  })

  it('trend-series 带过滤时 URL 含 provider_id', async () => {
    ok(seriesOk())
    await fetchTrendSeries({ days: 7, provider_id: 5 })
    expect(reqMock.mock.calls[0]![1]).toBe(
      '/api/admin/usage/trend-series?days=7&provider_id=5',
    )
  })

  it('trend-models 缺省打到 days=1', async () => {
    ok(modelsOk())
    await fetchTrendModels()
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/usage/trend-models?days=1')
  })

  it('trend-models 带 model 多选时用重复参数', async () => {
    ok(modelsOk())
    await fetchTrendModels({ model: ['a', 'b'] })
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/usage/trend-models?days=1&model=a&model=b')
  })

  it('fetch 会过解包器', async () => {
    const d = seriesOk()
    delete (d as Record<string, unknown>).degraded
    ok(d)
    await expect(fetchTrendSeries()).rejects.toThrow(/degraded/)
  })

  it('models 的 fetch 也会过解包器', async () => {
    const d = modelsOk()
    delete (d as Record<string, unknown>).degraded
    ok(d)
    await expect(fetchTrendModels()).rejects.toThrow(/degraded/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ①② 降级语义：零用量 vs 没算出来
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('零用量与降级的区分', () => {
  it('非降级且空序列判定为真的零用量', () => {
    const r = unwrapTrendSeries(seriesOk({ series: [] }))
    expect(trendSeriesIsGenuinelyEmpty(r)).toBe(true)
  })

  it('降级且空序列判定为真的不是零用量', () => {
    // ★★★ 唯一区分手段就是 degraded 那个键
    const r = unwrapTrendSeries(seriesDegraded())
    expect(trendSeriesIsGenuinelyEmpty(r)).toBe(false)
  })

  it('降级时即使有序列也不算真的零用量', () => {
    // 走一遍解包拿到已定型的序列，避免给夹具塞 unknown
    const real = unwrapTrendSeries(seriesOk()).series
    const r = unwrapTrendSeries(seriesDegraded({ series: real }))
    expect(trendSeriesIsGenuinelyEmpty(r)).toBe(false)
    expect(r.series).toHaveLength(1)
  })

  it('非降级且有序列不算零用量', () => {
    const r = unwrapTrendSeries(seriesOk())
    expect(trendSeriesIsGenuinelyEmpty(r)).toBe(false)
  })

  it('降级时 top 仍是 clamp 后的解析值', () => {
    // :205 降级分支照样填 `Top: f.top`
    const r = unwrapTrendSeries(seriesDegraded({ top: 20 }))
    expect(trendTopEchoed(r)).toBe(20)
  })

  it('降级时 bucket_minutes 仍是解析值', () => {
    // :204 降级分支照样填 `BucketMinutes: tr.trendBucketMinutes()`
    const r = unwrapTrendSeries(seriesDegraded({ bucket_minutes: 60 }))
    expect(trendBucketMinutesKnown(r)).toBe(true)
    expect(r.bucket_minutes).toBe(60)
  })

  it('degraded_reason 与 missing_view 同值', () => {
    // :209-210 两个键写的是同一个 view
    const r = unwrapTrendSeries(seriesDegraded())
    expect(trendDegradedReason(r)).toBe('request_stats_dim_minute')
    expect(trendMissingView(r)).toBe(trendDegradedReason(r))
  })

  it('models 的 degraded_reason 与 missing_view 同值', () => {
    const r = unwrapTrendModels(modelsDegraded())
    expect(trendDegradedReason(r)).toBe(trendMissingView(r))
  })

  it('未降级时 reason 与 view 都是 null', () => {
    const r = unwrapTrendSeries(seriesOk())
    expect(trendDegradedReason(r)).toBeNull()
    expect(trendMissingView(r)).toBeNull()
  })

  it('degraded 非 true 时判为未降级', () => {
    expect(trendDegraded({ degraded: false })).toBe(false)
    expect(trendDegraded({})).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑧ 折叠判定
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('折叠判定', () => {
  const withOthers = seriesOk({
    series: [
      ser('gpt-4o', 30, 3000, 12, 1.5, [pt('2026-10-07T00:00:00Z', 30, 3000, 12, 1.5)]),
      ser(TREND_OTHERS_KEY, 8, 800, 3, 0.2, [pt('2026-10-07T00:00:00Z', 8, 800, 3, 0.2)]),
    ],
  })

  /** __others__ 的总量**大于**各条真实模型线（后端仍强制把它排最后）。 */
  const withOthersBig = seriesOk({
    series: [
      ser('gpt-4o', 30, 3000, 12, 1.5, [pt('2026-10-07T00:00:00Z', 30, 3000, 12, 1.5)]),
      ser('claude', 20, 2000, 8, 1.0, [pt('2026-10-07T00:00:00Z', 20, 2000, 8, 1.0)]),
      ser(TREND_OTHERS_KEY, 999, 9000, 40, 9.9, [pt('2026-10-07T00:00:00Z', 999, 9000, 40, 9.9)]),
    ],
  })

  it('出现 __others__ 且未指定 model 时判定为已折叠', () => {
    const r = unwrapTrendSeries(withOthers)
    expect(trendWasFolded(r, undefined)).toBe(true)
  })

  it('没有 __others__ 时判定为未折叠', () => {
    // ★ 负控：模型数 <= top 时后端透传不折叠（:455-457）
    const r = unwrapTrendSeries(seriesOk())
    expect(r.series.some((s) => s.model === TREND_OTHERS_KEY)).toBe(false)
    expect(trendWasFolded(r, undefined)).toBe(false)
  })

  it('指定了 model 时即便有 __others__ 也不判为折叠', () => {
    // :439 modelFiltered 直接透传
    const r = unwrapTrendSeries(withOthers)
    expect(trendWasFolded(r, ['a'])).toBe(false)
  })

  it('指定 model 会抑制折叠', () => {
    expect(trendFoldSuppressedByModelSelection(['a'])).toBe(true)
    expect(trendFoldSuppressedByModelSelection(undefined)).toBe(false)
    expect(trendFoldSuppressedByModelSelection([])).toBe(false)
  })

  it('折叠线可被取出', () => {
    const r = unwrapTrendSeries(withOthers)
    expect(trendOthersLine(r)?.total_requests).toBe(8)
  })

  it('没有折叠线时返回 null', () => {
    const r = unwrapTrendSeries(seriesOk())
    expect(trendOthersLine(r)).toBeNull()
  })

  it('__others__ 排在最后时判定成立', () => {
    // :221-229 `__others__` 固定排最后
    const r = unwrapTrendSeries(withOthers)
    expect(r.series[r.series.length - 1]!.model).toBe(TREND_OTHERS_KEY)
    expect(trendOthersIsLast(r)).toBe(true)
  })

  it('__others__ 排在中间时判定不成立', () => {
    const d = seriesOk()
    d.series = [
      ser('gpt-4o', 30, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 30, 1, 1, 1)]),
      ser(TREND_OTHERS_KEY, 8, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 8, 1, 1, 1)]),
      ser('claude', 20, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 20, 1, 1, 1)]),
    ]
    const r = unwrapTrendSeries(d)
    expect(trendOthersIsLast(r)).toBe(false)
  })

  it('请求数非递增时次序判定成立', () => {
    const d = seriesOk()
    d.series = [
      ser('a', 30, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 30, 1, 1, 1)]),
      ser('b', 20, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 20, 1, 1, 1)]),
      ser('c', 20, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 20, 1, 1, 1)]),
    ]
    expect(trendSeriesNonIncreasing(unwrapTrendSeries(d))).toBe(true)
  })

  it('★ 请求数出现回升时次序判定不成立', () => {
    // ★ 负控：夹具乱序 ⇒ 恒 true 的实现会被这条抓住
    const d = seriesOk()
    d.series = [
      ser('small', 5, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 5, 1, 1, 1)]),
      ser('big', 30, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 30, 1, 1, 1)]),
    ]
    expect(trendSeriesNonIncreasing(unwrapTrendSeries(d))).toBe(false)
  })

  it('请求数回升发生在序列中段时判定不成立', () => {
    // ★ 夹具只在第 2→3 位回升 ⇒ 逐位比较才抓得住
    const d = seriesOk()
    d.series = [
      ser('a', 30, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 30, 1, 1, 1)]),
      ser('b', 20, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 20, 1, 1, 1)]),
      ser('c', 25, 1, 1, 1, [pt('2026-10-07T00:00:00Z', 25, 1, 1, 1)]),
    ]
    expect(trendSeriesNonIncreasing(unwrapTrendSeries(d))).toBe(false)
  })

  it('__others__ 不参与非递增比较', () => {
    // ★ __others__ 强制排最后（:222-227），哪怕它的总量比谁都大
    const r = unwrapTrendSeries(withOthersBig)
    expect(r.series[r.series.length - 1]!.model).toBe(TREND_OTHERS_KEY)
    expect(trendSeriesNonIncreasing(r)).toBe(true)
  })

  it('单条序列恒满足非递增', () => {
    expect(trendSeriesNonIncreasing(unwrapTrendSeries(seriesOk()))).toBe(true)
  })

  it('空序列满足非递增', () => {
    expect(trendSeriesNonIncreasing(unwrapTrendSeries(seriesOk({ series: [] })))).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑨ trend-models 静默截断与被 model 收敛
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('trend-models 上限与收敛', () => {
  it('满 100 条时提示可能截断', () => {
    const models = Array.from({ length: 100 }, (_, i) =>
      entry(`m${String(i).padStart(3, '0')}`, 1000 - i, 1, 1, 0.1),
    )
    const r = unwrapTrendModels(modelsOk({ models }))
    expect(trendModelsPossiblyTruncated(r)).toBe(true)
  })

  it('99 条时判定未触上限', () => {
    // ★ 负控：夹具硬写 99（后端 LIMIT 100 的字面量）
    const models = Array.from({ length: 99 }, (_, i) =>
      entry(`m${String(i).padStart(3, '0')}`, 1000 - i, 1, 1, 0.1),
    )
    const r = unwrapTrendModels(modelsOk({ models }))
    expect(r.models).toHaveLength(99)
    expect(trendModelsPossiblyTruncated(r)).toBe(false)
  })

  it('空列表判定未触上限', () => {
    expect(trendModelsPossiblyTruncated(unwrapTrendModels(modelsOk({ models: [] })))).toBe(false)
  })

  it('未指定 model 时列表未被收敛', () => {
    const r = unwrapTrendModels(modelsOk())
    expect(trendModelsListIsConstrained(r, undefined)).toBe(false)
  })

  it('指定 model 时列表已被收敛到选中集合', () => {
    // :579-581 `= ANY($n)` 把结果集收窄
    const r = unwrapTrendModels(modelsOk())
    expect(trendModelsListIsConstrained(r, ['gpt-4o'])).toBe(true)
  })

  it('降级时列表不算被收敛', () => {
    const r = unwrapTrendModels(modelsDegraded())
    expect(trendModelsListIsConstrained(r, ['gpt-4o'])).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * hint 本地推导
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('hint 本地推导', () => {
  it('view 为空时是固定文案', () => {
    // dashboard_degrade.go:84
    expect(trendHintLocal(null)).toBe('数据视图尚未初始化，请先执行数据聚合迁移')
  })

  it('view 非空时带上视图名', () => {
    // dashboard_degrade.go:86
    expect(trendHintLocal('request_stats_dim_minute')).toBe(
      '数据视图 request_stats_dim_minute 尚未初始化，请先执行数据聚合迁移',
    )
  })

  it('后端 hint 与本地推导一致时不报警', () => {
    const r = unwrapTrendSeries(seriesDegraded())
    expect(trendHintDisagrees(r, trendMissingView(r))).toBe(false)
  })

  it('后端 hint 与本地推导不一致时报警', () => {
    const d = seriesDegraded({ hint: '别的说法' })
    const r = unwrapTrendSeries(d)
    expect(trendHintDisagrees(r, trendMissingView(r))).toBe(true)
  })

  it('未降级时没有 hint 不报警', () => {
    const r = unwrapTrendSeries(seriesOk())
    expect(trendHintDisagrees(r, null)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ④ end 字段形态
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('end 字段形态区分两条路径', () => {
  it('次日零点形态判为自定义区间', () => {
    expect(trendEndIsNextMidnight('2026-01-08T00:00:00Z')).toBe(true)
  })

  it('带时分秒形态判为预设区间', () => {
    expect(trendEndIsNextMidnight('2026-10-08T12:34:56Z')).toBe(false)
  })

  it('小时为零但分钟非零时不是次日零点形态', () => {
    // ★ 夹具只差「分钟」一个条件 ⇒ 能区分逐项检查是否都在
    expect(trendEndIsNextMidnight('2026-01-08T00:30:00Z')).toBe(false)
  })

  it('秒与毫秒非零时不是次日零点形态', () => {
    // ★ 夹具只差「秒」一个条件
    expect(trendEndIsNextMidnight('2026-01-08T00:00:30Z')).toBe(false)
  })

  it('非法时间戳判为非零点形态', () => {
    expect(trendEndIsNextMidnight('not-a-date')).toBe(false)
  })

  it('接近当前时刻判为预设路径', () => {
    const now = Date.parse('2026-10-08T12:34:56Z')
    expect(trendEndLooksLikeNow('2026-10-08T12:34:56Z', now)).toBe(true)
  })

  it('远离当前时刻不判为预设路径', () => {
    const now = Date.parse('2026-10-08T12:34:56Z')
    expect(trendEndLooksLikeNow('2026-01-08T00:00:00Z', now)).toBe(false)
  })

  it('★ 相距一小时不判为预设路径', () => {
    // ★ 夹具只差「容差」一个条件 ⇒ 容差被放大到 1 天时这条会红
    const now = Date.parse('2026-10-08T12:34:56Z')
    expect(trendEndLooksLikeNow('2026-10-08T13:34:56Z', now)).toBe(false)
  })

  it('相距一分钟判为预设路径', () => {
    // ★ 负控：容差内确实要判为预设路径（2 分钟容差是响应往返所需）
    const now = Date.parse('2026-10-08T12:34:56Z')
    expect(trendEndLooksLikeNow('2026-10-08T12:35:56Z', now)).toBe(true)
  })

  it('所选结束日比响应 end 早一天', () => {
    expect(trendSelectedEndDay('2026-01-08T00:00:00Z')?.toISOString()).toBe(
      '2026-01-07T00:00:00.000Z',
    )
  })

  it('非法时间戳时返回 null', () => {
    expect(trendSelectedEndDay('bad')).toBeNull()
  })
})
