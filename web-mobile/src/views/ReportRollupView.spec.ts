import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ReportRollupView from './ReportRollupView.vue'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { setLocale } from '@/i18n'
import {
  fetchReportRollupSummary,
  fetchReportRollupDimensions,
  REPORT_ROLLUP_DEGRADED_CODE,
  REPORT_ROLLUP_VIEWS,
  REPORT_ROLLUP_DEFAULT_VIEW,
  REPORT_ROLLUP_MAX_RANGE_DAYS,
  reportTotalsErrorRateIsZeroWhenNoRequests,
  reportTotalsTotalTokensMatches,
  reportCoverageDeriveSource,
} from '@/api/reportRollup'

/**
 * 对账报表面回归护栏（2026-10-14，第一百一十一批）。
 *
 * ★★ 权限：**superAdmin 档**（`admin/handler.go:1086`
 *   `h.superAdmin(h.handleReportRollup)`，且**整个前缀带尾斜杠**都是这一档）
 *   ⇒ 抽屉席须设 `requiresRole: 'super_admin'`（同步 `AppDrawer.spec.ts` 白名单）。
 *   ⇒ 视图内部**不应有任何角色分支**（档位已挡住）。
 *
 * ⚠️⚠️ **命名陷阱**：既有 `reconciliation` 席走 `@/api/costReconciliation`（费用对账），
 *   本席走 `@/api/reportRollup`（对账报表）。两张席都叫「对账」，接错模块
 *   本文件与棘轮的判据**都不会红** ⇒ 靠 nav 标题与视图文件头的显式区分。
 *
 * 判据钉十二组不变量：
 *  ① ★★★★★ **「降级」是 200 + 一个两键信封，主键整个消失**
 *      （`admin/report_rollup.go:317-319`）⇒ 两个端点各自都必须按**分形状**渲染，
 *      不能当成「加载失败」。
 *  ② ★★★★★ `source` **不是独立字段，而是从 `coverage` 两个数组派生**
 *      （`grainreport.go:945-951`）⇒ 不一致就是违例。
 *      ★★ 推论：`source === "grain"` **不蕴含** `grain_dates` 非空（两个都空也走 default）。
 *  ③ ★★★★ 四条**可自算的派生列**（`report.go:210-231` 的 `finalize()`）：
 *      · `error_count === request_count − success_count`（无条件）
 *      · `total_tokens === input + output + cache_read + cache_write`（★ 把 cache 也算进去）
 *      · ★★ `request_count === 0` ⇒ `error_rate` 是 **0**，不是 `null`、不是 `NaN`
 *      · `cache_hit_ratio` 分母为 0 ⇒ **裸 `null`**（指针无 omitempty，键恒在）
 *  ④ ★★★ 每条告警判据都**成对**：`xxx ⇒ 报` + `yyy ⇒ 不报`，且 `yyy` 取**正常业务取值**。
 *  ⑤ ★★★ `coverage` 的两个数组是**三态**：键缺失 / 裸 `null` / 有值。
 *  ⑥ ★★ `start` / `end` 回显的是 **RFC3339Nano**，不是发进去的 `YYYY-MM-DD`。
 *  ⑦ ★★ `filter` 标着 `json:"-"` ⇒ 六个维度过滤**一个都不回显**。
 *  ⑧ ★★★ 区间是 **T+1** 语义，闭区间且**不含今日** ⇒ 页面不该提供「今天」。
 *  ⑨ ★★★★ dimensions 的 `name` 缺失有**两种成因**：`fill()` 只回填三个维度，
 *      `models`/`tenants`/`persons` **恒缺**（不是「回查失败」）。
 *  ⑩ ★ 维度候选的 `key` **统一是字符串**（即便语义上是数值 id）。
 *  ⑪ ★★ superAdmin 档 ⇒ 视图内部无角色分支（结构层 + 行为层）。
 *  ⑫ ★ 新增触控控件 ≥48 CSS px。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function totals(over: Record<string, unknown> = {}) {
  const input = 100
  const output = 200
  const cacheRead = 300
  const cacheWrite = 400
  const requestCount = 10
  const successCount = 7
  return {
    request_count: requestCount,
    success_count: successCount,
    // ★ 无条件派生：error_count = request − success
    error_count: requestCount - successCount,
    error_rate: (requestCount - successCount) / requestCount,
    input_tokens: input,
    output_tokens: output,
    cache_read_tokens: cacheRead,
    cache_write_tokens: cacheWrite,
    // ★ total 把 cache 也算进去
    total_tokens: input + output + cacheRead + cacheWrite,
    estimated_cost_cents: 1234,
    currency: 'USD',
    credits_charged: 7,
    internal_cost_cents: 0,
    // ★★ 分母 = input + cacheRead = 400 > 0 ⇒ 这里给真实值
    cache_hit_ratio: cacheRead / (input + cacheRead),
    latency_p50_ms: 100,
    latency_p95_ms: 250,
    ...over,
  }
}

function report(over: Record<string, unknown> = {}) {
  return {
    // ★ 回显是 RFC3339Nano，不是 YYYY-MM-DD
    start: '2026-10-01T00:00:00Z',
    end: '2026-10-07T00:00:00Z',
    view: REPORT_ROLLUP_DEFAULT_VIEW,
    totals: totals(),
    error_breakdown: null,
    days: [],
    models: [],
    snapshot_dates: [],
    coverage: { grain_dates: ['2026-10-01'], legacy_dates: null },
    // ★ 与 coverage 一致 ⇒ 派生为 legacy（grain_dates 非空 + legacy_dates 空 ⇒ 走第二分支）
    source: 'grain',
    ...over,
  }
}

function summaryPayload(over: Record<string, unknown> = {}) {
  return { report: report(over) }
}

function dimPayload(over: Record<string, unknown> = {}) {
  return {
    dimensions: {
      providers: [],
      credentials: [],
      api_keys: [],
      models: [],
      tenants: [],
      persons: [],
      ...over,
    },
  }
}

/** 降级信封：**只有**两键，主键整个消失。 */
function degradedPayload() {
  return { degraded: true, error_code: REPORT_ROLLUP_DEGRADED_CODE }
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin', userInfo: { role: 'super_admin', tenant_id: 't-1' } }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/reportRollup', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/reportRollup')>()
  return { ...actual, fetchReportRollupSummary: vi.fn(), fetchReportRollupDimensions: vi.fn() }
})

const mSum = fetchReportRollupSummary as unknown as ReturnType<typeof vi.fn>
const mDims = fetchReportRollupDimensions as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(ReportRollupView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

/** ★★★ 取某一格文本 —— 负断言必须与被测对象**同格**。 */
function cell(w: W, id: string): string {
  return byId(w, id).text()
}

/**
 * 剥掉块注释与行注释。
 * ★ 判「视图有没有角色分支 / 有没有小触控目标」时，**文档不是行为**。
 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

function viewSource(): string {
  return readFileSync(join(process.cwd(), 'src/views/ReportRollupView.vue'), 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  authState.userInfo = { role: 'super_admin', tenant_id: 't-1' }
  mSum.mockResolvedValue(summaryPayload())
  mDims.mockResolvedValue(dimPayload())
})

// ── ① 降级是 200 + 两键信封 ───────────────────────────────────────────────

describe('① 降级信封', () => {
  it('①-1 ★★★★★ summary 降级 ⇒ 报「已降级」且**不渲染** report 字段', async () => {
    // ★★★ 降级是 **200**，不是错误 ⇒ 主键 `report` 整个消失。
    //   页面若把「没有 report」当成加载失败，就把这个契约讲成了另一个故事。
    mSum.mockResolvedValue(degradedPayload())
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-summary-degraded')).toBe(true)
    expect(cell(w, 'rr-summary-degraded')).toContain(REPORT_ROLLUP_DEGRADED_CODE)
    expect(hasId(w, 'rr-source')).toBe(false)
    expect(hasId(w, 'rr-t-request_count')).toBe(false)
  })

  it('①-2 ★★ summary 降级**不得**报「加载失败」（那是 200）', async () => {
    mSum.mockResolvedValue(degradedPayload())
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-summary-error')).toBe(false)
  })

  it('①-3 ★★★ dimensions 降级 ⇒ 独立形状，且与 summary 的降级互不影响', async () => {
    mSum.mockResolvedValue(summaryPayload())
    mDims.mockResolvedValue(degradedPayload())
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-dims-degraded')).toBe(true)
    // ★ summary 段仍是正常形状 ⇒ 两个端点的降级是**各自独立**的
    expect(hasId(w, 'rr-summary-degraded')).toBe(false)
  })

  it('①-4 ★★ 真正的加载失败才走 error 段，且与降级**互斥**', async () => {
    mSum.mockRejectedValue(new Error('report summary failed'))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-summary-error')).toBe('report summary failed')
    expect(hasId(w, 'rr-summary-degraded')).toBe(false)
  })

  it('①-5 ★ 503 文案是 database not available（本仓第三种措辞）', async () => {
    mSum.mockRejectedValue(new Error('database not available'))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-summary-error')).toContain('database not available')
  })
})

// ── ②③ 四条派生列（每条都有正控）────────────────────────────────────────

describe('② 四条派生列的自验', () => {
  it('②-1 ★★★ error_count === request − success ⇒ 不报违例（正控）', async () => {
    mSum.mockResolvedValue(summaryPayload())
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-t-error_count')).toBe('3')
    expect(hasId(w, 'rr-errorcount-bad')).toBe(false)
  })

  it('②-2 ★★★ error_count 对不上 ⇒ 报违例', async () => {
    mSum.mockResolvedValue(summaryPayload({ totals: totals({ error_count: 99 }) }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-errorcount-bad')).toBe(true)
  })

  it('②-3 ★★★ total_tokens === 四个分量之和 ⇒ 不报违例（正控）', async () => {
    const tt = totals()
    expect(reportTotalsTotalTokensMatches(tt)).toBe(true)
    mSum.mockResolvedValue(summaryPayload())
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-t-total_tokens')).toBe('1000')
    expect(hasId(w, 'rr-totaltokens-bad')).toBe(false)
  })

  it('②-4 ★★★ 「total = input + output」这种直觉写法在本族**恒不成立**', async () => {
    // ★★ total_tokens 把 cache_read / cache_write **也算进去** ⇒
    //   少了 cache 的和必然不等于 total。这是最容易写错的一条。
    const tt = totals()
    expect(tt.total_tokens).not.toBe(tt.input_tokens + tt.output_tokens)
    expect(reportTotalsTotalTokensMatches(tt)).toBe(true)
  })

  it('②-5 ★★★ total_tokens 对不上 ⇒ 报违例', async () => {
    mSum.mockResolvedValue(summaryPayload({ totals: totals({ total_tokens: 1 }) }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-totaltokens-bad')).toBe(true)
  })

  it('②-6 ★★★★ 零请求 ⇒ error_rate 必须是 0（不是 null、不是 NaN）', async () => {
    // ★★ 服务端把除法包在 `if RequestCount > 0` 里、而字段是值类型
    //   ⇒ 零请求时 error_rate 保持零值 0 并照常出现。
    const zero = totals({
      request_count: 0,
      success_count: 0,
      error_count: 0,
      error_rate: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      total_tokens: 0,
      cache_hit_ratio: null,
    })
    expect(reportTotalsErrorRateIsZeroWhenNoRequests(zero)).toBe(true)
    mSum.mockResolvedValue(summaryPayload({ totals: zero }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-t-error_rate')).toBe('0')
    expect(hasId(w, 'rr-errorrate-bad')).toBe(false)
  })

  it('②-7 ★★★★ 零请求却给了非 0 的 error_rate ⇒ 报违例', async () => {
    const zero = totals({
      request_count: 0,
      success_count: 0,
      error_count: 0,
      error_rate: 0.5,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      total_tokens: 0,
      cache_hit_ratio: null,
    })
    mSum.mockResolvedValue(summaryPayload({ totals: zero }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-errorrate-bad')).toBe(true)
  })

  it('②-8 ★★ 非零请求时**不得**触发零请求那条违例（正控：条件要钉住）', async () => {
    mSum.mockResolvedValue(summaryPayload())
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    // ★ 这条是给「把 `request_count === 0` 那个 if 删掉」的变异准备的：
    //   request_count=10 且 error_rate=0.3，本来就不该被判「零请求」
    expect(hasId(w, 'rr-errorrate-bad')).toBe(false)
  })

  it('②-9 ★★★ cache_hit_ratio 分母为 0 ⇒ 裸 null，且键**仍在**', async () => {
    const zero = totals({
      request_count: 0,
      success_count: 0,
      error_count: 0,
      error_rate: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      total_tokens: 0,
      cache_hit_ratio: null,
    })
    mSum.mockResolvedValue(summaryPayload({ totals: zero }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    // ★★ 指针**无 omitempty** ⇒ null 时键仍在（不是键缺、也不是 0）
    expect(cell(w, 'rr-t-cache_hit_ratio')).toContain('裸 null')
  })

  it('②-10 ★★★ 「有缓存但命中率是 0」≠「无法计算」（0 不是 null）', async () => {
    // ★★ 分母 > 0 时可以是真实的 0（cache_read = 0、input > 0）
    const t = totals({ input_tokens: 500, cache_read_tokens: 0, cache_hit_ratio: 0, total_tokens: 500 + 200 + 0 + 400 })
    mSum.mockResolvedValue(summaryPayload({ totals: t }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-t-cache_hit_ratio')).toBe('0')
    expect(cell(w, 'rr-t-cache_hit_ratio')).not.toContain('裸 null')
  })
})

// ── ③ source 由 coverage 派生 ────────────────────────────────────────────

describe('③ source 的派生不变式', () => {
  it('③-1 ★★★★★ source 与 coverage 一致 ⇒ 不报违例（正控）', async () => {
    // ★★★ 第一版我把 Go 的 case 顺序读反了，写成「grain_dates 非空 + legacy_dates 空
    //   ⇒ 派生 legacy」，实得 `grain` ⇒ **后端是对的、判据是错的**。
    //   `grainreport.go:945-951` 的第二个 case 要求的是 **LegacyDates > 0**，
//   不是 GrainDates > 0 ⇒ legacy_dates 为空时两个 case 都不匹配 ⇒ 落 `default` ⇒ `grain`。
    //   ★★ 这正是契约头 (14)「推论一」的来源：`source === "grain"` 连
    //   `grain_dates` 非空都**不蕴含**，更不含「全是新口径」的语义。
    const r = report({ coverage: { grain_dates: null, legacy_dates: ['2026-10-02'] }, source: 'legacy' })
    expect(reportCoverageDeriveSource(r as never)).toBe('legacy')
    mSum.mockResolvedValue(summaryPayload({ coverage: r.coverage, source: 'legacy' }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-source')).toContain('legacy')
    expect(hasId(w, 'rr-source-bad')).toBe(false)
  })

  it('③-2 ★★★★★ source 与 coverage 对不上 ⇒ 报违例', async () => {
    mSum.mockResolvedValue(summaryPayload({ source: 'mixed' }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-source-bad')).toBe(true)
  })

  it('③-3 ★★★★ 两个数组都空 ⇒ 派生值是 grain（default 分支）', async () => {
    // ★★ `source === "grain"` **不蕴含** `grain_dates` 非空 —— 两个都空也走 default。
    const r = report({ coverage: { grain_dates: null, legacy_dates: null }, source: 'grain' })
    expect(reportCoverageDeriveSource(r as never)).toBe('grain')
    mSum.mockResolvedValue(summaryPayload({ coverage: { grain_dates: null, legacy_dates: null }, source: 'grain' }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-source-bad')).toBe(false)
  })

  it('③-4 ★★★★ 两数组都非空 ⇒ 派生值是 mixed', async () => {
    const r = report({
      coverage: { grain_dates: ['2026-10-01'], legacy_dates: ['2026-10-02'] },
      source: 'mixed',
    })
    expect(reportCoverageDeriveSource(r as never)).toBe('mixed')
  })

  it('③-5 ★★ 只有 legacy_dates 非空 ⇒ 派生值是 legacy', async () => {
    const r = report({ coverage: { grain_dates: null, legacy_dates: ['2026-10-02'] }, source: 'legacy' })
    expect(reportCoverageDeriveSource(r as never)).toBe('legacy')
  })
})

// ── ④⑤ coverage 三态与升序 ───────────────────────────────────────────────

describe('④ coverage 的三态', () => {
  it('④-1 ★★★ legacy_dates 是裸 null ⇒ 显示「裸 null」，**不是** 0', async () => {
    mSum.mockResolvedValue(summaryPayload({ coverage: { grain_dates: ['2026-10-01'], legacy_dates: null }, source: 'legacy' }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    const t = cell(w, 'rr-coverage')
    expect(t).toContain('裸 null')
    // ★★ 与「有 0 项」是两件事
    expect(t).not.toContain('legacy_dates 0')
  })

  it('④-2 ★★★ 两个数组都键缺失 ⇒ 显示「键缺失」', async () => {
    mSum.mockResolvedValue(summaryPayload({ coverage: {}, source: 'grain' }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    const t = cell(w, 'rr-coverage')
    expect(t).toContain('键缺失')
  })

  it('④-3 ★★ 有值 ⇒ 显示**长度**（三种状态措辞各不相同）', async () => {
    mSum.mockResolvedValue(
      summaryPayload({ coverage: { grain_dates: ['2026-10-01', '2026-10-02'], legacy_dates: null }, source: 'legacy' }),
    )
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    // ★★ 负断言**必须与被测对象同格**：这一行同时报两个数组，
    //   而 legacy_dates 在本夹具里**就是 null** ⇒ 「裸 null」是**正确**呈现。
    //   第一版对整行断 `not.toContain('裸 null')` ⇒ 又一次把判据范围放错了。
    //   ⇒ 改成只断 grain 那一半。
    const t = cell(w, 'rr-coverage')
    expect(t).toContain('grain_dates 2 项')
    expect(t).toContain('legacy_dates 裸 null')
  })

  it('④-4 ★★ 日期数组升序 ⇒ 不报违例（正控）', async () => {
    mSum.mockResolvedValue(
      summaryPayload({ coverage: { grain_dates: ['2026-10-01', '2026-10-02'], legacy_dates: null }, source: 'legacy' }),
    )
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-coverage-order-bad')).toBe(false)
  })

  it('④-5 ★★ 日期数组乱序 ⇒ 报违例（后端各 sort.Strings 一次）', async () => {
    mSum.mockResolvedValue(
      summaryPayload({ coverage: { grain_dates: ['2026-10-05', '2026-10-01'], legacy_dates: null }, source: 'legacy' }),
    )
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-coverage-order-bad')).toBe(true)
  })
})

// ── ⑥⑦ 回显形状与 filter 不回显 ─────────────────────────────────────────

describe('⑥ 回显与不回显', () => {
  it('⑥-1 ★★ start/end 回显的是 RFC3339，不是发过去的 YYYY-MM-DD', async () => {
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-echo-range')).toContain('2026-10-01T00:00:00Z')
    expect(cell(w, 'rr-echo-range')).toContain('2026-10-07T00:00:00Z')
    expect(hasId(w, 'rr-echo-shape')).toBe(true)
  })

  it('⑥-2 ★★★ 发出去的区间是 YYYY-MM-DD 且**不含今日**（T+1）', async () => {
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    const p = mSum.mock.calls[0]![0]
    expect(p.start).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(p.end).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    // ★★ 今日必须**不在**区间里：聚合是 T+1 的
    const today = new Date().toISOString().slice(0, 10)
    expect(p.start <= today).toBe(true)
    expect(p.end < today).toBe(true)
  })

  it('⑥-3 ★★ 单日区间合法（start === end），上界是 366 天', async () => {
    // ★ `report.go:143-145` 判的是 `end.Sub(start) > 366*24h` ⇒ 单日不算超长
    expect(REPORT_ROLLUP_MAX_RANGE_DAYS).toBe(366)
    const w = await mountView()
    expect(hasId(w, 'rr-window-366')).toBe(true)
  })

  it('⑥-4 ★★ 页面**不得**提供超出 366 天的窗口档位', async () => {
    const w = await mountView()
    const ws = w.findAll('[data-testid^="rr-window-"]').map((n) => Number(n.attributes('data-testid')!.replace('rr-window-', '')))
    expect(ws.length).toBeGreaterThan(0)
    for (const x of ws) expect(x).toBeLessThanOrEqual(REPORT_ROLLUP_MAX_RANGE_DAYS)
    expect(Math.min(...ws)).toBe(1)
  })

  it('⑥-5 ★★★ filter 永不在响应里 ⇒ 页面显式说明「查不到」', async () => {
    mSum.mockResolvedValue(summaryPayload())
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-filter-not-echoed')).toBe(true)
    // ★ 六个维度过滤一个都不回显 ⇒ 页面不能假装能显示「当前过滤：…」
    expect(w.text()).not.toContain('当前过滤')
  })

  it('⑥-6 ★★ view 回显的是规范化值（小写）', async () => {
    mSum.mockResolvedValue(summaryPayload({ view: 'credential' }))
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-view-echo')).toContain('credential')
  })
})

// ── ⑧ 参数下发 ────────────────────────────────────────────────────────────

describe('⑧ 参数下发', () => {
  it('⑧-1 ★★ 四个视角chip 全在，且都发得出去', async () => {
    const w = await mountView()
    for (const v of REPORT_ROLLUP_VIEWS) expect(hasId(w, `rr-view-${v}`)).toBe(true)
  })

  it('⑧-2 ★★ 切视角 ⇒ 带上新 view 重发', async () => {
    const w = await mountView()
    await byId(w, 'rr-view-internal').trigger('click')
    await flushPromises()
    const last = mSum.mock.calls[mSum.mock.calls.length - 1]![0]
    expect(last.view).toBe('internal')
  })

  it('⑧-3 ★★★ detail 恒发（true→`1`，false→`0`），且不发布尔字面量', async () => {
    // ★ 真值集合是 `1`/`true`/`yes`/`daily`（`:194-200`）⇒ `true` 恰好也在集合里，
    //   但 `0` 必须在**两个**端点上都是假 ⇒ 显式发 0 最稳。
    const w = await mountView()
    await byId(w, 'rr-load').trigger('click')
    await flushPromises()
    expect(mSum.mock.calls[0]![0].detail).toBe('0')
    await byId(w, 'rr-detail').setValue(true)
    await flushPromises()
    const last = mSum.mock.calls[mSum.mock.calls.length - 1]![0]
    expect(last.detail).toBe('1')
  })

  it('⑧-4 ★★ dimensions 只带区间与视角（不带 detail）', async () => {
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    const p = mDims.mock.calls[0]![0]
    expect(p.start).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(p.view).toBe(REPORT_ROLLUP_DEFAULT_VIEW)
    expect(p.detail).toBeUndefined()
  })
})

// ── ⑨⑩ 维度候选 ─────────────────────────────────────────────────────────

describe('⑨ 维度候选', () => {
  it('⑨-1 ★★★★ providers 有 name ⇒ 显示 name', async () => {
    mDims.mockResolvedValue(dimPayload({ providers: [{ key: 'openai', name: 'OpenAI', requests: 12 }] }))
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-name-providers-0')).toBe('OpenAI')
  })

  it('⑨-2 ★★★★ models 的 name **恒缺**（fill() 只回填三个维度）', async () => {
    // ★★ `report_rollup.go:378-380` 只把 names 传给 providers/credentials/api_keys
    //   ⇒ models/tenants/persons 连 names 表都没传进去 ⇒ 恒缺，不是「回查失败」。
    mDims.mockResolvedValue(dimPayload({ models: [{ key: 'gpt-4o', requests: 5 }] }))
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-name-models-0')).toBe('（该维度不回填名称）')
    // ★★★ 两种成因措辞必须不同 —— 混成一句就等于替后端下了它没下的结论
    expect(cell(w, 'rr-name-models-0')).not.toContain('回查失败')
  })

  it('⑨-3 ★★★ tenants / persons 的 name 同样恒缺（三个维度逐个钉）', async () => {
    mDims.mockResolvedValue(
      dimPayload({
        tenants: [{ key: 't-1', requests: 3 }],
        persons: [{ key: 'u-9', requests: 2 }],
      }),
    )
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-name-tenants-0')).toContain('不回填名称')
    expect(cell(w, 'rr-name-persons-0')).toContain('不回填名称')
  })

  it('⑨-4 ★★★★ providers 缺 name ⇒ 是「回查失败」，与 models 的措辞**不同**', async () => {
    mDims.mockResolvedValue(dimPayload({ providers: [{ key: '999', requests: 1 }] }))
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-name-providers-0')).toContain('名称回查失败')
    expect(cell(w, 'rr-name-providers-0')).not.toContain('不回填名称')
  })

  it('⑨-5 ★★★ 同一份载荷里两组措辞并存（别用同一个函数处理两种成因）', async () => {
    mDims.mockResolvedValue(
      dimPayload({
        providers: [{ key: '999', requests: 1 }],
        models: [{ key: 'gpt-4o', requests: 5 }],
      }),
    )
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-name-providers-0')).toContain('名称回查失败')
    expect(cell(w, 'rr-name-models-0')).toContain('不回填名称')
  })

  it('⑨-6 ★★ 六个维度组全部渲染（少一个就是接线漏了）', async () => {
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    for (const g of ['providers', 'credentials', 'api_keys', 'models', 'tenants', 'persons']) {
      expect(hasId(w, `rr-group-${g}`)).toBe(true)
    }
  })

  it('⑨-7 ★★ 某个维度候选为空 ⇒ 显示该组为空，**不是**整页空', async () => {
    mDims.mockResolvedValue(dimPayload({ providers: [{ key: 'a', name: 'A', requests: 1 }] }))
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-group-empty-credentials')).toBe(true)
    expect(hasId(w, 'rr-dims-empty')).toBe(false)
  })

  it('⑨-8 ★★ 六组全空 ⇒ 才显示整页空态', async () => {
    mDims.mockResolvedValue(dimPayload())
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(hasId(w, 'rr-dims-empty')).toBe(true)
  })

  it('⑨-9 ★★★ key 统一是字符串（即便语义上是数值 id）⇒ 数字 id 要标出来', async () => {
    mDims.mockResolvedValue(dimPayload({ providers: [{ key: '42', name: 'P42', requests: 8 }] }))
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-key-providers-0')).toContain('42')
    expect(cell(w, 'rr-key-providers-0')).toContain('看似数值 id')
  })

  it('⑨-10 ★★ 非数值 key ⇒ **不得**标「看似数值 id」（正控）', async () => {
    mDims.mockResolvedValue(dimPayload({ providers: [{ key: 'openai', name: 'OpenAI', requests: 8 }] }))
    const w = await mountView()
    await byId(w, 'rr-load-dims').trigger('click')
    await flushPromises()
    expect(cell(w, 'rr-key-providers-0')).not.toContain('看似数值 id')
  })
})

// ── ⑪ superAdmin 档 ⇒ 无角色分支 ────────────────────────────────────────

describe('⑪ superAdmin 档的处置', () => {
  it('⑪-1 ★★★★★ 视图源码（剥注释后）不含 auth store / 角色字面量', () => {
    const src = stripComments(viewSource())
    expect(src).not.toMatch(/from\s+['"]@\/stores\/auth['"]/)
    expect(src).not.toMatch(/\buseAuthStore\b/)
    expect(src).not.toMatch(/super_admin|isSuperAdmin|tenant_admin/)
  })

  it('⑪-2 ★★★★★ 换角色重挂 ⇒ 渲染逐字节相同', () => {
    mSum.mockResolvedValue(summaryPayload())
    authState.role = 'tenant_admin'
    authState.userInfo = { role: 'tenant_admin', tenant_id: 't-1' }
    const a = mount(ReportRollupView)
    authState.role = 'super_admin'
    authState.userInfo = { role: 'super_admin', tenant_id: 't-1' }
    const b = mount(ReportRollupView)
    return flushPromises().then(() => {
      expect(a.html()).toBe(b.html())
    })
  })
})

// ── ⑫ 触控目标 ───────────────────────────────────────────────────────────

describe('⑫ 触控目标', () => {
  it('⑫-1 ★ 新增控件都 ≥48 CSS px', () => {
    const src = viewSource()
    const scoped = src.slice(src.indexOf('<style scoped>'))
    // ★ 把数值**解析出来**再比大小；正则 `[1-4]?\d` 会吃掉两位数、连 48 都会被误判
    const vals = [...scoped.matchAll(/min-height:\s*(\d+(?:\.\d+)?)px/g)].map((m) => Number(m[1]))
    expect(vals.length).toBeGreaterThan(0)
    for (const v of vals) expect(v).toBeGreaterThanOrEqual(48)
  })
})
