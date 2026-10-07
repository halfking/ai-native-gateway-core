import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AutoRouteView from './AutoRouteView.vue'
import {
  fetchAutoRouteAudit,
  fetchAutoRouteIndex,
  fetchAutoRouteCustomerCost,
  fetchAutoRouteModelCost,
  fetchAutoRouteAccuracy,
} from '@/api/autoRouteRead'
import { setLocale, locale } from '@/i18n'

/**
 * AutoRouteView 的不变量（2026-10-08，第五十六批）。
 *
 * ★ 本批最该被钉住的五条**全是「不能都渲染成同一个东西」**：
 *   ① **稀疏键缺失 ≠ 0**（三段列表，每行只有 1~4 个键无条件）⇒ 判据锚在
 *      `.ar__cell--nodata` 这个 class 上，不只看字形。
 *   ② **audit 三个块缺键 = 查询失败**，有键为空 = 没有数据 ⇒ 两个不同文案。
 *   ③ **outcome_source.stale** ⇒ 数字停止产生，不是数字低 ⇒ 必须挂免责句。
 *   ④ **空索引哨兵 `[{warning}]` ≠ 真·空索引 `[]`**。
 *   ⑤ **success_rate 的 0 有两个来源**：没有流量 vs 真的全失败。
 *   外加：失败路径不得渲染任何空态文案（错误 ≠ 零行）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/autoRouteRead', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/autoRouteRead')>()
  return {
    ...actual,
    fetchAutoRouteAudit: vi.fn(),
    fetchAutoRouteIndex: vi.fn(),
    fetchAutoRouteCustomerCost: vi.fn(),
    fetchAutoRouteModelCost: vi.fn(),
    fetchAutoRouteAccuracy: vi.fn(),
  }
})

const auditMock = fetchAutoRouteAudit as unknown as ReturnType<typeof vi.fn>
const indexMock = fetchAutoRouteIndex as unknown as ReturnType<typeof vi.fn>
const customerMock = fetchAutoRouteCustomerCost as unknown as ReturnType<typeof vi.fn>
const modelMock = fetchAutoRouteModelCost as unknown as ReturnType<typeof vi.fn>
const accuracyMock = fetchAutoRouteAccuracy as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(AutoRouteView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

/** 找第 n 个「加载/重新加载」按钮（每段一个，顺序即模板顺序）。 */
function loadBtn(w: W, n: number) {
  return w.findAll('.ar__load')[n]!
}
async function clickLoad(w: W, n: number): Promise<void> {
  await loadBtn(w, n).trigger('click')
  await flushPromises()
  await flushPromises()
}

/** audit 段（第一段）的根容器。 */
function sectionOf(w: W, index: number) {
  return w.findAll('.ar__section')[index]!
}

/** 数一段里某个子串出现几次 —— 「出现过」这种断言会被同义的第二处顶住。 */
function countOccurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1
}

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────── */

/** auto_route.go:571-578 + outcome_source_freshness.go:70-76。 */
const AUDIT_FULL = {
  total_requests: 1200,
  total_auto_requests: 900,
  specified_model_requests: 300,
  success_rate: 0.98,
  task_distribution: { code: 520 },
  profile_distribution: { smart: 400 },
  top_chosen_models: [{ model: 'gpt-4o', count: 610 }],
  outcome_source: {
    available: true,
    as_of: '2026-10-08T02:55:00Z',
    age_seconds: 3600,
    stale: false,
    stale_after_seconds: 14400,
    reason: 'live',
  },
}

/** ★ 三个条件键**全部缺失**（查询失败，HTTP 仍 200）。 */
const AUDIT_BLOCKS_MISSING = {
  total_requests: 1200,
  total_auto_requests: 900,
  specified_model_requests: 300,
  success_rate: 0.5,
  outcome_source: {
    available: true,
    as_of: '2026-10-08T02:55:00Z',
    age_seconds: 3600,
    stale: false,
    stale_after_seconds: 14400,
    reason: 'live',
  },
}

/** ★ 有键但为空数组（真的没有数据，不是查询失败）。 */
const AUDIT_BLOCKS_EMPTY = {
  ...AUDIT_FULL,
  task_distribution: {},
  profile_distribution: {},
  top_chosen_models: [],
}

/** ★ total=0 ⇒ 后端强制写 success_rate=0.0（auto_route.go:577-580）。 */
const AUDIT_NO_TRAFFIC = {
  ...AUDIT_BLOCKS_MISSING,
  total_requests: 0,
  total_auto_requests: 0,
  specified_model_requests: 0,
  success_rate: 0,
}

/** ★ 数据源过期：数字不再产生。 */
const AUDIT_STALE = {
  ...AUDIT_FULL,
  outcome_source: {
    available: true,
    as_of: '2026-10-07T20:00:00Z',
    age_seconds: 99999,
    stale: true,
    stale_after_seconds: 14400,
    reason: 'stale',
  },
}

/** auto_route.go:352-399 全键行 + :352-356 稀疏行。 */
const INDEX_FULL = {
  bucket: '2026-10-08T03:00:00Z',
  credential_id: 42,
  raw_model: 'gpt-4o-2024-11-20',
  success_rate: 0.97,
  p95_latency_ms: 2400,
  score_smart: 0.93,
  pressure_ratio: 0.19,
  updated_at: '2026-10-08T03:04:12Z',
}
const INDEX_SPARSE = {
  bucket: '2026-10-08T03:00:00Z',
  credential_id: 43,
  raw_model: 'unknown-model',
  updated_at: '2026-10-08T03:04:12Z',
}

/** auto_route.go:296 的空索引哨兵。 */
const INDEX_SENTINEL = [
  { warning: 'credential_model_index is empty; awaiting first bg worker refresh (within 5 minutes of gateway start)' },
]

/** auto_route.go:914-947 全键行 + :914-916 单键行。 */
const CUSTOMER_FULL = {
  api_key_id: 501,
  key_alias: 'prod-billing',
  cost_usd_24h: 30.5,
  cost_usd_7d: 210.75,
  total_auto_requests: 4000,
  last_request_at: '2026-10-08T02:59:00Z',
}
const CUSTOMER_SPARSE = { api_key_id: 502 }

/** ★ success > requests —— 成本视图计数器自相矛盾。 */
const CUSTOMER_CONTRADICT = {
  api_key_id: 503,
  total_auto_requests: 10,
  total_auto_success: 11,
}

/** auto_route.go:1006-1026 全键行；per-request 分母为 0 与缺失两种。 */
const MODEL_FULL = {
  raw_model: 'gpt-4o',
  total_cost_usd: 88.25,
  total_requests: 9000,
  success_rate: 0.99,
}
const MODEL_ZERO_DENOM = { raw_model: 'zero-denom', total_cost_usd: 5, total_requests: 0 }
const MODEL_NO_DENOM = { raw_model: 'no-denom', total_cost_usd: 5 }

/** auto_route_tuning.go:791-800 正常行 / 全 0 编造行。 */
const ACCURACY_ROW = {
  task_type: 'code',
  classifier: 'fuzzy_v2',
  total: 812,
  avg_quality: 0.88,
  avg_success: 0.96,
  avg_latency: 2300,
  avg_cost: 0.021,
  drift_rate: 0.04,
}
const ACCURACY_ZERO_ROW = {
  task_type: 'rare',
  classifier: 'none',
  total: 10,
  avg_quality: 0,
  avg_success: 0,
  avg_latency: 0,
  avg_cost: 0,
  drift_rate: 0,
}

function accuracyResp(rows: unknown[] = [ACCURACY_ROW], windowDays = 7) {
  return { window_days: windowDays, breakdown: rows, generated_at: '2026-10-08T03:00:00Z' }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  auditMock.mockResolvedValue(AUDIT_FULL)
  indexMock.mockResolvedValue([INDEX_FULL, INDEX_SPARSE])
  customerMock.mockResolvedValue([CUSTOMER_FULL, CUSTOMER_SPARSE])
  modelMock.mockResolvedValue([MODEL_FULL])
  accuracyMock.mockResolvedValue(accuracyResp())
  document.body.innerHTML = ''
})

afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE)
})

/* ═══════════════════════════════════════════════════════════════════════ */

describe('AutoRouteView / 按需加载', () => {
  it('★ 挂载时不自动发任何请求（五段全是按需）', async () => {
    const w = await mountView()
    expect(auditMock).not.toHaveBeenCalled()
    expect(indexMock).not.toHaveBeenCalled()
    expect(customerMock).not.toHaveBeenCalled()
    expect(modelMock).not.toHaveBeenCalled()
    expect(accuracyMock).not.toHaveBeenCalled()
    expect(w.find('.ar__kpis').exists()).toBe(false)
    expect(w.findAll('.ar__item').length).toBe(0)
  })

  it('五个加载按钮都在，且逐段只触发自己那条端点', async () => {
    const w = await mountView()
    expect(w.findAll('.ar__load').length).toBe(5)
    await clickLoad(w, 0)
    expect(auditMock).toHaveBeenCalledTimes(1)
    expect(indexMock).not.toHaveBeenCalled()
    await clickLoad(w, 1)
    expect(indexMock).toHaveBeenCalledTimes(1)
    expect(customerMock).not.toHaveBeenCalled()
  })

  it('加载后按钮变成「重新加载」', async () => {
    const w = await mountView()
    expect(loadBtn(w, 0).text()).toBe('加载')
    await clickLoad(w, 0)
    expect(loadBtn(w, 0).text()).toBe('重新加载')
  })
})

describe('AutoRouteView / audit 聚合', () => {
  it('★ success_rate 有流量时按百分比渲染', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const kpis = sectionOf(w, 0).findAll('.ar__kpi-value')
    expect(kpis[3]!.text()).toBe('98.00%')
  })

  it('★ total=0 时 success_rate 显示「没有流量」而不是 0.00%', async () => {
    auditMock.mockResolvedValue(AUDIT_NO_TRAFFIC)
    const w = await mountView()
    await clickLoad(w, 0)
    const rate = sectionOf(w, 0).findAll('.ar__kpi-value')[3]!
    expect(rate.text()).toBe('没有流量')
    // ★ 视觉上也要与真实百分比可分
    expect(rate.classes()).toContain('ar__kpi-value--notraffic')
  })

  it('★ 真的 0% 成功率（有流量）**不带** notraffic class', async () => {
    auditMock.mockResolvedValue({ ...AUDIT_FULL, success_rate: 0 })
    const w = await mountView()
    await clickLoad(w, 0)
    const rate = sectionOf(w, 0).findAll('.ar__kpi-value')[3]!
    expect(rate.text()).toBe('0.00%')
    expect(rate.classes()).not.toContain('ar__kpi-value--notraffic')
  })

  it('★ outcome_source.stale ⇒ 必须出现免责句', async () => {
    auditMock.mockResolvedValue(AUDIT_STALE)
    const w = await mountView()
    await clickLoad(w, 0)
    const warn = sectionOf(w, 0).find('.ar__stale')
    expect(warn.exists()).toBe(true)
    expect(warn.text()).toContain('停止产生')
  })

  it('★ 数据源新鲜时**不**出现免责句', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.ar__stale').exists()).toBe(false)
  })

  it('★ 三个块缺键 ⇒ 渲染「查询失败」', async () => {
    auditMock.mockResolvedValue(AUDIT_BLOCKS_MISSING)
    const w = await mountView()
    await clickLoad(w, 0)
    const sec = sectionOf(w, 0)
    expect(sec.find('.ar__block-failed').exists()).toBe(true)
    expect(sec.text()).toContain('查询失败')
  })

  it('★ 有键但为空 ⇒ 渲染「没有数据」而不是「查询失败」', async () => {
    auditMock.mockResolvedValue(AUDIT_BLOCKS_EMPTY)
    const w = await mountView()
    await clickLoad(w, 0)
    const sec = sectionOf(w, 0)
    // ★ 判据锚在 class 上再逐个断言**文案**：只断言「出现过」的话，
    //   把其中一处改成别的文案照样绿（本批 B8 变异就是这么溜过去的）。
    // ★ 失败分支的 span 同时挂 ar__cell--nodata 与 ar__block-failed，
    //   所以「空数据格」必须用 :not() 排除，否则两个 class 不互斥、数不准。
    const cells = sec.findAll('.ar__cell--nodata:not(.ar__block-failed)')
    expect(cells.length).toBe(3)
    for (const c of cells) expect(c.text()).toBe('没有数据')
    expect(sec.findAll('.ar__block-failed').length).toBe(0)
  })

  it('★★ 空块场景：三处都必须是「没有数据」，一次「查询失败」都不许出现', async () => {
    // ★ 每个用例只挂一次：attachTo document.body 下同挂两个 wrapper 会互相干扰
    //   （第一次实测就因为这个把 3 数成了 6）。
    auditMock.mockResolvedValue(AUDIT_BLOCKS_EMPTY)
    const w = await mountView()
    await clickLoad(w, 0)
    const cells = sectionOf(w, 0).findAll('.ar__cell--nodata:not(.ar__block-failed)')
    expect(cells.length).toBe(3)
    for (const c of cells) expect(c.text()).toBe('没有数据')
    expect(sectionOf(w, 0).findAll('.ar__block-failed').length).toBe(0)
  })

  it('★★ 缺键场景：三处都必须是「查询失败」，一次「没有数据」都不许出现', async () => {
    auditMock.mockResolvedValue(AUDIT_BLOCKS_MISSING)
    const w = await mountView()
    await clickLoad(w, 0)
    const sec = sectionOf(w, 0)
    const failed = sec.findAll('.ar__block-failed')
    expect(failed.length).toBe(3)
    for (const f of failed) expect(f.text()).toBe('查询失败')
    expect(sec.findAll('.ar__cell--nodata:not(.ar__block-failed)').length).toBe(0)
  })

  it('★ 完整数据 ⇒ 三个块都渲染出内容且无失败态', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const sec = sectionOf(w, 0)
    expect(sec.find('.ar__block-failed').exists()).toBe(false)
    expect(sec.text()).toContain('gpt-4o')
    expect(sec.text()).toContain('code')
    expect(sec.text()).toContain('smart')
  })

  it('★ 词表外的 reason 落到「未知」而不是猜一个', async () => {
    auditMock.mockResolvedValue({
      ...AUDIT_FULL,
      outcome_source: { ...AUDIT_FULL.outcome_source, reason: 'brand_new_reason' },
    })
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.ar__echo').text()).toContain('未知')
  })
})

describe('AutoRouteView / 稀疏键', () => {
  it('★ index 稀疏行的四个指标渲染成「无数据」且带 nodata class，不是 0.00', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const items = sectionOf(w, 1).findAll('.ar__item')
    expect(items.length).toBe(2)
    const sparseItem = items[1]!
    const cells = sparseItem.findAll('dd')
    expect(cells.length).toBe(4)
    for (const c of cells) {
      expect(c.text()).toBe('无数据')
      expect(c.classes()).toContain('ar__cell--nodata')
    }
    expect(sparseItem.text()).not.toContain('0.00')
  })

  it('★ index 全键行渲染出真实数值，且**不带** nodata class', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const fullItem = sectionOf(w, 1).findAll('.ar__item')[0]!
    expect(fullItem.findAll('.ar__cell--nodata').length).toBe(0)
    expect(fullItem.text()).toContain('0.97')
    expect(fullItem.text()).toContain('2,400')
  })

  it('★ customer 成本稀疏行同样渲染「无数据」', async () => {
    const w = await mountView()
    await clickLoad(w, 2)
    const items = sectionOf(w, 2).findAll('.ar__item')
    expect(items.length).toBe(2)
    const sparseItem = items[1]!
    for (const c of sparseItem.findAll('dd')) {
      expect(c.text()).toBe('无数据')
      expect(c.classes()).toContain('ar__cell--nodata')
    }
  })
})

describe('AutoRouteView / 索引空态', () => {
  it('★ 空索引哨兵 ⇒ 「等待首轮刷新」，且不渲染行', async () => {
    indexMock.mockResolvedValue(INDEX_SENTINEL)
    const w = await mountView()
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    expect(sec.findAll('.ar__item').length).toBe(0)
    expect(sec.text()).toContain('等待后台首轮刷新')
    expect(sec.text()).not.toContain('没有数据')
  })

  it('★ 真·空数组 ⇒ 「没有数据」，不是「等待首轮刷新」', async () => {
    indexMock.mockResolvedValue([])
    const w = await mountView()
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    expect(sec.text()).toContain('没有数据')
    expect(sec.text()).not.toContain('等待后台首轮刷新')
  })
})

describe('AutoRouteView / 成本', () => {
  it('★ 分母为 0 ⇒ 每请求成本显示「无数据」，不显示 Infinity/空白', async () => {
    modelMock.mockResolvedValue([MODEL_ZERO_DENOM])
    const w = await mountView()
    await clickLoad(w, 3)
    const sec = sectionOf(w, 3)
    expect(sec.text()).not.toContain('Infinity')
    expect(sec.text()).not.toContain('NaN')
    const perReq = sec.findAll('.ar__kv-row').find((r) => r.text().includes('每请求成本'))!
    expect(perReq.find('dd').text()).toBe('无数据')
    expect(perReq.find('dd').classes()).toContain('ar__cell--nodata')
  })

  it('★ 分母缺失 ⇒ 每请求成本显示「无数据」', async () => {
    modelMock.mockResolvedValue([MODEL_NO_DENOM])
    const w = await mountView()
    await clickLoad(w, 3)
    const perReq = sectionOf(w, 3)
      .findAll('.ar__kv-row')
      .find((r) => r.text().includes('每请求成本'))!
    expect(perReq.find('dd').text()).toBe('无数据')
  })

  it('正常分母时每请求成本算出真实值', async () => {
    const w = await mountView()
    await clickLoad(w, 3)
    const perReq = sectionOf(w, 3)
      .findAll('.ar__kv-row')
      .find((r) => r.text().includes('每请求成本'))!
    expect(perReq.find('dd').text()).toContain('$')
    expect(perReq.find('dd').classes()).not.toContain('ar__cell--nodata')
  })

  it('★ 计数器自相矛盾 ⇒ 单独报警', async () => {
    customerMock.mockResolvedValue([CUSTOMER_CONTRADICT])
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('计数器自相矛盾')
  })

  it('正常行不报矛盾', async () => {
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).not.toContain('计数器自相矛盾')
  })
})

describe('AutoRouteView / 调优准确率', () => {
  it('★ 五个 avg_* 全 0 而 total>0 ⇒ 挂「编造的 0」免责', async () => {
    accuracyMock.mockResolvedValue(accuracyResp([ACCURACY_ZERO_ROW]))
    const w = await mountView()
    await clickLoad(w, 4)
    expect(sectionOf(w, 4).text()).toContain('COALESCE 编造')
  })

  it('★ 整表免责与逐行免责**各出现一次**（只断言「出现过」会被逐行那条顶住）', async () => {
    // ★ 反向样本：全 0 行 ⇒ 整表 1 条 + 逐行 1 条 = 2
    accuracyMock.mockResolvedValue(accuracyResp([ACCURACY_ZERO_ROW]))
    const w1 = await mountView()
    await clickLoad(w1, 4)
    expect(countOccurrences(sectionOf(w1, 4).text(), 'COALESCE 编造')).toBe(2)

    // ★ 正常行 ⇒ 两条都不该出现（整表 0 + 逐行 0 = 0）
    accuracyMock.mockResolvedValue(accuracyResp([ACCURACY_ROW]))
    const w2 = await mountView()
    await clickLoad(w2, 4)
    expect(countOccurrences(sectionOf(w2, 4).text(), 'COALESCE 编造')).toBe(0)
  })

  it('★ 全 0 行在整表里存在时，逐行免责不能缺席（两处都要挂）', async () => {
    accuracyMock.mockResolvedValue(accuracyResp([ACCURACY_ROW, ACCURACY_ZERO_ROW]))
    const w = await mountView()
    await clickLoad(w, 4)
    const items = sectionOf(w, 4).findAll('.ar__item')
    expect(items.length).toBe(2)
    expect(items[0]!.text()).not.toContain('COALESCE 编造')
    expect(items[1]!.text()).toContain('COALESCE 编造')
  })

  it('★ 正常行**不**挂该免责', async () => {
    const w = await mountView()
    await clickLoad(w, 4)
    expect(sectionOf(w, 4).text()).not.toContain('COALESCE 编造')
  })

  it('★ 窗口 ≤7 显示 5 分钟桶', async () => {
    const w = await mountView()
    await clickLoad(w, 4)
    expect(sectionOf(w, 4).find('.ar__granularity').text()).toContain('5 分钟')
  })

  it('★ 窗口 30 天显示天桶（后端按窗口换物化视图）', async () => {
    const w = await mountView()
    await clickLoad(w, 4)
    const input = sectionOf(w, 4).find('input[type="number"]')
    await input.setValue(30)
    await clickLoad(w, 4)
    expect(accuracyMock).toHaveBeenLastCalledWith({ days: 30 })
    expect(sectionOf(w, 4).find('.ar__granularity').text()).not.toContain('5 分钟')
  })
})

describe('AutoRouteView / 失败路径', () => {
  it('★ 拉取失败 ⇒ 渲染错误，且**不**渲染任何空态文案', async () => {
    auditMock.mockRejectedValue(Object.assign(new Error('boom'), { status: 500 }))
    const w = await mountView()
    await clickLoad(w, 0)
    const sec = sectionOf(w, 0)
    expect(sec.find('.ar__msg--err').text()).toContain('boom')
    expect(sec.text()).not.toContain('没有数据')
    expect(sec.find('.ar__kpis').exists()).toBe(false)
  })

  it('★ 403 ⇒ 渲染「仅超管可查看」而不是原始错误', async () => {
    auditMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.ar__msg--err').text()).toContain('仅超管')
  })

  it('★ index 失败 ⇒ 不渲染「等待首轮刷新」或「没有数据」', async () => {
    indexMock.mockRejectedValue(new Error('idx boom'))
    const w = await mountView()
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    expect(sec.text()).toContain('idx boom')
    expect(sec.text()).not.toContain('等待后台首轮刷新')
    expect(sec.text()).not.toContain('没有数据')
  })

  it('★★ 重新加载失败 ⇒ **上一轮的数据必须被清掉**，不能与本轮错误并存', async () => {
    // ★ 必须「先成功、再失败」：只在首次就失败的话，audit.value 从来没被赋过值，
    //   「清不清」根本无从观测 —— 那样写的断言是恒真的。
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.ar__kpis').exists()).toBe(true)
    expect(sectionOf(w, 0).text()).toContain('1,200')

    auditMock.mockRejectedValue(Object.assign(new Error('reload boom'), { status: 500 }))
    await clickLoad(w, 0)

    const sec = sectionOf(w, 0)
    expect(sec.find('.ar__msg--err').text()).toContain('reload boom')
    expect(sec.find('.ar__kpis').exists()).toBe(false)
    expect(sec.text()).not.toContain('1,200')
  })
})