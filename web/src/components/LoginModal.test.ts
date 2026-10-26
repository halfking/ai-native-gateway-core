import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
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

// 2026-09-13: LoginModal 壳层迁移到 ui/AppModal 后，AppModal 依赖 useI18n
// （common.button.close 等词条），测试需安装 i18n 插件。
//
// ★ 2026-10-27（第 10 增量）：弹窗文案全部走语言包了 ⇒ 这里改成挂**真实的
//   zh-CN 语言包**，而不是只塞一条 common.button.close ——
//   断言的仍是「用户真正看到的字」，而且**顺带把「键缺了」变成测试失败**
//   （用桩 i18n 时 t('login.hint') 会返回键路径，缺键会静默通过）。
import zhCN from '../locales/zh-CN'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN as any },
})

describe('LoginModal first-login hint', () => {
  it('renders first-login password reset hint', () => {
    const wrapper = mount(LoginModal, {
      props: { modelValue: true },
      global: { plugins: [i18n], stubs: { Teleport: true } },
    })

    expect(wrapper.text()).toContain('首次登录或管理员重置密码后，需要先修改密码才能继续使用')
  })

  it('hosts the panel in AppModal shell (overlay + dialog roles)', () => {
    const wrapper = mount(LoginModal, {
      props: { modelValue: true },
      global: { plugins: [i18n], stubs: { Teleport: true } },
    })
    expect(wrapper.find('.app-modal').exists()).toBe(true)
    expect(wrapper.find('.modal.login-modal').exists()).toBe(true)
  })
})
