import { describe, it, expect, beforeEach } from 'vitest'
import { __testing } from './liveStreamStore'
import type { LiveStreamDelta, LiveStreamLane, LiveStreamTile } from './liveStreamStore'

function tile(id: string): LiveStreamTile {
  return {
    request_id: id,
    timestamp: '2026-07-14T12:00:00Z',
    model: 'gpt-4o',
    vendor: 'openai',
    provider: 'openai',
    status: 'success',
  }
}

function lane(id: string, total: number, tiles: LiveStreamTile[] = []): LiveStreamLane {
  return {
    id,
    name: id,
    dimension: 'vendor',
    requests: tiles,
    stats: { total, success: total, failure: 0 },
    isOthers: false,
  }
}

describe('mergeDelta', () => {
  beforeEach(() => {
    __testing.resetStream()
    __testing.state.snapshot = {
      summary: { total: 2, success: 2, failure: 0 },
      detail_dimensions: {
        vendor: [lane('anthropic', 1), lane('openai', 1, [tile('r1')])],
        provider: [],
        model: [],
      },
      dimensions: {
        vendor: [lane('anthropic', 1), lane('openai', 1, [tile('r1')])],
        provider: [],
        model: [],
      },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    }
  })

  it('preserves lane object reference when lane data is unchanged', () => {
    const openaiBefore = __testing.state.snapshot!.dimensions.vendor[1]
    const delta: LiveStreamDelta = {
      summary: { total: 3, success: 3, failure: 0 },
      changed_lanes: {
        vendor: [lane('anthropic', 2), lane('openai', 1, [tile('r1')])],
        provider: [],
        model: [],
      },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    __testing.mergeDelta(delta)
    expect(__testing.state.snapshot!.dimensions.vendor[1]).toBe(openaiBefore)
    expect(__testing.state.snapshot!.dimensions.vendor[0].stats.total).toBe(2)
  })

  it('replaces lane object when tiles change', () => {
    const openaiBefore = __testing.state.snapshot!.dimensions.vendor[1]
    const delta: LiveStreamDelta = {
      summary: { total: 3, success: 3, failure: 0 },
      changed_lanes: {
        vendor: [lane('anthropic', 1), lane('openai', 2, [tile('r1'), tile('r2')])],
        provider: [],
        model: [],
      },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    __testing.mergeDelta(delta)
    expect(__testing.state.snapshot!.dimensions.vendor[1]).not.toBe(openaiBefore)
    expect(__testing.state.snapshot!.dimensions.vendor[1].requests).toHaveLength(2)
  })
})
