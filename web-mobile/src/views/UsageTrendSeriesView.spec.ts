import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import UsageTrendSeriesView from './UsageTrendSeriesView.vue'
import { setLocale } from '@/i18n'
import {
  fetchTrendSeries,
  fetchTrendModels,
  TREND_MODELS_SQL_LIMIT,
  TREND_MAX_MODELS,
  TREND_OTHERS_KEY,
  TREND_SOURCE_DIM,
} from '@/api/usageTrendSeries'

/**
 * 用量趋势线回归护栏（2026-10-10，第一百零八批）。
 *
 * ★★ 本族的租户口径是本仓**第四种，而且是唯一安全的那种**：
 *   `statsTenantScope`（`admin/stats.go:87-97`）对非 `super_admin` / `admin_key`
 *   强制用 `auth.TenantID` 并**忽略 `tenant_id` 参数** ⇒ **tenant_admin 天然隔离**。
 *   ⇒ 与批 104（`dashboard_board_aux.go` 三条子查询硬编码 tenant）、批 106
 *     （`self_check_runs` 有 tenant_id 列却从不引用）**恰好相反**，
 *     所以本页**不做**任何角色分支 —— 那是多余的复杂度。
 *
 * 判据钉九组不变量：
 *  ① ★★★★★ **降级时 `series` 也是 `[]`** ⇒ 空数组**分不清**「零用量」与「没算出来」，
 *      唯一区分是 `degraded` ⇒ **把空数组渲染成「这段时间没有用量」就是撒谎**。
 *  ② ★★★★★ `start`/`end` 只给一个 ⇒ 后端 400（`usage.go:1451-1453`）⇒ 页面不许发半对。
 *  ③ ★★★★ 自定义区间的响应 `end` 是**次日零点** ⇒ 显示结束日必须减一天。
 *  ④ ★★★★ 自定义区间的响应 `start` 必须**逐字等于**本地复算值。
 *  ⑤ ★★★★ **非法/负数 ID 被后端静默当成 0** ⇒ 换数据档而响应看不出异常 ⇒ 必须提示。
 *  ⑥ ★★★★ 模型多选超 20 静默截断 / `trend-models` 硬编码 `LIMIT 100` 静默截断
 *      ⇒ 两处都得说「丢了/被截断」，而不是安静地少几个。
 *  ⑦ ★★★★ 传了 `model` 多选 ⇒ `trend-models` 被 `= ANY` 收敛 ⇒ 不是「可选清单」。
 *  ⑧ ★★★ 排序契约两条：`__others__` 末位 + 其余按 `total_requests` 非递增 ⇒ 违规要显形。
 *  ⑨ ★★★ 后端 `hint` 与本地推算不一致 ⇒ 两端口径漂移，必须说。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function point(bucket: string, requests: number) {
  return { bucket, requests, tokens: requests * 10, credits: 0, cost_usd: 0 }
}

function line(model: string, totalRequests: number, buckets: string[] = ['2026-10-08T00:00:00Z']) {
  return {
    model,
    total_requests: totalRequests,
    total_tokens: totalRequests * 10,
    total_credits: 0,
    total_cost_usd: 0,
    points: buckets.map((b) => point(b, 1)),
  }
}

/** 正常：有序列、没降级。 */
const SERIES_OK = {
  start: '2026-10-08T00:00:00Z',
  end: '2026-10-08T12:00:00Z', // 预设路径 = now（带时分秒）
  bucket_minutes: 5,
  top: 8,
  source: TREND_SOURCE_DIM,
  series: [line('gpt-4o', 120), line('claude-3', 40)],
  degraded: false,
}

/** ★★★★★ 空序列 + 降级。★ 这**不是**「零用量」。 */
const SERIES_DEGRADED_EMPTY = {
  start: '2026-10-08T00:00:00Z',
  end: '2026-10-08T12:00:00Z',
  bucket_minutes: 5,
  top: 8,
  source: TREND_SOURCE_DIM,
  series: [], // ★ 降级时后端也给 `[]`（`:207`）
  degraded: true,
  degraded_reason: 'missing materialized view',
  missing_view: 'request_stats_minute',
  hint: '数据视图 request_stats_minute 尚未初始化，请先执行数据聚合迁移',
}

/** 空序列 + 没降级 ⇒ 这才是真的零用量。 */
const SERIES_TRULY_EMPTY = { ...SERIES_OK, series: [] }

/** ★ 自定义区间：`end` 是**次日零点**，`start` 是选中日零点。 */
const SERIES_CUSTOM = {
  ...SERIES_OK,
  start: '2026-01-01T00:00:00Z',
  end: '2026-01-08T00:00:00Z', // ★ 用户选到 01-07，次日的零点
}

/** 折叠：含 `__others__` 且在末位。 */
const SERIES_FOLDED = {
  ...SERIES_OK,
  series: [line('a', 100), line('b', 50), line(TREND_OTHERS_KEY, 10)],
}

/** ★ 排序契约被破坏：请求数非递增不成立。 */
const SERIES_ORDER_BAD = {
  ...SERIES_OK,
  series: [line('a', 10), line('b', 100)],
}

/** ★ 排序契约被破坏：`__others__` 不在末位。 */
const SERIES_OTHERS_NOT_LAST = {
  ...SERIES_OK,
  series: [line(TREND_OTHERS_KEY, 10), line('a', 100)],
}

/** ★ `hint` 与本地推算不一致（view 为 null 时本地推算是另一句话）。 */
const SERIES_HINT_DRIFT = {
  ...SERIES_DEGRADED_EMPTY,
  hint: '请稍后再试', // ★ 与 trendHintLocal 复算结果不一致
}

function modelsPayload(count: number) {
  return {
    start: '2026-10-08T00:00:00Z',
    end: '2026-10-08T12:00:00Z',
    source: TREND_SOURCE_DIM,
    models: Array.from({ length: count }, (_, i) => ({
      model: `m${i}`,
      requests: count - i,
      tokens: 0,
      credits: 0,
      cost_usd: 0,
    })),
    degraded: false,
  }
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/usageTrendSeries', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/usageTrendSeries')>()
  return { ...actual, fetchTrendSeries: vi.fn(), fetchTrendModels: vi.fn() }
})

const mSeries = fetchTrendSeries as unknown as ReturnType<typeof vi.fn>
const mModels = fetchTrendModels as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(UsageTrendSeriesView)
  await flushPromises()
  return w
}

function byTestId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasTestId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  mSeries.mockResolvedValue(SERIES_OK)
  mModels.mockResolvedValue(modelsPayload(3))
})

// ── ① 降级 vs 零用量（本族最要紧的一格）────────────────────────────────

describe('① 空序列：零用量 与 降级 是两件事', () => {
  it('★★★★★ 降级 + 空序列 ⇒ 不得出现「确实没有用量」', async () => {
    mSeries.mockResolvedValue(SERIES_DEGRADED_EMPTY)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-truly-empty')).toBe(false)
    expect(hasTestId(w, 'uts-empty-degraded')).toBe(true)
    expect(w.text()).not.toContain('确实没有用量')
  })

  it('★★★★★ 正控：空序列 + 未降级 ⇒ 必须说「确实没有用量」', async () => {
    // ★ 没有这条，上面那条就可能是「页面永远不显示那句」而照样绿。
    mSeries.mockResolvedValue(SERIES_TRULY_EMPTY)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-truly-empty')).toBe(true)
    expect(hasTestId(w, 'uts-empty-degraded')).toBe(false)
  })

  it('★★★★ 降级 ⇒ 常驻披露（不随数据段消失）且带原因与视图名', async () => {
    mSeries.mockResolvedValue(SERIES_DEGRADED_EMPTY)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const badge = byTestId(w, 'uts-degraded')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toContain('missing materialized view')
    expect(badge.text()).toContain('request_stats_minute')
  })

  it('★★★★ 正控：未降级 ⇒ 不得出现降级徽标', async () => {
    mSeries.mockResolvedValue(SERIES_OK)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-degraded')).toBe(false)
  })

  it('★★★ 降级时桶粒度与 top 仍照常显示（后端 `:204-205` 填的是解析后的值）', async () => {
    mSeries.mockResolvedValue(SERIES_DEGRADED_EMPTY)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const meta = byTestId(w, 'uts-meta')
    expect(meta.exists()).toBe(true)
    expect(meta.text()).toContain('5')
    expect(meta.text()).toContain('8')
  })

  it('★★★★ hint 与本地推算不一致 ⇒ 显式报「两端口径已漂」', async () => {
    mSeries.mockResolvedValue(SERIES_HINT_DRIFT)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-hint-drift')).toBe(true)
  })

  it('★★★ 正控：hint 一致 ⇒ 不得报漂移', async () => {
    mSeries.mockResolvedValue(SERIES_DEGRADED_EMPTY)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-hint-drift')).toBe(false)
    // ★ 且本地推算的 hint 必须真的渲染出来了
    expect(byTestId(w, 'uts-degraded-hint').text()).toContain('数据视图')
  })
})

// ── ② 只给一个日期 ⇒ 不发请求 ──────────────────────────────────────────

describe('② start/end 必须配对', () => {
  it('★★★★★ 只给 start ⇒ 告警存在，且**一次请求都不发**', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-mode-custom').trigger('click')
    await byTestId(w, 'uts-start').setValue('2026-01-01')
    await flushPromises()
    expect(hasTestId(w, 'uts-pair-broken')).toBe(true)
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(mSeries).not.toHaveBeenCalled()
  })

  it('★★★★★ 只给 end ⇒ 同样不发请求（对称）', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-mode-custom').trigger('click')
    await byTestId(w, 'uts-end').setValue('2026-01-07')
    await flushPromises()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(mSeries).not.toHaveBeenCalled()
  })

  it('★★★★ 配齐 ⇒ 真的发请求，且 start/end 原样发用户选的日期（不加一天）', async () => {
    mSeries.mockResolvedValue(SERIES_CUSTOM)
    const w = await mountView()
    await byTestId(w, 'uts-mode-custom').trigger('click')
    await byTestId(w, 'uts-start').setValue('2026-01-01')
    await byTestId(w, 'uts-end').setValue('2026-01-07')
    await flushPromises()
    expect(hasTestId(w, 'uts-pair-broken')).toBe(false)
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(mSeries).toHaveBeenCalledTimes(1)
    const q = mSeries.mock.calls[0]![0]!
    expect(q.start).toBe('2026-01-01')
    // ★★ +1 天是**后端**做的事（usage.go:1469）；客户端补齐会让响应 end 对不上
    expect(q.end).toBe('2026-01-07')
  })

  it('★★★ 预设模式下不因残留日期而误判为「配对损坏」', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-mode-custom').trigger('click')
    await byTestId(w, 'uts-start').setValue('2026-01-01')
    await byTestId(w, 'uts-mode-preset').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-pair-broken')).toBe(false)
  })
})

// ── ③④ end 的两种形态 / start 对拍 ────────────────────────────────────

describe('③④ 时间窗回显', () => {
  it('★★★★★ 自定义区间：响应 end 是次日零点 ⇒ 显示的结束日必须**减一天**', async () => {
    mSeries.mockResolvedValue(SERIES_CUSTOM)
    const w = await mountView()
    await byTestId(w, 'uts-mode-custom').trigger('click')
    await byTestId(w, 'uts-start').setValue('2026-01-01')
    await byTestId(w, 'uts-end').setValue('2026-01-07')
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const echo = byTestId(w, 'uts-window-echo')
    // ★ 用户选到 01-07 ⇒ 页面必须显示 01-07，而不是次日零点 01-08
    expect(echo.text()).toContain('2026-01-07')
    expect(echo.text()).not.toContain('2026-01-08')
  })

  it('★★★★ 正控：预设路径 end=now ⇒ 照原样显示、不减一天', async () => {
    mSeries.mockResolvedValue(SERIES_OK)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    // ★ end 带时分秒 ⇒ 不是次日零点 ⇒ 不减一天
    expect(byTestId(w, 'uts-window-echo').text()).toContain('2026-10-08T12:00:00Z')
  })

  it('★★★★★ start 与本地复算不一致 ⇒ 显式报「后端把窗口换掉了」', async () => {
    mSeries.mockResolvedValue({ ...SERIES_CUSTOM, start: '2026-06-01T00:00:00Z' })
    const w = await mountView()
    await byTestId(w, 'uts-mode-custom').trigger('click')
    await byTestId(w, 'uts-start').setValue('2026-01-01')
    await byTestId(w, 'uts-end').setValue('2026-01-07')
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-start-drift')).toBe(true)
  })

  it('★★★ 正控：start 与本地复算一致 ⇒ 不得报漂移', async () => {
    mSeries.mockResolvedValue(SERIES_CUSTOM)
    const w = await mountView()
    await byTestId(w, 'uts-mode-custom').trigger('click')
    await byTestId(w, 'uts-start').setValue('2026-01-01')
    await byTestId(w, 'uts-end').setValue('2026-01-07')
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-start-drift')).toBe(false)
  })

  it('★★★ 预设模式不做 start 对拍（后端按 now 推起点，本地算不出）', async () => {
    mSeries.mockResolvedValue(SERIES_OK)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-start-drift')).toBe(false)
  })
})

// ── ⑤ 非法 / 负数 ID ───────────────────────────────────────────────────

describe('⑤ 静默换数据档的 ID 过滤', () => {
  it('★★★★★ provider_id = 0 ⇒ 告警（会被后端静默当成没传）', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-provider').setValue('0')
    await flushPromises()
    expect(byTestId(w, 'uts-id-ignored').text()).toContain('provider_id')
  })

  it('★★★★★ provider_id = -5 ⇒ 同样告警（负数也不构成条件）', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-provider').setValue('-5')
    await flushPromises()
    expect(hasTestId(w, 'uts-id-ignored')).toBe(true)
  })

  it('★★★★ api_key_id 非数字 ⇒ 告警点名 api_key_id', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-apikey').setValue('abc')
    await flushPromises()
    expect(byTestId(w, 'uts-id-ignored').text()).toContain('api_key_id')
  })

  it('★★★★ 非法 ID 不进查询串（发了会换数据档）', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-provider').setValue('0')
    await byTestId(w, 'uts-apikey').setValue('-3')
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const q = mSeries.mock.calls[0]![0]!
    expect(q.provider_id).toBeUndefined()
    expect(q.api_key_id).toBeUndefined()
  })

  it('★★★ 正控：合法正整数 ⇒ 不告警且真的发出去', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-provider').setValue('7')
    await flushPromises()
    expect(hasTestId(w, 'uts-id-ignored')).toBe(false)
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(mSeries.mock.calls[0]![0]!.provider_id).toBe(7)
  })

  it('★★★ 正控：空串 ⇒ 不告警、不发（等价于没填）', async () => {
    const w = await mountView()
    await flushPromises()
    expect(hasTestId(w, 'uts-id-ignored')).toBe(false)
  })
})

// ── ⑥ 两处静默截断 ─────────────────────────────────────────────────────

describe('⑥ 静默截断的两处', () => {
  it('★★★★★ trend-models 满 LIMIT 100 ⇒ 明说「被静默截断」', async () => {
    mModels.mockResolvedValue(modelsPayload(TREND_MODELS_SQL_LIMIT))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    const p = byTestId(w, 'uts-models-truncated')
    expect(p.exists()).toBe(true)
    expect(p.text()).toContain(String(TREND_MODELS_SQL_LIMIT))
  })

  it('★★★ 正控：99 条 ⇒ 不得说截断', async () => {
    mModels.mockResolvedValue(modelsPayload(TREND_MODELS_SQL_LIMIT - 1))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-models-truncated')).toBe(false)
  })

  it('★★★★★ 选超过 20 个模型 ⇒ 明说丢了几个', async () => {
    // ★ 只能点到清单里有的模型 ⇒ 先加载 20+ 条
    mModels.mockResolvedValue(modelsPayload(TREND_MAX_MODELS + 5))
    const w2 = await mountView()
    await byTestId(w2, 'uts-load-models').trigger('click')
    await flushPromises()
    const more = w2.findAll('[data-testid^="uts-model-"]')
    expect(more.length).toBe(TREND_MAX_MODELS + 5)
    for (let i = 0; i < TREND_MAX_MODELS + 5; i++) {
      await more[i]!.trigger('click')
    }
    await flushPromises()
    const dropped = byTestId(w2, 'uts-models-dropped')
    expect(dropped.exists()).toBe(true)
    expect(dropped.text()).toContain('5')
  })

  it('★★★ 正控：没超 20 ⇒ 不得说丢了', async () => {
    mModels.mockResolvedValue(modelsPayload(3))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    await byTestId(w, 'uts-model-m0').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-models-dropped')).toBe(false)
    expect(byTestId(w, 'uts-models-selected').text()).toContain('1')
  })

  it('★★★ 模型多选用**重复参数**，不是逗号串（后端读 Query()["model"]）', async () => {
    mModels.mockResolvedValue(modelsPayload(2))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    await byTestId(w, 'uts-model-m0').trigger('click')
    await byTestId(w, 'uts-model-m1').trigger('click')
    await flushPromises()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const q = mSeries.mock.calls[0]![0]!
    expect(Array.isArray(q.model)).toBe(true)
    expect(q.model).toEqual(['m0', 'm1'])
  })
})

// ── ⑦ trend-models 的双重身份 ─────────────────────────────────────────

describe('⑦ trend-models 不总是「可选清单」', () => {
  it('★★★★★ 选了模型 ⇒ 明说这份清单已被收敛，不能当可选清单', async () => {
    mModels.mockResolvedValue(modelsPayload(3))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    await byTestId(w, 'uts-model-m0').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-models-constrained')).toBe(true)
  })

  it('★★★ 正控：没选模型 ⇒ 不得说收敛（它就是可选清单）', async () => {
    mModels.mockResolvedValue(modelsPayload(3))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-models-constrained')).toBe(false)
  })

  it('★★★ 加载清单的请求**不带** model 多选（带了就没得选了）', async () => {
    mModels.mockResolvedValue(modelsPayload(3))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    const q = mModels.mock.calls[0]![0]!
    expect(q.model).toBeUndefined()
  })

  it('★★★ 模型清单为空 ⇒ 空态与「截断」是两回事（不出截断提示）', async () => {
    mModels.mockResolvedValue(modelsPayload(0))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('没有可用模型')
    expect(hasTestId(w, 'uts-models-truncated')).toBe(false)
  })
})

// ── ⑧ 排序契约 ─────────────────────────────────────────────────────────

describe('⑧ 排序契约违规要显形', () => {
  it('★★★★★ total_requests 非递增被破坏 ⇒ 显式告警', async () => {
    mSeries.mockResolvedValue(SERIES_ORDER_BAD)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-order-bad')).toBe(true)
  })

  it('★★★★★ `__others__` 不在末位 ⇒ 显式告警', async () => {
    mSeries.mockResolvedValue(SERIES_OTHERS_NOT_LAST)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-others-not-last')).toBe(true)
  })

  it('★★★ 正控：合规序列 ⇒ 两条告警都不出现', async () => {
    mSeries.mockResolvedValue(SERIES_OK)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-order-bad')).toBe(false)
    expect(hasTestId(w, 'uts-others-not-last')).toBe(false)
  })

  it('★★★★★ `__others__` 渲染成「其余模型合计」而不是那个魔数', async () => {
    mSeries.mockResolvedValue(SERIES_FOLDED)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const card = byTestId(w, `uts-line-${TREND_OTHERS_KEY}`)
    expect(card.exists()).toBe(true)
    expect(card.text()).toContain('其余模型合计')
    expect(w.text()).not.toContain(TREND_OTHERS_KEY)
  })

  it('★★★★ 发生折叠 ⇒ 明说排名 N 之后被汇总（别让人以为只有这几个模型）', async () => {
    mSeries.mockResolvedValue(SERIES_FOLDED)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(byTestId(w, 'uts-folded').text()).toContain('8')
  })

  it('★★★ 正控：指定模型后长尾不折叠 ⇒ 说「可以超过 top」', async () => {
    mModels.mockResolvedValue(modelsPayload(2))
    const w = await mountView()
    await byTestId(w, 'uts-load-models').trigger('click')
    await flushPromises()
    await byTestId(w, 'uts-model-m0').trigger('click')
    await flushPromises()
    mSeries.mockResolvedValue(SERIES_FOLDED)
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-fold-suppressed')).toBe(true)
    expect(hasTestId(w, 'uts-folded')).toBe(false)
  })
})

// ── 元信息与失败路径 ───────────────────────────────────────────────────

describe('元信息与失败路径', () => {
  it('★★★ 桶粒度与 top 用**响应回显值**，不自己兜底', async () => {
    // ★★ 桌面写 `resp.bucket_minutes || 60`，兜底恒不生效且 60 不是唯一合法档位
    mSeries.mockResolvedValue({ ...SERIES_OK, bucket_minutes: 15, top: 20 })
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const meta = byTestId(w, 'uts-meta')
    expect(meta.text()).toContain('15')
    expect(meta.text()).toContain('20')
  })

  it('★★★ days=7 的提示说的是「6 天前的零点」而不是 7 天', async () => {
    const w = await mountView()
    await byTestId(w, 'uts-days-7').trigger('click')
    await flushPromises()
    // ★ todayStart.Add(-(days-1)*24h) ⇒ 减 6 天
    expect(byTestId(w, 'uts-days-hint').text()).toContain('6')
  })

  it('★★★★ 请求失败 ⇒ 报错可见，且不得留下上一次的成功结果', async () => {
    mSeries.mockResolvedValue(SERIES_OK)
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(hasTestId(w, 'uts-line-gpt-4o')).toBe(true)
    mSeries.mockRejectedValueOnce(new Error('boom'))
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(byTestId(w, 'uts-series-error').text()).toContain('boom')
    expect(hasTestId(w, 'uts-line-gpt-4o')).toBe(false)
  })

  it('★★★ 数据点过多时不逐桶渲染，只给桶数与首末桶', async () => {
    const many = Array.from({ length: 288 }, (_, i) =>
      point(`2026-10-08T${String(Math.floor(i / 12)).padStart(2, '0')}:${String((i % 12) * 5).padStart(2, '0')}:00Z`, 1),
    )
    mSeries.mockResolvedValue({
      ...SERIES_OK,
      series: [{ ...line('a', 288), points: many }],
    })
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    const card = byTestId(w, 'uts-line-a')
    expect(card.text()).toContain('288')
    // ★ 逐桶渲染会让移动端 DOM 爆掉 ⇒ 只渲染首末两个时间
    expect(w.findAll('td').length).toBeLessThan(20)
  })

  it('★★★ 空序列 ⇒ 点摘要说「没有数据点」而不是空白', async () => {
    mSeries.mockResolvedValue({
      ...SERIES_OK,
      series: [{ ...line('a', 0), points: [] }],
    })
    const w = await mountView()
    await byTestId(w, 'uts-load-series').trigger('click')
    await flushPromises()
    expect(byTestId(w, 'uts-line-a').text()).toContain('没有数据点')
  })
})
