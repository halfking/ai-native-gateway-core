// LiveStageTreeView.detail.test.ts —— 「点击请求行 ⇒ 展开请求详情」判据（2026-11-18）
//
// ★ 背景：设计原型（docs/audit/2026-11-16-live阶段树原型*.html 的 `toggleBandDetail`）
//   里**点击请求行会展开「请求详情」**，而线上实现缺了这一段 ⇒ 点行毫无反应。
//   本文件钉的是「这个交互真的存在、真的按设计工作」：
//   ① 点一行 ⇒ 详情面板出现，且五个分区都在；
//   ② 手风琴：点第二条 ⇒ 第一条自动收起（原型语义：同时只展开一条）；
//   ③ 再点同一条 ⇒ 收起（可逆）；
//   ④ 诚实性：没上报的值显示「—」，**不拿 0 冒充**（未测量 ≠ 0）；
//   ⑤ 紧凑模式下详情里**不出现 ✓**（设计规则 15：紧凑模式全页 ✓ 计数 = 0）；
//   ⑥ 键盘可达（Enter / Space 同样能展开）。

import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import LiveStageTreeView from './LiveStageTreeView.vue'
import zhCN from '../locales/zh-CN/liveStageTree'
import zhTopology from '../locales/zh-CN/liveTopology'

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
  return {
    requestsRef,
    nodesRef,
    queueRef,
    actionsRef,
    connectionRef,
    initialDataReceivedRef,
    getRequestActions: (id: string) => actionsMap.get(id) ?? [],
    __test: {
      setRequests: (v: Frame[]) => (requestsRef.value = v),
      setNodes: (v: Frame[]) => (nodesRef.value = v),
      setActions: (id: string, v: Frame[]) => actionsMap.set(id, v),
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

vi.mock('../api/model-credibility', () => ({
  getModelCredibilityList: vi.fn().mockResolvedValue([]),
}))

import * as store from '../composables/liveStreamStore'

const testApi = (store as unknown as {
  __test: {
    setRequests: (v: Frame[]) => void
    setNodes: (v: Frame[]) => void
    setActions: (id: string, v: Frame[]) => void
    clearActions: () => void
  }
}).__test

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      liveStageTree: zhCN as never,
      liveTopology: zhTopology as never,
    },
  },
})

const nowIso = () => new Date().toISOString()

const mounted: Array<{ unmount: () => void }> = []

afterEach(() => {
  while (mounted.length) {
    const w = mounted.pop()
    try {
      w?.unmount()
    } catch {
      /* 清理是卫生问题，不是判据问题 */
    }
  }
})

async function mountView() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/live2', component: LiveStageTreeView }],
  })
  await router.push('/live2')
  await router.isReady()
  const wrapper = mount(LiveStageTreeView, { global: { plugins: [i18n, router] } })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, router }
}

/** 一条**信息齐备**的请求帧（用于断言详情里的真实值）。 */
function richFrame(id: string): Frame {
  return {
    ts: nowIso(),
    request_id: id,
    status: 'success',
    provider_code: 'deepseek',
    credential_id: 7,
    client_name: 'cli',
    client_type: 'cli',
    client_protocol: 'openai',
    model: 'glm-4.5-flash',
    total_tokens: 120,
    prompt_tokens: 100,
    cost_usd: 0.001234,
    latency_ms: 1500,
  }
}

beforeEach(() => {
  testApi.setNodes([{ credential_id: 7, provider_code: 'deepseek', health_status: 'healthy' }])
  testApi.clearActions()
  testApi.setRequests([])
})

describe('live2 请求详情（点击请求行展开）', () => {
  it('★ 默认态：详情面板**不存在**（点行之前不渲染任何详情）', async () => {
    testApi.setRequests([richFrame('r-a')])
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="stage-detail-title"]').exists()).toBe(false)
  })

  it('★ 点一行 ⇒ 详情面板出现，五个分区都在', async () => {
    testApi.setRequests([richFrame('r-a')])
    const { wrapper } = await mountView()
    await wrapper.find('[data-testid="stage-band-r-a"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(true)
    for (const sec of ['resolve', 'reply', 'identity', 'stages', 'session']) {
      expect(wrapper.find(`[data-testid="stage-detail-section-${sec}"]`).exists()).toBe(true)
    }
    // 真实值上屏（不是空面板）
    const text = wrapper.find('[data-testid="stage-detail-r-a"]').text()
    expect(text).toContain('glm-4.5-flash')
    expect(text).toContain('100')
    expect(text).toContain('1500')
  })

  it('★ 手风琴：点第二条 ⇒ 第一条自动收起（同时只展开一条）', async () => {
    testApi.setRequests([richFrame('r-a'), richFrame('r-b')])
    const { wrapper } = await mountView()
    await wrapper.find('[data-testid="stage-band-r-a"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(true)

    await wrapper.find('[data-testid="stage-band-r-b"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="stage-detail-r-b"]').exists()).toBe(true)
  })

  it('★ 再点同一条 ⇒ 收起（可逆）', async () => {
    testApi.setRequests([richFrame('r-a')])
    const { wrapper } = await mountView()
    await wrapper.find('[data-testid="stage-band-r-a"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(true)
    await wrapper.find('[data-testid="stage-band-r-a"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(false)
  })

  it('★ 诚实性：没上报的值显示「—」，**不拿 0 冒充**', async () => {
    // 只有 request_id / status ⇒ 耗时、token、成本、缓存、帧数全部未上报
    testApi.setRequests([
      { ts: nowIso(), request_id: 'r-bare', status: 'success', provider_code: 'deepseek' },
    ])
    const { wrapper } = await mountView()
    await wrapper.find('[data-testid="stage-band-r-bare"]').trigger('click')
    await flushPromises()

    const text = wrapper.find('[data-testid="stage-detail-r-bare"]').text()
    expect(text).toContain('—')
    // 「0 ms」「0 tok」「$0.000000」都不许出现（那是拿 0 冒充未测量）
    expect(text).not.toContain('0 ms')
    expect(text).not.toContain('0 tok')
    expect(text).not.toContain('$0.000000')
    expect(text).not.toContain('0%')
  })

  it('★ 紧凑模式：详情里**不出现 ✓**（设计规则 15：紧凑模式全页 ✓ 计数 = 0）', async () => {
    testApi.setRequests([richFrame('r-a')])
    const { wrapper } = await mountView()
    await wrapper.find('[data-testid="stage-mode-compact"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="stage-band-r-a"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(true)
    const page = document.body.textContent ?? ''
    expect((page.match(/✓/g) ?? []).length).toBe(0)
  })

  it('★ 键盘可达：Enter / Space 也能展开（无障碍不是可选项）', async () => {
    testApi.setRequests([richFrame('r-a')])
    const { wrapper } = await mountView()
    const band = wrapper.find('[data-testid="stage-band-r-a"]')
    expect(band.attributes('tabindex')).toBe('0')
    expect(band.attributes('role')).toBe('button')

    await band.trigger('keydown.enter')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(true)

    await wrapper.find('[data-testid="stage-band-r-a"]').trigger('keydown.space')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-detail-r-a"]').exists()).toBe(false)
  })
})
