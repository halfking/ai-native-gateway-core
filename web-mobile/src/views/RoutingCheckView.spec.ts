import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import RoutingCheckView from './RoutingCheckView.vue'
import { resolveRouting } from '@/api/credentialsOps'
import { ApiError } from '@/api/client'

/**
 * 路由检查视图（17 §2 desktopOnly 让位轮，2026-10-06）。
 *
 * 钉三条**契约性**行为，都是从后端实现推出来的、容易写错的地方：
 *   ① 「无变体」时后端返回的是**合法空结果**（candidates: []）而不是错误
 *      （admin/routing.go:288-296）⇒ 视图必须显示「无候选」，不能进错误态。
 *      ★ 反过来做也过不了：把空结果当错误，会让「模型名打错」显示成「加载失败」。
 *   ② 候选分「可路由 / 被阻塞」两组显示 —— 后端特意始终返回全部候选并带阻塞
 *      原因（admin/routing.go:273-275 注释），被阻塞的才是 explain 的价值所在。
 *   ③ model 为空不发请求（后端 model 缺失直接 400，:272-275）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/credentialsOps', () => ({
  resolveRouting: vi.fn(),
}))

const RESOLVE_OK = {
  client_model: 'claude-sonnet-4.6',
  canonical_name: 'claude-sonnet-4-6',
  canonical_id: 12,
  resolution_path: 'alias_bridge',
  raw_models: ['claude-sonnet-4.6', 'claude-sonnet-4-6'],
  plan_order: [],
  candidates: [
    {
      rank: 1, provider_id: 3, provider_name: 'Anthropic', catalog_code: 'anthropic',
      protocol: 'anthropic-messages', credential_id: 7, credential_label: 'main',
      credential_status: 'ok', lifecycle_status: 'active', availability_state: 'ready',
      availability_recover_at: null, quota_state: 'ok', quota_recover_at: null,
      concurrency_limit: 20, effective_concurrency: 1, circuit_state: 'closed',
      available: true, tier: 1, weight: 100, success_rate: 0.99, p95_latency_ms: 820,
    },
    {
      rank: 2, provider_id: 5, provider_name: 'Bedrock', catalog_code: 'aws',
      protocol: 'anthropic-messages', credential_id: 9, credential_label: 'spare',
      credential_status: 'ok', lifecycle_status: 'active', availability_state: 'down',
      availability_recover_at: '2026-10-06T18:00:00Z', quota_state: 'ok', quota_recover_at: null,
      concurrency_limit: 10, effective_concurrency: 0, circuit_state: 'open',
      available: false, tier: 2, weight: 50, success_rate: 0.1, p95_latency_ms: 5000,
      block_reason: 'circuit_open',
    },
  ],
}

// 后端 admin/routing.go:288-296：无变体时返回的**合法空响应**。
const RESOLVE_EMPTY = {
  client_model: 'no-such-model',
  canonical_name: 'no-such-model',
  canonical_id: null,
  resolution_path: 'direct',
  raw_models: ['no-such-model'],
  plan_order: [],
  candidates: [],
}

async function mountView() {
  const w = mount(RoutingCheckView, { attachTo: document.body })
  await flushPromises()
  return w
}

async function queryWith(w: Awaited<ReturnType<typeof mountView>>, model: string) {
  const input = w.get('input[type="search"]')
  await input.setValue(model)
  const go = w.findAll('.btn').find((b) => b.text().includes('Check'))
  if (!go) throw new Error('检查按钮不存在')
  await go.trigger('click')
  await flushPromises()
  await flushPromises()
}

describe('RoutingCheckView 路由检查', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('模型名为空时不发请求（后端空 model 直接 400）', async () => {
    const w = await mountView()
    expect(resolveRouting).not.toHaveBeenCalled()
    expect(w.text()).not.toContain('Routable candidates')
  })

  it('可路由与被阻塞候选分组显示，阻塞原因可见', async () => {
    ;(resolveRouting as ReturnType<typeof vi.fn>).mockResolvedValueOnce(RESOLVE_OK)
    const w = await mountView()
    await queryWith(w, 'claude-sonnet-4.6')

    expect(resolveRouting).toHaveBeenCalledWith('claude-sonnet-4.6')
    const text = w.text()
    // 可路由组
    expect(text).toContain('Routable candidates')
    expect(text).toContain('Routable')
    // 被阻塞组 + 原因（explain 的价值所在）
    expect(text).toContain('Blocked candidates (1)')
    expect(text).toContain('circuit_open')
    expect(text).toContain('Circuit open')
  })

  it('★ 空候选是合法结果，显示「无候选」而不是错误态', async () => {
    ;(resolveRouting as ReturnType<typeof vi.fn>).mockResolvedValueOnce(RESOLVE_EMPTY)
    const w = await mountView()
    await queryWith(w, 'no-such-model')

    const text = w.text()
    expect(text).toContain('No candidate credentials')
    // ★ 不得退化成错误提示 —— 那会把「模型名打错」说成「加载失败」
    expect(text).not.toContain('Load failed')
    expect(w.find('.routing__error').exists()).toBe(false)
  })

  it('后端 400 单独给可执行提示，不显示英文原文', async () => {
    ;(resolveRouting as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new ApiError(400, 'model parameter required'),
    )
    const w = await mountView()
    await queryWith(w, 'x')

    const err = w.find('.routing__error')
    if (!err.exists()) throw new Error('错误态未渲染')
    expect(err.text()).toContain('Invalid query parameter')
  })
})

/**
 * 全量可路由性总览（2026-10-08，admin/routing.go:2075）。
 *
 * ★ 这组用例守三件**后端事实导致的行为**：
 *   ① 按需加载 —— overview 是全网笛卡尔积，不能随 explain 的输入框自动拉。
 *   ② featured 为空 ⇒ 「只看精选」过滤整个不下发 ⇒ 退化成全量且无提示，必须显式告知。
 *   ③ 编造默认值（COALESCE 0.9 / 9999）⇒ UI 不能把这两个数当实测指标展示。
 */
vi.mock('@/api/routingRead', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/routingRead')>()
  return {
    ...actual,
    fetchRoutingOverview: vi.fn(),
  }
})

function ovRow(over: Record<string, unknown> = {}) {
  return {
    model_name: 'gpt-4o', provider_id: 3, provider_name: '小米大模型', catalog_code: 'xiaoai',
    protocol: 'openai', base_url: 'https://api.mi.com/v1', provider_enabled: true,
    credential_id: 9, credential_label: 'tok-a', credential_status: 'active',
    lifecycle_status: 'active', availability_state: 'ready', availability_recover_at: null,
    quota_state: 'ok', quota_recover_at: null, balance_usd: 1, effective_at: null, expires_at: null,
    circuit_state: 'closed', cooling_until: null, available: true, tier: 1, weight: 100,
    unit_price_in_per_1m: 1, unit_price_out_per_1m: 2, currency: 'USD',
    success_rate: 0.98, p95_latency_ms: 800, standardized_name: 'gpt-4o',
    runtime_routable: true, routable: true,
    ...over,
  }
}

describe('RoutingCheckView 全量总览', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  async function mountAndLoad(payload: unknown) {
    const { fetchRoutingOverview } = await import('@/api/routingRead')
    ;(fetchRoutingOverview as ReturnType<typeof vi.fn>).mockResolvedValue(payload as never)
    const w = mount(RoutingCheckView)
    await flushPromises()
    const btn = w.findAll('button').find((b) => (b.text() ?? '').includes('Load overview'))
    if (!btn) throw new Error('加载总览按钮不存在')
    await btn.trigger('click')
    await flushPromises()
    await flushPromises()
    return w
  }

  it('★★★★ 按需加载：进页面**不**自动请求总览', async () => {
    const { fetchRoutingOverview } = await import('@/api/routingRead')
    mount(RoutingCheckView)
    await flushPromises()
    expect(fetchRoutingOverview).not.toHaveBeenCalled()
    // 入口按钮在
    expect(mount(RoutingCheckView).html()).toContain('Load overview')
  })

  it('★★★ 加载后按「可路由 / 被阻塞」两栏呈现，并列出阻塞原因', async () => {
    const w = await mountAndLoad({
      featured: ['gpt-4o'],
      rows: [
        ovRow(),
        ovRow({ credential_id: 10, model_name: 'gpt-5', runtime_routable: false, routable: false, runtime_block_reason: 'circuit_open' }),
      ],
    })
    const text = w.text()
    expect(text).toContain('circuit_open')
    // 两栏计数都在
    expect(text).toContain('Routable')
    expect(text).toContain('Blocked')
    expect(text).toContain('gpt-5')
    // ★★ 条目数必须精确为 1：夹具有 2 行（1 可路由 + 1 阻塞）。
    //   只断言「阻塞原因出现了」会被「阻塞栏返回全部行」这种变异蒙混过去
    //   —— 因为可路由那行里也写着同一个值。这条是 E5 的判据。
    expect(w.findAll('.routing__ov-item').length).toBe(1)
  })

  it('★★★★★ featured 为空 ⇒ 显式提示「只看精选」不会生效', async () => {
    const w = await mountAndLoad({ featured: [], rows: [ovRow()] })
    expect(w.text()).toContain('featured only')
  })

  it('★★★★★ 编造默认值（0.9 / 9999）必须带免责，不能当实测展示', async () => {
    const w = await mountAndLoad({
      featured: ['gpt-4o'],
      rows: [ovRow({ credential_id: 11, model_name: 'never-probed', runtime_routable: false, routable: false, runtime_block_reason: 'offer_unavailable', success_rate: 0.9, p95_latency_ms: 9999 })],
    })
    expect(w.text()).toContain('may be defaults')
  })

  it('★★★ routable 与 runtime_routable 不等 ⇒ 提示契约漂移', async () => {
    const w = await mountAndLoad({
      featured: ['gpt-4o'],
      rows: [ovRow({ credential_id: 12, model_name: 'drift', runtime_routable: false, routable: true, runtime_block_reason: 'quota_exhausted' })],
    })
    expect(w.text()).toContain('contract drift')
  })

  it('★★★ 拉取失败显示错误态，且不牵连 explain 区', async () => {
    const { fetchRoutingOverview } = await import('@/api/routingRead')
    ;(fetchRoutingOverview as ReturnType<typeof vi.fn>).mockRejectedValue(new ApiError(403, 'forbidden'))
    const w = mount(RoutingCheckView)
    await flushPromises()
    const btn = w.findAll('button').find((b) => (b.text() ?? '').includes('Load overview'))
    await btn?.trigger('click')
    await flushPromises()
    // ★ 用实际文案而非「permission」：describeError 走的是 routing.errForbidden
    expect(w.text()).toContain('Your account cannot view routing details')
    // explain 的输入框还在
    expect(w.html()).toContain('routing__hint')
  })
})

/**
 * 模型路由树 / 熔断健康 / 可用模型名单的视图接入（2026-10-08）。
 *
 * ★★★ 这组用例的核心是**两种形状必须分流渲染**：
 *   后端 `model-tree` 按角色返回两种结构（`readonly: true` 标记），
 *   而 `available` 在两侧**层级与语义都不同**。
 *   若不分开渲染，裁剪树会因 `v.credentials` 为 undefined 而整段空掉，
 *   完整树则会显示出一个恒为 undefined 的「可用」列。
 */
vi.mock('@/api/routingTree', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/routingTree')>()
  return {
    ...actual,
    fetchModelTree: vi.fn(),
    fetchRoutingHealth: vi.fn(),
    fetchAvailableModelsRaw: vi.fn(),
  }
})

describe('RoutingCheckView 模型路由树（按形状分流）', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  async function loadTree(payload: unknown) {
    const { fetchModelTree } = await import('@/api/routingTree')
    ;(fetchModelTree as ReturnType<typeof vi.fn>).mockResolvedValue(payload as never)
    const w = mount(RoutingCheckView)
    await flushPromises()
    const btn = w.findAll('button').find((b) => (b.text() ?? '').includes('Load tree'))
    if (!btn) throw new Error('加载路由树按钮不存在')
    await btn.trigger('click')
    await flushPromises()
    await flushPromises()
    return w
  }

  it('★★★★ 按需加载：进页面不自动请求', async () => {
    const { fetchModelTree } = await import('@/api/routingTree')
    mount(RoutingCheckView)
    await flushPromises()
    expect(fetchModelTree).not.toHaveBeenCalled()
  })

  it('★★★★★ 裁剪形状（readonly）⇒ 显示「明细已隐藏」+ 凭据个数，**不列**凭据', async () => {
    const w = await loadTree({
      featured: ['gpt-4o'],
      series: [
        {
          series: 'gpt',
          generations: [
            {
              generation: 'gpt-4',
              variants: [{ variant: 'gpt-4o', canonical_name: 'gpt-4o', tags: [], available: true, credential_count: 2 }],
            },
          ],
        },
      ],
      unmapped: [],
      readonly: true,
    })
    const text = w.text()
    expect(text).toContain('credential details are hidden')
    expect(text).toContain('2 credentials')
    expect(text).toContain('all credentials available')
    // ★ 裁剪形状根本没有凭据明细，绝不能凭 variant 名渲染出凭据行
    expect(w.findAll('.routing__tree-cred').length).toBe(0)
  })

  it('★★★★★ 完整形状 ⇒ 逐凭据列出，且**不显示**「明细已隐藏」', async () => {
    const w = await loadTree({
      featured: ['gpt-4o'],
      series: [
        {
          series: 'gpt',
          generations: [
            {
              generation: 'gpt-4',
              variants: [
                {
                  variant: 'gpt-4o',
                  canonical_name: 'gpt-4o',
                  tags: [],
                  credentials: [
                    {
                      credential_id: 9, credential_label: 'tok-a', credential_status: 'active',
                      provider_id: 3, provider_name: '小米大模型', available: true, tier: 1, weight: 100,
                      unit_price_in_per_1m: 1, unit_price_out_per_1m: 2, success_rate: 0.98,
                      p95_latency_ms: 800, currency: 'USD', availability_state: 'ready',
                    },
                    {
                      // ★ 第二个凭据**不可用** ⇒ 「全部可用」的全称判断才有区分度。
                      //   没有它，「fold 恒 false / 恒 true」两种变异都测不出来。
                      credential_id: 10, credential_label: 'tok-b', credential_status: 'active',
                      provider_id: 3, provider_name: '小米大模型', available: false, tier: 2, weight: 100,
                      unit_price_in_per_1m: 1, unit_price_out_per_1m: 2, success_rate: 0.5,
                      p95_latency_ms: 5000, currency: 'USD', availability_state: 'down',
                    },
                  ],
                },
              ],
            },
          ],
        },
      ],
      unmapped: [],
    })
    const text = w.text()
    expect(text).not.toContain('credential details are hidden')
    expect(text).toContain('tok-a')
    expect(text).toContain('tok-b')
    expect(w.findAll('.routing__tree-cred').length).toBe(2)
    // ★★★ 完整形状自己 fold 出「部分不可用」；且**不出现**裁剪形状那档文案
    expect(text).toContain('some credentials unavailable')
    expect(text).not.toContain('all credentials available')
    expect(text).not.toContain('credentials (details hidden)')
    // ★★★★ 必须**结构性**断言是哪种判据算出来的：两处文案可能相同，
    //   只看文本分不开「裁剪形状的 available」与「完整形状的 fold」。
    expect(w.findAll('.routing__agg-full').length).toBe(1)
    expect(w.findAll('.routing__agg-redacted').length).toBe(0)
  })

  it('★★★★★ 全部凭据都可用 ⇒ fold 判「全部可用」（恒 false 的变异才测得出）', async () => {
    // ★ 这条是补的：前一个夹具里有一条不可用，于是「fold 恒 false」与正确实现
    //   **输出完全相同** —— 测的是巧合。
    const w = await loadTree({
      featured: ['gpt-4o'],
      series: [
        {
          series: 'gpt',
          generations: [
            {
              generation: 'gpt-4',
              variants: [
                {
                  variant: 'gpt-4o',
                  canonical_name: 'gpt-4o',
                  tags: [],
                  credentials: [
                    { credential_id: 9, credential_label: 'tok-a', credential_status: 'active',
                      provider_id: 3, provider_name: 'p', available: true, tier: 1, weight: 100,
                      unit_price_in_per_1m: 1, unit_price_out_per_1m: 2, success_rate: 0.9,
                      p95_latency_ms: 9999, currency: 'USD', availability_state: 'ready' },
                  ],
                },
              ],
            },
          ],
        },
      ],
      unmapped: [],
    })
    const text = w.text()
    expect(text).toContain('all credentials available')
    expect(text).not.toContain('some credentials unavailable')
    expect(w.findAll('.routing__agg-full').length).toBe(1)
    expect(w.findAll('.routing__agg-redacted').length).toBe(0)
  })

  it('★★★★★ 裁剪形状的变体若**意外带了** credentials，也不得逐条渲染（形状判据优先）', async () => {
    // ★ 这条是补的：夹具里裁剪形状本来就没有 credentials ⇒
    //   「凭据列表不按形状分流」这种变异照样全绿 —— 测的是巧合。
    const w = await loadTree({
      featured: ['gpt-4o'],
      series: [
        {
          series: 'gpt',
          generations: [
            {
              generation: 'gpt-4',
              variants: [
                {
                  variant: 'gpt-4o', canonical_name: 'gpt-4o', tags: [], available: true, credential_count: 2,
                  // ★★ 契约漂移：readonly 形状却带了 credentials
                  credentials: [
                    { credential_id: 9, credential_label: 'SHOULD-NOT-RENDER', credential_status: 'active',
                      provider_id: 3, provider_name: 'p', available: true, tier: 1, weight: 100,
                      unit_price_in_per_1m: 1, unit_price_out_per_1m: 2, success_rate: 0.9,
                      p95_latency_ms: 1, currency: 'USD', availability_state: 'ready' },
                  ],
                },
              ],
            },
          ],
        },
      ],
      unmapped: [],
      readonly: true,
    })
    const text = w.text()
    expect(text).not.toContain('SHOULD-NOT-RENDER')
    expect(w.findAll('.routing__tree-cred').length).toBe(0)
    expect(text).toContain('2 credentials')
  })

  it('★★★★★ 状态未知被后端写成 ready ⇒ 必须显示「状态未知」而不是「就绪」', async () => {
    const w = await loadTree({
      featured: [],
      series: [
        {
          series: 'gpt',
          generations: [
            {
              generation: 'gpt-4',
              variants: [
                {
                  variant: 'gpt-4o',
                  canonical_name: 'gpt-4o',
                  tags: [],
                  credentials: [
                    {
                      credential_id: 9, credential_label: 'tok-a', credential_status: 'unknown',
                      provider_id: 3, provider_name: '小米大模型', available: true, tier: 1, weight: 100,
                      unit_price_in_per_1m: 1, unit_price_out_per_1m: 2, success_rate: 0.9,
                      p95_latency_ms: 9999, currency: 'USD', availability_state: 'ready',
                    },
                  ],
                },
              ],
            },
          ],
        },
      ],
      unmapped: [],
    })
    const text = w.text()
    expect(text).toContain('status unknown')
    // ★ 编造的 0.9 / 9999 也要带免责
    expect(text).toContain('may be defaults')
  })

  it('★★★ featured 为空 ⇒ 提示「只看精选」不会生效', async () => {
    const w = await loadTree({ featured: [], series: [], unmapped: [] })
    expect(w.text()).toContain('featured only')
  })

  it('★★★★ 空树必须显示空态，而不是一片空白', async () => {
    const w = await loadTree({ featured: ['gpt-4o'], series: [], unmapped: [] })
    expect(w.text()).toContain('No matching model bindings')
  })
})

describe('RoutingCheckView 熔断健康与可用模型名单', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('★★★★ summary 与实际列表对不上 ⇒ 提示契约漂移', async () => {
    const { fetchRoutingHealth } = await import('@/api/routingTree')
    ;(fetchRoutingHealth as ReturnType<typeof vi.fn>).mockResolvedValue({
      credentials: [
        { credential_id: 9, label: 'tok-a', status: 'active', circuit_state: 'open', consecutive_failures: 3, circuit_open_count_window: 2, cooling_until: null, provider_name: 'p', catalog_code: null },
        { credential_id: 10, label: 'SHOULD-NOT-LIST-AS-OPEN', status: 'active', circuit_state: 'closed', consecutive_failures: 0, circuit_open_count_window: 0, cooling_until: null, provider_name: 'p', catalog_code: null },
      ],
      // ★ 对不上：列表 1 条但 summary 说 5 条
      summary: { total: 5, open: 0, closed: 5 },
    } as never)
    const w = mount(RoutingCheckView)
    await flushPromises()
    const btn = w.findAll('button').find((b) => (b.text() ?? '').includes('Load health'))
    await btn?.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('contract drift')
    // 熔断中的那条要列出来
    expect(w.text()).toContain('tok-a')
    // ★★ 而 closed 的那条**不能**被列进「熔断中」
    expect(w.text()).not.toContain('SHOULD-NOT-LIST-AS-OPEN')
  })

  it('★★★ 没有熔断中的凭据 ⇒ 显示空态', async () => {
    const { fetchRoutingHealth } = await import('@/api/routingTree')
    ;(fetchRoutingHealth as ReturnType<typeof vi.fn>).mockResolvedValue({
      credentials: [
        { credential_id: 10, label: 'tok-b', status: 'active', circuit_state: 'closed', consecutive_failures: 0, circuit_open_count_window: 0, cooling_until: null, provider_name: 'p', catalog_code: null },
      ],
      summary: { total: 1, open: 0, closed: 1 },
    } as never)
    const w = mount(RoutingCheckView)
    await flushPromises()
    const btn = w.findAll('button').find((b) => (b.text() ?? '').includes('Load health'))
    await btn?.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('No credential is currently open')
  })

  it('★★★★★ 名单拉取失败 ⇒ 不得渲染成「零个可用模型」（故障 ≠ 真结论）', async () => {
    const { fetchAvailableModelsRaw } = await import('@/api/routingTree')
    ;(fetchAvailableModelsRaw as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('boom'))
    const w = mount(RoutingCheckView)
    await flushPromises()
    const btn = w.findAll('button').find((b) => (b.text() ?? '').includes('Load names'))
    await btn?.trigger('click')
    await flushPromises()
    await flushPromises()
    // ★ 端点挂了，但清单非关键 ⇒ 什么都不显示，**绝不能**说「没有可用模型」
    expect(w.text()).not.toContain('No model is currently available')
  })

  it('★★★ 可用模型名单为零 ⇒ 显示「没有可用模型」，不是错误态', async () => {
    const { fetchAvailableModelsRaw } = await import('@/api/routingTree')
    ;(fetchAvailableModelsRaw as ReturnType<typeof vi.fn>).mockResolvedValue([] as never)
    const w = mount(RoutingCheckView)
    await flushPromises()
    const btn = w.findAll('button').find((b) => (b.text() ?? '').includes('Load names'))
    await btn?.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('No model is currently available')
    expect(w.findAll('.routing__error').length).toBe(0)
  })
})
