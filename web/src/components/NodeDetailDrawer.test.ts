import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import NodeDetailDrawer from './NodeDetailDrawer.vue'
import type { RoutingCandidate } from '../api/routing'
import { liveStreamState } from '../composables/liveStreamStore'
// 全局 i18n 实例（`useNodeDetailDrawerLoad` 用的就是它，不是下面那个
// 组件内自建的 `i18n`）——只读提示的文案由它解析，切语言要切它。
import { i18n as appI18n } from '../i18n'

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
          template: '<div class="stub-concurrency" :data-can-edit="String(canEdit)">并发与指纹槽位</div>',
        },
        NodeDetailOtherModelsPanel: {
          name: 'NodeDetailOtherModelsPanel',
          props: ['credentialId', 'currentModel', 'models', 'canEdit', 'needsRefresh'],
          template: '<div class="stub-other-models" :data-can-edit="String(canEdit)">其它模型面板</div>',
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
          template: '<div class="stub-emergency" :data-can-edit="String(canEdit)">紧急维护面板</div>',
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
    // ★ 2026-10-21：原文断言的是**硬编码中文字面量**「仅 default 租户可以维护节点」。
    //   那条断言**只因缺陷而绿**——提示语当时是源码里的字面量，与界面语言无关。
    //   提示改成 i18n 键后，本用例在 jsdom（navigator.language=en-US ⇒ locale 'en'）
    //   立刻变红 ⇒ 反过来证明「非 default 租户的用户在非中文界面下看到的是中文」
    //   是个真实缺陷（`defaultLocale()` 按 navigator.language 选语种）。
    //   现在断言「提示 = i18n 解析结果」，不钉字面量、随界面语言走。
    expect(wrapper.text()).toContain(appI18n.global.t('ops.overview.nodeDetailDefaultTenantOnlyHint'))
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

/**
 * ★ 写侧角色门（2026-10-20）
 *
 * 节点详情抽屉里的**每一个**写操作，后端都要求 super_admin：
 *   · session-ping / models-test / emergency-repair / candidate-binding → h.superAdmin
 *   · set-concurrency-auto / model-toggle / set|clear-manual-disabled → RequireSuperAdminForWrite
 *   · PATCH /api/providers/{id}/credentials/{id}（手动并发 / 指纹槽位 / 生命周期）
 *     → providerConsole 对 tenant_admin 只读（admin/provider_access.go:67-74）
 *
 * 而抽屉的这层门原先只判「是否 default 租户」：default 租户的 tenant_admin
 * canEdit=true ⇒ 能点到写按钮，点下去只拿到 403。
 *
 * 判据纪律（AGENTS.md）：
 *   ① 断言落在**组件真正收到的那层**（面板拿到的 canEdit），不是源码文本；
 *   ② **允许侧也要有正向断言**（super_admin 确实拿到 true），否则
 *      「因为压根没渲染」也能让阻断侧全绿 ⇒ 假绿。
 */
describe('★ 写侧角色门 —— 节点详情抽屉的写操作一律要求 super_admin', () => {
  it('default 租户的 tenant_admin ⇒ 三个写面板拿到的 canEdit 都是 false', async () => {
    superAdmin.mockReturnValue(false)
    defaultTenant.mockReturnValue(true)
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    await clickMainTab(wrapper, '其它模型')
    expect(wrapper.find('.stub-other-models').attributes('data-can-edit')).toBe('false')

    await clickMainTab(wrapper, '设置与维护')
    expect(wrapper.find('.stub-concurrency').attributes('data-can-edit')).toBe('false')
    expect(wrapper.find('.stub-emergency').attributes('data-can-edit')).toBe('false')
  })

  it('★ 允许侧正向断言：super_admin 拿到的 canEdit 是 true（防假绿）', async () => {
    superAdmin.mockReturnValue(true)
    defaultTenant.mockReturnValue(true)
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    await clickMainTab(wrapper, '其它模型')
    expect(wrapper.find('.stub-other-models').attributes('data-can-edit')).toBe('true')

    await clickMainTab(wrapper, '设置与维护')
    expect(wrapper.find('.stub-concurrency').attributes('data-can-edit')).toBe('true')
    expect(wrapper.find('.stub-emergency').attributes('data-can-edit')).toBe('true')
  })

  it('非 default 租户 ⇒ 仍是 false（原有的租户门不被放宽）', async () => {
    superAdmin.mockReturnValue(true)
    defaultTenant.mockReturnValue(false)
    const wrapper = mountDrawer('m-1')
    await flushPromises()

    await clickMainTab(wrapper, '其它模型')
    expect(wrapper.find('.stub-other-models').attributes('data-can-edit')).toBe('false')
  })
})

/**
 * ★ 只读提示的本地化（2026-10-21 批判式审计的订正）
 *
 * `editGateHint` 有**两个**阻塞分支，各给一句提示：
 *   · 非 default 租户       → 「仅 default 租户可以维护节点…」
 *   · default 但非 super_admin → 「仅超级管理员可以维护节点…」
 * 上一轮只把**第二支**接上了 i18n 键，第一支仍是**硬编码中文字面量**
 * ⇒ 非 default 租户的用户无论界面语言是什么，都只看到中文。
 *
 * 判据纪律（AGENTS.md）：
 *   ① **断言必须落在「界面语言变了、提示也跟着变」这个用户可见效果上。**
 *      只断言「提示等于 zh-CN 文案」是**假绿**——zh-CN 文案与被替换掉的
 *      硬编码字面量**逐字相同**，改回硬编码照样全绿。
 *      ⇒ 本块把 i18n 实例切到 'en' 再断言英文文案 + **零中文字符**。
 *   ② 抽屉组件用的是**组件内自建的** i18n 实例（`createI18n`），
 *      而 `useNodeDetailDrawerLoad` 用的是**全局** `../i18n` 实例
 *      ⇒ 提示文案走全局实例，必须改全局实例的 locale 才切得动。
 *   ③ 本 describe 自带 beforeEach（不依赖前一个 describe 留下的
 *      `liveStreamState.nodes`，避免顺序依赖的隐性前提）。
 */
describe('★ 只读提示的本地化 —— 两个阻塞分支都必须走 i18n', () => {
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
      credentials: [{
        id: 5, provider_id: 1, concurrency_limit: null, concurrency_limit_auto: 4,
        effective_concurrency: 4, models: [modelStatus('m-1')],
      }],
    })
    decisions.mockReset().mockResolvedValue({ decisions: [] })
    slidingWindow.mockReset().mockResolvedValue({
      entries: [], stats: { total: 0, success: 0, failed: 0, failure_rate: 0, error_kinds: {} },
      source: 'redis',
    })
    modelHistory.mockReset().mockResolvedValue({ events: [] })
    resolve.mockReset().mockResolvedValue({ candidates: [] })
  })

  it('★ 非 default 租户：英文界面下提示是英文（防「改回硬编码中文」假绿）', async () => {
    defaultTenant.mockReturnValue(false)
    superAdmin.mockReturnValue(true)
    const prev = appI18n.global.locale.value
    appI18n.global.locale.value = 'en'
    try {
      const wrapper = mountDrawer('m-1')
      await flushPromises()
      await clickMainTab(wrapper, '其它模型')

      const hint = wrapper.find('.nd-notice--warn')
      expect(hint.exists()).toBe(true)
      expect(hint.text()).toBe('Only the default tenant can maintain nodes; showing read-only.')
      expect(hint.text()).not.toMatch(/[一-鿿]/)
    } finally {
      appI18n.global.locale.value = prev
    }
  })

  it('非 default 租户：zh-CN 界面下解析成本地化文案（不是原始 key 路径）', async () => {
    defaultTenant.mockReturnValue(false)
    superAdmin.mockReturnValue(true)
    const prev = appI18n.global.locale.value
    appI18n.global.locale.value = 'zh-CN'
    try {
      const wrapper = mountDrawer('m-1')
      await flushPromises()
      await clickMainTab(wrapper, '其它模型')

      const hint = wrapper.find('.nd-notice--warn')
      expect(hint.exists()).toBe(true)
      // v735 式事故的护栏：vue-i18n 解析不到时会**原样渲染 key 路径**。
      expect(hint.text()).not.toBe('ops.overview.nodeDetailDefaultTenantOnlyHint')
      expect(hint.text()).toBe(appI18n.global.t('ops.overview.nodeDetailDefaultTenantOnlyHint'))
    } finally {
      appI18n.global.locale.value = prev
    }
  })

  it('default 租户但非 super_admin：英文界面下提示也是英文（同一族两支口径一致）', async () => {
    defaultTenant.mockReturnValue(true)
    superAdmin.mockReturnValue(false)
    const prev = appI18n.global.locale.value
    appI18n.global.locale.value = 'en'
    try {
      const wrapper = mountDrawer('m-1')
      await flushPromises()
      await clickMainTab(wrapper, '其它模型')

      const hint = wrapper.find('.nd-notice--warn')
      expect(hint.exists()).toBe(true)
      expect(hint.text()).toBe('Only super admins can maintain nodes; showing read-only.')
      expect(hint.text()).not.toMatch(/[一-鿿]/)
    } finally {
      appI18n.global.locale.value = prev
    }
  })

  it('★ 运行时切换界面语言 ⇒ 提示跟着变（不是只在挂载时取一次）', async () => {
    // `editGateHint` 是 computed，里面调 `i18n.global.t(...)`。
    // 若 t() 没有把 locale 记成依赖，computed 会**缓存旧语种**的文案 ⇒
    // 用户在界面上切语言后，这句提示还停在上一种语言。
    // ⇒ 本条断言「切换发生在挂载**之后**」时提示也会更新。
    defaultTenant.mockReturnValue(false)
    superAdmin.mockReturnValue(true)
    const prev = appI18n.global.locale.value
    appI18n.global.locale.value = 'zh-CN'
    try {
      const wrapper = mountDrawer('m-1')
      await flushPromises()
      await clickMainTab(wrapper, '其它模型')
      expect(wrapper.find('.nd-notice--warn').text()).toMatch(/[一-鿿]/)

      appI18n.global.locale.value = 'en'
      await flushPromises()

      expect(wrapper.find('.nd-notice--warn').text())
        .toBe('Only the default tenant can maintain nodes; showing read-only.')
    } finally {
      appI18n.global.locale.value = prev
    }
  })
})
