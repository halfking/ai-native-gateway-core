import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchAvailabilityTimeline,
  fetchCacheState,
  timelineAtCap,
  formatSuccessRatePct,
  avgLatencyOf,
  groupByModel,
  cacheStateAtCap,
  classifyCacheStateError,
  cacheStateTone,
  cacheStateKeyOf,
  cacheStateContradiction,
  AVAILABILITY_TIMELINE_ROW_CAP,
  CACHE_STATE_KEY_CAP,
  type AvailabilityPoint,
  type AvailabilityTimelineResponse,
  type CacheStateEntry,
  type CacheStateResponse,
} from './probeTimelineCache'

/**
 * 时间线 + 缓存快照的契约测试（2026-10-07）。
 *
 * 三条重点：
 * 1. ★★ 两个端点都有**静默截断**且响应里**没有**截断标记
 *    ⇒ 只能靠「撞上写死的上限」推导；
 * 2. ★★★ `success_rate` 在这个视图里**已经乘过 100**（0..100），
 *    而 `probe/dashboard` 的 `avg_success_rate_7d` 是 0..1
 *    ⇒ 同一次会话里两个「成功率」量纲相反；
 * 3. ★ `format=prom` 会把同一个 URL 变成 **text/plain**。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}
function lastUrl(): string {
  return decodeURIComponent(String(fetchMock.mock.calls[0]![0]))
}

function point(over: Partial<AvailabilityPoint> = {}): AvailabilityPoint {
  return {
    raw_model_name: 'gpt-4o',
    outbound_model_name: 'gpt-4o',
    hour_bucket: '2026-10-07T10:00:00Z',
    total_probes: 10,
    successful_probes: 9,
    failed_probes: 1,
    success_rate: 90.0,
    avg_latency_ms: 1200,
    probed_credentials: 3,
    successful_credentials: 2,
    failed_credentials: 1,
    ...over,
  }
}

describe('availability-timeline：model 是精确匹配（与 dashboard 的 ILIKE 不同）', () => {
  it('不发 model ⇒ 完全不发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ timeline: [], total: 0 }))
    await fetchAvailabilityTimeline()
    expect(lastUrl()).not.toContain('model=')
  })

  it('★ 发 model 即照发（后端是 `raw_model_name = $1` 精确匹配）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ timeline: [], total: 0 }))
    await fetchAvailabilityTimeline({ model: 'gpt-4o' })
    expect(lastUrl()).toContain('model=gpt-4o')
  })
})

describe('★★ 判据 1：静默截断只能靠撞上限推导', () => {
  it('total=500 ⇒ 判为撞上限', () => {
    expect(timelineAtCap({ timeline: [], total: AVAILABILITY_TIMELINE_ROW_CAP })).toBe(true)
  })

  it('★ total=499 ⇒ 不判（证明上一条不是恒真）', () => {
    expect(timelineAtCap({ timeline: [], total: 499 })).toBe(false)
  })

  it('total 缺失 ⇒ 不判', () => {
    expect(timelineAtCap({ timeline: [] })).toBe(false)
    expect(timelineAtCap(null)).toBe(false)
  })

  it('★ 上限常量是 500（SQL 里写死的 LIMIT）', () => {
    expect(AVAILABILITY_TIMELINE_ROW_CAP).toBe(500)
  })

  it('★ 响应里**没有**截断标记（所以只能这么推）', () => {
    const resp: AvailabilityTimelineResponse = { timeline: [point()], total: 500 }
    expect((resp as unknown as Record<string, unknown>).truncated).toBeUndefined()
    expect((resp as unknown as Record<string, unknown>).has_more).toBeUndefined()
  })
})

describe('★★★ 判据 2：success_rate 已经是百分数（0..100）', () => {
  // ★ baseline SQL: round(count(ok) * 100.0 / count(*), 2)
  it('90 直接渲染成 90.0%（**不能再乘 100**）', () => {
    expect(formatSuccessRatePct(90)).toBe('90.0%')
  })

  it('★ 与 dashboard 的 0..1 量纲相反：乘一次就是 100 倍错误', () => {
    // 对照：avg_success_rate_7d 是 0..1，要 ×100；这个不是
    expect(formatSuccessRatePct(0.9)).toBe('0.9%') // 不是 90.0%
    expect(formatSuccessRatePct(100)).toBe('100.0%')
    expect(formatSuccessRatePct(0)).toBe('0.0%')
  })

  it('缺值 ⇒ 破折号', () => {
    expect(formatSuccessRatePct(null)).toBe('—')
    expect(formatSuccessRatePct(undefined)).toBe('—')
  })
})

describe('★ avg_latency_ms：缺失 ≠ 0ms', () => {
  it('有值 ⇒ 返回数值', () => {
    expect(avgLatencyOf(point({ avg_latency_ms: 1200 }))).toBe(1200)
  })

  // ★ 视图里是 avg(...) FILTER (WHERE status='ok')
  //   ⇒ 那一小时没有成功探测时是 SQL NULL，omitempty ⇒ 键整个不存在
  it('★ 字段缺失 ⇒ null（那一小时没有成功探测）', () => {
    expect(avgLatencyOf(point({ avg_latency_ms: undefined }))).toBeNull()
    expect(avgLatencyOf(point({ avg_latency_ms: null }))).toBeNull()
  })

  it('★ 真的是 0 ⇒ 返回 0（与缺失区分开）', () => {
    expect(avgLatencyOf(point({ avg_latency_ms: 0 }))).toBe(0)
  })
})

describe('★ timeline 空时是 null（nil slice），不是 []', () => {
  it('★ null 不崩且分组为空', () => {
    expect(groupByModel(null)).toEqual([])
    expect(groupByModel(undefined)).toEqual([])
  })

  it('按模型分组，保留后端顺序', () => {
    const g = groupByModel([
      point({ raw_model_name: 'gpt-4o', hour_bucket: '2026-10-07T10:00:00Z' }),
      point({ raw_model_name: 'gpt-4o', hour_bucket: '2026-10-07T09:00:00Z' }),
      point({ raw_model_name: 'claude', hour_bucket: '2026-10-07T10:00:00Z' }),
    ])
    expect(g.map((x) => x.model)).toEqual(['gpt-4o', 'claude'])
    // ★ SQL 是 hour DESC ⇒ 组内已是最新的在前，不要再排
    expect(g[0]!.points.map((p) => p.hour_bucket)).toEqual([
      '2026-10-07T10:00:00Z',
      '2026-10-07T09:00:00Z',
    ])
  })
})

describe('★ cache-state：credential_id 非法值被后端静默忽略', () => {
  it('★ 0 / 负数 / NaN 一律不发（发了会被静默忽略=返回全量）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ entries: [], count: 0 }))
    await fetchCacheState({ credentialId: 0 })
    expect(lastUrl()).not.toContain('credential_id=')
  })

  it('负数不发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ entries: [], count: 0 }))
    await fetchCacheState({ credentialId: -5 })
    expect(lastUrl()).not.toContain('credential_id=')
  })

  it('★ 正整数照发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ entries: [], count: 0 }))
    await fetchCacheState({ credentialId: 11 })
    expect(lastUrl()).toContain('credential_id=11')
  })

  it('★ 永远不发 format（prom 会把同一个 URL 变成 text/plain）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ entries: [], count: 0 }))
    await fetchCacheState({ model: 'glm-5.2' })
    expect(lastUrl()).not.toContain('format=')
  })
})

describe('★ cache-state 撞 4096 上限', () => {
  it('count=4096 ⇒ 判为截断', () => {
    expect(cacheStateAtCap({ count: CACHE_STATE_KEY_CAP } as CacheStateResponse)).toBe(true)
  })

  it('count=4095 ⇒ 不判（证明上一条不是恒真）', () => {
    expect(cacheStateAtCap({ count: 4095 } as CacheStateResponse)).toBe(false)
  })
})

describe('★ 503 的两种来源必须区分', () => {
  it('reader not wired ⇒ not_wired', () => {
    const e = classifyCacheStateError(Object.assign(new Error('availability reader not wired'), { status: 503 }))
    expect(e.kind).toBe('not_wired')
  })

  it('redis client unavailable ⇒ redis_unavailable', () => {
    const e = classifyCacheStateError(Object.assign(new Error('redis client unavailable'), { status: 503 }))
    expect(e.kind).toBe('redis_unavailable')
  })

  // ★ 两者都是「读不到」，绝不能显示成「缓存里什么都没有」
  it('★ 两种都不是「空」', () => {
    const a = classifyCacheStateError(Object.assign(new Error('availability reader not wired'), { status: 503 }))
    const b = classifyCacheStateError(Object.assign(new Error('redis client unavailable'), { status: 503 }))
    expect(a.kind).not.toBe('empty')
    expect(b.kind).not.toBe('empty')
  })

  it('500 ⇒ other', () => {
    expect(classifyCacheStateError(Object.assign(new Error('internal server error'), { status: 500 })).kind).toBe(
      'other',
    )
  })
})

const ENTRY: CacheStateEntry = {
  credential_id: 11,
  raw_model_name: 'glm-5.2',
  state: 'healthy_confirmed',
  available: true,
  last_status: 'ok',
  consecutive_successes: 3,
  consecutive_failures: 0,
  updated_at: '2026-10-07T10:00:00Z',
  source: 'model_probe',
}

describe('★ cache-state：state 与 available 的矛盾检测', () => {
  it('healthy + available=true ⇒ 不报矛盾', () => {
    expect(cacheStateContradiction(ENTRY)).toBe(false)
  })

  // ★ 这一条是「模型明明健康却没被选中」的根因信号
  it('★ healthy 但 available=false ⇒ 报矛盾（探测说好、路由不认）', () => {
    expect(cacheStateContradiction({ ...ENTRY, available: false })).toBe(true)
  })

  it('failing + available=false ⇒ 不报（本来就该不路由）', () => {
    expect(cacheStateContradiction({ ...ENTRY, state: 'failing', available: false })).toBe(false)
  })

  it('★ 词表外 state 不算「看起来健康」', () => {
    expect(cacheStateContradiction({ ...ENTRY, state: 'weird_state', available: false })).toBe(false)
  })
})

describe('cache-state 状态配色：词表外不给 success', () => {
  it('已知状态', () => {
    expect(cacheStateTone(ENTRY)).toBe('success')
    expect(cacheStateTone({ ...ENTRY, state: 'failing' })).toBe('danger')
    expect(cacheStateTone({ ...ENTRY, state: 'suspicious' })).toBe('warning')
  })

  it('★ 词表外 ⇒ muted（不是 success）', () => {
    expect(cacheStateTone({ ...ENTRY, state: 'brand_new' })).toBe('muted')
    expect(cacheStateKeyOf({ ...ENTRY, state: 'brand_new' })).toBe('cache.state.unknown')
  })

  it('大小写不敏感', () => {
    expect(cacheStateTone({ ...ENTRY, state: 'HEALTHY_CONFIRMED' })).toBe('success')
  })
})
