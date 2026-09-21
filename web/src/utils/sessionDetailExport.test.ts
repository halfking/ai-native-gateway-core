import { describe, expect, it, vi, beforeEach } from 'vitest'
import { buildSessionDetailMarkdown } from './sessionDetailExport'

const {
  getSessionSnapshot,
  fetchSessionTurnsTree,
  getRequestLogs,
  getRequestLogDetail,
} = vi.hoisted(() => ({
  getSessionSnapshot: vi.fn(),
  fetchSessionTurnsTree: vi.fn(),
  getRequestLogs: vi.fn(),
  getRequestLogDetail: vi.fn(),
}))

vi.mock('../api/sessions_v2', () => ({ getSessionSnapshot }))
vi.mock('../api/sessionTurnsTree', () => ({ fetchSessionTurnsTree }))
vi.mock('../api/logs', () => ({ getRequestLogs, getRequestLogDetail }))

describe('buildSessionDetailMarkdown', () => {
  beforeEach(() => {
    getSessionSnapshot.mockReset()
    fetchSessionTurnsTree.mockReset()
    getRequestLogs.mockReset()
    getRequestLogDetail.mockReset()
  })

  it('builds markdown with turn Q&A', async () => {
    getSessionSnapshot.mockResolvedValue({
      title: 'Demo Session',
      summary: 'brief',
      total_turns: 1,
      total_cost_usd: 0.01,
    })
    fetchSessionTurnsTree.mockResolvedValue({
      session_id: 's1',
      turns: [{
        turn_number: 1,
        request_id: 'r1',
        status: 'success',
        model: 'm1',
        latency: 12,
        child_requests: [],
      }],
      count: 1,
      has_more: false,
      next_cursor: '',
    })
    getRequestLogs.mockResolvedValue({ items: [], total: 0 })
    getRequestLogDetail.mockResolvedValue({
      request_id: 'r1',
      request_body: { messages: [{ role: 'user', content: 'hello' }] },
      response_body: { choices: [{ message: { content: 'world' } }] },
    })

    const md = await buildSessionDetailMarkdown({ sessionId: 's1' })
    expect(md).toContain('# Demo Session')
    expect(md).toContain('## 会话摘要')
    expect(md).toContain('brief')
    expect(md).toContain('Turn #1')
    expect(md).toContain('hello')
    expect(md).toContain('world')
    expect(md).toContain('**success**')
  })

  it('rejects empty session id', async () => {
    await expect(buildSessionDetailMarkdown({ sessionId: '  ' })).rejects.toThrow(/sessionId/)
  })
})
