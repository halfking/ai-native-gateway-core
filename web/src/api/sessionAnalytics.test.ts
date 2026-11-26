import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  defaultDateRange,
  getAgentRanking,
  getModelRanking,
  getOverview,
  getProjectTasks,
  getUserDetail,
  getUserProjects,
  getUserRanking,
  getUserModelUsage,
  getUserAPIKeyUsage,
  isViewMissingError,
  isViewStaleError,
  toDateParam,
} from './sessionAnalytics'
import { ApiError, resetAuthRedirectForTests } from './_core'

/**
 * Contract lock for the Session Analytics admin client.
 *
 * These assertions are derived from the Go handlers
 * (admin/session_analytics/handlers.go) — they are not style preferences:
 *   · the server requires `start_date`/`end_date` as YYYY-MM-DD;
 *   · `owner` and `project` are PATH SEGMENTS, so an unencoded value would
 *     address a different endpoint (silently, with a 200 on the wrong path);
 *   · empty result sets serialize as `null`, not `[]`.
 */

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
  resetAuthRedirectForTests()
  vi.restoreAllMocks()
})

function captureFetch(body: unknown = {}): { calls: string[] } {
  const calls: string[] = []
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    calls.push(typeof input === 'string' ? input : String(input))
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as unknown as typeof fetch
  return { calls }
}

describe('sessionAnalytics client — URL construction', () => {
  it('overview sends the required date window and drops empty optionals', async () => {
    const { calls } = captureFetch()
    await getOverview({ start_date: '2026-11-01', end_date: '2026-11-07' })
    expect(calls[0]).toBe(
      '/api/admin/session-analytics/v2/overview?start_date=2026-11-01&end_date=2026-11-07',
    )
  })

  it('overview carries tenant_id only when provided', async () => {
    const { calls } = captureFetch()
    await getOverview({
      start_date: '2026-11-01',
      end_date: '2026-11-07',
      tenant_id: 'tenant-a',
    })
    expect(calls[0]).toContain('tenant_id=tenant-a')
  })

  it('user-ranking passes sort/pagination through', async () => {
    const { calls } = captureFetch()
    await getUserRanking({
      start_date: '2026-11-01',
      end_date: '2026-11-07',
      project: 'alpha',
      sort_by: 'cost',
      order: 'asc',
      limit: 20,
      offset: 40,
    })
    const url = calls[0]
    expect(url).toContain('project=alpha')
    expect(url).toContain('sort_by=cost')
    expect(url).toContain('order=asc')
    expect(url).toContain('limit=20')
    expect(url).toContain('offset=40')
  })

  it('owner is percent-encoded as a path segment', async () => {
    const { calls } = captureFetch()
    await getUserDetail('a/b', { start_date: '2026-11-01', end_date: '2026-11-07' })
    expect(calls[0]).toContain('/user-ranking/a%2Fb?')
  })

  it('project tasks encodes BOTH path segments', async () => {
    const { calls } = captureFetch()
    await getProjectTasks('o wner', 'p/1', {
      start_date: '2026-11-01',
      end_date: '2026-11-07',
    })
    expect(calls[0]).toContain('/user-ranking/o%20wner/projects/p%2F1/tasks?')
  })

  it('agent and model rankings hit their own endpoints', async () => {
    const { calls } = captureFetch()
    await getAgentRanking({ start_date: '2026-11-01', end_date: '2026-11-07' })
    await getModelRanking({ start_date: '2026-11-01', end_date: '2026-11-07' })
    expect(calls[0]).toContain('/api/admin/session-analytics/v2/agent-ranking?')
    expect(calls[1]).toContain('/api/admin/session-analytics/v2/model-ranking?')
  })

  it('user projects endpoint is nested under the owner', async () => {
    const { calls } = captureFetch()
    await getUserProjects('alice', { start_date: '2026-11-01', end_date: '2026-11-07' })
    expect(calls[0]).toContain('/api/admin/session-analytics/v2/user-ranking/alice/projects?')
  })

  it('all personnel detail tabs keep the selected tenant and the same natural-day window', async () => {
    const { calls } = captureFetch()
    const scope = { tenant_id: 'tenant-b', start_date: '2026-11-01', end_date: '2026-11-07' }
    await getUserDetail('alice', scope)
    await getUserProjects('alice', scope)
    await getProjectTasks('alice', 'p/1', scope)
    await getUserModelUsage('alice', scope)
    await getUserAPIKeyUsage('alice', scope)
    for (const url of calls) {
      expect(url).toContain('/api/admin/session-analytics/v2/user-ranking/alice')
      expect(url).toContain('tenant_id=tenant-b&start_date=2026-11-01&end_date=2026-11-07')
    }
  })
})

describe('sessionAnalytics client — empty payloads', () => {
  it('a null list from the server is surfaced as null, not an exception', async () => {
    globalThis.fetch = vi.fn(async () =>
      new Response(JSON.stringify({ total: 0, users: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ) as unknown as typeof fetch

    const res = await getUserRanking({
      start_date: '2026-11-01',
      end_date: '2026-11-07',
    })
    expect(res.total).toBe(0)
    // The UI layer is responsible for `?? []`; the client must not invent rows.
    expect(res.users).toBeNull()
  })
})

describe('sessionAnalytics client — error classification', () => {
  it('503 + analytics_view_stale is recognised as a refreshing state', () => {
    const e = new ApiError(503, 'stale', 'analytics_view_stale')
    expect(isViewStaleError(e)).toBe(true)
  })

  it('a generic 503 is NOT treated as a stale view', () => {
    expect(isViewStaleError(new ApiError(503, 'boom', ''))).toBe(false)
  })

  it('a non-ApiError value is not classified', () => {
    expect(isViewStaleError(new Error('network'))).toBe(false)
    expect(isViewMissingError(new Error('network'))).toBe(false)
  })

  it('analytics_view_missing is recognised separately', () => {
    expect(isViewMissingError(new ApiError(503, 'missing', 'analytics_view_missing'))).toBe(true)
  })
})

describe('sessionAnalytics client — date helpers', () => {
  it('toDateParam zero-pads month and day', () => {
    expect(toDateParam(new Date(2026, 0, 5))).toBe('2026-01-05')
    expect(toDateParam(new Date(2026, 11, 31))).toBe('2026-12-31')
  })

  it('defaultDateRange spans exactly N days inclusive', () => {
    const r = defaultDateRange(7)
    const start = new Date(r.start_date + 'T00:00:00')
    const end = new Date(r.end_date + 'T00:00:00')
    const spanDays = Math.round((end.getTime() - start.getTime()) / 86400000) + 1
    expect(spanDays).toBe(7)
  })

  it('defaultDateRange emits the YYYY-MM-DD shape the server requires', () => {
    const r = defaultDateRange(3)
    expect(r.start_date).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(r.end_date).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})
