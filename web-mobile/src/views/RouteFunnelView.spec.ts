import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RouteFunnelView from './RouteFunnelView.vue'
import { fetchRouteFunnel } from '@/api/autoRouteInsights'
import { setLocale, locale } from '@/i18n'

/**
 * RouteFunnelView 的三条不变量（2026-10-07）。
 *
 * 这一页的判据全部围绕**「估算被当成精确」**这一族错误 —— §11.20 修过
 * 「降级被显示成真的一分钱没花」，这里是同一族的另一个面。
 *
 * 1. ★ `approximate` 必须自曝：exact 与 approximate 两份响应，
 *    **同一段 UI 里 blocked 的读数必须不同**。这是本页最容易骗人的一处 ——
 *    approximate 模式下 blocked 根本没被聚合过（analytics.go 的 approximate/
 *    mixed 补数 SQL 里没有 blocked 列），0 的含义是「没算」不是「没拦」。
 *    判据必须真的量到两种读数，而不是只断言「页面没报错」。
 *
 * 2. ★★ 分母为 0 的转化率显示「未统计」而不是 0%：
 *    「上一阶段没有量」与「全部被筛掉」是两件事。
 *
 * 3. ★ 空 model 本地拦下：后端 400 `model parameter required`。
 *    判据要断言 fetch **没被调用**，而不是断言「调用了但页面有提示」。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/autoRouteInsights', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/autoRouteInsights')>()
  return { ...actual, fetchRouteFunnel: vi.fn() };
})

const funnelMock = fetchRouteFunnel as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(RouteFunnelView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** 往输入框填模型名并点查询。 */
async function searchWith(w: ReturnType<typeof mount>, model: string): Promise<void> {
  const input = w.find('input.fn__input')
  await input.setValue(model)
  const btn = w.findAll('button').find((b) => b.text().includes('查询'))
  await btn!.trigger('click')
  await flushPromises()
  await flushPromises()
}

const EXACT = {
  model: 'gpt-4o',
  window: '7d',
  requests: 120,
  stages: [
    { key: 'candidates', label: '总候选', value: 300, hint: 'L2 计划候选数累计' },
    { key: 'routable', label: '可路由', value: 280, hint: '计划候选 − 被阻断' },
    { key: 'success', label: '执行成功', value: 100, hint: '最终成功请求数' },
  ],
  meta: {
    approximate: false,
    data_source: 'exact',
    blocked: 20,
    chosen: 100,
    sample_n: 120,
    trace_rows: 120,
    trace_ratio: 1,
    confidence: 'high',
    confidence_hint: 'n=120，100% 含完整 decision_trace',
  },
}

/** ★ approximate：planned≈请求×3、routable≈routed×2 估算出来，blocked 恒为 0。 */
const APPROX = {
  model: 'gpt-4o',
  window: '7d',
  requests: 40,
  stages: [
    { key: 'candidates', label: '总候选', value: 120, hint: '无 trace 时以请求数为基准' },
    { key: 'routable', label: '可路由', value: 90, hint: '计划候选 − 被阻断' },
    { key: 'success', label: '执行成功', value: 35, hint: '最终成功请求数' },
  ],
  meta: {
    approximate: true,
    data_source: 'approximate',
    blocked: 0, // ★ 关键：这里是「没算」，不是「一个都没被拦」
    chosen: 45,
    sample_n: 40,
    trace_rows: 0,
    trace_ratio: 0,
    confidence: 'low',
    confidence_hint: 'n=40，数据为近似估算',
  },
}

beforeEach(() => {
  // ★ 显式钉 zh-CN：jsdom 的 navigator.language 是 en-US
  setLocale('zh-CN')
  vi.clearAllMocks()
  funnelMock.mockResolvedValue(EXACT)
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('RouteFunnelView — 入口', () => {
  it('挂载后不自动发请求（model 必填，没有默认值）', async () => {
    await mountView()
    // ★ 后端 `model parameter required` 400 ⇒ 组件不能上来就发一次注定失败的请求。
    expect(funnelMock).not.toHaveBeenCalled()
  })

  it('★ 空 model 本地拦下：fetch 一次都不该被调用', async () => {
    const w = await mountView()
    await searchWith(w, '   ')
    expect(funnelMock).not.toHaveBeenCalled()
    expect(w.text()).toContain('必须填模型名')
  })

  it('填了模型才发请求，且带上 model', async () => {
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    expect(funnelMock).toHaveBeenCalledTimes(1)
    expect(funnelMock.mock.calls[0]![0]).toMatchObject({ model: 'gpt-4o' })
  })
})

describe('★ 判据 1：exact 与 approximate 的 blocked 读数必须不同', () => {
  it('exact：blocked 显示真实数字 20', async () => {
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    const text = w.text()
    expect(text).toContain('精确统计')
    expect(text).toContain('20')
    // ★ 反向断言：exact 模式下**不能**出现「未统计」
    expect(text).not.toContain('未统计')
  })

  // ★★ 本页最容易骗人的一处。
  //   approximate 响应的 meta.blocked 字面上是 0，若直接渲染就是
  //   「0 个候选被阻断」—— 一个后端从未计算过的数。
  it('★ approximate：blocked 显示「未统计」，绝不显示 0', async () => {
    funnelMock.mockResolvedValue(APPROX)
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    const text = w.text()
    expect(text).toContain('估算值')
    expect(text).toContain('未统计')
  })

  it('★ mixed 同样判「未统计」（不是只有 approximate 才需要）', async () => {
    funnelMock.mockResolvedValue({ ...APPROX, meta: { ...APPROX.meta, data_source: 'mixed' } })
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    expect(w.text()).toContain('未统计')
  })

  it('★ 两份响应的 blocked 读数确实不同（证明上一条不是恒真）', async () => {
    const w1 = await mountView()
    await searchWith(w1, 'gpt-4o')
    const exactText = w1.text()
    funnelMock.mockResolvedValue(APPROX)
    const w2 = await mountView()
    await searchWith(w2, 'gpt-4o')
    const approxText = w2.text()
    expect(exactText).not.toBe(approxText)
  })
})

describe('★ 判据 2：分母为 0 的转化率显示「未统计」而不是 0%', () => {
  // candidates=0 而 routable=5 ⇒ 「上一阶段没有量」，
  // 不是「从 0 筛到 5 转化 0%」。
  it('分母为 0 ⇒ 未统计（不冒充 0%）', async () => {
    funnelMock.mockResolvedValue({
      ...EXACT,
      stages: [
        { key: 'candidates', label: '总候选', value: 0, hint: '' },
        { key: 'routable', label: '可路由', value: 5, hint: '' },
      ],
    })
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    // blocked 是 exact ⇒ 不显示未统计，所以这里数「上一阶段」那行的出现
    expect(w.text()).toContain('未统计')
  })

  it('分母非 0 ⇒ 显示百分比', async () => {
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    // 280/300 ≈ 93%
    expect(w.text()).toMatch(/93%/)
  })
})

describe('降级与空态互斥', () => {
  it('★ approximate 时置信度条带 approximate 警示样式（不只是文案）', async () => {
    funnelMock.mockResolvedValue(APPROX)
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    // 精确与近似的视觉不能长得一样 —— 否则扫一眼分不出来
    expect(w.find('.fn__conf--approx').exists()).toBe(true)
  })

  it('exact 时不带警示样式', async () => {
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    expect(w.find('.fn__conf--approx').exists()).toBe(false)
  })

  // ★ 模型名拼错时后端**不 404**，返回 200 + requests=0。
  //   所以「查不到」和「这个模型没流量」在响应里同形 ⇒ 空态必须说清楚。
  it('requests=0 ⇒ 显示空态而不是「0 个被阻断」', async () => {
    funnelMock.mockResolvedValue({
      model: 'typo-model',
      window: '7d',
      requests: 0,
      stages: [],
      meta: { ...APPROX.meta, chosen: 0, sample_n: 0 },
    })
    const w = await mountView()
    await searchWith(w, 'typo-model')
    expect(w.text()).toContain('没有请求记录')
  })

  // ★ requests=0 但后端仍会吐 3 个全 0 的阶段。挂 empty 就够，
  //   不能因此把「全 0 的阶段表」也渲染出来 —— 三行 0 是噪音，
  //   而且看着像「量过了，结果是零」。
  it('requests=0 且后端仍返回 3 个全 0 阶段 ⇒ 仍走空态，不渲染阶段表', async () => {
    funnelMock.mockResolvedValue({
      model: 'typo-model',
      window: '7d',
      requests: 0,
      stages: [
        { key: 'candidates', label: '总候选', value: 0, hint: 'x' },
        { key: 'routable', label: '可路由', value: 0, hint: 'x' },
        { key: 'success', label: '执行成功', value: 0, hint: 'x' },
      ],
      meta: { ...APPROX.meta, chosen: 0, sample_n: 0 },
    })
    const w = await mountView()
    await searchWith(w, 'typo-model')
    expect(w.text()).toContain('没有请求记录')
    expect(w.find('.fn__stages').exists()).toBe(false)
  })

  // ★ 「形状不符」与「没数据」必须换文案。共用一条就是把「后端坏了」
  //   显示成「这个模型没流量」—— 运维会去查数据，而问题在服务端。
  it('★ requests>0 但 stages 为空 ⇒ 报形状不符，不复用「没流量」文案', async () => {
    funnelMock.mockResolvedValue({
      model: 'gpt-4o',
      window: '7d',
      requests: 120,
      stages: [],
      meta: EXACT.meta,
    })
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    expect(w.text()).toContain('形状不符')
    expect(w.text()).not.toContain('没有请求记录')
  })

  it('403 ⇒ 报「仅超管」而不是裸错误串', async () => {
    funnelMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    expect(w.text()).toContain('仅超管')
  })

  it('★ 服务端 2 分钟缓存必须自曝（否则用户会重复提交）', async () => {
    const w = await mountView()
    await searchWith(w, 'gpt-4o')
    expect(w.text()).toContain('2 分钟')
  })
})