import { afterEach, describe, expect, it, vi } from 'vitest'
import { envelopeCode, isMaintenanceEnvelopeCode, req, resetAuthRedirectForTests } from './_core'

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

/**
 * ★★ 2026-10-22 R107 轮（12h 审计）补的门。
 *
 * ★ 补的是什么：上一组门只钉住了「code 能被**提出来**」，
 *   **没有钉住「提出来之后怎么**决策**」** ——
 *   而那条决策（「维护态 ⇒ 不 clearJwt」）在修复前是**内联在
 *   `web/src/App.vue` 的 setup 体里** ⇒ 桌面端**没有任何测试覆盖它**。
 *   ⇒ 把内联判据删掉、或把 `if (!maintenance && !transient) clearJwt()`
 *     改回无条件 `clearJwt()`，**桌面端测试套件仍然全绿**。
 *   ⇒ 现在判据已提成导出函数 `isMaintenanceEnvelopeCode`（./api/_core），
 *     本门把它钉住，并与移动端 `isMaintenanceMode` 同口径。
 *
 * ★ 设计原则（沿用移动端那道门的纪律）：
 *   ① 判据必须**覆盖整族**，不能只覆盖一个样例；
 *   ② 必须有**负控**——证明判据不是恒真。
 */
describe('isMaintenanceEnvelopeCode —— 认整族，不认单字面量', () => {
  it('本仓 maintain_proxy 生产的三个码全部命中', () => {
    for (const code of [
      'maintain.upstream_unavailable',
      'maintain.proxy_unavailable',
      'maintain.not_configured',
    ]) {
      expect(isMaintenanceEnvelopeCode(code)).toBe(true)
    }
  })

  it('部署版边缘组件的 maintenance_mode 仍然命中', () => {
    expect(isMaintenanceEnvelopeCode('maintenance_mode')).toBe(true)
  })

  it('族内新增码自动命中（前缀判据 ⇒ 不必逐个补）', () => {
    expect(isMaintenanceEnvelopeCode('maintain.some_future_code')).toBe(true)
  })

  // 负控：判据不是恒真。
  it('负控：普通业务错误码不命中', () => {
    expect(isMaintenanceEnvelopeCode('invalid_token')).toBe(false)
    expect(isMaintenanceEnvelopeCode('captcha_required')).toBe(false)
    expect(isMaintenanceEnvelopeCode('db_not_configured')).toBe(false)
  })

  it('负控：空/非字符串不命中', () => {
    expect(isMaintenanceEnvelopeCode('')).toBe(false)
    expect(isMaintenanceEnvelopeCode(undefined)).toBe(false)
    expect(isMaintenanceEnvelopeCode(null)).toBe(false)
    expect(isMaintenanceEnvelopeCode(503)).toBe(false)
    expect(isMaintenanceEnvelopeCode({ code: 'maintain.x' })).toBe(false)
  })

  it('负控：只看前缀 maintenance 之外的不算维护态', () => {
    expect(isMaintenanceEnvelopeCode('maintenanced')).toBe(false)
    expect(isMaintenanceEnvelopeCode('not_maintain.x')).toBe(false)
  })
})
