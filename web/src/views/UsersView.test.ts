import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UsersView from './UsersView.vue'

const getUsersMock = vi.fn()
const getTenantsAdminMock = vi.fn()
const createUserMock = vi.fn()
const resetUserPasswordMock = vi.fn()
let readOnlyMode = true
let tenantAdminMode = true

vi.mock('../api', () => ({
  getUsers: (...args: any[]) => getUsersMock(...args),
  createUser: (...args: any[]) => createUserMock(...args),
  updateUser: vi.fn(),
  deleteUser: vi.fn(),
  resetUserPassword: (...args: any[]) => resetUserPasswordMock(...args),
  getTenantsAdmin: (...args: any[]) => getTenantsAdminMock(...args),
}))

vi.mock('../store', () => ({
  store: {
    userInfo: { id: 7, tenant_id: 'tenant-a', username: 'tenantadmin', display_name: 'Tenant Admin', email: '', role: 'tenant_admin', enabled: true },
  },
  isReadOnlyMode: () => readOnlyMode,
  isTenantAdmin: () => tenantAdminMode,
}))

describe('UsersView tenant admin permissions', () => {
  beforeEach(() => {
    readOnlyMode = true
    tenantAdminMode = true
    getUsersMock.mockReset()
    getTenantsAdminMock.mockReset()
    createUserMock.mockReset()
    resetUserPasswordMock.mockReset()

    getUsersMock.mockResolvedValue([
      {
        id: 11,
        tenant_id: 'tenant-a',
        username: 'alice',
        display_name: 'Alice',
        email: 'alice@example.com',
        role: 'tenant_admin',
        enabled: true,
        last_login_at: null,
        created_at: '2026-06-27T00:00:00Z',
      },
    ])
    getTenantsAdminMock.mockResolvedValue([{ code: 'tenant-a', name: 'Tenant A', status: 'active' }])
  })

  it('shows reset password but hides create and delete in read-only tenant mode', async () => {
    const wrapper = mount(UsersView)
    await flushPromises()

    expect(wrapper.text()).toContain('当前仅开放查看和重置本租户用户密码')
    expect(wrapper.text()).toContain('重置密码')
    expect(wrapper.findAll('button').some((node) => node.text().includes('删除'))).toBe(false)
    expect(wrapper.findAll('button').some((node) => node.text().includes('新建用户'))).toBe(false)
  })

  it('shows live password policy feedback in reset dialog', async () => {
    const wrapper = mount(UsersView)
    await flushPromises()

    const resetButton = wrapper.findAll('button').find((node) => node.text().includes('重置密码'))
    expect(resetButton?.exists()).toBe(true)
    await resetButton!.trigger('click')
    await flushPromises()

    const passwordInput = wrapper.find('input[type="password"]')
    await passwordInput.setValue('lowercase123')
    await flushPromises()

    expect(wrapper.text()).toContain('○ 包含大写字母')
    const confirmFields = wrapper.findAll('input[type="password"]')
    await confirmFields[1].setValue('Mismatch123')
    await flushPromises()

    expect(wrapper.text()).toContain('✕ 两次输入的新密码不一致')
    const confirmButton = wrapper.findAll('button').find((node) => node.text().includes('确认'))
    expect((confirmButton!.element as HTMLButtonElement).disabled).toBe(true)
  })

  it('blocks create until password confirmation matches', async () => {
    readOnlyMode = false
    tenantAdminMode = false
    createUserMock.mockResolvedValue({ id: 99 })

    const wrapper = mount(UsersView)
    await flushPromises()

    const openButton = wrapper.findAll('button').find((node) => node.text().includes('新建用户'))
    expect(openButton?.exists()).toBe(true)
    await openButton!.trigger('click')
    await flushPromises()

    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('bob')
    await inputs[1].setValue('ValidPass123')
    await inputs[2].setValue('Mismatch123')
    await flushPromises()

    expect(wrapper.text()).toContain('✕ 两次输入的密码不一致')
    const createButton = wrapper.findAll('button').find((node) => node.text().includes('创建'))
    expect((createButton!.element as HTMLButtonElement).disabled).toBe(true)
    expect(createUserMock).not.toHaveBeenCalled()
  })
})
