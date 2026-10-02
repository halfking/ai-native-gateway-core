import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import type { WaterfallAttempt, WaterfallRequest } from '../../api/dispatch'
import RequestWaterfallPanel from './RequestWaterfallPanel.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { credentialFallback: '凭据' },
      requestDetail: {
        waterfall: {
          loading: '加载调度瀑布…',
          source: '数据源：{src}',
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

function request(partial: Partial<WaterfallRequest> = {}): WaterfallRequest {
  return {
    request_id: 'req-waterfall-panel',
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

const attempt: WaterfallAttempt = {
  attempt_id: 'attempt-1',
  attempt_no: 1,
  credential_id: 5,
  model: 'model-a',
  outcome: 'success',
}

function mountPanel(props: InstanceType<typeof RequestWaterfallPanel>['$props']) {
  return mount(RequestWaterfallPanel, {
    props,
    global: { plugins: [i18n] },
  })
}

describe('RequestWaterfallPanel', () => {
  it('renders a loading state before selected request data arrives', () => {
    const wrapper = mountPanel({ selected: null, loading: true })

    expect(wrapper.get('[data-testid="request-waterfall-loading"]').text()).toContain('加载调度瀑布')
    expect(wrapper.find('[data-testid="waterfall-request-detail"]').exists()).toBe(false)
  })

  it('renders the same selected waterfall record content as the dispatch drawer', () => {
    const drawerAttempt = { ...attempt, attempt_no: 7, model: 'drawer-model' }
    const richerRoutingAttempt = { ...attempt, attempt_no: 8, model: 'routing-model' }
    const wrapper = mountPanel({
      selected: request({ attempts: [drawerAttempt] }),
      attempts: [richerRoutingAttempt],
    })

    expect(wrapper.find('[data-testid="waterfall-request-detail"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="waterfall-request-summary"]').text()).toContain('req-waterfall-panel')
    expect(wrapper.find('[data-testid="waterfall-stage-table"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="waterfall-attempts"]').text()).toContain('#7')
    expect(wrapper.get('[data-testid="waterfall-attempts"]').text()).toContain('drawer-model')
    expect(wrapper.get('[data-testid="waterfall-attempts"]').text()).not.toContain('routing-model')
  })

  it('keeps merged routing attempts visible when the durable waterfall response omits attempts', () => {
    const fallbackAttempt = { ...attempt, attempt_no: 8, model: 'routing-model', error_kind: 'timeout' }
    const durableFallback = request()
    delete durableFallback.attempts
    const wrapper = mountPanel({
      selected: durableFallback,
      attempts: [fallbackAttempt],
    })

    const attempts = wrapper.get('[data-testid="waterfall-attempts"]').text()
    expect(attempts).toContain('#8')
    expect(attempts).toContain('routing-model')
    expect(attempts).toContain('timeout')
  })

  it('keeps an explicit empty waterfall attempt list empty, matching the dispatch drawer', () => {
    const wrapper = mountPanel({
      selected: request({ attempts: [] }),
      attempts: [attempt],
    })

    expect(wrapper.find('[data-testid="waterfall-attempts"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('无 Attempts 记录。')
  })

  it('shows attempts without a timeline in attempts-only mode', () => {
    const wrapper = mountPanel({ selected: null, attempts: [attempt] })

    expect(wrapper.find('[data-testid="request-waterfall-empty"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="request-waterfall-attempts-only"]').text()).toContain('#1')
    expect(wrapper.get('[data-testid="request-waterfall-attempts-only"]').text()).toContain('model-a')
    expect(wrapper.find('[data-testid="waterfall-request-detail"]').exists()).toBe(false)
  })
})
