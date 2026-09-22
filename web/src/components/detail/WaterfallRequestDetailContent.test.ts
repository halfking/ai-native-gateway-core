import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import type { WaterfallAttempt, WaterfallRequest } from '../../api/dispatch'
import WaterfallRequestDetailContent from './WaterfallRequestDetailContent.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { credentialFallback: '凭据' },
      requestDetail: {
        waterfall: {
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

function request(partial: Partial<WaterfallRequest> = {}): WaterfallRequest {
  return {
    request_id: 'req-waterfall-1',
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
    ...partial,
  }
}

const selectedAttempt: WaterfallAttempt = {
  attempt_id: 'selected-attempt',
  attempt_no: 1,
  credential_id: 9,
  model: 'model-a',
  outcome: 'success',
}

const enrichedAttempt: WaterfallAttempt = {
  attempt_id: 'enriched-attempt',
  attempt_no: 2,
  credential_id: 10,
  model: 'model-b',
  outcome: 'failure',
  error_kind: 'timeout',
}

describe('WaterfallRequestDetailContent', () => {
  it('renders the shared selected-request body with summary, stages, and attempts', () => {
    const wrapper = mount(WaterfallRequestDetailContent, {
      global: { plugins: [i18n] },
      props: {
        request: request({ attempts: [selectedAttempt] }),
        showRequestSummary: true,
      },
    })

    expect(wrapper.find('[data-testid="waterfall-request-detail"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="waterfall-request-summary"]').text()).toContain('req-waterfall-1')
    expect(wrapper.get('[data-testid="waterfall-request-summary"]').text()).toContain('model-a')
    expect(wrapper.get('[data-testid="waterfall-stage-table"]').text()).toContain('T0–T9 阶段')
    expect(wrapper.get('[data-testid="waterfall-attempts"]').text()).toContain('#1')
    expect(wrapper.get('[data-testid="waterfall-attempts"]').text()).toContain('model-a')
    expect(wrapper.get('[data-testid="waterfall-attempts"]').text()).toContain('success')
  })

  it('renders the selected waterfall record attempts exactly as the dispatch drawer does', () => {
    const fallbackAttempt = { ...enrichedAttempt, attempt_no: 3, model: 'fallback-model' }
    const wrapper = mount(WaterfallRequestDetailContent, {
      global: { plugins: [i18n] },
      props: {
        request: request({ attempts: [enrichedAttempt] }),
        fallbackAttempts: [fallbackAttempt],
      },
    })

    const attemptRows = wrapper.get('[data-testid="waterfall-attempts"]').findAll('li')
    expect(attemptRows).toHaveLength(1)
    expect(attemptRows[0].text()).toContain('#2')
    expect(attemptRows[0].text()).toContain('model-b')
    expect(attemptRows[0].text()).toContain('timeout')
    expect(attemptRows[0].text()).not.toContain('fallback-model')
  })

  it('uses merged request-detail attempts when a durable waterfall fallback has no attempts', () => {
    const wrapper = mount(WaterfallRequestDetailContent, {
      global: { plugins: [i18n] },
      props: {
        request: request({ attempts: undefined }),
        fallbackAttempts: [enrichedAttempt],
      },
    })

    const attemptRows = wrapper.get('[data-testid="waterfall-attempts"]').findAll('li')
    expect(attemptRows).toHaveLength(1)
    expect(attemptRows[0].text()).toContain('#2')
    expect(attemptRows[0].text()).toContain('model-b')
    expect(attemptRows[0].text()).toContain('timeout')
  })
})
