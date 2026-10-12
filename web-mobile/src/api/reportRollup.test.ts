import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchReportRollupSummary,
  fetchReportRollupDimensions,
  unwrapReportRollupSummary,
  unwrapReportRollupDimensions,
  reportRollupIsDegraded,
  reportRollupHasData,
  reportRollupDegradedCode,
  reportRollupDefaultWindowExcludesToday,
  reportRollupRangeIsTooLong,
  reportRollupSingleDayIsValid,
  normalizeReportView,
  reportRollupViewIsKnown,
  reportRollupFilterIsNeverEchoed,
  reportTotalsErrorCountMatches,
  reportTotalsCacheHitRatioIsNull,
  reportTotalsCacheHitRatioIsZero,
  reportBreakdownIsNullOnParentRow,
  reportBreakdownIsAbsentOnDailyRow,
  reportRollupSourceIsKnown,
  reportRollupIsLegacyOnly,
  reportRollupIsMixed,
  reportCoverageDeriveSource,
  reportRollupSourceMatchesCoverage,
  reportLegacyOnlyHasLegacyDates,
  reportCoverageGrainDatesIsNull,
  reportCoverageLegacyDatesIsNull,
  reportCoverageDatesAreAscending,
  reportStartIsRfc3339NotDateOnly,
  reportStartIsDateOnly,
  reportDetailIsOn,
  reportExportDetailIsOff,
  reportDimensionHasName,
  reportDimensionKeyLooksNumeric,
  REPORT_ROLLUP_DEGRADED_CODE,
  REPORT_ROLLUP_DB_UNAVAILABLE_MESSAGE,
  REPORT_ROLLUP_VIEWS,
  REPORT_ROLLUP_DEFAULT_VIEW,
  REPORT_ROLLUP_SOURCES,
  REPORT_ROLLUP_MAX_RANGE_DAYS,
  REPORT_ROLLUP_DEFAULT_WINDOW_DAYS,
  REPORT_ROLLUP_DETAIL_ON_TOKENS,
  REPORT_ROLLUP_EXPORT_OFF_TOKENS,
  REPORT_ROLLUP_DEGRADED_KEYS,
  REPORT_TOTALS_KEYS,
  REPORT_GRAIN_ALWAYS_KEYS,
  REPORT_GRAIN_OPTIONAL_KEYS,
  REPORT_DIMENSIONS_KEYS,
  type ReportTotals,
  type ReportGrainReport,
  type ReportDimensionOption,
  type ReportRollupSummaryResponse,
  type ReportRollupDegradedResponse,
} from './reportRollup'

/**
 * 对账汇总 + 筛选栏候选的契约测试（2026-10-08，第八十四批）。
 *
 * 后端：`admin/handler.go:1067`
 * `mux.HandleFunc("/api/admin/report-rollup/", h.superAdmin(h.handleReportRollup))`
 * → `admin/report_rollup.go`（`:94-118` 的 `strings.HasSuffix` switch）
 * → 类型在 `domains/reportrollup/`。
 *
 * 重点是源文件头写明的十八件事 (1)…(15a)。带 ★ 的自校验判据都能被变异打掉。
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
function lastUrl(): string {
  return String(fetchMock.mock.calls[0]![0])
}

// ── 夹具：逐字照抄 `domains/reportrollup/report.go` 的 `Totals` ──
// 16 个无 omitempty 的键恒在 + `internal_currency` 条件键。

function totals(over: Partial<ReportTotals> = {}): ReportTotals {
  return {
    request_count: 100,
    success_count: 97,
    // ★ 97 成功 ⇒ 3 失败（`ErrorCount = RequestCount − SuccessCount`，report.go:40）
    error_count: 3,
    error_rate: 0.03,
    input_tokens: 1000,
    output_tokens: 200,
    cache_read_tokens: 50,
    cache_write_tokens: 10,
    total_tokens: 1260,
    estimated_cost_cents: 420,
    currency: 'USD',
    credits_charged: 12,
    internal_cost_cents: 0,
    // ★ 分母 0 时后端是**裸 null**（`CacheHitRatio *float64` 无 omitempty）
    cache_hit_ratio: null,
    latency_p50_ms: 120,
    latency_p95_ms: 480,
    ...over,
  }
}

/** `GrainReport`（`grainreport.go:109-144`）：10 个恒在键 + 14 个条件键。 */
function grain(over: Partial<ReportGrainReport> = {}): ReportGrainReport {
  return {
    // ★ 回显是 `time.Time` 的 RFC3339Nano，**不是**发进去的 `YYYY-MM-DD`
    start: '2026-10-01T00:00:00Z',
    end: '2026-10-07T00:00:00Z',
    view: 'provider',
    totals: totals(),
    // ★ 无 omitempty ⇒ nil 时是**裸 null**（父行）
    error_breakdown: null,
    days: [],
    models: [],
    snapshot_dates: ['2026-10-07'],
    // ★ 两个 []string 无 omitempty ⇒ 可为裸 null
    coverage: { grain_dates: ['2026-10-07'], legacy_dates: [] },
    // ★ 由 coverage 派生：legacy 空 ⇒ grain
    source: 'grain',
    ...over,
  }
}

function summary(over: Partial<ReportGrainReport> = {}): Record<string, unknown> {
  return { report: grain(over) }
}

/** `admin/report_rollup.go:318-319` 的降级信封 —— **主键整个消失**。 */
function degraded(): ReportRollupDegradedResponse {
  return { degraded: true, error_code: 'REPORT_SNAPSHOTS_NOT_MIGRATED' }
}

function option(over: Partial<ReportDimensionOption> = {}): ReportDimensionOption {
  return { key: '7', requests: 42, ...over }
}

/** `dimensions` 六个键全部无 omitempty ⇒ 恒在（`dimensions.go:27-33`）。 */
function dims(over: Partial<Record<string, unknown>> = {}): Record<string, unknown> {
  return {
    providers: [option({ key: '7', requests: 42, name: 'openai' })],
    credentials: [option({ key: '3', requests: 20 })],
    api_keys: [option({ key: 'sk-abc', requests: 9, name: 'key-alias' })],
    models: [option({ key: 'gpt-4o', requests: 30 })],
    tenants: [option({ key: 't-1', requests: 11 })],
    persons: [option({ key: 'alice', requests: 5 })],
    ...over,
  }
}

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ══════════════════════════════════════════════════════════════════════════
// (1) 降级信封：200 + 两键，主键消失
// ══════════════════════════════════════════════════════════════════════════

describe('(1) 降级信封', () => {
  it('★ summary 降级 ⇒ 原样返回且 degraded === true', () => {
    const r = unwrapReportRollupSummary(degraded())
    expect(reportRollupIsDegraded(r)).toBe(true)
    expect(reportRollupHasData(r)).toBe(false)
  })

  it('★ ★ 降级响应里**没有** report 主键（这才是解包器必须接受的形状）', () => {
    const r = unwrapReportRollupSummary(degraded())
    expect('report' in (r as unknown as Record<string, unknown>)).toBe(false)
  })

  it('★ dimensions 降级 ⇒ 同样走降级分支，主键 dimensions 也消失', () => {
    const r = unwrapReportRollupDimensions(degraded())
    expect(reportRollupIsDegraded(r)).toBe(true)
    expect('dimensions' in (r as unknown as Record<string, unknown>)).toBe(false)
  })

  it('★ 降级缺 error_code ⇒ 抛（收紧到降级那一步的措辞）', () => {
    const d = del({ degraded: true, error_code: 'X' } as unknown as Record<string, unknown>, 'error_code')
    expect(() => unwrapReportRollupSummary({ degraded: true })).toThrow(/对账汇总（降级） 缺 1 个键（error_code）/)
    expect(() => unwrapReportRollupSummary(d)).toThrow(/对账汇总（降级） 缺 1 个键（error_code）/)
  })

  it('★ dimensions 降级缺 error_code ⇒ 抛（同一个缺陷在两个 handler 都在）', () => {
    expect(() => unwrapReportRollupDimensions({ degraded: true })).toThrow(
      /对账候选（降级） 缺 1 个键（error_code）/,
    )
  })

  it('★ ★ 缺 degraded 键 ⇒ **不算降级**，改走「缺主键」分支', () => {
    expect(() => unwrapReportRollupSummary({ error_code: 'REPORT_SNAPSHOTS_NOT_MIGRATED' })).toThrow(
      /对账汇总 缺 1 个键（report）/,
    )
  })

  it('★ degraded 为 false ⇒ 不算降级（后端只发字面量 true，但判据要能区分）', () => {
    expect(() => unwrapReportRollupSummary({ degraded: false })).toThrow(/对账汇总 缺 1 个键（report）/)
  })

  it('★ 降级但 error_code 不符 ⇒ 抛并报实得值', () => {
    expect(() => unwrapReportRollupSummary({ degraded: true, error_code: 'OTHER_CODE' })).toThrow(
      /error_code 不是 REPORT_SNAPSHOTS_NOT_MIGRATED（实得 OTHER_CODE）/,
    )
  })

  it('★ dimensions 降级但 error_code 不符 ⇒ 抛（两个 handler 各有一条）', () => {
    expect(() => unwrapReportRollupDimensions({ degraded: true, error_code: 'OTHER_CODE' })).toThrow(
      /对账候选 的 error_code 不是 REPORT_SNAPSHOTS_NOT_MIGRATED（实得 OTHER_CODE）/,
    )
  })

  it('★ reportRollupDegradedCode 降级时返码', () => {
    expect(reportRollupDegradedCode(degraded())).toBe('REPORT_SNAPSHOTS_NOT_MIGRATED')
  })

  it('★ ★ reportRollupDegradedCode 非降级时返 undefined（反向）', () => {
    const ok = unwrapReportRollupSummary(summary())
    expect(reportRollupDegradedCode(ok)).toBeUndefined()
  })

  it('★ reportRollupHasData 非降级时为 true（反向）', () => {
    const ok = unwrapReportRollupSummary(summary())
    expect(reportRollupHasData(ok)).toBe(true)
    expect(reportRollupIsDegraded(ok)).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 形状不符（不是对象）
// ══════════════════════════════════════════════════════════════════════════

describe('响应不是对象', () => {
  it('★ 顶层 null ⇒ 抛并报 null', () => {
    expect(() => unwrapReportRollupSummary(null)).toThrow(/对账汇总 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ ★ 顶层数组 ⇒ 抛并报 array（不是 null）', () => {
    expect(() => unwrapReportRollupSummary([])).toThrow(/对账汇总 响应形状不符：期望裸对象，实得 array/)
  })

  it('★ 顶层字符串 ⇒ 抛并报 string', () => {
    expect(() => unwrapReportRollupSummary('nope')).toThrow(/实得 string/)
  })

  it('★ 顶层数字 ⇒ 抛并报 number', () => {
    expect(() => unwrapReportRollupSummary(7)).toThrow(/实得 number/)
  })

  it('★ dimensions 顶层 null ⇒ 抛（文案用「对账候选」）', () => {
    expect(() => unwrapReportRollupDimensions(null)).toThrow(/对账候选 响应形状不符：期望裸对象，实得 null/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5)(6)(7) report 内部形状
// ══════════════════════════════════════════════════════════════════════════

describe('(7) report 主体校验', () => {
  it('★ 缺 report 主键 ⇒ 抛（收紧到 1 个键）', () => {
    expect(() => unwrapReportRollupSummary({ nope: 1 })).toThrow(/对账汇总 缺 1 个键（report）/)
  })

  it('★ report 不是对象 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary({ report: 'x' })).toThrow(
      /对账汇总 的 report 响应形状不符：期望裸对象，实得 string/,
    )
  })

  it('★ report 缺一个恒在键 ⇒ 抛并点名该键', () => {
    const g = del(grain() as unknown as Record<string, unknown>, 'snapshot_dates')
    expect(() => unwrapReportRollupSummary({ report: g })).toThrow(
      /对账汇总 的 report 缺 1 个键（snapshot_dates）/,
    )
  })

  it('★ ★ report 缺三个恒在键 ⇒ 抛并报数量与键名顺序', () => {
    let g = grain() as unknown as Record<string, unknown>
    g = del(g, 'start')
    g = del(g, 'source')
    g = del(g, 'days')
    expect(() => unwrapReportRollupSummary({ report: g })).toThrow(
      /对账汇总 的 report 缺 3 个键（start, days, source）/,
    )
  })

  it('★ view 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ view: 1 as unknown as string }))).toThrow(
      /对账汇总 的 report 的 view 不是字符串/,
    )
  })

  it('★ ★ view 不在四值枚举内 ⇒ 抛并报实得值', () => {
    expect(() => unwrapReportRollupSummary(summary({ view: 'Provider2' }))).toThrow(
      /对账汇总 的 report 的 view 不是已知视角（Provider2）/,
    )
  })

  it('★ view = internal 通过', () => {
    expect(() => unwrapReportRollupSummary(summary({ view: 'internal' }))).not.toThrow()
  })

  it('★ view = credential 通过', () => {
    expect(() => unwrapReportRollupSummary(summary({ view: 'credential' }))).not.toThrow()
  })

  it('★ view = key 通过', () => {
    expect(() => unwrapReportRollupSummary(summary({ view: 'key' }))).not.toThrow()
  })

  it('★ source 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ source: 2 as unknown as string }))).toThrow(
      /的 source 不是字符串/,
    )
  })

  it('★ ★ source 不在三值枚举内 ⇒ 抛并报实得值', () => {
    expect(() => unwrapReportRollupSummary(summary({ source: 'grain_v2' }))).toThrow(
      /的 source 不是已知来源（grain_v2）/,
    )
  })

  it('★ start 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ start: null as unknown as string }))).toThrow(
      /的 start 不是字符串/,
    )
  })

  it('★ end 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ end: [] as unknown as string }))).toThrow(
      /的 end 不是字符串/,
    )
  })
})

describe('(12)(13) totals 校验', () => {
  it('★ totals 不是对象 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ totals: [] as unknown as ReportTotals }))).toThrow(
      /的 totals 响应形状不符：期望裸对象，实得 array/,
    )
  })

  it('★ totals 缺一个恒在键 ⇒ 抛并点名', () => {
    const t = del(totals() as unknown as Record<string, unknown>, 'credits_charged')
    expect(() => unwrapReportRollupSummary(summary({ totals: t as unknown as ReportTotals }))).toThrow(
      /的 totals 缺 1 个键（credits_charged）/,
    )
  })

  it('★ ★ request_count 是字符串 ⇒ 抛「不是数字」', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ totals: totals({ request_count: '100' as unknown as number }) })),
    ).toThrow(/的 totals 的 request_count 不是数字/)
  })

  it('★ ★ error_rate 不是数字 ⇒ 抛（它是第二组，与计数组分开校验）', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ totals: totals({ error_rate: '0.03' as unknown as number }) })),
    ).toThrow(/的 totals 的 error_rate 不是数字/)
  })

  it('★ currency 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ totals: totals({ currency: 1 as unknown as string }) }))).toThrow(
      /的 totals 的 currency 不是字符串/,
    )
  })

  it('★ ★ cache_hit_ratio 为 null ⇒ **放行**（分母 0 的正常形态）', () => {
    expect(() => unwrapReportRollupSummary(summary({ totals: totals({ cache_hit_ratio: null }) }))).not.toThrow()
  })

  it('★ ★ cache_hit_ratio 为数字 ⇒ 放行', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ totals: totals({ cache_hit_ratio: 0.0476 }) })),
    ).not.toThrow()
  })

  it('★ ★★ cache_hit_ratio 为 0 ⇒ 放行且**不是 null**（0 与 null 是两回事）', () => {
    const t = totals({ cache_hit_ratio: 0 })
    expect(() => unwrapReportRollupSummary(summary({ totals: t }))).not.toThrow()
    expect(reportTotalsCacheHitRatioIsNull(t)).toBe(false)
    expect(reportTotalsCacheHitRatioIsZero(t)).toBe(true)
  })

  it('★ cache_hit_ratio 为字符串 ⇒ 抛（收紧到它自己那一步）', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ totals: totals({ cache_hit_ratio: '0' as unknown as number | null }) })),
    ).toThrow(/的 totals 的 cache_hit_ratio 不是数字也不是 null/)
  })

  it('★ internal_currency 存在且是字符串 ⇒ 放行（internal 视角的正常形态）', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ totals: totals({ internal_currency: 'CREDIT' }) })),
    ).not.toThrow()
  })

  it('★ ★ internal_currency 是数字 ⇒ 抛（它是**条件键**，键在但类型错）', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ totals: totals({ internal_currency: 1 as unknown as string }) })),
    ).toThrow(/的 totals 的 internal_currency 不是字符串/)
  })

  it('★ ★ internal_currency 缺键 ⇒ 放行（非 internal 视角）', () => {
    const t = del(totals() as unknown as Record<string, unknown>, 'internal_currency')
    expect(() => unwrapReportRollupSummary(summary({ totals: t as unknown as ReportTotals }))).not.toThrow()
  })

  it('★ reportTotalsErrorCountMatches 等式成立 ⇒ true', () => {
    expect(reportTotalsErrorCountMatches(totals({ request_count: 100, success_count: 97, error_count: 3 }))).toBe(true)
  })

  it('★ ★ reportTotalsErrorCountMatches 等式不成立 ⇒ false', () => {
    expect(reportTotalsErrorCountMatches(totals({ request_count: 100, success_count: 97, error_count: 9 }))).toBe(false)
  })

  it('★ ★★★ error_count 小于正确值 ⇒ false（专打 `<=` 那条变异）', () => {
    // ★ 样本要挑「改成 <= 会翻」的那一格：错误计数**偏小**，
    //   100-97=3 而 error_count=1 ⇒ 原判据 false、`<=` 变 true。
    expect(reportTotalsErrorCountMatches(totals({ request_count: 100, success_count: 97, error_count: 1 }))).toBe(false)
  })

  it('★ ★★ rate_limited 计失败：0 成功 5 请求 ⇒ error_count 必须是 5', () => {
    expect(reportTotalsErrorCountMatches(totals({ request_count: 5, success_count: 0, error_count: 5 }))).toBe(true)
  })
})

describe('(11) error_breakdown 的两种 nil 编码', () => {
  it('★ report.error_breakdown 为 null ⇒ 放行（父行无 omitempty）', () => {
    expect(() => unwrapReportRollupSummary(summary({ error_breakdown: null }))).not.toThrow()
  })

  it('★ report.error_breakdown 为空对象 ⇒ 放行（空 map 也是可达形态）', () => {
    expect(() => unwrapReportRollupSummary(summary({ error_breakdown: {} }))).not.toThrow()
  })

  it('★ report.error_breakdown 有值 ⇒ 放行', () => {
    expect(() => unwrapReportRollupSummary(summary({ error_breakdown: { timeout: 3 } }))).not.toThrow()
  })

  it('★ error_breakdown 的值不是数字 ⇒ 抛并点名那个键', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ error_breakdown: { timeout: '3' as unknown as number } })),
    ).toThrow(/的 error_breakdown.timeout 不是数字/)
  })

  it('★ error_breakdown 是数组 ⇒ 抛（不是对象也不是 null）', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ error_breakdown: [] as unknown as Record<string, number> })),
    ).toThrow(/的 error_breakdown 不是对象也不是 null/)
  })

  it('★ reportBreakdownIsNullOnParentRow 父行为 null ⇒ true', () => {
    expect(reportBreakdownIsNullOnParentRow({ error_breakdown: null })).toBe(true)
  })

  it('★ ★ reportBreakdownIsNullOnParentRow 父行有值 ⇒ false', () => {
    expect(reportBreakdownIsNullOnParentRow({ error_breakdown: { timeout: 3 } })).toBe(false)
  })

  it('★ ★★ reportBreakdownIsAbsentOnDailyRow 按天行键缺 ⇒ true（用 delete 造）', () => {
    const row = del({ date: '2026-10-07', totals: totals() }, 'error_breakdown')
    expect('error_breakdown' in row).toBe(false)
    expect(reportBreakdownIsAbsentOnDailyRow(row)).toBe(true)
  })

  it('★ ★★ reportBreakdownIsAbsentOnDailyRow 按天行值是 null ⇒ false（★ 对照）', () => {
    // ★ 陷阱：父行 null 与按天行的 null **不是同一语义**；按天行本该是键缺。
    expect(reportBreakdownIsAbsentOnDailyRow({ error_breakdown: null })).toBe(false)
  })

  it('★ ★★ reportBreakdownIsNullOnParentRow 按天行键缺 ⇒ false（两个判据互斥）', () => {
    const row = del({ date: '2026-10-07' }, 'error_breakdown')
    expect(reportBreakdownIsNullOnParentRow(row)).toBe(false)
  })

  it('★ daily_models 条件键为数组 ⇒ 放行', () => {
    expect(() => unwrapReportRollupSummary(summary({ daily_models: [{ date: '2026-10-07' }] }))).not.toThrow()
  })

  it('★ daily_providers 条件键为空数组 ⇒ 放行（空 slice ⇒ [] 不是 null）', () => {
    expect(() => unwrapReportRollupSummary(summary({ daily_providers: [] }))).not.toThrow()
  })

  it('★ top_error_count 条件键为数字 ⇒ 放行', () => {
    expect(() => unwrapReportRollupSummary(summary({ top_error_count: 3 }))).not.toThrow()
  })

  it('★ ★ 条件键类型不对（top_error_count 是对象）⇒ 抛', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ top_error_count: { n: 1 } as unknown as number })),
    ).toThrow(/的 top_error_count 类型不对/)
  })

  it('★ 条件键 top_error_kind 为字符串 ⇒ 放行', () => {
    expect(() => unwrapReportRollupSummary(summary({ top_error_kind: 'timeout' }))).not.toThrow()
  })

  it('★ 条件键全缺 ⇒ 放行（汇总缺省就是没有 daily_*）', () => {
    expect(() => unwrapReportRollupSummary(summary())).not.toThrow()
  })
})

describe('顶层数组与 coverage', () => {
  it('★ days 不是数组 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ days: {} as unknown as unknown[] }))).toThrow(
      /的 days 不是数组/,
    )
  })

  it('★ models 不是数组 ⇒ 抛', () => {
    expect(() => unwrapReportRollupSummary(summary({ models: null as unknown as unknown[] }))).toThrow(
      /的 models 不是数组/,
    )
  })

  it('★ snapshot_dates 不是数组 ⇒ 抛', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ snapshot_dates: 'x' as unknown as unknown[] })),
    ).toThrow(/的 snapshot_dates 不是数组/)
  })

  it('★ coverage 不是对象 ⇒ 抛', () => {
    expect(() =>
      unwrapReportRollupSummary(summary({ coverage: [] as unknown as { grain_dates?: string[] } })),
    ).toThrow(/的 coverage 响应形状不符：期望裸对象，实得 array/)
  })

  it('★ ★ coverage 两个键都可以是裸 null ⇒ 放行（nil slice 无 omitempty）', () => {
    expect(() =>
      unwrapReportRollupSummary(
        summary({ coverage: { grain_dates: null, legacy_dates: null }, source: 'grain' }),
      ),
    ).not.toThrow()
  })

  it('★ reportRollupFilterIsNeverEchoed 正常响应 ⇒ true（filter 标了 json:"-"）', () => {
    const r = unwrapReportRollupSummary(summary())
    expect(reportRollupFilterIsNeverEchoed(r)).toBe(true)
  })

  it('★ ★ reportRollupFilterIsNeverEchoed 注入 filter 键 ⇒ false', () => {
    const g = { ...(grain() as unknown as Record<string, unknown>), filter: { provider_id: 7 } }
    const r = unwrapReportRollupSummary({ report: g }) as ReportRollupSummaryResponse
    expect(reportRollupFilterIsNeverEchoed(r)).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (15) dimensions 解包
// ══════════════════════════════════════════════════════════════════════════

describe('(15) dimensions 解包', () => {
  it('★ 缺 dimensions 主键 ⇒ 抛（收紧到 1 个键）', () => {
    expect(() => unwrapReportRollupDimensions({ nope: 1 })).toThrow(/对账候选 缺 1 个键（dimensions）/)
  })

  it('★ dimensions 不是对象 ⇒ 抛', () => {
    expect(() => unwrapReportRollupDimensions({ dimensions: 'x' })).toThrow(
      /对账候选 的 dimensions 响应形状不符：期望裸对象，实得 string/,
    )
  })

  it('★ ★ dimensions 缺 persons ⇒ 抛并点名（六键都无 omitempty）', () => {
    const d = del(dims(), 'persons')
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(
      /对账候选 的 dimensions 缺 1 个键（persons）/,
    )
  })

  it('★ dimensions 六键全在 ⇒ 放行', () => {
    expect(() => unwrapReportRollupDimensions({ dimensions: dims() })).not.toThrow()
  })

  it('★ 某维不是数组 ⇒ 抛', () => {
    const d = dims({ providers: {} })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(/dimensions 的 providers 不是数组/)
  })

  it('★ 空数组维度 ⇒ 放行（区间内该维度没出现过）', () => {
    const d = dims({ models: [] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).not.toThrow()
  })

  it('★ option 缺 key ⇒ 抛并点名下标', () => {
    const d = dims({ providers: [del(option() as unknown as Record<string, unknown>, 'key')] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(
      /dimensions 的 providers\[0\] 缺 1 个键（key）/,
    )
  })

  it('★ option 缺 requests ⇒ 抛', () => {
    const d = dims({ models: [del(option() as unknown as Record<string, unknown>, 'requests')] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(
      /dimensions 的 models\[0\] 缺 1 个键（requests）/,
    )
  })

  it('★ option 的 key 不是字符串 ⇒ 抛（**候选键统一成字符串**，数值 id 也一样）', () => {
    const d = dims({ credentials: [{ key: 3, requests: 20 }] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(
      /dimensions 的 credentials\[0\] 的 key 不是字符串/,
    )
  })

  it('★ option 的 requests 不是数字 ⇒ 抛', () => {
    const d = dims({ tenants: [{ key: 't-1', requests: '11' }] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(
      /dimensions 的 tenants\[0\] 的 requests 不是数字/,
    )
  })

  it('★ ★ name 存在且是字符串 ⇒ 放行', () => {
    const d = dims({ api_keys: [option({ key: 'sk-abc', requests: 9, name: 'key-alias' })] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).not.toThrow()
  })

  it('★ ★★ name 缺键 ⇒ 放行（omitempty 条件键；models/tenants/persons **恒缺**）', () => {
    const d = dims({ models: [option({ key: 'gpt-4o', requests: 30 })] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).not.toThrow()
    expect(reportDimensionHasName({ key: 'gpt-4o', requests: 30 })).toBe(false)
  })

  it('★ ★ name 存在但不是字符串 ⇒ 抛', () => {
    const d = dims({ providers: [option({ key: '7', requests: 42, name: 9 as unknown as string })] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(
      /dimensions 的 providers\[0\] 的 name 不是字符串/,
    )
  })

  it('★ 第二项 option 出错时点名下标 1', () => {
    const d = dims({ persons: [option(), { key: 'bob', requests: 'x' }] })
    expect(() => unwrapReportRollupDimensions({ dimensions: d })).toThrow(
      /dimensions 的 persons\[1\] 的 requests 不是数字/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5)(6) 区间
// ══════════════════════════════════════════════════════════════════════════

describe('(5)(6) 区间判据', () => {
  it('★ 缺省窗口不含今日 ⇒ true', () => {
    const today = new Date('2026-10-08T09:00:00Z')
    expect(reportRollupDefaultWindowExcludesToday(today, '2026-10-07', '2026-10-01')).toBe(true)
  })

  it('★ ★ start 等于今日 ⇒ false（T+1 语义下今日快照不存在）', () => {
    const today = new Date('2026-10-08T09:00:00Z')
    expect(reportRollupDefaultWindowExcludesToday(today, '2026-10-07', '2026-10-08')).toBe(false)
  })

  it('★ yesterday 不早于今日 ⇒ false（那不是「昨日」）', () => {
    const today = new Date('2026-10-08T09:00:00Z')
    expect(reportRollupDefaultWindowExcludesToday(today, '2026-10-08', '2026-10-01')).toBe(false)
  })

  it('★ ★★ start 晚于 yesterday ⇒ false（起点越过了终点；today 刻意再退一天让另两项为真）', () => {
    // ★ 样本必须挑「只有 start <= yesterday 触发、另两项都不触发」的那一格：
    //   start=10-08 > yesterday=10-07（B 假），而 start=10-08 < today=10-09、
    //   yesterday=10-07 < today=10-09 两项都真 ⇒ 去掉 B 就会翻成 true。
    const today = new Date('2026-10-09T09:00:00Z')
    expect(reportRollupDefaultWindowExcludesToday(today, '2026-10-07', '2026-10-08')).toBe(false)
  })

  it('★ ★★ 恰好 366 天 ⇒ 不超（判定是 `> 366`，不是 `>=`）', () => {
    expect(reportRollupRangeIsTooLong(366)).toBe(false)
  })

  it('★ ★ 367 天 ⇒ 超', () => {
    expect(reportRollupRangeIsTooLong(367)).toBe(true)
  })

  it('★ ★★ 冗余项已删：「start < today」恒被另两项蕴含（today 取昨日当天也不判真）', () => {
    // ★ 这一条把「第三个合取项是可证冗余」钉住：start == today 时
    //   `start <= yesterday` 已经为假 ⇒ 与显式写 `start < today` 同答案。
    const today = new Date('2026-10-08T00:00:00Z')
    expect(reportRollupDefaultWindowExcludesToday(today, '2026-10-07', '2026-10-08')).toBe(false)
  })

  it('★ reportRollupSingleDayIsValid start == end ⇒ true', () => {
    expect(reportRollupSingleDayIsValid()).toBe(true)
  })

  it('★ reportRollupDefaultWindowExcludesToday 跨月窗口（昨日是月初）⇒ true', () => {
    const today = new Date('2026-03-02T00:30:00Z')
    expect(reportRollupDefaultWindowExcludesToday(today, '2026-03-01', '2026-02-23')).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (7) view 规范化
// ══════════════════════════════════════════════════════════════════════════

describe('(7) view 规范化', () => {
  it('★ 大写 PROVIDER ⇒ 规范化为小写（后端 ToLower）', () => {
    expect(normalizeReportView('PROVIDER')).toBe('provider')
  })

  it('★ 带空格 " internal " ⇒ 去空格后小写', () => {
    expect(normalizeReportView(' internal ')).toBe('internal')
  })

  it('★ 空串 ⇒ 缺省 provider', () => {
    expect(normalizeReportView('')).toBe('provider')
  })

  it('★ ★ null ⇒ 缺省 provider', () => {
    expect(normalizeReportView(null)).toBe('provider')
  })

  it('★ ★ undefined ⇒ 缺省 provider', () => {
    expect(normalizeReportView(undefined)).toBe('provider')
  })

  it('★ 纯空格 ⇒ 缺省 provider（TrimSpace 后为空）', () => {
    expect(normalizeReportView('   ')).toBe('provider')
  })

  it('★ reportRollupViewIsKnown 四个已知视角各为 true', () => {
    expect(reportRollupViewIsKnown('provider')).toBe(true)
    expect(reportRollupViewIsKnown('internal')).toBe(true)
    expect(reportRollupViewIsKnown('credential')).toBe(true)
    expect(reportRollupViewIsKnown('key')).toBe(true)
  })

  it('★ ★ reportRollupViewIsKnown 未知视角为 false', () => {
    expect(reportRollupViewIsKnown('tenant')).toBe(false)
  })

  it('★ ★★ 规范化后的值可被校验（这才是能严格校验的前提）', () => {
    expect(reportRollupViewIsKnown(normalizeReportView('  CREDENTIAL '))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (9) detail 的缺省相反
// ══════════════════════════════════════════════════════════════════════════

describe('(9) detail 双缺省', () => {
  it('★ summary 侧 detail=daily ⇒ 真', () => {
    expect(reportDetailIsOn('daily')).toBe(true)
  })

  it('★ summary 侧 detail=1 ⇒ 真', () => {
    expect(reportDetailIsOn('1')).toBe(true)
  })

  it('★ summary 侧 detail=TRUE ⇒ 真（大小写不敏感）', () => {
    expect(reportDetailIsOn('TRUE')).toBe(true)
  })

  it('★ summary 侧 detail=" yes " ⇒ 真（TrimSpace + 大小写）', () => {
    expect(reportDetailIsOn(' yes ')).toBe(true)
  })

  it('★ ★ summary 侧 detail=xyz ⇒ **假**（不在那四个字面量里）', () => {
    expect(reportDetailIsOn('xyz')).toBe(false)
  })

  it('★ summary 侧 detail=0 ⇒ 假', () => {
    expect(reportDetailIsOn('0')).toBe(false)
  })

  it('★ summary 侧 detail 缺省（undefined）⇒ 假', () => {
    expect(reportDetailIsOn(undefined)).toBe(false)
  })

  it('★ ★ export 侧 detail=0 ⇒ 假', () => {
    expect(reportExportDetailIsOff('0')).toBe(true)
  })

  it('★ export 侧 detail=false ⇒ 假', () => {
    expect(reportExportDetailIsOff('false')).toBe(true)
  })

  it('★ export 侧 detail=NO ⇒ 假（大小写不敏感）', () => {
    expect(reportExportDetailIsOff('NO')).toBe(true)
  })

  it('★ export 侧 detail=summary ⇒ 假', () => {
    expect(reportExportDetailIsOff('summary')).toBe(true)
  })

  it('★ ★★ 陷阱对照：detail=xyz 在 summary 侧是假、在 export 侧**不是假**（= 真）', () => {
    expect(reportDetailIsOn('xyz')).toBe(false)
    expect(reportExportDetailIsOff('xyz')).toBe(false)
  })

  it('★ ★★ 陷阱对照：detail=daily 在两侧判定相反', () => {
    expect(reportDetailIsOn('daily')).toBe(true)
    expect(reportExportDetailIsOff('daily')).toBe(false)
  })

  it('★ ★★ 两组字面量互补：summary 的真值集 ∩ export 的假值集 = 空', () => {
    for (const t of REPORT_ROLLUP_DETAIL_ON_TOKENS) {
      expect((REPORT_ROLLUP_EXPORT_OFF_TOKENS as readonly string[]).includes(t)).toBe(false)
    }
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (14)(14b) source ⇔ coverage —— 本族最锐利的可自验不变式
// ══════════════════════════════════════════════════════════════════════════

describe('(14) source 由 coverage 派生', () => {
  it('★ 两数组都有 ⇒ 派生为 mixed', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-06'], legacy_dates: ['2026-10-07'] } })
    expect(reportCoverageDeriveSource(r)).toBe('mixed')
  })

  it('★ ★ 只有 legacy 非空 ⇒ 派生为 legacy', () => {
    const r = grain({ coverage: { grain_dates: [], legacy_dates: ['2026-10-07'] } })
    expect(reportCoverageDeriveSource(r)).toBe('legacy')
  })

  it('★ ★ 只有 grain 非空 ⇒ 派生为 grain', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-07'], legacy_dates: [] } })
    expect(reportCoverageDeriveSource(r)).toBe('grain')
  })

  it('★ ★★ 两数组**都空** ⇒ 仍派生为 grain（后端 default 分支）', () => {
    const r = grain({ coverage: { grain_dates: [], legacy_dates: [] } })
    expect(reportCoverageDeriveSource(r)).toBe('grain')
  })

  it('★ ★★ 两数组都是裸 null ⇒ 按长度 0 算 ⇒ 派生为 grain', () => {
    const r = grain({ coverage: { grain_dates: null, legacy_dates: null } })
    expect(reportCoverageDeriveSource(r)).toBe('grain')
  })

  it('★ source=mixed 且 coverage 自洽 ⇒ matches 为 true', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-06'], legacy_dates: ['2026-10-07'] }, source: 'mixed' })
    expect(reportRollupSourceMatchesCoverage(r)).toBe(true)
    expect(reportRollupIsMixed(r)).toBe(true)
  })

  it('★ source=legacy 且 coverage 自洽 ⇒ matches 为 true', () => {
    const r = grain({ coverage: { grain_dates: [], legacy_dates: ['2026-10-07'] }, source: 'legacy' })
    expect(reportRollupSourceMatchesCoverage(r)).toBe(true)
    expect(reportRollupIsLegacyOnly(r)).toBe(true)
  })

  it('★ ★★ source 与 coverage 不一致 ⇒ matches 为 false（自洽性校验，不是回显）', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-07'], legacy_dates: [] }, source: 'legacy' })
    expect(reportRollupSourceMatchesCoverage(r)).toBe(false)
  })

  it('★ ★ legacy ⇒ legacy_dates 必非空', () => {
    const r = grain({ coverage: { grain_dates: [], legacy_dates: ['2026-10-07'] }, source: 'legacy' })
    expect(reportLegacyOnlyHasLegacyDates(r)).toBe(true)
  })

  it('★ ★★ legacy_dates 为 null ⇒ HasLegacyDates 为 false', () => {
    const r = grain({ coverage: { grain_dates: null, legacy_dates: null } })
    expect(reportLegacyOnlyHasLegacyDates(r)).toBe(false)
  })

  it('★ ★★ legacy_dates 为**空数组** ⇒ HasLegacyDates 为 false（`!![]` 会误判成 true）', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-07'], legacy_dates: [] } })
    expect(reportLegacyOnlyHasLegacyDates(r)).toBe(false)
  })

  it('★ reportRollupSourceIsKnown 三值都为 true', () => {
    expect(reportRollupSourceIsKnown(grain({ source: 'grain' }))).toBe(true)
    expect(reportRollupSourceIsKnown(grain({ source: 'mixed' }))).toBe(true)
    expect(reportRollupSourceIsKnown(grain({ source: 'legacy' }))).toBe(true)
  })

  it('★ ★ reportRollupSourceIsKnown 第四个值为 false', () => {
    expect(reportRollupSourceIsKnown(grain({ source: 'partial' }))).toBe(false)
  })
})

describe('(14b) coverage 数组', () => {
  it('★ grain_dates 为裸 null ⇒ IsNull 为 true', () => {
    expect(reportCoverageGrainDatesIsNull(grain({ coverage: { grain_dates: null } }))).toBe(true)
  })

  it('★ ★ legacy_dates 为裸 null ⇒ IsNull 为 true（**另一个键**）', () => {
    expect(reportCoverageLegacyDatesIsNull(grain({ coverage: { legacy_dates: null } }))).toBe(true)
  })

  it('★ ★ grain_dates 非 null ⇒ IsNull 为 false（反向）', () => {
    expect(reportCoverageGrainDatesIsNull(grain({ coverage: { grain_dates: ['2026-10-07'] } }))).toBe(false)
  })

  it('★ 升序 ⇒ true（后端对两者各 sort.Strings）', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-05', '2026-10-06', '2026-10-07'], legacy_dates: [] } })
    expect(reportCoverageDatesAreAscending(r)).toBe(true)
  })

  it('★ ★ 降序 ⇒ false（只有一个键降序就足以打掉）', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-07', '2026-10-06'], legacy_dates: [] } })
    expect(reportCoverageDatesAreAscending(r)).toBe(false)
  })

  it('★ ★ legacy_dates 降序 ⇒ false（**另一个键**）', () => {
    const r = grain({ coverage: { grain_dates: [], legacy_dates: ['2026-10-07', '2026-10-01'] } })
    expect(reportCoverageDatesAreAscending(r)).toBe(false)
  })

  it('★ 非降序（含重复）⇒ true（后端没去重）', () => {
    const r = grain({ coverage: { grain_dates: ['2026-10-07', '2026-10-07'], legacy_dates: [] } })
    expect(reportCoverageDatesAreAscending(r)).toBe(true)
  })

  it('★ null 数组 ⇒ 单调为 true（长度 0）', () => {
    expect(reportCoverageDatesAreAscending(grain({ coverage: { grain_dates: null, legacy_dates: null } }))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (15a) 回显是 RFC3339Nano
// ══════════════════════════════════════════════════════════════════════════

describe('(15a) start/end 回显形状', () => {
  it('★ 回显带 T ⇒ 不是纯日期', () => {
    expect(reportStartIsRfc3339NotDateOnly('2026-10-01T00:00:00Z')).toBe(true)
    expect(reportStartIsDateOnly('2026-10-01T00:00:00Z')).toBe(false)
  })

  it('★ ★ 带小数秒（Nano）也是 RFC3339', () => {
    expect(reportStartIsRfc3339NotDateOnly('2026-10-01T00:00:00.123456789Z')).toBe(true)
  })

  it('★ ★ 带时区偏移也是 RFC3339', () => {
    expect(reportStartIsRfc3339NotDateOnly('2026-10-01T00:00:00+08:00')).toBe(true)
  })

  it('★ ★★ 10 位 YYYY-MM-DD ⇒ 判为纯日期（回显里不该出现这种形状）', () => {
    expect(reportStartIsDateOnly('2026-10-01')).toBe(true)
    expect(reportStartIsRfc3339NotDateOnly('2026-10-01')).toBe(false)
  })

  it('★ ★★★ 长度 >10 但不含 T ⇒ 判为纯日期（专打「只看长度」那条变异）', () => {
    // ★ 空格分隔的本地时间串：19 字符 > 10，却没有 `T` ⇒ 后端不会发这种形状，
    //   但它正是「只看长度」的判据唯一能区分的那一格。
    expect(reportStartIsRfc3339NotDateOnly('2026-10-01 00:00:00')).toBe(false)
    expect(reportStartIsDateOnly('2026-10-01 00:00:00')).toBe(true)
  })

  it('★ ★ 解包器放行 10 位 start（它只校验「是字符串」，不校验形状）', () => {
    expect(() => unwrapReportRollupSummary(summary({ start: '2026-10-01' }))).not.toThrow()
    expect(reportStartIsDateOnly('2026-10-01')).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (15) 候选判据
// ══════════════════════════════════════════════════════════════════════════

describe('(15) 候选判据', () => {
  it('★ name 在 ⇒ HasName 为 true', () => {
    expect(reportDimensionHasName({ key: '7', requests: 42, name: 'openai' })).toBe(true)
  })

  it('★ ★ name 缺 ⇒ HasName 为 false', () => {
    expect(reportDimensionHasName({ key: '7', requests: 42 })).toBe(false)
  })

  it('★ ★★★ name 键在但值是空串 ⇒ HasName 为 true（真值判断会误判成 false）', () => {
    // ★ omitempty 的空值是**键缺**，但手写夹具能造出「键在 + 空串」这一格
    //   ⇒ 正是「用真值判断替代 `in`」唯一能区分的那一格。
    expect(reportDimensionHasName({ key: '7', requests: 42, name: '' })).toBe(true)
  })

  it('★ ★★ 数值 id 的 key 仍是字符串，但「看起来像数字」', () => {
    expect(reportDimensionKeyLooksNumeric({ key: '7', requests: 42 })).toBe(true)
    expect(typeof { key: '7', requests: 42 }.key).toBe('string')
  })

  it('★ key 是模型名 ⇒ 不像数字', () => {
    expect(reportDimensionKeyLooksNumeric({ key: 'gpt-4o', requests: 30 })).toBe(false)
  })

  it('★ ★ key 是负数 id ⇒ 像数字', () => {
    expect(reportDimensionKeyLooksNumeric({ key: '-3', requests: 1 })).toBe(true)
  })

  it('★ ★ key 是空串 ⇒ 不像数字（`^-?\d+$` 要求至少一位）', () => {
    expect(reportDimensionKeyLooksNumeric({ key: '', requests: 0 })).toBe(false)
  })

  it('★ ★ key 带小数点 ⇒ 不像数字（后端 ParseInt 会失败 ⇒ fill 直接 continue）', () => {
    expect(reportDimensionKeyLooksNumeric({ key: '7.0', requests: 1 })).toBe(false)
  })

  it('★ key 带前导空格 ⇒ 不像数字（TrimSpace 只发生在请求侧，响应侧没做）', () => {
    expect(reportDimensionKeyLooksNumeric({ key: ' 7', requests: 1 })).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 层：URL 拼装
// ══════════════════════════════════════════════════════════════════════════

describe('fetch URL 拼装', () => {
  it('★ 无参数 ⇒ summary 路径不带问号（后端前缀带尾斜杠，必须自己拼对）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary()
    expect(lastUrl()).toContain('/api/admin/report-rollup/summary')
    expect(lastUrl()).not.toContain('?')
  })

  it('★ 无参数 ⇒ dimensions 路径不带问号', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ dimensions: dims() }))
    await fetchReportRollupDimensions()
    expect(lastUrl()).toContain('/api/admin/report-rollup/dimensions')
    expect(lastUrl()).not.toContain('?')
  })

  it('★ start/end 按原样拼进查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ start: '2026-10-01', end: '2026-10-07' })
    expect(lastUrl()).toContain('start=2026-10-01')
    expect(lastUrl()).toContain('end=2026-10-07')
  })

  it('★ ★ 只给 start 时不补 end（缺省由后端算）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ start: '2026-10-01' })
    expect(lastUrl()).toContain('start=2026-10-01')
    expect(lastUrl()).not.toContain('end=')
  })

  it('★ view 客户端就规范化后发出（小写）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ view: ' INTERNAL ' })
    expect(lastUrl()).toContain('view=internal')
  })

  it('★ ★ provider_id 用 providerId 命名映射到下划线键', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ providerId: 7, credentialId: 3, apiKeyId: 9 })
    expect(lastUrl()).toContain('provider_id=7')
    expect(lastUrl()).toContain('credential_id=3')
    expect(lastUrl()).toContain('api_key_id=9')
  })

  it('★ 数字维度传字符串也接受（URLSearchParams 走 String()）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ providerId: '7' })
    expect(lastUrl()).toContain('provider_id=7')
  })

  it('★ ★ providerId = 0 ⇒ 仍然发出（`!= null` 对 0 为真；后端会 ParseInt 成 0 并设过滤）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ providerId: 0 })
    expect(lastUrl()).toContain('provider_id=0')
  })

  it('★ ★ providerId = undefined ⇒ 不发（真正「没给」）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ providerId: undefined })
    expect(lastUrl()).not.toContain('provider_id')
  })

  it('★ ★ providerId = 空串 ⇒ 发出空值，后端 TrimSpace 后为空 ⇒ 静默忽略该维度', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ providerId: '' })
    expect(lastUrl()).toContain('provider_id=')
    expect(lastUrl()).not.toContain('provider_id=undefined')
  })

  it('★ 文本维度 tenant/person/model 原样拼（不做大小写折叠）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ tenantId: 't-1', person: 'Alice', model: 'GPT-4o' })
    expect(lastUrl()).toContain('tenant_id=t-1')
    expect(lastUrl()).toContain('person=Alice')
    expect(lastUrl()).toContain('model=GPT-4o')
  })

  it('★ ★ 文本维度**不做**小写折叠（后端注释自陈：标识符不能折叠大小写）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ model: 'GPT-4o' })
    expect(lastUrl()).toContain('model=GPT-4o')
    expect(lastUrl()).not.toContain('model=gpt-4o')
  })

  it('★ detail 透传（不在客户端判定，由后端两套缺省分别处理）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ detail: 'daily' })
    expect(lastUrl()).toContain('detail=daily')
  })

  it('★ 全部参数齐发时顺序稳定（URLSearchParams 插入序）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    await fetchReportRollupSummary({ start: '2026-10-01', view: 'key', providerId: 7, detail: '1' })
    const q = lastUrl().split('?')[1]!
    expect(q.indexOf('start=')).toBeLessThan(q.indexOf('view='))
    expect(q.indexOf('view=')).toBeLessThan(q.indexOf('provider_id='))
    expect(q.indexOf('provider_id=')).toBeLessThan(q.indexOf('detail='))
  })
})

describe('fetch 端到端', () => {
  it('★ summary 满配响应被解包器放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(summary()))
    const r = await fetchReportRollupSummary()
    expect(reportRollupHasData(r)).toBe(true)
  })

  it('★ ★ summary 降级响应（200）也能解包', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(degraded(), 200))
    const r = await fetchReportRollupSummary()
    expect(reportRollupIsDegraded(r)).toBe(true)
  })

  it('★ dimensions 满配响应被解包器放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ dimensions: dims() }))
    const r = await fetchReportRollupDimensions()
    expect(reportRollupHasData(r)).toBe(true)
  })

  it('★ ★ dimensions 降级响应（200）也能解包', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(degraded(), 200))
    const r = await fetchReportRollupDimensions()
    expect(reportRollupIsDegraded(r)).toBe(true)
  })

  it('★ 形状不符 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: true }))
    await expect(fetchReportRollupSummary()).rejects.toThrow(/对账汇总 缺 1 个键（report）/)
  })

  it('★ ★ summary 与 dimensions 对同一个降级信封给出一致判定', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(degraded(), 200))
    const s = await fetchReportRollupSummary()
    fetchMock.mockResolvedValueOnce(jsonResponse(degraded(), 200))
    const d = await fetchReportRollupDimensions()
    expect(reportRollupIsDegraded(s)).toBe(reportRollupIsDegraded(d))
    expect(reportRollupDegradedCode(s)).toBe(reportRollupDegradedCode(d))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值（逐条对齐后端字面量，不用被测常量造夹具）
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ REPORT_ROLLUP_DEGRADED_CODE === report_rollup.go:319 的字面量', () => {
    expect(REPORT_ROLLUP_DEGRADED_CODE).toBe('REPORT_SNAPSHOTS_NOT_MIGRATED')
  })

  it('★ REPORT_ROLLUP_DB_UNAVAILABLE_MESSAGE === report_rollup.go:100 的字面量', () => {
    expect(REPORT_ROLLUP_DB_UNAVAILABLE_MESSAGE).toBe('database not available')
  })

  it('★ REPORT_ROLLUP_DB_UNAVAILABLE_MESSAGE **没有** is（批 79 那条有）', () => {
    expect(REPORT_ROLLUP_DB_UNAVAILABLE_MESSAGE).not.toContain('is not configured')
  })

  it('★ REPORT_ROLLUP_VIEWS 四值与 report.go:27-33 一致', () => {
    expect([...REPORT_ROLLUP_VIEWS]).toEqual(['provider', 'internal', 'credential', 'key'])
  })

  it('★ REPORT_ROLLUP_DEFAULT_VIEW === report_rollup.go:160 的缺省', () => {
    expect(REPORT_ROLLUP_DEFAULT_VIEW).toBe('provider')
  })

  it('★ REPORT_ROLLUP_SOURCES 三值与 grainreport.go:946-950 一致', () => {
    expect([...REPORT_ROLLUP_SOURCES]).toEqual(['grain', 'mixed', 'legacy'])
  })

  it('★ REPORT_ROLLUP_MAX_RANGE_DAYS === report_rollup.go:144 的 366', () => {
    expect(REPORT_ROLLUP_MAX_RANGE_DAYS).toBe(366)
  })

  it('★ REPORT_ROLLUP_DEFAULT_WINDOW_DAYS === report_rollup.go:125 的 7 天闭区间', () => {
    expect(REPORT_ROLLUP_DEFAULT_WINDOW_DAYS).toBe(7)
  })

  it('★ REPORT_ROLLUP_DETAIL_ON_TOKENS 四值与 reportDailyDetail 的 case 列表一致', () => {
    expect([...REPORT_ROLLUP_DETAIL_ON_TOKENS]).toEqual(['1', 'true', 'yes', 'daily'])
  })

  it('★ REPORT_ROLLUP_EXPORT_OFF_TOKENS 四值与 reportExportDetail 的 case 列表一致', () => {
    expect([...REPORT_ROLLUP_EXPORT_OFF_TOKENS]).toEqual(['0', 'false', 'no', 'summary'])
  })

  it('★ REPORT_ROLLUP_DEGRADED_KEYS 就是降级信封的两键', () => {
    expect([...REPORT_ROLLUP_DEGRADED_KEYS]).toEqual(['degraded', 'error_code'])
  })

  it('★ REPORT_TOTALS_KEYS 恰是 16 个无 omitempty 的键', () => {
    expect(REPORT_TOTALS_KEYS.length).toBe(16)
  })

  it('★ ★ REPORT_TOTALS_KEYS **不含** internal_currency（它是条件键）', () => {
    expect(REPORT_TOTALS_KEYS as readonly string[]).not.toContain('internal_currency')
  })

  it('★ ★ REPORT_TOTALS_KEYS 含 cache_hit_ratio（它是裸 null 不是键缺）', () => {
    expect(REPORT_TOTALS_KEYS as readonly string[]).toContain('cache_hit_ratio')
  })

  it('★ REPORT_GRAIN_ALWAYS_KEYS 恰是 10 个无 omitempty 的键', () => {
    expect(REPORT_GRAIN_ALWAYS_KEYS.length).toBe(10)
  })

  it('★ ★ 两组 GrainReport 键**互不重叠**', () => {
    const always = REPORT_GRAIN_ALWAYS_KEYS as readonly string[]
    const opt = REPORT_GRAIN_OPTIONAL_KEYS as readonly string[]
    expect(always.filter((k) => opt.includes(k))).toEqual([])
  })

  it('★ ★ 两组 GrainReport 键**合起来覆盖 grainreport.go:109-144 的 24 个键**', () => {
    expect(REPORT_GRAIN_ALWAYS_KEYS.length + REPORT_GRAIN_OPTIONAL_KEYS.length).toBe(24)
  })

  it('★ REPORT_GRAIN_OPTIONAL_KEYS 恰是 14 个 omitempty 的键', () => {
    expect(REPORT_GRAIN_OPTIONAL_KEYS.length).toBe(14)
  })

  it('★ ★ 恒在键里**不含** filter（它标了 json:"-"）', () => {
    expect(REPORT_GRAIN_ALWAYS_KEYS as readonly string[]).not.toContain('filter')
  })

  it('★ REPORT_DIMENSIONS_KEYS 六维与 dimensions.go:27-33 一致', () => {
    expect([...REPORT_DIMENSIONS_KEYS]).toEqual([
      'providers',
      'credentials',
      'api_keys',
      'models',
      'tenants',
      'persons',
    ])
  })
})