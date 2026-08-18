import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import NodeDetailDrawer from './NodeDetailDrawer.vue'
import { liveStreamState } from '../composables/liveStreamStore'

const { monitorSummary, decisions, slidingWindow, modelHistory, resolve, superAdmin } = vi.hoisted(() => ({
  monitorSummary: vi.fn(),
  decisions: vi.fn(),
  slidingWindow: vi.fn(),
  modelHistory: vi.fn(),
  resolve: vi.fn(),
  superAdmin: vi.fn(() => false),
}))

vi.mock('../store', () => ({ authBearer: () => 'test-token', isSuperAdmin: superAdmin }))
vi.mock('../api/routing', () => ({
  resolveRouting: resolve,
  emergencyRepair: vi.fn(),
  patchCandidateBinding: vi.fn(),
}))
vi.mock('../api/providers', () => ({
  updateCredentialLifecycle: vi.fn(),
  CREDENTIAL_LIFECYCLE_STATUSES: ['active', 'disabled', 'suspended', 'retired'],
}))
vi.mock('../api/credential-monitor', () => ({
  getCredentialMonitorSummary: monitorSummary,
  getCredentialDecisions: decisions,
  getSlidingWindow: slidingWindow,
  getModelHistory: modelHistory,
  setManualDisabled: vi.fn(),
  sessionPingCredential: vi.fn(),
  toggleModelAvailability: vi.fn(),
}))

function modelStatus(rawModelName: string, probeState = 'healthy_confirmed') {
  return {
    raw_model_name: rawModelName,
    offer_available: true,
    binding_available: true,
    probe_state: probeState,
    recent_success_rate: 0.98,
    recent_samples: 10,
    p95_latency_ms: 800,
    p95_source: 'live',
    data_source: 'live',
    last_used_at: '2026-08-18T00:00:00Z',
    total_calls: 10,
    effective_state: 'available',
  }
}

function mountDrawer(model?: string) {
  return mount(NodeDetailDrawer, {
    props: {
      modelValue: true,
      node: liveStreamState.nodes[0],
      ...(model !== undefined ? { model } : {}),
    },
    global: { stubs: { Teleport: true } },
  })
}

describe('NodeDetailDrawer model×node scope', () => {
  beforeEach(() => {
    liveStreamState.nodes = [{
      credential_id: 5,
      provider_id: 1,
      provider_code: 'p',
      manual_disabled: false,
      circuit_state: 'closed',
      raw_models: ['m-1', 'm-2'],
    }]
    monitorSummary.mockReset().mockResolvedValue({
      credentials: [{ id: 5, models: [modelStatus('m-1'), modelStatus('m-2')] }],
    })
    decisions.mockReset().mockResolvedValue({ decisions: [] })
    slidingWindow.mockReset().mockResolvedValue({
      entries: [],
      stats: { total: 0, success: 0, failed: 0, failure_rate: 0, error_kinds: {} },
      source: 'redis',
    })
    modelHistory.mockReset().mockResolvedValue({ events: [] })
    resolve.mockReset().mockResolvedValue({
      candidates: [{ credential_id: 5, model_name: 'm-1', tier: 2, weight: 100, routable: true }],
    })
    superAdmin.mockReturnValue(false)
  })

  it('locks the drawer to the scoped model and filters every query in one parallel round', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    // 所有请求按 scope 模型发出，且 monitor 重选被禁用（只调一轮）
    expect(decisions).toHaveBeenCalledWith(5, 50, 'm-1')
    expect(resolve).toHaveBeenCalledWith('m-1')
    expect(slidingWindow).toHaveBeenCalledWith(5, 'm-1', 60)
    expect(modelHistory).toHaveBeenCalledWith(5, 'm-1', 30)
    expect(monitorSummary).toHaveBeenCalledWith(
      { credential_id: 5 },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(resolve).toHaveBeenCalledTimes(1)
    expect(slidingWindow).toHaveBeenCalledTimes(1)

    // 标题与模型状态区只呈现 scope 模型，不显示无关模型
    expect(wrapper.get('h2').text()).toBe('m-1')
    const modelButtons = wrapper.findAll('.nd-model')
    expect(modelButtons).toHaveLength(1)
    expect(modelButtons[0].text()).toContain('m-1')
    expect(wrapper.text()).not.toContain('m-2')
  })

  it('keeps unscoped behaviour for the matrix entry (all node models, credential-wide requests)', async () => {
    const wrapper = mountDrawer()
    await flushPromises()

    expect(decisions).toHaveBeenCalledWith(5, 30)
    // 初始模型 = raw_models[0]，monitor 认可后不触发二次加载
    expect(resolve).toHaveBeenCalledWith('m-1')
    expect(resolve).toHaveBeenCalledTimes(1)

    const modelButtons = wrapper.findAll('.nd-model')
    expect(modelButtons).toHaveLength(2)
    expect(wrapper.text()).toContain('m-2')
  })

  it('reloads when the same node is reopened under a different model scope', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()
    expect(resolve).toHaveBeenNthCalledWith(1, 'm-1')

    await wrapper.setProps({ model: 'm-2' })
    await flushPromises()

    expect(resolve).toHaveBeenLastCalledWith('m-2')
    expect(decisions).toHaveBeenLastCalledWith(5, 50, 'm-2')
    expect(wrapper.get('h2').text()).toBe('m-2')
  })
})
