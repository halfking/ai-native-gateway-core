import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import UsersView from './UsersView.vue'
import usersMessages from '../locales/zh-CN/users'

// 详情抽屉 Teleport 到 body：每个用例后卸载，避免上个用例的抽屉残留在
// document.body 干扰后续对抽屉内容的断言。
enableAutoUnmount(afterEach)

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      users: usersMessages,
      common: {
        confirm: '确认',
        cancel: '取消',
      },
    },
  },
  missingWarn: false,
  fallbackWarn: false,
})

const getUsersMock = vi.fn()
const getTenantsAdminMock = vi.fn()
const createUserMock = vi.fn()
const resetUserPasswordMock = vi.fn()
const getUserUsageSummaryMock = vi.fn()
const getUserStatsMock = vi.fn()
let readOnlyMode = true
let tenantAdminMode = true
let superAdminMode = true

vi.mock('../api', () => ({
  getUsers: (...args: any[]) => getUsersMock(...args),
  createUser: (...args: any[]) => createUserMock(...args),
  updateUser: vi.fn(),
  deleteUser: vi.fn(),
  resetUserPassword: (...args: any[]) => resetUserPasswordMock(...args),
  getTenantsAdmin: (...args: any[]) => getTenantsAdminMock(...args),
  getUserUsageSummary: (...args: any[]) => getUserUsageSummaryMock(...args),
  getUserStats: (...args: any[]) => getUserStatsMock(...args),
}))

vi.mock('../store', () => ({
  store: {
    userInfo: { id: 7, tenant_id: 'tenant-a', username: 'tenantadmin', display_name: 'Tenant Admin', email: '', role: 'tenant_admin', enabled: true },
  },
  isReadOnlyMode: () => readOnlyMode,
  isTenantAdmin: () => tenantAdminMode,
  isSuperAdmin: () => superAdminMode,
}))

describe('UsersView tenant admin permissions', () => {
  beforeEach(() => {
    readOnlyMode = true
    tenantAdminMode = true
    superAdminMode = true
    getUsersMock.mockReset()
    getTenantsAdminMock.mockReset()
    createUserMock.mockReset()
    resetUserPasswordMock.mockReset()
    getUserUsageSummaryMock.mockReset()
    getUserStatsMock.mockReset()

    getUsersMock.mockResolvedValue([
      {
        id: 11,
        tenant_id: 'tenant-a',
        username: 'alice',
        display_name: 'Alice',
        email: 'alice@example.com',
        role: 'tenant_admin',
        enabled: true,
        must_change_password: true,
        last_login_at: null,
        created_at: '2026-06-27T00:00:00Z',
      },
    ])
    getTenantsAdminMock.mockResolvedValue([{ code: 'tenant-a', name: 'Tenant A', status: 'active' }])
    getUserUsageSummaryMock.mockResolvedValue({ days: 30, items: [] })
  })

  it('shows reset password but hides create and delete in read-only tenant mode', async () => {
    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
    await flushPromises()

    expect(wrapper.text()).toContain('当前仅开放查看和重置本租户用户密码')
    expect(wrapper.text()).toContain('重置密码')
    expect(wrapper.text()).toContain('待改密')
    expect(wrapper.findAll('button').some((node) => node.text().includes('删除'))).toBe(false)
    expect(wrapper.findAll('button').some((node) => node.text().includes('新建用户'))).toBe(false)
  })

  it('shows live password policy feedback in reset dialog', async () => {
    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
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
    const confirmButton = wrapper.findAll('button').find((node) => node.text().includes('重置') && !node.text().includes('密码'))
    expect((confirmButton!.element as HTMLButtonElement).disabled).toBe(true)
  })

  it('blocks create until password confirmation matches', async () => {
    readOnlyMode = false
    tenantAdminMode = false
    createUserMock.mockResolvedValue({ id: 99 })

    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
    await flushPromises()

    const openButton = wrapper.findAll('button').find((node) => node.text().includes('新建用户'))
    expect(openButton?.exists()).toBe(true)
    await openButton!.trigger('click')
    await flushPromises()

    // 2026-09-30：列表头部新增搜索框后，输入索引会偏移；创建弹窗内输入
    // 一律以 .modal-card 作用域选取。
    const inputs = wrapper.find('.modal-card').findAll('input')
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

describe('UsersView 统计 UI 优化轮（2026-09-30）', () => {
  beforeEach(() => {
    readOnlyMode = false
    tenantAdminMode = false
    superAdminMode = true
    getUsersMock.mockReset()
    getTenantsAdminMock.mockReset()
    getUserUsageSummaryMock.mockReset()
    getUserStatsMock.mockReset()
    getUsersMock.mockResolvedValue([
      {
        id: 11, tenant_id: 'tenant-a', username: 'alice', display_name: 'Alice',
        email: 'alice@example.com', role: 'tenant_admin', enabled: true,
        must_change_password: false, last_login_at: null, created_at: '2026-06-27T00:00:00Z',
      },
      {
        id: 12, tenant_id: 'tenant-a', username: 'bob', display_name: 'Bob',
        email: '', role: 'user', enabled: false,
        must_change_password: false, last_login_at: null, created_at: '2026-06-28T00:00:00Z',
      },
    ])
    getTenantsAdminMock.mockResolvedValue([])
    getUserUsageSummaryMock.mockResolvedValue({
      days: 30,
      items: [
        { username: 'alice', requests: 12483, tokens: 892400000, credits: 45203, last_active_at: '2026-09-30T01:00:00Z' },
      ],
    })
  })

  it('顶部统计条：总用户/活跃/管理员/禁用 + 用量汇总', async () => {
    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
    await flushPromises()

    const bar = wrapper.find('[data-testid="user-stats-bar"]')
    expect(bar.exists()).toBe(true)
    const text = bar.text()
    expect(text).toContain('2')        // 总用户
    expect(text).toContain('1')        // 活跃(alice) / 管理员 / 禁用(bob)
    expect(text).toContain('12,483')   // 近 30 天请求
    expect(getUserUsageSummaryMock).toHaveBeenCalledWith(30)
  })

  it('用量列：有用量用户显示数值，无用量显示 —', async () => {
    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
    await flushPromises()

    const rows = wrapper.findAll('tbody tr')
    expect(rows.length).toBe(2)
    expect(rows[0].text()).toContain('12,483')
    expect(rows[0].text()).toContain('892.40M')
    expect(rows[1].text()).toContain('—')
  })

  it('点击行打开详情抽屉并加载单用户画像', async () => {
    getUserStatsMock.mockResolvedValue({
      user_id: 11, username: 'alice', days: 30,
      kpi: { requests: 12483, tokens: 892400000, credits: 45203, errors: 401, error_rate: 0.032, latency_p95_ms: 12400 },
      daily: [{ date: '2026-09-30', requests: 400, success: 390, errors: 10, tokens: 29000000, credits: 1500, cost_usd: 3.2 }],
      top_models: [{ name: 'claude-opus-5', requests: 5203, tokens: 412000000, credits: 18204, cost_usd: 7.8 }],
      top_apps: [{ name: 'codex-cli', requests: 4102, tokens: 298000000, credits: 15032, cost_usd: 6.1 }],
      top_keys: [{ name: 'k1', requests: 9000, tokens: 700000000, credits: 30000, cost_usd: 12.0 }],
      key_count: 2,
      recent_requests: [
        { ts: '2026-09-30T02:07:30Z', model: 'claude-opus-5', first_chunk_ms: 15060, total_ms: 33100, credits: 412, status: 'rate_limited' },
      ],
    })
    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
    await flushPromises()

    const row = wrapper.findAll('tbody tr')[0]
    await row.trigger('click')
    await flushPromises()

    expect(getUserStatsMock).toHaveBeenCalledWith(11, 30)
    // 抽屉挂载在 body（Teleport），用 body 查找。
    const drawerText = document.body.textContent ?? ''
    expect(drawerText).toContain('alice')
    expect(drawerText).toContain('claude-opus-5')
    expect(drawerText).toContain('codex-cli')
    expect(drawerText).toContain('3.2%')
    expect(drawerText).toContain('在日志中查看全部')
    const logsLink = document.body.querySelector('a[href*="owner_user=alice"]')
    expect(logsLink?.getAttribute('href')).toContain('/request-logs?')
    const keysLink = document.body.querySelector('a[href*="tab=keys"]')
    expect(keysLink?.getAttribute('href')).toContain('/tenants/tenant-a?')
    expect(keysLink?.textContent).toContain('2')
  })

  it('非 super（tenant_admin）查看抽屉：API 密钥不下钻，仅展示数字', async () => {
    getUserStatsMock.mockResolvedValue({
      user_id: 11, username: 'alice', days: 30,
      kpi: { requests: 5, tokens: 100, credits: 2, errors: 0, error_rate: 0, latency_p95_ms: 900 },
      daily: [], top_models: [], top_apps: [], top_keys: [],
      key_count: 42, recent_requests: [],
    })
    superAdminMode = false
    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
    await flushPromises()

    const row = wrapper.findAll('tbody tr')[0]
    await row.trigger('click')
    await flushPromises()

    // /tenants/:tenantId 挂 requiresSuper 路由门：非 super 不渲染死链，只留数字展示
    expect(document.body.querySelector('a[href*="tab=keys"]')).toBeNull()
    expect(document.body.textContent).toContain('42')
    // 日志链接（/request-logs）无路由门，保持展示
    expect(document.body.querySelector('a[href*="owner_user=alice"]')).not.toBeNull()
  })

  it('搜索过滤：无匹配时显示空态行', async () => {
    const wrapper = mount(UsersView, { global: { plugins: [i18n] } })
    await flushPromises()

    const search = wrapper.find('.search-input')
    await search.setValue('nonexistent-user')
    await flushPromises()

    expect(wrapper.findAll('tbody tr').length).toBe(1) // 空态行
    expect(wrapper.text()).toContain('无匹配用户')
  })
})
