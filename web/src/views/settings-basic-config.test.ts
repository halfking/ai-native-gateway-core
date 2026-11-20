import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import SettingsView from './SettingsView.vue'
import settingsMessages from '../locales/zh-CN/settings'
import commonMessages from '../locales/zh-CN/common'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      settings: settingsMessages,
      common: commonMessages,
    },
  },
  missingWarn: false,
  fallbackWarn: false,
})

const listSettingsMock = vi.fn()
const getSettingMock = vi.fn()
const updateSettingMock = vi.fn()
const rollbackSettingMock = vi.fn()
const getBasicConfigMock = vi.fn()

vi.mock('../api', () => ({
  listSettings: (...args: any[]) => listSettingsMock(...args),
  getSetting: (...args: any[]) => getSettingMock(...args),
  updateSetting: (...args: any[]) => updateSettingMock(...args),
  rollbackSetting: (...args: any[]) => rollbackSettingMock(...args),
  getBasicConfig: (...args: any[]) => getBasicConfigMock(...args),
}))

// basic_config_view_test.ts — 基础设置轮（2026-11-20）前端行为钉测：
// ① 搜索过滤（键名/描述/分类）；② 基础设置面板分组渲染 + 保存走既有通道；
// ③ 只读基础设施区展示（修改需重启语义）。
describe('SettingsView search + basic config panel', { timeout: 20_000 }, () => {
  beforeEach(() => {
    listSettingsMock.mockReset()
    getSettingMock.mockReset()
    updateSettingMock.mockReset()
    rollbackSettingMock.mockReset()
    getBasicConfigMock.mockReset()

    listSettingsMock.mockResolvedValue({
      items: [
        {
          key: 'compression.mode',
          env_name: 'LLM_GATEWAY_COMPRESSION_MODE',
          type: 'enum',
          scope: 'platform',
          category: 'compression',
          default: '2',
          value: '2',
          source: 'default',
          options: ['0', '1', '2'],
          description: '压缩模式',
          danger_level: 1,
          hot_reload: true,
        },
        {
          key: 'system.timezone',
          env_name: 'LLM_GATEWAY_SYSTEM_TIMEZONE',
          type: 'string',
          scope: 'platform',
          category: 'general',
          default: 'Asia/Shanghai',
          value: 'Asia/Shanghai',
          source: 'db',
          description: '系统时区（时间范围/日切口径）',
          danger_level: 0,
          hot_reload: true,
        },
      ],
    })
    getSettingMock.mockResolvedValue({
      spec: { key: 'compression.mode', type: 'enum', scope: 'platform', category: 'compression', default: '2', description: '压缩模式', danger_level: 1, hot_reload: true, env_name: 'X' },
      value: '2',
      source: 'default',
    })
    updateSettingMock.mockResolvedValue({ status: 'ok' })
    getBasicConfigMock.mockResolvedValue({
      groups: [
        {
          id: 'identity',
          items: [
            {
              key: 'gateway.display_name', group: 'identity', type: 'string', editable: true,
              default: '', value: '', source: 'default', hot_reload: true, danger_level: 0,
              description: '网关个性名称（对外展示名）',
            },
            {
              key: 'system.skin', group: 'identity', type: 'enum', editable: true,
              options: ['auto', 'light', 'dark'], default: 'auto', value: 'auto', source: 'default',
              hot_reload: true, danger_level: 0, description: '默认皮肤（未个性化用户的初始主题）',
            },
          ],
        },
        {
          id: 'storage',
          items: [{
            key: 'storage.attachment_public_base_url', group: 'storage', type: 'string', editable: true,
            default: '/api/attachments', value: '/api/attachments', source: 'default',
            hot_reload: true, danger_level: 1, description: '附件公开访问域名前缀',
          }],
        },
        {
          id: 'schedules',
          items: [{
            key: 'reports.daily_rollup.hour', group: 'schedules', type: 'int', editable: true,
            default: 2, value: 2, source: 'env', hot_reload: true, danger_level: 1,
            description: '对帐报表每日聚合时间（UTC 小时）',
          }],
        },
      ],
      infra: {
        instance_id: 'gw-211',
        redis: { configured: true, addr: '127.0.0.1:6379', db: '0' },
        database: { configured: true, source: 'env:DATABASE_URL', host: 'db:5432', database: 'llmgw', user: 'gw' },
        attachment_dir_env: '',
        cache_dir_env: '',
        reload_hint: 'startup_env_readonly_restart_required',
      },
    })
  })

  it('filters list by key and description via search box', async () => {
    const wrapper = mount(SettingsView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)

    await wrapper.find('.search-input').setValue('compression')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('compression.mode')

    // 描述也能命中（时区键的描述含「日切」）
    await wrapper.find('.search-input').setValue('日切')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('system.timezone')

    // 无命中显示空态
    await wrapper.find('.search-input').setValue('zzz-no-match')
    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
    expect(wrapper.text()).toContain('该类别暂无设置')

    // 清空恢复
    await wrapper.find('.search-clear').trigger('click')
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
  })

  it('renders basic config groups, saves via updateSetting, shows infra read-only', async () => {
    const wrapper = mount(SettingsView, { global: { plugins: [i18n] } })
    await flushPromises()

    // 面板初始不可见
    expect(wrapper.find('.basic-config-panel').exists()).toBe(false)
    await wrapper.find('.basic-toggle').trigger('click')
    await flushPromises()
    expect(getBasicConfigMock).toHaveBeenCalledTimes(1)

    // 三个分组卡都渲染
    expect(wrapper.text()).toContain('标识与外观')
    expect(wrapper.text()).toContain('存储')
    expect(wrapper.text()).toContain('周期任务')

    // infra 只读区 + 实例 ID + Redis 实况
    expect(wrapper.text()).toContain('基础设施（只读）')
    expect(wrapper.text()).toContain('gw-211')
    expect(wrapper.text()).toContain('127.0.0.1:6379')
    expect(wrapper.text()).toContain('db:5432/llmgw')

    // 修改个性名称并保存 → 走既有 updateSetting 通道
    const nameInput = wrapper.findAll('.basic-entry input[type="text"]')[0]
    await nameInput.setValue('问学灵果网关')
    const saveBtns = wrapper.findAll('.basic-save-btn')
    // display_name 是 identity 组第一个保存按钮
    await saveBtns[0].trigger('click')
    await flushPromises()

    expect(updateSettingMock).toHaveBeenCalledWith('gateway.display_name', { value: '问学灵果网关' })
    // 保存后面板与列表都刷新
    expect(getBasicConfigMock).toHaveBeenCalledTimes(2)
    expect(listSettingsMock).toHaveBeenCalledTimes(2)
  })

  it('integer entry parses number before save (rollup hour)', async () => {
    const wrapper = mount(SettingsView, { global: { plugins: [i18n] } })
    await flushPromises()
    await wrapper.find('.basic-toggle').trigger('click')
    await flushPromises()

    const numInput = wrapper.find('.basic-entry input[type="number"]')
    expect(numInput.exists()).toBe(true)
    await numInput.setValue('3')
    const saveBtns = wrapper.findAll('.basic-save-btn')
    // schedules 组的保存按钮是最后一个
    await saveBtns[saveBtns.length - 1].trigger('click')
    await flushPromises()

    expect(updateSettingMock).toHaveBeenCalledWith('reports.daily_rollup.hour', { value: 3 })
  })
})
