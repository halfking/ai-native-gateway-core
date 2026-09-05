import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import RequestProcessingFlowDiagram from './RequestProcessingFlowDiagram.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      requestJourneys: { event: { node_switched: '节点切换' } },
      requestDetail: {
        flow: {
              diagram: {
            ariaLabel: '请求处理流程', trackLabel: '流程事件序列',
            event: '流程事件', noData: '暂无可观测流程', noEvents: '暂无流程事件',
            stages: { routing: '路由解析', upstream: '上游请求', retrying: '重试中' },
            status: { success: '成功', failed: '失败', timeout: '超时', skipped: '跳过' },
            flags: { compression: '压缩', retry: '重试', nodeSwitch: '节点切换', degraded: '观测降级' },
            evidenceLabel: '路由与瀑布观测证据', evidenceTitle: '已观测尝试', waterfallAttempt: '瀑布尝试 #{number}', noDetails: '没有更多详情', degraded: '观测降级', lanesLabel: '请求尝试泳道', lanesTitle: '已观测旅程尝试', attemptLane: '尝试 #{number}', sourceJourney: 'Journey 来源', waterfallLabel: '瀑布阶段条', waterfallTitle: '瀑布阶段条', lastAttemptSemantics: '请求级时间；T5–T9 可能反映末次尝试', synthesizedBars: '合成阶段表示缺少时间戳（观测降级）。', requestTimeline: '请求时间线',
            legend: { success: '成功', failed: '失败/超时', special: '压缩 · 重试 · 节点切换' },
          },
        },
      },
    },
  },
})

function trace(events: any[]) {
  return {
    request_id: 'req-1',
    events,
    final_status: 'success',
    total_duration_ms: 120,
    source: 'postgres',
  }
}

describe('RequestProcessingFlowDiagram', () => {
  it('renders ordered stages and readable metadata', () => {
    const wrapper = mount(RequestProcessingFlowDiagram, {
      global: { plugins: [i18n] },
      props: {
        trace: trace([
          { seq: 2, stage: 'upstream', module: 'provider', timestamp: '', duration_ms: 80, status: 'success', details: {} },
          { seq: 1, stage: 'routing', module: 'router', timestamp: '', duration_ms: 40, status: 'success', details: {} },
        ]) as any,
      },
    })
    expect(wrapper.find('[data-testid="request-processing-flow"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('路由解析')
    expect(wrapper.text()).toContain('上游请求')
    expect(wrapper.text()).toContain('40ms')
    expect(wrapper.text()).toContain('80ms')
  })

  it('marks retry, compression, node switch and errors from real event data', () => {
    const wrapper = mount(RequestProcessingFlowDiagram, {
      global: { plugins: [i18n] },
      props: {
        trace: trace([
          { seq: 1, stage: 'retrying', module: 'router', timestamp: '', duration_ms: 0, status: 'failed', error: 'timeout', details: { retry_reason: 'timeout', compression_applied: true, to_credential_id: 9 } },
        ]) as any,
      },
    })
    expect(wrapper.text()).toContain('重试')
    expect(wrapper.text()).toContain('压缩')
    expect(wrapper.text()).toContain('节点切换')
    expect(wrapper.text()).toContain('timeout')
    expect(wrapper.text()).toContain('—')
  })

  it('renders real journey and waterfall evidence without fabricating trace nodes', () => {
    const wrapper = mount(RequestProcessingFlowDiagram, {
      global: { plugins: [i18n] },
      props: {
        trace: null,
        journey: {
          observation_status: 'observation_degraded',
          events: [{ seq: 1, event_type: 'node_switched', stage: 'node_selection', observation_status: 'observation_degraded', occurred_at: '', request_id: 'req-1', tenant_id: 't', gateway_instance_id: 'g', from_credential_id: 1, to_credential_id: 2 }],
        },
        waterfall: { attempts: [{ attempt_id: 'a-1', attempt_no: 1, credential_id: 2, model: 'model-a', outcome: 'failure' }] },
      } as any,
    })
    expect(wrapper.find('.flow-node').exists()).toBe(false)
    expect(wrapper.text()).toContain('已观测尝试')
    expect(wrapper.text()).toContain('节点切换')
    expect(wrapper.text()).toContain('瀑布尝试 #1')
    expect(wrapper.text()).toContain('观测降级')
  })


  it('renders separate journey attempt lanes and waterfall stage bars', () => {
    const wrapper = mount(RequestProcessingFlowDiagram, {
      global: { plugins: [i18n] },
      props: {
        trace: null,
        journey: { observation_status: 'complete', events: [
          { seq: 1, event_type: 'attempt_started', stage: 'upstream', observation_status: 'complete', occurred_at: '', request_id: 'req-1', tenant_id: 't', gateway_instance_id: 'g', attempt: { attempt_id: 'a1', attempt_no: 1, provider: 'p1' } },
          { seq: 2, event_type: 'attempt_failed', stage: 'retrying', retry_reason: 'timeout', observation_status: 'complete', occurred_at: '', request_id: 'req-1', tenant_id: 't', gateway_instance_id: 'g', attempt: { attempt_id: 'a2', attempt_no: 2, provider: 'p2' } },
        ] },
        waterfall: { request_id: 'req-1', result: 'failure', arrived_at: '2026-01-01T00:00:00Z', waiting_in_total_ms: 10, waiting_in_model_ms: 0, waiting_in_node_ms: 0, routing_ms: 0, acquire_ms: 0, upstream_latency_ms: 20, streaming_duration_ms: 0, queue_wait_ms: 10, total_ms: 30 },
      } as any,
    })
    expect(wrapper.find('[data-testid="journey-attempt-lane-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="journey-attempt-lane-2"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="waterfall-stage-bars"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('请求级时间')
    expect(wrapper.text()).toContain('合成阶段')
  })

  it('does not fabricate a flow when trace data is absent', () => {
    const wrapper = mount(RequestProcessingFlowDiagram, { global: { plugins: [i18n] }, props: { trace: null } })
    expect(wrapper.text()).toContain('暂无可观测流程')
    expect(wrapper.find('.flow-node').exists()).toBe(false)
  })
})
