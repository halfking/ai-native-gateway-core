// liveStreamStore unit tests — 2026-07-14 regression guards for
// the "swim lane flicker" + "missing probe after no-candidates"
// fixes. These tests exercise the pure helpers (pushOrQueue /
// mergeDelta) without spinning up a real SSE connection.

import { describe, it, expect, beforeEach } from 'vitest'
import {
  __testing,
  acquireLiveStream,
  resetStream,
  type LiveRequest,
  type LiveStreamDelta,
  type LiveStreamLane,
} from './liveStreamStore'

// These tests poke the singleton store directly via __testing so we
// can mutate state without spinning up an EventSource.
const store = __testing

describe('liveStreamStore.pushOrQueue', () => {
  beforeEach(() => {
    resetStream()
  })

  it('appends a fresh request_id to the tail', () => {
    store.handleEnvelope({
      type: 'request',
      ts: new Date().toISOString(),
      request: {
        request_id: 'r1',
        ts: '2026-07-14T00:00:00Z',
        model: 'gpt-4o',
        provider_code: 'openai',
        status: 'in_progress',
      },
    })
    store.handleEnvelope({
      type: 'request',
      ts: new Date().toISOString(),
      request: {
        request_id: 'r2',
        ts: '2026-07-14T00:00:01Z',
        model: 'gpt-4o',
        provider_code: 'openai',
        status: 'in_progress',
      },
    })
    const ids = store.state.requests.map((r) => r.request_id)
    expect(ids).toEqual(['r1', 'r2'])
  })

  // 2026-07-14: same request_id twice used to overwrite in place,
  // producing the "blue tile silently turns green" flicker. The fix
  // splice-then-pushes so the Vue TransitionGroup runs the
  // leave+enter animation sequence. The store still de-dups, but
  // now the new tile always lands at the tail.
  it('moves an updated request_id to the tail (animate out + in)', () => {
    store.handleEnvelope({
      type: 'request',
      ts: new Date().toISOString(),
      request: {
        request_id: 'r1',
        ts: '2026-07-14T00:00:00Z',
        model: 'gpt-4o',
        provider_code: 'openai',
        status: 'in_progress',
      },
    })
    store.handleEnvelope({
      type: 'request',
      ts: new Date().toISOString(),
      request: {
        request_id: 'r2',
        ts: '2026-07-14T00:00:01Z',
        model: 'gpt-4o',
        provider_code: 'openai',
        status: 'in_progress',
      },
    })
    // Simulate r1 transitioning from in_progress -> success.
    store.handleEnvelope({
      type: 'request',
      ts: new Date().toISOString(),
      request: {
        request_id: 'r1',
        ts: '2026-07-14T00:00:02Z',
        model: 'gpt-4o',
        provider_code: 'openai',
        status: 'success',
      },
    })
    const ids = store.state.requests.map((r) => r.request_id)
    // r1 must now appear LAST (after r2), not at index 0 where it
    // used to be. The idIndex still de-dups correctly.
    expect(ids).toEqual(['r2', 'r1'])
    expect(store.state.requests[1]?.status).toBe('success')
  })

  it('does not de-dup idle_marker by request_id', () => {
    store.handleEnvelope({ type: 'idle_marker', ts: '2026-07-14T00:00:00Z' })
    store.handleEnvelope({ type: 'idle_marker', ts: '2026-07-14T00:01:00Z' })
    expect(store.state.requests.length).toBe(2)
    expect(store.state.requests.every((r) => r.type === 'idle_marker')).toBe(true)
  })
})

describe('liveStreamStore.mergeDelta', () => {
  beforeEach(() => {
    resetStream()
    // Seed an initial snapshot so mergeDelta takes the merge branch
    // (rather than the bootstrap branch).
    store.handleEnvelope({
      type: 'initial_data',
      ts: new Date().toISOString(),
      requests: [],
      snapshot: {
        summary: { total: 0, success: 0, failure: 0 },
        detail_dimensions: { vendor: [], provider: [], model: [] },
        dimensions: {
          vendor: [
            {
              id: 'openai',
              name: 'openai',
              dimension: 'vendor',
              requests: [],
              stats: { total: 5, success: 4, failure: 1 },
              isOthers: false,
            },
          ],
          provider: [],
          model: [],
        },
        dimension_legends: {
          vendor: [{ key: 'openai', name: 'openai', count: 5 }],
          provider: [],
          model: [],
        },
        status_legends: [],
      },
    })
  })

  // 2026-07-14: the previous implementation replaced the whole
  // dimensions[dim] array on every delta, which destroyed every
  // lane's component identity and re-triggered Vue's enter/leave
  // animations even when only one lane's stats moved. The fix
  // merges by lane.id so unchanged lanes keep their slot.
  it('mutates an existing lane in place (no array replacement)', () => {
    const before = store.state.snapshot!.dimensions.vendor
    expect(before).toHaveLength(1)
    const beforeRef = before[0]

    const delta: LiveStreamDelta = {
      summary: { total: 6, success: 5, failure: 1 },
      changed_lanes: {
        vendor: [
          {
            id: 'openai',
            name: 'openai',
            dimension: 'vendor',
            requests: [],
            stats: { total: 6, success: 5, failure: 1 },
            isOthers: false,
          },
        ],
        provider: [],
        model: [],
      },
      dimension_legends: {
        vendor: [{ key: 'openai', name: 'openai', count: 6 }],
        provider: [],
        model: [],
      },
      status_legends: [],
    }
    store.mergeDelta(delta)
    const after = store.state.snapshot!.dimensions.vendor
    expect(after).toHaveLength(1)
    // The exact same lane object — Vue should not see a remount.
    expect(after[0]).toBe(beforeRef)
    expect(after[0]?.stats.total).toBe(6)
  })

  it('appends a brand-new lane to the tail', () => {
    const delta: LiveStreamDelta = {
      summary: { total: 8, success: 6, failure: 2 },
      changed_lanes: {
        vendor: [
          {
            id: 'anthropic',
            name: 'anthropic',
            dimension: 'vendor',
            requests: [],
            stats: { total: 2, success: 1, failure: 1 },
            isOthers: false,
          },
        ],
        provider: [],
        model: [],
      },
      dimension_legends: {
        vendor: [{ key: 'anthropic', name: 'anthropic', count: 2 }],
        provider: [],
        model: [],
      },
      status_legends: [],
    }
    store.mergeDelta(delta)
    const lanes = store.state.snapshot!.dimensions.vendor as LiveStreamLane[]
    expect(lanes.map((l) => l.id)).toEqual(['openai', 'anthropic'])
  })

  it('preserves order of pre-existing lanes when a new one arrives', () => {
    // Seed openai, anthropic in that order via two deltas.
    store.mergeDelta({
      summary: { total: 7, success: 5, failure: 2 },
      changed_lanes: {
        vendor: [
          {
            id: 'anthropic',
            name: 'anthropic',
            dimension: 'vendor',
            requests: [],
            stats: { total: 2, success: 1, failure: 1 },
            isOthers: false,
          },
        ],
        provider: [],
        model: [],
      },
      dimension_legends: {
        vendor: [{ key: 'anthropic', name: 'anthropic', count: 2 }],
        provider: [],
        model: [],
      },
      status_legends: [],
    })
    expect(
      store.state.snapshot!.dimensions.vendor.map((l: LiveStreamLane) => l.id)
    ).toEqual(['openai', 'anthropic'])

    // New delta adds google. openai + anthropic must not change order.
    store.mergeDelta({
      summary: { total: 8, success: 6, failure: 2 },
      changed_lanes: {
        vendor: [
          {
            id: 'google',
            name: 'google',
            dimension: 'vendor',
            requests: [],
            stats: { total: 1, success: 1, failure: 0 },
            isOthers: false,
          },
        ],
        provider: [],
        model: [],
      },
      dimension_legends: {
        vendor: [{ key: 'google', name: 'google', count: 1 }],
        provider: [],
        model: [],
      },
      status_legends: [],
    })
    expect(
      store.state.snapshot!.dimensions.vendor.map((l: LiveStreamLane) => l.id)
    ).toEqual(['openai', 'anthropic', 'google'])
  })
})

describe('liveStreamStore EventSource wiring', () => {
  it('acquireLiveStream returns a release function', () => {
    const release = acquireLiveStream()
    expect(typeof release).toBe('function')
    release()
    // EventSource refcount is internal; we just assert the shape.
    expect(store.refCount()).toBe(0)
  })
})

// Touch a LiveRequest import so the linter doesn't flag the type
// import as unused when only used in the helper signatures above.
const _typeProbe: LiveRequest | undefined = undefined
void _typeProbe