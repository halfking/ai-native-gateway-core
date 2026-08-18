import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import QueuePerspectivePanel from './QueuePerspectivePanel.vue'
import { __testing, liveStreamState } from '../composables/liveStreamStore'

vi.mock('../api/routing', () => ({
  getFeatured: vi.fn().mockResolvedValue({ featured_models: ['gpt-4o', 'claude-sonnet', 'm-1'] }),
}))
vi.mock('../api/logs', () => ({
  getRequestLogTopModels: vi.fn().mockResolvedValue({ items: [
    { canonical_name: 'gpt-4o', display_name: 'gpt-4o', request_count: 12 },
    { canonical_name: 'claude-sonnet', display_name: 'claude-sonnet', request_count: 8 },
    { canonical_name: 'm-1', display_name: 'm-1', request_count: 3 },
  ] }),
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
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      models: [],
      credentials: [],
    }
    liveStreamState.requests = []
    liveStreamState.nodes = []
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

  it('hides the pipeline overview entirely when the BE3 fields are absent', () => {
    const wrapper = mountPanel()

    expect(wrapper.find('.qp-pipeline').exists()).toBe(false)
    // 禁止用零值冒充 pipeline 统计：p50/p95/降级键整体不出现
    expect(wrapper.text()).not.toContain('等待 p50')
    expect(wrapper.text()).not.toContain('p95')
    expect(wrapper.find('.qp-pipeline-degraded').exists()).toBe(false)
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

    expect(wrapper.find('.qp-pipeline').exists()).toBe(true)
    expect(wrapper.text()).toContain('排队')
    expect(wrapper.text()).toContain('3')
    expect(wrapper.text()).toContain('在途')
    expect(wrapper.text()).toContain('7')
    // 无样本 = 未上报：p50/p95 键整体不出现
    expect(wrapper.text()).not.toContain('等待 p50')
    expect(wrapper.text()).not.toContain('p95')
    expect(wrapper.find('.qp-pipeline-degraded').exists()).toBe(false)
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
    expect(wrapper.find('.qp-pipeline-degraded').exists()).toBe(true)
    expect(wrapper.text()).toContain('降级')
  })

  // ── OBS-FE2（节点操作区） ────────────────────────────────────────────────

  it('renders the node ops section only when node_update data exists', () => {
    const wrapper = mountPanel()
    expect(wrapper.text()).not.toContain('节点操作')

    liveStreamState.nodes = [{
      credential_id: 9,
      provider_id: 2,
      provider_code: 'anthropic',
      manual_disabled: false,
      circuit_state: 'open',
    }]
    const wrapper2 = mountPanel()
    expect(wrapper2.text()).toContain('节点操作')
    expect(wrapper2.text()).toContain('节点 9')
    expect(wrapper2.text()).toContain('强制启用')
    expect(wrapper2.text()).toContain('手工禁用 ▼')
  })

  it('sorts unhealthy nodes first in the ops list', () => {
    liveStreamState.nodes = [
      { credential_id: 1, provider_id: 2, manual_disabled: false, circuit_state: 'closed', health_status: 'healthy' },
      { credential_id: 2, provider_id: 2, manual_disabled: true, circuit_state: 'closed' },
      { credential_id: 3, provider_id: 2, manual_disabled: false, circuit_state: 'open' },
    ]
    const wrapper = mountPanel()
    const rows = wrapper.findAll('.node-ops-row .nor-id')
    expect(rows.map(r => r.text())).toEqual(['节点 2', '节点 3', '节点 1'])
  })

  it('labels truncation explicitly when more than 8 nodes exist', () => {
    liveStreamState.nodes = Array.from({ length: 10 }, (_, i) => ({
      credential_id: i + 1,
      provider_id: 2,
      manual_disabled: false,
      circuit_state: 'closed',
    }))
    const wrapper = mountPanel()
    expect(wrapper.findAll('.node-ops-row')).toHaveLength(8)
    expect(wrapper.text()).toContain('8/10 个节点')
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
    expect(reqList.text()).toContain('m-1')
    expect(reqList.text()).toContain('success')
    expect(reqList.text()).toContain('410ms')
  })
})
