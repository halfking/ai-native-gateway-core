import { afterEach, describe, expect, it, vi } from 'vitest'
import { envelopeCode, req, resetAuthRedirectForTests } from './_core'

/**
 * 2026-10-22 12h 审计轮 · 桌面端维护信封回归门。
 *
 * ★ 审计发现（CONFIRMED）：桌面端 web/src/App.vue 的会话水合
 *   对**任何**失败都 clearJwt() ⇒ 清 userInfo + localStorage
 *   ⇒ 维护窗口 / 断网 / 5xx 都把用户踢回登录页。
 *   而桌面端此前**根本不提取**顶层信封的 code ⇒ 任何按 code 分支的 UI
 *   都认不出维护态。移动端同一处缺陷（web-mobile/src/stores/auth.ts）
 *   已在本轮修；本门把桌面端的「信封 code 能被提出来」钉住。
 *
 * ★ 判据：与移动端 web-mobile/src/api/client.ts 的 errorCode() 同一口径 ——
 *   `error.code` 优先，其次顶层 `code`。
 */

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
  resetAuthRedirectForTests()
  vi.restoreAllMocks()
})

describe('envelopeCode —— 维护/边缘信封的业务码', () => {
  it('顶层 {code:…} 信封的 code 被提出来', () => {
    expect(envelopeCode(JSON.stringify({ code: 'maintenance_mode', message: '维护中' }))).toBe(
      'maintenance_mode',
    )
    expect(
      envelopeCode(JSON.stringify({ code: 'maintain.proxy_unavailable', message: '维护中' })),
    ).toBe('maintain.proxy_unavailable')
  })

  it('error.code 优先于顶层 code', () => {
    expect(
      envelopeCode(JSON.stringify({ error: { code: 'captcha_required' }, code: 'top_level' })),
    ).toBe('captcha_required')
  })

  // 负控：判据不是恒真。
  it('负控：没有 code 的体返回空串', () => {
    expect(envelopeCode('')).toBe('')
    expect(envelopeCode('not json')).toBe('')
    expect(envelopeCode(JSON.stringify({ message: 'x' }))).toBe('')
    expect(envelopeCode(JSON.stringify({ code: 123 }))).toBe('')
  })
})

describe('req —— 失败时 ApiError 带上面那个 code', () => {
  it('503 + 维护信封 ⇒ ApiError.code 是维护码', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ code: 'maintain.upstream_unavailable', message: '维护中' }), {
        status: 503,
      }),
    )
    await expect(req('GET', '/api/auth/me')).rejects.toMatchObject({
      status: 503,
      code: 'maintain.upstream_unavailable',
    })
  })
})
