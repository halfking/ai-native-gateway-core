import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'
import QueuePerspectivePanel from './QueuePerspectivePanel.vue'
import { liveStreamState } from '../composables/liveStreamStore'

describe('QueuePerspectivePanel', () => {
  beforeEach(() => {
    liveStreamState.queue = {
      enabled: true,
      wired: true,
      models: [],
      credentials: [],
    }
    liveStreamState.requests = []
  })

  it('shows an idle queue instead of reporting that data is not wired', () => {
    const wrapper = mount(QueuePerspectivePanel)

    expect(wrapper.text()).toContain('当前无排队请求')
    expect(wrapper.text()).not.toContain('队列数据未接入')
  })

  it('shows the latest request processing path', () => {
    liveStreamState.requests = [{
      ts: '2026-08-14T08:00:00Z',
      request_id: 'req-12345678',
      model: 'claude-sonnet',
      provider_code: 'anthropic',
      status: 'success',
      latency_ms: 820,
    }]

    const wrapper = mount(QueuePerspectivePanel)

    expect(wrapper.text()).toContain('最近请求处理轨迹')
    expect(wrapper.text()).toContain('claude-sonnet')
    expect(wrapper.text()).toContain('anthropic')
    expect(wrapper.text()).toContain('820ms')
  })
})
