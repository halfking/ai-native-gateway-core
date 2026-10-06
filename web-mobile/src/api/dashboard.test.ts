import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  unwrapDashboardEnvelope,
  dashboardIsDegraded,
  dashboardMissingView,
  dashboardHint,
  dashboardDegradedWithoutMissingView,
  dashboardDaysEffective,
  dashboardSizeEffective,
  dashboardPageEffective,
  dashboardTenantFilterHonored,
  dashboardPaginationDisagrees,
  dashboardHasNextPage,
  dashboardErrorRatePercent,
  dashboardErrorRateMeaningless,
  dashboardLatencyQuantilesOutOfOrder,
  fetchSessionOverview,
  fetchSessionTrend,
  fetchSessionHealth,
  fetchSessionActive,
  fetchModuleStats,
  fetchDashboardErrors,
  fetchDashboardPerformance,
  DASHBOARD_DAYS_DEFAULT,
  DASHBOARD_SIZE_DEFAULT,
  DASHBOARD_SIZE_MAX,
  SESSION_OVERVIEW_KEYS,
  SESSION_TREND_KEYS,
  SESSION_HEALTH_KEYS,
  SESSION_ACTIVE_KEYS,
  MODULE_STATS_KEYS,
  ERROR_STATS_KEYS,
  PERFORMANCE_KEYS,
  PERFORMANCE_SUMMARY_KEYS,
  type DashboardEnvelope,
  type SessionActiveData,
  type ErrorStatsData,
} from './dashboard'

/**
 * Dashboard API v2 七条只读端点（2026-10-08，第五十八批）。
 *
 * ★ 本族最该被钉住的一条：
 *   **降级响应的 data 与「真的全是零」逐字段相同。**
 *   `writeDegraded`（errors.go:319-334）返回 **HTTP 200 + success:true**，
 *   把 data 填成零值/空数组，只在 metadata 上打 degraded/missing_view/hint。
 *   ⇒ 唯一区分信号是 `metadata.degraded`，而它**带 omitempty**（正常时键不存在）。
 */

const fetchMock = vi.fn()
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
function lastCall(): { url: string; init: RequestInit } {
  const c = fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit]
  return { url: c[0], init: c[1] }
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

/* ── 夹具：逐字照抄 dashboardapi 各 struct 的 json tag ──────────── */

/** writeSuccessJSON（types.go:186-192）产出的成功信封。 */
function okEnv(data: unknown, metadata: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    success: true,
    data,
    metadata: { generated_at: '2026-10-08T03:00:00Z', took_ms: 12, ...metadata },
    timestamp: '2026-10-08T03:00:00.123Z',
  }
}

/** writeErrorJSON（types.go:194-216）产出的错误信封：**没有 data / metadata**。 */
function errEnv(): Record<string, unknown> {
  return {
    success: false,
    code: 'INVALID_PARAM',
    message: 'invalid parameter',
    error: { code: 'INVALID_PARAM', message: 'invalid parameter' },
    timestamp: '2026-10-08T03:00:00.123Z',
  }
}

/** ★ writeDegraded：HTTP 200 + success:true + data 全零 + metadata.degraded。 */
function degradedEnv(data: unknown, view = 'request_logs_7d'): Record<string, unknown> {
  return {
    success: true,
    data,
    metadata: {
      generated_at: '2026-10-08T03:00:00Z',
      took_ms: 3,
      degraded: true,
      missing_view: view,
      hint: '数据视图尚未初始化，请先执行数据聚合迁移',
    },
    timestamp: '2026-10-08T03:00:00.123Z',
  }
}

const OVERVIEW_DATA = {
  total_sessions: 120,
  active_sessions: 8,
  new_sessions_24h: 14,
  closed_sessions_24h: 5,
  health_distribution: { excellent: 40, good: 60, fair: 15, poor: 5 },
  compliance_stats: { compliant: 100, violations: 2 },
  cost_stats: { total_cost: 12.5, total_tokens: 900000 },
  model_usage: [{ model: 'gpt-4o', count: 10 }],
  top_clients: [{ name: 'acme', sessions: 30 }],
  top_tasks: [{ name: 'code', sessions: 20 }],
  cost_trend: [{ date: '2026-10-07', cost: 3.2 }],
  session_trend: [{ date: '2026-10-07', sessions: 9 }],
  generated_at: '2026-10-08T03:00:00Z',
  period_start: '2026-10-01T00:00:00Z',
  period_end: '2026-10-08T00:00:00Z',
}

const TREND_DATA = {
  trend: [{ date: '2026-10-07', new: 4, active: 3, closed: 2 }],
  summary: { total_new: 30, total_active: 12, total_closed: 9, avg_daily_new: 4.3 },
  period_start: '2026-10-01T00:00:00Z',
  period_end: '2026-10-08T00:00:00Z',
}

const HEALTH_DATA = {
  distribution: { excellent: 40, good: 60 },
  trend: [{ date: '2026-10-07', avg_score: 82, grade_a: 40, grade_f: 5 }],
  top_issues: [{ issue: 'timeout', count: 3 }],
}

const ACTIVE_DATA = {
  sessions: [{ id: 's1' }, { id: 's2' }],
  total_active: 8,
  page: 1,
  size: 2,
}

const MODULE_DATA = {
  modules: [{ name: 'search', executions: 40 }],
  summary: { total_modules: 6, total_executions: 120, avg_cache_hit_rate: 0.72, avg_duration_ms: 42 },
  period_start: '2026-10-01T00:00:00Z',
  period_end: '2026-10-08T00:00:00Z',
}

/** ★ Go 字段叫 `Trend`，JSON 键是 `recent_errors`（errors.go:31）。 */
const ERRORS_DATA = {
  summary: { total_errors: 12, error_rate: 0.02, total_requests: 600, avg_error_latency_ms: 830 },
  distribution: [{ status: 500, count: 12 }],
  recent_errors: [{ id: 'e1' }],
  top_errors: [{ message: 'upstream timeout', count: 5 }],
}

const PERF_DATA = {
  summary: { avg_latency_ms: 820, p50_latency_ms: 700, p95_latency_ms: 1900, p99_latency_ms: 4200 },
  latency_distribution: { '0-100ms': 30 },
  throughput: [{ ts: '2026-10-08T02:00:00Z', rps: 3.2 }],
  slow_queries: [{ query: 'select 1', ms: 3100 }],
}

/* ═══════════════════════════════════════════════════════════════════════ */

describe('dashboard / 信封', () => {
  it('成功信封通过', () => {
    const env = unwrapDashboardEnvelope<typeof OVERVIEW_DATA>(okEnv(OVERVIEW_DATA))
    expect(env.success).toBe(true)
    expect(env.data?.total_sessions).toBe(120)
  })

  it('错误信封通过（无 data / metadata）', () => {
    const env = unwrapDashboardEnvelope(errEnv())
    expect(env.success).toBe(false)
    expect(env.code).toBe('INVALID_PARAM')
    expect(env.data).toBeUndefined()
    expect(env.metadata).toBeUndefined()
  })

  it('★ 成功但 data 缺失 ⇒ 抛错（data 带 omitempty，缺失即形状不符）', () => {
    const bad = okEnv(OVERVIEW_DATA)
    delete bad.data
    expect(() => unwrapDashboardEnvelope(bad)).toThrow('成功信封缺 data')
  })

  it('缺 success / timestamp / 顶层非对象 ⇒ 抛错', () => {
    const noSucc = okEnv(OVERVIEW_DATA)
    delete noSucc.success
    expect(() => unwrapDashboardEnvelope(noSucc)).toThrow('缺 success')
    const noTs = okEnv(OVERVIEW_DATA)
    delete noTs.timestamp
    expect(() => unwrapDashboardEnvelope(noTs)).toThrow('缺 timestamp')
    expect(() => unwrapDashboardEnvelope(null)).toThrow('实得 null')
    expect(() => unwrapDashboardEnvelope([])).toThrow('实得 array')
  })

  it('metadata 缺 generated_at（唯一无 omitempty 的键）即抛错', () => {
    const bad = okEnv(OVERVIEW_DATA, {})
    ;(bad.metadata as Record<string, unknown>).generated_at = undefined
    delete (bad.metadata as Record<string, unknown>).generated_at
    expect(() => unwrapDashboardEnvelope(bad)).toThrow('metadata 缺 1 个键（generated_at）')
  })

  it('metadata 不是对象即抛错', () => {
    const bad = okEnv(OVERVIEW_DATA)
    bad.metadata = null
    expect(() => unwrapDashboardEnvelope(bad)).toThrow('metadata 不是对象')
  })

  it('错误信封缺 code / error 即抛错', () => {
    const noCode = errEnv()
    delete noCode.code
    expect(() => unwrapDashboardEnvelope(noCode)).toThrow('错误信封缺 code / error')
    const noErr = errEnv()
    delete noErr.error
    expect(() => unwrapDashboardEnvelope(noErr)).toThrow('错误信封缺 code / error')
  })
})

describe('dashboard / 降级三联', () => {
  it('★ degraded=true ⇒ 唯一可信的「数据管道挂了」信号', () => {
    const env = unwrapDashboardEnvelope(degradedEnv(ERRORS_DATA))
    expect(dashboardIsDegraded(env)).toBe(true)
    expect(dashboardMissingView(env)).toBe('request_logs_7d')
    expect(dashboardHint(env)).toContain('数据视图尚未初始化')
  })

  it('★ 正常信封的 degraded 是**键缺失**，不是 false', () => {
    const env = unwrapDashboardEnvelope(okEnv(ERRORS_DATA))
    expect('degraded' in (env.metadata as object)).toBe(false)
    expect(dashboardIsDegraded(env)).toBe(false)
    // ★ 缺键时不能报缺失视图/提示
    expect(dashboardMissingView(env)).toBeNull()
    expect(dashboardHint(env)).toBeNull()
  })

  it('★★ 降级与真零值的 data 逐字段相同 —— 唯一区别就是 metadata', () => {
    // ★ 降级时 errors.go:323-329 填的就是零值
    const zeroed = {
      summary: { total_errors: 0, error_rate: 0, total_requests: 0, avg_error_latency_ms: 0 },
      distribution: [],
      recent_errors: [],
      top_errors: [],
    }
    const degraded = unwrapDashboardEnvelope(degradedEnv(zeroed))
    const healthy = unwrapDashboardEnvelope(okEnv(zeroed))

    // data 完全一样 …
    expect(degraded.data).toEqual(healthy.data)
    // … 只有 metadata.degraded 分得开
    expect(dashboardIsDegraded(degraded)).toBe(true)
    expect(dashboardIsDegraded(healthy)).toBe(false)
  })

  it('★ 降级但缺 missing_view ⇒ 自曝（提示会告诉运维「有东西不对，但说不清是什么」）', () => {
    const env = unwrapDashboardEnvelope(degradedEnv(ERRORS_DATA))
    expect(dashboardDegradedWithoutMissingView(env)).toBe(false)
    const noView = degradedEnv(ERRORS_DATA)
    delete (noView.metadata as Record<string, unknown>).missing_view
    const env2 = unwrapDashboardEnvelope(noView)
    expect(dashboardIsDegraded(env2)).toBe(true)
    expect(dashboardMissingView(env2)).toBeNull()
    expect(dashboardDegradedWithoutMissingView(env2)).toBe(true)
  })

  it('★★ 判别样本：metadata 里有 missing_view 但**没有** degraded ⇒ 不得报「缺视图」', () => {
    // ★ 后端 writeDegraded 总是两个一起写，所以这个组合理论上不会发生。
    //   但判据的价值正在这里：**降级标记缺失时就不许声称「缺表」**，
    //   否则一个残缺/异构的响应会被我们解读成「数据管道挂了」。
    //   没有这条，D4 那条变异（去掉 isDegraded 守卫）根本看不出来 ——
    //   正常夹具里本来就没有 missing_view，两条实现输出相同。
    const partial = okEnv(ERRORS_DATA, { missing_view: 'request_logs_7d' })
    const env = unwrapDashboardEnvelope(partial)
    expect('degraded' in (env.metadata as object)).toBe(false)
    expect(dashboardIsDegraded(env)).toBe(false)
    expect(dashboardMissingView(env)).toBeNull()
    expect(dashboardHint(env)).toBeNull()
  })

  it('非降级时不算「降级却缺视图」', () => {
    const env = unwrapDashboardEnvelope(okEnv(ERRORS_DATA))
    expect(dashboardDegradedWithoutMissingView(env)).toBe(false)
  })
})

describe('dashboard / 查询参数', () => {
  it('★ days 越界静默回落 7（不报错）', () => {
    expect(dashboardDaysEffective(undefined)).toBe(7)
    expect(dashboardDaysEffective(7)).toBe(7)
    expect(dashboardDaysEffective(1)).toBe(1)
    expect(dashboardDaysEffective(90)).toBe(90)
    expect(dashboardDaysEffective(0)).toBe(DASHBOARD_DAYS_DEFAULT)
    expect(dashboardDaysEffective(91)).toBe(DASHBOARD_DAYS_DEFAULT)
    // ★ 非整数 ⇒ Atoi 失败 ⇒ 也回落
    expect(dashboardDaysEffective(7.5)).toBe(DASHBOARD_DAYS_DEFAULT)
    expect(dashboardDaysEffective(NaN)).toBe(DASHBOARD_DAYS_DEFAULT)
  })

  it('★ size 越界静默回落 20，上限 100', () => {
    expect(dashboardSizeEffective(undefined)).toBe(20)
    expect(dashboardSizeEffective(100)).toBe(100)
    expect(dashboardSizeEffective(DASHBOARD_SIZE_MAX)).toBe(100)
    expect(dashboardSizeEffective(101)).toBe(DASHBOARD_SIZE_DEFAULT)
    expect(dashboardSizeEffective(0)).toBe(DASHBOARD_SIZE_DEFAULT)
    expect(dashboardSizeEffective(20.5)).toBe(DASHBOARD_SIZE_DEFAULT)
  })

  it('page < 1 ⇒ 回落 1', () => {
    expect(dashboardPageEffective(undefined)).toBe(1)
    expect(dashboardPageEffective(3)).toBe(3)
    expect(dashboardPageEffective(0)).toBe(1)
    expect(dashboardPageEffective(-5)).toBe(1)
  })

  it('★★★ tenant_admin 填 tenant_id 会被后端静默改写 ⇒ UI 不得给这个筛选框', () => {
    expect(dashboardTenantFilterHonored('super_admin')).toBe(true)
    expect(dashboardTenantFilterHonored('admin_key')).toBe(true)
    expect(dashboardTenantFilterHonored('tenant_admin')).toBe(false)
    expect(dashboardTenantFilterHonored('')).toBe(false)
    expect(dashboardTenantFilterHonored(null)).toBe(false)
  })
})

describe('dashboard / session-active 分页', () => {
  it('data 与 metadata 的 page/size 同源时不算漂移', () => {
    const env = unwrapDashboardEnvelope<SessionActiveData>(
      okEnv(ACTIVE_DATA, { page: 1, size: 2, total: 8 }),
    )
    expect(dashboardPaginationDisagrees(env)).toBe(false)
  })

  it('★ page 不一致 ⇒ 漂移被抓到', () => {
    const env = unwrapDashboardEnvelope<SessionActiveData>(
      okEnv(ACTIVE_DATA, { page: 3, size: 2 }),
    )
    expect(dashboardPaginationDisagrees(env)).toBe(true)
  })

  it('★ size 不一致 ⇒ 漂移被抓到', () => {
    const env = unwrapDashboardEnvelope<SessionActiveData>(
      okEnv(ACTIVE_DATA, { page: 1, size: 99 }),
    )
    expect(dashboardPaginationDisagrees(env)).toBe(true)
  })

  it('★ metadata 缺 page/size ⇒ 不算漂移（只是没给）', () => {
    const env = unwrapDashboardEnvelope<SessionActiveData>(okEnv(ACTIVE_DATA))
    expect(dashboardPaginationDisagrees(env)).toBe(false)
  })

  it('★ 翻页判断：优先用 metadata.pages，退化时用「本页取满」', () => {
    const withPages = unwrapDashboardEnvelope<SessionActiveData>(
      okEnv({ ...ACTIVE_DATA, sessions: [] }, { page: 1, size: 20, pages: 4 }),
    )
    expect(dashboardHasNextPage(withPages)).toBe(true)
    const lastPage = unwrapDashboardEnvelope<SessionActiveData>(
      okEnv({ ...ACTIVE_DATA, sessions: [], page: 4 }, { page: 4, size: 20, pages: 4 }),
    )
    expect(dashboardHasNextPage(lastPage)).toBe(false)
    // ★ 无 pages ⇒ 「取满即还有」
    const full = unwrapDashboardEnvelope<SessionActiveData>(okEnv(ACTIVE_DATA))
    expect(dashboardHasNextPage(full)).toBe(true)
    const notFull = unwrapDashboardEnvelope<SessionActiveData>(
      okEnv({ ...ACTIVE_DATA, sessions: [{ id: 's1' }] }),
    )
    expect(dashboardHasNextPage(notFull)).toBe(false)
  })

  it('无 data 时一律 false', () => {
    const env = unwrapDashboardEnvelope(errEnv()) as DashboardEnvelope<SessionActiveData>
    expect(dashboardPaginationDisagrees(env)).toBe(false)
    expect(dashboardHasNextPage(env)).toBe(false)
  })
})

describe('dashboard / errors', () => {
  it('★ JSON 键是 recent_errors（Go 字段叫 Trend）', () => {
    const env = unwrapDashboardEnvelope<ErrorStatsData>(okEnv(ERRORS_DATA))
    expect(env.data?.recent_errors).toEqual([{ id: 'e1' }])
    expect('trend' in (env.data as object)).toBe(false)
  })

  it('error_rate 是比值，展示时换算成百分数', () => {
    const d = ERRORS_DATA
    expect(dashboardErrorRatePercent(d)).toBeCloseTo(2)
  })

  it('★ 分母为 0 ⇒ 比值无意义（降级时正是这个形态）', () => {
    const zeroed: ErrorStatsData = {
      summary: { total_errors: 0, error_rate: 0, total_requests: 0, avg_error_latency_ms: 0 },
      distribution: [], recent_errors: [], top_errors: [],
    }
    expect(dashboardErrorRateMeaningless(zeroed)).toBe(true)
    expect(dashboardErrorRateMeaningless(ERRORS_DATA)).toBe(false)
  })
})

describe('dashboard / performance', () => {
  it('分位数单调时不算乱序', () => {
    expect(dashboardLatencyQuantilesOutOfOrder(PERF_DATA)).toBe(false)
  })

  it('★ p95 > p99 ⇒ 乱序被抓到', () => {
    expect(
      dashboardLatencyQuantilesOutOfOrder({
        ...PERF_DATA,
        summary: { ...PERF_DATA.summary, p95_latency_ms: 5000, p99_latency_ms: 4200 },
      }),
    ).toBe(true)
  })

  it('★ p50 > p95 ⇒ 乱序被抓到', () => {
    expect(
      dashboardLatencyQuantilesOutOfOrder({
        ...PERF_DATA,
        summary: { ...PERF_DATA.summary, p50_latency_ms: 9000 },
      }),
    ).toBe(true)
  })
})

describe('dashboard / fetch 与逐键钉住', () => {
  it('session-overview 打 GET 且带查询参数', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(okEnv(OVERVIEW_DATA)))
    await fetchSessionOverview({ days: 14, tenantId: 'acme', refresh: true })
    const { url, init } = lastCall()
    expect(init.method).toBe('GET')
    expect(url).toContain('/api/admin/dashboard/session-overview?')
    expect(url).toContain('days=14')
    expect(url).toContain('tenant_id=acme')
    // ★ 后端判 `== "true"`，所以必须发字面量
    expect(url).toContain('refresh=true')
  })

  it('无参数时不带问号', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(okEnv(TREND_DATA)))
    await fetchSessionTrend()
    expect(lastCall().url).toBe('/api/admin/dashboard/session-trend')
  })

  it('★ 每个端点的必填键逐个钉住（删一个就抛错）', async () => {
    const cases: Array<[string, unknown, readonly string[], () => Promise<unknown>]> = [
      ['session-overview', OVERVIEW_DATA, SESSION_OVERVIEW_KEYS, () => fetchSessionOverview()],
      ['session-trend', TREND_DATA, SESSION_TREND_KEYS, () => fetchSessionTrend()],
      ['session-health', HEALTH_DATA, SESSION_HEALTH_KEYS, () => fetchSessionHealth()],
      ['session-active', ACTIVE_DATA, SESSION_ACTIVE_KEYS, () => fetchSessionActive()],
      ['module-stats', MODULE_DATA, MODULE_STATS_KEYS, () => fetchModuleStats()],
      ['errors', ERRORS_DATA, ERROR_STATS_KEYS, () => fetchDashboardErrors()],
      ['performance', PERF_DATA, PERFORMANCE_KEYS, () => fetchDashboardPerformance()],
    ]
    for (const [name, data, keys, call] of cases) {
      expect(keys.length).toBeGreaterThan(0)
      // ★ 每轮复制一份再删：直接 delete 共享夹具会让缺失**累积**，
      //   第二轮起就变成「缺 2 个键」，判据名不副实。
      //   也不能用 `[k]: undefined` —— 键仍在，`k in obj` 为真。
      for (const k of keys) {
        const broken: Record<string, unknown> = { ...(data as object) }
        delete broken[k]
        fetchMock.mockResolvedValueOnce(jsonResponse(okEnv(broken)))
        await expect(call()).rejects.toThrow(new RegExp(`${name} data 缺 1 个键（${k}）`))
      }
    }
  })

  it('★ summary 子对象缺键也抛错（performance）', async () => {
    for (const k of PERFORMANCE_SUMMARY_KEYS) {
      const broken = { ...PERF_DATA, summary: { ...PERF_DATA.summary } } as Record<string, unknown>
      delete (broken.summary as Record<string, unknown>)[k]
      fetchMock.mockResolvedValueOnce(jsonResponse(okEnv(broken)))
      await expect(fetchDashboardPerformance()).rejects.toThrow(
        new RegExp(`dashboard/performance.summary data 缺 1 个键（${k}）`),
      )
    }
  })

  it('降级响应同样能解开（data 零值但结构完整）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(degradedEnv(ERRORS_DATA)))
    const env = await fetchDashboardErrors()
    expect(dashboardIsDegraded(env)).toBe(true)
    // ★ top_errors 是 top_errors 数组；recent_errors 才是 [{id:'e1'}]
    expect(env.data?.recent_errors).toEqual([{ id: 'e1' }])
    expect(env.data?.top_errors).toEqual([{ message: 'upstream timeout', count: 5 }])
  })

  it('★ 信封不合法时 fetch 也抛错（不是把坏形状递给调用方）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: true }))
    await expect(fetchSessionOverview()).rejects.toThrow('缺 success')
  })
})
