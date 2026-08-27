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
  LiveStreamEnvelope,
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
      dimensions: {
        credential: [],
        vendor: [lane('anthropic', 1), lane('openai', 1, [tile('r1')])],
        provider: [],
        model: [],
      },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }
  })

  it('preserves lane object reference when lane data is unchanged', () => {
    const openaiBefore = __testing.state.snapshot!.dimensions.vendor[1]
    const delta: LiveStreamDelta = {
      summary: { total: 3, success: 3, failure: 0 },
      changed_lanes: {
        credential: [],
        vendor: [lane('anthropic', 2), lane('openai', 1, [tile('r1')])],
        provider: [],
        model: [],
      },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
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
        credential: [],
        vendor: [lane('anthropic', 1), lane('openai', 2, [tile('r1'), tile('r2')])],
        provider: [],
        model: [],
      },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    __testing.mergeDelta(delta)
    expect(__testing.state.snapshot!.dimensions.vendor[1]).toBe(openaiBefore)
    expect(__testing.state.snapshot!.dimensions.vendor[1].requests).toHaveLength(2)
  })

  it('drops trimmed tiles while preserving surviving tile identity', () => {
    const survivor = tile('r2')
    __testing.state.snapshot!.dimensions.vendor[1].requests = [tile('r1'), survivor]
    
    const delta: LiveStreamDelta = {
      summary: { total: 2, success: 2, failure: 0 },
      changed_lanes: {
        credential: [],
        vendor: [lane('anthropic', 1), lane('openai', 1, [tile('r2')])],
        provider: [],
        model: [],
      },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }

    __testing.mergeDelta(delta)

    const after = __testing.state.snapshot!.dimensions.vendor[1]
    expect(after.requests).toHaveLength(1)
    expect(after.requests[0].request_id).toBe('r2')
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
  it('follows the server-provided lane order and preserves lane identity', () => {
    const openaiBefore = __testing.state.snapshot!.dimensions.vendor[1]
    const delta: LiveStreamDelta = {
      summary: { total: 4, success: 3, failure: 0 },
      changed_lanes: {
        credential: [],
        vendor: [
          lane('google', 1, [tile('r3')]),
          lane('anthropic', 2),
          lane('openai', 1, [tile('r1')]),
        ],
        provider: [],
        model: [],
      },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    __testing.mergeDelta(delta)
    const after = __testing.state.snapshot!.dimensions.vendor
    expect(after.map((l) => l.id)).toEqual(['google', 'anthropic', 'openai'])
    expect(after[2]).toBe(openaiBefore)
  })

  it('merges credential lanes from an SSE delta', () => {
    const delta: LiveStreamDelta = {
      summary: { total: 1, success: 1, failure: 0 },
      changed_lanes: {
        credential: [{
          ...lane('credential-42', 1, [tile('r-credential')]),
          dimension: 'credential',
          name: 'Primary credential',
        }],
        vendor: [],
        provider: [],
        model: [],
      },
      dimension_legends: {
        credential: [{ key: 'credential-42', name: 'Primary credential', count: 1 }],
        vendor: [],
        provider: [],
        model: [],
      },
      status_legends: [],
    }

    __testing.mergeDelta(delta)

    expect(__testing.state.snapshot!.dimensions.credential[0]?.name).toBe('Primary credential')
    expect(__testing.state.snapshot!.dimension_legends.credential[0]?.key).toBe('credential-42')
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
      dimensions: { credential: [], vendor: [lane('openai', 1, [tile('r1')])], provider: [], model: [] },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }
  })

  it('removes lanes omitted by an authoritative snapshot', () => {
    __testing.mergeSnapshotFromServer({
      summary: { total: 0, success: 0, failure: 0 },
      dimensions: { credential: [], vendor: [], provider: [], model: [] },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    })
    expect(__testing.state.snapshot!.dimensions.vendor.map((l) => l.id)).toEqual([])
  })

  it('updates summary from incoming without dropping lanes', () => {
    __testing.mergeSnapshotFromServer({
      summary: { total: 2, success: 2, failure: 0 },
      dimensions: { credential: [], vendor: [lane('openai', 2, [tile('r1'), tile('r2')])], provider: [], model: [] },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
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
        dimensions: { credential: [], vendor: [lane('openai', 2, [tile('r1'), tile('r2')])], provider: [], model: [] },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
      },
    })
    expect(__testing.state.requests.map((request) => request.request_id)).toEqual(['r1'])
  })

  it('applies an idle marker delta to the affected lane', () => {
    const delta: LiveStreamDelta = {
      summary: { total: 2, success: 2, failure: 0 },
      changed_lanes: {
        credential: [],
        vendor: [lane('anthropic', 2), lane('openai', 1, [tile('r1')])],
        provider: [],
        model: [],
      },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    __testing.handleEnvelope({ type: 'idle_marker', ts: '2026-07-14T00:01:00Z', delta })
    const anthropic = __testing.state.snapshot!.dimensions.vendor.find((lane) => lane.id === 'anthropic')
    expect(anthropic?.stats.total).toBe(2)
  })

  // ---------------------------------------------------------------------------
  // snapshot_refresh timestamp guard (latest_request_ts versioning)
  // ---------------------------------------------------------------------------

  it('accepts snapshot_refresh when latest_request_ts > maxSeenTs', () => {
    __testing.resetStream()
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: '2026-07-14T00:01:00Z',
      snapshot: {
        summary: { total: 1, success: 1, failure: 0 },
        dimensions: { credential: [], vendor: [lane('openai', 1, [tile('r1')])], provider: [], model: [] },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
        latest_request_ts: '2026-07-14T12:00:02Z',
      },
    })
    expect(__testing.state.snapshot!.summary.total).toBe(1)
    expect(__testing.maxSeenTs()).toBe('2026-07-14T12:00:02Z')
  })

  it('rejects stale snapshot_refresh when latest_request_ts <= maxSeenTs', () => {
    __testing.resetStream()
    // First snapshot raises maxSeenTs to 12:00:02
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: '2026-07-14T00:01:00Z',
      snapshot: {
        summary: { total: 1, success: 1, failure: 0 },
        dimensions: { credential: [], vendor: [lane('openai', 1, [tile('r1')])], provider: [], model: [] },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
        latest_request_ts: '2026-07-14T12:00:02Z',
      },
    })
    // Second snapshot with OLDER ts should be rejected
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: '2026-07-14T00:02:00Z',
      snapshot: {
        summary: { total: 999, success: 999, failure: 0 },
        dimensions: { credential: [], vendor: [lane('openai', 999, [tile('r1')])], provider: [], model: [] },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
        latest_request_ts: '2026-07-14T12:00:01Z',
      },
    })
    // Snapshot should still show the FIRST snapshot's data (rejected the stale one)
    expect(__testing.state.snapshot!.summary.total).toBe(1)
    expect(__testing.maxSeenTs()).toBe('2026-07-14T12:00:02Z')
  })

  it('delta with higher tiles raises maxSeenTs, rejecting subsequent stale snapshot', () => {
    __testing.resetStream()
    // First snapshot with latest_request_ts = 12:00:01
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: '2026-07-14T00:01:00Z',
      snapshot: {
        summary: { total: 1, success: 1, failure: 0 },
        dimensions: { credential: [], vendor: [lane('openai', 1, [tile('r1')])], provider: [], model: [] },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
        latest_request_ts: '2026-07-14T12:00:01Z',
      },
    })
    // Delta with a tile at 12:00:03 raises maxSeenTs
    __testing.handleEnvelope({
      type: 'request',
      ts: '2026-07-14T00:01:30Z',
      delta: {
        summary: { total: 2, success: 2, failure: 0 },
        changed_lanes: {
          credential: [],
          vendor: [lane('openai', 2, [{ ...tile('r2'), timestamp: '2026-07-14T12:00:03Z' }])],
          provider: [],
          model: [],
        },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
      },
    })
    expect(__testing.maxSeenTs()).toBe('2026-07-14T12:00:03Z')
    // Stale snapshot at 12:00:02 should be rejected
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: '2026-07-14T00:02:00Z',
      snapshot: {
        summary: { total: 999, success: 999, failure: 0 },
        dimensions: { credential: [], vendor: [lane('openai', 999, [tile('r1')])], provider: [], model: [] },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
        latest_request_ts: '2026-07-14T12:00:02Z',
      },
    })
    // total remains 2 (the delta's value, not the stale snapshot's 999)
    expect(__testing.state.snapshot!.summary.total).toBe(2)
    expect(__testing.maxSeenTs()).toBe('2026-07-14T12:00:03Z')
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

// ---------------------------------------------------------------------------
// 2026-07-26: Deterministic property tests — the "no jump" invariants.
//
// These are the regression guards for the swim-lane flicker/jump root cause.
// They assert that rendering order depends ONLY on server state, never on
// message arrival history. A seeded PRNG keeps runs reproducible.
// ---------------------------------------------------------------------------

function tsAt(seconds: number): string {
  // Fixed base so timestamps are comparable and deterministic.
  return new Date(Date.UTC(2026, 6, 26, 12, 0, 0) + seconds * 1000).toISOString()
}

describe('mergeTilesById — deterministic ordering (no-jump invariants)', () => {
  it('tile order after a delta equals (ts ASC, id ASC) of server state', () => {
    // Regression for defect 1: previously new tiles were appended to the tail
    // regardless of sort, so rendering drifted from server truth until a
    // full snapshot force-corrected it ("page flip").
    const existing: LiveStreamTile[] = [
      { ...tile('a'), timestamp: tsAt(10) },
      { ...tile('b'), timestamp: tsAt(20) },
    ]
    // incoming from backend is ASC (oldest first) after lane builder normalization
    const incoming: LiveStreamTile[] = [
      { ...tile('c'), timestamp: tsAt(30) },
      { ...tile('a'), timestamp: tsAt(10) },
      { ...tile('b'), timestamp: tsAt(20) },
    ]
    __testing.mergeTilesById(existing, incoming)

    const ids = existing.map((t) => t.request_id)
    // a(10) < b(20) < c(30): oldest left, newest right
    expect(ids).toEqual(['a', 'b', 'c'])
  })

  it('normalizes newest-first refresh snapshots to oldest-left order', () => {
    const newestFirst = [
      { ...tile('newest'), timestamp: tsAt(30) },
      { ...tile('middle'), timestamp: tsAt(20) },
      { ...tile('oldest'), timestamp: tsAt(10) },
    ]

    __testing.resetStream()
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: tsAt(30),
      snapshot: {
        summary: { total: 3, success: 3, failure: 0 },
        dimensions: {
          credential: [],
          vendor: [lane('openai', 3, [...newestFirst])],
          provider: [lane('openai', 3, [...newestFirst])],
          model: [lane('gpt-4o', 3, [...newestFirst])],
        },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
        latest_request_ts: tsAt(30),
      },
    })

    for (const dimension of ['vendor', 'provider', 'model'] as const) {
      const requests = __testing.state.snapshot!.dimensions[dimension][0].requests
      expect(requests.map((request) => request.request_id)).toEqual([
        'oldest',
        'middle',
        'newest',
      ])
      const detailRequests = __testing.state.snapshot!.dimensions[dimension][0].requests
      expect(detailRequests.map((request) => request.request_id)).toEqual([
        'oldest',
        'middle',
        'newest',
      ])
    }
  })

  it('normalizes a newly created delta lane before appending it', () => {
    const newestFirst = [
      { ...tile('newest'), timestamp: tsAt(30) },
      { ...tile('oldest'), timestamp: tsAt(10) },
    ]

    __testing.resetStream()
    __testing.handleEnvelope({
      type: 'request',
      ts: tsAt(30),
      delta: {
        summary: { total: 2, success: 2, failure: 0 },
        changed_lanes: {
          credential: [],
          vendor: [lane('new-vendor', 2, newestFirst)],
          provider: [],
          model: [],
        },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
      },
    })

    expect(__testing.state.snapshot!.dimensions.vendor[0].requests.map((request) => request.request_id))
      .toEqual(['oldest', 'newest'])
  })

  it('truncates to 20 keeping the NEWEST tiles when over capacity', () => {
    const incoming: LiveStreamTile[] = []
    // 25 tiles, oldest first in the authoritative sense
    for (let i = 0; i < 25; i++) incoming.push({ ...tile(`r${i}`), timestamp: tsAt(i) })
    // backend delivers ASC (oldest first); reverse simulates legacy DESC wire order
    incoming.reverse()
    const existing: LiveStreamTile[] = []
    __testing.mergeTilesById(existing, incoming)
    expect(existing).toHaveLength(20)
    // tiles 5..24 survive (the 20 newest)
    expect(existing[0].request_id).toBe('r5')
    expect(existing[19].request_id).toBe('r24')
  })

  it('a new request always appears in the visible window (defect 1 regression)', () => {
    // Pre-existing lane already at capacity (20 tiles)
    const existing: LiveStreamTile[] = Array.from({ length: 20 }, (_, i) => ({
      ...tile(`old${i}`),
      timestamp: tsAt(i),
    }))
    // backend delta may arrive out of order; mergeTilesById normalizes ASC
    const incoming: LiveStreamTile[] = [
      { ...tile('NEW'), timestamp: tsAt(100) },
      ...existing
        .map((t) => ({ ...t }))
        .sort((a, b) => b.timestamp.localeCompare(a.timestamp)),
    ]
    __testing.mergeTilesById(existing, incoming)
    // The newest tile MUST be present after merge (it was being dropped before)
    expect(existing.some((t) => t.request_id === 'NEW')).toBe(true)
    expect(existing[existing.length - 1].request_id).toBe('NEW')
  })

  it('equal-ts snapshots are idempotent: re-applying produces identical order', () => {
    // Regression for defect 2: previously `<=` rejected equal-ts snapshots, so
    // reconciliation never ran. Now `<` allows equal-ts to apply, and combined
    // with deterministic sort the result is identical → no visual change.
    function apply(tiles: LiveStreamTile[]) {
      __testing.resetStream()
      __testing.handleEnvelope({
        type: 'snapshot_refresh',
        ts: '2026-07-26T00:00:00Z',
        snapshot: {
          summary: { total: tiles.length, success: tiles.length, failure: 0 },
          dimensions: { credential: [], vendor: [lane('openai', tiles.length, tiles)], provider: [], model: [] },
          dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
          status_legends: [],
          latest_request_ts: tiles[tiles.length - 1]?.timestamp || '',
        },
      })
      return __testing.state.snapshot!.dimensions.vendor[0].requests.map((t) => t.request_id).join(',')
    }
    const tiles = [
      { ...tile('a'), timestamp: tsAt(10) },
      { ...tile('b'), timestamp: tsAt(20) },
      { ...tile('c'), timestamp: tsAt(30) },
    ]
    const first = apply(tiles)
    const second = apply(tiles) // same ts — would have been rejected under old `<=`
    expect(second).toBe(first)
  })

  it('normalizes an authoritative tile set independently of wire order', () => {
    const tiles = [
      { ...tile('r1'), timestamp: tsAt(1) },
      { ...tile('r2'), timestamp: tsAt(2) },
      { ...tile('r3'), timestamp: tsAt(3) },
    ]
    const apply = (incoming: LiveStreamTile[]) => {
      const existing: LiveStreamTile[] = []
      __testing.mergeTilesById(existing, incoming)
      return existing.map((item) => item.request_id).join('>')
    }

    expect(apply(tiles)).toBe('r1>r2>r3')
    expect(apply([...tiles].reverse())).toBe('r1>r2>r3')
  })

  it('retains existing tiles absent from a partial delta (no whole-lane wipe)', () => {
    const existing: LiveStreamTile[] = [
      { ...tile('keep-a'), timestamp: tsAt(10) },
      { ...tile('keep-b'), timestamp: tsAt(20) },
    ]
    // Partial delta: only the new tile — must not briefly collapse the lane.
    __testing.mergeTilesById(existing, [{ ...tile('new-c'), timestamp: tsAt(30) }])
    expect(existing.map((t) => t.request_id)).toEqual(['keep-a', 'keep-b', 'new-c'])
  })

  it('defaults in_progress tiles without stage_category to routing on merge', () => {
    const existing: LiveStreamTile[] = []
    __testing.mergeTilesById(existing, [{
      ...tile('inflight'),
      status: 'in_progress',
      timestamp: tsAt(1),
    }])
    expect(existing[0].stage_category).toBe('routing')
  })
})

describe('request_lifecycle stage_category patch', () => {
  it('updates lane tile stage_category when upstream_request arrives', () => {
    __testing.resetStream()
    const inflight: LiveStreamTile = {
      ...tile('req-up'),
      status: 'in_progress',
      stage_category: 'routing',
      timestamp: tsAt(1),
    }
    __testing.handleEnvelope({
      type: 'snapshot_refresh',
      ts: tsAt(1),
      snapshot: {
        summary: { total: 1, success: 0, failure: 0, in_progress: 1 },
        dimensions: { credential: [], vendor: [lane('openai', 1, [inflight])], provider: [], model: [] },
        dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
        status_legends: [],
        latest_request_ts: tsAt(1),
      },
    })
    __testing.handleEnvelope({
      type: 'request_lifecycle',
      ts: tsAt(2),
      action: [{
        request_id: 'req-up',
        seq: 1,
        action: 'upstream_request',
        ts: tsAt(2),
        stage: 'upstream',
        stage_category: 'llm',
      }],
    } as LiveStreamEnvelope)
    const patched = __testing.state.snapshot!.dimensions.vendor[0].requests[0]
    expect(patched.stage_category).toBe('llm')
  })
})
