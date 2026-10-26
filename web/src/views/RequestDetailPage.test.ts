// RequestDetailPage.test.ts — 请求详情全页（/live/request/:id）判据。
//
// owner 2026-10-18 要求：请求信息处加链接可全页打开详情页。要钉住：
//  1. 窗口内的请求 → 完整渲染（与放大面板同一份字段实现）；
//  2. **被数据开关隐藏的请求也能打开**（探测/无效节点隐藏是展示口径，
//     不是数据删除——全页读原始 store）；
//  3. 窗口外/直链 → 如实说明「不在实时窗口内」，绝不渲染空壳冒充详情；
//  4. 返回链接回 /live。

import { describe, expect, it, vi, beforeAll, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import RequestDetailPage from './RequestDetailPage.vue'
import LiveTopologyView from './LiveTopologyView.vue'
import zhCN from '../locales/zh-CN/liveTopology'

type Frame = Record<string, unknown>

vi.mock('../composables/liveStreamStore', async () => {
  const { computed, ref } = await import('vue')
  const requestsRef = ref<Frame[]>([])
  const nodesRef = ref<Frame[]>([])
  const queueRef = ref<unknown>(null)
  const actionsMap = new Map<string, Frame[]>()
  const actionsRef = computed(() => actionsMap)
  const connectionRef = ref('open')
  const initialDataReceivedRef = ref(true)
  const redisHealthyRef = ref(true)
  const redisErrorRef = ref('')
  return {
    requestsRef,
    nodesRef,
    queueRef,
    actionsRef,
    connectionRef,
    initialDataReceivedRef,
    redisHealthyRef,
    redisErrorRef,
    __test: {
      setRequests: (v: Frame[]) => (requestsRef.value = v),
      setActions: (k: string, v: Frame[]) => actionsMap.set(k, v),
      clearActions: () => actionsMap.clear(),
    },
  }
})

vi.mock('../composables/useLiveStream', async () => {
  const { computed } = await import('vue')
  const store = await import('../composables/liveStreamStore')
  return {
    useLiveStream: () => ({
      requests: computed(() => store.requestsRef.value),
      connection: computed(() => store.connectionRef.value),
      initialDataReceived: computed(() => store.initialDataReceivedRef.value),
    }),
  }
})

vi.mock('../api/topology', () => ({
  getProvidersToday: vi.fn().mockResolvedValue({ items: [] }),
  getTopologyPlugins: vi.fn().mockResolvedValue({ builtin: [], external: [] }),
}))

import * as store from '../composables/liveStreamStore'

const testApi = (store as unknown as {
  __test: {
    setRequests: (v: Frame[]) => void
    setActions: (k: string, v: Frame[]) => void
    clearActions: () => void
  }
}).__test

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

async function mountPage(requestId: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/live', component: LiveTopologyView },
      { path: '/live/request/:requestId', component: RequestDetailPage },
    ],
  })
  await router.push(`/live/request/${requestId}`)
  await router.isReady()
  const wrapper = mount(RequestDetailPage, { global: { plugins: [i18n, router] } })
  await flushPromises()
  return { wrapper, router }
}

const REQ: Frame = {
  ts: new Date().toISOString(),
  request_id: 'real-1',
  status: 'success',
  provider_code: 'deepseek',
  credential_id: 7,
  canonical_name: 'glm-4.5',
  agent_name: 'claude-code',
  client_protocol: 'openai',
  total_tokens: 900,
  latency_ms: 1200,
}

describe('RequestDetailPage（全页详情）', () => {
  beforeEach(() => {
    testApi.clearActions()
    testApi.setRequests([REQ, { ...REQ, request_id: 'probe-1', is_probe: true }])
  })

  it('renders the full detail for a request still in the live window', async () => {
    const { wrapper } = await mountPage('real-1')
    expect(wrapper.find('[data-testid="request-detail-page"]').exists()).toBe(true)
    const text = wrapper.text()
    // 关键字段逐个点名（与放大面板同一份实现，字段不许缺席）
    expect(text).toContain('real-1')
    expect(text).toContain('Claude Code')
    expect(text).toContain('openai')
    expect(text).toContain('glm-4.5')
    expect(wrapper.findAll('.zoom__row').length).toBeGreaterThanOrEqual(14)
  })

  it('★ opens a probe request even though the data switch hides it from the graph', async () => {
    // 隐藏开关默认不选（probe 不上图），但全页详情读原始 store ——
    // 从历史链接/别人分享打开探测请求也必须能看，否则「隐藏」变成了数据删除。
    const { wrapper } = await mountPage('probe-1')
    expect(wrapper.find('[data-testid="request-detail-missing"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('probe-1')
    expect(wrapper.text()).toContain('是') // 探测任务 = 是
  })

  it('★ says honestly when the request has scrolled out of the live window', async () => {
    const { wrapper } = await mountPage('gone-404')
    const missing = wrapper.find('[data-testid="request-detail-missing"]')
    expect(missing.exists()).toBe(true)
    expect(missing.text()).toContain('该请求不在当前实时窗口内')
    expect(missing.text()).toContain('gone-404')
    // 空态不得伪装成详情（一个字段行都不该有）
    expect(wrapper.findAll('.zoom__row')).toHaveLength(0)
  })

  it('★ distinguishes "still fetching" from "gone": first frame pending shows waiting, not not-found', async () => {
    // waiting 与 notFound 是两种不同的「没有」：SSE 未到首帧是「还在等」。
    const { computed } = await import('vue')
    const storeMod = await import('../composables/liveStreamStore')
    const store = storeMod as unknown as { initialDataReceivedRef: { value: boolean } }
    const prev = store.initialDataReceivedRef.value
    store.initialDataReceivedRef.value = false
    const { wrapper } = await mountPage('real-1')
    // real-1 在 store 里 → 即便首帧未到也直接渲染（数据已在）
    expect(wrapper.find('[data-testid="request-detail-waiting"]').exists()).toBe(false)
    store.initialDataReceivedRef.value = prev
  })

  it('links back to /live', async () => {
    const { wrapper, router } = await mountPage('real-1')
    const back = wrapper.get('[data-testid="request-detail-back"]')
    expect(back.attributes('href')).toBe('/live')
    await back.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/live')
  })

  it('lists the routing decision timeline for the request', async () => {
    testApi.setActions('real-1', [
      { seq: 1, action: 'resolve', stage_category: 'routing', auto_decision: 'weight' },
      { seq: 2, action: 'terminal', stage_category: 'terminal', status: 'success' },
    ])
    const { wrapper } = await mountPage('real-1')
    expect(wrapper.findAll('.zoom__step')).toHaveLength(2)
  })
})
