import { describe, it, expect } from 'vitest'
import { applyLiveRequestToBoard, isTerminalBoardRequest, isWithinBoardDays } from './boardLiveMerge'
import type { BoardPayload } from '../api/board'
import type { LiveRequest } from './liveStreamStore'

function emptyBoard(): BoardPayload {
  return {
    summary: { total_requests: 10, success_rate: 0.9, avg_latency_ms: 100, total_tokens: 1000 },
    pies: { clients: [], virtual_ips: [], identity_hashes: [], models: [], errors: [], tenants: [], providers: [] },
    trends: [{ bucket: '2026-07-14T16:00:00.000Z', requests: 10, tokens: 1000, credits: 0, cost_usd: 0.01 }],
    background_tasks: {},
    selfcheck: {},
    days: 7,
    source: 'postgresql_baseline',
  }
}

describe('boardLiveMerge', () => {
  it('ignores in-progress requests', () => {
    const req: LiveRequest = { ts: new Date().toISOString(), status: 'in_progress', request_id: 'r1' }
    expect(isTerminalBoardRequest(req)).toBe(false)
    const out = applyLiveRequestToBoard(emptyBoard(), req, 7)
    expect(out.summary?.total_requests).toBe(10)
  })

  it('increments summary and pies for success', () => {
    const ts = new Date().toISOString()
    const req: LiveRequest = {
      ts,
      request_id: 'r2',
      status: 'success',
      model: 'gpt-4o',
      provider_code: 'openai',
      tenant_id: 'default',
      prompt_tokens: 10,
      completion_tokens: 20,
      total_tokens: 30,
      cost_usd: 0.002,
      latency_ms: 200,
    }
    const out = applyLiveRequestToBoard(emptyBoard(), req, 7)
    expect(out.summary?.total_requests).toBe(11)
    expect(out.summary?.total_tokens).toBe(1030)
    expect(out.pies?.models?.find((m) => m.key === 'gpt-4o')?.requests).toBe(1)
    expect(out.source).toBe('live_sse_delta')
  })

  it('skips requests outside selected days window', () => {
    const req: LiveRequest = {
      ts: '2020-01-01T00:00:00Z',
      request_id: 'old',
      status: 'success',
    }
    expect(isWithinBoardDays(req.ts, 7)).toBe(false)
    const out = applyLiveRequestToBoard(emptyBoard(), req, 7)
    expect(out.summary?.total_requests).toBe(10)
  })
})
