import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { WaterfallRequest } from '../api/dispatch'

const { fetchDispatchWaterfall, fetchDispatchQueues, openRequestDetailPage } = vi.hoisted(() => ({
  fetchDispatchWaterfall: vi.fn(),
  fetchDispatchQueues: vi.fn(),
  openRequestDetailPage: vi.fn(),
}))

vi.mock('../api/dispatch', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/dispatch')>()
  return { ...actual, fetchDispatchWaterfall, fetchDispatchQueues }
})
vi.mock('../utils/openRequestDetailPage', () => ({ openRequestDetailPage }))

import DispatchWaterfallView from './DispatchWaterfallView.vue'

const selectedRequest: WaterfallRequest = {
  request_id: 'req-dispatch-42',
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

const timelineStub = {
  emits: ['select'],
  template: '<button type="button" data-testid="select-waterfall" @click="$emit(\'select\', $attrs.request)">select</button>',
}
const detailStub = {
  emits: ['open-fullscreen'],
  template: '<button type="button" data-testid="open-fullscreen" @click="$emit(\'open-fullscreen\')">全屏详情</button>',
}

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/dispatch/waterfall', component: DispatchWaterfallView }],
  })
}

describe('DispatchWaterfallView', () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  it('opens the selected request in the waterfall detail tab', async () => {
    fetchDispatchWaterfall.mockResolvedValue({
      requests: [selectedRequest],
      source: 'memory',
      wired: true,
      enabled: true,
      bottleneck_diagnosis: { bottleneck: 'none', message: 'ok' },
    })
    fetchDispatchQueues.mockResolvedValue({ enabled: true, wired: true, models: [], credentials: [] })

    const router = makeRouter()
    await router.push('/dispatch/waterfall')
    await router.isReady()
    const wrapper = mount(DispatchWaterfallView, {
      global: {
        plugins: [router],
        stubs: {
          DispatchWaterfallToolbar: true,
          DispatchWaterfallStatus: true,
          QueueWaterfallTimeline: timelineStub,
          DispatchWaterfallDetail: detailStub,
        },
      },
    })
    const timeline = wrapper.getComponent(timelineStub)
    await timeline.vm.$emit('select', selectedRequest)
    await flushPromises()

    await wrapper.get('[data-testid="open-fullscreen"]').trigger('click')

    expect(openRequestDetailPage).toHaveBeenCalledWith(
      'req-dispatch-42',
      { tab: 'waterfall' },
      router,
    )
  })
})
