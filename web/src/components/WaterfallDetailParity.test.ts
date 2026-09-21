import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import type { WaterfallRequest } from '../api/dispatch'
import DispatchWaterfallDetail from './DispatchWaterfallDetail.vue'
import RequestWaterfallPanel from './detail/RequestWaterfallPanel.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { credentialFallback: '凭据' },
      requestDetail: {
        waterfall: {
          loading: '加载调度瀑布…',
          stageHeader: 'T0–T9 阶段',
          cols: { stage: '阶段', duration: '耗时', source: '来源' },
          syntheticTag: '合成',
          measuredTag: '实测',
          empty: '暂无瀑布时间线',
          noAttempts: '无 Attempts 记录。',
        },
      },
    },
  },
})

const request: WaterfallRequest = {
  request_id: 'req-parity',
  model: 'model-a',
  result: 'success',
  waiting_in_total_ms: 10,
  waiting_in_model_ms: 2,
  waiting_in_node_ms: 3,
  routing_ms: 4,
  acquire_ms: 5,
  upstream_latency_ms: 50,
  streaming_duration_ms: 100,
  queue_wait_ms: 10,
  total_ms: 160,
  arrived_at: '2026-09-21T10:00:00Z',
  attempts: [{
    attempt_id: 'attempt-1',
    attempt_no: 1,
    credential_id: 5,
    model: 'model-a',
    outcome: 'success',
  }],
}

describe('Waterfall detail parity', () => {
  it('renders one shared request-detail body in the drawer and inline request-detail tab', () => {
    const drawer = mount(DispatchWaterfallDetail, {
      props: { selected: request },
      global: { plugins: [i18n], stubs: { Teleport: true } },
    })
    const inline = mount(RequestWaterfallPanel, {
      props: { selected: request },
      global: { plugins: [i18n] },
    })

    const drawerBody = drawer.get('[data-testid="waterfall-request-detail"]')
    const inlineBody = inline.get('[data-testid="waterfall-request-detail"]')
    expect(inlineBody.text()).toBe(drawerBody.text())
    expect(inlineBody.findAll('[data-testid="waterfall-stage-table"] tbody tr')).toHaveLength(
      drawerBody.findAll('[data-testid="waterfall-stage-table"] tbody tr').length,
    )
    expect(inlineBody.findAll('[data-testid="waterfall-attempts"] li')).toHaveLength(
      drawerBody.findAll('[data-testid="waterfall-attempts"] li').length,
    )
  })
})
