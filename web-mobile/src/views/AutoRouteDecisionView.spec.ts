import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AutoRouteDecisionView from './AutoRouteDecisionView.vue'
import { fetchAutoRouteDecision } from '@/api/autoRouteRead'
import { setLocale, locale } from '@/i18n'

/**
 * AutoRouteDecisionView 的不变量（2026-10-08，第五十七批）。
 *
 * ★ 本页最该被钉住的四条**全是「不能只说一半」**：
 *   ① **404 身兼两职**：不存在 **与** 跨租户被过滤都返 404
 *      （analytics.go:806-810）⇒ 不许只说「这个请求不存在」。
 *   ② **l2 缺失有两种说法**：形态上不可能（后端连查询都不发）／
 *      查了但表里没这行。二者必须分开说。
 *   ③ **l1 的 splat 覆盖不可判定**：有新键 = blob 跑过（有证据）；
 *      **没有新键不等于没被覆盖**（blob 可以只带一个同名键）。
 *   ④ **模型两个都空**才说「没有模型信息」；单侧空不能合并。
 */

const routeParams: Record<string, string> = {}

vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return { ...actual, useRoute: () => ({ params: routeParams }) }
})

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/autoRouteRead', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/autoRouteRead')>()
  return { ...actual, fetchAutoRouteDecision: vi.fn() }
})

const decisionMock = fetchAutoRouteDecision as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

type W = ReturnType<typeof mount>

async function mountView(): Promise<W> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(AutoRouteDecisionView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

async function typeAndLoad(w: W, id: string): Promise<void> {
  await w.find('input[type="text"]').setValue(id)
  await w.find('.ad__go').trigger('click')
  await flushPromises()
  await flushPromises()
}

/* ── 夹具：逐字照抄 analytics.go:817-841（无 l2）/ :889-916（有 l2） ── */

const HEX32 = 'a1b2c3d4e5f60718293a4b5c6d7e8f90'
const DASHED = 'a1b2c3d4-e5f6-0718-293a-4b5c6d7e8f90'
const PROBE_ID = 'probe-20261008-001'

function decisionNoL2(over: Record<string, unknown> = {}) {
  return {
    request_id: HEX32,
    ts: '2026-10-08T02:31:00Z',
    success: true,
    client_model: 'gpt-4o',
    outbound_model: 'gpt-4o-2024-11-20',
    api_key_id: 501,
    credential_id: 42,
    latency_ms: 1870,
    l1: { task_type: 'code', profile: 'smart', confidence: 0.91 },
    ...over,
  }
}

function decisionWithL2() {
  return {
    ...decisionNoL2(),
    l2: {
      ts: '2026-10-08T02:31:00Z',
      success: true,
      chosen_credential_id: 42,
      chosen_provider_id: 3,
      tier: 1,
      candidates_tried: 4,
      resolution_path: 'direct',
      canonical_model: 'gpt-4o',
      decision_trace: { planned_candidates: 4, blocked_candidates: 1 },
    },
  }
}

/** ★ 模型两个都空（`nullStringOrEmpty` 把 NULL 变成 `""`）。 */
const DECISION_NO_MODELS = decisionNoL2({ client_model: '', outbound_model: '' })

/** ★ blob 带新键 ⇒ splat 跑过的可判定证据。 */
const DECISION_SPLAT = decisionNoL2({
  l1: { task_type: 'from-blob', profile: 'smart', reason: 'quota' },
})

/** ★ 真正的盲区：blob 只带同名键、不带任何新键。 */
const DECISION_SHADOWED_SILENTLY = decisionNoL2({
  l1: { task_type: 'from-blob', profile: 'smart' },
})

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  decisionMock.mockResolvedValue(decisionNoL2())
  for (const k of Object.keys(routeParams)) delete routeParams[k]
  document.body.innerHTML = ''
})

afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE)
})

/* ═════════════════════════════════════════════════════════════════════════ */

describe('AutoRouteDecisionView / 形态校验', () => {
  it('★ 非法 id ⇒ 明说形态问题，按钮禁用，**不发请求**', async () => {
    const w = await mountView()
    await w.find('input[type="text"]').setValue('bad id!!')
    expect(decisionMock).not.toHaveBeenCalled()
    expect(w.find('.ad__go').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('请求 ID 形态非法')
  })

  it('★★ 回车触发 ⇒ load() 里的本地守卫必须自己拦（按钮禁用时那条路径根本走不到）', async () => {
    // ★ 反向样本：按钮是禁用的，点不出守卫；只有 @keyup.enter 才会真的调 load()。
    //   没有这条，「本地就拦、别拿明显非法的 id 去换一个 400」这条纪律是恒真的。
    const w = await mountView()
    await w.find('input[type="text"]').setValue('bad id!!')
    await w.find('input[type="text"]').trigger('keyup.enter')
    await flushPromises()
    expect(decisionMock).not.toHaveBeenCalled()
    expect(w.text()).toContain('请求 ID 形态非法')
  })

  it('★ 空输入 ⇒ 按钮禁用，不发请求', async () => {
    const w = await mountView()
    expect(w.find('.ad__go').attributes('disabled')).toBeDefined()
    expect(decisionMock).not.toHaveBeenCalled()
  })

  it('合法 id ⇒ 按钮可用并发出请求', async () => {
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(decisionMock).toHaveBeenCalledWith(HEX32)
    expect(w.find('.ad__go').attributes('disabled')).toBeUndefined()
  })

  it('★ 探测 id 形态上不会有 L2 段 —— **发请求之前**就提示', async () => {
    const w = await mountView()
    await w.find('input[type="text"]').setValue(PROBE_ID)
    expect(decisionMock).not.toHaveBeenCalled()
    expect(w.text()).toContain('形态上就不会有 L2 段')
  })

  it('★ 32 位 hex / dashed uuid 不给该提示', async () => {
    const w1 = await mountView()
    await w1.find('input[type="text"]').setValue(HEX32)
    expect(w1.text()).not.toContain('形态上就不会有 L2 段')
    const w2 = await mountView()
    await w2.find('input[type="text"]').setValue(DASHED)
    expect(w2.text()).not.toContain('形态上就不会有 L2 段')
  })
})

describe('AutoRouteDecisionView / 404 身兼两职', () => {
  it('★ 404 ⇒ 提示里必须包含「可能属于你不可见的租户」', async () => {
    decisionMock.mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }))
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    const err = w.find('.ad__msg--err')
    expect(err.exists()).toBe(true)
    expect(err.text()).toContain('不可见的租户')
    expect(err.text()).toContain('无法区分')
  })

  it('★ 404 ⇒ 不渲染任何 L1/L2 区块', async () => {
    decisionMock.mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }))
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.text()).not.toContain('L1 自动决策')
    expect(w.text()).not.toContain('L2 路由决策')
  })

  it('★★ 先成功回放、再 404 ⇒ 上一轮的 L1/L2 必须被清掉', async () => {
    // ★ 必须「先成功、再失败」：只在首次就失败的话 detail 从未被赋值，
    //   断言「L1 不出现」是恒真的（第五十六批 G2 踩过同一个坑）。
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.text()).toContain('L1 自动决策')

    decisionMock.mockRejectedValue(Object.assign(new Error('gone'), { status: 404 }))
    await typeAndLoad(w, DASHED)

    const text = w.text()
    expect(text).toContain('不可见的租户')
    expect(text).not.toContain('L1 自动决策')
    expect(text).not.toContain('L2 路由决策')
  })

  it('★ 403 ⇒ 渲染「仅超管」而不是原始错误', async () => {
    decisionMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.find('.ad__msg--err').text()).toContain('仅超管')
  })
})

describe('AutoRouteDecisionView / L1 splat 覆盖', () => {
  it('★ 有多余键 ⇒ 挂免责句并给库列打 shadowed 标记', async () => {
    decisionMock.mockResolvedValue(DECISION_SPLAT)
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.text()).toContain('可能已被覆盖')
    expect(w.findAll('.ad__shadowed').length).toBeGreaterThan(0)
  })

  it('★ 无多余键 ⇒ 不挂覆盖免责，但要挂「不能证明」的免责', async () => {
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.text()).not.toContain('可能已被覆盖')
    expect(w.find('.ad__echo').text()).toContain('不能')
    expect(w.findAll('.ad__shadowed').length).toBe(0)
  })

  it('★★ 盲区样本：blob 只带同名键 ⇒ 依然无证据，且**不能**显示「可信」', async () => {
    decisionMock.mockResolvedValue(DECISION_SHADOWED_SILENTLY)
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    // 值确实已被顶掉……
    expect(w.text()).toContain('from-blob')
    // ……但页面既没有说它可信，也仍然提示「不能证明」
    expect(w.text()).not.toContain('可能已被覆盖')
    expect(w.find('.ad__echo').text()).toContain('不能')
  })
})

describe('AutoRouteDecisionView / L2 条件键', () => {
  it('有 l2 ⇒ 渲染 L2 字段与轨迹字段数', async () => {
    decisionMock.mockResolvedValue(decisionWithL2())
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    const text = w.text()
    expect(text).toContain('chosen_credential_id')
    expect(text).toContain('resolution_path')
    expect(text).toContain('决策轨迹含 2 个字段')
  })

  it('★ 无 l2 且 id 形态可查 ⇒ 「表里没有这一行」', async () => {
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.text()).toContain('没有 L2 路由决策记录')
    expect(w.text()).toContain('但表里没有这一行')
  })

  it('★★ 无 l2 且 id 形态不可查 ⇒ 换成「形态上不会有」的说法', async () => {
    // 探测 id 形态合法（isValidRequestId 放行），但 uuidVariants 产不出 dashed 变体
    decisionMock.mockResolvedValue(decisionNoL2({ request_id: PROBE_ID }))
    const w = await mountView()
    await typeAndLoad(w, PROBE_ID)
    expect(w.text()).toContain('形态上就不会有 L2 段')
    expect(w.text()).not.toContain('但表里没有这一行')
  })

  it('★ 两种说法互斥，不会同时出现', async () => {
    const w1 = await mountView()
    await typeAndLoad(w1, HEX32)
    const t1 = w1.text()
    expect(t1.includes('但表里没有这一行')).toBe(true)
    expect(t1.includes('形态上就不会有 L2 段')).toBe(false)
  })
})

describe('AutoRouteDecisionView / 概要', () => {
  it('两个模型都空 ⇒ 「没有模型信息」并带 nodata 标记', async () => {
    decisionMock.mockResolvedValue(DECISION_NO_MODELS)
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    const cell = w.find('.ad__cell--nodata')
    expect(cell.exists()).toBe(true)
    expect(cell.text()).toBe('没有模型信息')
  })

  it('★ 单侧为空 **不** 合并成「没有模型信息」', async () => {
    decisionMock.mockResolvedValue(decisionNoL2({ client_model: '' }))
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.find('.ad__cell--nodata').exists()).toBe(false)
    expect(w.text()).toContain('gpt-4o-2024-11-20')
  })

  it('可选字段缺失时整行不出现', async () => {
    decisionMock.mockResolvedValue({
      request_id: HEX32,
      ts: '2026-10-08T02:31:00Z',
      success: false,
      client_model: 'a',
      outbound_model: 'b',
      l1: { task_type: 'code', profile: 'smart' },
    })
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    const text = w.text()
    expect(text).not.toContain('API Key')
    expect(text).not.toContain('凭据')
    expect(text).not.toContain('置信度')
  })

  it('success=false 时成功字段渲染「否」', async () => {
    decisionMock.mockResolvedValue({ ...decisionNoL2(), success: false })
    const w = await mountView()
    await typeAndLoad(w, HEX32)
    expect(w.text()).toContain('否')
  })
})
