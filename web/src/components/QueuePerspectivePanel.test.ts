import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import QueuePerspectivePanel from './QueuePerspectivePanel.vue'
import NodeDetailDrawer from './NodeDetailDrawer.vue'
import { __testing, liveStreamState } from '../composables/liveStreamStore'
import { ApiError } from '../api/_core'
import { readLiveStreamPreferences, liveStreamPreferencesStorageKey } from '../composables/liveStreamPreferences'

const { getFeatured, resolveRouting, reorderCandidateBindings, getSlidingWindow, getSlidingWindowBatch, superAdmin, mockedStore } = vi.hoisted(() => ({
  getFeatured: vi.fn(),
  resolveRouting: vi.fn(),
  reorderCandidateBindings: vi.fn(),
  getSlidingWindow: vi.fn().mockResolvedValue({ entries: [], stats: { total: 0, success: 0, failed: 0, failure_rate: 0, error_kinds: {} }, source: 'redis' }),
  getSlidingWindowBatch: vi.fn().mockResolvedValue({ window_minutes: 5, count: 0, results: [] }),
  superAdmin: vi.fn(() => false),
  mockedStore: {
    userInfo: null,
  },
}))

vi.mock('../store', () => ({
  isSuperAdmin: superAdmin,
  isDefaultTenant: () => true,
  store: mockedStore,
  getCurrentTenantId: () => 'default',
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('../api/routing', () => ({
  getFeatured,
  resolveRouting,
  reorderCandidateBindings,
}))
vi.mock('../api/logs', () => ({
  getRequestLogTopModels: vi.fn().mockResolvedValue({ items: [
    { canonical_name: 'gpt-4o', display_name: 'gpt-4o', request_count: 12 },
    { canonical_name: 'claude-sonnet', display_name: 'claude-sonnet', request_count: 8 },
    { canonical_name: 'm-1', display_name: 'm-1', request_count: 3 },
  ] }),
}))
vi.mock('../api/credential-monitor', () => ({
  getCredentialMonitorSummary: vi.fn().mockResolvedValue({ credentials: [] }),
  getCredentialDecisions: vi.fn().mockResolvedValue({ decisions: [] }),
  getSlidingWindow,
  getSlidingWindowBatch,
  getModelHistory: vi.fn().mockResolvedValue({ events: [] }),
  setManualDisabled: vi.fn(),
  toggleModelAvailability: vi.fn(),
}))
vi.mock('../api/providers', () => ({
  updateCredentialLifecycle: vi.fn(),
  CREDENTIAL_LIFECYCLE_STATUSES: ['active', 'disabled', 'suspended', 'retired'],
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { requestJourneys: {
    modelScopeLoading: '正在加载特色模型和近 3 天热门模型…',
    refreshScope: '刷新范围',
    modelScopeError: '模型范围暂不可用，未展示模型节点。',
    noModelNodes: '当前特色/热门模型没有实时节点绑定。',
    nodeDetailHint: '点击节点可查看完整明细、近期窗口、请求记录和维护设置。',
    noNodeRequests: '当前没有关联到该模型节点的实时请求。',
  } } },
})

function mountPanel() {
  return mount(QueuePerspectivePanel, { global: { plugins: [i18n] } })
}


describe('QueuePerspectivePanel', () => {
  beforeEach(() => {
    localStorage.clear()
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      models: [],
      credentials: [],
    }
    liveStreamState.requests = []
    liveStreamState.nodes = []
    getFeatured.mockReset().mockResolvedValue({ featured_models: ['gpt-4o', 'claude-sonnet', 'm-1'] })
    resolveRouting.mockReset().mockResolvedValue({ raw_models: [], candidates: [] })
    reorderCandidateBindings.mockReset().mockResolvedValue({ message: 'updated', items: [] })
    superAdmin.mockReturnValue(false)
  })

  it('shows an idle queue instead of reporting that data is not wired', () => {
    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('当前无排队请求')
    expect(wrapper.text()).not.toContain('队列数据未接入')
  })

  it('shows the latest request processing path', () => {
    liveStreamState.requests = [{
      ts: '2026-08-14T08:00:00Z',
      request_id: 'req-12345678',
      model: 'claude-sonnet',
      provider_code: 'anthropic',
      status: 'success',
      latency_ms: 820,
    }]

    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('最近请求处理轨迹')
    expect(wrapper.text()).toContain('claude-sonnet')
    expect(wrapper.text()).toContain('anthropic')
    expect(wrapper.text()).toContain('820ms')
  })

  // ── OBS-FE2（OBS-BE3 pipeline 层）：缺省隐藏，禁止零值冒充 ──────────────

  it('hides pipeline stats entirely when the BE3 fields are absent', () => {
    const wrapper = mountPanel()

    // 指标条只剩节点健康度一项；pipeline 的排队/在途/p50/p95 不出现
    expect(wrapper.findAll('.qp-stat')).toHaveLength(1)
    expect(wrapper.text()).not.toMatch(/排队 \d/)
    expect(wrapper.text()).not.toMatch(/在途 \d/)
    expect(wrapper.text()).not.toContain('p50')
    expect(wrapper.text()).not.toContain('p95')
    expect(wrapper.find('.qp-stat-degraded').exists()).toBe(false)
    // 节点健康度指标条仍然存在
    expect(wrapper.find('.qp-stats').exists()).toBe(true)
    expect(wrapper.text()).toContain('总 ·')
  })

  it('renders pipeline depth/inFlight and omits absent p50/p95 (never zeroes)', () => {
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      pipeline: { depth: 3, inFlight: 7, degraded: false, waitingMsP50: null, waitingMsP95: null },
      models: [],
      credentials: [],
    }
    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('调度链路')
    expect(wrapper.text()).toContain('排队 3')
    expect(wrapper.text()).toContain('在途 7')
    // 无样本 = 未上报：p50/p95 键整体不出现
    expect(wrapper.text()).not.toContain('p50')
    expect(wrapper.text()).not.toContain('p95')
    expect(wrapper.find('.qp-stat-degraded').exists()).toBe(false)
  })

  it('renders p50/p95 only when sampled and flags degradation', () => {
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      pipeline: { depth: 12, inFlight: 0, degraded: true, waitingMsP50: 240, waitingMsP95: 1890 },
      models: [],
      credentials: [],
    }
    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('240ms')
    expect(wrapper.text()).toContain('1890ms')
    expect(wrapper.find('.qp-stat-degraded').exists()).toBe(true)
    expect(wrapper.text()).toContain('降级')
  })

  // ── 队列深度分区：默认折叠，拥堵时自动展开 ────────────────────────────────

  it('restores queue depth and node-status selections, then persists user changes', async () => {
    localStorage.setItem(liveStreamPreferencesStorageKey(), JSON.stringify({
      version: 1,
      groupBy: 'queue',
      mode: 'small',
      filters: {},
      queue: {
        depthOpen: true,
        statusFilter: { active: true, degraded: false, manualDisabled: false, exhausted: true },
      },
    }))
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      models: [{ model: 'glm-5.2', depth: 2 }],
      credentials: [],
    }
    liveStreamState.nodes = [{
      credential_id: 1,
      provider_id: 2,
      manual_disabled: false,
      circuit_state: 'closed',
      raw_models: ['gpt-4o'],
    }]

    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.find('.qp-depth-body').exists()).toBe(true)
    const boxes = wrapper.findAll('.qp-status-filters input')
    expect((boxes[0].element as HTMLInputElement).checked).toBe(true)
    expect((boxes[1].element as HTMLInputElement).checked).toBe(false)
    expect((boxes[2].element as HTMLInputElement).checked).toBe(false)
    expect((boxes[3].element as HTMLInputElement).checked).toBe(true)

    await wrapper.get('.qp-depth-toggle').trigger('click')
    const saved = readLiveStreamPreferences()
    expect(saved.queue.depthOpen).toBe(false)
  })

  it('collapses queue depth rows by default and expands on toggle', async () => {
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      models: [{ model: 'glm-5.2', depth: 2 }],
      credentials: [{ credential: 7, depth: 1 }],
    }
    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('队列深度')
    expect(wrapper.find('.qp-depth-body').exists()).toBe(false)

    await wrapper.get('.qp-depth-toggle').trigger('click')
    expect(wrapper.find('.qp-depth-body').exists()).toBe(true)
    expect(wrapper.text()).toContain('总队列')
    expect(wrapper.text()).toContain('glm-5.2')
    expect(wrapper.text()).toContain('节点 7')
  })

  it('keeps an explicit collapsed preference during congestion', () => {
    localStorage.setItem(liveStreamPreferencesStorageKey(), JSON.stringify({
      version: 1,
      groupBy: 'queue',
      mode: 'small',
      selectedLegends: [],
      filters: {},
      queue: { depthOpen: false, expandedModels: [], statusFilter: {} },
    }))
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      models: [{ model: 'glm-5.2', depth: 60 }],
      credentials: [],
    }
    const wrapper = mountPanel()
    expect(wrapper.text()).toContain('模型队列拥堵')
    expect(wrapper.find('.qp-depth-body').exists()).toBe(false)
  })

  it('auto-expands queue depth when congestion is detected', () => {
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      models: [{ model: 'glm-5.2', depth: 60 }],
      credentials: [],
    }
    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('模型队列拥堵')
    expect(wrapper.find('.qp-depth-body').exists()).toBe(true)
  })

  // ── 动作事件驱动的处理轨迹（24号 §7） ────────────────────────────────────

  it('drives the processing trail from lifecycle actions with stage and node', async () => {
    liveStreamState.requests = [{
      ts: '2026-08-15T08:00:00Z',
      request_id: 'req-trail-1',
      model: 'glm-5.2',
      provider_code: 'zhipu',
      status: 'in_progress',
      stage: 'forwarding',
      client_protocol: 'anthropic-messages',
    }]
    __testing.handleEnvelope({
      type: 'request_lifecycle',
      ts: '2026-08-15T08:00:00Z',
      action: [
        { request_id: 'req-trail-1', seq: 1, action: 'arrive', ts: '2026-08-15T08:00:01Z' },
        { request_id: 'req-trail-1', seq: 2, action: 'upstream_request', ts: '2026-08-15T08:00:02Z', credential_id: 7 },
      ],
    })

    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('动作事件驱动')
    expect(wrapper.text()).toContain('anthropic-messages')
    expect(wrapper.text()).toContain('forwarding')
    expect(wrapper.text()).toContain('转发请求')
    expect(wrapper.text()).toContain('节点 7')
  })

  it('falls back to snapshot wording when no lifecycle events were pushed', () => {
    liveStreamState.requests = [{
      ts: '2026-08-15T08:00:00Z',
      request_id: 'req-trail-2',
      model: 'glm-5.2',
      provider_code: 'zhipu',
      status: 'in_progress',
    }]
    const wrapper = mountPanel()

    expect(wrapper.text()).toContain('路由选择')
    expect(wrapper.find('.trail-stage').exists()).toBe(false)
  })

  // ── OBS-UI：按模型分组的可用节点（2026-08-17） ────────────────────────────

  it('hides the model-grouped section entirely when no node reports raw_models', () => {
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 2, manual_disabled: false, circuit_state: 'closed' },
      { credential_id: 2, provider_id: 2, manual_disabled: false, circuit_state: 'closed' },
    ]
    const wrapper = mountPanel()
    expect(wrapper.find('.qp-layer--model-groups').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('按模型分组的可用节点')
  })

  it('groups nodes by their raw_models and shows request counts under each model', async () => {
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 2, provider_code: 'a', manual_disabled: false, circuit_state: 'closed', raw_models: ['gpt-4o'] },
      { credential_id: 2, provider_id: 3, provider_code: 'b', manual_disabled: false, circuit_state: 'closed', raw_models: ['gpt-4o', 'claude-sonnet'] },
      { credential_id: 3, provider_id: 4, provider_code: 'c', manual_disabled: false, circuit_state: 'closed', raw_models: ['claude-sonnet'] },
    ]
    __testing.handleEnvelope({
      type: 'request_lifecycle',
      ts: '2026-08-17T00:00:00Z',
      action: [
        { request_id: 'r-gpt', seq: 1, action: 'credential_selected', credential_id: 1 },
        { request_id: 'r-gpt-b', seq: 1, action: 'credential_selected', credential_id: 2 },
        { request_id: 'r-claude', seq: 1, action: 'credential_selected', credential_id: 3 },
        { request_id: 'r-claude-b', seq: 1, action: 'credential_selected', credential_id: 2 },
      ],
    })
    liveStreamState.requests = [
      { ts: '2026-08-17T00:00:01Z', request_id: 'r-gpt', model: 'gpt-4o', status: 'in_progress', latency_ms: 320 },
      { ts: '2026-08-17T00:00:02Z', request_id: 'r-gpt-b', model: 'gpt-4o', status: 'success', latency_ms: 420 },
      { ts: '2026-08-17T00:00:03Z', request_id: 'r-claude', model: 'claude-sonnet', status: 'success', latency_ms: 880 },
      { ts: '2026-08-17T00:00:04Z', request_id: 'r-claude-b', model: 'claude-sonnet', status: 'success', latency_ms: 900 },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    const groupLayer = wrapper.find('.qp-layer--model-groups')
    expect(groupLayer.exists()).toBe(true)
    expect(groupLayer.text()).toContain('按模型分组的可用节点')

    const groups = groupLayer.findAll('.qp-model-group')
    expect(groups).toHaveLength(2)
    // 特色模型优先，其次按近三天热门请求数排序。
    expect(groups[0].text()).toContain('gpt-4o')
    expect(groups[0].text()).toContain('2 节点')
    expect(groups[0].text()).toContain('2 当前请求')
    expect(groups[1].text()).toContain('claude-sonnet')
    expect(groups[1].text()).toContain('2 节点')
    expect(groups[1].text()).toContain('2 当前请求')

    // 节点以 供应商+凭据 小卡片呈现（标题 + 四态点 + 状态摘要）
    const gptCards = groups[0].findAll('.qp-node-card')
    expect(gptCards.map(card => card.get('.qp-node-card-title').text()).sort()).toEqual(['a · #1', 'b · #2'])
    expect(gptCards[0].findAll('.qp-dot')).toHaveLength(4)
    expect(gptCards[0].text()).toMatch(/✓/)
    expect(gptCards[0].text()).toMatch(/5m/)

    // 折叠态下不展开请求列表
    expect(groups[0].findAll('.qp-model-group-requests')).toHaveLength(0)
  })

  it('expands a model to show requests routed to those nodes', async () => {
    liveStreamState.nodes = [
      { credential_id: 5, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
    ]
    __testing.handleEnvelope({
      type: 'request_lifecycle',
      ts: '2026-08-17T00:00:00Z',
      action: [
        { request_id: 'r-A', seq: 1, action: 'credential_selected', credential_id: 5 },
      ],
    })
    liveStreamState.requests = [
      { ts: '2026-08-17T00:00:01Z', request_id: 'r-A', model: 'm-1', status: 'success', latency_ms: 410 },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    const toggle = wrapper.find('.qp-model-group-toggle')
    expect(toggle.exists()).toBe(true)
    await toggle.trigger('click')

    const body = wrapper.find('.qp-model-group-body')
    expect(body.exists()).toBe(true)
    const reqList = body.find('.qp-model-group-requests')
    expect(reqList.exists()).toBe(true)
    // 请求行的节点标示同样使用 供应商 · #凭据
    expect(reqList.text()).toContain('p · #5')
    expect(reqList.text()).toContain('m-1')
    expect(reqList.text()).toContain('success')
    expect(reqList.text()).toContain('410ms')
  })

  it('passes the clicked model group as the drawer scope', async () => {
    liveStreamState.nodes = [
      { credential_id: 5, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1', 'm-2'] },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.get('.qp-node-card').trigger('click')

    const drawer = wrapper.findComponent(NodeDetailDrawer)
    expect(drawer.exists()).toBe(true)
    expect(drawer.props('modelValue')).toBe(true)
    expect(drawer.props('model')).toBe('m-1')
  })

  it('reorders with spaced priorities (step 5) and echoes the server revision', async () => {
    superAdmin.mockReturnValue(true)
    resolveRouting.mockImplementation(async (model: string) => ({
      raw_models: [model],
      reorder_revision: `rev-${model}`,
      candidates: model === 'm-1'
        ? [
            { credential_id: 1, model_name: 'm-1', manual_priority: 5, credential_label: 'alpha-key', effective_concurrency: 2 },
            { credential_id: 2, model_name: 'm-1', manual_priority: 10, credential_label: 'beta-key', effective_concurrency: 8 },
          ]
        : [],
    }))
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
      { credential_id: 2, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    const cards = wrapper.findAll('.qp-node-card')
    expect(cards).toHaveLength(2)
    expect(wrapper.text()).toContain('alpha-key')
    expect(wrapper.text()).toContain('beta-key')
    expect(wrapper.text()).toContain('✓')
    expect(cards.every(card => !card.attributes('draggable'))).toBe(true)
    const handles = wrapper.findAll('.qp-node-card-drag-handle')
    expect(handles.every(h => h.attributes('draggable') === 'true')).toBe(true)
    expect(handles.every(h => h.classes().includes('is-enabled'))).toBe(true)
    const wraps = wrapper.findAll('.qp-node-card-wrap')
    expect(Number.parseInt(wraps[0].attributes('style')?.match(/width:\s*(\d+)px/)?.[1] || '0', 10)).toBeLessThan(
      Number.parseInt(wraps[1].attributes('style')?.match(/width:\s*(\d+)px/)?.[1] || '0', 10),
    )

    const transfer = { effectAllowed: '', dropEffect: '', setData: vi.fn() }
    await handles[0].trigger('dragstart', { dataTransfer: transfer })
    await wraps[1].trigger('dragover', { dataTransfer: transfer })
    await wraps[1].trigger('drop', { dataTransfer: transfer })
    await flushPromises()

    expect(reorderCandidateBindings).toHaveBeenCalledWith(
      [
        { credential_id: 2, raw_model: 'm-1', manual_priority: 5 },
        { credential_id: 1, raw_model: 'm-1', manual_priority: 10 },
      ],
      { rawModel: 'm-1', expectedRevision: 'rev-m-1' },
    )
  })

  it('disables reordering when the server did not return a reorder revision', async () => {
    superAdmin.mockReturnValue(true)
    resolveRouting.mockImplementation(async (model: string) => ({
      raw_models: [model],
      candidates: model === 'm-1'
        ? [
            { credential_id: 1, model_name: 'm-1', manual_priority: 1 },
            { credential_id: 2, model_name: 'm-1', manual_priority: 2 },
          ]
        : [],
    }))
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
      { credential_id: 2, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    const cards = wrapper.findAll('.qp-node-card')
    expect(cards).toHaveLength(2)
    expect(cards.every(card => !card.attributes('draggable'))).toBe(true)
    const handles = wrapper.findAll('.qp-node-card-drag-handle')
    expect(handles.every(h => h.attributes('draggable') === 'false')).toBe(true)
    expect(handles.every(h => !h.classes().includes('is-enabled'))).toBe(true)
    expect(wrapper.html()).toContain('尚未拿到后端修订版本')
  })

  it('surfaces a stale-revision hint and refetches when the backend rejects the reorder with 409', async () => {
    superAdmin.mockReturnValue(true)
    resolveRouting.mockImplementation(async (model: string) => ({
      raw_models: [model],
      reorder_revision: `rev-${model}`,
      candidates: model === 'm-1'
        ? [
            { credential_id: 1, model_name: 'm-1', manual_priority: 1 },
            { credential_id: 2, model_name: 'm-1', manual_priority: 2 },
          ]
        : [],
    }))
    reorderCandidateBindings.mockReset().mockRejectedValueOnce(new ApiError(409, 'stale candidate binding set, refetch and retry'))
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
      { credential_id: 2, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    const handles = wrapper.findAll('.qp-node-card-drag-handle')
    const wraps = wrapper.findAll('.qp-node-card-wrap')
    const transfer = { effectAllowed: '', dropEffect: '', setData: vi.fn() }
    await handles[0].trigger('dragstart', { dataTransfer: transfer })
    await wraps[1].trigger('dragover', { dataTransfer: transfer })
    await wraps[1].trigger('drop', { dataTransfer: transfer })
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('排序已过期')
  })

  it('enables reorder when live nodes are a subset of a larger candidate set', async () => {
    superAdmin.mockReturnValue(true)
    resolveRouting.mockImplementation(async (model: string) => ({
      raw_models: [model],
      reorder_revision: `rev-${model}`,
      candidates: model === 'm-1'
        ? [
            { credential_id: 1, model_name: 'm-1', manual_priority: 5, credential_label: 'live-a', effective_concurrency: 2 },
            { credential_id: 2, model_name: 'm-1', manual_priority: 10, credential_label: 'offline-b', effective_concurrency: 4 },
            { credential_id: 3, model_name: 'm-1', manual_priority: 15, credential_label: 'live-c', effective_concurrency: 8 },
          ]
        : [],
    }))
    // Only credentials 1 and 3 are currently live; #2 is still in the binding set.
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
      { credential_id: 3, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    const cards = wrapper.findAll('.qp-node-card')
    expect(cards).toHaveLength(2)
    expect(wrapper.text()).toContain('live-a')
    expect(wrapper.text()).toContain('live-c')
    expect(cards.every(card => !card.attributes('draggable'))).toBe(true)
    const handles = wrapper.findAll('.qp-node-card-drag-handle')
    expect(handles.every(h => h.attributes('draggable') === 'true')).toBe(true)
    const wraps = wrapper.findAll('.qp-node-card-wrap')
    expect(Number.parseInt(wraps[0].attributes('style')?.match(/width:\s*(\d+)px/)?.[1] || '0', 10)).toBeLessThan(
      Number.parseInt(wraps[1].attributes('style')?.match(/width:\s*(\d+)px/)?.[1] || '0', 10),
    )

    const transfer = { effectAllowed: '', dropEffect: '', setData: vi.fn() }
    await handles[0].trigger('dragstart', { dataTransfer: transfer })
    await wraps[1].trigger('dragover', { dataTransfer: transfer })
    await wraps[1].trigger('drop', { dataTransfer: transfer })
    await flushPromises()

    // Visible order becomes [3, 1]; offline #2 keeps its relative slot → [3, 2, 1].
    expect(reorderCandidateBindings).toHaveBeenCalledWith(
      [
        { credential_id: 3, raw_model: 'm-1', manual_priority: 5 },
        { credential_id: 2, raw_model: 'm-1', manual_priority: 10 },
        { credential_id: 1, raw_model: 'm-1', manual_priority: 15 },
      ],
      { rawModel: 'm-1', expectedRevision: 'rev-m-1' },
    )
  })

  it('reorders visible nodes while preserving hidden candidates in the full set', async () => {
    superAdmin.mockReturnValue(true)
    resolveRouting.mockImplementation(async (model: string) => ({
      raw_models: [model],
      reorder_revision: `rev-${model}`,
      candidates: model === 'm-1'
        ? [
            { credential_id: 1, model_name: 'm-1', manual_priority: 1 },
            { credential_id: 2, model_name: 'm-1', manual_priority: 2 },
            { credential_id: 3, model_name: 'm-1', manual_priority: 3 },
          ]
        : [],
    }))
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
      { credential_id: 2, provider_id: 1, provider_code: 'p', manual_disabled: true, circuit_state: 'closed', raw_models: ['m-1'] },
      { credential_id: 3, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    // Default filter is active-only: cards 1 and 3 visible; card 2 hidden.
    const cards = wrapper.findAll('.qp-node-card')
    expect(cards).toHaveLength(2)
    expect(cards.every(card => !card.attributes('draggable'))).toBe(true)
    const handles = wrapper.findAll('.qp-node-card-drag-handle')
    expect(handles.every(h => h.attributes('draggable') === 'true')).toBe(true)
    const wraps = wrapper.findAll('.qp-node-card-wrap')

    const transfer = { effectAllowed: '', dropEffect: '', setData: vi.fn() }
    await handles[0].trigger('dragstart', { dataTransfer: transfer })
    await wraps[1].trigger('dragover', { dataTransfer: transfer })
    await wraps[1].trigger('drop', { dataTransfer: transfer })
    await flushPromises()

    // Visible order becomes [3, 1]; hidden #2 keeps its relative slot → [3, 2, 1].
    expect(reorderCandidateBindings).toHaveBeenCalledWith(
      [
        { credential_id: 3, raw_model: 'm-1', manual_priority: 5 },
        { credential_id: 2, raw_model: 'm-1', manual_priority: 10 },
        { credential_id: 1, raw_model: 'm-1', manual_priority: 15 },
      ],
      { rawModel: 'm-1', expectedRevision: 'rev-m-1' },
    )
  })

  it('loads window stats via batch API instead of N-way GET', async () => {
    getSlidingWindowBatch.mockReset().mockResolvedValue({
      window_minutes: 5,
      count: 2,
      results: [
        {
          credential_id: 1,
          model: 'm-1',
          source: 'redis',
          stats: { total: 3, success: 2, failed: 1, failure_rate: 0.33 },
          entries: [
            { rid: 'a', ts: 1, ok: true, lat: 10 },
            { rid: 'b', ts: 2, ok: false, lat: 20 },
            { rid: 'c', ts: 3, ok: true, lat: 30 },
          ],
        },
        {
          credential_id: 2,
          model: 'm-1',
          source: 'redis',
          stats: { total: 1, success: 1, failed: 0, failure_rate: 0 },
          entries: [{ rid: 'd', ts: 4, ok: true, lat: 40 }],
        },
      ],
    })
    getSlidingWindow.mockClear()
    resolveRouting.mockImplementation(async (model: string) => ({
      raw_models: [model],
      reorder_revision: `rev-${model}`,
      candidates: model === 'm-1'
        ? [
            { credential_id: 1, model_name: 'm-1', manual_priority: 5, credential_label: 'a' },
            { credential_id: 2, model_name: 'm-1', manual_priority: 10, credential_label: 'b' },
          ]
        : [],
    }))
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
      { credential_id: 2, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
    ]

    const wrapper = mountPanel()
    await flushPromises()
    await flushPromises()

    expect(getSlidingWindowBatch).toHaveBeenCalledWith(
      expect.any(Array),
      expect.objectContaining({ includeEntries: true, entryLimit: 24 }),
      expect.anything(),
    )
    expect(getSlidingWindow).not.toHaveBeenCalled()
    expect(wrapper.text()).toMatch(/✓2/)
    expect(wrapper.text()).toMatch(/✗1/)
    expect(wrapper.findAll('.qp-node-window-cell')).toHaveLength(4)
    expect(wrapper.findAll('.qp-node-window-cell.ok')).toHaveLength(3)
    expect(wrapper.findAll('.qp-node-window-cell.bad')).toHaveLength(1)
  })

  it('clears mini-window cells when a later batch omits empty entries', async () => {
    vi.useFakeTimers()
    try {
      getSlidingWindowBatch.mockReset().mockResolvedValue({
        window_minutes: 5,
        count: 1,
        results: [{
          credential_id: 1,
          model: 'm-1',
          source: 'redis',
          stats: { total: 2, success: 2, failed: 0, failure_rate: 0 },
          entries: [
            { rid: 'a', ts: 1, ok: true, lat: 10 },
            { rid: 'b', ts: 2, ok: true, lat: 20 },
          ],
        }],
      })
      resolveRouting.mockImplementation(async (model: string) => ({
        raw_models: [model],
        reorder_revision: `rev-${model}`,
        candidates: model === 'm-1'
          ? [{ credential_id: 1, model_name: 'm-1', manual_priority: 5, credential_label: 'a' }]
          : [],
      }))
      liveStreamState.nodes = [
        { credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false, circuit_state: 'closed', raw_models: ['m-1'] },
      ]

      const wrapper = mountPanel()
      await flushPromises()
      await flushPromises()
      expect(wrapper.findAll('.qp-node-window-cell')).toHaveLength(2)

      getSlidingWindowBatch.mockResolvedValue({
        window_minutes: 5,
        count: 1,
        results: [{
          credential_id: 1,
          model: 'm-1',
          source: 'redis',
          stats: { total: 0, success: 0, failed: 0, failure_rate: 0 },
        }],
      })
      await vi.advanceTimersByTimeAsync(30_000)
      await flushPromises()
      expect(wrapper.text()).toMatch(/✓0/)
      expect(wrapper.findAll('.qp-node-window-cell')).toHaveLength(0)
    } finally {
      vi.useRealTimers()
    }
  })
})
