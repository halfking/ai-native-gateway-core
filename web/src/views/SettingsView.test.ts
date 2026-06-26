import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SettingsView from './SettingsView.vue'

const listSettingsMock = vi.fn()
const getSettingMock = vi.fn()
const updateSettingMock = vi.fn()
const rollbackSettingMock = vi.fn()

vi.mock('../api', () => ({
  listSettings: (...args: any[]) => listSettingsMock(...args),
  getSetting: (...args: any[]) => getSettingMock(...args),
  updateSetting: (...args: any[]) => updateSettingMock(...args),
  rollbackSetting: (...args: any[]) => rollbackSettingMock(...args),
}))

describe('SettingsView session alias editor', () => {
  beforeEach(() => {
    listSettingsMock.mockReset()
    getSettingMock.mockReset()
    updateSettingMock.mockReset()
    rollbackSettingMock.mockReset()

    listSettingsMock.mockResolvedValue({
      items: [{
        key: 'session.id_body_keys',
        env_name: 'LLM_GATEWAY_SESSION_ID_BODY_KEYS',
        type: 'string',
        scope: 'platform',
        category: 'session',
        default: '',
        value: 'workspaceId,room_session_key',
        source: 'db',
        description: '请求体会话别名键',
        danger_level: 1,
        hot_reload: true,
      }],
    })
    getSettingMock.mockResolvedValue({
      spec: {
        key: 'session.id_body_keys',
        env_name: 'LLM_GATEWAY_SESSION_ID_BODY_KEYS',
        type: 'string',
        scope: 'platform',
        category: 'session',
        default: '',
        description: '请求体会话别名键',
        danger_level: 1,
        hot_reload: true,
      },
      value: 'workspaceId,room_session_key',
      source: 'db',
    })
    updateSettingMock.mockResolvedValue({ status: 'ok' })
  })

  it('adds, removes, and saves alias tags as a comma string', async () => {
    const wrapper = mount(SettingsView)
    await flushPromises()

    const row = wrapper.find('tbody tr')
    expect(row.exists()).toBe(true)
    await row.trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.tag-chip')).toHaveLength(2)

    const input = wrapper.find('.tag-input')
    expect(input.exists()).toBe(true)
    await input.setValue('chatRoomId')
    await input.trigger('keydown', { key: 'Enter' })
    await flushPromises()

    expect(wrapper.findAll('.tag-chip')).toHaveLength(3)
    expect(wrapper.text()).toContain('示例请求预览')
    expect(wrapper.text()).toContain('chatRoomId')

    await wrapper.findAll('.tag-chip-remove')[0].trigger('click')
    await flushPromises()

    const chips = wrapper.findAll('.tag-chip').map(node => node.text())
    expect(chips.some(text => text.includes('workspaceId'))).toBe(false)
    expect(chips.some(text => text.includes('room_session_key'))).toBe(true)
    expect(chips.some(text => text.includes('chatRoomId'))).toBe(true)

    await wrapper.find('.btn.btn-primary').trigger('click')
    await flushPromises()

    expect(updateSettingMock).toHaveBeenCalledWith('session.id_body_keys', {
      value: 'room_session_key,chatRoomId',
    })
  })
})
