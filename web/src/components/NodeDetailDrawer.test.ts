import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import NodeDetailDrawer from './NodeDetailDrawer.vue'
import type { RoutingCandidate } from '../api/routing'
import { liveStreamState } from '../composables/liveStreamStore'

const openRequestDetailPageMock = vi.hoisted(() => vi.fn())

vi.mock('../utils/openRequestDetailPage', () => ({
  openRequestDetailPage: openRequestDetailPageMock,
}))

const {
  monitorSummary,
  decisions,
  slidingWindow,
  modelHistory,
  resolve,
  superAdmin,
  defaultTenant,
  fpSlotStats,
  setConcurrencyAuto,
  updateCredential,
  getRequestLogDetail,
} = vi.hoisted(() => ({
  monitorSummary: vi.fn(),
  decisions: vi.fn(),
  slidingWindow: vi.fn(),
  modelHistory: vi.fn(),
  resolve: vi.fn(),
  superAdmin: vi.fn(() => false),
  defaultTenant: vi.fn(() => true),
  fpSlotStats: vi.fn(),
  setConcurrencyAuto: vi.fn(),
  updateCredential: vi.fn(),
  getRequestLogDetail: vi.fn(),
}))

vi.mock('../store', () => ({ authBearer: () => 'test-token', isSuperAdmin: superAdmin, isDefaultTenant: defaultTenant }))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))
vi.mock('../api/routing', () => ({
  resolveRouting: resolve,
  emergencyRepair: vi.fn(),
  patchCandidateBinding: vi.fn(),
}))
vi.mock('../api/providers', () => ({
  updateCredentialLifecycle: vi.fn(),
  updateCredential,
  getCredentialFpSlotStats: fpSlotStats,
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
  testCredentialModel: vi.fn(),
  setConcurrencyAuto,
}))
vi.mock('../api/logs', () => ({
  getRequestLogDetail,
}))

const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })

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
    global: {
      plugins: [i18n],
      stubs: {
        Teleport: true,
        RequestLogDrawer: {
          name: 'RequestLogDrawer',
          props: ['requestId', 'stackLevel'],
          template: '<div class="stub-request-log" :data-id="requestId || \'\'" :data-stack="stackLevel || \'\'" />',
        },
        NodeDetailConcurrencyPanel: {
          name: 'NodeDetailConcurrencyPanel',
          props: ['credentialId', 'providerId', 'monitor', 'monitorLoading', 'canEdit'],
          template: '<div class="stub-concurrency">并发与指纹槽位</div>',
        },
        NodeDetailOtherModelsPanel: {
          name: 'NodeDetailOtherModelsPanel',
          props: ['credentialId', 'currentModel', 'models', 'canEdit', 'needsRefresh'],
          template: '<div class="stub-other-models">其它模型面板</div>',
        },
        NodeDetailAccessErrorsPanel: false,
        NodeDetailAvailabilityPanel: {
          name: 'NodeDetailAvailabilityPanel',
          props: ['candidate', 'loading'],
          template: '<div class="stub-availability">可用性面板</div>',
        },
        NodeDetailEmergencyPanel: {
          name: 'NodeDetailEmergencyPanel',
          props: ['candidate', 'rawModel', 'canEdit'],
          template: '<div class="stub-emergency">紧急维护面板</div>',
        },
        FpSlotVisualizer: true,
      },
    },
  })
}

async function clickMainTab(wrapper: ReturnType<typeof mountDrawer>, label: string) {
  const tab = wrapper.findAll('[role="tab"]').find(btn => btn.text().includes(label))
  expect(tab).toBeTruthy()
  await tab!.trigger('click')
  await flushPromises()
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
    monitorSummary.mockReset().mockImplementation(async (opts: { mode?: string }) => ({
      credentials: [{
        id: 5,
        provider_id: 1,
        concurrency_limit: null,
        concurrency_limit_auto: 4,
        effective_concurrency: 4,
        models: opts.mode === 'detail'
          ? [modelStatus('m-1'), modelStatus('m-2')]
          : [modelStatus('m-1')],
      }],
    }))
    decisions.mockReset().mockResolvedValue({ decisions: [] })
    slidingWindow.mockReset().mockResolvedValue({
      entries: [{ rid: 'req-abc', ts: Date.now(), ok: true, lat: 120 }],
      stats: { total: 1, success: 1, failed: 0, failure_rate: 0, error_kinds: {} },
      source: 'redis',
    })
    modelHistory.mockReset().mockResolvedValue({ events: [] })
    resolve.mockReset().mockResolvedValue({
      candidates: [{
        credential_id: 5,
        provider_id: 1,
        model_name: 'm-1',
        tier: 2,
        weight: 100,
        routable: true,
      }],
    })
    fpSlotStats.mockReset().mockResolvedValue({
      credential_id: 5,
      slot_limit: 3,
      healthy_slots: 3,
      occupied_slots: 1,
      free_slots: 2,
      details: [],
    })
    setConcurrencyAuto.mockReset().mockResolvedValue({ success: true })
    updateCredential.mockReset().mockResolvedValue({ message: 'ok' })
    getRequestLogDetail.mockReset().mockResolvedValue({ request_id: 'req-abc' })
    superAdmin.mockReturnValue(false)
    defaultTenant.mockReturnValue(true)
  })

  it('auto-loads all three tabs in parallel when the drawer opens', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    expect(monitorSummary).toHaveBeenCalledWith(
      { credential_id: 5, mode: 'core' },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(monitorSummary).toHaveBeenCalledWith(
      { credential_id: 5, mode: 'detail' },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(resolve).toHaveBeenCalledWith('m-1', undefined, false, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(slidingWindow).toHaveBeenCalledWith(5, 'm-1', 60, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(modelHistory).toHaveBeenCalledWith(5, 'm-1', 30, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(decisions).toHaveBeenCalledWith(5, 50, 'm-1', expect.objectContaining({ signal: expect.any(AbortSignal) }))

    expect(wrapper.get('h2').text()).toBe('m-1')
    expect(wrapper.text()).toContain('实时状态')
    expect(wrapper.find('.nd-window-cell').exists()).toBe(true)
  })

  it('opens availability tab with seedCandidate and shows emergency panel in settings', async () => {
    // Partial seed: the drawer tolerates missing fields via ?? defaults,
    // but the prop type is the full RoutingCandidate — cast the literal.
    const seed = {
      credential_id: 5,
      provider_id: 1,
      model_name: 'm-1',
      available: true,
      runtime_routable: true,
      routable: true,
      tier: 1,
      weight: 100,
    } as RoutingCandidate
    const wrapper = mount(NodeDetailDrawer, {
      props: {
        modelValue: true,
        node: liveStreamState.nodes[0],
        model: 'm-1',
        initialTab: 'availability',
        seedCandidate: seed,
      },
      global: {
        plugins: [i18n],
        stubs: {
          Teleport: true,
          RequestLogDrawer: true,
          NodeDetailConcurrencyPanel: true,
          NodeDetailOtherModelsPanel: true,
          NodeDetailAccessErrorsPanel: true,
          NodeDetailAvailabilityPanel: {
            name: 'NodeDetailAvailabilityPanel',
            props: ['candidate', 'loading'],
            template: '<div class="stub-availability">可用性面板</div>',
          },
          NodeDetailEmergencyPanel: {
            name: 'NodeDetailEmergencyPanel',
            props: ['candidate', 'rawModel', 'canEdit'],
            template: '<div class="stub-emergency">紧急维护面板</div>',
          },
        },
      },
    })
    await flushPromises()
    expect(wrapper.find('.stub-availability').exists()).toBe(true)
    const tabs = wrapper.findAll('[role="tab"]')
    await tabs.find(btn => btn.text().includes('设置与维护'))!.trigger('click')
    await flushPromises()
    expect(wrapper.find('.stub-emergency').exists()).toBe(true)
  })

  it('locks the drawer to the scoped model and filters model buttons', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    const modelButtons = wrapper.findAll('.nd-model')
    expect(modelButtons).toHaveLength(1)
    expect(modelButtons[0].text()).toContain('m-1')
    expect(wrapper.text()).not.toContain('m-2')
  })

  it('keeps unscoped behaviour for the matrix entry (all node models)', async () => {
    resolve.mockResolvedValue({
      candidates: [{
        credential_id: 5,
        provider_id: 1,
        model_name: 'm-1',
        tier: 2,
        weight: 100,
        routable: true,
      }],
    })
    const wrapper = mountDrawer()
    await flushPromises()

    expect(resolve).toHaveBeenCalledWith('m-1', undefined, false, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    const modelButtons = wrapper.findAll('.nd-model')
    expect(modelButtons).toHaveLength(2)
    expect(wrapper.text()).toContain('m-2')
  })

  it('opens request detail when a sliding-window cell is clicked', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    await wrapper.get('.nd-window-cell').trigger('click')
    expect(openRequestDetailPageMock).toHaveBeenCalledWith('req-abc')
  })

  it('shows settings sections with concurrency panel without a manual load button', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()
    await clickMainTab(wrapper, '设置与维护')

    expect(wrapper.text()).toContain('连通性')
    expect(wrapper.text()).toContain('紧急维护')
    expect(wrapper.text()).toContain('并发与指纹槽位')
    expect(wrapper.text()).not.toContain('加载设置面板')
  })

  it('shows other-models panel on top-level tab', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()
    await clickMainTab(wrapper, '其它模型')
    expect(wrapper.find('.stub-other-models').exists()).toBe(true)
    expect(wrapper.find('.nd-subtabs').exists()).toBe(false)
  })

  it('lists failed window entries in 最近访问与异常', async () => {
    slidingWindow.mockResolvedValue({
      entries: [
        { rid: 'req-ok', ts: Date.now(), ok: true, lat: 100 },
        { rid: 'req-fail', ts: Date.now(), ok: false, lat: 50, err: 'timeout' },
      ],
      stats: { total: 2, success: 1, failed: 1, failure_rate: 0.5, error_kinds: { timeout: 1 } },
      source: 'redis',
    })
    decisions.mockResolvedValue({
      decisions: [{
        request_id: 'dec-fail',
        model: 'm-1',
        ts: '2026-08-21T00:00:00Z',
        success: false,
        latency_ms: 30,
        error_class: 'upstream_5xx',
      }],
    })
    const wrapper = mountDrawer('m-1')
    await flushPromises()
    expect(wrapper.text()).toContain('最近访问与异常')
    expect(wrapper.text()).toContain('timeout')
    expect(wrapper.text()).toContain('upstream_5xx')
  })

  it('disables maintain actions for non-default tenant', async () => {
    defaultTenant.mockReturnValue(false)
    const wrapper = mountDrawer('m-1')
    await flushPromises()
    await clickMainTab(wrapper, '设置与维护')
    expect(wrapper.text()).toContain('仅 default 租户可以维护节点')
    const ping = wrapper.findAll('button').find(b => b.text().includes('会话 Ping'))
    expect(ping?.attributes('disabled')).toBeDefined()
  })

  it('aborts in-flight core and detail requests when the drawer is closed', async () => {
    let observedSignal: AbortSignal | undefined
    monitorSummary.mockImplementationOnce((_opts: any, requestOptions?: any) => {
      observedSignal = requestOptions?.signal
      return new Promise(resolve => {
        requestOptions?.signal?.addEventListener('abort', () => resolve({ credentials: [] }))
      })
    })

    const wrapper = mountDrawer('m-1')
    await flushPromises()
    expect(observedSignal).toBeDefined()
    expect(observedSignal!.aborted).toBe(false)

    await wrapper.setProps({ modelValue: false })
    await flushPromises()
    expect(observedSignal!.aborted).toBe(true)
  })

  it('discards a stale core response when the node changes before the promise settles', async () => {
    const wrapper = mountDrawer('m-1')

    let resolveFirst!: (value: unknown) => void
    monitorSummary.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))

    await flushPromises()

    liveStreamState.nodes = [{
      credential_id: 99,
      provider_id: 1,
      provider_code: 'p',
      manual_disabled: false,
      circuit_state: 'closed',
      raw_models: ['x-1'],
    }]
    await wrapper.setProps({ node: liveStreamState.nodes[0] })
    await flushPromises()

    resolveFirst({ credentials: [{ id: 5, models: [] }] })
    await flushPromises()

    expect(wrapper.get('h2').text()).not.toBe('5')
  })

  it('discards stale model history after the scoped model changes', async () => {
    let resolveHistory!: (value: unknown) => void
    modelHistory.mockImplementationOnce(() => new Promise(resolve => { resolveHistory = resolve }))
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    await wrapper.setProps({ model: 'm-2' })
    await flushPromises()
    resolveHistory({ events: [{ event: 'broke', source: 'auto', ts: '2026-08-20T00:00:00Z', error_message: 'stale-history' }] })
    await flushPromises()

    expect(wrapper.text()).not.toContain('stale-history')
    expect(wrapper.get('h2').text()).toBe('m-2')
  })

  it('discards stale decisions after the scoped model changes', async () => {
    let resolveDecisions!: (value: unknown) => void
    decisions.mockImplementationOnce(() => new Promise(resolve => { resolveDecisions = resolve }))
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    await wrapper.setProps({ model: 'm-2' })
    await flushPromises()
    resolveDecisions({ decisions: [{ request_id: 'stale-decision', model: 'm-1', ts: '2026-08-20T00:00:00Z', success: false }] })
    await flushPromises()

    expect(wrapper.text()).not.toContain('stale-decision')
    expect(wrapper.get('h2').text()).toBe('m-2')
  })

  it('clears stale detail and reloads the new model scope automatically', async () => {
    const wrapper = mountDrawer('m-1')
    await flushPromises()
    expect(resolve).toHaveBeenCalledWith('m-1', undefined, false, expect.objectContaining({ signal: expect.any(AbortSignal) }))

    await wrapper.setProps({ model: 'm-2' })
    await flushPromises()

    expect(resolve).toHaveBeenCalledWith('m-2', undefined, false, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.text()).not.toContain('加载明细数据')
    expect(wrapper.get('h2').text()).toBe('m-2')
  })
})
