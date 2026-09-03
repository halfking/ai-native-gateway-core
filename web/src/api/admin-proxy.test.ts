import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  getProxySubscriptions,
  createProxySubscription,
  refreshProxySubscription,
  deleteProxySubscription,
  getProxyNodes,
  createProxyNode,
  healthCheckProxyNode,
  deleteProxyNode,
  getProxyStatus,
} from './proxy'

// Mock fetch globally
const originalFetch = globalThis.fetch

beforeEach(() => {
  globalThis.fetch = vi.fn()
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.clearAllMocks()
})

describe('proxy API client', () => {
  const subscriptionItem = {
    id: 1,
    name: 'test-sub',
    subscribe_url: 'https://example.com/sub',
    status: 'active',
    node_count: 5,
    last_fetch_at: null,
  }
  const nodeItem = {
    id: 1,
    name: 'node1',
    protocol: 'http',
    server: '127.0.0.1',
    port: 8080,
    dialable: true,
    status: 'active',
    response_time_ms: 12,
    consecutive_failures: 0,
  }

  it('getProxySubscriptions unwraps {items,total} envelope and calls GET /api/proxy/subscriptions', async () => {
    const mockItems = [subscriptionItem]
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ items: mockItems, total: mockItems.length }),
    })

    const result = await getProxySubscriptions()
    expect(result).toEqual(mockItems)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/subscriptions',
      expect.objectContaining({ method: 'GET' })
    )
  })

  it('getProxySubscriptions tolerates bare-array responses (legacy)', async () => {
    const mockItems = [subscriptionItem]
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockItems),
    })

    const result = await getProxySubscriptions()
    expect(result).toEqual(mockItems)
  })

  it('filters list items missing fields or field types required by the page', async () => {
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({
        items: [
          null,
          { ...subscriptionItem, name: 42 },
          { ...subscriptionItem, subscribe_url: null },
          { ...subscriptionItem, node_count: '5' },
          subscriptionItem,
        ],
      }),
    })

    await expect(getProxySubscriptions()).resolves.toEqual([subscriptionItem])

    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({
        items: [
          { ...nodeItem, server: null },
          { ...nodeItem, port: '8080' },
          { ...nodeItem, dialable: 1 },
          { ...nodeItem, consecutive_failures: null },
          nodeItem,
        ],
      }),
    })

    await expect(getProxyNodes()).resolves.toEqual([nodeItem])

    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ total: 0 }),
    })
    await expect(getProxySubscriptions()).resolves.toEqual([])
  })

  it('surfaces malformed successful response bodies as parse errors', async () => {
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => 'not-json',
    })

    await expect(getProxySubscriptions()).rejects.toBeInstanceOf(SyntaxError)
  })
  it('createProxySubscription calls POST /api/proxy/subscriptions', async () => {
    const mockResp = { id: 2, name: 'new-sub', node_count: 0 }
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockResp),
    })

    const result = await createProxySubscription({ name: 'new-sub', subscribe_url: 'https://example.com' })
    expect(result).toEqual(mockResp)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/subscriptions',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ name: 'new-sub', subscribe_url: 'https://example.com' }),
      })
    )
  })

  it('refreshProxySubscription calls POST /api/proxy/subscriptions/:id/refresh', async () => {
    const mockResp = { ok: true, node_count: 3 }
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockResp),
    })

    const result = await refreshProxySubscription(1)
    expect(result).toEqual(mockResp)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/subscriptions/1/refresh',
      expect.objectContaining({ method: 'POST' })
    )
  })

  it('deleteProxySubscription calls DELETE /api/proxy/subscriptions/:id', async () => {
    const mockResp = { ok: true }
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockResp),
    })

    const result = await deleteProxySubscription(1)
    expect(result).toEqual(mockResp)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/subscriptions/1',
      expect.objectContaining({ method: 'DELETE' })
    )
  })

  it('getProxyNodes with query params unwraps {items,total} envelope', async () => {
    const mockItems = [nodeItem]
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ items: mockItems, total: mockItems.length }),
    })

    const result = await getProxyNodes(2, true)
    expect(result).toEqual(mockItems)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/nodes?subscription_id=2&dialable=true',
      expect.objectContaining({ method: 'GET' })
    )
  })

  it('createProxyNode calls POST /api/proxy/nodes', async () => {
    const mockResp = { id: 5, name: 'bridge', protocol: 'http', dialable: true }
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockResp),
    })

    const result = await createProxyNode({
      subscription_id: 1,
      name: 'bridge',
      protocol: 'http',
      server: '127.0.0.1',
      port: 7897,
    })
    expect(result).toEqual(mockResp)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/nodes',
      expect.objectContaining({ method: 'POST' })
    )
  })

  it('healthCheckProxyNode calls POST /api/proxy/nodes/:id/health-check', async () => {
    const mockResp = { ok: true, status_code: 204, response_time_ms: 120 }
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockResp),
    })

    const result = await healthCheckProxyNode(5)
    expect(result).toEqual(mockResp)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/nodes/5/health-check',
      expect.objectContaining({ method: 'POST' })
    )
  })

  it('deleteProxyNode calls DELETE /api/proxy/nodes/:id', async () => {
    const mockResp = { ok: true }
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockResp),
    })

    const result = await deleteProxyNode(5)
    expect(result).toEqual(mockResp)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/nodes/5',
      expect.objectContaining({ method: 'DELETE' })
    )
  })

  it('getProxyStatus calls GET /api/proxy/status', async () => {
    const mockStatus = {
      subscription_count: 2,
      node_count: 10,
      dialable_count: 8,
      selected_node: { id: 3, name: 'bridge', protocol: 'http' },
    }
    ;(globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(mockStatus),
    })

    const result = await getProxyStatus()
    expect(result).toEqual(mockStatus)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/status',
      expect.objectContaining({ method: 'GET' })
    )
  })
})
