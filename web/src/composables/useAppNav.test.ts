// useAppNav.test.ts — 静态菜单 + 角色过滤 + 运维中心远端菜单合并（mock）。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { navDrawerOpen, useAppNav, _resetUseAppNavForTests } from './useAppNav'

// 角色开关（可按用例切换）
const roleState = vi.hoisted(() => ({
  superAdmin: true,
  platformOps: true,
  providerConsole: true,
}))
vi.mock('../store', () => ({
  isSuperAdmin: () => roleState.superAdmin,
  isPlatformOpsView: () => roleState.platformOps,
  isProviderConsoleView: () => roleState.providerConsole,
}))

// 运维中心远端菜单（null = 未覆盖走本地；数组 = mergeRemoteOps 接管）
const opsState = vi.hoisted(() => ({ remote: null as unknown }))
vi.mock('../config/edition', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../config/edition')>()
  return {
    ...actual,
    resolveOpsMenu: async () => opsState.remote as never,
  }
})

// 插件菜单固定为空，避免测试内真实 fetch
vi.mock('@/api/plugins', () => ({
  fetchPluginNav: async () => [],
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { nav: { mainAria: '主导航', item: { activateAction: '激活' } } } },
})

const Host = {
  template: '<div />',
  setup() {
    const nav = useAppNav()
    return { nav }
  },
}

const router = createRouter({
  history: createMemoryHistory(),
  routes: [{ path: '/:pathMatch(.*)*', component: Host }],
})

function mountNav() {
  return mount(Host, { global: { plugins: [i18n, router] } })
}

describe('useAppNav', () => {
  beforeEach(() => {
    roleState.superAdmin = true
    roleState.platformOps = true
    roleState.providerConsole = true
    opsState.remote = null
    _resetUseAppNavForTests()
  })

  afterEach(() => {
    _resetUseAppNavForTests()
  })

  it('超管/运维视图：包含 super 分组与运维中心，且过滤租户专属分组', () => {
    const w = mountNav()
    const groups = (w.vm.nav as ReturnType<typeof useAppNav>).navGroups.value
    const ids = groups.map((g) => g.id)
    expect(ids).toContain('tenant-users')
    expect(ids).toContain('data-ops')
    expect(ids).not.toContain('tenant-portal') // tenantOnly 全部被过滤
    // 本地 opsplatform 组带 opsPlatform 标记：维护服务未探测可达时隐藏（与
    // AppTopbar 现状一致）；远端合并场景见下方用例
    expect(ids).not.toContain('opsplatform')
  })

  it('租户门户：仅租户可见分组，super 项被过滤', () => {
    roleState.superAdmin = false
    roleState.platformOps = false
    roleState.providerConsole = false
    const w = mountNav()
    const nav = w.vm.nav as ReturnType<typeof useAppNav>
    const ids = nav.navGroups.value.map((g) => g.id)
    expect(ids).toContain('tenant-portal') // tenantOnly 分组
    expect(ids).not.toContain('opsplatform') // 全部 super 项 → 组被过滤
    const tenantUsers = nav.navGroups.value.find((g) => g.id === 'tenant-users')
    expect(tenantUsers).toBeTruthy()
    // 租户只留下无 super/hideForTenant 标记的项（users / keys）
    expect(tenantUsers!.items.map(({ item }) => item.path)).toEqual(['/users', '/keys'])
  })

  it('远端运维菜单合并：本地 opsplatform 组被远端条目替换', async () => {
    opsState.remote = [
      {
        id: 'opsplatform',
        label: '运维中心(远端)',
        items: [
          {
            path: '/maintain/ops/remote-only',
            label: '远端专属',
            labelKey: undefined,
            icon: '🛰️',
            super: false,
            hide_for_tenant: false,
            external: true,
          },
        ],
      },
    ]
    const w = mountNav()
    // resolveOpsMenu 是异步覆盖：composable 启动后台加载，等待微任务刷新
    await new Promise((r) => setTimeout(r, 0))
    const nav = w.vm.nav as ReturnType<typeof useAppNav>
    const ops = nav.navGroups.value.find((g) => g.id === 'opsplatform')
    expect(ops).toBeTruthy()
    expect(ops!.items.map(({ item }) => item.path)).toContain('/maintain/ops/remote-only')
  })

  it('navDrawerOpen 为模块级共享开关：汉堡打开后任意消费方可读', () => {
    const w = mountNav()
    expect(navDrawerOpen.value).toBe(false)
    navDrawerOpen.value = true
    expect((w.vm.nav as ReturnType<typeof useAppNav>).isSuperAdmin.value).toBe(true)
    expect(navDrawerOpen.value).toBe(true)
    navDrawerOpen.value = false
  })
})
