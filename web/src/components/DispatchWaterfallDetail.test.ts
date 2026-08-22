import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DispatchWaterfallDetail from './DispatchWaterfallDetail.vue'
import type { WaterfallRequest } from '../api/dispatch'

function req(partial: Partial<WaterfallRequest> = {}): WaterfallRequest {
  return {
    request_id: 'req-abc',
    result: 'success',
    session_id: 'gw_sess_1',
    waiting_in_total_ms: 10,
    waiting_in_model_ms: 0,
    waiting_in_node_ms: 0,
    routing_ms: 0,
    acquire_ms: 0,
    upstream_latency_ms: 50,
    streaming_duration_ms: 100,
    queue_wait_ms: 10,
    total_ms: 160,
    arrived_at: '2026-08-21T10:00:00.000Z',
    ...partial,
  }
}

describe('DispatchWaterfallDetail', () => {
  it('teleports drawer to body for right-side overlay', () => {
    mount(DispatchWaterfallDetail, {
      props: { selected: req() },
      attachTo: document.body,
      global: { stubs: { Teleport: true } },
    })
    expect(document.body.innerHTML).toContain('drawer-backdrop')
    expect(document.body.innerHTML).toContain('T0–T9 阶段')
  })

  it('emits open-session when session button is clicked', async () => {
    const wrapper = mount(DispatchWaterfallDetail, {
      props: { selected: req() },
      global: { stubs: { Teleport: true } },
    })
    const openBtn = wrapper.findAll('button').find(b => b.text().includes('打开会话'))
    expect(openBtn).toBeTruthy()
    await openBtn!.trigger('click')
    expect(wrapper.emitted('open-session')).toHaveLength(1)
  })
})
