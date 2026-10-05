import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  EpochError,
  bindAuthContext,
  bumpSessionEpoch,
  currentSessionEpoch,
  isEpochError,
  req,
  resetUnauthorizedForTests,
  setUnauthorizedHandler,
} from './client'

// jsdom 提供真实 fetch？不提供——全部 mock。
const fetchMock = vi.fn()

describe('api/client（17 §5 契约）', () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    resetUnauthorizedForTests()
    bindAuthContext({ getUserInfo: () => null, getBearer: () => '', isAuthenticated: () => false })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('带 body 时设置 Content-Type；cookie-first 无 Bearer', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ ok: 1 }))
    await req('POST', '/api/keys', { a: 1 })
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect((init.headers as Record<string, string>)['Authorization']).toBeUndefined()
    expect(init.credentials).toBe('same-origin')
  })

  it('无 userInfo（旧 sk 路径）才补 Authorization Bearer', async () => {
    bindAuthContext({ getUserInfo: () => null, getBearer: () => 'sk-test', isAuthenticated: () => true })
    fetchMock.mockResolvedValue(jsonResponse({ ok: 1 }))
    await req('GET', '/api/keys')
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((init.headers as Record<string, string>)['Authorization']).toBe('Bearer sk-test')
  })

  it('有 userInfo 时 cookie 承载，不再附内存 Bearer（防遮蔽新 cookie）', async () => {
    bindAuthContext({ getUserInfo: () => fakeUser(), getBearer: () => 'jwt-x', isAuthenticated: () => true })
    fetchMock.mockResolvedValue(jsonResponse({ ok: 1 }))
    await req('GET', '/api/keys')
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((init.headers as Record<string, string>)['Authorization']).toBeUndefined()
  })

  it('401：admin 端点触发单飞跳转 + 抛 ApiError；auth 探测端点不跳', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    // 每次调用生成新 Response（body 只能读一次）
    fetchMock.mockImplementation(() => new Response(JSON.stringify({ error: 'expired' }), { status: 401 }))
    await expect(req('GET', '/api/keys')).rejects.toThrow(ApiError)
    expect(handler).toHaveBeenCalledTimes(1)
    // 第二次 401 不再重复跳（闩锁）
    await expect(req('GET', '/api/admin/dashboard/board?days=7')).rejects.toThrow(ApiError)
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('401 on /api/auth/me 不触发跳转（水合探测的正常路径）', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    fetchMock.mockResolvedValue(new Response('{"error":"no session"}', { status: 401 }))
    await expect(req('GET', '/api/auth/me')).rejects.toThrow(ApiError)
    expect(handler).not.toHaveBeenCalled()
  })

  it('sessionEpoch 变更 → 响应丢弃为 EpochError（17 §4-R2 验收用例 7）', async () => {
    const epochAtStart = currentSessionEpoch()
    let resolveFetch: (r: Response) => void = () => {}
    fetchMock.mockReturnValue(
      new Promise<Response>((resolve) => {
        resolveFetch = resolve
      }),
    )
    const pending = req('GET', '/api/keys')
    bumpSessionEpoch() // 另一标签页登出
    resolveFetch(jsonResponse({ ok: 1 }))
    await expect(pending).rejects.toSatisfy(isEpochError)
    expect(epochAtStart).toBeLessThan(currentSessionEpoch())
    expect(new EpochError().name).toBe('EpochError')
  })

  it('非 2xx 错误信息从 JSON error 字段提取', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: { message: 'bad request' } }), { status: 400 }))
    const err = await req('GET', '/api/keys').catch((e: unknown) => e)
    expect((err as ApiError).status).toBe(400)
    expect((err as ApiError).detail).toBe('bad request')
  })

  // ↓ 移植自 feat/web-mobile-hyper 的 _core.spec.ts 语义（断网归一化）

  it('fetch reject（断网）→ ApiError status 0，供视图区分网络故障与服务端 500', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
    const err = await req('GET', '/api/keys').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    // status 0 = 用户侧。HyperList.vue:56 的 status===0 分支靠它命中。
    expect((err as ApiError).status).toBe(0)
  })

  it('AbortError 原样抛出，不计入错误态（对照组：取消≠故障）', async () => {
    const abort = new Error('aborted')
    abort.name = 'AbortError'
    fetchMock.mockRejectedValue(abort)
    const err = await req('GET', '/api/keys').catch((e: unknown) => e)
    expect(err).toBe(abort)
    expect(err).not.toBeInstanceOf(ApiError)
  })
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function fakeUser() {
  return {
    id: 1,
    tenant_id: 'default',
    username: 'admin',
    display_name: 'Admin',
    email: 'a@b.c',
    role: 'super_admin',
    enabled: true,
  }
}
