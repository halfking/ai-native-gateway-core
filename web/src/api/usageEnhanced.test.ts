import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { req } from './_core'
import { getUsageEnhancedData } from './usageEnhanced'

vi.mock('./_core', () => ({ req: vi.fn() }))

const mockedReq = vi.mocked(req)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('getUsageEnhancedData', () => {
  it('aggregates 5 endpoints concurrently and maps fields 1:1', async () => {
    mockedReq.mockImplementation(async (_m, url) => {
      if (url.startsWith('/api/usage/summary')) {
        return { days: 7, total_cost: 12.34, total_requests: 100, total_tokens: 5000 }
      }
      if (url.startsWith('/api/admin/usage/cost-trend')) {
        return { group_by: 'model', data: [{ date: '2026-10-16', cost: 1.23, requests: 10 }] }
      }
      if (url.startsWith('/api/admin/usage/period-compare')) {
        return {
          current: { cost: 12, requests: 100 },
          previous: { cost: 10, requests: 80 },
          delta_cost: 2,
          delta_cost_pct: 0.2,
          delta_requests: 20,
          delta_requests_pct: 0.25,
        }
      }
      if (url.startsWith('/api/admin/usage/cache-economics')) {
        return { cache_hit_rate: 0.42, cache_cost_saved: 1.5, total_cacheable_requests: 200 }
      }
      if (url.startsWith('/api/admin/usage/hot-api-keys')) {
        return {
          keys: [
            { key_id: 1, key_name: 'k1', total_requests: 50, total_cost: 5.5 },
            { key_id: 2, key_name: 'k2', total_requests: 30, total_cost: 3.5 },
          ],
        }
      }
      throw new Error('unexpected url ' + url)
    })

    const out = await getUsageEnhancedData({ days: 7, topN: 10 })
    expect(out.summary.days).toBe(7)
    expect(out.summary.total_cost).toBeCloseTo(12.34)
    expect(out.cost_trend.group_by).toBe('model')
    expect(out.cost_trend.data).toHaveLength(1)
    expect(out.period_compare.delta_cost_pct).toBeCloseTo(0.2)
    expect(out.cache_economics.cache_hit_rate).toBeCloseTo(0.42)
    expect(out.hot_api_keys).toHaveLength(2)
    expect(out.hot_api_keys[0].key_id).toBe(1)
    expect(typeof out.refreshedAt).toBe('string')
    expect(mockedReq).toHaveBeenCalledTimes(5)
  })

  it('defaults days=7 / topN=10 and omits query string when no params', async () => {
    mockedReq.mockResolvedValue({
      keys: [],
    } as never)
    mockedReq.mockResolvedValueOnce({ days: 7, total_cost: 0, total_requests: 0, total_tokens: 0 })
    mockedReq.mockResolvedValueOnce({ group_by: 'model', data: [] })
    mockedReq.mockResolvedValueOnce({
      current: { cost: 0, requests: 0 },
      previous: { cost: 0, requests: 0 },
      delta_cost: 0,
      delta_cost_pct: 0,
      delta_requests: 0,
      delta_requests_pct: 0,
    })
    mockedReq.mockResolvedValueOnce({ cache_hit_rate: 0, cache_cost_saved: 0, total_cacheable_requests: 0 })

    await getUsageEnhancedData()

    // days/limit/cost-trend date range 不传 ⇒ 应无 query string（除 cost-trend 必须 group_by=model + period-compare current=last_7d）
    const urls = mockedReq.mock.calls.map((c) => c[1] as string)
    expect(urls.some((u) => u.includes('days=7&limit=10'))).toBe(true)
    expect(urls.some((u) => u.includes('group_by=model'))).toBe(true)
    expect(urls.some((u) => u.includes('current=last_7d'))).toBe(true)
  })

  it('returns empty hot_api_keys list when backend omits keys', async () => {
    mockedReq.mockResolvedValueOnce({ days: 1, total_cost: 0, total_requests: 0, total_tokens: 0 })
    mockedReq.mockResolvedValueOnce({ group_by: 'model', data: [] })
    mockedReq.mockResolvedValueOnce({
      current: { cost: 0, requests: 0 },
      previous: { cost: 0, requests: 0 },
      delta_cost: 0,
      delta_cost_pct: 0,
      delta_requests: 0,
      delta_requests_pct: 0,
    })
    mockedReq.mockResolvedValueOnce({ cache_hit_rate: 0, cache_cost_saved: 0, total_cacheable_requests: 0 })
    // hot-api-keys: response with NO `keys` property
    mockedReq.mockResolvedValueOnce({} as never)

    const out = await getUsageEnhancedData()
    expect(out.hot_api_keys).toEqual([])
  })
})
