import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import LoginView from './LoginView.vue'

const mocks = vi.hoisted(() => ({
  replace: vi.fn(),
  login: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: mocks.replace }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ login: mocks.login }),
}))

vi.mock('@/i18n', () => ({
  t: (key: string) => key,
}))

describe('LoginView mobile keyboard behavior', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('does not auto-focus the username field on a touch device', async () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
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
