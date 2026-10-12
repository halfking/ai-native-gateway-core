import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import LoginView from './LoginView.vue'
import { ApiError } from '@/api/client'

const mocks = vi.hoisted(() => ({
  replace: vi.fn(),
  login: vi.fn(),
  fetchCaptcha: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: mocks.replace }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ login: mocks.login }),
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    fetchCaptcha: mocks.fetchCaptcha,
  }
})

vi.mock('@/i18n', () => ({
  t: (key: string) => key,
}))

/** 一张"正常"的验证码响应：required=false（未达失败阈值）。 */
function challenge(overrides: Record<string, unknown> = {}) {
  return {
    captcha_id: 'cap-1',
    image: 'data:image/png;base64,AAAA',
    expires_at: '2026-10-10T00:00:00Z',
    required: false,
    ttl_seconds: 300,
    ...overrides,
  }
}

/** 填好用户名/密码，让 submit 能真正跑起来。 */
async function fillCredentials(wrapper: ReturnType<typeof mount>) {
  await wrapper.find('#mobile-login-username').setValue('admin')
  await wrapper.find('#mobile-login-password').setValue('secret')
}

beforeEach(() => {
  mocks.replace.mockReset()
  mocks.login.mockReset()
  mocks.fetchCaptcha.mockReset()
  mocks.fetchCaptcha.mockResolvedValue(challenge())
  mocks.login.mockResolvedValue({})
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('LoginView mobile keyboard behavior', () => {
  it('does not auto-focus the username field on a touch device', async () => {
    const focused: HTMLElement[] = []
    vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement) {
      focused.push(this)
    })
    const wrapper = mount(LoginView)

    await flushPromises()

    expect(focused.some((element) => element.matches('input[name="username"]'))).toBe(false)
    wrapper.unmount()
  })
})

describe('LoginView 保持登录', () => {
  it('把「保持登录」的勾选状态透传给 auth store', async () => {
    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)

    // 默认不勾选。
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(mocks.login).toHaveBeenLastCalledWith(
      'admin',
      'secret',
      expect.objectContaining({ rememberMe: false }),
    )

    await wrapper.find('#mobile-login-remember').setValue(true)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(mocks.login).toHaveBeenLastCalledWith(
      'admin',
      'secret',
      expect.objectContaining({ rememberMe: true }),
    )
    wrapper.unmount()
  })
})

describe('LoginView 验证码', () => {
  it('挂载时拉一张验证码并渲染出来', async () => {
    const wrapper = mount(LoginView)
    await flushPromises()

    expect(mocks.fetchCaptcha).toHaveBeenCalledTimes(1)
    const img = wrapper.find('#mobile-login-captcha-image')
    expect(img.exists()).toBe(true)
    expect(img.attributes('src')).toBe('data:image/png;base64,AAAA')
    wrapper.unmount()
  })

  // 未达后端阈值时验证码是**可选**的：留空也必须能把登录提交出去。
  // 把这里写成「必须填」会让正常用户每次登录都多点一次。
  it('required=false 时留空验证码照样提交', async () => {
    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mocks.login).toHaveBeenCalledTimes(1)
    expect(mocks.login).toHaveBeenLastCalledWith(
      'admin',
      'secret',
      expect.objectContaining({ captchaId: 'cap-1', captchaCode: '' }),
    )
    wrapper.unmount()
  })

  it('required=true 且留空时本地拦下，不再打后端', async () => {
    mocks.fetchCaptcha.mockResolvedValue(challenge({ required: true }))
    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mocks.login).not.toHaveBeenCalled()
    // 本地拦下后必须换一张图，否则用户对着同一张图重试。
    expect(mocks.fetchCaptcha).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('login.captchaRequired')
    wrapper.unmount()
  })

  it('required=true 时提交已填的验证码', async () => {
    mocks.fetchCaptcha.mockResolvedValue(challenge({ required: true }))
    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)
    await wrapper.find('#mobile-login-captcha').setValue('AB39')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mocks.login).toHaveBeenLastCalledWith(
      'admin',
      'secret',
      expect.objectContaining({ captchaId: 'cap-1', captchaCode: 'AB39' }),
    )
    wrapper.unmount()
  })

  // 验证码是一次性的：后端在失败响应里作废了它，UI 必须换图，
  // 否则用户对着一个死验证码反复输，第二次必然也失败。
  it('收到 captcha_invalid 后换一张新图并给出专属文案', async () => {
    mocks.login.mockRejectedValue(new ApiError(400, 'captcha answer is incorrect or expired', 'captcha_invalid'))
    mocks.fetchCaptcha
      .mockResolvedValueOnce(challenge({ required: false }))
      .mockResolvedValueOnce(challenge({ required: true, captcha_id: 'cap-2' }))

    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)
    await wrapper.find('#mobile-login-captcha').setValue('ZZZZ')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mocks.fetchCaptcha).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('login.captchaInvalid')
    // 服务端已经把 required 抬起来了，UI 必须跟着变。
    expect(wrapper.find('#mobile-login-captcha').attributes('aria-required')).toBe('true')
    wrapper.unmount()
  })

  it('password 字段带「跳过密码管理器」的 iOS 标签（避免 Keychain 弹窗）', async () => {
    // 2026-10-11 加：iOS 26 模拟器 WKWebView 在 password autocomplete 上
    // 会触发「输入 iPhone 密码」系统级 Keychain 弹窗，整张登录页被遮住。
    // data-lpignore / data-1p-ignore / data-form-type 是 1Password / iCloud
    // Keychain / Dashlane 通用的「跳过此字段」标记，保留 autocomplete 让
    // 真机的「saved password」列表仍能正常显示。
    const wrapper = mount(LoginView)
    await flushPromises()
    const pw = wrapper.find('#mobile-login-password')
    expect(pw.attributes('autocomplete')).toBe('current-password')
    expect(pw.attributes('data-lpignore')).toBe('true')
    expect(pw.attributes('data-1p-ignore')).toBe('true')
    expect(pw.attributes('data-form-type')).toBe('other')
    wrapper.unmount()
  })

  it('captcha_required 走专用文案，而不是「用户名或密码错误」', async () => {
    mocks.login.mockRejectedValue(new ApiError(400, 'captcha required', 'captcha_required'))
    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('login.captchaRequired')
    expect(text).not.toContain('login.failed')
    wrapper.unmount()
  })

  it('密码错误时显示 login.failed（验证码文案不串台）', async () => {
    mocks.login.mockRejectedValue(new ApiError(401, 'Invalid credentials'))
    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('login.failed')
    expect(text).not.toContain('login.captchaRequired')
    expect(text).not.toContain('login.captchaInvalid')
    wrapper.unmount()
  })

  // 验证码拉不到时**不能**把登录堵死：此时后端还没判定强制。
  it('验证码加载失败不阻断登录', async () => {
    mocks.fetchCaptcha.mockRejectedValue(new ApiError(503, 'captcha service busy'))
    const wrapper = mount(LoginView)
    await flushPromises()
    await fillCredentials(wrapper)

    expect(wrapper.find('#mobile-login-captcha-image').exists()).toBe(false)
    expect(wrapper.find('#mobile-login-captcha-reload').exists()).toBe(true)

    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(mocks.login).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('点击验证码图片可换一张', async () => {
    const wrapper = mount(LoginView)
    await flushPromises()

    await wrapper.find('#mobile-login-captcha-image').trigger('click')
    await flushPromises()

    expect(mocks.fetchCaptcha).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})