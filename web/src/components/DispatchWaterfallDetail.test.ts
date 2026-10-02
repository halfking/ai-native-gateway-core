import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import DispatchWaterfallDetail from './DispatchWaterfallDetail.vue'
import type { WaterfallRequest } from '../api/dispatch'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { credentialFallback: '凭据' },
      requestDetail: {
        waterfall: {
          source: '数据源：{src}',
          stageHeader: 'T0–T9 阶段',
          cols: { stage: '阶段', duration: '耗时', source: '来源' },
          syntheticTag: '合成',
          measuredTag: '实测',
          noAttempts: '无 Attempts 记录。',
        },
      },
    },
  },
})

const mountOptions = { global: { plugins: [i18n], stubs: { Teleport: true } } }

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
      ...mountOptions,
    })
    expect(document.body.innerHTML).toContain('drawer-backdrop')
    expect(document.body.innerHTML).toContain('T0–T9 阶段')
  })

  it('emits open-session when session button is clicked', async () => {
    const wrapper = mount(DispatchWaterfallDetail, {
      props: { selected: req() },
      ...mountOptions,
    })
    const openBtn = wrapper.findAll('button').find(b => b.text().includes('打开会话'))
    expect(openBtn).toBeTruthy()
    await openBtn!.trigger('click')
    expect(wrapper.emitted('open-session')).toHaveLength(1)
  })

  it('uses the shared waterfall detail body and emits open-fullscreen', async () => {
    const wrapper = mount(DispatchWaterfallDetail, {
      props: { selected: req({ model: 'model-a' }) },
      ...mountOptions,
    })

    expect(wrapper.find('[data-testid="waterfall-request-detail"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="waterfall-request-summary"]').text()).toContain('req-abc')
    const fullscreenBtn = wrapper.findAll('button').find(b => b.text().includes('全屏详情'))
    expect(fullscreenBtn).toBeTruthy()
    await fullscreenBtn!.trigger('click')
    expect(wrapper.emitted('open-fullscreen')).toHaveLength(1)
  })
})
