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

  it('groups unhealthy nodes and opens their detail drawer', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body })
    expect(wrapper.text()).toContain('异常节点 (1)')

    await wrapper.find('.nm-card').trigger('click')
    expect(document.querySelector('.nm-drawer')).not.toBeNull()
    expect(document.body.textContent).toContain('anthropic')
  })

  it('sends the confirmation contract when disabling a node', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body })
    await wrapper.find('.nm-card').trigger('click')
    const toggleButton = document.querySelector<HTMLButtonElement>('.nm-btn--danger')
    expect(toggleButton).not.toBeNull()
    toggleButton?.click()
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/admin/providers/7/enable', expect.objectContaining({
      method: 'PATCH',
      headers: expect.objectContaining({
        'X-Confirm': 'yes',
        'Idempotency-Key': expect.stringContaining('node-toggle-7-false-'),
        'X-Correlation-ID': expect.stringContaining('node-toggle-7-false-'),
      }),
      body: JSON.stringify({ enabled: false, reason: 'Disabled from node status matrix' }),
    }))
  })
})
