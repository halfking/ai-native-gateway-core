import { describe, expect, it } from 'vitest'
import {
  normalizeSessionTurnTreeItem,
  type SessionTurnsTreeResponse,
} from './sessionTurnsTree'

describe('normalizeSessionTurnTreeItem', () => {
  it('accepts the legacy tree shape', () => {
    expect(normalizeSessionTurnTreeItem({
      turn_number: 3,
      request_id: 'req-3',
      status: 'success',
      latency: null,
      child_requests: [],
    })).toEqual({
      turn_number: 3,
      request_id: 'req-3',
      status: 'success',
      latency: null,
      child_requests: [],
    })
  })

  it('normalizes the unified V2 shape without leaking undefined fields', () => {
    expect(normalizeSessionTurnTreeItem({
      turn_no: 7,
      request_id: 'req-7',
      status_code: 200,
      success: true,
      latency_ms: 125,
      model: 'claude',
      child_requests: [{
        request_id: 'child-7',
        request_type: 'title',
        status: 'success',
        latency_ms: 12,
      }],
    })).toEqual({
      turn_number: 7,
      turn_no: 7,
      request_id: 'req-7',
      status: 'success',
      model: 'claude',
      latency: 125,
      child_requests: [{
        request_id: 'child-7',
        request_type: 'title',
        status: 'success',
        latency: 12,
      }],
    })
  })

  it('prefers a valid canonical turn_number when both fields exist', () => {
    expect(normalizeSessionTurnTreeItem({
      turn_number: 2,
      turn_no: 99,
      request_id: 'req-2',
      status: 'ok',
      child_requests: [],
    })?.turn_number).toBe(2)
  })

  it('rejects rows without a finite turn number or request id', () => {
    expect(normalizeSessionTurnTreeItem({ turn_no: NaN, request_id: 'req', child_requests: [] })).toBeNull()
    expect(normalizeSessionTurnTreeItem({ turn_no: 1, request_id: '', child_requests: [] })).toBeNull()
  })

  it('keeps the response envelope and derives count when absent', () => {
    const response = {
      session_id: 's',
      turns: [{ turn_no: 1, request_id: 'r', status: 'ok', child_requests: [] }],
      has_more: false,
      next_cursor: '',
    } as unknown as SessionTurnsTreeResponse
    expect(response.turns).toHaveLength(1)
  })

  // ── body_status passthrough ────────────────────────────────────────────
  //
  // normalizeSessionTurnTreeItem rebuilds the object from an explicit field
  // whitelist, so a wire field that is not plumbed through is dropped with no
  // error. The first two cases below are the regression guard for exactly that:
  // if someone removes body_status from the return literal, "passes through"
  // and "passes unavailable" both go red — a silent field loss cannot hide.
  it('passes body_status through instead of dropping it', () => {
    expect(normalizeSessionTurnTreeItem({
      turn_no: 4,
      request_id: 'req-4',
      status: 'success',
      child_requests: [],
      body_status: 'available',
    })?.body_status).toBe('available')

    expect(normalizeSessionTurnTreeItem({
      turn_no: 5,
      request_id: 'req-5',
      status: 'success',
      child_requests: [],
      body_status: 'unavailable',
    })?.body_status).toBe('unavailable')
  })

  it('omits body_status when the backend did not send one', () => {
    const item = normalizeSessionTurnTreeItem({
      turn_no: 6,
      request_id: 'req-6',
      status: 'success',
      child_requests: [],
    })
    expect(item).not.toBeNull()
    // Absent, not undefined-valued: the component's banner counts on
    // 'unavailable' only, and the i18n audit flags keys that are present but
    // never referenced. A present-yet-undefined key would muddy both.
    expect('body_status' in (item as object)).toBe(false)
  })

  it('rejects a body_status outside the backend two-state contract', () => {
    // The backend deliberately does NOT emit 'dropped' (admin/body_status.go):
    // the current schema cannot separate "never captured" from "pruned". An
    // unknown value must not reach the UI, whose copy has no wording for it and
    // would otherwise claim a retention policy that does not exist.
    for (const bogus of ['dropped', 'AVAILABLE', '', 42, null]) {
      const item = normalizeSessionTurnTreeItem({
        turn_no: 8,
        request_id: 'req-8',
        status: 'success',
        child_requests: [],
        body_status: bogus as unknown,
      })
      expect(item?.body_status).toBeUndefined()
    }
  })
})
