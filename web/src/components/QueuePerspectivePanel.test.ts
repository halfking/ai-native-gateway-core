import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'
import QueuePerspectivePanel from './QueuePerspectivePanel.vue'
import { __testing, liveStreamState } from '../composables/liveStreamStore'

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
    const wrapper = mount(QueuePerspectivePanel)

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

    const wrapper = mount(QueuePerspectivePanel)

    expect(wrapper.text()).toContain('最近请求处理轨迹')
    expect(wrapper.text()).toContain('claude-sonnet')
    expect(wrapper.text()).toContain('anthropic')
    expect(wrapper.text()).toContain('820ms')
  })

  // ── OBS-FE2（OBS-BE3 pipeline 层）：缺省隐藏，禁止零值冒充 ──────────────

  it('hides the pipeline overview entirely when the BE3 fields are absent', () => {
    const wrapper = mount(QueuePerspectivePanel)

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
    const wrapper = mount(QueuePerspectivePanel)

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
    const wrapper = mount(QueuePerspectivePanel)

    expect(wrapper.text()).toContain('240ms')
    expect(wrapper.text()).toContain('1890ms')
    expect(wrapper.find('.qp-pipeline-degraded').exists()).toBe(true)
    expect(wrapper.text()).toContain('降级')
  })

  // ── OBS-FE2（节点操作区） ────────────────────────────────────────────────

  it('renders the node ops section only when node_update data exists', () => {
    const wrapper = mount(QueuePerspectivePanel)
    expect(wrapper.text()).not.toContain('节点操作')

    liveStreamState.nodes = [{
      credential_id: 9,
      provider_id: 2,
      provider_code: 'anthropic',
      manual_disabled: false,
      circuit_state: 'open',
    }]
    const wrapper2 = mount(QueuePerspectivePanel)
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
    const wrapper = mount(QueuePerspectivePanel)
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
    const wrapper = mount(QueuePerspectivePanel)
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

    const wrapper = mount(QueuePerspectivePanel)

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
    const wrapper = mount(QueuePerspectivePanel)

    expect(wrapper.text()).toContain('路由选择')
    expect(wrapper.find('.trail-stage').exists()).toBe(false)
  })
})
