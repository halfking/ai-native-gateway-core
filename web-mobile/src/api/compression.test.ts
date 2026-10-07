import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  // 时间窗
  hoursParseEffective,
  hoursEffective,
  timeWindowMode,
  timeWindowQuery,
  HOURS_DEFAULT,
  HOURS_MIN,
  HOURS_MAX,
  // stats
  fetchCompressionStats,
  unwrapCompressionStats,
  compressionRateMeaningless,
  strategyCountsDisagree,
  tokenBandCount,
  outboundTokensMissing,
  estimatedOrigMissing,
  tokensSavedMissing,
  summaryModeRowsMissing,
  seriesGranularityOf,
  hourlySeriesMayBeFailed,
  bucketCountsDisagree,
  COMPRESSION_STATS_KEYS,
  COMPRESSION_STATS_OPTIONAL_KEYS,
  HOUR_BUCKET_KEYS,
  // sessions
  fetchCompressionSessions,
  unwrapCompressionSessions,
  sessionsPageEffective,
  sessionsPageSizeEffective,
  sessionsEmptyIsAmbiguous,
  countExceedsListable,
  sessionsHasNextPage,
  sessionReductionUnknown,
  sessionOutboundMsgUnknown,
  sessionReductionClamped,
  SESSIONS_PAGE_DEFAULT,
  SESSIONS_PAGE_SIZE_DEFAULT,
  SESSIONS_PAGE_SIZE_MAX,
  COMPRESSION_SESSION_KEYS,
} from '@/api/compression'

/**
 * compression 只读两条端点的不变量（2026-10-08，第六十八批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **`count` 查询失败返 200 + `{items:[],count:0}`**（不是 500）
 *      （compression_sessions.go:108-112）⇒ `count:0` 是**二义的**。
 *   ② ★★★★ **`hours` 只在 from/to 都缺省时生效**（:89-109 / :64-85）
 *      ⇒ 传了 from 就等于把 hours 扔了。
 *   ③ ★★★★ **`compressed_total` 是组级口径**：组内有一行有 outbound_body
 *      就整组 cnt 都算（:173-176）⇒ 压缩率的分子不是「被压缩的行数」。
 *   ④ ★★★★ **本族两个端点的 nil 编码相反**：
 *      stats 的七个 token 字段有 omitempty ⇒ **键缺失**；
 *      sessions 的四个 `*int` 无 omitempty ⇒ 键在、值为 **null**。
 *   ⑤ ★★★ **租户隔离之外还有第二重口径**：`($3 OR rl.success)`
 *      ⇒ tenant_admin 只看成功请求。
 *   ⑥ ★★★ **`count` 是真实总数**（COUNT DISTINCT），与 approvals 的
 *      `total`（本页条数）**正好相反**。
 *   ⑦ ★★★ 空 `gw_session_id` 被静默丢弃但 count 仍算它（:190）
 *      ⇒ `count` 可能大于能列出的会话数。
 *   ⑧ ★★ **`msg_reduction` 负值被夹到 0**（:185-187）⇒ 永不为负。
 *   ⑨ ★★ **`estimated_tokens_saved` 键缺失 ≠ 没节省**（:204-206）。
 *   ⑩ ★★ **`hourly_series` 的粒度随时间窗变化**（:257-266），字段名骗人。
 *
 * 夹具说明：字段名逐字取自后端 struct 的 json tag；数值按后端 SQL 的
 * 计算式构造。nullable 四字段按「键恒存在、值可为 null」构造。
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

/* ── 夹具：compression_stats.go:113-130 ─────────────────────────────── */

/**
 * 全字段版本：七个 token 字段全部出现（都达阈值）。
 * `compression_rate` 是 **0-1 比例**（:187 除法无 ×100），
 * `hourly_series[].rate` 同（:294）。
 */
function statsFixture() {
  return {
    total_requests: 1000,
    compressed_total: 600,
    compression_rate: 0.6,
    strategy_distribution: { none: 400, p2c: 400, summary: 200 },
    token_band_below: 700,
    token_band_preliminary: 250,
    total_outbound_tokens: 480000,
    estimated_original_tokens: 960000,
    estimated_tokens_saved: 480000,
    summary_mode_rows: 12,
    // 字段名叫 hourly，粒度却是「每 6 小时」（:263）
    hourly_series: [
      { hour: '2026-10-08T06:00:00Z', total: 300, compressed: 180, rate: 0.6 },
      { hour: '2026-10-08T12:00:00Z', total: 200, compressed: 120, rate: 0.6 },
      { hour: '2026-10-08T18:00:00Z', total: 500, compressed: 300, rate: 0.6 },
    ],
  }
}

/** ★ 七个 omitempty 字段全部缺键（都是「没达阈值」或「查询静默失败」）。 */
function statsBareFixture() {
  return {
    total_requests: 0,
    compressed_total: 0,
    compression_rate: 0,
    strategy_distribution: {},
    hourly_series: [],
  }
}

/* ── 夹具：compression_sessions.go:16-32 ────────────────────────────── */

/**
 * `estimated_original_msgs` 由 SQL `COALESCE(latest.orig_msg_count, 0)` 兜底（:125）
 * ⇒ 恒非 null；`outbound_msg_count` 直接 MAX 扫进指针 ⇒ 可为 null。
 */
function sessionsFixture() {
  return {
    items: [
      {
        gw_session_id: 'sess-a',
        // ★ MAX(compression_strategy) 是字典序最大，不是首个/最新
        compression_strategy: 'p2c',
        request_count: 8,
        first_ts: '2026-10-08T01:00:00.123456789Z',
        last_ts: '2026-10-08T01:30:00.987654321Z',
        outbound_msg_count: 4,
        outbound_token_est: 1200,
        estimated_original_msgs: 20,
        msg_reduction: 16,
        sample_request_id: 'req-0008',
      },
      {
        gw_session_id: 'sess-b',
        compression_strategy: 'none',
        request_count: 2,
        first_ts: '2026-10-08T00:10:00Z',
        last_ts: '2026-10-08T00:12:00Z',
        // ★ outbound_msg_count 为 null ⇒ msg_reduction 必为 null（:183）
        outbound_msg_count: null,
        outbound_token_est: 300,
        estimated_original_msgs: 9,
        msg_reduction: null,
        sample_request_id: 'req-0002',
      },
      {
        gw_session_id: 'sess-c',
        compression_strategy: 'summary',
        request_count: 1,
        first_ts: '2026-10-08T02:00:00Z',
        last_ts: '2026-10-08T02:00:00Z',
        outbound_msg_count: 30,
        outbound_token_est: 900,
        // ★ 压缩后比原来还多 ⇒ 差值为负，被夹到 0（:185-187）
        estimated_original_msgs: 10,
        msg_reduction: 0,
        sample_request_id: 'req-0009',
      },
    ],
    count: 42,
  }
}

/* ═══════════════════════════════════════════════════════════════════════
 * ② hours / from / to 的时间窗口径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('compression / 时间窗参数', () => {
  it('handler.go:1536-1542 空串或非整数 ⇒ 回落缺省 24', () => {
    expect(hoursParseEffective(undefined)).toBe(HOURS_DEFAULT)
    expect(hoursParseEffective('')).toBe(24)
    expect(hoursParseEffective('abc')).toBe(24)
    expect(hoursParseEffective('1.5')).toBe(24)
    expect(hoursParseEffective('48')).toBe(48)
  })

  it('stats:75-80 是 **clamp 到 [1,720]**，不是回落缺省', () => {
    expect(hoursEffective(0)).toBe(HOURS_MIN)
    expect(hoursEffective(-50)).toBe(1)
    expect(hoursEffective(721)).toBe(HOURS_MAX)
    expect(hoursEffective(99999)).toBe(720)
    expect(hoursEffective(24)).toBe(24)
  })

  it('★ 都缺省时 hours 才是时间窗口径', () => {
    expect(timeWindowMode(undefined)).toBe('hours')
    expect(timeWindowMode({ hours: 24 })).toBe('hours')
  })

  it('★★ 传了 from ⇒ hours 被完全忽略', () => {
    expect(timeWindowMode({ hours: 24, from: '2026-10-08T00:00:00Z' })).toBe('explicit')
  })

  it('★★ 传了 to ⇒ hours 同样被忽略', () => {
    expect(timeWindowMode({ hours: 24, to: '2026-10-08T00:00:00Z' })).toBe('explicit')
  })

  it('explicit 口径下查询串里**不含 hours**（后端会忽略它，发了只会误导）', () => {
    const q = timeWindowQuery({ hours: 24, from: '2026-10-08T00:00:00Z' })
    expect(q).toContain('from=')
    expect(q).not.toContain('hours=')
  })

  it('hours 口径下才发 hours，并按 clamp 后的值发', () => {
    expect(timeWindowQuery({ hours: 9999 })).toBe('?hours=720')
    expect(timeWindowQuery({ hours: 0 })).toBe('?hours=1')
  })

  it('from 与 to 都在时两个都发，仍不含 hours', () => {
    const q = timeWindowQuery({
      hours: 48,
      from: '2026-10-01T00:00:00Z',
      to: '2026-10-08T00:00:00Z',
    })
    expect(q).toContain('from=2026-10-01T00%3A00%3A00Z')
    expect(q).toContain('to=2026-10-08T00%3A00%3A00Z')
    expect(q).not.toContain('hours=')
  })

  it('什么都不传 ⇒ 仍显式发 hours=24（与后端缺省一致，显式更可读）', () => {
    expect(timeWindowQuery(undefined)).toBe('?hours=24')
    expect(timeWindowQuery({})).toBe('?hours=24')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * stats：解包与形状守卫
 * ═══════════════════════════════════════════════════════════════════════ */

describe('compression / stats 解包', () => {
  it('compression_stats.go:113-130 全字段响应正常解包', () => {
    const m = unwrapCompressionStats(statsFixture())
    expect(m.total_requests).toBe(1000)
    expect(m.compression_rate).toBe(0.6)
    expect(m.hourly_series).toHaveLength(3)
  })

  it('必检键恰好五个，optional 键恰好七个（后者不是必填）', () => {
    expect(COMPRESSION_STATS_KEYS).toHaveLength(5)
    expect(COMPRESSION_STATS_OPTIONAL_KEYS).toHaveLength(7)
    expect(HOUR_BUCKET_KEYS).toHaveLength(4)
  })

  it('★★ 七个 omitempty 键全缺时**正常接受**（不能抛错）', () => {
    const m = unwrapCompressionStats(statsBareFixture())
    expect(m.total_requests).toBe(0)
    expect(m.hourly_series).toEqual([])
  })

  it('顶层缺必检键 ⇒ 抛错并点名', () => {
    const f = { ...statsFixture() } as Record<string, unknown>
    delete f.compression_rate
    expect(() => unwrapCompressionStats(f)).toThrow(/compression_rate/)
  })

  it('strategy_distribution 不是对象 ⇒ 抛错', () => {
    const f = { ...statsFixture(), strategy_distribution: [] } as unknown
    expect(() => unwrapCompressionStats(f)).toThrow(/strategy_distribution 不是对象/)
  })

  it('strategy_distribution 的值不是数字 ⇒ 抛错并带键名', () => {
    const f = { ...statsFixture(), strategy_distribution: { none: '400' } }
    expect(() => unwrapCompressionStats(f)).toThrow(/strategy_distribution\["none"\]/)
  })

  it('hourly_series 不是数组 ⇒ 抛错', () => {
    const f = { ...statsFixture(), hourly_series: null }
    expect(() => unwrapCompressionStats(f)).toThrow(/hourly_series 不是数组/)
  })

  it('hourly_series[1] 缺子键 ⇒ 抛错并带下标', () => {
    const f = JSON.parse(JSON.stringify(statsFixture())) as Record<string, unknown>
    delete (f.hourly_series as Record<string, unknown>[])[1]!['rate']
    expect(() => unwrapCompressionStats(f)).toThrow(/hourly_series\[1\]/)
  })

  it('拿到 dashboardapi 信封形状 ⇒ 拒绝', () => {
    expect(() => unwrapCompressionStats({ success: true, timestamp: 1, data: statsFixture() }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('非对象响应 ⇒ 报出实际形状', () => {
    expect(() => unwrapCompressionStats([])).toThrow(/实得 array/)
    expect(() => unwrapCompressionStats(null)).toThrow(/实得 null/)
  })

  it('fetchCompressionStats 打 GET /api/admin/compression/stats', async () => {
    stub(statsFixture())
    await fetchCompressionStats()
    expect(arg().method).toBe('GET')
    expect(arg().path).toBe('/api/admin/compression/stats?hours=24')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ③⑨⑩ stats：口径与缺失语义
 * ═══════════════════════════════════════════════════════════════════════ */

describe('compression / stats 口径与缺失', () => {
  it('★ total_requests=0 ⇒ 那个 compression_rate 0 无意义', () => {
    expect(compressionRateMeaningless(unwrapCompressionStats(statsBareFixture()))).toBe(true)
    expect(compressionRateMeaningless(unwrapCompressionStats(statsFixture()))).toBe(false)
  })

  it('★ 各组之和等于 total_requests 时判定为正常', () => {
    // 400+400+200 = 1000
    expect(strategyCountsDisagree(unwrapCompressionStats(statsFixture()))).toBe(false)
  })

  it('策略名撞键导致少算时判定为矛盾', () => {
    const f = { ...statsFixture(), strategy_distribution: { none: 400, p2c: 400 } }
    expect(strategyCountsDisagree(unwrapCompressionStats(f))).toBe(true)
  })

  it('★ band 计数缺失时返回 null 而不是 0（缺键 ≠ 0 条）', () => {
    const m = unwrapCompressionStats(statsFixture())
    expect(tokenBandCount(m, 'below')).toBe(700)
    // 夹具没有 token_band_forced
    expect(tokenBandCount(m, 'forced')).toBeNull()
    expect(tokenBandCount(unwrapCompressionStats(statsBareFixture()), 'below')).toBeNull()
  })

  it('outbound token 合计缺键时能判定出来', () => {
    expect(outboundTokensMissing(unwrapCompressionStats(statsBareFixture()))).toBe(true)
    expect(outboundTokensMissing(unwrapCompressionStats(statsFixture()))).toBe(false)
  })

  it('★ 估算原始 token 缺键有「查询静默失败」与「值为 0」两种可能，客户端分不出', () => {
    expect(estimatedOrigMissing(unwrapCompressionStats(statsBareFixture()))).toBe(true)
    expect(estimatedOrigMissing(unwrapCompressionStats(statsFixture()))).toBe(false)
  })

  it('★★★★ 节省估算缺键**不能**被当成「没节省」（负值与失败都是缺键）', () => {
    const f = { ...statsFixture() } as Record<string, unknown>
    delete f.estimated_tokens_saved
    expect(tokensSavedMissing(unwrapCompressionStats(f))).toBe(true)
    // 节省为 0 与节省为负 都会走到这里 —— 响应里同样没有这个键
  })

  it('summary_mode_rows 缺键能判定出来（0 与失败都缺键）', () => {
    expect(summaryModeRowsMissing(unwrapCompressionStats(statsBareFixture()))).toBe(true)
    expect(summaryModeRowsMissing(unwrapCompressionStats(statsFixture()))).toBe(false)
  })

  it('★ 序列粒度随时间窗变化：≤48h 按小时、≤168h 按 6 小时、更长按天', () => {
    expect(seriesGranularityOf(1)).toBe('hour')
    expect(seriesGranularityOf(48)).toBe('hour')
    expect(seriesGranularityOf(49)).toBe('6h')
    expect(seriesGranularityOf(168)).toBe('6h')
    expect(seriesGranularityOf(169)).toBe('day')
    expect(seriesGranularityOf(720)).toBe('day')
  })

  it('★ 序列为空时不能断言「没有流量」（bucket 查询失败也是空数组）', () => {
    expect(hourlySeriesMayBeFailed(unwrapCompressionStats(statsBareFixture()))).toBe(true)
    expect(hourlySeriesMayBeFailed(unwrapCompressionStats(statsFixture()))).toBe(false)
  })

  it('桶内 compressed > total ⇒ 判定计数矛盾', () => {
    const f = JSON.parse(JSON.stringify(statsFixture())) as { hourly_series: { total: number; compressed: number }[] }
    f.hourly_series[0]!.compressed = 999
    expect(bucketCountsDisagree(unwrapCompressionStats(f))).toBe(true)
  })

  it('桶计数正常时判定为无矛盾（负控）', () => {
    expect(bucketCountsDisagree(unwrapCompressionStats(statsFixture()))).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ④⑥⑦⑧ sessions
 * ═══════════════════════════════════════════════════════════════════════ */

describe('compression / sessions 分页口径', () => {
  it('sessions:53-55 page < 1 ⇒ 回落 1', () => {
    expect(sessionsPageEffective(undefined)).toBe(SESSIONS_PAGE_DEFAULT)
    expect(sessionsPageEffective(0)).toBe(1)
    expect(sessionsPageEffective(-5)).toBe(1)
    expect(sessionsPageEffective(3)).toBe(3)
  })

  it('★★ sessions:56-58 page_size 不在 [1,200] ⇒ **静默回落 50**（不是 clamp）', () => {
    expect(sessionsPageSizeEffective(undefined)).toBe(SESSIONS_PAGE_SIZE_DEFAULT)
    expect(sessionsPageSizeEffective(0)).toBe(50)
    expect(sessionsPageSizeEffective(-1)).toBe(50)
    expect(sessionsPageSizeEffective(201)).toBe(50)
    expect(sessionsPageSizeEffective(1)).toBe(1)
    expect(sessionsPageSizeEffective(SESSIONS_PAGE_SIZE_MAX)).toBe(200)
  })

  it('两个常量的值必须等于后端字面量（夹具不得由常量推导）', () => {
    expect(SESSIONS_PAGE_DEFAULT).toBe(1)
    expect(SESSIONS_PAGE_SIZE_DEFAULT).toBe(50)
    expect(SESSIONS_PAGE_SIZE_MAX).toBe(200)
  })

  it('fetchCompressionSessions 把 page/page_size 原样发出（按生效值）', async () => {
    stub(sessionsFixture())
    await fetchCompressionSessions({ page: 2, pageSize: 999 })
    expect(arg().path).toBe('/api/admin/compression/sessions?hours=24&page=2&page_size=50')
  })

  it('strategy 过滤原样拼接（后端不校验合法性，非法值返空列表）', async () => {
    stub(sessionsFixture())
    await fetchCompressionSessions({ strategy: 'p2c' })
    expect(arg().path).toContain('strategy=p2c')
  })

  it('★ count 是真实总数 ⇒ 可以据此判「还有下一页」', () => {
    const r = unwrapCompressionSessions({ items: Array.from({ length: 50 }, () => sessionsFixture().items[0]), count: 42 })
    expect(sessionsHasNextPage(r)).toBe(true)
  })

  it('★ 自定义 pageSize 时阈值要用请求值，不能恒按 50 判', () => {
    const r = unwrapCompressionSessions({ items: Array.from({ length: 20 }, () => sessionsFixture().items[0]), count: 100 })
    expect(sessionsHasNextPage(r, 20)).toBe(true)
    expect(sessionsHasNextPage(r)).toBe(false)
  })

  it('未取满 ⇒ 没有下一页（负控）', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(sessionsHasNextPage(r)).toBe(false)
  })
})

describe('compression / sessions 解包与缺失语义', () => {
  it('compression_sessions.go:16-32 三行正常解包', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(r.items).toHaveLength(3)
    expect(r.count).toBe(42)
  })

  it('十个必检键齐全，四个 nullable 键也在其中（键恒存在）', () => {
    expect(COMPRESSION_SESSION_KEYS).toHaveLength(10)
    const f = sessionsFixture().items[0] as Record<string, unknown>
    for (const k of ['outbound_msg_count', 'outbound_token_est', 'estimated_original_msgs', 'msg_reduction']) {
      expect(k in f).toBe(true)
    }
  })

  it('★ nullable 键值为 null 时必须被接受（这是本族的正常编码）', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(r.items[1]!.outbound_msg_count).toBeNull()
    expect(r.items[1]!.msg_reduction).toBeNull()
  })

  it('★ nullable 键值既不是数字也不是 null ⇒ 抛错并带下标与键名', () => {
    const f = JSON.parse(JSON.stringify(sessionsFixture())) as { items: Record<string, unknown>[] }
    f.items[0]!.outbound_msg_count = 'four'
    expect(() => unwrapCompressionSessions(f)).toThrow(/items\[0\] outbound_msg_count/)
  })

  it('nullable 键整体缺失 ⇒ 抛错（后端没有 omitempty，键不可能缺）', () => {
    const f = JSON.parse(JSON.stringify(sessionsFixture())) as { items: Record<string, unknown>[] }
    delete f.items[0]!.msg_reduction
    expect(() => unwrapCompressionSessions(f)).toThrow(/msg_reduction/)
  })

  it('顶层缺键 ⇒ 抛错并点名', () => {
    const f = { ...sessionsFixture() } as Record<string, unknown>
    delete f.count
    expect(() => unwrapCompressionSessions(f)).toThrow(/count/)
  })

  it('items 不是数组 ⇒ 抛错', () => {
    expect(() => unwrapCompressionSessions({ items: {}, count: 0 })).toThrow(/items 不是数组/)
  })

  it('拿到信封形状 ⇒ 拒绝', () => {
    expect(() => unwrapCompressionSessions({ success: true, timestamp: 2, data: sessionsFixture() }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('★ count=0 且列表空 ⇒ 二义（真没有 or count 查询失败返 200）', () => {
    expect(sessionsEmptyIsAmbiguous(unwrapCompressionSessions({ items: [], count: 0 }))).toBe(true)
  })

  it('列表非空 ⇒ 不必用二义措辞（负控）', () => {
    expect(sessionsEmptyIsAmbiguous(unwrapCompressionSessions(sessionsFixture()))).toBe(false)
  })

  it('★ count 大于本页条数是可预期的（空 session_id 被丢弃但 count 仍算它）', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(countExceedsListable(r)).toBe(true)
  })

  it('count 等于本页条数时判定为无缺口（负控）', () => {
    const f = sessionsFixture()
    f.count = f.items.length
    expect(countExceedsListable(unwrapCompressionSessions(f))).toBe(false)
  })

  it('msg_reduction 为 null ⇒ 判定为「未知」', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(sessionReductionUnknown(r.items[1]!)).toBe(true)
    expect(sessionReductionUnknown(r.items[0]!)).toBe(false)
  })

  it('outbound_msg_count 为 null ⇒ 判定为「未知」', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(sessionOutboundMsgUnknown(r.items[1]!)).toBe(true)
    expect(sessionOutboundMsgUnknown(r.items[0]!)).toBe(false)
  })

  it('★ outbound 比 orig 还多且 reduction=0 ⇒ 判定为「被夹到 0」（差值为负被抹平）', () => {
    // sess-c: outbound 30 > estimated 10 ⇒ red = 10-30 = -20 → 夹到 0
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(sessionReductionClamped(r.items[2]!)).toBe(true)
  })

  it('outbound 恰等于 orig 且 reduction=0 ⇒ 本来就是 0，不是夹值（负控）', () => {
    const f = JSON.parse(JSON.stringify(sessionsFixture())) as { items: Record<string, unknown>[] }
    f.items[2]!.outbound_msg_count = 10
    f.items[2]!.estimated_original_msgs = 10
    f.items[2]!.msg_reduction = 0
    const r = unwrapCompressionSessions(f)
    expect(sessionReductionClamped(r.items[2]!)).toBe(false)
  })

  it('reduction 为正时不是夹值（负控）', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(sessionReductionClamped(r.items[0]!)).toBe(false)
  })

  it('reduction 为 null 时不判定「被夹」（样本不足以判断）', () => {
    const r = unwrapCompressionSessions(sessionsFixture())
    expect(sessionReductionClamped(r.items[1]!)).toBe(false)
  })
})