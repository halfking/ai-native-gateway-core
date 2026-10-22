import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  isAbortError,
  isMaintenanceMode,
  req,
  resetUnauthorizedForTests,
} from './client'
import { unwrapMe, type MeResponse } from './auth'

/**
 * 2026-10-22 12h 审计轮 · 维护态信封族的回归门。
 *
 * ★ 这一组守的是**一条族**，不是一个字面量：
 *   维护/边缘组件下发的是**顶层**信封 {"code":…,"message":…,"request_id":…}，
 *   而 code 的取值有多个来源：
 *     · 部署版边缘组件        → "maintenance_mode"
 *     · 本仓 maintain_proxy   → "maintain.upstream_unavailable" /
 *                              "maintain.proxy_unavailable" /
 *                              "maintain.not_configured"
 *                              （cmd/gateway/maintain_proxy.go）
 *
 * ★ 审计发现（CONFIRMED）：修复前 isMaintenanceMode 只认 "maintenance_mode"
 *   —— 而**本仓自己生产的维护信封用的是 maintain.*** ⇒ 换个码就静默退回
 *   「踢登录」⇒ 本次要治的「维护态误杀会话」原样复发。
 *
 * ★ 门的设计原则（沿用本仓既有纪律）：
 *   ① 判据必须**覆盖整族**，不能只覆盖一个样例；
 *   ② 必须有**负控**——证明判据不是恒真；
 *   ③ 必须能**区分「没探成」与「探死了」**。
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 503): Response {
  return new Response(JSON.stringify(body), { status })
}

describe('维护态信封族（2026-10-22 审计）', () => {
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    resetUnauthorizedForTests()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  describe('isMaintenanceMode —— 认整族，不认单字面量', () => {
    it('本仓 maintain_proxy 生产的三个码全部命中', () => {
      for (const code of [
        'maintain.upstream_unavailable',
        'maintain.proxy_unavailable',
        'maintain.not_configured',
      ]) {
        expect(isMaintenanceMode(new ApiError(503, 'maintenance', code))).toBe(true)
      }
    })

    it('部署版边缘组件的 maintenance_mode 仍然命中', () => {
      expect(isMaintenanceMode(new ApiError(503, 'maintenance', 'maintenance_mode'))).toBe(true)
    })

    // 负控：判据不是恒真。
    it('负控：普通业务错误码不命中', () => {
      expect(isMaintenanceMode(new ApiError(401, 'bad token', 'invalid_token'))).toBe(false)
      expect(isMaintenanceMode(new ApiError(400, 'bad', 'captcha_required'))).toBe(false)
    })

    it('负控：没有 code 的错误不命中', () => {
      expect(isMaintenanceMode(new ApiError(500, 'boom'))).toBe(false)
      expect(isMaintenanceMode(new Error('plain'))).toBe(false)
      expect(isMaintenanceMode(null)).toBe(false)
    })
  })

  describe('req —— 顶层信封的 code 必须被提出来', () => {
    it('顶层 {code:…} 信封的 code 进入 ApiError.code', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ code: 'maintain.proxy_unavailable', message: '维护中', request_id: '01J' }),
      )
      await expect(req('GET', '/api/auth/me')).rejects.toMatchObject({
        code: 'maintain.proxy_unavailable',
      })
    })
  })

  describe('unwrapMe —— 2xx 的信封不能被当成 UserInfo', () => {
    it('2xx + 维护信封 ⇒ 抛 ApiError（不造「假登录态」）', () => {
      const envelope = { code: 'maintain.not_configured', message: '维护中' } as unknown as MeResponse
      expect(() => unwrapMe(envelope)).toThrowError(ApiError)
      try {
        unwrapMe(envelope)
      } catch (e) {
        expect((e as ApiError).code).toBe('maintain.not_configured')
        // 503 ⇒ 上层按瞬时处理：不踢登录、不清本地态。
        expect((e as ApiError).status).toBe(503)
      }
    })

    it('包裹态与裸态仍然正常解包（不误伤真实形态）', () => {
      const user = {
        id: 41,
        tenant_id: 'default',
        username: 'admin',
        display_name: '系统管理员',
        email: '',
        role: 'super_admin',
        enabled: true,
      }
      expect(unwrapMe({ access_token: 'x', expires_at: 'y', user } as unknown as MeResponse).role).toBe(
        'super_admin',
      )
      expect(unwrapMe(user as unknown as MeResponse).username).toBe('admin')
    })
  })

  describe('「没探成」≠「探死了」', () => {
    it('AbortError 不是会话死亡的证据', () => {
      expect(isAbortError(new DOMException('aborted', 'AbortError'))).toBe(true)
    })
  })
})
