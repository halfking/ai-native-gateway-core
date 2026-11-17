// LiveStageTreeView.designParity.test.ts —— live2 页的**设计对齐**判据（2026-11-17）
//
// ★ 本文件钉的是「线上看到的界面 == 设计方案里的界面」这件事：
//   ① **后端心跳不渲染**（idle_marker 不是请求）；
//   ② **阶段节点有值**（线上 SSE 的摊平形状也能点亮取值）；
//   ③ **没有裸 i18n 键上屏**（first_byte → firstByte 的键名错位）；
//   ④ **版面件齐**：时间列 / 图例 / 列头 / 完整-紧凑双模式 / 紧凑模式的最右错误列；
//   ⑤ **胶囊读数带窗口与请求数**（设计第 3 轮的口径）。

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
  testApi.setNodes([{ credential_id: 7, provider_code: 'deepseek', health_status: 'healthy' }])
  testApi.clearActions()
  testApi.setRequests([])
})

describe('live2 设计对齐：内容', () => {
  it('★ 后端 idle_marker 心跳**不渲染成请求带**', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'idle-global-model-x', type: 'idle_marker', status: 'idle', error_kind: 'no_traffic_5min' },
      { ts: nowIso(), request_id: 'r-real', status: 'success', provider_code: 'deepseek', credential_id: 7, total_tokens: 10 },
    ])
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-testid="stage-band-idle-global-model-x"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="stage-band-r-real"]').exists()).toBe(true)
    // 界面上也不该出现心跳的错误码
    expect(wrapper.text()).not.toContain('no_traffic_5min')
  })

  it('★ 线上 SSE 的摊平动作形状 ⇒ 阶段节点有值（不是只有名字）', async () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-flat',
        status: 'success',
        provider_code: 'deepseek',
        credential_id: 7,
        total_tokens: 100,
      },
    ])
    testApi.setActions('r-flat', [
      { request_id: 'r-flat', seq: 1, action: 'arrive', client_protocol: 'openai' },
      { request_id: 'r-flat', seq: 2, action: 'sanitize_applied', mutations: '3' },
      { request_id: 'r-flat', seq: 3, action: 'route_resolved', candidates: '5', auto_task_type: 'code' },
      { request_id: 'r-flat', seq: 4, action: 'node_selected', model: 'gpt-4o' },
      { request_id: 'r-flat', seq: 5, action: 'reply', status: 'success', latency_ms: '1200' },
    ])
    const { wrapper } = await mountView()
    // 脱敏命中 3 处
    expect(wrapper.get('[data-testid="stage-send-sanitize"]').text()).toContain('3')
    // ★ 路由格＝最终匹配模型（设计口径）；候选数走摘要行的独立 chip
    expect(wrapper.get('[data-testid="stage-send-route"]').text()).toContain('gpt-4o')
    expect(wrapper.get('[data-testid="stage-band-candidates"]').text()).toContain('5')
    // 回复成功
    expect(wrapper.get('[data-testid="stage-recv-reply"]').text()).toContain('success')
  })

  it('★ 界面上不出现裸 i18n 键（first_byte 的键名错位）', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'r-key', status: 'success', provider_code: 'deepseek', credential_id: 7, total_tokens: 10 },
    ])
    testApi.setActions('r-key', [
      { request_id: 'r-key', seq: 1, action: 'arrive', client_protocol: 'openai' },
      { request_id: 'r-key', seq: 2, action: 'first_byte', ttfb_ms: '820' },
    ])
    const { wrapper } = await mountView()
    const text = wrapper.text()
    expect(text).not.toContain('liveStageTree.stage')
    expect(text).not.toContain('liveStageTree.')
    // 阶段名要真的显示出来
    expect(text).toContain('首字节')
  })
})

describe('live2 设计对齐：版面', () => {
  it('★ 版面件齐：时间列 / 图例 / 列头 / 双模式切换', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'r-layout', status: 'success', provider_code: 'deepseek', credential_id: 7, total_tokens: 10 },
    ])
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-testid="stage-tree-legend"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="stage-tree-header"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="stage-mode-full"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="stage-mode-compact"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="stage-band-time-r-layout"]').exists()).toBe(true)
    // 列头里的阶段名（表头与带共用列宽）
    const head = wrapper.get('[data-testid="stage-tree-header"]').text()
    for (const name of ['鉴权/限流', '脱敏', '审批', '注入检测', '路由', '压缩', '供应商/令牌池']) {
      expect(head, `列头缺 ${name}`).toContain(name)
    }
  })

  it('★ 紧凑模式：单行瓦片 + 最右固定错误列（owner：最右边显示错误源头及类型）', async () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-fail',
        status: 'failure',
        failure_stage: 'upstream',
        error_kind: 'upstream_timeout',
        provider_code: 'deepseek',
        credential_id: 7,
      },
    ])
    const { wrapper } = await mountView()
    await wrapper.get('[data-testid="stage-mode-compact"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-tile-auth"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="stage-tile-first_byte"]').exists()).toBe(true)
    const errCol = wrapper.get('[data-testid="stage-band-errorcol"]').text()
    expect(errCol).toContain('供应商')
    expect(errCol).toContain('upstream_timeout')
  })

  it('★ 摘要行：状态 / 会话轮次 / 最终模型 / 可信度 都在', async () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-sum',
        status: 'success',
        provider_code: 'deepseek',
        credential_label: 'kiro',
        credential_id: 7,
        agent_name: 'claude-code',
        agent_type: 'coding_agent',
        gw_session_id: 'sess-x',
        client_protocol: 'openai',
        total_tokens: 100,
        prompt_tokens: 60,
        completion_tokens: 40,
      },
    ])
    testApi.setActions('r-sum', [
      { request_id: 'r-sum', seq: 1, action: 'arrive', client_protocol: 'openai' },
      { request_id: 'r-sum', seq: 2, action: 'node_selected', model: 'gpt-4o' },
    ])
    const { wrapper } = await mountView()
    const summary = wrapper.get('.band__summary').text()
    expect(summary).toContain('成功')
    expect(summary).toContain('首轮')
    expect(summary).toContain('gpt-4o')
    // 客户端节点：类型徽标 + 名称 + 模型入口（设计第 5 轮的三行）
    const client = wrapper.get('.band__client').text()
    expect(client).toContain('编码助手')
    expect(client).toContain('claude-code')
    expect(client).toContain('openai')
  })

  it('★ 统计胶囊读数带窗口与请求数（设计第 3 轮口径）', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'r-cap', status: 'success', provider_code: 'deepseek', credential_id: 7, total_tokens: 10 },
    ])
    const { wrapper } = await mountView()
    const capsule = wrapper.get('[data-testid="stage-stats-open"]').text()
    expect(capsule).toContain('近 5 分钟')
    expect(capsule).toContain('1 请求')
    expect(capsule).toContain('成功率')
    expect(capsule).toContain('在途 0')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 紧凑模式的**设计契约**（设计原型第 6/7/8/9 轮）
//
// ★ 为什么单独成块：紧凑模式在原型里是**另一套版面**（单行流水 + 右侧行内块 +
//   最右固定错误列），不是完整模式的缩小版。两者混用会互相打架 ——
//   实测过一版：发送轨节点块没按模式隐藏 ⇒ 与瓦片**抢同一批网格列** ⇒
//   瓦片被挤到第二行、节点文字被压成「✓ 鉴 o…」。
// ─────────────────────────────────────────────────────────────────────────────
describe('live2 设计对齐：紧凑模式版面契约', () => {
  const failing = () => {
    testApi.setRequests([
      {
        ts: nowIso(),
        request_id: 'r-cmp',
        status: 'failure',
        failure_stage: 'upstream',
        error_kind: 'upstream_timeout',
        provider_code: 'deepseek',
        credential_label: 'kiro',
        credential_id: 7,
      },
    ])
    testApi.setActions('r-cmp', [
      { request_id: 'r-cmp', seq: 1, action: 'arrive', client_protocol: 'openai' },
      { request_id: 'r-cmp', seq: 2, action: 'node_selected', model: 'gpt-4o' },
      { request_id: 'r-cmp', seq: 3, action: 'upstream_request', model: 'gpt-4o' },
    ])
  }

  it('★ 紧凑模式**不显示阶段列头**（原型：mode-compact 下 .stagehead 隐藏）', async () => {
    failing()
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-testid="stage-tree-header"]').exists()).toBe(true)
    await wrapper.get('[data-testid="stage-mode-compact"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-tree-header"]').exists()).toBe(false)
  })

  it('★ 紧凑模式**不再渲染发送/接收轨节点块**（否则与瓦片抢列、瓦片被挤到第二行）', async () => {
    failing()
    const { wrapper } = await mountView()
    await wrapper.get('[data-testid="stage-mode-compact"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="stage-send-auth"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="stage-recv-reply"]').exists()).toBe(false)
    // 11 段瓦片在位
    for (const key of ['auth', 'sanitize', 'approval', 'injection', 'route', 'compress', 'provider', 'first_byte', 'restore', 'meter', 'reply']) {
      expect(wrapper.find(`[data-testid="stage-tile-${key}"]`).exists(), `缺瓦片 ${key}`).toBe(true)
    }
  })

  it('★ 紧凑模式**完成态不画 ✓**（owner 第 8 轮：整个紧凑模式不要勾勾）', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'r-ok', status: 'success', provider_code: 'deepseek', credential_id: 7, total_tokens: 10 },
    ])
    testApi.setActions('r-ok', [
      { request_id: 'r-ok', seq: 1, action: 'arrive', client_protocol: 'openai' },
      { request_id: 'r-ok', seq: 2, action: 'node_selected', model: 'gpt-4o' },
    ])
    const { wrapper } = await mountView()
    // 完整模式**保留 ✓**（那里行高充裕，✓ 是有用的冗余通道）
    expect(wrapper.get('[data-testid="stage-send-auth"]').text()).toContain('✓')
    await wrapper.get('[data-testid="stage-mode-compact"]').trigger('click')
    await flushPromises()
    const auth = wrapper.get('[data-testid="stage-tile-auth"]').text()
    expect(auth).not.toContain('✓')
  })

  it('★ 紧凑模式：瓦片带轨道标记与方向箭头；右侧是**同一行**的摘要块', async () => {
    failing()
    const { wrapper } = await mountView()
    await wrapper.get('[data-testid="stage-mode-compact"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="stage-tile-auth"]').attributes('data-rail')).toBe('send')
    expect(wrapper.get('[data-testid="stage-tile-first_byte"]').attributes('data-rail')).toBe('recv')
    // 该轨最后一段后面不画短线
    expect(wrapper.get('[data-testid="stage-tile-provider"]').attributes('data-tail')).toBe('1')
    expect(wrapper.get('[data-testid="stage-tile-reply"]').attributes('data-tail')).toBe('1')
    expect(wrapper.get('[data-testid="stage-tile-auth"]').attributes('data-tail')).toBeUndefined()
    // 右侧摘要块在**同一行**（不是另起一行的 .band__summary）
    expect(wrapper.find('[data-testid="stage-band-cright"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="stage-band-cright"]').text()).toContain('gpt-4o')
    expect(wrapper.find('.band__summary').exists()).toBe(false)
  })

  it('★ 紧凑模式：错误来源角标叠在**失败段**的瓦片上（不是塞在行尾）', async () => {
    failing()
    const { wrapper } = await mountView()
    await wrapper.get('[data-testid="stage-mode-compact"]').trigger('click')
    await flushPromises()
    // 失败在上游 ⇒ 失败段＝供应商段
    expect(wrapper.find('[data-testid="stage-tile-provider"] .band__tile-src').exists()).toBe(true)
    expect(wrapper.find('[data-testid="stage-tile-auth"] .band__tile-src').exists()).toBe(false)
  })
})
