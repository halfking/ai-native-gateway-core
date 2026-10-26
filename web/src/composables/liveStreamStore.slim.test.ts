// liveStreamStore slim-wire tests — 2026-10-27 bandwidth round.
//
// The backend (?slim=1) ships tile-granular deltas keyed
// dim → laneId → patch instead of whole lanes per changed dimension.
// These tests pin the merge semantics the Go emitter (admin/live_stream_tile_delta.go)
// relies on, plus the ?dims= subscription plumbing:
//   - upsert replaces in place by request_id / appends new tiles;
//   - removed filters the lane's tiles;
//   - an unknown lane id creates the lane (name falls back to the id);
//   - removed_lanes drops whole lanes;
//   - mergeTileDelta preserves lane object references like mergeDelta does
//     (TransitionGroup keys must not flicker);
//   - buildUrl carries slim=1 + dims= and a dims change reconnects once.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  __testing,
  __resetStreamDimsForTest,
  acquireLiveStream,
  setLiveStreamDimsQuery,
  ENDPOINT,
} from './liveStreamStore'
import type { LiveStreamLanePatch, LiveStreamTileDelta } from './liveStreamStore'

function tile(id: string, status = 'success', ts = '2026-10-27T00:00:00Z') {
  return {
    request_id: id,
    timestamp: ts,
    model: 'gpt-x',
    vendor: 'openai',
    provider: 'openai',
    status,
  }
}

function patchOf(over: Partial<LiveStreamLanePatch> = {}): LiveStreamLanePatch {
  return {
    name: 'gpt-x',
    stats: { total: 1, success: 1, failure: 0 },
    ...over,
  }
}

describe('mergeTileDelta (slim wire)', () => {
  beforeEach(() => {
    __testing.resetStream()
    __testing.state.snapshot = {
      summary: { total: 1, success: 1, failure: 0 },
      dimensions: {
        credential: [],
        vendor: [],
        provider: [],
        model: [
          {
            id: 'gpt-x',
            name: 'gpt-x',
            dimension: 'model',
            requests: [tile('r1')],
            stats: { total: 1, success: 1, failure: 0 },
            isOthers: false,
          },
        ],
      },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }
  })

  it('upserts a tile in place and appends new tiles at the tail', () => {
    const td: LiveStreamTileDelta = {
      summary: { total: 2, success: 1, failure: 0 },
      changed_tiles: {
        model: {
          'gpt-x': patchOf({
            stats: { total: 2, success: 1, failure: 0 },
            upserts: [tile('r1', 'failure'), tile('r2', 'in_progress', '2026-10-27T00:00:05Z')],
          }),
        },
      },
    }
    __testing.mergeTileDelta(td)
    const lane = __testing.state.snapshot!.dimensions.model.find((l) => l.id === 'gpt-x')!
    expect(lane.requests.map((t) => t.request_id)).toEqual(['r1', 'r2'])
    expect(lane.requests[0].status).toBe('failure')
    expect(lane.stats.total).toBe(2)
    expect(__testing.state.snapshot!.summary.total).toBe(2)
  })

  it('filters removed tiles out of the lane', () => {
    const td: LiveStreamTileDelta = {
      summary: { total: 0, success: 0, failure: 0 },
      changed_tiles: {
        model: { 'gpt-x': patchOf({ removed: ['r1'], upserts: [] }) },
      },
    }
    __testing.mergeTileDelta(td)
    const lane = __testing.state.snapshot!.dimensions.model.find((l) => l.id === 'gpt-x')!
    expect(lane.requests).toHaveLength(0)
  })

  it('creates an unknown lane from its patch', () => {
    const td: LiveStreamTileDelta = {
      summary: { total: 1, success: 0, failure: 0 },
      changed_tiles: {
        model: {
          'gpt-new': patchOf({ name: 'GPT New', upserts: [tile('r9')] }),
        },
      },
    }
    __testing.mergeTileDelta(td)
    const lane = __testing.state.snapshot!.dimensions.model.find((l) => l.id === 'gpt-new')
    expect(lane).toBeDefined()
    expect(lane!.name).toBe('GPT New')
    expect(lane!.dimension).toBe('model')
    expect(lane!.requests.map((t) => t.request_id)).toEqual(['r9'])
  })

  it('drops lanes listed in removed_lanes', () => {
    const td: LiveStreamTileDelta = {
      summary: { total: 0, success: 0, failure: 0 },
      removed_lanes: { model: ['gpt-x'] },
    }
    __testing.mergeTileDelta(td)
    expect(__testing.state.snapshot!.dimensions.model).toHaveLength(0)
  })

  it('preserves the lane object reference when only stats change (no flicker)', () => {
    const before = __testing.state.snapshot!.dimensions.model.find((l) => l.id === 'gpt-x')
    const td: LiveStreamTileDelta = {
      summary: { total: 1, success: 1, failure: 0 },
      changed_tiles: {
        model: { 'gpt-x': patchOf({ stats: { total: 3, success: 3, failure: 0 } }) },
      },
    }
    __testing.mergeTileDelta(td)
    const after = __testing.state.snapshot!.dimensions.model.find((l) => l.id === 'gpt-x')
    expect(after).toBe(before)
  })

  it('merges dimension legends by key', () => {
    const td: LiveStreamTileDelta = {
      summary: { total: 2, success: 1, failure: 0 },
      changed_tiles: {
        model: { 'gpt-x': patchOf() },
      },
      dimension_legends: {
        model: [{ key: 'gpt-x', name: 'gpt-x', count: 2 }],
      },
    }
    __testing.mergeTileDelta(td)
    expect(__testing.state.snapshot!.dimension_legends.model).toEqual([
      { key: 'gpt-x', name: 'gpt-x', count: 2 },
    ])
  })
})

describe('slim URL plumbing', () => {
  beforeEach(() => {
    __resetStreamDimsForTest()
  })

  it('keeps the endpoint unchanged (params ride the query string)', () => {
    expect(ENDPOINT).toBe('/api/admin/live-stream')
  })

  it('carries slim=1 + dims= on the connection URL and reconnects once per dims burst', () => {
    class FakeEventSource {
      static urls: string[] = []
      url: string
      constructor(url: string) {
        this.url = url
        FakeEventSource.urls.push(url)
      }
      close() {}
      addEventListener() {}
      removeEventListener() {}
    }
    vi.stubGlobal('EventSource', FakeEventSource)
    vi.useFakeTimers()
    const release = acquireLiveStream()
    try {
      // No dims declared yet → slim flag only, no dims param.
      const first = FakeEventSource.urls[0] ?? ''
      expect(FakeEventSource.urls).toHaveLength(1)
      expect(new URLSearchParams(first.split('?')[1] ?? '').get('slim')).toBe('1')
      expect(new URLSearchParams(first.split('?')[1] ?? '').get('dims')).toBeNull()

      // Declaring a subscription must (a) record it and (b) reconnect once —
      // bursts coalesce into a single reconnect.
      setLiveStreamDimsQuery('model')
      setLiveStreamDimsQuery('provider')
      setLiveStreamDimsQuery('')
      expect(__testing.streamDimsQuery()).toBe('')
      vi.advanceTimersByTime(500)
      expect(FakeEventSource.urls).toHaveLength(2)
      const secondQ = FakeEventSource.urls[1].split('?')[1] ?? ''
      expect(new URLSearchParams(secondQ).get('slim')).toBe('1')
      // '' subscription is queue mode: the param must be PRESENT and empty.
      expect(new URLSearchParams(secondQ).get('dims')).toBe('')
    } finally {
      release()
      vi.useRealTimers()
      vi.unstubAllGlobals()
    }
  })
})
