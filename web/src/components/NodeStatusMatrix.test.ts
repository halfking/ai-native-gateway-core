import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import NodeStatusMatrix from './NodeStatusMatrix.vue'
import { liveStreamState } from '../composables/liveStreamStore'

describe('NodeStatusMatrix', () => {
  beforeEach(() => {
    liveStreamState.nodes = [{
      credential_id: 11,
      provider_id: 7,
      provider_code: 'anthropic',
      circuit_state: 'open',
      availability_state: 'ready',
      quota_state: 'ok',
      health_status: 'unreachable',
      manual_disabled: false,
    }]
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) }))
    vi.stubGlobal('confirm', vi.fn(() => true))
  })

  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('groups unhealthy nodes and opens the unified detail drawer', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body, global: { stubs: { Teleport: true } } })
    expect(wrapper.text()).toContain('异常节点 (1)')

    await wrapper.find('.nm-card').trigger('click')
    expect(wrapper.find('.nd-drawer').exists()).toBe(true)
    expect(wrapper.text()).toContain('anthropic')
  })

  it('opens unified maintenance controls for a selected node', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body, global: { stubs: { Teleport: true } } })
    await wrapper.find('.nm-card').trigger('click')
    await wrapper.get('.nd-tabs button:last-child').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('设置与维护')
    expect(wrapper.text()).toContain('强制启用')
    expect(wrapper.text()).toContain('强制禁用')
  })
})
