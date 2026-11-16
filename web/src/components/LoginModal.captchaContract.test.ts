import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginModal from './LoginModal.vue'

// ───────────────────────────────────────────────────────────────────────────
// LoginModal 的 captcha 契约钉测（2026-11-17）
//
// ★ 钉的是什么：后端 admin/auth_captcha.go 的 verifyLoginCaptcha 只认三种形态：
//     · captcha_id 与 captcha_code **都不带** ⇒ 放行（自适应：正常用户不必被打扰）
//     · required 却不带                ⇒ 400 captcha_required
//     · **只带其中一个**               ⇒ 400 captcha_invalid（「answer is incomplete」）
//
// ★ 为什么要有这根钉：2026-11-17 实测到线上网页登录对**所有用户**整体不可用 ——
//   前端无条件把服务端返回的 captcha_id 带上，却只在用户填了码时才带 captcha_code
//   ⇒ 不需要验证码时反而恒被后端判 captcha_invalid。
//   线上复现：带 id 不带 code → HTTP 400 captcha_invalid；两个都不带 → HTTP 200。
//
// ★ 判据是**请求体本身**（不是渲染结果）：后端看的就是这两个字段在不在。
// ───────────────────────────────────────────────────────────────────────────

const loginMock = vi.fn()
const getCaptchaMock = vi.fn()
const getAuthMeMock = vi.fn()

vi.mock('../api', () => ({
  login: (...args: any[]) => loginMock(...args),
  getCaptcha: (...args: any[]) => getCaptchaMock(...args),
  getAuthMe: (...args: any[]) => getAuthMeMock(...args),
}))

vi.mock('../store', () => ({
  setApiKey: vi.fn(),
  setJwtToken: vi.fn(),
  setUserInfo: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    currentRoute: { value: { query: {}, path: '/' } },
    push: vi.fn(),
    replace: vi.fn(),
  }),
}))

vi.mock('../composables/useLoginModal', () => ({
  useLoginModal: () => ({ closeLogin: vi.fn() }),
}))

import zhCN from '../locales/zh-CN'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN as any },
})

async function submitWith(opts: { required: boolean; code?: string }) {
  getCaptchaMock.mockResolvedValue({
    captcha_id: 'captcha-id-1',
    required: opts.required,
    image: 'data:image/png;base64,',
    expires_at: '2026-11-17T00:00:00Z',
    ttl_seconds: 300,
  })
  loginMock.mockResolvedValue({ access_token: 'jwt-1', user: { id: 41, role: 'super_admin' } })

  const wrapper = mount(LoginModal, {
    // ★ 必须**先关后开**：组件里的 watch(() => props.modelValue) 没有 immediate
    //   ⇒ 挂载时 modelValue 已是 true 的话，watcher 不会跑，
    //   checkCaptchaRequired() 也就不会被调用（真实场景里弹窗总是「关→开」）。
    props: { modelValue: false },
    global: { plugins: [i18n], stubs: { Teleport: true } },
  })
  await wrapper.setProps({ modelValue: true })
  await flushPromises()

  await wrapper.find('#login-username').setValue('admin')
  await wrapper.find('#login-password').setValue('pw')
  if (opts.code !== undefined) {
    // CaptchaInput 的输入框（CaptchaInput.vue 的 #captcha-code，v-model 绑 captchaCode）
    const captchaInput = wrapper.find('#captcha-code')
    expect(captchaInput.exists()).toBe(true)
    await captchaInput.setValue(opts.code)
  }
  await wrapper.find('form').trigger('submit')
  await flushPromises()

  return { wrapper, payload: loginMock.mock.calls[0]?.[0] }
}

describe('LoginModal captcha 契约（成对发 / 都不发）', () => {
  beforeEach(() => {
    loginMock.mockReset()
    getCaptchaMock.mockReset()
    getAuthMeMock.mockReset()
  })

  it('不需要验证码时：captcha_id 与 captcha_code **都不带**', async () => {
    const { payload } = await submitWith({ required: false })

    expect(loginMock).toHaveBeenCalledTimes(1)
    expect(payload).toBeTruthy()
    // ★ 关键：id 也不许带。后端「只带 id」⇒ 400 captcha_invalid ⇒ 登录整体不可用。
    expect('captcha_id' in payload).toBe(false)
    expect('captcha_code' in payload).toBe(false)
    expect(payload.username).toBe('admin')
  })

  it('需要验证码且用户填了码：两个字段**成对**带', async () => {
    const { payload } = await submitWith({ required: true, code: '1234' })

    expect(loginMock).toHaveBeenCalledTimes(1)
    expect(payload.captcha_id).toBe('captcha-id-1')
    expect(payload.captcha_code).toBe('1234')
  })

  it('需要验证码但用户没填码：前端拦下，**不发请求**', async () => {
    const { payload } = await submitWith({ required: true })

    expect(loginMock).not.toHaveBeenCalled()
    expect(payload).toBeUndefined()
  })
})
