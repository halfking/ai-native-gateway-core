// LiveTopologyView.filters.test.ts — 新首页的过滤收敛判据（2026-10-16）
//
// ★ 真问题：**图、左栏、右栏必须收敛到同一个过滤子集**。
// 只要三者各自算各自的，用户就会看到「图里没有这条链路，右栏却有」这种
// 自相矛盾的画面 —— 态势页最容易犯、也最难自查的一类缺陷。
//
// 判据直接驱动被测组件（不是读源码）：mock 掉 SSE 单例，注入固定请求集，
// 断言三个区域互相吻合；同时钉住 URL ⇄ 过滤态往返。
//
// 注：vi.mock 工厂会被提升到文件顶部，所以 refs 必须在工厂内部创建
// （顶层 ref 在工厂执行时还没初始化 —— hoist 报错的经典形态）。

import { describe, expect, it, vi, beforeAll, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import LiveTopologyView from './LiveTopologyView.vue'
import zhCN from '../locales/zh-CN/liveTopology'

type Frame = Record<string, unknown>

vi.mock('../composables/liveStreamStore', async () => {
  const { computed, ref } = await import('vue')
  const requestsRef = ref<Frame[]>([])
  const nodesRef = ref<Frame[]>([])
  const queueRef = ref<unknown>(null)
  const actionsMap = new Map<string, Frame[]>()
  // 真实 store 里 actionsRef 是包住 Map 的 computedRef，这里保持同一形状
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
    // 测试侧注入口
    __test: {
      setRequests: (v: Frame[]) => (requestsRef.value = v),
      setNodes: (v: Frame[]) => (nodesRef.value = v),
      setActions: (k: string, v: Frame[]) => actionsMap.set(k, v),
      clearActions: () => actionsMap.clear(),
      setConnection: (v: string) => (connectionRef.value = v),
      setInitialData: (v: boolean) => (initialDataReceivedRef.value = v),
      setRedis: (healthy: boolean, err = '') => {
        redisHealthyRef.value = healthy
        redisErrorRef.value = err
      },
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
  getProvidersToday: vi.fn().mockResolvedValue({ day_utc: '2026-10-16', totals: {}, items: [] }),
}))

// 左栏在这个判据里只验「是否收到同一个过滤值」，统计口径由
// ProviderTodayRail.test.ts 单独钉，避免两头都浅。
vi.mock('../components/live-topology/ProviderTodayRail.vue', () => ({
  default: {
    name: 'ProviderTodayRailStub',
    props: ['activeProvider'],
    emits: ['select-provider'],
    template: '<div class="rail-stub" :data-provider="activeProvider"></div>',
  },
}))

import * as store from '../composables/liveStreamStore'

const testApi = (store as unknown as {
  __test: {
    setRequests: (v: Frame[]) => void
    setNodes: (v: Frame[]) => void
    setActions: (k: string, v: Frame[]) => void
    clearActions: () => void
    setConnection: (v: string) => void
    setInitialData: (v: boolean) => void
    setRedis: (healthy: boolean, err?: string) => void
  }
}).__test

// jsdom 没装 canvas 包，getContext 会抛 "Not implemented"。
// 组件本身已做 try/catch 降级（隐私模式浏览器同理），这里再兜一层，
// 免得 rAF 循环把测试输出刷成噪声。
beforeAll(() => {
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext']
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

const REQUESTS: Frame[] = [
  {
    ts: new Date().toISOString(), request_id: 'r-ds-1', status: 'success',
    provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
    agent_name: 'claude-code', total_tokens: 900, latency_ms: 1200,
  },
  {
    ts: new Date().toISOString(), request_id: 'r-ds-2', status: 'failure',
    provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
    agent_name: 'claude-code', total_tokens: 0, error_kind: 'upstream_timeout',
  },
  {
    ts: new Date().toISOString(), request_id: 'r-mm-1', status: 'success',
    provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi-k2',
    agent_name: 'codex', total_tokens: 4200, latency_ms: 800,
  },
]

async function mountView(initialPath = '/live') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/live', component: LiveTopologyView }],
  })
  await router.push(initialPath)
  await router.isReady()
  const wrapper = mount(LiveTopologyView, { global: { plugins: [i18n, router] } })
  await flushPromises()
  return { wrapper, router }
}

describe('LiveTopologyView filter convergence', () => {
  beforeEach(() => {
    testApi.setRequests(REQUESTS.map((r) => ({ ...r })))
    testApi.setNodes([
      { credential_id: 7, provider_code: 'deepseek', health_status: 'healthy', in_flight: 1 },
      { credential_id: 8, provider_code: 'moonshot', health_status: 'warning', in_flight: 0 },
    ])
    testApi.clearActions()
    testApi.setConnection('open')
    testApi.setInitialData(true)
    testApi.setRedis(true)
  })

  it('renders graph, rail and strip from one shared subset', async () => {
    const { wrapper } = await mountView()
    const labels = wrapper.findAll('.topo-node').map((n) => n.attributes('aria-label') ?? '')
    expect(labels.length).toBeGreaterThanOrEqual(6)
    expect(labels.join('|')).toContain('glm-4.5')
    expect(labels.join('|')).toContain('kimi-k2')
    expect(wrapper.findAll('.strip__item')).toHaveLength(3)
    expect(wrapper.find('.rail-stub').attributes('data-provider')).toBe('')
  })

  it('converges graph + strip + rail when a provider filter is applied', async () => {
    const { wrapper } = await mountView('/live?provider=deepseek')

    const items = wrapper.findAll('.strip__item')
    expect(items).toHaveLength(2)
    expect(items.every((i) => i.text().includes('deepseek'))).toBe(true)

    const labels = wrapper.findAll('.topo-node').map((n) => n.attributes('aria-label') ?? '').join('|')
    expect(labels).not.toContain('kimi-k2')
    expect(labels).not.toContain('moonshot')

    expect(wrapper.find('.rail-stub').attributes('data-provider')).toBe('deepseek')
  })

  it('distinguishes "filtered to nothing" from "no traffic at all"', async () => {
    const { wrapper } = await mountView('/live?provider=does-not-exist')
    expect(wrapper.findAll('.strip__item')).toHaveLength(0)
    expect(wrapper.text()).toContain('当前过滤条件下窗口内没有请求')

    testApi.setRequests([])
    const { wrapper: empty } = await mountView('/live')
    expect(empty.text()).not.toContain('当前过滤条件下窗口内没有请求')
  })

  it('shows the connection banner instead of pretending to be live', async () => {
    testApi.setConnection('reconnecting')
    const { wrapper } = await mountView()
    expect(wrapper.text()).toContain('实时流未连接')
  })

  it('waits for the first authoritative frame when connected but not yet delivered', async () => {
    testApi.setInitialData(false)
    const { wrapper } = await mountView()
    expect(wrapper.text()).toContain('等待本代际首帧数据')
  })

  it('surfaces a Redis failure as degraded health, not as a fake outage', async () => {
    testApi.setRedis(false, 'redis: connection refused')
    const { wrapper } = await mountView()
    expect(wrapper.text()).toContain('connection refused')
  })

  it('writes filters back into the URL so the view is shareable', async () => {
    const { router } = await mountView()
    await router.replace({ path: '/live', query: { provider: 'moonshot', model: 'kimi-k2' } })
    await flushPromises()
    await flushPromises()
    expect(router.currentRoute.value.query.provider).toBe('moonshot')
    expect(router.currentRoute.value.query.model).toBe('kimi-k2')
  })

  it('renders the routing decision timeline for the selected request', async () => {
    testApi.setActions('r-ds-2', [
      { seq: 1, action: 'resolve', stage_category: 'routing', auto_decision: 'weight' },
      { seq: 2, action: 'upstream', stage_category: 'llm', error_kind: 'upstream_timeout' },
      { seq: 3, action: 'terminal', stage_category: 'terminal', status: 'failure' },
    ])
    const { wrapper } = await mountView()
    const items = wrapper.findAll('.strip__item')
    // 窄条最新在上：r-mm-1 → r-ds-2 → r-ds-1
    await items[0].trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('该请求暂无判定事件')

    await wrapper.findAll('.strip__item')[1].trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.decisions__row')).toHaveLength(3)
    expect(wrapper.find('.decisions__stage.is-terminal').exists()).toBe(true)
  })
})
