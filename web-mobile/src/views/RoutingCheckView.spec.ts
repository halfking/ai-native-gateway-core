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
