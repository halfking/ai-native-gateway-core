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
})
