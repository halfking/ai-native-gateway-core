// LiveStageTreeView.honesty.test.ts — live2 请求阶段树的展示诚实性判据（2026-11-16）
//
// ★ owner 2026-11-16 的三条硬要求，全部钉成行为事实：
//   ① 「过滤条件还是还原到上一步，放在顶层」—— 过滤栏在顶栏**常驻**，
//      不是浮窗、不是折叠抽屉；
//   ② 「统计点开可以展开全页形式，盖在动态显示的上层」—— 浮层是
//      position:fixed + inset:0 的**全页**覆盖，且有 Esc 出口；
//   ③ 「结合真实数据」—— 阶段状态只能来自真实 SSE，缺信号就是「未测量」。
//
// 判据纪律：读样式契约用**源码文本**（vitest 默认 css:false ⇒ computed style
// 恒为空串 ⇒ 那是恒假绿）；交互判据驱动真实组件 + mock 掉的 SSE 单例。

import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import LiveStageTreeView from './LiveStageTreeView.vue'
import LiveTopologyFilterBar from '../components/live-topology/LiveTopologyFilterBar.vue'
import zhCN from '../locales/zh-CN/liveStageTree'
import zhTopology from '../locales/zh-CN/liveTopology'

const readSrc = (rel: string) => readFileSync(resolve(process.cwd(), rel), 'utf8')

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

// 可信度清单：默认空（关联不上 ⇒ 「未测量」）。
vi.mock('../api/model-credibility', () => ({
  getModelCredibilityList: vi.fn().mockResolvedValue([]),
}))

// 统计面板自身有独立的 API 与口径（StatsPanel.test.ts 钉），这里只验浮层行为。
vi.mock('../components/live-topology/StatsPanel.vue', () => ({
  default: {
    name: 'StatsPanelStub',
    props: ['activeClient', 'activeProvider', 'activeModel', 'inFlight', 'queueDepth'],
    template: '<div class="stats-stub"></div>',
  },
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

beforeEach(() => {
  testApi.setNodes([
    { credential_id: 7, provider_code: 'deepseek', health_status: 'healthy' },
  ])
  testApi.clearActions()
  testApi.setRequests([])
})

describe('live2 过滤条件常驻顶层', () => {
  it('★ 过滤栏在顶栏里常驻（不是浮窗、不是抽屉）', async () => {
    const { wrapper } = await mountView()
    // 过滤控件在顶栏内 —— 顶栏是常驻层，不是 overlay。
    expect(wrapper.find('.stage-tree__bar [data-testid="filter-clients"]').exists()).toBe(true)
    expect(wrapper.find('.stage-tree__bar [data-testid="filter-providers"]').exists()).toBe(true)
    expect(wrapper.find('.stage-tree__bar [data-testid="filter-models"]').exists()).toBe(true)
    expect(wrapper.find('.stage-tree__bar [data-testid="filter-nodes"]').exists()).toBe(true)
    // 统计浮层此刻不存在（还没点开）。
    expect(wrapper.find('[data-testid="stage-stats-overlay"]').exists()).toBe(false)
  })

  it('★ 过滤栏与 /live 共用同一份实现（data-testid 同源，不会漂移出两套口径）', () => {
    // 同源纪律：两页各写一份 markup ⇒ 以后改一个忘另一个 ⇒
    // 「图里没有、右栏却有」的矛盾画面。两页都必须调同一个组件。
    const bar = readSrc('src/components/live-topology/LiveTopologyFilterBar.vue')
    expect(bar).toContain('data-testid="filter-clients"')
    expect(readSrc('src/views/LiveStageTreeView.vue')).toContain('LiveTopologyFilterBar')
    expect(readSrc('src/views/LiveTopologyView.vue')).toContain('LiveTopologyFilterBar')
  })
})

describe('live2 统计全页浮层', () => {
  it('★ 点开统计 ⇒ 全页浮层盖住动态显示（position:fixed + inset:0）', async () => {
    const { wrapper } = await mountView()
    await wrapper.get('[data-testid="stage-stats-open"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-stats-overlay"]').exists()).toBe(true)

    // 样式契约读源码（vitest css:false ⇒ computed style 恒为空串 ⇒ 恒假绿）。
    const src = readSrc('src/components/live-stage-tree/StageTreeStatsOverlay.vue')
    const rule = src.match(/\.stage-stats-overlay\s*\{[^}]*\}/)
    expect(rule, '浮层样式规则未找到').toBeTruthy()
    expect(rule![0]).toMatch(/position:\s*fixed/)
    expect(rule![0]).toMatch(/inset:\s*0/)
    // 盖住整个视口 ⇒ 高度/宽度都撑满。
    expect(rule![0]).toMatch(/height:\s*100%/)
  })

  it('★ 浮层有 Esc 出口（不是只能点关闭按钮）', async () => {
    const { wrapper } = await mountView()
    await wrapper.get('[data-testid="stage-stats-open"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-stats-overlay"]').exists()).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-stats-overlay"]').exists()).toBe(false)
  })

  it('★ 关掉浮层不丢过滤态', async () => {
    const { wrapper } = await mountView()
    await wrapper.get('[data-testid="stage-stats-open"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="stage-stats-close"]').trigger('click')
    await flushPromises()
    // 过滤栏仍在（过滤态由父组件持有，浮层只读统计）。
    expect(wrapper.find('.stage-tree__bar [data-testid="filter-clients"]').exists()).toBe(true)
  })
})

describe('live2 阶段树渲染', () => {
  it('★ 发送轨 8 段 + 接收轨 3 段都渲染出来', async () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-1',
        status: 'success',
        provider_code: 'deepseek',
        credential_id: 7,
        agent_name: 'claude-code',
        gw_session_id: 'sess-1',
        total_tokens: 1000,
      },
    ])
    testApi.setActions('r-1', [
      { request_id: 'r-1', seq: 1, action: 'arrive', detail: { client_protocol: 'openai' } },
      { request_id: 'r-1', seq: 2, action: 'route_resolved', detail: { candidates: '2' }, model: 'gpt-4o' },
      { request_id: 'r-1', seq: 3, action: 'node_selected', detail: { attempt: '1' }, model: 'gpt-4o' },
      { request_id: 'r-1', seq: 4, action: 'reply', detail: { status: 'success' } },
    ])
    const { wrapper } = await mountView()

    for (const key of ['auth', 'sanitize', 'approval', 'injection', 'route', 'compress', 'provider']) {
      expect(wrapper.find(`[data-testid="stage-send-${key}"]`).exists(), `发送轨缺 ${key}`).toBe(true)
    }
    for (const key of ['first_byte', 'restore', 'meter', 'reply']) {
      expect(wrapper.find(`[data-testid="stage-recv-${key}"]`).exists(), `接收轨缺 ${key}`).toBe(true)
    }
  })

  it('★ auto 请求：显示 AUTO 徽标 + 最终匹配到的模型（不是「请求 auto」）', async () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-auto',
        status: 'success',
        provider_code: 'deepseek',
        credential_id: 7,
        agent_name: 'claude-code',
        is_auto_request: true,
        client_model: 'claude-sonnet-4-5',
        total_tokens: 10,
      },
    ])
    testApi.setActions('r-auto', [
      { request_id: 'r-auto', seq: 1, action: 'arrive', detail: { client_protocol: 'openai' } },
      {
        request_id: 'r-auto',
        seq: 2,
        action: 'route_resolved',
        detail: { candidates: '3', auto: 'true', auto_task_type: 'code' },
        model: 'auto',
      },
      { request_id: 'r-auto', seq: 3, action: 'node_selected', detail: {}, model: 'claude-sonnet-4-5' },
    ])
    const { wrapper } = await mountView()

    expect(wrapper.find('[data-testid="stage-band-auto"]').exists()).toBe(true)
    const finalModel = wrapper.get('[data-testid="stage-band-final-model"]').text()
    expect(finalModel).toContain('claude-sonnet-4-5')
    // ★ 界面不出现「请求 auto」字样。
    expect(wrapper.text()).not.toContain('请求 auto')
  })

  it('★ 缓存命中率：口径不可判定显示「—」，不冒充 0%', async () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-cache',
        status: 'success',
        provider_code: 'deepseek',
        credential_id: 7,
        cache_read_tokens: 100,
        prompt_tokens: 400,
        upstream_protocol: '',
        total_tokens: 500,
      },
    ])
    const { wrapper } = await mountView()
    const cache = wrapper.get('.band__cache').text()
    expect(cache).toContain('—')
    expect(cache).not.toContain('0%')
  })

  it('★ 可信度关联不上 ⇒ 显示「未测量」，不显示 0 分', async () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-cred',
        status: 'success',
        provider_code: 'deepseek',
        credential_id: 7,
        total_tokens: 10,
      },
    ])
    const { wrapper } = await mountView()
    const cred = wrapper.get('[data-testid="stage-band-credibility"]').text()
    expect(cred).toContain('未测量')
    expect(cred).not.toMatch(/\b0\b/)
  })
})

describe('live2 过滤选项口径（数据来源 + 与图默认可见口径一致）', () => {
  it('★ 选项来自时间窗内的真实请求统计，无效节点的流量不进候选（不会出现「选中后图里空无一物」的死选项）', async () => {
    const { wrapper } = await mountView()
    testApi.setNodes([
      { credential_id: 7, provider_code: 'deepseek', health_status: 'healthy' },
      { credential_id: 8, provider_code: 'moonshot', circuit_state: 'open' },
    ])
    const ts = Date.now()
    testApi.setRequests([
      {
        request_id: 'opt-1', ts, status: 'success',
        credential_id: 7, provider_code: 'deepseek',
        model: 'deepseek-chat', canonical_name: 'deepseek-chat', agent_name: 'zcode',
      },
      {
        request_id: 'opt-2', ts, status: 'success',
        credential_id: 8, provider_code: 'moonshot',
        model: 'kimi-k3', canonical_name: 'kimi-k3', agent_name: 'zcode',
      },
    ])
    await flushPromises()
    const bar = wrapper.findComponent(LiveTopologyFilterBar)
    const opts = bar.props('options') as Record<string, Array<{ value: string }>>
    const nodeValues = (opts.nodes ?? []).map(o => o.value)
    expect(nodeValues).toContain('cred:7')
    expect(nodeValues).not.toContain('cred:8')
    // 无效节点上的整条请求被剔除：moonshot 的唯一流量来自无效节点，
    // 它在供应商候选里也不出现（图上默认同样看不到它的带）。
    const providerValues = (opts.providers ?? []).map(o => o.value)
    expect(providerValues).toContain('deepseek')
    expect(providerValues).not.toContain('moonshot')
    const modelValues = (opts.models ?? []).map(o => o.value)
    expect(modelValues).toContain('deepseek-chat')
    expect(modelValues).not.toContain('kimi-k3')
  })

  it('★ 源码结构：options 的数据池是 optionRequests（过 hiddenNodes），不得回退成 realRequests 直连', () => {
    const src = readSrc('src/views/LiveStageTreeView.vue')
    expect(src).toContain('const optionRequests = computed')
    expect(src).toContain('providerNodeKeyOf')
    expect(src).toMatch(/filterOptions\(optionRequests\.value/)
    // 防回退：options computed 直接消费 realRequests = 口径又漂回「死选项」。
    expect(src).not.toMatch(/filterOptions\(realRequests\.value/)
  })
})
