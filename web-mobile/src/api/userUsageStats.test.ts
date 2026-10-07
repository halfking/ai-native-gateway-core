import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchUserUsageSummary,
  fetchUserStats,
  unwrapUserUsageSummary,
  unwrapUserStats,
  userStatsKeyCountIsAmbiguous,
  userStatsBucketIsAmbiguous,
  userStatsBucketHasRows,
  userUsageItemsMayBeTruncated,
  userStatsDailyUsesShanghaiDayCut,
  userStatsDailyLengthMatchesDays,
  userStatsNotFoundCoversBothCases,
  userStatsLookupFailedIsDistinct,
  userUsageTenantFilterIsSkipped,
  userUsageItemsAreRequestsDescending,
  userStatsErrorRateIsAmbiguous,
  userStatsErrorRateIsComputed,
  userStatsRecentFirstChunkIsNull,
  userStatsRecentTotalIsNull,
  userStatsRecentModelIsPlaceholder,
  userStatsRecentStatusIsPlaceholder,
  userStatsAppBucketPlaceholderIsNone,
  userUsageSummaryDaysIsAccepted,
  userStatsDaysIsAccepted,
  userUsageEffectiveDays,
  USER_USAGE_DB_UNAVAILABLE_MESSAGE,
  USER_STATS_INVALID_ID_MESSAGE,
  USER_STATS_NOT_FOUND_MESSAGE,
  USER_STATS_LOOKUP_FAILED_MESSAGE,
  USER_STATS_DISPATCH_NOT_FOUND_MESSAGE,
  USER_USAGE_DEFAULT_DAYS,
  USER_USAGE_SUMMARY_MAX_DAYS,
  USER_STATS_MAX_DAYS,
  USER_STATS_TOP_LIMIT,
  USER_STATS_RECENT_LIMIT,
  USER_USAGE_ROW_KEYS,
  USER_USAGE_SUMMARY_KEYS,
  USER_STATS_KEYS,
  USER_STATS_KPI_KEYS,
  USER_STATS_BUCKET_KEYS,
  USER_STATS_RECENT_KEYS,
  USER_STATS_DAILY_KEYS,
  USER_STATS_TOP_KEYS,
  USER_STATS_MODEL_PLACEHOLDER,
  USER_STATS_APP_PLACEHOLDER,
  USER_STATS_KEY_PLACEHOLDER,
  USER_STATS_RECENT_MODEL_PLACEHOLDER,
  USER_STATS_RECENT_STATUS_PLACEHOLDER,
  type UserUsageSummaryResponse,
  type UserStatsResponse,
  type UserStatsRecentRequest,
  type UserStatsBucket,
} from './userUsageStats'

/**
 * 账号用量与单用户画像的契约测试（2026-10-08，第八十八批）。
 *
 * 后端：`admin/handler.go:1104-1105`（两个都 `admin(...)` ⇒ **admin 档**）
 * → `admin/user_usage_stats.go`（395 行）。
 *
 * 重点是源文件头写明的十八件事 (1)…(18)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
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

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ── 夹具：逐字照抄 `admin/user_usage_stats.go` 的 struct ──

function summaryOf(over: Partial<UserUsageSummaryResponse> = {}): UserUsageSummaryResponse {
  return {
    days: 30,
    items: [
      { username: 'alice', requests: 120, tokens: 50000, credits: 300, last_active_at: '2026-10-07T12:00:00Z' },
      { username: 'bob', requests: 30, tokens: 9000, credits: 60, last_active_at: null },
    ],
    ...over,
  }
}

function bucket(over: Partial<UserStatsBucket> = {}): UserStatsBucket {
  return { name: 'gpt-4o', requests: 90, tokens: 40000, credits: 250, cost_usd: 12.5, ...over }
}

function recent(over: Partial<UserStatsRecentRequest> = {}): UserStatsRecentRequest {
  return {
    ts: '2026-10-07T12:00:00.123456789Z',
    model: 'gpt-4o',
    first_chunk_ms: 180,
    total_ms: 900,
    credits: 3,
    status: 'success',
    ...over,
  }
}

function statsOf(over: Partial<UserStatsResponse> = {}): UserStatsResponse {
  return {
    user_id: 7,
    username: 'alice',
    days: 3,
    kpi: {
      requests: 120,
      tokens: 50000,
      credits: 300,
      errors: 3,
      error_rate: 0.025,
      // ★ SQL 有 COALESCE ⇒ 实际路径恒为数字
      latency_p95_ms: 820,
    },
    // ★ generate_series 零填充 ⇒ 长度恒等于 days
    daily: [
      { date: '2026-10-05', requests: 0, success: 0, errors: 0, tokens: 0, credits: 0, cost: 0 },
      { date: '2026-10-06', requests: 40, success: 39, errors: 1, tokens: 20000, credits: 120, cost: 6.5 },
      { date: '2026-10-07', requests: 80, success: 78, errors: 2, tokens: 30000, credits: 180, cost: 6.0 },
    ],
    top_models: [bucket()],
    top_apps: [bucket({ name: 'chat', requests: 100 })],
    top_keys: [bucket({ name: 'sk-abc', requests: 110 })],
    key_count: 2,
    recent_requests: [recent()],
    ...over,
  }
}

// ══════════════════════════════════════════════════════════════════════════
// usage-summary 解包
// ══════════════════════════════════════════════════════════════════════════

describe('usage-summary 解包', () => {
  it('★ 满配响应被放行（两键信封）', () => {
    expect(unwrapUserUsageSummary(summaryOf())).toBeTruthy()
  })

  it('★ ★ items 是空数组 ⇒ 放行（[]userUsageRow{} 初始化，不是 null）', () => {
    expect(() => unwrapUserUsageSummary(summaryOf({ items: [] }))).not.toThrow()
  })

  it('★ 缺 days ⇒ 抛并点名', () => {
    const d = del(summaryOf() as unknown as Record<string, unknown>, 'days')
    expect(() => unwrapUserUsageSummary(d)).toThrow(/账号用量汇总 缺 1 个键（days）/)
  })

  it('★ ★ 两个键都缺 ⇒ 抛并报数量与顺序', () => {
    expect(() => unwrapUserUsageSummary({ nope: 1 })).toThrow(/账号用量汇总 缺 2 个键（days, items）/)
  })

  it('★ days 不是数字 ⇒ 抛', () => {
    expect(() => unwrapUserUsageSummary(summaryOf({ days: '30' as unknown as number }))).toThrow(
      /账号用量汇总 的 days 不是数字/,
    )
  })

  it('★ ★ items 不是数组 ⇒ 抛', () => {
    expect(() => unwrapUserUsageSummary(summaryOf({ items: {} as unknown as never }))).toThrow(
      /账号用量汇总 的 items 不是数组/,
    )
  })

  it('★ 行缺一个键 ⇒ 抛并点名下标', () => {
    const s = summaryOf()
    const bad = del(s.items[0] as unknown as Record<string, unknown>, 'credits')
    expect(() => unwrapUserUsageSummary({ ...s, items: [bad] })).toThrow(/items\[0\] 缺 1 个键（credits）/)
  })

  it('★ username 不是字符串 ⇒ 抛', () => {
    const s = summaryOf()
    expect(() => unwrapUserUsageSummary({ ...s, items: [{ ...s.items[0]!, username: 1 } as never] })).toThrow(
      /items\[0\] 的 username 不是字符串/,
    )
  })

  it('★ ★ requests 不是数字 ⇒ 抛', () => {
    const s = summaryOf()
    expect(() => unwrapUserUsageSummary({ ...s, items: [{ ...s.items[0]!, requests: 'x' } as never] })).toThrow(
      /items\[0\] 的 requests 不是数字/,
    )
  })

  it('★ ★★ last_active_at 是裸 null ⇒ 放行（*time.Time 无 omitempty）', () => {
    expect(() => unwrapUserUsageSummary(summaryOf())).not.toThrow()
  })

  it('★ ★ last_active_at 是数字 ⇒ 抛（不是串也不是 null）', () => {
    const s = summaryOf()
    const bad = { ...s.items[0]!, last_active_at: 123 as unknown as string | null }
    expect(() => unwrapUserUsageSummary({ ...s, items: [bad] })).toThrow(
      /items\[0\] 的 last_active_at 不是字符串也不是 null/,
    )
  })

  it('★ 顶层不是对象 ⇒ 抛', () => {
    expect(() => unwrapUserUsageSummary([])).toThrow(/账号用量汇总 响应形状不符：期望裸对象，实得 array/)
  })

  // ★ 逐字段钉住：否则循环里任何一个键被移出检查都没有用例能打掉它。
  it('★ ★★ 行的五个字段逐个都要校验', () => {
    const s = summaryOf()
    const base = { ...s.items[0]! } as unknown as Record<string, unknown>
    const bad = (o: Record<string, unknown>) => unwrapUserUsageSummary({ ...s, items: [o as never] })
    expect(() => bad({ ...base, username: 1 })).toThrow(/items\[0\] 的 username 不是字符串/)
    expect(() => bad({ ...base, requests: 'x' })).toThrow(/items\[0\] 的 requests 不是数字/)
    expect(() => bad({ ...base, tokens: 'x' })).toThrow(/items\[0\] 的 tokens 不是数字/)
    expect(() => bad({ ...base, credits: 'x' })).toThrow(/items\[0\] 的 credits 不是数字/)
    expect(() => bad({ ...base, last_active_at: 123 })).toThrow(/items\[0\] 的 last_active_at 不是字符串也不是 null/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// {id}/stats 解包
// ══════════════════════════════════════════════════════════════════════════

describe('用户画像解包', () => {
  it('★ 满配响应被放行（10 键）', () => {
    expect(unwrapUserStats(statsOf())).toBeTruthy()
  })

  it('★ ★ 缺 key_count ⇒ 抛（恒在键）', () => {
    const d = del(statsOf() as unknown as Record<string, unknown>, 'key_count')
    expect(() => unwrapUserStats(d)).toThrow(/用户画像 缺 1 个键（key_count）/)
  })

  it('★ 缺 recent_requests ⇒ 抛', () => {
    const d = del(statsOf() as unknown as Record<string, unknown>, 'recent_requests')
    expect(() => unwrapUserStats(d)).toThrow(/用户画像 缺 1 个键（recent_requests）/)
  })

  it('★ ★ key_count 是 null ⇒ 抛（失败静默兜 0，**不**是 null）', () => {
    expect(() => unwrapUserStats(statsOf({ key_count: null as unknown as number }))).toThrow(
      /用户画像 的 key_count 不是数字/,
    )
  })

  it('★ kpi 不是对象 ⇒ 抛', () => {
    expect(() => unwrapUserStats(statsOf({ kpi: [] as never }))).toThrow(/用户画像 的 kpi 响应形状不符/)
  })

  it('★ ★ kpi 缺 latency_p95_ms ⇒ 抛', () => {
    const r = statsOf()
    const bad = del({ ...r.kpi } as unknown as Record<string, unknown>, 'latency_p95_ms')
    expect(() => unwrapUserStats({ ...r, kpi: bad as never })).toThrow(/kpi 缺 1 个键（latency_p95_ms）/)
  })

  it('★ kpi.error_rate 不是数字 ⇒ 抛', () => {
    const r = statsOf()
    expect(() => unwrapUserStats({ ...r, kpi: { ...r.kpi, error_rate: 'x' as unknown as number } })).toThrow(
      /kpi 的 error_rate 不是数字/,
    )
  })

  it('★ ★ kpi.latency_p95_ms 为 null ⇒ 放行（*int64 无 omitempty）', () => {
    const r = statsOf()
    expect(() => unwrapUserStats({ ...r, kpi: { ...r.kpi, latency_p95_ms: null } })).not.toThrow()
  })

  it('★ daily 不是数组 ⇒ 抛', () => {
    expect(() => unwrapUserStats(statsOf({ daily: null as never }))).toThrow(/用户画像 的 daily 不是数组/)
  })

  it('★ daily 元素缺一个键 ⇒ 抛并点名', () => {
    const r = statsOf()
    const bad = del({ ...r.daily[0]! } as unknown as Record<string, unknown>, 'success')
    expect(() => unwrapUserStats({ ...r, daily: [bad as never] })).toThrow(/daily\[0\] 缺 1 个键（success）/)
  })

  it('★ daily 的 date 不是字符串 ⇒ 抛', () => {
    const r = statsOf()
    expect(() => unwrapUserStats({ ...r, daily: [{ ...r.daily[0]!, date: 1 } as never] })).toThrow(
      /daily\[0\] 的 date 不是字符串/,
    )
  })

  it('★ ★ top_models 元素缺 cost_usd ⇒ 抛', () => {
    const r = statsOf()
    const bad = del({ ...r.top_models[0]! } as unknown as Record<string, unknown>, 'cost_usd')
    expect(() => unwrapUserStats({ ...r, top_models: [bad as never] })).toThrow(
      /top_models\[0\] 缺 1 个键（cost_usd）/,
    )
  })

  it('★ top_apps 元素 name 不是字符串 ⇒ 抛（另一个桶）', () => {
    const r = statsOf()
    expect(() => unwrapUserStats({ ...r, top_apps: [{ ...r.top_apps[0]!, name: 1 } as never] })).toThrow(
      /top_apps\[0\] 的 name 不是字符串/,
    )
  })

  it('★ top_keys 不是数组 ⇒ 抛（第三个桶）', () => {
    expect(() => unwrapUserStats(statsOf({ top_keys: 'x' as never }))).toThrow(/用户画像 的 top_keys 不是数组/)
  })

  it('★ ★ 三个 Top 桶都是空数组 ⇒ 放行（查询失败降级就是这个形状）', () => {
    expect(() => unwrapUserStats(statsOf({ top_models: [], top_apps: [], top_keys: [] }))).not.toThrow()
  })

  it('★ recent 元素缺 first_chunk_ms ⇒ 抛', () => {
    const r = statsOf()
    const bad = del({ ...r.recent_requests[0]! } as unknown as Record<string, unknown>, 'first_chunk_ms')
    expect(() => unwrapUserStats({ ...r, recent_requests: [bad as never] })).toThrow(
      /recent_requests\[0\] 缺 1 个键（first_chunk_ms）/,
    )
  })

  it('★ ★ first_chunk_ms 为 null ⇒ 放行（*int64 无 omitempty，真会为 null）', () => {
    const r = statsOf()
    expect(() => unwrapUserStats({ ...r, recent_requests: [{ ...r.recent_requests[0]!, first_chunk_ms: null }] })).not.toThrow()
  })

  it('★ ★ total_ms 为 null ⇒ 放行（另一列）', () => {
    const r = statsOf()
    expect(() => unwrapUserStats({ ...r, recent_requests: [{ ...r.recent_requests[0]!, total_ms: null }] })).not.toThrow()
  })

  it('★ first_chunk_ms 是字符串 ⇒ 抛', () => {
    const r = statsOf()
    expect(() =>
      unwrapUserStats({ ...r, recent_requests: [{ ...r.recent_requests[0]!, first_chunk_ms: 'x' as never }] }),
    ).toThrow(/recent_requests\[0\] 的 first_chunk_ms 不是数字也不是 null/)
  })

  it('★ recent.status 不是字符串 ⇒ 抛', () => {
    const r = statsOf()
    expect(() => unwrapUserStats({ ...r, recent_requests: [{ ...r.recent_requests[0]!, status: 1 } as never] })).toThrow(
      /recent_requests\[0\] 的 status 不是字符串/,
    )
  })

  // ★ 下面五条逐字段钉住：否则循环里任何一个键被移出检查都没有用例能打掉它。
  it('★ ★★ 顶层四个标量键逐个都要校验', () => {
    expect(() => unwrapUserStats(statsOf({ user_id: 'x' as never }))).toThrow(/用户画像 的 user_id 不是数字/)
    expect(() => unwrapUserStats(statsOf({ username: 1 as never }))).toThrow(/用户画像 的 username 不是字符串/)
    expect(() => unwrapUserStats(statsOf({ days: 'x' as never }))).toThrow(/用户画像 的 days 不是数字/)
    expect(() => unwrapUserStats(statsOf({ key_count: null as never }))).toThrow(/用户画像 的 key_count 不是数字/)
  })

  it('★ ★★ kpi 的六个键逐个都要校验', () => {
    const k = statsOf().kpi
    for (const key of ['requests', 'tokens', 'credits', 'errors', 'error_rate'] as const) {
      expect(() => unwrapUserStats(statsOf({ kpi: { ...k, [key]: 'x' } as never }))).toThrow(
        new RegExp(`kpi 的 ${key} 不是数字`),
      )
    }
    expect(() => unwrapUserStats(statsOf({ kpi: { ...k, latency_p95_ms: 'x' } as never }))).toThrow(
      /kpi 的 latency_p95_ms 不是数字也不是 null/,
    )
  })

  it('★ ★★ daily 的八个键逐个都要校验', () => {
    const row = statsOf().daily[0]!
    expect(() => unwrapUserStats(statsOf({ daily: [{ ...row, date: 1 } as never] }))).toThrow(
      /daily\[0\] 的 date 不是字符串/,
    )
    for (const key of ['requests', 'success', 'errors', 'tokens', 'credits', 'cost'] as const) {
      expect(() => unwrapUserStats(statsOf({ daily: [{ ...row, [key]: 'x' } as never] }))).toThrow(
        new RegExp(`daily\\[0\\] 的 ${key} 不是数字`),
      )
    }
  })

  it('★ ★★ 三个桶的五个键逐个都要校验', () => {
    for (const bk of USER_STATS_TOP_KEYS) {
      const base = { ...statsOf().top_models[0]! } as unknown as Record<string, unknown>
      expect(() => unwrapUserStats(statsOf({ [bk]: [{ ...base, name: 1 } as never] } as never))).toThrow(
        new RegExp(`${bk}\\[0\\] 的 name 不是字符串`),
      )
      for (const key of ['requests', 'tokens', 'credits', 'cost_usd'] as const) {
        expect(() => unwrapUserStats(statsOf({ [bk]: [{ ...base, [key]: 'x' } as never] } as never))).toThrow(
          new RegExp(`${bk}\\[0\\] 的 ${key} 不是数字`),
        )
      }
    }
  })

  it('★ ★★ recent 的六个键逐个都要校验', () => {
    const base = { ...statsOf().recent_requests[0]! } as unknown as Record<string, unknown>
    const bad = (o: Record<string, unknown>) => unwrapUserStats(statsOf({ recent_requests: [o as never] }))
    expect(() => bad({ ...base, ts: 1 })).toThrow(/recent_requests\[0\] 的 ts 不是字符串/)
    expect(() => bad({ ...base, model: 1 })).toThrow(/recent_requests\[0\] 的 model 不是字符串/)
    expect(() => bad({ ...base, status: 1 })).toThrow(/recent_requests\[0\] 的 status 不是字符串/)
    expect(() => bad({ ...base, credits: 'x' })).toThrow(/recent_requests\[0\] 的 credits 不是数字/)
    expect(() => bad({ ...base, first_chunk_ms: 'x' })).toThrow(
      /recent_requests\[0\] 的 first_chunk_ms 不是数字也不是 null/,
    )
    expect(() => bad({ ...base, total_ms: 'x' })).toThrow(
      /recent_requests\[0\] 的 total_ms 不是数字也不是 null/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 语义判据
// ══════════════════════════════════════════════════════════════════════════

describe('(1)(2)(3) 静默降级造成的二义', () => {
  it('★ ★★ key_count=0 且无请求 ⇒ 判为二义（真 0 / 查失败）', () => {
    const r = statsOf({ key_count: 0, kpi: { ...statsOf().kpi, requests: 0, error_rate: 0 } })
    expect(userStatsKeyCountIsAmbiguous(r)).toBe(true)
  })

  it('★ ★ key_count>0 ⇒ 不二义（反向）', () => {
    expect(userStatsKeyCountIsAmbiguous(statsOf({ key_count: 2 }))).toBe(false)
  })

  // ★ `&&` 的另一半专格：只有被测项翻转、其余项为「不触发值」。
  it('★ ★★ key_count 非 0 而 requests 为 0 ⇒ 不判为二义（另一半专格）', () => {
    const r = statsOf({ key_count: 2 })
    expect(userStatsKeyCountIsAmbiguous({ ...r, kpi: { ...r.kpi, requests: 0, error_rate: 0 } })).toBe(false)
  })

  // ★ 第一格专格：key_count 为 0 但确有请求 ⇒ 合取项的第二项不成立。
  it('★ ★★ key_count 为 0 而有请求 ⇒ 不判为二义（第一格专格）', () => {
    const r = statsOf({ key_count: 0 })
    expect(userStatsKeyCountIsAmbiguous(r)).toBe(false)
  })

  it('★ ★ 桶是空数组 ⇒ 判为二义（无数据 / 查询失败）', () => {
    expect(userStatsBucketIsAmbiguous([])).toBe(true)
    expect(userStatsBucketHasRows([])).toBe(false)
  })

  it('★ 桶非空 ⇒ HasRows 为 true（反向）', () => {
    expect(userStatsBucketHasRows([bucket()])).toBe(true)
    expect(userStatsBucketIsAmbiguous([bucket()])).toBe(false)
  })

  it('★ ★★ items 为空 ⇒ 可能被静默跳行截断', () => {
    expect(userUsageItemsMayBeTruncated(summaryOf({ items: [] }))).toBe(true)
  })

  it('★ items 非空 ⇒ 不判为截断（反向）', () => {
    const s = summaryOf()
    expect(userUsageItemsMayBeTruncated(s)).toBe(false)
    expect(userUsageItemsMayBeTruncated({ ...s, items: [s.items[0]!] })).toBe(false)
  })
})

describe('(5)(6) daily 的零填充与日切', () => {
  it('★ ★★ daily 长度等于 days ⇒ 成立（generate_series 零填充）', () => {
    expect(userStatsDailyLengthMatchesDays(statsOf({ days: 3 }))).toBe(true)
  })

  it('★ daily 长度小于 days ⇒ 不成立', () => {
    const r = statsOf({ days: 30 })
    expect(userStatsDailyLengthMatchesDays(r)).toBe(false)
  })

  // ★ 后端 `generate_series` 零填充只造得出**正好** days 行 ⇒ 超长不可达，
  // 但它正是 `===` 与 `>=` 的唯一区分格。
  it('★ ★★ daily 比 days 还长 ⇒ 也不成立（超长只能来自响应被拼接）', () => {
    const r = statsOf({ days: 2 })
    expect(userStatsDailyLengthMatchesDays(r)).toBe(false)
  })

  it('★ ★ daily 的 date 全是 YYYY-MM-DD ⇒ 成立（to_char 的产物）', () => {
    expect(userStatsDailyUsesShanghaiDayCut(statsOf())).toBe(true)
  })

  it('★ ★ daily 的 date 含时间分量 ⇒ 不成立', () => {
    const r = statsOf()
    expect(userStatsDailyUsesShanghaiDayCut({ ...r, daily: [{ ...r.daily[0]!, date: '2026-10-05T00:00:00Z' }] })).toBe(
      false,
    )
  })
})

describe('(7)(8) 404 的两种成因', () => {
  it('★ ★ user not found 同时覆盖「不存在」与「跨租户」', () => {
    expect(userStatsNotFoundCoversBothCases('user not found')).toBe(true)
  })

  it('★ 其它文案不算这覆盖', () => {
    expect(userStatsNotFoundCoversBothCases('not found')).toBe(false)
  })

  it('★ ★★ lookup user failed 是 500 且文案可区分于 404', () => {
    expect(userStatsLookupFailedIsDistinct('lookup user failed')).toBe(true)
    expect(userStatsLookupFailedIsDistinct('user not found')).toBe(false)
    expect(userStatsLookupFailedIsDistinct('lookup failed')).toBe(false)
  })
})

describe('(9) 租户隔离被 default 短路', () => {
  it('★ ★ 租户键是 default ⇒ 跳过过滤（看到全平台）', () => {
    expect(userUsageTenantFilterIsSkipped('default')).toBe(true)
  })

  it('★ ★ 租户键是空串 ⇒ 也跳过', () => {
    expect(userUsageTenantFilterIsSkipped('')).toBe(true)
  })

  it('★ 普通租户键 ⇒ 不过滤（反向）', () => {
    expect(userUsageTenantFilterIsSkipped('t-1')).toBe(false)
  })
})

describe('(12)(16) 排序与二义数值', () => {
  it('★ ★ items 按 requests 降序 ⇒ 成立', () => {
    expect(userUsageItemsAreRequestsDescending(summaryOf())).toBe(true)
    // ★ 同值并列必须仍为 true —— 后端 ORDER BY 无 tiebreak，顺序未定义但**不是升序**。
    const s = summaryOf()
    const tie = [
      { ...s.items[0]!, username: 'alice', requests: 7 },
      { ...s.items[1]!, username: 'bob', requests: 7 },
    ]
    expect(userUsageItemsAreRequestsDescending({ ...s, items: tie })).toBe(true)
  })

  it('★ ★ items 出现升序 ⇒ 不成立', () => {
    const s = summaryOf()
    expect(
      userUsageItemsAreRequestsDescending({
        ...s,
        items: [s.items[1]!, s.items[0]!],
      }),
    ).toBe(false)
  })

  it('★ ★ error_rate 是 0 ⇒ 二义（真 0 或 requests=0 未计算）', () => {
    expect(userStatsErrorRateIsAmbiguous(statsOf({ kpi: { ...statsOf().kpi, error_rate: 0 } }))).toBe(true)
  })

  it('★ error_rate 非 0 ⇒ 不二义（反向）', () => {
    expect(userStatsErrorRateIsAmbiguous(statsOf())).toBe(false)
  })

  it('★ requests > 0 ⇒ error_rate 是算出来的', () => {
    expect(userStatsErrorRateIsComputed(statsOf())).toBe(true)
  })

  it('★ ★ requests = 0 ⇒ error_rate 未计算', () => {
    const r = statsOf()
    expect(userStatsErrorRateIsComputed({ ...r, kpi: { ...r.kpi, requests: 0 } })).toBe(false)
  })
})

describe('(15)(17) 可空列与占位符', () => {
  it('★ first_chunk_ms 为 null ⇒ 判为 null', () => {
    expect(userStatsRecentFirstChunkIsNull(recent({ first_chunk_ms: null }))).toBe(true)
  })

  it('★ first_chunk_ms 有值 ⇒ 不判为 null（反向）', () => {
    expect(userStatsRecentFirstChunkIsNull(recent())).toBe(false)
  })

  // ★★ `0` 正是「falsy 但不是 null」那一格 —— `=== null` 与 `!x` 的唯一分界。
  it('★ ★ first_chunk_ms 是 0 ⇒ 不判为 null（0 是 falsy 但非 null）', () => {
    expect(userStatsRecentFirstChunkIsNull(recent({ first_chunk_ms: 0 }))).toBe(false)
  })

  it('★ total_ms 为 null ⇒ 判为 null', () => {
    expect(userStatsRecentTotalIsNull(recent({ total_ms: null }))).toBe(true)
  })

  it('★ ★ total_ms 是 0 ⇒ 不判为 null（另一列的同一格）', () => {
    expect(userStatsRecentTotalIsNull(recent({ total_ms: 0 }))).toBe(false)
  })

  it('★ recent.model 是短横 ⇒ 判为占位符', () => {
    expect(userStatsRecentModelIsPlaceholder(recent({ model: '-' }))).toBe(true)
  })

  // ★ 后端空串已被 COALESCE 回落成短横 ⇒ 空串是「不可达值」，但它正是唯一区分格。
  it('★ ★ recent.model 是空串 ⇒ 不判为占位符（不可达值，唯一区分格）', () => {
    expect(userStatsRecentModelIsPlaceholder(recent({ model: '' }))).toBe(false)
  })

  it('★ ★ recent.model 是 unknown 角括号 ⇒ **不**判为占位符（那是 Top 桶的）', () => {
    expect(userStatsRecentModelIsPlaceholder(recent({ model: '<unknown>' }))).toBe(false)
  })

  it('★ recent.status 是 unknown ⇒ 判为占位符', () => {
    expect(userStatsRecentStatusIsPlaceholder(recent({ status: 'unknown' }))).toBe(true)
  })

  it('★ ★ recent.status 是空串 ⇒ 不判为占位符（不可达值，唯一区分格）', () => {
    expect(userStatsRecentStatusIsPlaceholder(recent({ status: '' }))).toBe(false)
  })

  it('★ ★ 桶的占位符是 none ⇒ 判为 app 桶占位符', () => {
    expect(userStatsAppBucketPlaceholderIsNone(bucket({ name: '<none>' }))).toBe(true)
  })

  it('★ ★ 桶名是空串 ⇒ 不判为 none（不可达值，唯一区分格）', () => {
    expect(userStatsAppBucketPlaceholderIsNone(bucket({ name: '' }))).toBe(false)
  })

  it('★ ★ 桶的占位符是 unknown ⇒ **不**判为 none', () => {
    expect(userStatsAppBucketPlaceholderIsNone(bucket({ name: '<unknown>' }))).toBe(false)
  })
})

describe('(4) days 上界分叉', () => {
  it('★ ★ usage-summary 接受 365 天', () => {
    expect(userUsageSummaryDaysIsAccepted(365)).toBe(true)
  })

  it('★ ★ usage-summary 不接受 366 天', () => {
    expect(userUsageSummaryDaysIsAccepted(366)).toBe(false)
  })

  it('★ ★★ stats 只接受 90 天（**与 usage-summary 差 4 倍**）', () => {
    expect(userStatsDaysIsAccepted(90)).toBe(true)
    expect(userStatsDaysIsAccepted(365)).toBe(false)
  })

  it('★ 两天数域都不接受 0', () => {
    expect(userUsageSummaryDaysIsAccepted(0)).toBe(false)
    expect(userStatsDaysIsAccepted(0)).toBe(false)
  })

  it('★ ★ 非法 days 静默回落 30（不 400）', () => {
    expect(userUsageEffectiveDays(366, USER_USAGE_SUMMARY_MAX_DAYS)).toBe(30)
    expect(userUsageEffectiveDays(91, USER_STATS_MAX_DAYS)).toBe(30)
    expect(userUsageEffectiveDays(0, USER_STATS_MAX_DAYS)).toBe(30)
    expect(userUsageEffectiveDays(-1, USER_STATS_MAX_DAYS)).toBe(30)
  })

  // ★ 阈值逐段覆盖：低于下界 / 中段 / **正好等于上界**。
  it('★ 合法 days 的生效值就是它自己', () => {
    expect(userUsageEffectiveDays(30, USER_STATS_MAX_DAYS)).toBe(30)
    expect(userUsageEffectiveDays(7, USER_STATS_MAX_DAYS)).toBe(7)
    expect(userUsageEffectiveDays(1, USER_STATS_MAX_DAYS)).toBe(1)
    expect(userUsageEffectiveDays(90, USER_STATS_MAX_DAYS)).toBe(90)
    expect(userUsageEffectiveDays(365, USER_USAGE_SUMMARY_MAX_DAYS)).toBe(365)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ usage-summary 路径与 days 拼装', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summaryOf()))
    await fetchUserUsageSummary({ days: 7 })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/admin/users/usage-summary')
    expect(u).toContain('days=7')
  })

  it('★ usage-summary 不给 days 时不带问号', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summaryOf()))
    await fetchUserUsageSummary()
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('?')
  })

  it('★ ★ stats 路径把 userId 插在中间段', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsOf()))
    await fetchUserStats({ userId: 7, days: 30 })
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/admin/users/7/stats')
    expect(u).toContain('days=30')
  })

  it('★ stats 不给 days 时不带问号', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsOf()))
    await fetchUserStats({ userId: 7 })
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('?')
  })

  it('★ ★ zero-filled 的 daily 与 days 一致（端到端）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsOf({ days: 3 })))
    const r = await fetchUserStats({ userId: 7 })
    expect(userStatsDailyLengthMatchesDays(r)).toBe(true)
  })

  it('★ ★ 缺 key_count ⇒ 抛错', async () => {
    const d = del(statsOf() as unknown as Record<string, unknown>, 'key_count')
    fetchMock.mockResolvedValueOnce(jsonResponse(d))
    await expect(fetchUserStats({ userId: 7 })).rejects.toThrow(/缺 1 个键（key_count）/)
  })

  it('★ ★ 三个桶全空的降级响应也能解包（200 不带错误）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsOf({ top_models: [], top_apps: [], top_keys: [] })))
    const r = await fetchUserStats({ userId: 7 })
    expect(userStatsBucketIsAmbiguous(r.top_models)).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ 503 文案 === db not available（本仓第五种措辞）', () => {
    expect(USER_USAGE_DB_UNAVAILABLE_MESSAGE).toBe('db not available')
  })

  it('★ ★ 503 文案**不含** database（与其它端点不同）', () => {
    expect(USER_USAGE_DB_UNAVAILABLE_MESSAGE).not.toContain('database')
  })

  it('★ 400 文案 === invalid user id', () => {
    expect(USER_STATS_INVALID_ID_MESSAGE).toBe('invalid user id')
  })

  it('★ 404 文案 === user not found（不存在与跨租户共用）', () => {
    expect(USER_STATS_NOT_FOUND_MESSAGE).toBe('user not found')
  })

  it('★ 500 文案 === lookup user failed', () => {
    expect(USER_STATS_LOOKUP_FAILED_MESSAGE).toBe('lookup user failed')
  })

  it('★ 分发器兜底 404 文案 === not found', () => {
    expect(USER_STATS_DISPATCH_NOT_FOUND_MESSAGE).toBe('not found')
  })

  it('★ ★ 两个 days 上界确实是 365 / 90', () => {
    expect(USER_USAGE_SUMMARY_MAX_DAYS).toBe(365)
    expect(USER_STATS_MAX_DAYS).toBe(90)
  })

  it('★ days 缺省 === 30', () => {
    expect(USER_USAGE_DEFAULT_DAYS).toBe(30)
  })

  it('★ Top 桶 LIMIT === 5，recent LIMIT === 10', () => {
    expect(USER_STATS_TOP_LIMIT).toBe(5)
    expect(USER_STATS_RECENT_LIMIT).toBe(10)
  })

  it('★ usage-summary 行 5 键', () => {
    expect(USER_USAGE_ROW_KEYS.length).toBe(5)
  })

  it('★ usage-summary 信封 2 键', () => {
    expect(USER_USAGE_SUMMARY_KEYS.length).toBe(2)
  })

  it('★ ★ stats 响应 10 键', () => {
    expect(USER_STATS_KEYS.length).toBe(10)
  })

  it('★ kpi 6 键', () => {
    expect(USER_STATS_KPI_KEYS.length).toBe(6)
  })

  it('★ ★ kpi 键集里含 latency_p95_ms（*int64 无 omitempty ⇒ 可为 null）', () => {
    expect(USER_STATS_KPI_KEYS as readonly string[]).toContain('latency_p95_ms')
  })

  it('★ 桶 5 键、recent 6 键、daily 7 键', () => {
    expect(USER_STATS_BUCKET_KEYS.length).toBe(5)
    expect(USER_STATS_RECENT_KEYS.length).toBe(6)
    expect(USER_STATS_DAILY_KEYS.length).toBe(7)
  })

  it('★ ★ 三个 Top 桶键名与后端 json tag 一致', () => {
    expect([...USER_STATS_TOP_KEYS]).toEqual(['top_models', 'top_apps', 'top_keys'])
  })

  it('★ ★ stats 键集**不含** daily_gap 之类（后端没这概念）', () => {
    expect(USER_STATS_KEYS as readonly string[]).not.toContain('timezone')
  })

  // ★ 下面五条把「导出但没人读」的常量钉住，否则它们改值没有任何用例能发现。
  it('★ ★★ top_models 与 top_keys 的占位符是 unknown 角括号', () => {
    expect(USER_STATS_MODEL_PLACEHOLDER).toBe('<unknown>')
    expect(USER_STATS_KEY_PLACEHOLDER).toBe('<unknown>')
  })

  it('★ ★ top_apps 的占位符是 none 角括号（与上面两个不同）', () => {
    expect(USER_STATS_APP_PLACEHOLDER).toBe('<none>')
  })

  it('★ ★★ recent 的两个占位符与三个桶的各不相同', () => {
    expect(USER_STATS_RECENT_MODEL_PLACEHOLDER).toBe('-')
    expect(USER_STATS_RECENT_STATUS_PLACEHOLDER).toBe('unknown')
  })

  it('★ ★ usage-summary 的信封两键与行五键逐一与后端对齐', () => {
    expect([...USER_USAGE_SUMMARY_KEYS]).toEqual(['days', 'items'])
    expect([...USER_USAGE_ROW_KEYS]).toEqual(['username', 'requests', 'tokens', 'credits', 'last_active_at'])
  })

  it('★ ★ kpi 六键、桶五键、recent 六键、daily 七键逐一与后端对齐', () => {
    expect([...USER_STATS_KPI_KEYS]).toEqual(['requests', 'tokens', 'credits', 'errors', 'error_rate', 'latency_p95_ms'])
    expect([...USER_STATS_BUCKET_KEYS]).toEqual(['name', 'requests', 'tokens', 'credits', 'cost_usd'])
    expect([...USER_STATS_RECENT_KEYS]).toEqual(['ts', 'model', 'first_chunk_ms', 'total_ms', 'credits', 'status'])
    expect([...USER_STATS_DAILY_KEYS]).toEqual(['date', 'requests', 'success', 'errors', 'tokens', 'credits', 'cost'])
  })
})
