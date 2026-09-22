import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { WaterfallRequest } from '../../api/dispatch'
import RequestDetailSectionHost from './RequestDetailSectionHost.vue'

const waterfall: WaterfallRequest = {
  request_id: 'req-host',
  result: 'success',
  waiting_in_total_ms: 1,
  waiting_in_model_ms: 1,
  waiting_in_node_ms: 1,
  routing_ms: 1,
  acquire_ms: 1,
  upstream_latency_ms: 1,
  streaming_duration_ms: 1,
  queue_wait_ms: 1,
  total_ms: 8,
}

const waterfallPanelStub = {
  props: ['selected', 'attempts', 'loading', 'error'],
  template: '<div data-testid="waterfall-panel-stub" />',
}

function mountHost(section: 'overview' | 'waterfall' | 'attempts' = 'waterfall') {
  return mount(RequestDetailSectionHost, {
    props: {
      section,
      requestId: 'req-host',
      log: null,
      unified: null,
      sessionSnap: null,
      sessionId: null,
      requestBody: null,
      responseBody: null,
      outboundBody: null,
      waterfall,
      attempts: [{ attempt_id: 'a-1', attempt_no: 1, credential_id: 5, outcome: 'success' }],
      waterfallLoading: false,
      waterfallError: '',
    },
    global: {
      stubs: {
        RequestWaterfallPanel: waterfallPanelStub,
        RequestOverviewPanel: { template: '<button data-testid="overview-goto" @click="$emit(\'goto\', \'waterfall\')" />' },
        ConversationMessagesPanel: true,
        FlowTimingPanel: true,
        CompressionRedactionPanel: true,
        MultimodalAttachmentsPanel: true,
      },
    },
  })
}

describe('RequestDetailSectionHost', () => {
  it('forwards waterfall data into the inline shared-content wrapper', () => {
    const wrapper = mountHost('waterfall')
    const panel = wrapper.getComponent(waterfallPanelStub)

    expect(panel.props('selected')).toEqual(waterfall)
    expect(panel.props('attempts')).toEqual([{ attempt_id: 'a-1', attempt_no: 1, credential_id: 5, outcome: 'success' }])
  })

  it('keeps attempts-only mode intentionally separate from the waterfall timeline', () => {
    const wrapper = mountHost('attempts')
    const panel = wrapper.getComponent(waterfallPanelStub)

    expect(panel.props('selected')).toBeNull()
    expect(panel.props('attempts')).toHaveLength(1)
  })

  it('forwards overview waterfall navigation to its parent', async () => {
    const wrapper = mountHost('overview')

    await wrapper.get('[data-testid="overview-goto"]').trigger('click')
    expect(wrapper.emitted('goto')).toEqual([['waterfall']])
  })
})
