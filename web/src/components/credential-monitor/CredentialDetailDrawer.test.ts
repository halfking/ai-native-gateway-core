// CredentialDetailDrawer.test.ts — 抽屉开合（v-model 单向流）、AppDrawer 关闭
// 委托回写 null、详情数据加载委托 refresh-list，以及响应式源码断言
// （AppDrawer/AppModal 承载、640px 白名单断点、无 drawer-backdrop 遗留）。
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import CredentialDetailDrawer from './CredentialDetailDrawer.vue'
import { getCredentialMonitorSummary } from '../../api'
import type { CredentialMonitorSummary } from '../../api'

// 完全自包含的模块 mock：不 importOriginal（真实 api 模块链会连带触发
// 本环境 jsdom localStorage 缺失的存量问题）；工厂内不引用顶层变量
// （vi.mock 会被提升到文件最前，顶层 const 尚未初始化）。
vi.mock('../../api', () => {
  const detail = {
    id: 5,
    label: 'cred-5-detail',
    provider_id: 11,
    provider_name: 'provider-a',
    availability_state: 'ready',
    health_status: 'healthy',
    quota_state: 'ok',
    consecutive_failures: 0,
    manual_disabled: false,
    total_requests: 100,
    concurrency_limit: 5,
    concurrency_limit_auto: null,
    effective_concurrency: 5,
    models: [],
  }
  return {
    getCredentialMonitorSummary: vi.fn(async () => ({ credentials: [detail], meta: null })),
    getCredentialDecisions: vi.fn(async () => ({ decisions: [] })),
    getCredentialFpSlotStats: vi.fn(async () => ({ unlimited: true, message: 'no slots' })),
    getSlidingWindow: vi.fn(async () => ({ entries: [], source: 'redis', stats: { error_kinds: {} } })),
    getModelHistory: vi.fn(async () => ({ events: [] })),
    promoteCredential: vi.fn(async () => ({})),
    demoteCredential: vi.fn(async () => ({})),
    setConcurrencyAuto: vi.fn(async () => ({})),
    toggleModelAvailability: vi.fn(async () => ({})),
    clearManualDisabled: vi.fn(async () => ({})),
    setManualDisabled: vi.fn(async () => ({})),
  }
})

// useCredentialLabels 内部引用真实 api/credential-monitor → store 模块链，
// 同样会触发本环境 jsdom localStorage 缺失问题；以最小替身隔离。
vi.mock('../../composables/useCredentialLabels', () => ({
  useCredentialLabels: () => ({
    credentialDisplayName: (_id: number, fallback: string) => fallback,
    loadCredentialLabels: async () => undefined,
  }),
}))

// store.ts 在模块顶层读取 localStorage（本环境未定义，即存量 40 文件失败
// 的同一环境问题）；组件仅用 isSuperAdmin，以替身隔离。
vi.mock('../../store', () => ({
  isSuperAdmin: () => true,
  getCurrentTenantId: () => 'default',
}))

const source = readFileSync(resolve(process.cwd(), 'src/components/credential-monitor/CredentialDetailDrawer.vue'), 'utf8')

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { button: { close: '关闭' } },
      credentialMonitor: {
        drawer: { tab: { overview: '概览', models: '模型', requests: '历史' } },
        chart: { allHealthy: '全绿', errorsTitle: '错误分布', errorsWhenHealthy: '无错误' },
        error: { clearFailed: '清除失败' },
      },
    },
  },
})

function makeSummary(overrides: Partial<CredentialMonitorSummary> = {}): CredentialMonitorSummary {
  return {
    id: 5,
    label: 'cred-5',
    provider_id: 11,
    provider_name: 'provider-a',
    availability_state: 'ready',
    health_status: 'healthy',
    quota_state: 'ok',
    consecutive_failures: 0,
    manual_disabled: false,
    total_requests: 100,
    concurrency_limit: 5,
    concurrency_limit_auto: null,
    effective_concurrency: 5,
    models: [],
    ...overrides,
  } as CredentialMonitorSummary
}

function mountDrawer(modelValue: CredentialMonitorSummary | null) {
  return mount(CredentialDetailDrawer, {
    props: { modelValue },
    global: { plugins: [i18n], stubs: { Teleport: true } },
    attachTo: document.body,
  })
}

describe('CredentialDetailDrawer', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('modelValue=null 不渲染；传入凭据后渲染 AppDrawer 面板并拉取详情', async () => {
    const w = mountDrawer(null)
    expect(w.find('.app-drawer').exists()).toBe(false)

    await w.setProps({ modelValue: makeSummary() })
    expect(w.find('.app-drawer').exists()).toBe(true)
    await flushPromises()
    expect(vi.mocked(getCredentialMonitorSummary)).toHaveBeenCalled()
    // 详情加载完成后经 update:modelValue 回写完整明细
    const writes = w.emitted('update:modelValue')!
    expect(writes.some((args) => (args[0] as CredentialMonitorSummary)?.label === 'cred-5-detail')).toBe(true)
    // 首次打开不强制刷列表（仅刷新详情时刷，保持原视图行为）
    expect(w.emitted('refresh-list')).toBeUndefined()
    w.unmount()
  })

  it('AppDrawer 遮罩关闭委托回写 update:modelValue=null', async () => {
    const w = mountDrawer(makeSummary())
    await flushPromises()
    await w.find('.app-drawer').trigger('click')
    const writes = w.emitted('update:modelValue')!
    expect(writes[writes.length - 1]).toEqual([null])
    w.unmount()
  })

  it('概览 tab 渲染状态概览与并发限流区块', async () => {
    const w = mountDrawer(makeSummary())
    await flushPromises()
    expect(w.text()).toContain('状态概览')
    expect(w.text()).toContain('并发限流')
    expect(w.text()).toContain('provider-a')
    w.unmount()
  })

  it('源码断言：AppDrawer/AppModal 承载、宽度与断点白名单、无手写弹层遗留', () => {
    expect(source).toContain("import AppDrawer from '../ui/AppDrawer.vue'")
    expect(source).toContain("import AppModal from '../ui/AppModal.vue'")
    // 6 个确认弹窗全部 AppModal（sm 档）
    expect(source.match(/<AppModal/g)).toHaveLength(6)
    expect(source.match(/size="sm"/g)).toHaveLength(6)
    // 外壳宽度保持原 drawer-panel-wide 视觉
    expect(source).toContain('min(1000px, 95vw)')
    // 无手写 drawer-backdrop / drawer-panel 遗留
    expect(source).not.toContain('drawer-backdrop')
    expect(source).not.toContain('class="drawer-panel')
    // 断点只允许白名单值（640 为存量过渡值）
    expect(source).toMatch(/@media \(max-width: 640px\)/)
    expect(source).not.toMatch(/max-width: (700|720|760|800|900|960)px/)
  })
})
