import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import QueueWaterfallTimeline from './QueueWaterfallTimeline.vue'
import type { WaterfallRequest } from '../api/dispatch'

function req(partial: Partial<WaterfallRequest> = {}): WaterfallRequest {
  return {
    request_id: 'req-aaaaaaaaaa',
    result: 'success',
    model: 'gpt-4',
    waiting_in_total_ms: 40,
    waiting_in_model_ms: 0,
    waiting_in_node_ms: 0,
    routing_ms: 0,
    acquire_ms: 0,
    upstream_latency_ms: 120,
    streaming_duration_ms: 240,
    queue_wait_ms: 40,
    total_ms: 400,
    arrived_at: '2026-08-21T10:00:00.000Z',
    ...partial,
  }
}

describe('QueueWaterfallTimeline', () => {
  it('shows empty copy without legend when there are no samples', () => {
    const w = mount(QueueWaterfallTimeline, {
      props: { requests: [], wired: false, source: 'none' },
    })
    expect(w.get('[data-testid="qwt-empty"]').text()).toMatch(/未接线/)
    expect(w.find('[data-testid="qwt-legend"]').exists()).toBe(false)
  })

  it('places legend above the table and one row per request', () => {
    const w = mount(QueueWaterfallTimeline, {
      props: {
        requests: [req(), req({ request_id: 'req-bbbbbbbbbb', total_ms: 800 })],
        wired: true,
        source: 'memory',
      },
    })
    expect(w.find('[data-testid="qwt-empty"]').exists()).toBe(false)
    const legend = w.get('[data-testid="qwt-legend"]')
    expect(legend.text()).toContain('总队列')
    expect(legend.text()).toContain('流式')
    expect(w.findAll('[data-testid="qwt-row"]')).toHaveLength(2)
    expect(w.findAll('[data-testid="qwt-seg"]').length).toBeGreaterThan(0)
    expect(w.find('[data-testid="qwt-composition"]').exists()).toBe(true)
  })

  it('emits select when a row is clicked', async () => {
    const w = mount(QueueWaterfallTimeline, {
      props: { requests: [req()], wired: true, source: 'memory' },
    })
    await w.get('[data-testid="qwt-row"]').trigger('click')
    expect(w.emitted('select')?.[0]?.[0]).toMatchObject({ request_id: 'req-aaaaaaaaaa' })
  })
})
