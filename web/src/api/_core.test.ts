import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, headers, isAbortError, req, resetAuthRedirectForTests } from './_core'
import { clearAll, store } from '../store'

const originalFetch = globalThis.fetch
const originalLocation = window.location.href

afterEach(() => {
  globalThis.fetch = originalFetch
  resetAuthRedirectForTests()
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

  it('keeps a request-detail waterfall URL when an admin endpoint 401s', async () => {
    store.userInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true }
    const assigned: string[] = []
    const originalLocation = window.location
    let href = 'http://localhost/request-detail/abc?mode=request&tab=waterfall'
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        pathname: '/request-detail/abc',
        search: '?mode=request&tab=waterfall',
        get href() { return href },
        set href(next: string) {
          href = next
          assigned.push(next)
        },
      },
    })
    // ★ mockImplementation 而不是 mockResolvedValue：Response 的 body **只能读一次**。
    //   复用同一个实例时，第二次调用（这里是会话探针 /api/auth/me）拿到的是
    //   「body 已消费」的 Response ⇒ r.text() 抛 TypeError ⇒ 401 被降级成
    //   非 ApiError ⇒ 探针判「探不成」⇒ 不踢登录。
    //   真实浏览器每次 fetch 都拿到全新 Response ⇒ 必须逐次新建。
    globalThis.fetch = vi.fn().mockImplementation(async () => new Response(
      JSON.stringify({ error: { detail: 'authentication required' } }),
      { status: 401 },
    ))

    try {
      await expect(req('GET', '/api/admin/dispatch/waterfall/request/abc')).rejects.toMatchObject({ status: 401 })
      expect(assigned).toEqual([
        '/?login=1&redirect=' + encodeURIComponent('/request-detail/abc?mode=request&tab=waterfall'),
      ])
      expect(store.userInfo).toBeNull()
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: originalLocation })
    }
  })

  // ── 401 会话裁决（2026-10-24 审计）──────────────────────────────────────
  //
  // 后端把「端点没注册 / feature flag 关」与「你没带 token」回成**逐字节相同**
  // 的 401 ⇒ 401 **不等于**会话过期。踢登录前必须探一次 /api/auth/me。
  // 仓内先例：web-mobile/src/api/client.ts 的 sessionReallyDead
  // （2026-10-12 实测事故 → 2026-10-13 加探针 → 2026-10-22 修探针口径）。
  // 桌面端此前只同步了一半（ApiError.sessionAlive 从未落地，踢登录判定没改）。

  it('does NOT bounce to login when the session probe succeeds but the endpoint 401s', async () => {
    // 会话有效（探针 200），但这个端点自己拒（401）。
    // 真实场景：feature flag 关闭 ⇒ 端点未注册 ⇒ 后端回 401，
    // 而同会话的 /api/auth/me 仍是 200（移动端 2026-10-12 实测）。
    store.userInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true }
    const assigned: string[] = []
    const originalLocation = window.location
    let href = 'http://localhost/live'
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        pathname: '/live',
        search: '',
        get href() { return href },
        set href(next: string) {
          href = next
          assigned.push(next)
        },
      },
    })
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/auth/me') {
        return new Response(JSON.stringify({
          access_token: 'tok',
          expires_at: 1,
          user: { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true },
        }), { status: 200 })
      }
      return new Response(JSON.stringify({ error: { detail: 'authentication required' } }), { status: 401 })
    })

    try {
      await expect(req('GET', '/api/admin/billing/reconciliation')).rejects.toMatchObject({
        status: 401,
        sessionAlive: true,
      })
      // 探针真的打过（否则这条断言是空转）
      expect(globalThis.fetch).toHaveBeenCalledWith('/api/auth/me', expect.anything())
      // 关键行为：会话有效 ⇒ **不踢登录**、**不清登录态**
      expect(assigned).toEqual([])
      expect(store.userInfo?.id).toBe(1)
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: originalLocation })
    }
  })

  it('still bounces to login when the session probe also 401s', async () => {
    // 端点 401 + 探针也 401 ⇒ 会话确实死了 ⇒ 照旧踢（保留吊销后的强制登出）。
    store.userInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true }
    const assigned: string[] = []
    const originalLocation = window.location
    let href = 'http://localhost/live'
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        pathname: '/live',
        search: '',
        get href() { return href },
        set href(next: string) {
          href = next
          assigned.push(next)
        },
      },
    })
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/auth/me') {
        // 探针：明确的认证失败
        return new Response(JSON.stringify({ error: { detail: 'authentication required' } }), { status: 401 })
      }
      // 端点：另一个 401（正文刻意不同，便于区分两次响应）
      return new Response(JSON.stringify({ error: { detail: 'endpoint refused' } }), { status: 401 })
    })

    try {
      await expect(req('GET', '/api/admin/whatever')).rejects.toMatchObject({
        status: 401,
        detail: 'endpoint refused',
        sessionAlive: false,
      })
      expect(assigned).toEqual(['/?login=1&redirect=' + encodeURIComponent('/live')])
      expect(store.userInfo).toBeNull()
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: originalLocation })
    }
  })

  it('does NOT bounce to login when the session probe cannot be completed', async () => {
    // 探针探不成（5xx / 断网 / 非 JSON）⇒ **没有任何证据**说会话死了 ⇒ 不踢。
    // 移动端 2026-10-22 实测：把「探针抛异常」判成死亡 ⇒ 维护页 ⇄ 登录页死循环。
    store.userInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'a', email: '', role: 'super_admin', enabled: true }
    const assigned: string[] = []
    const originalLocation = window.location
    let href = 'http://localhost/live'
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        pathname: '/live',
        search: '',
        get href() { return href },
        set href(next: string) {
          href = next
          assigned.push(next)
        },
      },
    })
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/auth/me') {
        // 探针：服务端 500 ⇒ 不是认证失败信号
        return new Response(JSON.stringify({ error: { detail: 'boom' } }), { status: 500 })
      }
      return new Response(JSON.stringify({ error: { detail: 'authentication required' } }), { status: 401 })
    })

    try {
      const err = await req('GET', '/api/admin/whatever').then(() => null, (e: unknown) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).toBe(401)
      // 「没探成」不可当成 false
      expect((err as ApiError).sessionAlive).toBeUndefined()
      expect(assigned).toEqual([])
      expect(store.userInfo?.id).toBe(1)
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: originalLocation })
    }
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
