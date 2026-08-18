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

  it('renders only a lazy trigger by default and opens the panorama on demand', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body, global: { stubs: { Teleport: true } } })

    // 默认零渲染：只有触发按钮 + 健康摘要，矩阵分组不挂载
    const trigger = wrapper.get('.nm-trigger')
    expect(trigger.text()).toContain('节点状态矩阵')
    expect(trigger.text()).toContain('1 个节点')
    expect(trigger.text()).toContain('异常 1')
    expect(wrapper.find('.nm-group').exists()).toBe(false)

    await trigger.trigger('click')
    expect(wrapper.find('.nm-modal').exists()).toBe(true)
    expect(wrapper.text()).toContain('异常节点 (1)')

    // 关闭弹窗即销毁内容
    await wrapper.get('.nm-modal-close').trigger('click')
    expect(wrapper.find('.nm-modal').exists()).toBe(false)
  })

  it('groups unhealthy nodes and opens the unified detail drawer', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body, global: { stubs: { Teleport: true } } })
    await wrapper.get('.nm-trigger').trigger('click')
    expect(wrapper.text()).toContain('异常节点 (1)')

    await wrapper.find('.nm-card').trigger('click')
    expect(wrapper.find('.nd-drawer').exists()).toBe(true)
    expect(wrapper.text()).toContain('anthropic')
  })

  it('opens unified maintenance controls for a selected node', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body, global: { stubs: { Teleport: true } } })
    await wrapper.get('.nm-trigger').trigger('click')
    await wrapper.find('.nm-card').trigger('click')
    await wrapper.get('.nd-tabs button:last-child').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('设置与维护')
    expect(wrapper.text()).toContain('强制启用')
    expect(wrapper.text()).toContain('强制禁用')
    expect(wrapper.get('.nd-actions .btn-primary').attributes('disabled')).toBeDefined()
  })

  it('shows recent routing requests in a dedicated tab', async () => {
    const wrapper = mount(NodeStatusMatrix, { attachTo: document.body, global: { stubs: { Teleport: true } } })
    await wrapper.get('.nm-trigger').trigger('click')
    await wrapper.find('.nm-card').trigger('click')
    await flushPromises()

    // 明细 tab 不承载请求表（空态文案不出现）；独立 tab 承载
    expect(wrapper.text()).not.toContain('暂无路由请求记录')
    await wrapper.get('.nd-tabs button:nth-child(2)').trigger('click')

    expect(wrapper.text()).toContain('最近路由请求')
    expect(wrapper.text()).toContain('暂无路由请求记录')
    // 设置 tab 仍是最后一个
    expect(wrapper.get('.nd-tabs button:last-child').text()).toContain('设置与维护')
  })
})
