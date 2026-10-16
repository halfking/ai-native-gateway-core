import { describe, expect, it, vi } from 'vitest'
import { listSessionAnalyticsFilterOptions } from './sessionAnalyticsFilterOptions'

// sessionAnalyticsFilterOptions.test.ts — 批 158a（2026-10-17）
// 关键安全判据：3 端点并发 + 字段映射正确 + 空数组安全。

vi.mock('./_core', () => ({
  req: vi.fn(async (method: string, path: string) => {
    if (path.startsWith('/api/admin/session-analytics/clients')) {
      return {
        clients: [
          { client_id: 'cli-001' },
          { client_id: 'cli-002' },
        ],
      }
    }
    if (path.startsWith('/api/admin/session-analytics/tasks')) {
      return {
        tasks: [
          { task_id: 'task-1' },
          { task_id: 'task-2' },
        ],
      }
    }
    if (path.startsWith('/api/admin/session-analytics/users')) {
      return {
        users: [
          { owner: 'alice' },
          { owner: 'bob' },
        ],
      }
    }
    throw new Error('unexpected path: ' + path + ' method=' + method)
  }),
}))

describe('listSessionAnalyticsFilterOptions', () => {
  it('并发调 3 端点并映射字段', async () => {
    const result = await listSessionAnalyticsFilterOptions({ hours: 24, limit: 20 })
    expect(result.clients).toEqual(['cli-001', 'cli-002'])
    expect(result.tasks).toEqual(['task-1', 'task-2'])
    expect(result.users).toEqual(['alice', 'bob'])
    expect(result.refreshedAt).toMatch(/^\d{4}-\d{2}-\d{2}T/)
  })

  it('空响应返回空数组', async () => {
    const { req } = await import('./_core')
    ;(req as any).mockResolvedValueOnce({ clients: [] })
      .mockResolvedValueOnce({ tasks: [] })
      .mockResolvedValueOnce({ users: [] })
    const result = await listSessionAnalyticsFilterOptions()
    expect(result.clients).toEqual([])
    expect(result.tasks).toEqual([])
    expect(result.users).toEqual([])
  })

  it('默认参数 hours=24 limit=20', async () => {
    const { req } = await import('./_core')
    const calls: Array<[string, string]> = []
    ;(req as any).mockImplementation(async (_m: string, p: string) => {
      calls.push([_m, p])
      return { clients: [], tasks: [], users: [] }
    })
    await listSessionAnalyticsFilterOptions()
    expect(calls[0][1]).toContain('hours=24')
    expect(calls[0][1]).toContain('limit=20')
  })
})
