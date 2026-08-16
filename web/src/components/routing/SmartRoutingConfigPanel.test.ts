import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SmartRoutingConfigPanel from './SmartRoutingConfigPanel.vue'

const mocks = vi.hoisted(() => ({
  getWorkType: vi.fn(),
  listWorkTypes: vi.fn(),
  putWorkTypeRoutes: vi.fn(),
  refreshWorkTypes: vi.fn().mockResolvedValue(undefined),
}))

const { getWorkType, listWorkTypes, putWorkTypeRoutes, refreshWorkTypes } = mocks

vi.mock('../../api-work-types', () => ({
  getWorkType: mocks.getWorkType,
  listWorkTypes: mocks.listWorkTypes,
  putWorkTypeRoutes: mocks.putWorkTypeRoutes,
  groupRoutesByLayer(routes: any[] = []) {
    const groups = { primary: [], secondary: [], fallback: [] } as Record<string, any[]>
    for (const route of routes) groups[route.tier]?.push(route)
    return groups
  },
}))

vi.mock('../../composables/useWorkTypes', () => ({
  useWorkTypes: () => ({ refreshWorkTypes: mocks.refreshWorkTypes }),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'en-US',
  messages: {
    'en-US': {
      routingDefault: {
        title: 'Smart routing', subtitle: 'Configure routes', rail: { all: 'All' },
        actions: { refresh: 'Refresh' }, empty: { none: 'No routes' },
      },
      workTypes: {
        layers: {
          primary: 'Primary', secondary: 'Secondary', fallback: 'Fallback',
          emptyPrimary: 'No primary', emptySecondary: 'No secondary', emptyFallback: 'No fallback',
        },
        detail: { selectModelPlaceholder: 'Select model', routeEnabled: 'Enabled' },
        saving: 'Saving', saveBtn: 'Save routes', savedOk: 'Saved',
      },
    },
  },
})

const ModelPickerStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<input class="model-picker" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
}

function route(tier: 'primary' | 'secondary' | 'fallback', canonical_name: string) {
  return { canonical_name, tier, weight: 1, min_score: 0, enabled: true, task_quality_score: 0 }
}

describe('SmartRoutingConfigPanel', () => {
  beforeEach(() => {
    getWorkType.mockReset()
    listWorkTypes.mockReset()
    putWorkTypeRoutes.mockReset()
    refreshWorkTypes.mockClear()
  })

  it('persists multiple fallback models through the work-type route API', async () => {
    listWorkTypes.mockResolvedValue([{ key: 'code_review', label: 'Code review', enabled: true }])
    getWorkType.mockResolvedValue({
      key: 'code_review', label: 'Code review', enabled: true,
      model_routes: [route('primary', 'primary-model'), route('fallback', 'fallback-a'), route('fallback', 'fallback-b')],
    })
    putWorkTypeRoutes.mockImplementation(async (_key, routes) => ({ key: 'code_review', model_routes: routes }))

    const wrapper = mount(SmartRoutingConfigPanel, {
      global: { plugins: [i18n], stubs: { ModelPicker: ModelPickerStub } },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('Fallback 2/5')
    await wrapper.find('.btn-primary').trigger('click')
    await flushPromises()

    expect(putWorkTypeRoutes).toHaveBeenCalledWith('code_review', expect.arrayContaining([
      expect.objectContaining({ canonical_name: 'primary-model', tier: 'primary', weight: 1 }),
      expect.objectContaining({ canonical_name: 'fallback-a', tier: 'fallback', weight: 2 }),
      expect.objectContaining({ canonical_name: 'fallback-b', tier: 'fallback', weight: 1 }),
    ]))
    expect(refreshWorkTypes).toHaveBeenCalledOnce()
  })

  it('does not request a detail when there are no enabled work types', async () => {
    listWorkTypes.mockResolvedValue([])

    mount(SmartRoutingConfigPanel, {
      global: { plugins: [i18n], stubs: { ModelPicker: ModelPickerStub } },
    })
    await flushPromises()

    expect(getWorkType).not.toHaveBeenCalled()
  })
})
