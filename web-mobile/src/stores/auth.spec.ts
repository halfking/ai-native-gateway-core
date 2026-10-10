import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAuthStore } from './auth'

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  logout: vi.fn(),
  fetchMe: vi.fn(),
  clearPersistence: vi.fn(),
}))

vi.mock('@/api/auth', () => ({
  login: mocks.login,
  logout: mocks.logout,
  fetchMe: mocks.fetchMe,
}))

vi.mock('@/hyper', () => ({
  Hyper: { clearPersistence: mocks.clearPersistence },
}))

const USER_KEY = 'llmgw_mobile_user'

function userPayload() {
  return {
    id: 7,
    tenant_id: 'default',
    username: 'admin',
    display_name: '系统管理员',
    email: 'a@b.c',
    role: 'super_admin',
    enabled: true,
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  window.localStorage.clear()
  window.sessionStorage.clear()
  mocks.login.mockReset()
  mocks.logout.mockReset()
  mocks.fetchMe.mockReset()
  mocks.clearPersistence.mockReset()
  mocks.login.mockResolvedValue({ access_token: 'tok', user: userPayload() })
  mocks.logout.mockResolvedValue(undefined)
  mocks.fetchMe.mockResolvedValue(userPayload())
})

afterEach(() => {
  window.localStorage.clear()
  window.sessionStorage.clear()
})

describe('auth store 「保持登录」的落盘位置', () => {
  // 这是整个特性的承重点：勾选与否必须落到**不同**的 storage，
  // 否则「不保持登录」就只剩一个没生效的复选框。
  it('勾选 → window.localStorage，且 window.sessionStorage 不留副本', async () => {
    const auth = useAuthStore()
    await auth.login('admin', 'secret', { rememberMe: true })

    expect(window.localStorage.getItem(USER_KEY)).toBeTruthy()
    expect(window.sessionStorage.getItem(USER_KEY)).toBeNull()
    expect(auth.rememberMe).toBe(true)
  })

  it('不勾选 → window.sessionStorage，且 window.localStorage 不留副本', async () => {
    const auth = useAuthStore()
    await auth.login('admin', 'secret', { rememberMe: false })

    expect(window.sessionStorage.getItem(USER_KEY)).toBeTruthy()
    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
    expect(auth.rememberMe).toBe(false)
  })

  // 模式切换必须**清掉另一侧**。不清的话，先勾选登录、再取消勾选登录，
  // window.localStorage 里会残留上一个账号 —— 用户明明选了"不保持登录"，
  // 下次冷启动还是自动登进去了。
  it('从勾选切到不勾选时清掉 window.localStorage 的旧值', async () => {
    const auth = useAuthStore()
    await auth.login('admin', 'secret', { rememberMe: true })
    expect(window.localStorage.getItem(USER_KEY)).toBeTruthy()

    await auth.login('admin', 'secret', { rememberMe: false })

    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
    expect(window.sessionStorage.getItem(USER_KEY)).toBeTruthy()
  })

  it('不传 rememberMe 时默认不保持登录', async () => {
    const auth = useAuthStore()
    await auth.login('admin', 'secret')

    expect(window.sessionStorage.getItem(USER_KEY)).toBeTruthy()
    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
  })

  // hydrate()（冷启动/刷新）必须沿用本次登录的模式，
  // 否则「不勾选」的会话刷新一下就把自己写进 window.localStorage 了。
  it('hydrate 沿用已有的「不勾选」模式，不升级成持久存储', async () => {
    const auth = useAuthStore()
    await auth.login('admin', 'secret', { rememberMe: false })
    window.sessionStorage.clear()

    await auth.hydrate()

    expect(mocks.fetchMe).toHaveBeenCalledTimes(1)
    expect(window.sessionStorage.getItem(USER_KEY)).toBeTruthy()
    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
  })

  // 页面刷新 = 整个 store 重建，rememberMe ref 会复位成 null。
  // 若冷启动不从「落盘在哪个 storage」把模式反推回来，
  // 刷新一次就会走 `?? true` 把用户升级成持久用户 —— 正是这个特性要避免的事。
  // （2026-10-10 实测修正：上一版判据复用同一个 store 实例，测不到这条路径。）
  it('页面刷新后重建 store，「不勾选」的会话不会被升级成持久存储', async () => {
    // 第一次会话：登录且不勾选。
    const first = useAuthStore()
    await first.login('admin', 'secret', { rememberMe: false })
    expect(window.sessionStorage.getItem(USER_KEY)).toBeTruthy()

    // 刷新：storage 不清（新 store），store 实例重建。
    setActivePinia(createPinia())
    const afterReload = useAuthStore()
    expect(afterReload.isAuthenticated).toBe(true)
    expect(afterReload.rememberMe).toBe(false)

    await afterReload.hydrate()

    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
    expect(window.sessionStorage.getItem(USER_KEY)).toBeTruthy()
  })

  it('页面刷新后重建 store，「勾选」的会话仍保持持久', async () => {
    const first = useAuthStore()
    await first.login('admin', 'secret', { rememberMe: true })

    setActivePinia(createPinia())
    const afterReload = useAuthStore()
    expect(afterReload.rememberMe).toBe(true)

    await afterReload.hydrate()

    expect(window.localStorage.getItem(USER_KEY)).toBeTruthy()
  })

  it('hydrate 在「保持登录」模式下继续写 window.localStorage', async () => {
    const auth = useAuthStore()
    await auth.login('admin', 'secret', { rememberMe: true })
    window.localStorage.clear()

    await auth.hydrate()

    expect(window.localStorage.getItem(USER_KEY)).toBeTruthy()
    expect(window.sessionStorage.getItem(USER_KEY)).toBeNull()
  })

  it('logout 清掉两侧的落盘', async () => {
    const auth = useAuthStore()
    await auth.login('admin', 'secret', { rememberMe: true })
    window.sessionStorage.setItem(USER_KEY, '{}') // 模拟两侧都脏

    await auth.logout()

    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
    expect(window.sessionStorage.getItem(USER_KEY)).toBeNull()
    expect(auth.isAuthenticated).toBe(false)
  })
})

describe('auth store 用户信息的读取', () => {
  it('冷启动时从 window.localStorage 恢复（保持登录的路径）', () => {
    window.localStorage.setItem(USER_KEY, JSON.stringify(userPayload()))
    const auth = useAuthStore()
    expect(auth.isAuthenticated).toBe(true)
    expect(auth.userInfo?.username).toBe('admin')
    expect(auth.role).toBe('super_admin')
  })

  it('冷启动时从 window.sessionStorage 恢复（会话登录的路径）', () => {
    window.sessionStorage.setItem(USER_KEY, JSON.stringify(userPayload()))
    const auth = useAuthStore()
    expect(auth.isAuthenticated).toBe(true)
    expect(auth.userInfo?.username).toBe('admin')
  })

  // 2026-10-06 修正过的坑：包裹态 {access_token, user:{...}} 必须能解出用户，
  // 否则 super_admin 会被显示成「普通用户」。改存储位置不能把这个守卫改回去。
  it('仍能解开包裹态的落盘数据', () => {
    window.localStorage.setItem(USER_KEY, JSON.stringify({ access_token: 'x', expires_at: 'y', user: userPayload() }))
    const auth = useAuthStore()
    expect(auth.userInfo?.username).toBe('admin')
    expect(auth.role).toBe('super_admin')
  })

  it('hydrate 失败时两侧落盘都清掉', async () => {
    window.localStorage.setItem(USER_KEY, JSON.stringify(userPayload()))
    window.sessionStorage.setItem(USER_KEY, JSON.stringify(userPayload()))
    mocks.fetchMe.mockRejectedValue(new Error('401'))

    const auth = useAuthStore()
    await auth.hydrate()

    expect(auth.isAuthenticated).toBe(false)
    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
    expect(window.sessionStorage.getItem(USER_KEY)).toBeNull()
    expect(auth.authHydrated).toBe(true)
  })
})
describe('origin-scoped logout and in-flight authentication', () => {
  it('offline logout stays logged out after reload and preserves node preferences', async () => {
    window.localStorage.setItem('llmgw:shell.directory:v2', 'node-directory')
    mocks.logout.mockRejectedValueOnce(new Error('offline'))
    const auth = useAuthStore()
    await auth.login('admin', 'secret', { rememberMe: true })
    await auth.logout()
    setActivePinia(createPinia())
    const restored = useAuthStore()
    await restored.hydrate()
    expect(restored.isAuthenticated).toBe(false)
    expect(mocks.fetchMe).not.toHaveBeenCalled()
    expect(window.localStorage.getItem('llmgw:shell.directory:v2')).toBe('node-directory')
    await restored.login('admin', 'secret')
    expect(window.localStorage.getItem('llmgw_mobile_logged_out')).toBeNull()
    expect(restored.isAuthenticated).toBe(true)
  })
  it('late hydration cannot restore a session after logout', async () => {
    let resolve!: (value: ReturnType<typeof userPayload>) => void
    mocks.fetchMe.mockReturnValueOnce(new Promise((done) => { resolve = done }))
    const auth = useAuthStore()
    const pending = auth.hydrate()
    await auth.logout()
    resolve(userPayload())
    await pending
    expect(auth.isAuthenticated).toBe(false)
    expect(window.localStorage.getItem(USER_KEY)).toBeNull()
  })
  it('login waits for a pending server logout before issuing a new cookie', async () => {
    let resolve!: () => void
    mocks.logout.mockReturnValueOnce(new Promise<void>((done) => { resolve = done }))
    const auth = useAuthStore()
    const exiting = auth.logout()
    const entering = auth.login('admin', 'secret')
    expect(auth.isAuthenticated).toBe(false)
    expect(mocks.login).not.toHaveBeenCalled()
    resolve()
    await exiting
    await entering
    expect(auth.isAuthenticated).toBe(true)
  })
})
