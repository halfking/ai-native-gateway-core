// authCaptchaWire.spec.ts — 钉死 /api/auth/token **请求体**上的验证码字段契约。
//
// 为什么钉在 api 层而不是 LoginView.spec：
// LoginView.spec 把整个 `@/api/auth` 模块 mock 掉了，只断言「store.login 被
// 怎么调用」。真正的 bug 恰恰发生在 store 之后 —— 请求体被组错时，
// 视图层所有断言照样全绿（2026-10-11 实测：12/12 PASS，模拟器上 100% 登录失败）。
// 所以这里断言**序列化之后真正上线的那个 body**。
//
// 契约来源：后端 admin/auth_captcha.go verifyLoginCaptcha
//   - captcha_id 与 captcha_code **同时非空** → 视为「提交了验证码」，必须校验；
//   - 两者**同时为空** → 视为「没提交」，未达失败阈值时放行；
//   - 只带 id 不带答案 → captcha_invalid（永远失败）。
import { beforeEach, afterEach, describe, expect, it } from 'vitest'
import { login } from '@/api/auth'
import { setTransport, resetTransport, type TransportRequest } from '@/api/transport'

let sent: TransportRequest | null = null

function captureBody(body: unknown): Record<string, unknown> {
  return JSON.parse((body as string)) as Record<string, unknown>
}

beforeEach(() => {
  sent = null
  resetTransport()
  setTransport(async (r) => {
    sent = r
    return {
      status: 200,
      statusText: 'OK',
      ok: true,
      text: async () =>
        JSON.stringify({
          access_token: 't',
          token_type: 'Bearer',
          expires_at: '2026-10-12T00:00:00Z',
          user: { id: 1, tenant_id: 'default', username: 'admin', role: 'super_admin' },
        }),
    }
  })
})

afterEach(() => {
  resetTransport()
})

describe('POST /api/auth/token 验证码字段', () => {
  it('用户没填验证码时，id 与 code 必须**同时**留空（走服务端「没提交」分支）', async () => {
    await login('admin', 'secret', { captchaId: 'cap-1', captchaCode: '' })
    const body = captureBody(sent!.body)
    // 页面 onMounted 总是先取一张码 ⇒ captchaId 必然非空。
    // 只带 id 不带 code 会被服务端判 captcha_invalid，自适应路径彻底走不通。
    expect(body.captcha_id).toBe('')
    expect(body.captcha_code).toBe('')
  })

  it('全空白（只有空格）的验证码同样视为「没填」，不得只带 id', async () => {
    await login('admin', 'secret', { captchaId: 'cap-1', captchaCode: '   ' })
    const body = captureBody(sent!.body)
    expect(body.captcha_id).toBe('')
    expect(body.captcha_code).toBe('')
  })

  it('完全没传 captcha 选项时两个字段都是空串（而不是 undefined）', async () => {
    await login('admin', 'secret')
    const body = captureBody(sent!.body)
    expect(body.captcha_id).toBe('')
    expect(body.captcha_code).toBe('')
  })

  it('填了验证码时 id 与 code 成对上传，答案按 trim 归一', async () => {
    await login('admin', 'secret', { captchaId: 'cap-1', captchaCode: ' AB39 ' })
    const body = captureBody(sent!.body)
    expect(body.captcha_id).toBe('cap-1')
    expect(body.captcha_code).toBe('AB39')
  })

  it('登录主体字段不受影响（防止修 captcha 时把用户名密码弄坏）', async () => {
    await login('admin', 'secret', { rememberMe: true, captchaId: 'cap-1', captchaCode: '' })
    const body = captureBody(sent!.body)
    expect(body.username).toBe('admin')
    expect(body.password).toBe('secret')
    expect(body.remember_me).toBe(true)
  })
})