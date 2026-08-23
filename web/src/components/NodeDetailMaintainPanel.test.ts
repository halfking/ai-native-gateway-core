import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import NodeDetailMaintainPanel from './NodeDetailMaintainPanel.vue'
import type { RoutingCandidate } from '../api/routing'

// Priority flag vs sort order contract: the settings form must expose BOTH
// concepts with distinct labels — manual_priority stays "排序序号" (dense
// sort rank) while the boolean flag is "优先凭据" (quota-first routing).
const candidate = {
  credential_id: 7,
  model_name: 'minimax-m3',
  manual_priority: 12,
  priority: true,
  tier: 2,
  weight: 100,
  lifecycle_status: 'active',
} as unknown as RoutingCandidate

function mountPanel(props: Partial<InstanceType<typeof NodeDetailMaintainPanel>['$props']> = {}) {
  return mount(NodeDetailMaintainPanel, {
    props: {
      canEdit: true,
      saving: false,
      coreLoading: false,
      selectedModel: 'minimax-m3',
      pingResult: null,
      candidate,
      candidateLoading: false,
      lifecycle: 'active',
      manualPriority: 12,
      priorityFlag: true,
      routingTier: 2,
      weight: 100,
      modelActionReason: '',
      selectedModelStatus: null,
      ...props,
    },
  })
}

describe('NodeDetailMaintainPanel priority flag', () => {
  it('renders both the sort-order number input and the priority-credential toggle with distinct labels', () => {
    const wrapper = mountPanel()
    const text = wrapper.text()
    expect(text).toContain('排序序号')
    expect(text).toContain('优先凭据')
    // The distinction hint must be visible so operators don't conflate the two.
    expect(text).toContain('与排序序号无关')
  })

  it('reflects the priority flag state and emits update on toggle', async () => {
    const wrapper = mountPanel()
    const toggle = wrapper.find('input[type="checkbox"]')
    expect((toggle.element as HTMLInputElement).checked).toBe(true)

    await toggle.setValue(false)
    expect(wrapper.emitted('update:priorityFlag')).toEqual([[false]])
  })

  it('keeps the toggle disabled when editing is not allowed', () => {
    const wrapper = mountPanel({ canEdit: false })
    const toggle = wrapper.find('input[type="checkbox"]')
    expect((toggle.element as HTMLInputElement).disabled).toBe(true)
  })
})
