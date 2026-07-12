import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const getSettingMock = vi.fn()
const getTenantSettingMock = vi.fn()
const updateSettingMock = vi.fn()
const updateTenantSettingMock = vi.fn()
const getAvailableModelsMock = vi.fn()
const getCompressionStatsMock = vi.fn()

const messages = {
  'zh-CN': {
    common: { save: '保存', saving: '保存中' },
    sessions: {
      config: {
        title: '会话配置',
        subtitle: '配置',
        approvalTab: '审批',
        compressionTab: '压缩',
        healthTab: '健康',
        tabsLabel: '配置分区',
        platformScope: '平台',
        tenantScope: '租户',
        saving: '保存中',
        loadError: '加载失败',
        saveError: '保存失败',
        saveSuccess: '保存成功',
        statsLoadError: '统计失败',
        statsViewFull: '查看完整统计',
        compressionSection: '压缩',
        compressionSectionHint: '',
        handoffSection: '交接',
        handoffSectionHint: '',
        summaryEngineLabel: '总结引擎',
        summaryEngineHint: '',
        summaryModelLabel: '总结模型',
        summaryModelHint: '',
        summaryModelFallback: '跟随自动路由',
        summaryKeepLabel: '保留',
        summaryKeepHint: '',
        modelContextUnknown: '上下文未知',
        modelVendorUnknown: '未知厂商',
        modelNone: '尚未选择',
        removeModel: '移除',
        compressionEnabledLabel: '',
        compressionEnabledHint: '',
        compressionModeLabel: '',
        compressionModeHint: '',
        compressionWindowLabel: '',
        compressionWindowHint: '',
        compressionModelLabel: '',
        compressionModelHint: '',
        handoffEnabledLabel: '',
        handoffEnabledHint: '',
        handoffThresholdLabel: '',
        handoffThresholdHint: '',
        revertToDefault: '恢复',
        statsTitle: '统计',
        statsTotalRequests: '总',
        statsCompressed: '已压缩',
        statsRate: '率',
        statsSaved: '省',
        loading: '加载中',
      },
    },
  },
}

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

vi.mock('../store', () => ({
  getCurrentTenantId: () => 'tenant-a',
  store: { userInfo: { id: 1, tenant_id: 'tenant-a' } },
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages,
})

async function load() {
  const { default: CompressionConfigPanel } = await import('./CompressionConfigPanel.vue')
  const wrapper = mount(CompressionConfigPanel, { global: { plugins: [i18n] } })
  await flushPromises()
  return wrapper
}

describe('CompressionConfigPanel', () => {
  beforeEach(() => {
    getSettingMock.mockReset()
    getTenantSettingMock.mockReset()
    updateSettingMock.mockReset()
    updateTenantSettingMock.mockReset()
    getAvailableModelsMock.mockReset()
    getCompressionStatsMock.mockReset()

    const platform: Record<string, any> = {
      'compression.enabled': { value: true, default: true, source: 'default', spec: { default: true } },
      'compression.mode': { value: '0', default: 'smart', source: 'db', spec: { default: 'smart', options: ['off', 'auto_threshold', 'on_4xx', 'smart', 'aggressive'] } },
      'compression.window_fraction': { value: 0.95, default: 0.8, source: 'db', spec: { default: 0.8, min: 0, max: 1 } },
      'compression.llm_model': { value: 'minimax-text-01,gemini-2.5-flash,minimax-text-01', default: 'minimax-text-01,gemini-2.5-flash', source: 'default', spec: { default: '' } },
      'handoff.enabled': { value: true, default: true, source: 'default', spec: { default: true } },
      'handoff.threshold': { value: 0.8, default: 0.8, source: 'env', spec: { default: 0.8, min: 0, max: 1 } },
    }
    const tenant: Record<string, any> = {
      'handoff.summary_engine': { value: 'llm', default: 'llm', source: 'default', spec: { default: 'llm', options: ['llm', 'rule', 'hybrid'] } },
      'handoff.summary_model': { value: '', default: '', source: 'default', spec: { default: '' } },
      'handoff.summary_keep_recent_n': { value: 4, default: 4, source: 'default', spec: { default: 4, min: 0, max: 50 } },
    }
    getSettingMock.mockImplementation((key: string) => Promise.resolve(platform[key]))
    getTenantSettingMock.mockImplementation((_tid: string, key: string) => Promise.resolve(tenant[key]))
    updateSettingMock.mockResolvedValue({ status: 'ok', new_value: null })
    updateTenantSettingMock.mockResolvedValue({ status: 'ok', new_value: null })
    getAvailableModelsMock.mockResolvedValue({
      families: [{
        id: 'g', vendor: 'Demo', display_name: 'Demo',
        versions: [{ canonical_name: 'minimax-text-01', display_name: 'MiniMax Text', modality: 'chat', context_window: 1048576, parameters_b: null, aliases: ['demo:minimax-text-01'], raw_names: [], provider_count: 2, featured: true, tags: [] }],
      }],
      popular: [], unmapped: [], total_raw: 1,
    })
    getCompressionStatsMock.mockResolvedValue({ total_requests: 100, compressed_total: 30, compression_rate: 0.3, estimated_tokens_saved: 12000 })
  })

  it('normalizes legacy compression.mode values to supported options', async () => {
    getSettingMock.mockImplementation((key: string) => {
      if (key === 'compression.mode') return Promise.resolve({ value: '0', default: 'smart', source: 'db', spec: { default: 'smart', options: ['off', 'auto_threshold', 'on_4xx', 'smart', 'aggressive'] } })
      return Promise.resolve({ value: true, default: true, source: 'default', spec: { default: true } })
    })
    const wrapper = await load()
    expect((wrapper.find('#compression-mode').element as HTMLSelectElement).value).toBe('off')
  })

  it('clamps compression window to backend min/max range', async () => {
    getSettingMock.mockImplementation((key: string) => {
      if (key === 'compression.window_fraction') return Promise.resolve({ value: 2, default: 0.8, source: 'db', spec: { default: 0.8, min: 0, max: 1 } })
      return Promise.resolve({ value: true, default: true, source: 'default', spec: { default: true } })
    })
    const wrapper = await load()
    const range = wrapper.find<HTMLInputElement>('input[type=range]')
    expect(Number(range.element.value)).toBeLessThanOrEqual(1)
  })

  it('does not auto-save and requires explicit save button', async () => {
    const wrapper = await load()
    await wrapper.find('#compression-mode').setValue('aggressive')
    expect(updateSettingMock).not.toHaveBeenCalled()
    expect(updateTenantSettingMock).not.toHaveBeenCalled()
  })

  it('dispatches platform and tenant updates through save', async () => {
    const wrapper = await load()
    // Drive the draft through the script exposure rather than DOM (v-model bypass).
    const vm = wrapper.vm as unknown as { dirty: boolean; draft: Record<string, any>; save: () => Promise<void> }
    vm.draft['handoff.summary_engine'] = 'rule'
    vm.dirty = true
    await vm.save()
    expect(updateSettingMock).toHaveBeenCalled()
    expect(updateTenantSettingMock).toHaveBeenCalled()
  })

  it('renders summary model placeholder when summary model is empty', async () => {
    const wrapper = await load()
    const summaryInput = wrapper.find('#summary-model')
    expect((summaryInput.element as HTMLInputElement).placeholder).toBe('跟随自动路由')
  })
})