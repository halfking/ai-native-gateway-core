import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ProvidersView from './ProvidersView.vue'
import { getProviders, type Provider } from '@/api/providers'

/**
 * ProvidersView 供应商列表（2026-10-06）。
 *
 * 钉两条**分工**约定，它们是这个页面最容易写反的地方：
 *   ① 筛选条件进了**服务端 query**（后端 routability 参数，admin/handler.go:1224），
 *      所以换筛选必须**清缓存重取**；若只做本地过滤，用户点「不可用」看到的
 *      仍是全量里筛出来的结果，与后端口径不一致且看着像「过滤没生效」。
 *   ② 搜索是**客户端**过滤（同 search 实现，13 §5 debounce 250ms）。
 *      ⇒ 两者混用：一次「换筛选」调用应带上对应 routability，搜索则不应触发重取。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/providers', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/providers')>()
  return { ...actual, getProviders: vi.fn(async () => []) }
})

const P_DOWN: Provider = {
  id: 5, code: 'bedrock', display_name: 'Bedrock', catalog_code: 'aws',
  protocol: 'anthropic-messages', base_url: null, enabled: true,
  health_status: 'unreachable', routability: 'unavailable', manual_disabled: false,
  credential_count: 2, model_count: 6,
}
const P_OFF: Provider = {
  id: 7, code: 'canary', display_name: null, catalog_code: 'x',
  protocol: 'openai-completions', base_url: null, enabled: false,
  routability: 'manual_disabled', manual_disabled: true, credential_count: 1,
}
const P_OK: Provider = {
  id: 3, code: 'anthropic', display_name: 'Anthropic', catalog_code: 'anthropic',
  protocol: 'anthropic-messages', base_url: null, enabled: true,
  health_status: 'healthy', routability: 'available', manual_disabled: false,
  credential_count: 4, model_count: 12,
}

async function mountView() {
  const w = mount(ProvidersView, { attachTo: document.body })
  await flushPromises()
  await flushPromises()
  return w
}

describe('ProvidersView 供应商列表', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('首取不带 routability=all（全量），并渲染三家的状态', async () => {
    ;(getProviders as ReturnType<typeof vi.fn>).mockResolvedValueOnce([P_OK, P_DOWN, P_OFF])
    const w = await mountView()

    // 'all' 不该进 query —— 后端有特殊语义，全量就是不传
    expect(getProviders).toHaveBeenCalledWith({ routability: 'all' })
    const cards = w.findAll('.provider-card')
    expect(cards.length).toBe(3)
    const text = w.text()
    expect(text).toContain('Anthropic')
    expect(text).toContain('Bedrock')
    expect(text).toContain('Unavailable')
    expect(text).toContain('Manually disabled')
    // display_name 为 null 时回落到 code
    expect(text).toContain('canary')
  })

  it('★ 换筛选必须重取（带新 routability），不能只本地过滤', async () => {
    ;(getProviders as ReturnType<typeof vi.fn>).mockResolvedValueOnce([P_OK, P_DOWN, P_OFF])
    const w = await mountView()
    ;(getProviders as ReturnType<typeof vi.fn>).mockResolvedValueOnce([P_DOWN])

    const chip = w.findAll('.providers__chip').find((c) => c.text().includes('Unavailable'))
    if (!chip) throw new Error('不可用筛选 chip 不存在')
    await chip.trigger('click')
    await flushPromises()
    await flushPromises()

    // 关键：第二次调用必须带 routability='unavailable'，且卡片数随之后端结果收敛
    expect(getProviders).toHaveBeenCalledTimes(2)
    expect(getProviders).toHaveBeenLastCalledWith({ routability: 'unavailable' })
    const cards = w.findAll('.provider-card')
    expect(cards.length).toBe(1)
    expect(cards[0]?.text()).toContain('Bedrock')
  })

  it('搜索是客户端过滤，不触发重取', async () => {
    ;(getProviders as ReturnType<typeof vi.fn>).mockResolvedValueOnce([P_OK, P_DOWN, P_OFF])
    const w = await mountView()
    const callsBefore = (getProviders as ReturnType<typeof vi.fn>).mock.calls.length

    const input = w.get('input[type="search"]')
    await input.setValue('bedro')
    // 250ms debounce（13 §5）
    await new Promise((r) => setTimeout(r, 300))
    await flushPromises()

    expect((getProviders as ReturnType<typeof vi.fn>).mock.calls.length).toBe(callsBefore)
    const cards = w.findAll('.provider-card')
    expect(cards.length).toBe(1)
    expect(cards[0]?.text()).toContain('Bedrock')
  })

  it('计数只在字段存在时渲染（不把 undefined 打到屏幕上）', async () => {
    ;(getProviders as ReturnType<typeof vi.fn>).mockResolvedValueOnce([{ ...P_OFF, credential_count: undefined, model_count: undefined }])
    const w = await mountView()
    expect(w.text()).not.toContain('undefined')
  })
})
