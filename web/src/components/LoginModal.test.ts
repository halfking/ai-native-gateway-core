import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import LoginModal from './LoginModal.vue'

const loginMock = vi.fn()
const getAuthMeMock = vi.fn()
const pushMock = vi.fn()
const replaceMock = vi.fn()

vi.mock('../api', () => ({
  login: (...args: any[]) => loginMock(...args),
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
    push: pushMock,
    replace: replaceMock,
  }),
}))

vi.mock('../composables/useLoginModal', () => ({
  useLoginModal: () => ({ closeLogin: vi.fn() }),
}))

describe('LoginModal first-login hint', () => {
  it('renders first-login password reset hint', () => {
    const wrapper = mount(LoginModal, {
      props: { modelValue: true },
      global: { stubs: { Teleport: true } },
    })

    expect(wrapper.text()).toContain('首次登录或管理员重置密码后，需要先修改密码才能继续使用')
  })
})
