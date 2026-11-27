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
    // 2026-11-04 W1-T1：tenant-portal 组退役并入 operations（A-13）——
    // 租户门户视角现在看到的是 operations 组的「我的服务」5 项。
    expect(ids).toContain('operations') // tenantOnly 项迁入的新组
    const operations = nav.navGroups.value.find((g) => g.id === 'operations')
    expect(operations).toBeTruthy()
    // ★ 2026-11-27：/tenant/pricing（套餐与充值）挂 navHidden 路径隐藏清单
    //   （owner 指令「将套餐与充值菜单进行隐藏」）⇒ 不再出现在任何视角。
    expect(operations!.items.map(({ item }) => item.path)).toEqual([
      '/tenant/models', '/tenant/usage', '/tenant/bills', '/tenant/account',
    ])
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

  // 2026-11-23 owner 指令：隐藏「运维中心」下的「赞助与捐赠」。
  // 线上实测远端运维菜单含该条目（path=/maintain/ops/donations）⇒ 合并时按路径剔除。
  it('远端运维菜单合并：捐赠条目被隐藏，其余远端条目保留（管理视角）', async () => {
    opsState.remote = [
      {
        id: 'opsplatform',
        label: '运维中心(远端)',
        items: [
          {
            path: '/maintain/ops/donations',
            label: '赞助与捐赠',
            labelKey: undefined,
            icon: '💖',
            super: true,
            hide_for_tenant: true,
            external: true,
          },
          {
            path: '/maintain/ops/faults',
            label: '故障管理',
            labelKey: undefined,
            icon: '⚠️',
            super: true,
            hide_for_tenant: true,
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
    const paths = ops!.items.map(({ item }) => item.path)
    expect(paths).not.toContain('/maintain/ops/donations')
    expect(paths).toContain('/maintain/ops/faults')
  })

  // ★ 2026-11-27 非管理员菜单重构轮（owner 指令「非管理员不需要运维中心」）：
  //   远端运维菜单是后台无凭据拉取的整组管理面 ⇒ 非管理视角（tenant_admin /
  //   user / 非 default 租户）必须**整组不合并**，不能只靠逐条路径隐藏。
  //   同轮口径收敛：管理视角 = isPlatformOps（super ∧ default 租户），
  //   tenant_admin（哪怕在 default 租户）也算被裁剪对象。
  it('非管理视角：远端运维菜单整组不合并（tenant_admin 也不可见）', async () => {
    roleState.superAdmin = false
    roleState.platformOps = false
    roleState.providerConsole = false
    opsState.remote = [
      {
        id: 'opsplatform',
        label: '运维中心(远端)',
        items: [
          { path: '/maintain/ops/faults', label: '故障管理', labelKey: undefined, icon: '⚠️', super: true, hide_for_tenant: true, external: true },
        ],
      },
    ]
    const w = mountNav()
    await new Promise((r) => setTimeout(r, 0))
    const nav = w.vm.nav as ReturnType<typeof useAppNav>
    // 远端组整组不合并 ⇒ 渲染面不含 opsplatform
    expect(nav.navGroups.value.map((g) => g.id)).not.toContain('opsplatform')
    // management 条目（凭据监控/自动路由闭环等）同轮对 tenant_admin 隐藏：
    // isManagementView = isPlatformOps，tenant_admin 不再是管理视角
    const modelsRouting = nav.navGroups.value.find((g) => g.id === 'models-routing')
    expect(modelsRouting).toBeTruthy()
    expect(modelsRouting!.items.map(({ item }) => item.path)).not.toContain('/routing-v2/credentials')
  })

  // ★ 2026-11-27：userFacing 穿透 —— /dashboard、/live 等条目对普通用户可见
  //   （旧角色闸 super/platformOps/hideForTenant 被 userFacing 压过；
  //   数据面由后端 requestStatsV2Scope 三档收窄）。
  //   注意：useAppNav 里顶层直链在 navPrimaryResolved（projected.direct），
  //   不进 navGroups 的任何分组（与 mergeNav 的 'primary' 组是两条链）。
  it('普通用户视角：总览与实时拓扑可见（userFacing 穿透旧角色闸）', () => {
    roleState.superAdmin = false
    roleState.platformOps = false
    roleState.providerConsole = false
    const w = mountNav()
    const nav = w.vm.nav as ReturnType<typeof useAppNav>
    const paths = nav.navPrimaryResolved.value.map(({ item }) => item.path)
    expect(paths).toContain('/dashboard')
    expect(paths).toContain('/live')
    // /live2 不带 userFacing ⇒ 平台运维专属，普通用户不可见
    expect(paths).not.toContain('/live2')
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
