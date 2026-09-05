import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import NodeDetailMaintainPanel from './NodeDetailMaintainPanel.vue'
import zhCNProviders from '../locales/zh-CN/providers'
import type { RoutingCandidate } from '../api/routing'
import type { CredentialLifecycleStatus } from '../api/providers'

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

// vue-i18n v9 normalizes 'zh-CN' to 'zh' internally, so messages live under
// the 'zh' key (same pattern as NodeStatusMatrix.test.ts).
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { zh: { providers: zhCNProviders } },
})

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
      lifecycle: 'active' as CredentialLifecycleStatus,
      manualDisabled: false,
      manualStateDetail: '',
      manualPriority: 12,
      priorityFlag: true,
      routingTier: 2,
      weight: 100,
      modelActionReason: '',
      selectedModelStatus: null,
      ...props,
    },
    global: { plugins: [i18n] },
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

describe('NodeDetailMaintainPanel lifecycle state chips', () => {
  it('renders every lifecycle state as a clickable chip with the current one marked', () => {
    const wrapper = mountPanel({ lifecycle: 'active' })
    const chips = wrapper.findAll('.nd-lc-chip')
    expect(chips.map(c => c.text())).toEqual([
      expect.stringContaining('在用'),
      expect.stringContaining('停用'),
      expect.stringContaining('暂停'),
      expect.stringContaining('退役'),
    ])
    // No native select anymore — the lifecycle must be a direct-manipulation chip group.
    expect(wrapper.find('select').exists()).toBe(false)
    const current = chips.find(c => c.classes().includes('is-current'))
    expect(current).toBeDefined()
    expect(current!.text()).toContain('在用')
    expect(current!.attributes('aria-checked')).toBe('true')
  })

  it('emits lifecycleChange with the target state on click', async () => {
    const wrapper = mountPanel({ lifecycle: 'active' })
    const chips = wrapper.findAll('.nd-lc-chip')
    await chips[2]!.trigger('click')
    expect(wrapper.emitted('lifecycleChange')).toEqual([['suspended']])
  })

  it('disables the chips for read-only viewers', () => {
    const wrapper = mountPanel({ canEdit: false })
    for (const chip of wrapper.findAll('.nd-lc-chip')) {
      expect(chip.attributes('disabled')).toBeDefined()
    }
  })
})

describe('NodeDetailMaintainPanel manual disabled state', () => {
  it('shows the normal-routing badge and a disable action when not disabled', () => {
    const wrapper = mountPanel({ manualDisabled: false })
    const badge = wrapper.find('[data-testid="manual-disabled-badge"]')
    expect(badge.text()).toContain('正常参与路由')
    const action = wrapper.findAll('button').find(b => b.text().includes('人工禁用'))
    expect(action).toBeDefined()
    expect(wrapper.emitted('setCredentialDisabled')).toBeUndefined()
  })

  it('shows the disabled badge and an unban action when manually disabled', async () => {
    const wrapper = mountPanel({ manualDisabled: true, manualStateDetail: '运营商欠费' })
    const badge = wrapper.find('[data-testid="manual-disabled-badge"]')
    expect(badge.text()).toContain('已人工禁用')
    expect(wrapper.text()).toContain('运营商欠费')
    const action = wrapper.findAll('button').find(b => b.text().includes('解禁'))
    expect(action).toBeDefined()
    await action!.trigger('click')
    expect(wrapper.emitted('setCredentialDisabled')).toEqual([[false]])
  })
})
