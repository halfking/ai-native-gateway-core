import { describe, expect, it } from 'vitest'
import {
  emptyStateMessage,
  formatAxisMs,
  layoutBar,
  layoutRows,
  medianComposition,
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

const T0 = '2026-08-21T10:00:00.000Z'

describe('waterfallTimeline', () => {
  it('uses arrived_at as anchor', () => {
    expect(waterfallAnchorMs(base({ arrived_at: T0 }))).toBe(Date.parse(T0))
  })

  it('prefers real timestamps over synthesis', () => {
    const bars = resolveStageBars(base({
      arrived_at: T0,
      total_enqueued_at: '2026-08-21T10:00:00.010Z',
      total_dequeued_at: '2026-08-21T10:00:00.040Z',
      waiting_in_total_ms: 30,
    }))
    const total = bars.find((b) => b.key === 'total')
    expect(total?.synthesized).toBe(false)
    expect(total?.ms).toBe(30)
  })

  it('resolves nine sequential gaps from T0–T9 timestamps', () => {
    const bars = resolveStageBars(base({
      arrived_at: T0,
      total_enqueued_at: '2026-08-21T10:00:00.010Z',
      total_dequeued_at: '2026-08-21T10:00:00.050Z',
      model_enqueued_at: '2026-08-21T10:00:00.055Z',
      model_dequeued_at: '2026-08-21T10:00:00.155Z',
      cred_enqueued_at: '2026-08-21T10:00:00.160Z',
      cred_dequeued_at: '2026-08-21T10:00:00.180Z',
      forward_start_at: '2026-08-21T10:00:00.185Z',
      response_start_at: '2026-08-21T10:00:00.385Z',
      response_end_at: '2026-08-21T10:00:00.885Z',
      routing_ms: 110,
    }))
    expect(bars.map((b) => b.key)).toEqual([
      'arrive', 'total', 'admit', 'model', 'select', 'cred', 'acquire', 'upstream', 'stream',
    ])
    expect(bars.find((b) => b.label === '路由')).toBeUndefined()
    expect(bars.find((b) => b.key === 'model')?.ms).toBe(100)
  })

  it('does not emit routing_ms as a sibling bar', () => {
    const bars = resolveStageBars(base({
      arrived_at: T0,
      routing_ms: 500,
      waiting_in_model_ms: 80,
    }))
    expect(bars.map((b) => b.key)).toEqual(['model'])
  })

  it('synthesizes contiguous bars including acquire_ms', () => {
    const bars = resolveStageBars(base({
      arrived_at: T0,
      waiting_in_total_ms: 40,
      waiting_in_model_ms: 100,
      waiting_in_node_ms: 20,
      acquire_ms: 15,
      upstream_latency_ms: 200,
      streaming_duration_ms: 500,
    }))
    expect(bars.map((b) => b.key)).toEqual([
      'total', 'model', 'cred', 'acquire', 'upstream', 'stream',
    ])
    expect(bars[5].end - bars[0].start).toBe(40 + 100 + 20 + 15 + 200 + 500)
  })

  it('skips zero-ms stages when synthesizing', () => {
    const bars = resolveStageBars(base({
      arrived_at: T0,
      upstream_latency_ms: 150,
      streaming_duration_ms: 300,
    }))
    expect(bars.map((b) => b.key)).toEqual(['upstream', 'stream'])
  })

  it('lays out bars relative to T0 against sample max span', () => {
    const { rows, axisMax } = layoutRows([
      base({ request_id: 'a', arrived_at: T0, waiting_in_total_ms: 100, total_ms: 400 }),
      base({ request_id: 'b', arrived_at: T0, waiting_in_total_ms: 50, total_ms: 200 }),
    ])
    expect(axisMax).toBe(400)
    expect(rows[0].bars[0].widthPct).toBe(25)
    expect(rows[1].bars[0].widthPct).toBe(12.5)
  })

  it('clamps tiny bars to a visible minimum width', () => {
    const laid = layoutBar({
      key: 'total', label: '总队列', color: '#409EFF',
      start: 0, end: 1, ms: 1, synthesized: true,
    }, 0, 10_000)
    expect(laid.widthPct).toBe(0.4)
  })

  it('stacks median stage shares to 100%', () => {
    const a = resolveStageBars(base({ arrived_at: T0, waiting_in_total_ms: 10, upstream_latency_ms: 90 }))
    const b = resolveStageBars(base({ arrived_at: T0, waiting_in_total_ms: 30, upstream_latency_ms: 70 }))
    const slices = medianComposition([a, b])
    expect(slices.map((s) => s.key)).toEqual(['total', 'upstream'])
    expect(slices[0].pct + slices[1].pct).toBe(100)
  })

  it('formatAxisMs uses ms under 1s', () => {
    expect(formatAxisMs(40)).toBe('40ms')
    expect(formatAxisMs(2000)).toBe('2s')
  })

  it('emptyStateMessage distinguishes unwired vs no data', () => {
    expect(emptyStateMessage({ wired: false, count: 0 })).toMatch(/未接线/)
    expect(emptyStateMessage({ wired: true, source: 'none', count: 0 })).toMatch(/t0_arrived_at/)
    expect(emptyStateMessage({ wired: true, source: 'memory', count: 3 })).toBe('')
  })
})
