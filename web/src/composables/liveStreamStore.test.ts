// liveStreamStore unit tests — 2026-07-14 regression guards for
// the "swim lane flicker" + "missing probe after no-candidates"
// fixes. These tests exercise the pure helpers (pushOrQueue /
// mergeDelta) without spinning up a real SSE connection.
//
// Origin-side: lane-object reference preservation in mergeDelta
// (admin/feat/live-stream Redis pub/sub series). HEAD-side: splice
// + push for same request_id transition so Vue TransitionGroup
// runs the leave/enter animation. We keep both.

import { describe, it, expect, beforeEach } from 'vitest'
import { __testing } from './liveStreamStore'
import type {
  LiveRequest,
  LiveStreamDelta,
  LiveStreamLane,
  LiveStreamTile,
} from './liveStreamStore'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

function makeRequest(id: string, status: LiveStatus, opts: Partial<LiveRequest> = {}): LiveRequest {
  return {
    request_id: id,
    ts: '2026-07-14T00:00:00Z',
    model: 'gpt-4o',
    provider_code: 'openai',
    status,
    ...opts,
  }
}

// Tiny alias to keep the call sites readable; the source-of-truth type
// is the literal union in liveStreamStore.
type LiveStatus = NonNullable<LiveRequest['status']>

// ---------------------------------------------------------------------------
// mergeDelta (origin-side: lane-object reference preservation)
// ---------------------------------------------------------------------------

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

  it('updates lane tiles in place when requests change', () => {
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
    expect(__testing.state.snapshot!.dimensions.vendor[1]).toBe(openaiBefore)
    expect(__testing.state.snapshot!.dimensions.vendor[1].requests).toHaveLength(2)
  })

  // 2026-07-14: a brand-new lane id arrives. Origin's
  // mergeLaneList implementation only preserves lanes that are
  // also present in the incoming batch; the SSE wire payload must
  // therefore carry the FULL lane list for the dimension on every
  // change (the backend's lanesChanged check ensures this — see
  // admin/live_stream_redis_store.go:lanesChanged). The test
  // asserts the contract on both sides: backend sends full
  // dimension, frontend reuses unchanged objects so the DOM stays
  // stable.
  it('appends a new lane at the tail without reordering existing lanes', () => {
    const openaiBefore = __testing.state.snapshot!.dimensions.vendor[1]
    const delta: LiveStreamDelta = {
      summary: { total: 4, success: 3, failure: 0 },
      changed_lanes: {
        vendor: [
          lane('google', 1, [tile('r3')]),
          lane('anthropic', 2),
          lane('openai', 1, [tile('r1')]),
        ],
        provider: [],
        model: [],
      },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    __testing.mergeDelta(delta)
    const after = __testing.state.snapshot!.dimensions.vendor
    expect(after.map((l) => l.id)).toEqual(['anthropic', 'openai', 'google'])
    expect(after[1]).toBe(openaiBefore)
  })
})

// ---------------------------------------------------------------------------
// mergeSnapshotFromServer — lanes must survive empty Redis reconcile
// ---------------------------------------------------------------------------

describe('mergeSnapshotFromServer', () => {
  beforeEach(() => {
    __testing.resetStream()
    __testing.state.snapshot = {
      summary: { total: 1, success: 1, failure: 0 },
      detail_dimensions: { vendor: [lane('openai', 1, [tile('r1')])], provider: [], model: [] },
      dimensions: { vendor: [lane('openai', 1, [tile('r1')])], provider: [], model: [] },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    }
  })

  it('keeps existing lanes when incoming snapshot omits them', () => {
    __testing.mergeSnapshotFromServer({
      summary: { total: 0, success: 0, failure: 0 },
      detail_dimensions: { vendor: [], provider: [], model: [] },
      dimensions: { vendor: [], provider: [], model: [] },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    })
    expect(__testing.state.snapshot!.dimensions.vendor.map((l) => l.id)).toEqual(['openai'])
  })

  it('updates summary from incoming without dropping lanes', () => {
    __testing.mergeSnapshotFromServer({
      summary: { total: 2, success: 2, failure: 0 },
      detail_dimensions: { vendor: [lane('openai', 2, [tile('r1'), tile('r2')])], provider: [], model: [] },
      dimensions: { vendor: [lane('openai', 2, [tile('r1'), tile('r2')])], provider: [], model: [] },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    })
    expect(__testing.state.snapshot!.summary.total).toBe(2)
    expect(__testing.state.snapshot!.dimensions.vendor[0].requests).toHaveLength(2)
  })
})

// ---------------------------------------------------------------------------
// pushOrQueue (HEAD-side: same request_id transition triggers leave+enter)
// ---------------------------------------------------------------------------

describe('pushOrQueue', () => {
  beforeEach(() => {
    __testing.resetStream()
  })

  it('appends a fresh request_id to the tail', () => {
    __testing.pushOrQueue(makeRequest('r1', 'in_progress'))
    __testing.pushOrQueue(makeRequest('r2', 'in_progress'))
    expect(__testing.state.requests.map((r) => r.request_id)).toEqual(['r1', 'r2'])
  })

  // 2026-07-14 regression guard: an in_progress -> success transition
  // used to overwrite the tile in place, producing a "blue silently
  // turns green" flicker. The fix splice+pushes so the old key
  // disappears (Vue TransitionGroup leave animation) and a fresh key
  // appears at the tail (enter animation).
  it('moves an updated request_id to the tail (animate out + in)', () => {
    __testing.pushOrQueue(makeRequest('r1', 'in_progress'))
    __testing.pushOrQueue(makeRequest('r2', 'in_progress'))
    __testing.pushOrQueue(makeRequest('r1', 'success'))
    const ids = __testing.state.requests.map((r) => r.request_id)
    expect(ids).toEqual(['r2', 'r1'])
    expect(__testing.state.requests[1]?.status).toBe('success')
  })

  it('does not de-dup idle_marker by request_id', () => {
    __testing.pushOrQueue({ type: 'idle_marker', ts: '2026-07-14T00:00:00Z' } as LiveRequest)
    __testing.pushOrQueue({ type: 'idle_marker', ts: '2026-07-14T00:01:00Z' } as LiveRequest)
    expect(__testing.state.requests).toHaveLength(2)
    expect(__testing.state.requests.every((r) => r.type === 'idle_marker')).toBe(true)
  })
})
