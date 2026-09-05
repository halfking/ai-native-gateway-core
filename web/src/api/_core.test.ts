import { afterEach, describe, expect, it, vi } from 'vitest'
import { headers, isAbortError, req } from './_core'
import { clearAll, store } from '../store'

const originalFetch = globalThis.fetch
const originalLocation = window.location.href

afterEach(() => {
  globalThis.fetch = originalFetch
  clearAll()
  window.history.replaceState({}, '', originalLocation)
  vi.restoreAllMocks()
})

describe('req', () => {
  it('uses error.message from the standard admin error envelope', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: {
        message: 'a default with the same (task_type, profile, tier, tenant_id) already exists',
        type: 'admin_error',
      },
    }), { status: 409, statusText: 'Conflict' }))

    await expect(req('POST', '/api/admin/auto-route/defaults', {})).rejects.toThrow(
      'a default with the same (task_type, profile, tier, tenant_id) already exists',
    )
  })

  it('only sends Content-Type when a JSON body exists', async () => {
    expect(headers('POST')).not.toHaveProperty('Content-Type')
    expect(headers('POST', true)).toMatchObject({ 'Content-Type': 'application/json' })

    globalThis.fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    await req('POST', '/api/admin/logout-probe')
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/admin/logout-probe', expect.objectContaining({
      headers: expect.not.objectContaining({ 'Content-Type': expect.anything() }),
      body: undefined,
    }))
  })

  it('lets auth hydration 401 reach its caller without redirecting', async () => {
    store.userInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true }
    globalThis.fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { detail: 'authentication required' } }), { status: 401 }))

    await expect(req('GET', '/api/auth/me')).rejects.toMatchObject({ status: 401, detail: 'authentication required' })
    expect(store.userInfo?.id).toBe(1)
    expect(window.location.pathname).not.toBe('/login')
  })

  it('does not clear an existing session when login fails', async () => {
    store.userInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true }
    globalThis.fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { detail: 'Invalid credentials' } }), { status: 401 }))

    await expect(req('POST', '/api/auth/token', { username: 'a', password: 'bad' })).rejects.toMatchObject({ status: 401, detail: 'Invalid credentials' })
    expect(store.userInfo?.id).toBe(1)
  })

  it('accepts 205 and empty successful responses', async () => {
    globalThis.fetch = vi.fn()
      .mockResolvedValueOnce(new Response(null, { status: 205 }))
      .mockResolvedValueOnce(new Response('', { status: 200 }))

    await expect(req<void>('POST', '/api/admin/reset')).resolves.toBeUndefined()
    await expect(req<void>('GET', '/api/admin/empty')).resolves.toBeUndefined()
  })

  it('does not send the in-memory JWT when cookie auth is available', () => {
    // Mirror a fresh JWT login: the backend sets the HttpOnly session
    // cookie during /api/auth/token so once userInfo is populated the
    // browser carries auth via cookie and headers() must NOT add a
    // Bearer that would shadow a newer cookie from another tab.
    store.jwtToken = 'stale-jwt'
    store.userInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true }
    expect(headers('GET')).not.toHaveProperty('Authorization')
  })

  it('recognizes AbortError across runtimes', () => {
    expect(isAbortError(new DOMException('aborted', 'AbortError'))).toBe(true)
    expect(isAbortError(Object.assign(new Error('aborted'), { name: 'AbortError' }))).toBe(true)
    expect(isAbortError(new Error('other'))).toBe(false)
  })
})
