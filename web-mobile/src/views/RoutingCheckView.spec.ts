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
