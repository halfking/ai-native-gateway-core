import { describe, it, expect, vi, beforeEach } from 'vitest'
import * as api from './errorsTrend'
import type { ErrorsTrendResponse } from './errorsTrend'

vi.mock('./client', () => ({ req: vi.fn(), ApiError: class extends Error {} }))

import { req } from './client'
const rq = vi.mocked(req)

function ok(payload: unknown): void {
  rq.mockResolvedValueOnce(payload)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 夹具：逐字照抄 errors_trend.go 的赋值
 * ═══════════════════════════════════════════════════════════════════════════ */

/** errorsTrendPoint（`:37-44`），带两个非空 map。 */
function point(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    timestamp: '2026-10-07T09:00:00Z',
    error_count: 12,
    unique_requests: 9,
    affected_users: 4,
    by_supplier: { openrouter: 8, deepseek: 4 },
    by_error_type: { upstream_5xx: 12 },
    ...over,
  }
}

/** 聚合后的 map 被 FILTER 全滤掉 ⇒ 扫描得空字节 ⇒ map 为 nil ⇒ 键被省略。 */
function pointNoDims(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    timestamp: '2026-10-07T09:00:00Z',
    error_count: 3,
    unique_requests: 2,
    affected_users: 1,
    ...over,
  }
}

function row(over: Record<string, unknown> = {}): Record<string, unknown> {
  return { key: 'upstream_5xx', count: 12, ...over }
}

/** errorsTrendResponse（`:59-67`）的完整形态。 */
function trend(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    source: 'stats',
    granularity: 'hour',
    hours: 24,
    since: '2026-10-06T09:00:00Z',
    until: '2026-10-07T09:00:00Z',
    time_series: [point()],
    summary: {
      total_errors: 12,
      unique_requests: 9,
      top_error_types: [row()],
      top_suppliers: [row({ key: 'openrouter', count: 8 })],
      affected_credentials: 3,
    },
    ...over,
  }
}

function resp(over: Record<string, unknown> = {}): ErrorsTrendResponse {
  return api.unwrapErrorsTrend(trend(over))
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('★ hours 是严格三值、缺省 24', () => {
    expect([...api.ERRORS_TREND_HOURS]).toEqual([1, 24, 168])
    expect(api.ERRORS_TREND_DEFAULT_HOURS).toBe(24)
  })

  it('granularity 是三值', () => {
    expect([...api.ERRORS_TREND_GRANULARITIES]).toEqual(['minute', 'hour', 'day'])
  })

  it('★ source 是两值', () => {
    expect([...api.ERRORS_TREND_SOURCES]).toEqual(['stats', 'fallback'])
  })

  it('★ 通配值是空串与 all 两个', () => {
    expect([...api.ERRORS_TREND_FILTER_WILDCARDS]).toEqual(['', 'all'])
  })

  it('breakdown 截断上限是 10', () => {
    // loadBreakdowns:319/:323
    expect(api.ERRORS_TREND_BREAKDOWN_LIMIT).toBe(10)
  })

  it('查询超时 8 秒', () => {
    // :105
    expect(api.ERRORS_TREND_QUERY_TIMEOUT_MS).toBe(8000)
  })

  it('★ 六个 error code', () => {
    expect([...api.ERRORS_TREND_CODES]).toEqual([
      'db_not_configured',
      'invalid_hours',
      'invalid_granularity',
      'invalid_credential_id',
      'trend_stats_query_failed',
      'trend_fallback_query_failed',
    ])
  })

  it('★ 503 文案与其他端点措辞不同', () => {
    // :71 是 "database is not configured"（别处是 "database not configured"）
    expect(api.ERRORS_TREND_DB_NOT_CONFIGURED_MESSAGE).toBe('database is not configured')
    expect(api.ERRORS_TREND_DB_NOT_CONFIGURED_MESSAGE).not.toBe('database not configured')
  })

  it('三条 400 文案逐字对齐', () => {
    expect(api.ERRORS_TREND_BAD_HOURS_MESSAGE).toBe('hours must be 1, 24, or 168')
    expect(api.ERRORS_TREND_BAD_GRANULARITY_MESSAGE).toBe(
      'granularity must be minute, hour, or day',
    )
    expect(api.ERRORS_TREND_BAD_CREDENTIAL_ID_MESSAGE).toBe(
      'credential_id must be a positive integer',
    )
  })

  it('★ 两个 500 的 detail 原文相同（只有 code 不同）', () => {
    expect(api.ERRORS_TREND_STATS_FAILED_MESSAGE).toBe('failed to load error trend')
    expect(api.ERRORS_TREND_FALLBACK_FAILED_MESSAGE).toBe('failed to load error trend')
  })

  it('顶层七键、summary 五键、点四恒在两条件', () => {
    expect([...api.ERRORS_TREND_KEYS]).toEqual([
      'source', 'granularity', 'hours', 'since', 'until', 'time_series', 'summary',
    ])
    expect(api.ERRORS_TREND_SUMMARY_KEYS).toHaveLength(5)
    expect(api.ERRORS_TREND_POINT_ALWAYS_KEYS).toHaveLength(4)
    expect(api.ERRORS_TREND_POINT_OPTIONAL_KEYS).toEqual(['by_supplier', 'by_error_type'])
  })

  it('恒在键与条件键无交集', () => {
    const a = new Set<string>(api.ERRORS_TREND_POINT_ALWAYS_KEYS)
    expect(api.ERRORS_TREND_POINT_OPTIONAL_KEYS.filter((k) => a.has(k))).toEqual([])
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('解包 · 错误趋势', () => {
  it('健康载荷原样通过', () => {
    const r = api.unwrapErrorsTrend(trend())
    expect(r.time_series).toHaveLength(1)
    expect(r.summary.total_errors).toBe(12)
  })

  it('★ time_series 空数组不是 null', () => {
    const r = api.unwrapErrorsTrend(trend({ time_series: [] }))
    expect(r.time_series).toEqual([])
  })

  it('★ breakdown 空数组不是 null', () => {
    const r = api.unwrapErrorsTrend(
      trend({
        summary: {
          total_errors: 0, unique_requests: 0, top_error_types: [], top_suppliers: [],
          affected_credentials: 0,
        },
      }),
    )
    expect(r.summary.top_error_types).toEqual([])
  })

  it('顶层缺键抛错', () => {
    const t = trend()
    delete (t as Record<string, unknown>).since
    expect(() => api.unwrapErrorsTrend(t)).toThrow(/缺 1 个键（since）/)
  })

  it('★ source 是未知值时抛错', () => {
    expect(() => api.unwrapErrorsTrend(trend({ source: 'unified' }))).toThrow(
      /source 不是已知来源（unified）/,
    )
  })

  it('granularity 是未知值时抛错', () => {
    expect(() => api.unwrapErrorsTrend(trend({ granularity: 'week' }))).toThrow(
      /granularity 不是已知粒度（week）/,
    )
  })

  it('hours 不是数字时抛错', () => {
    expect(() => api.unwrapErrorsTrend(trend({ hours: '24' }))).toThrow(/hours 不是数字/)
  })

  it('since 不是字符串时抛错', () => {
    expect(() => api.unwrapErrorsTrend(trend({ since: 0 }))).toThrow(/since 不是字符串/)
  })

  it('time_series 不是数组时抛错', () => {
    expect(() => api.unwrapErrorsTrend(trend({ time_series: null }))).toThrow(
      /time_series 不是数组/,
    )
  })

  it('响应形状不是裸对象时抛错', () => {
    expect(() => api.unwrapErrorsTrend(null)).toThrow(/期望裸对象，实得 null/)
    expect(() => api.unwrapErrorsTrend([])).toThrow(/期望裸对象，实得 array/)
    expect(() => api.unwrapErrorsTrend(undefined)).toThrow(/期望裸对象，实得 undefined/)
  })

  it('★ 点缺恒在键时抛错', () => {
    expect(() =>
      api.unwrapErrorsTrend(trend({ time_series: [{ timestamp: 'x' }] })),
    ).toThrow(/time_series\[0\] 缺 3 个键/)
  })

  it('点的 error_count 不是数字时抛错', () => {
    expect(() =>
      api.unwrapErrorsTrend(trend({ time_series: [point({ error_count: '12' })] })),
    ).toThrow(/time_series\[0\] 的 error_count 不是数字/)
  })

  it('★ 点的 timestamp 不是字符串时抛错', () => {
    expect(() =>
      api.unwrapErrorsTrend(trend({ time_series: [point({ timestamp: 1 })] })),
    ).toThrow(/time_series\[0\] 的 timestamp 不是字符串/)
  })

  it('★ by_supplier 是数组时抛错', () => {
    expect(() =>
      api.unwrapErrorsTrend(trend({ time_series: [point({ by_supplier: [] })] })),
    ).toThrow(/time_series\[0\] 的 by_supplier 不是对象/)
  })

  it('★ by_supplier 的值不是数字时抛错并点名该供应商', () => {
    expect(() =>
      api.unwrapErrorsTrend(trend({ time_series: [point({ by_supplier: { or: '8' } })] })),
    ).toThrow(/time_series\[0\] 的 by_supplier\.or 不是数字/)
  })

  it('★ by_supplier 为空对象时通过（空 map 也可能是显式空）', () => {
    expect(() =>
      api.unwrapErrorsTrend(trend({ time_series: [point({ by_supplier: {} })] })),
    ).not.toThrow()
  })

  it('summary 形状不对时点名 summary', () => {
    expect(() => api.unwrapErrorsTrend(trend({ summary: 'none' }))).toThrow(
      /错误趋势 的 summary 响应形状不符/,
    )
  })

  it('summary 缺键抛错', () => {
    const t = trend()
    ;(t.summary as Record<string, unknown>).affected_credentials = undefined
    delete (t.summary as Record<string, unknown>).affected_credentials
    expect(() => api.unwrapErrorsTrend(t)).toThrow(
      /summary 缺 1 个键（affected_credentials）/,
    )
  })

  it('summary 的 total_errors 不是数字时抛错', () => {
    const t = trend()
    ;(t.summary as Record<string, unknown>).total_errors = '12'
    expect(() => api.unwrapErrorsTrend(t)).toThrow(/summary 的 total_errors 不是数字/)
  })

  it('top_suppliers 不是数组时抛错', () => {
    const t = trend()
    ;(t.summary as Record<string, unknown>).top_suppliers = null
    expect(() => api.unwrapErrorsTrend(t)).toThrow(/summary 的 top_suppliers 不是数组/)
  })

  it('★ breakdown 元素缺 count 时抛错并点名下标', () => {
    const t = trend()
    ;(t.summary as Record<string, unknown>).top_error_types = [{ key: 'x' }]
    expect(() => api.unwrapErrorsTrend(t)).toThrow(/top_error_types\[0\] 缺 1 个键（count）/)
  })

  it('breakdown 的 key 不是字符串时抛错', () => {
    const t = trend()
    ;(t.summary as Record<string, unknown>).top_error_types = [row({ key: 7 })]
    expect(() => api.unwrapErrorsTrend(t)).toThrow(/top_error_types\[0\].key 不是字符串/)
  })

  it('breakdown 的 count 不是数字时抛错', () => {
    const t = trend()
    ;(t.summary as Record<string, unknown>).top_suppliers = [row({ count: '8' })]
    expect(() => api.unwrapErrorsTrend(t)).toThrow(/top_suppliers\[0\].count 不是数字/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定 · 粒度缺省推导
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('语义判定 · granularity 缺省由 hours 推导', () => {
  it('★ hours=1 ⇒ minute', () => {
    expect(api.errorsTrendDefaultGranularity(1)).toBe('minute')
  })

  it('★ hours=24 ⇒ hour', () => {
    expect(api.errorsTrendDefaultGranularity(24)).toBe('hour')
  })

  it('★ hours=168 ⇒ day', () => {
    expect(api.errorsTrendDefaultGranularity(168)).toBe('day')
  })

  it('★ hours=0 也落 minute（判据是 <=1 而不是 ===1）', () => {
    expect(api.errorsTrendDefaultGranularity(0)).toBe('minute')
  })

  it('★ hours=25 落 day（判据是 >24）', () => {
    expect(api.errorsTrendDefaultGranularity(25)).toBe('day')
  })

  it('★ 显式传了 granularity 时不算「符合缺省」', () => {
    const r = resp({ hours: 24, granularity: 'day' })
    expect(api.errorsTrendGranularityIsDefault(r, true)).toBe(false)
  })

  it('★ 没传且回显符合推导时成立', () => {
    const r = resp({ hours: 168, granularity: 'day' })
    expect(api.errorsTrendGranularityIsDefault(r, false)).toBe(true)
  })

  it('★★★ 显式传了且恰好等于缺省时也不成立', () => {
    // ★ 只测「显式传了且与缺省不同」打不出差异：
    //   去掉 `!granularityWasSent` 后，两种情况都返回 false。
    //   必须有一条「显式传了、但值正好等于推导值」的用例。
    const r = resp({ hours: 24, granularity: 'hour' })
    expect(api.errorsTrendGranularityIsDefault(r, true)).toBe(false)
  })

  it('★ 没传但回显不符推导时（契约漂移）不成立', () => {
    const r = resp({ hours: 168, granularity: 'hour' })
    expect(api.errorsTrendGranularityIsDefault(r, false)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定 · source 与空窗口
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('语义判定 · source 与空窗口', () => {
  const emptySummary = {
    total_errors: 0, unique_requests: 0, top_error_types: [], top_suppliers: [],
    affected_credentials: 0,
  }

  it('★ stats 源不判预聚合缺失', () => {
    expect(api.errorsTrendPreAggregationMissed(resp())).toBe(false)
  })

  it('★ fallback 源判预聚合缺失（可能是粒度不匹配）', () => {
    const r = resp({ source: 'fallback' })
    expect(api.errorsTrendPreAggregationMissed(r)).toBe(true)
  })

  it('stats 源判为来自聚合器', () => {
    expect(api.errorsTrendSourceIsAggregated(resp())).toBe(true)
  })

  it('★ fallback + 空序列 ⇒ 窗口确实没有错误（确定的）', () => {
    const r = resp({ source: 'fallback', time_series: [], summary: emptySummary })
    expect(api.errorsTrendWindowIsGenuinelyEmpty(r)).toBe(true)
  })

  it('★ fallback + 有序列 ⇒ 不是「空窗口」', () => {
    const r = resp({ source: 'fallback' })
    expect(api.errorsTrendWindowIsGenuinelyEmpty(r)).toBe(false)
  })

  it('★ stats + 空序列不是「空窗口」（那是聚合器没写）', () => {
    const r = resp({ source: 'stats', time_series: [], summary: emptySummary })
    expect(api.errorsTrendWindowIsGenuinelyEmpty(r)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定 · 汇总口径
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('语义判定 · 汇总与桶求和', () => {
  const twoPoints = {
    time_series: [
      point({ error_count: 10, unique_requests: 4 }),
      point({ timestamp: '2026-10-07T10:00:00Z', error_count: 5, unique_requests: 3 }),
    ],
    summary: {
      total_errors: 15, unique_requests: 7, top_error_types: [], top_suppliers: [],
      affected_credentials: 2,
    },
  }

  it('★ 汇总与序列求和自洽', () => {
    expect(api.errorsTrendSummaryMatchesSeries(resp(twoPoints))).toBe(true)
  })

  it('★ 汇总与序列求和不自洽时判为漂移', () => {
    const r = resp({
      time_series: [point({ error_count: 10 })],
      summary: {
        total_errors: 99, unique_requests: 9, top_error_types: [], top_suppliers: [],
        affected_credentials: 1,
      },
    })
    expect(api.errorsTrendSummaryMatchesSeries(r)).toBe(false)
  })

  it('★ unique_requests 是桶求和 ⇒ 恒不小于去重真值（判为上界）', () => {
    expect(api.errorsTrendUniqueRequestsIsUpperBound(resp(twoPoints))).toBe(true)
  })

  it('汇总小于桶求和时说明有漂移', () => {
    const r = resp({
      time_series: [point({ unique_requests: 9 })],
      summary: {
        total_errors: 12, unique_requests: 1, top_error_types: [], top_suppliers: [],
        affected_credentials: 1,
      },
    })
    expect(api.errorsTrendUniqueRequestsIsUpperBound(r)).toBe(false)
  })

  it('★ 序列按时间升序', () => {
    expect(api.errorsTrendSeriesIsAscending(resp(twoPoints))).toBe(true)
  })

  it('序列乱序时判为不合规', () => {
    const r = resp({
      time_series: [
        point({ timestamp: '2026-10-07T10:00:00Z' }),
        point({ timestamp: '2026-10-07T09:00:00Z' }),
      ],
    })
    expect(api.errorsTrendSeriesIsAscending(r)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定 · 截断与维度
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('语义判定 · breakdown 截断与维度缺失', () => {
  const ten = Array.from({ length: 10 }, (_, i) => row({ key: `k${i}`, count: i }))

  it('★ 满 10 条时判为可能被截断', () => {
    const r = resp({
      summary: {
        total_errors: 12, unique_requests: 9, top_error_types: ten, top_suppliers: ten,
        affected_credentials: 3,
      },
    })
    expect(api.errorsTrendBreakdownIsTruncated(r)).toBe(true)
  })

  it('不到 10 条时不判截断', () => {
    expect(api.errorsTrendBreakdownIsTruncated(resp())).toBe(false)
  })

  it('★★★ 六条时不判截断（5..9 这一段也要有专属用例）', () => {
    // ★ 只测 1 条与 10 条时，把阈值从 10 改成 5 打不出差异。
    const six = Array.from({ length: 6 }, (_, i) => row({ key: `k${i}`, count: i }))
    const r = resp({
      summary: {
        total_errors: 12, unique_requests: 9, top_error_types: six, top_suppliers: six,
        affected_credentials: 3,
      },
    })
    expect(api.errorsTrendBreakdownIsTruncated(r)).toBe(false)
  })

  it('★ 维度键缺失时判为无供应商维', () => {
    const p = api.unwrapErrorsTrend(trend({ time_series: [pointNoDims()] })).time_series[0]!
    expect(api.errorsTrendPointHasSupplierDim(p)).toBe(false)
    expect(api.errorsTrendPointHasErrorTypeDim(p)).toBe(false)
  })

  it('★ 维度键在时判为有该维', () => {
    const p = api.unwrapErrorsTrend(trend()).time_series[0]!
    expect(api.errorsTrendPointHasSupplierDim(p)).toBe(true)
    expect(api.errorsTrendPointHasErrorTypeDim(p)).toBe(true)
  })

  it('★ 维度键在但为空对象时判为无该维', () => {
    const p = api.unwrapErrorsTrend(
      trend({ time_series: [pointNoDims({ by_supplier: {}, by_error_type: {} })] }),
    ).time_series[0]!
    expect(api.errorsTrendPointHasSupplierDim(p)).toBe(false)
  })

  it('★ 空串是通配值', () => {
    expect(api.errorsTrendFilterIsWildcard('')).toBe(true)
  })

  it('★ all 是通配值', () => {
    expect(api.errorsTrendFilterIsWildcard('all')).toBe(true)
  })

  it('具体供应商不是通配值', () => {
    expect(api.errorsTrendFilterIsWildcard('openrouter')).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('取数', () => {
  beforeEach(() => {
    rq.mockReset()
  })

  it('★ 不传参数时打裸路径（后端补 hours=24、granularity=hour）', async () => {
    ok(trend())
    await api.fetchErrorsTrend()
    expect(rq.mock.calls[0]![0]).toBe('GET')
    expect(rq.mock.calls[0]![1]).toBe('/api/errors/trend')
  })

  it('★ 五个过滤参数按序拼进 query', async () => {
    ok(trend())
    await api.fetchErrorsTrend({
      hours: 168,
      granularity: 'day',
      supplier: 'openrouter',
      credentialId: 7,
      errorType: 'upstream_5xx',
    })
    expect(rq.mock.calls[0]![1]).toBe(
      '/api/errors/trend?hours=168&granularity=day&supplier=openrouter&credential_id=7&error_type=upstream_5xx',
    )
  })

  it('★ 空串 supplier 也要发（后端把它当通配）', async () => {
    ok(trend())
    await api.fetchErrorsTrend({ supplier: '' })
    expect(rq.mock.calls[0]![1]).toBe('/api/errors/trend?supplier=')
  })

  it('★ all 作为 supplier 照发', async () => {
    ok(trend())
    await api.fetchErrorsTrend({ supplier: 'all' })
    expect(rq.mock.calls[0]![1]).toBe('/api/errors/trend?supplier=all')
  })

  it('★ credentialId 为 0 也要发（后端会 400）', async () => {
    ok(trend())
    await api.fetchErrorsTrend({ credentialId: 0 })
    expect(rq.mock.calls[0]![1]).toBe('/api/errors/trend?credential_id=0')
  })

  it('★ 只传 hours 时只带 hours', async () => {
    ok(trend({ hours: 1, granularity: 'minute' }))
    await api.fetchErrorsTrend({ hours: 1 })
    expect(rq.mock.calls[0]![1]).toBe('/api/errors/trend?hours=1')
  })

  it('★ 会拒绝未知 source', async () => {
    ok(trend({ source: 'nope' }))
    await expect(api.fetchErrorsTrend()).rejects.toThrow(/source 不是已知来源/)
  })

  it('★ 会拒绝缺 since 的载荷', async () => {
    const t = trend()
    delete t.since
    ok(t)
    await expect(api.fetchErrorsTrend()).rejects.toThrow(/缺 1 个键（since）/)
  })

  it('★ 会拒绝 map 元素类型错', async () => {
    ok(trend({ time_series: [point({ by_error_type: { x: '1' } })] }))
    await expect(api.fetchErrorsTrend()).rejects.toThrow(/by_error_type\.x 不是数字/)
  })
})