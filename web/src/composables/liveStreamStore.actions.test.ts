// liveStreamStore.actions tests — 2026-08-15 (24号 §2/§3/§7) store-layer
// guards for the request_lifecycle / child_request SSE frames.
//
// Covers: seq-ordered insertion regardless of arrival order, single vs
// aggregated batch frames, per-request cap 50, global cap 2000 (oldest
// evicted), child_request mounting into the parent index + card refresh,
// and silent ignore of unknown event types (backward compatibility).

import { describe, it, expect, beforeEach } from 'vitest'
import { __testing, getRequestActions, getRequestChildren, actionsRef, childrenRef } from './liveStreamStore'
import type { ActionEvent, LiveRequest, LiveStreamEnvelope } from './liveStreamStore'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function action(requestId: string, seq: number, opts: Partial<ActionEvent> = {}): ActionEvent {
  return {
    request_id: requestId,
    seq,
    action: 'arrive',
    ts: `2026-08-15T12:00:${String(seq % 60).padStart(2, '0')}Z`,
    ...opts,
  }
}

function makeRequest(id: string, opts: Partial<LiveRequest> = {}): LiveRequest {
  return {
    request_id: id,
    ts: '2026-08-15T00:00:00Z',
    model: 'gpt-4o',
    provider_code: 'openai',
    status: 'in_progress',
    ...opts,
  }
}

function lifecycle(payload: ActionEvent | ActionEvent[], ts = '2026-08-15T00:00:00Z') {
  __testing.handleEnvelope({ type: 'request_lifecycle', ts, action: payload } as LiveStreamEnvelope)
}

function child(parentRequestId: string, request: LiveRequest) {
  __testing.handleEnvelope({
    type: 'child_request',
    ts: request.ts,
    parent_request_id: parentRequestId,
    request,
  } as LiveStreamEnvelope)
}

function countAllActions(): number {
  let total = 0
  for (const list of __testing.state.actions.values()) total += list.length
  return total
}

beforeEach(() => {
  __testing.resetStream()
})

// ---------------------------------------------------------------------------
// request_lifecycle — ordering & frame shapes
// ---------------------------------------------------------------------------

describe('request_lifecycle frames', () => {
  it('sorts by seq regardless of arrival order (out-of-order arrival)', () => {
    lifecycle(action('r1', 3, { action: 'first_byte' }))
    lifecycle(action('r1', 1, { action: 'arrive' }))
    lifecycle(action('r1', 2, { action: 'route_resolved' }))
    const seqs = getRequestActions('r1').map((a) => a.seq)
    expect(seqs).toEqual([1, 2, 3])
    expect(getRequestActions('r1').map((a) => a.action)).toEqual(['arrive', 'route_resolved', 'first_byte'])
  })

  it('supports a single action object frame', () => {
    lifecycle(action('r1', 1))
    expect(getRequestActions('r1')).toHaveLength(1)
    expect(getRequestActions('r1')[0].request_id).toBe('r1')
  })

  it('supports an aggregated batch array frame across requests', () => {
    lifecycle([
      action('r1', 2),
      action('r2', 1),
      action('r1', 1),
    ])
    expect(getRequestActions('r1').map((a) => a.seq)).toEqual([1, 2])
    expect(getRequestActions('r2').map((a) => a.seq)).toEqual([1])
  })

  it('re-delivering the same seq replaces in place (replay dedupe)', () => {
    lifecycle(action('r1', 1, { action: 'arrive' }))
    lifecycle(action('r1', 1, { action: 'arrive', model: 'claude-3' }))
    const list = getRequestActions('r1')
    expect(list).toHaveLength(1)
    expect(list[0].model).toBe('claude-3')
    expect(__testing.actionsTotal()).toBe(1)
  })

  it('drops actions without a request_id (node-level events use other channels)', () => {
    lifecycle({ seq: 1, action: 'state_change' })
    expect(__testing.state.actions.size).toBe(0)
    expect(__testing.actionsTotal()).toBe(0)
  })

  it('exposes the timeline via actionsRef', () => {
    lifecycle(action('r1', 1))
    expect(actionsRef.value.get('r1')).toHaveLength(1)
  })
})

// ---------------------------------------------------------------------------
// Caps
// ---------------------------------------------------------------------------

describe('action timeline caps', () => {
  it('evicts the OLDEST actions past the per-request cap of 50', () => {
    for (let seq = 1; seq <= 55; seq++) {
      lifecycle(action('r1', seq))
    }
    const list = getRequestActions('r1')
    expect(list).toHaveLength(50)
    // seq 1..5 evicted; the 50 newest (6..55) survive, still sorted
    expect(list[0].seq).toBe(6)
    expect(list[49].seq).toBe(55)
    expect(__testing.actionsTotal()).toBe(50)
  })

  it('evicts globally oldest actions past the global cap of 2000', () => {
    // 45 requests × 45 actions = 2025 actions → 25 must be evicted from the
    // least recently active (first inserted) request.
    for (let r = 0; r < 45; r++) {
      for (let seq = 1; seq <= 45; seq++) {
        lifecycle(action(`r${r}`, seq))
      }
    }
    expect(countAllActions()).toBe(2000)
    expect(__testing.actionsTotal()).toBe(2000)
    // r0 was touched first: 25 of its oldest actions evicted (45 - 25 = 20 left)
    const first = getRequestActions('r0')
    expect(first).toHaveLength(20)
    expect(first[0].seq).toBe(26)
    // the most recently touched request is intact
    expect(getRequestActions('r44')).toHaveLength(45)
  })
})

// ---------------------------------------------------------------------------
// child_request
// ---------------------------------------------------------------------------

describe('child_request frames', () => {
  it('mounts the child under the parent index', () => {
    child('p1', makeRequest('c1', { requestType: 'title' }))
    const kids = getRequestChildren('p1')
    expect(kids).toHaveLength(1)
    expect(kids[0].request_id).toBe('c1')
    expect(kids[0].parentRequestId).toBe('p1')
    expect(childrenRef.value.get('p1')).toHaveLength(1)
  })

  it('collapses a child lifecycle update in place instead of duplicating', () => {
    child('p1', makeRequest('c1', { status: 'in_progress' }))
    child('p1', makeRequest('c1', { status: 'success' }))
    const kids = getRequestChildren('p1')
    expect(kids).toHaveLength(1)
    expect(kids[0].status).toBe('success')
  })

  it('updates the child request card in the flat replay buffer when present', () => {
    __testing.pushOrQueue(makeRequest('c1', { status: 'in_progress' }))
    child('p1', makeRequest('c1', { status: 'success', requestType: 'summary' }))
    const card = __testing.state.requests.find((r) => r.request_id === 'c1')
    expect(card?.status).toBe('success')
    expect(card?.requestType).toBe('summary')
  })

  it('does not inject an unknown child into the main replay buffer', () => {
    child('p1', makeRequest('c2'))
    expect(__testing.state.requests.map((r) => r.request_id)).toEqual([])
    expect(getRequestChildren('p1')).toHaveLength(1)
  })

  it('evicts the oldest children past the per-parent cap of 50', () => {
    for (let i = 0; i < 55; i++) {
      child('p1', makeRequest(`c${i}`))
    }
    const kids = getRequestChildren('p1')
    expect(kids).toHaveLength(50)
    expect(kids[0].request_id).toBe('c5')
    expect(kids[49].request_id).toBe('c54')
  })
})

// ---------------------------------------------------------------------------
// Backward compatibility
// ---------------------------------------------------------------------------

describe('unknown event types', () => {
  it('silently ignores unknown envelope types without touching state', () => {
    lifecycle(action('r1', 1))
    const before = {
      requests: __testing.state.requests.slice(),
      actionEntries: Array.from(__testing.state.actions.entries()),
      lastEventAt: __testing.state.lastEventAt,
    }
    expect(() => {
      __testing.handleEnvelope({
        type: 'future_event_type',
        ts: '2026-08-15T00:00:00Z',
      } as unknown as LiveStreamEnvelope)
    }).not.toThrow()
    expect(__testing.state.requests).toEqual(before.requests)
    expect(Array.from(__testing.state.actions.entries())).toEqual(before.actionEntries)
  })

  it('ignores request_lifecycle frames with an empty payload', () => {
    __testing.handleEnvelope({ type: 'request_lifecycle', ts: '2026-08-15T00:00:00Z' } as LiveStreamEnvelope)
    expect(__testing.state.actions.size).toBe(0)
  })

  it('ignores child_request frames missing request or parent_request_id', () => {
    __testing.handleEnvelope({ type: 'child_request', ts: '2026-08-15T00:00:00Z' } as LiveStreamEnvelope)
    __testing.handleEnvelope({
      type: 'child_request',
      ts: '2026-08-15T00:00:00Z',
      request: makeRequest('c1'),
    } as LiveStreamEnvelope)
    expect(__testing.state.children.size).toBe(0)
  })

  it('rebuilds the child index from initial_data after a reconnect', () => {
    const parent = makeRequest('parent')
    const childRequest = makeRequest('child', { parentRequestId: 'parent', requestType: 'title' })
    __testing.applyInitialData([parent, childRequest])

    expect(getRequestChildren('parent').map((r) => r.request_id)).toEqual(['child'])
    expect(__testing.childrenTotal()).toBe(1)

    // A later duplicate child_request frame must keep the same one-child index.
    child('parent', childRequest)
    expect(getRequestChildren('parent')).toHaveLength(1)
  })
})
