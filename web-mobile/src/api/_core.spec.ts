import { describe, expect, it, vi } from 'vitest'
import { buildHeaders, ApiError, req, setToken, setUserInfo, MOBILE_BASE } from './_core'

describe('_core（17 §5 请求层契约）', () => {
  it('cookie 优先：userInfo 已水合时不带内存 Bearer', () => {
    setUserInfo({ username: 'op' })
    setToken('jwt-abc')
    const h = buildHeaders('GET', false)
    expect(h['Authorization']).toBeUndefined()
  })

  it('无 userInfo 时补 Authorization: Bearer', () => {
    setUserInfo(null)
    setToken('jwt-abc')
    const h = buildHeaders('GET', false)
    expect(h['Authorization']).toBe('Bearer jwt-abc')
  })

  it('body 存在才带 Content-Type（WAF 兼容，桌面端同款）', () => {
    expect(buildHeaders('POST', true)['Content-Type']).toBe('application/json')
    expect(buildHeaders('GET', false)['Content-Type']).toBeUndefined()
  })

  it('401 清凭据并跳 /m/login?redirect=', async () => {
    setUserInfo({ username: 'op' })
    const replaceSpy = vi.fn()
    vi.stubGlobal('location', {
      pathname: '/m/keys',
      search: '',
      replace: replaceSpy,
    })
    const fetchMock = vi.fn(async () => new Response('{"error":"unauthorized"}', { status: 401 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(req('GET', '/api/keys')).rejects.toBeInstanceOf(ApiError)
    expect(replaceSpy).toHaveBeenCalledWith(expect.stringContaining(`${MOBILE_BASE}/login?redirect=`))
    vi.unstubAllGlobals()
  })

  it('公开端点 401 不跳登录（/api/auth/me 水合探测）', async () => {
    const replaceSpy = vi.fn()
    vi.stubGlobal('location', {
      pathname: '/m',
      search: '',
      replace: replaceSpy,
    })
    const fetchMock = vi.fn(async () => new Response('{"error":"no"}', { status: 401 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(req('GET', '/api/auth/me')).rejects.toBeInstanceOf(ApiError)
    expect(replaceSpy).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })
})

