import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import RequestProcessingFlowDiagram from './RequestProcessingFlowDiagram.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      requestDetail: {
        flow: {
          diagram: {
            event: '流程事件', noData: '暂无可观测流程', noEvents: '暂无流程事件',
            stages: { routing: '路由解析', upstream: '上游请求' },
            status: { success: '成功', failed: '失败', timeout: '超时', skipped: '跳过' },
            flags: { compression: '压缩', retry: '重试', nodeSwitch: '节点切换', degraded: '观测降级' },
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

  it('does not fabricate a flow when trace data is absent', () => {
    const wrapper = mount(RequestProcessingFlowDiagram, { global: { plugins: [i18n] }, props: { trace: null } })
    expect(wrapper.text()).toContain('暂无可观测流程')
    expect(wrapper.find('.flow-node').exists()).toBe(false)
  })
})
