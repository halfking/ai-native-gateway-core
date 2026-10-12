import { afterEach, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import MobileModelPicker from './MobileModelPicker.vue'
import { setLocale, t } from '@/i18n'
vi.mock('@/api/models', () => ({ fetchAvailableModels: vi.fn() }))
afterEach(() => setLocale('zh-CN'))
it('default trigger and sheet titles update when the language changes', async () => {
  setLocale('zh-CN')
  const wrapper = mount(MobileModelPicker, { props: { modelValue: '' }, global: { stubs: { AppSheet: { props: ['title'], template: '<div>{{ title }}<slot /></div>' } } } })
  expect(wrapper.text()).toContain(t('modelPicker.title'))
  setLocale('en-US'); await nextTick()
  expect(wrapper.find('.model-picker-trigger').text()).toBe(t('modelPicker.trigger'))
  expect(wrapper.text()).toContain(t('modelPicker.title'))
  wrapper.unmount()
})
