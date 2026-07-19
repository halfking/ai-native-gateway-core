// liveStreamStore unit tests — 2026-07-14 regression guards for
// the "swim lane flicker" + "missing probe after no-candidates"
// fixes. These tests exercise the pure helpers (pushOrQueue /
// mergeDelta) without spinning up a real SSE connection.
//
// Lane and request identity must remain stable across server deltas so the
// dashboard does not animate a routine state update as a remove/reinsert.

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

  it('keeps existing request replay during a snapshot refresh', () => {
    __testing.state.requests = [makeRequest('r1', 'in_progress')]
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: '2026-07-14T00:01:00Z',
      snapshot: {
        summary: { total: 2, success: 2, failure: 0 },
        detail_dimensions: { vendor: [lane('openai', 2, [tile('r1'), tile('r2')])], provider: [], model: [] },
        dimensions: { vendor: [lane('openai', 2, [tile('r1'), tile('r2')])], provider: [], model: [] },
        dimension_legends: { vendor: [], provider: [], model: [] },
        status_legends: [],
      },
    })
    expect(__testing.state.requests.map((request) => request.request_id)).toEqual(['r1'])
  })

  it('applies an idle marker delta to the affected lane', () => {
    const delta: LiveStreamDelta = {
      summary: { total: 2, success: 2, failure: 0 },
      changed_lanes: {
        vendor: [lane('anthropic', 2), lane('openai', 1, [tile('r1')])],
        provider: [],
        model: [],
      },
      dimension_legends: { vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    __testing.handleEnvelope({ type: 'idle_marker', ts: '2026-07-14T00:01:00Z', delta })
    const anthropic = __testing.state.snapshot!.dimensions.vendor.find((lane) => lane.id === 'anthropic')
    expect(anthropic?.stats.total).toBe(2)
  })
})

// ---------------------------------------------------------------------------
// pushOrQueue
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

  it('updates an existing request without changing queue order or length', () => {
    __testing.pushOrQueue(makeRequest('r1', 'in_progress'))
    __testing.pushOrQueue(makeRequest('r2', 'in_progress'))
    __testing.pushOrQueue(makeRequest('r1', 'success'))
    const ids = __testing.state.requests.map((r) => r.request_id)
    expect(ids).toEqual(['r1', 'r2'])
    expect(__testing.state.requests).toHaveLength(2)
    expect(__testing.state.requests[0]?.status).toBe('success')
  })

  it('updates idle_marker in place when request_id is stable', () => {
    __testing.pushOrQueue({ type: 'idle_marker', request_id: 'idle-openai', ts: '2026-07-14T00:00:00Z' } as LiveRequest)
    __testing.pushOrQueue({ type: 'idle_marker', request_id: 'idle-openai', ts: '2026-07-14T00:05:00Z' } as LiveRequest)
    expect(__testing.state.requests).toHaveLength(1)
    expect(__testing.state.requests[0]?.ts).toBe('2026-07-14T00:05:00Z')
  })
})
