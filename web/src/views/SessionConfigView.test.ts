import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const listModulesMock = vi.fn()
const getSettingMock = vi.fn()
const getTenantSettingMock = vi.fn()
const updateSettingMock = vi.fn()
const updateTenantSettingMock = vi.fn()
const getAvailableModelsMock = vi.fn()
const getCompressionStatsMock = vi.fn()

const messages = {
  'zh-CN': {
    sessions: {
      config: {
        title: '会话配置',
        subtitle: '统一管理会话审批、压缩策略和健康评分',
        approvalTab: '审批规则',
        compressionTab: '压缩策略',
        healthTab: '健康评分',
        tabsLabel: '会话配置分区',
        platformScope: '平台',
        tenantScope: '租户',
        loading: '加载中',
      },
    },
  },
}

vi.mock('../api/modules', () => ({
  listModules: (...args: any[]) => listModulesMock(...args),
  getModuleEnabled: async (key: string) => {
    const data = await listModulesMock()
    return Boolean(data.find((m: any) => m.key === key && m.enabled))
  },
}))

vi.mock('../api/settings', () => ({
  getSetting: (...args: any[]) => getSettingMock(...args),
  getTenantSetting: (...args: any[]) => getTenantSettingMock(...args),
  updateSetting: (...args: any[]) => updateSettingMock(...args),
  updateTenantSetting: (...args: any[]) => updateTenantSettingMock(...args),
}))

vi.mock('../api/models', () => ({
  getAvailableModels: (...args: any[]) => getAvailableModelsMock(...args),
}))

vi.mock('../api', () => ({
  getCompressionStats: (...args: any[]) => getCompressionStatsMock(...args),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('../components/ApprovalConfigPanel.vue', () => ({
  default: { name: 'ApprovalConfigPanel', template: '<div data-stub="approval" />' },
}))
vi.mock('../components/CompressionConfigPanel.vue', () => ({
  default: { name: 'CompressionConfigPanel', template: '<div data-stub="compression" />' },
}))
vi.mock('../components/HealthScoreConfigPanel.vue', () => ({
  default: { name: 'HealthScoreConfigPanel', template: '<div data-stub="health" />' },
}))

vi.mock('../store', () => ({
  getCurrentTenantId: () => 'tenant-a',
  store: { userInfo: { id: 1, tenant_id: 'tenant-a', role: 'super_admin' } },
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages,
})

async function loadView() {
  const { default: SessionConfigView } = await import('./SessionConfigView.vue')
  const wrapper = mount(SessionConfigView, { global: { plugins: [i18n] } })
  await flushPromises()
  return wrapper
}

describe('SessionConfigView tabs and module gating', () => {
  beforeEach(() => {
    listModulesMock.mockReset()
    getSettingMock.mockReset()
    getTenantSettingMock.mockReset()
    updateSettingMock.mockReset()
    updateTenantSettingMock.mockReset()
    getAvailableModelsMock.mockReset()
    getCompressionStatsMock.mockReset()

    listModulesMock.mockResolvedValue([
      { key: 'compression', enabled: true },
      { key: 'session_inspector', enabled: false },
    ])
    getSettingMock.mockResolvedValue({ value: true, default: true, source: 'default', spec: { default: true } })
    getTenantSettingMock.mockResolvedValue({ value: 'llm', default: 'llm', source: 'default', spec: { default: 'llm' } })
    updateSettingMock.mockResolvedValue({ status: 'ok', new_value: true })
    updateTenantSettingMock.mockResolvedValue({ status: 'ok', new_value: 'llm' })
    getAvailableModelsMock.mockResolvedValue({ families: [], popular: [], unmapped: [], total_raw: 0 })
    getCompressionStatsMock.mockResolvedValue({ total_requests: 0, compressed_total: 0, compression_rate: 0, estimated_tokens_saved: 0 })
  })

  it('hides health tab when session_inspector module is disabled', async () => {
    const wrapper = await loadView()
    const labels = wrapper.findAll('[role="tab"]').map((tab) => tab.text().trim())
    expect(labels).toEqual(['审批规则', '压缩策略'])
    expect(wrapper.find('[data-stub="health"]').exists()).toBe(false)
  })

  it('shows health tab and panel when session_inspector is enabled', async () => {
    listModulesMock.mockResolvedValue([{ key: 'session_inspector', enabled: true }])
    const wrapper = await loadView()
    const labels = wrapper.findAll('[role="tab"]').map((tab) => tab.text().trim())
    expect(labels).toContain('健康评分')
  })

  it('keeps approval panel visible when health module is enabled but not selected', async () => {
    listModulesMock.mockResolvedValue([{ key: 'session_inspector', enabled: true }])
    const wrapper = await loadView()
    expect(wrapper.find('[data-stub="approval"]').exists()).toBe(true)
    expect(wrapper.find('[data-stub="compression"]').exists()).toBe(false)
  })

  it('switches to compression panel when its tab is clicked', async () => {
    const wrapper = await loadView()
    const tabs = wrapper.findAll('[role="tab"]')
    const compressionTab = tabs.find((tab) => tab.text().trim() === '压缩策略')!
    await compressionTab.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-stub="compression"]').exists()).toBe(true)
    expect(wrapper.find('[data-stub="approval"]').exists()).toBe(false)
  })

  it('falls back to approval tab when health is disabled and user lands on health', async () => {
    listModulesMock.mockResolvedValue([{ key: 'session_inspector', enabled: false }])
    const wrapper = await loadView()
    expect(wrapper.find('[data-stub="approval"]').exists()).toBe(true)
  })
})