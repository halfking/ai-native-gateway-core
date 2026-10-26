import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  EpochError,
  bindAuthContext,
  bumpSessionEpoch,
  currentSessionEpoch,
  isEpochError,
  isMaintenanceMode,
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

  // ↓ 401 误杀会话（2026-10-12 实测修的缺陷）
  //
  // 后端把「端点没注册」和「你没带 token」返回成逐字节相同的 401，
  // 所以「进费用对账页 → 整页踢回登录页」的真实原因是端点未注册（flag 关），
  // 而**不是**会话过期。旧实现见 status 就跳 ⇒ 有效会话被误杀。
  // 这三条钉住新行为：会话还活着时不踢；真的死了仍踢；探针单飞。

  it('401：会话探针成功 ⇒ 不踢登录，只抛 ApiError（有效会话被误杀的回归门）', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    // 业务端点 401（flag 关 ⇒ 未注册），但 /api/auth/me 是 200
    fetchMock.mockImplementation((url: string) =>
      String(url).includes('/api/auth/me')
        ? jsonResponse({ id: 1, username: 'admin' })
        : new Response(JSON.stringify({ error: 'authentication required' }), { status: 401 }),
    )
    const err = await req('GET', '/api/admin/provider-cost-reconciliation?month=2026-10').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    // ★ 核心断言：会话还有效，不能把人踢回登录页
    expect(handler).not.toHaveBeenCalled()
    // 探针确实发出去了（否则这条断言会因为「压根没探」而假绿）
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/api/auth/me'))).toBe(true)
    // 裁决结论必须随错误传下去：文案层据此不说「登录状态已过期」
    // （2026-10-13 实测：不带这个字段时页面仍在说「请重新登录」，与事实相反）
    expect((err as ApiError).sessionAlive).toBe(true)
  })

  it('2026-10-22：顶层 {code,message} 信封（维护态形态）能提出 code 与文案', async () => {
    // 部署版边缘组件实测形态：{"code":"maintenance_mode","message":"网关维护中，请稍后重试。","request_id":"..."}
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ code: 'maintenance_mode', message: '网关维护中，请稍后重试。', request_id: '01JAZ' }), { status: 503 }),
    )
    const err = await req('GET', '/api/auth/me').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe('maintenance_mode')
    expect((err as ApiError).message).toBe('网关维护中，请稍后重试。')
    expect(isMaintenanceMode(err)).toBe(true)
  })

  it('2026-10-22：会话探针吃维护态 ⇒ 不踢登录（维护窗口不误杀好会话）', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    // 业务端点 401，探针 /api/auth/me 命中维护态信封（503 + maintenance_mode）
    fetchMock.mockImplementation((url: string) =>
      String(url).includes('/api/auth/me')
        ? new Response(JSON.stringify({ code: 'maintenance_mode', message: '网关维护中，请稍后重试。' }), { status: 503 })
        : new Response(JSON.stringify({ error: 'authentication required' }), { status: 401 }),
    )
    const err = await req('GET', '/api/keys').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    // ★ 维护窗口内不能把有效会话踢回登录页（踢了之后冷启水合又撞维护态，
    //   2026-10-22 实测形成「登录页↔维护 JSON」死循环）
    expect(handler).not.toHaveBeenCalled()
    expect((err as ApiError).sessionAlive).toBe(true)
  })

  it('401：会话探针也 401 ⇒ 仍踢登录（token 真被吊销的行为不能丢）', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    fetchMock.mockImplementation(() => new Response(JSON.stringify({ error: 'no session' }), { status: 401 }))
    const err = await req('GET', '/api/keys').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(handler).toHaveBeenCalledTimes(1)
    expect((err as ApiError).sessionAlive).toBe(false)
  })

  it('401 未探针时 sessionAlive 必须是 undefined 而不是 false（三态判据）', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    // /api/auth/me 属探针路径 ⇒ 不探 ⇒ 「没探」既不是 true 也不是 false
    fetchMock.mockResolvedValue(new Response('{"error":"no session"}', { status: 401 }))
    const err = await req('GET', '/api/auth/me').catch((e: unknown) => e)
    expect((err as ApiError).sessionAlive).toBeUndefined()
  })

  it('并发 401 只探一次会话（探针单飞，不是每个 401 都打一轮）', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    fetchMock.mockImplementation((url: string) =>
      String(url).includes('/api/auth/me')
        ? jsonResponse({ id: 1, username: 'admin' })
        : new Response(JSON.stringify({ error: 'authentication required' }), { status: 401 }),
    )
    await Promise.allSettled([
      req('GET', '/api/admin/a'),
      req('GET', '/api/admin/b'),
      req('GET', '/api/admin/c'),
    ])
    const probes = fetchMock.mock.calls.filter(([u]) => String(u).includes('/api/auth/me')).length
    expect(probes).toBe(1)
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
