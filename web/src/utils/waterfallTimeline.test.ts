import { describe, expect, it } from 'vitest'
import {
  emptyStateMessage,
  resolveStageBars,
  waterfallAnchorMs,
} from './waterfallTimeline'
import type { WaterfallRequest } from '../api/dispatch'

function base(partial: Partial<WaterfallRequest> = {}): WaterfallRequest {
  return {
    request_id: 'r1',
    result: 'success',
    waiting_in_total_ms: 0,
    waiting_in_model_ms: 0,
    waiting_in_node_ms: 0,
    routing_ms: 0,
    acquire_ms: 0,
    upstream_latency_ms: 0,
    streaming_duration_ms: 0,
    queue_wait_ms: 0,
    total_ms: 0,
    ...partial,
  }
}

describe('waterfallTimeline', () => {
  it('uses arrived_at as anchor', () => {
    const r = base({ arrived_at: '2026-08-21T10:00:00.000Z' })
    expect(waterfallAnchorMs(r)).toBe(Date.parse('2026-08-21T10:00:00.000Z'))
  })

  it('prefers real timestamps over synthesis', () => {
    const r = base({
      arrived_at: '2026-08-21T10:00:00.000Z',
      total_enqueued_at: '2026-08-21T10:00:00.010Z',
      total_dequeued_at: '2026-08-21T10:00:00.040Z',
      waiting_in_total_ms: 30,
    })
    const bars = resolveStageBars(r)
    const total = bars.find((b) => b.key === 'total')
    expect(total?.synthesized).toBe(false)
    expect(total?.ms).toBe(30)
  })

  it('synthesizes contiguous bars from ms when timestamps missing', () => {
    const r = base({
      arrived_at: '2026-08-21T10:00:00.000Z',
      waiting_in_total_ms: 40,
      waiting_in_model_ms: 100,
      waiting_in_node_ms: 20,
      upstream_latency_ms: 200,
      streaming_duration_ms: 500,
    })
    const bars = resolveStageBars(r)
    expect(bars).toHaveLength(5)
    expect(bars.every((b) => b.synthesized)).toBe(true)
    expect(bars[0].start).toBe(Date.parse('2026-08-21T10:00:00.000Z'))
    expect(bars[0].end - bars[0].start).toBe(40)
    expect(bars[1].start).toBe(bars[0].end)
    expect(bars[4].end - bars[0].start).toBe(40 + 100 + 20 + 200 + 500)
  })

  it('skips zero-ms stages when synthesizing', () => {
    const r = base({
      arrived_at: '2026-08-21T10:00:00.000Z',
      upstream_latency_ms: 150,
      streaming_duration_ms: 300,
    })
    const bars = resolveStageBars(r)
    expect(bars.map((b) => b.key)).toEqual(['upstream', 'stream'])
  })

  it('emptyStateMessage distinguishes unwired vs no data', () => {
    expect(emptyStateMessage({ wired: false, count: 0 })).toMatch(/未接线/)
    expect(emptyStateMessage({ wired: true, source: 'none', count: 0 })).toMatch(/t0_arrived_at/)
    expect(emptyStateMessage({ wired: true, source: 'memory', count: 3 })).toBe('')
  })
})
