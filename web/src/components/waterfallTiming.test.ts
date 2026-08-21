import { describe, it, expect } from 'vitest'
import { buildStageWindows } from './waterfallTiming'

describe('buildStageWindows', () => {
  it('uses real timestamps when present', () => {
    const wins = buildStageWindows({
      total_enqueued_at: '2026-08-21T10:00:00.000Z',
      total_dequeued_at: '2026-08-21T10:00:00.040Z',
      waiting_in_total_ms: 40,
    })
    expect(wins).toHaveLength(1)
    expect(wins[0].key).toBe('total')
    expect(wins[0].durationMs).toBe(40)
  })

  it('synthesises from ms chain when timestamps missing', () => {
    const now = Date.parse('2026-08-21T12:00:00.000Z')
    const wins = buildStageWindows({
      arrived_at: '2026-08-21T12:00:00.000Z',
      waiting_in_total_ms: 10,
      waiting_in_model_ms: 20,
      waiting_in_node_ms: 30,
      upstream_latency_ms: 100,
      streaming_duration_ms: 200,
    }, now)
    expect(wins.map((w) => w.key)).toEqual(['total', 'model', 'cred', 'upstream', 'stream'])
    expect(wins[0].startMs).toBe(now)
    expect(wins[4].endMs - wins[0].startMs).toBe(360)
  })

  it('falls back to total_ms single bar', () => {
    const wins = buildStageWindows({ total_ms: 500 }, 1_000)
    expect(wins).toHaveLength(1)
    expect(wins[0].durationMs).toBe(500)
    expect(wins[0].endMs).toBe(1_500)
  })

  it('returns empty when no timing signal', () => {
    expect(buildStageWindows({})).toEqual([])
  })
})
