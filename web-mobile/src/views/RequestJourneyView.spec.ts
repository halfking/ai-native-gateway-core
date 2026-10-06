import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import RequestJourneyView from './RequestJourneyView.vue'
import RequestJourneyDetailView from './RequestJourneyDetailView.vue'
import { fetchJourneyQueues, fetchJourneyDetail } from '@/api/requestJourney'
import { setLocale, locale } from '@/i18n'

/**
 * 排障线的核心不变量：**降级必须显示成降级，不能显示成「没有数据」**。
 *
 * 这条在 api 层已由 classifyJourneyDetail / isDegraded 守住；这里守的是
 * **渲染层** —— 因为「API 返回了正确的降级标记」和「用户看到了降级提示」
 * 是两件事，中间隔着模板。中间那一层此前从未被验证过。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/requestJourney', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/requestJourney')>()
  return { ...actual, fetchJourneyQueues: vi.fn(), fetchJourneyDetail: vi.fn() }
})

const queued = fetchJourneyQueues as unknown as ReturnType<typeof vi.fn>
const detailed = fetchJourneyDetail as unknown as ReturnType<typeof vi.fn>

let mountedList: Array<{ unmount(): void }> = []
/** 模块加载时的 locale，afterEach 复位用。 */
const ORIGIN_LOCALE = locale.value

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/journey/:id', name: 'journey-detail', component: { template: '<div />' } },
    ],
  })
}

async function mountQueues(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = makeRouter()
  await router.push('/')
  await router.isReady()
  const w = mount(RequestJourneyView, { attachTo: document.body, global: { plugins: [pinia, router] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  // ★ 显式钉 zh-CN。jsdom 的 navigator.language 是 en-US，所以默认跑的是英文；
  //   本文件的断言按中文写，若不钉住就全靠「词典里恰好有这句中文」才通过 ——
  //   那是 KeysView.spec 踩过的坑（见 2026-10-07 §11.29(3)）：
  //   当时它「通过」是因为 en 词典缺键回退到中文，而不是因为它测对了语言。
  setLocale('zh-CN')
  vi.clearAllMocks()
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
})
afterEach(() => {
  // locale 是模块级 ref，不复位会跨用例残留。
  setLocale(ORIGIN_LOCALE)
  vi.clearAllMocks()
})

const SNAP = {
  request_id: 'req-1',
  current_stage: 'forward',
  resolved_model: 'claude-sonnet-4-6',
  updated_at: '2026-10-07T10:00:00Z',
}

describe('RequestJourneyView 降级 ≠ 空', () => {
  it('observation_status=observation_degraded ⇒ 显示降级横幅', async () => {
    queued.mockResolvedValueOnce({
      view: 'total',
      observation_status: 'observation_degraded',
      observation_scope: 'instance_local',
      total_snapshot: { pending: 0, in_flight: 0, completed: 0, requests: [] },
    })
    const w = await mountQueues()
    const banner = w.find('.journey__banner--warn')
    expect(banner.exists()).toBe(true)
    // 关键：降级时**不能**让用户以为「一切正常」
    expect(w.text()).toContain('观测面降级')
  })

  it('★ 反向锁定：健康态下同样的空列表不得出现降级横幅', async () => {
    queued.mockResolvedValueOnce({
      view: 'total',
      observation_status: 'healthy',
      observation_scope: 'shared_redis',
      total_snapshot: { pending: 0, in_flight: 0, completed: 0, requests: [] },
    })
    const w = await mountQueues()
    // 「真的没有请求」是一个**真结论**，必须与「看不到」区分开
    expect(w.find('.journey__banner--warn').exists()).toBe(false)
    expect(w.find('.journey__banner').exists()).toBe(true) // 只剩 shared_redis 说明
  })

  // ★ 字段名必须是 observation_degraded。照抄仓库其它面的 `degraded` 会让
  //   判据恒为假 —— 这条用例就是为了锁死「不能改回 degraded」。
  it('裸 degraded 标记**不**触发横幅（字面值必须是 observation_degraded）', async () => {
    queued.mockResolvedValueOnce({
      view: 'total',
      observation_status: 'degraded' as never,
      total_snapshot: { requests: [] },
    })
    const w = await mountQueues()
    expect(w.find('.journey__banner--warn').exists()).toBe(false)
  })

  it('instance_local 提示只在非降级时出现', async () => {
    queued.mockResolvedValueOnce({
      view: 'total',
      observation_status: 'healthy',
      observation_scope: 'instance_local',
      total_snapshot: { requests: [] },
    })
    const w = await mountQueues()
    expect(w.text()).toContain('仅本实例可见')
  })

  it('有请求时渲染出卡片，模型名取 resolved_model', async () => {
    queued.mockResolvedValueOnce({
      view: 'total',
      observation_status: 'healthy',
      observation_scope: 'shared_redis',
      total_snapshot: { pending: 0, in_flight: 1, completed: 0, requests: [SNAP] },
    })
    const w = await mountQueues()
    const cards = w.findAll('.journey-card')
    expect(cards).toHaveLength(1)
    expect(cards[0]!.text()).toContain('claude-sonnet-4-6')
    expect(cards[0]!.text()).toContain('forward')
  })
})

describe('RequestJourneyDetailView 三态', () => {
  async function mountDetail(id: string): Promise<ReturnType<typeof mount>> {
    const pinia = createPinia()
    setActivePinia(pinia)
    const router = makeRouter()
    await router.push(`/journey/${id}`)
    await router.isReady()
    const w = mount(RequestJourneyDetailView, { attachTo: document.body, global: { plugins: [pinia, router] } })
    mountedList.push(w)
    await flushPromises()
    await flushPromises()
    return w
  }

  const JOURNEY = {
    tenant_id: 'default',
    gateway_instance_id: 'gw-1',
    request_id: 'req-1',
    observation_status: 'healthy',
    started_at: '2026-10-07T10:00:00Z',
    updated_at: '2026-10-07T10:00:05Z',
    events: [],
  }

  // ★★ request_journey.go:265-268：降级且无 journey 时后端返回 200。
  //   若视图把它显示成「查不到这条请求的链路」，就是断言了一个没有依据的结论。
  it('降级 + 200 无 journey ⇒ 显示「看不到」而不是「查不到」', async () => {
    detailed.mockResolvedValueOnce({ observation_status: 'observation_degraded' })
    const w = await mountDetail('req-1')
    const text = w.text()
    expect(text).toContain('观测面降级')
    expect(text).not.toContain('查不到这条请求的链路')
  })

  it('真的 404 ⇒ 显示「查不到」', async () => {
    detailed.mockRejectedValueOnce(Object.assign(new Error('not found'), { status: 404 }))
    const w = await mountDetail('req-1')
    expect(w.text()).toContain('查不到这条请求的链路')
  })

  it('非 404 错误不得显示成「查不到」（那会把排查方向指向不存在）', async () => {
    detailed.mockRejectedValueOnce(Object.assign(new Error('boom'), { status: 500 }))
    const w = await mountDetail('req-1')
    expect(w.text()).not.toContain('查不到这条请求的链路')
  })

  it('正常链路渲染出事件时间线', async () => {
    detailed.mockResolvedValueOnce({
      observation_status: 'healthy',
      journey: {
        ...JOURNEY,
        events: [
          {
            tenant_id: 'default',
            gateway_instance_id: 'gw-1',
            request_id: 'req-1',
            seq: 1,
            event_type: 'ingress',
            stage: 'ingress',
            model: 'claude-sonnet-4-6',
            observation_status: 'healthy',
            occurred_at: '2026-10-07T10:00:00Z',
          },
        ],
      },
    })
    const w = await mountDetail('req-1')
    expect(w.findAll('.jdetail__event')).toHaveLength(1)
    expect(w.text()).toContain('ingress')
  })
})
