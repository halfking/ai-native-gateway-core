import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it, vi } from 'vitest'
import TierGroupList from './TierGroupList.vue'
import routingDefault from '../../locales/en-US/routingDefault'

const i18n = createI18n({
  legacy: false,
  locale: 'en-US',
  messages: { 'en-US': { routingDefault } },
})

const ModelPickerStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<input data-test="model-picker" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
}

function mountList(add = vi.fn().mockResolvedValue(undefined)) {
  return mount(TierGroupList, {
    props: {
      taskType: 'code_generation',
      rows: [{
        id: 10,
        task_type: 'code_generation',
        profile: 'smart',
        tier: 'primary',
        canonical_model: 'existing-model',
        tenant_id: null,
        priority: 100,
        reason: '',
        created_at: '2026-08-13T00:00:00Z',
        updated_at: '2026-08-13T00:00:00Z',
      }],
      add,
    },
    global: {
      plugins: [i18n],
      stubs: { ModelPicker: ModelPickerStub },
    },
  })
}

describe('TierGroupList add model', () => {
  it('blocks a duplicate platform default before calling the API callback', async () => {
    const add = vi.fn().mockResolvedValue(undefined)
    const wrapper = mountList(add)

    await wrapper.findAll('button').find((button) => button.text().includes('Add model'))!.trigger('click')
    await wrapper.find('[data-test="model-picker"]').setValue('new-model')
    await wrapper.find('.btn-primary').trigger('click')

    expect(add).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('This configuration already exists (current model: existing-model).')
    expect(wrapper.find('.add-panel').exists()).toBe(true)
  })

  it('keeps the form open and surfaces an asynchronous creation failure', async () => {
    const add = vi.fn().mockRejectedValue(new Error('duplicate from server'))
    const wrapper = mountList(add)

    await wrapper.findAll('button').find((button) => button.text().includes('Add model'))!.trigger('click')
    await wrapper.find('select').setValue('speed_first')
    await wrapper.find('[data-test="model-picker"]').setValue('new-model')
    await wrapper.find('.btn-primary').trigger('click')
    await flushPromises()

    expect(add).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('duplicate from server')
    expect(wrapper.find('.add-panel').exists()).toBe(true)
  })
})
