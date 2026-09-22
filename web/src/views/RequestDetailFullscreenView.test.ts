import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import type { WaterfallRequest } from '../api/dispatch'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const loader = {
  metaLoading: ref(false),
  metaError: ref(''),
  log: ref(null),
  unified: ref(null),
  sessionSnap: ref(null),
  sessionId: ref<string | null>(null),
  requestBody: ref(null),
  responseBody: ref(null),
  outboundBody: ref(null),
  waterfallLoading: ref(false),
  waterfallError: ref(''),
  waterfall: ref<WaterfallRequest | null>(null),
  waterfallSource: ref(''),
  attempts: ref([]),
  loadMeta: vi.fn(async () => {}),
  onSectionNeed: vi.fn(async () => {}),
  dispose: vi.fn(),
}

vi.mock('../composables/useRequestDetailLoader', () => ({
  useRequestDetailLoader: () => loader,
}))

import RequestDetailFullscreenView from './RequestDetailFullscreenView.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { credentialFallback: '凭据' },
      requestDetail: {
        waterfall: {
          loading: '加载调度瀑布…',
          source: '数据源：{src}',
          stageHeader: 'T0–T9 阶段',
          cols: { stage: '阶段', duration: '耗时', source: '来源' },
          syntheticTag: '合成',
          measuredTag: '实测',
          empty: '暂无瀑布时间线',
          noAttempts: '无 Attempts 记录。',
        },
      },
    },
  },
})

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/request-detail/:requestId',
        name: 'request-detail',
        component: RequestDetailFullscreenView,
      },
      { path: '/', name: 'home', component: { template: '<div>home</div>' } },
    ],
  })
}

const sectionHostStub = {
  props: ['section'],
  emits: ['goto'],
  template: `
    <section data-testid="section-host" :data-section="section">
      <button type="button" data-testid="overview-waterfall" @click="$emit('goto', 'waterfall')">调度瀑布</button>
    </section>
  `,
}

const sessionTurnsStub = {
  emits: ['select-request', 'open-as-request'],
  template: `
    <section data-testid="session-turns-stub">
      <button type="button" data-testid="select-turn" @click="$emit('select-request', 'req-43')">选择轮次</button>
      <button type="button" data-testid="open-as-request" @click="$emit('open-as-request', 'req-43')">打开单请求</button>
    </section>
  `,
}

function mountView(initialPath: string) {
  const router = makeRouter()
  return router.push(initialPath).then(async () => {
    await router.isReady()
    const wrapper = mount(RequestDetailFullscreenView, {
      global: {
        plugins: [router, i18n],
        stubs: {
          SessionSummaryBar: true,
          SessionTurnsSyncPane: sessionTurnsStub,
          RequestDetailSectionHost: sectionHostStub,
        },
      },
    })
    await flushPromises()
    return { router, wrapper }
  })
}

describe('RequestDetailFullscreenView request-detail navigation', () => {
  beforeEach(() => {
    loader.metaLoading.value = false
    loader.metaError.value = ''
    loader.log.value = null
    loader.unified.value = null
    loader.sessionSnap.value = null
    loader.sessionId.value = null
    loader.requestBody.value = null
    loader.responseBody.value = null
    loader.outboundBody.value = null
    loader.waterfallLoading.value = false
    loader.waterfallError.value = ''
    loader.waterfall.value = null
    loader.waterfallSource.value = ''
    loader.attempts.value = []
    loader.loadMeta.mockClear()
    loader.onSectionNeed.mockClear()
    loader.dispose.mockClear()
  })

  it('deep-links directly to the inline waterfall tab instead of overview', async () => {
    const { router, wrapper } = await mountView(
      '/request-detail/req-42?mode=request&tab=waterfall',
    )

    expect(router.currentRoute.value.name).toBe('request-detail')
    expect(router.currentRoute.value.params.requestId).toBe('req-42')
    expect(router.currentRoute.value.query).toEqual({ mode: 'request', tab: 'waterfall' })
    expect(wrapper.get('[data-testid="section-host"]').attributes('data-section')).toBe('waterfall')
    expect(loader.onSectionNeed).toHaveBeenCalledWith('req-42', 'waterfall')
  })

  it('renders the real inline waterfall body for a waterfall deep link', async () => {
    loader.waterfall.value = {
      request_id: 'req-42',
      model: 'model-a',
      result: 'success',
      waiting_in_total_ms: 1,
      waiting_in_model_ms: 1,
      waiting_in_node_ms: 1,
      routing_ms: 1,
      acquire_ms: 1,
      upstream_latency_ms: 1,
      streaming_duration_ms: 1,
      queue_wait_ms: 1,
      total_ms: 8,
      attempts: [{ attempt_id: 'a-1', attempt_no: 1, credential_id: 5, outcome: 'success' }],
    }
    const router = makeRouter()
    await router.push('/request-detail/req-42?mode=request&tab=waterfall')
    await router.isReady()
    const wrapper = mount(RequestDetailFullscreenView, {
      global: {
        plugins: [router, i18n],
        stubs: {
          SessionSummaryBar: true,
          SessionTurnsSyncPane: sessionTurnsStub,
        },
      },
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="request-waterfall-panel"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="waterfall-request-detail"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="waterfall-stage-table"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('req-42')
    expect(wrapper.text()).toContain('T0–T9 阶段')
  })

  it('keeps the request-detail route and switches main content to waterfall after a child goto event', async () => {
    const { router, wrapper } = await mountView(
      '/request-detail/req-42?mode=session-turns&tab=overview&redirect=%2F&login=1&foo=stale',
    )

    await wrapper.get('[data-testid="overview-waterfall"]').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.name).toBe('request-detail')
    expect(router.currentRoute.value.path).toBe('/request-detail/req-42')
    expect(router.currentRoute.value.params.requestId).toBe('req-42')
    expect(router.currentRoute.value.query).toEqual({ mode: 'request', tab: 'waterfall' })
    expect(router.currentRoute.value.query).not.toHaveProperty('redirect')
    expect(router.currentRoute.value.query).not.toHaveProperty('login')
    expect(router.currentRoute.value.query).not.toHaveProperty('foo')
    expect(wrapper.get('[data-testid="section-host"]').attributes('data-section')).toBe('waterfall')
    expect(loader.onSectionNeed).toHaveBeenLastCalledWith('req-42', 'waterfall')
  })

  it('uses the same narrow route query contract when switching mode or selecting another turn', async () => {
    loader.sessionId.value = 'session-1'
    const { router, wrapper } = await mountView(
      '/request-detail/req-42?mode=request&tab=overview&redirect=%2F&foo=stale',
    )

    const sessionModeButton = wrapper.findAll('button').find(button => button.text().includes('会话轮次'))
    expect(sessionModeButton).toBeTruthy()
    await sessionModeButton!.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ mode: 'session-turns', tab: 'overview' })

    await wrapper.get('[data-testid="select-turn"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.params.requestId).toBe('req-43')
    expect(router.currentRoute.value.query).toEqual({ mode: 'session-turns', tab: 'overview' })

    await wrapper.get('[data-testid="open-as-request"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.params.requestId).toBe('req-43')
    expect(router.currentRoute.value.query).toEqual({ mode: 'request', tab: 'overview' })
  })

  it('keeps the session turns pane when a later turn is not found', async () => {
    loader.sessionId.value = 'session-1'
    loader.metaError.value = '请求详情未找到'
    const { router, wrapper } = await mountView(
      '/request-detail/missing-turn?mode=session-turns&tab=overview',
    )

    expect(router.currentRoute.value.name).toBe('request-detail')
    expect(wrapper.text()).toContain('请求详情未找到')
    expect(wrapper.find('[data-testid="session-turns-stub"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="section-host"]').exists()).toBe(false)
  })
})
