import { describe, it, expect } from 'vitest'
import { canShowNavItem, mergeNav, mergeRemotePluginNav, remoteNavToNavItems, NAV_GROUPS, NAV_PRIMARY_ITEMS, resolveNavItemActivation, ACTIVATE_REDIRECT_PATH, type RemoteNavEntry } from './appNav'

describe('mergeNav', () => {
  it('preserves visible primary and grouped navigation', () => {
    const merged = mergeNav(NAV_PRIMARY_ITEMS, NAV_GROUPS, {
      isSuperAdmin: true,
      isPlatformOps: true,
      isTenantPortal: false,
    })
    expect(merged.some((g) => g.id === 'primary')).toBe(true)
    expect(merged.some((g) => g.id === 'requests-sessions')).toBe(true)
  })
  it('hides tenant-only groups from platform navigation', () => {
    const merged = mergeNav(NAV_PRIMARY_ITEMS, NAV_GROUPS, {
      isSuperAdmin: true,
      isPlatformOps: true,
      isTenantPortal: false,
    })
    expect(merged.some((g) => g.id === 'tenant-portal')).toBe(false)
  })
})

// 2026-11-17（owner「打开功能」轮）：/live2 请求阶段树恢复可见。
//   ★ 上一版（本文件曾经的样子）钉的是「hidden === true」+「mergeNav 丢弃它」
//     —— 那是一条**会把功能再藏一次**的钉测：owner 已经把功能打开，钉测却还
//     要求它藏着 ⇒ 谁照着钉测改代码，就会把功能再关回去，而且测试全绿。
//   ⇒ 现在改成钉「**可见**」：菜单项在真源里、mergeNav 保留它、平台运维可见。
//     这条钉测的方向必须与 owner 的决定同向，否则它就是「反向锁」。
//
// 2026-11-25（owner 指令）：菜单**再次暂时隐藏** —— 但这次不是删条目、
//   也不是复活 NavItem.hidden 闸门，而是挂在 navHidden.ts 的路径级隐藏清单上
//   （与「赞助与捐赠」同机制）。⇒ 钉测方向随之更新：
//     ① 真源条目仍在（隐藏 ≠ 删除，口径门按真源计数）；
//     ② mergeNav 渲染结果里**不含** /live2（菜单已藏）；
//     ③ 路由与页面保留（直达 /live2 仍可用 —— 由 router.ts 保证，此处只钉菜单）。
describe('visible nav item (live2 stage tree)', () => {
  const live2 = NAV_PRIMARY_ITEMS.find((i) => i.path === '/live2')!

  it('keeps /live2 in the source list as a normal visible entry', () => {
    expect(live2).toBeTruthy()
    // 「hidden 闸门」已整体退役：类型上不再有这个字段，条目上也不再有它。
    expect((live2 as unknown as Record<string, unknown>).hidden).toBeUndefined()
  })

  it('canShowNavItem hides /live2 via the path-level hidden list (2026-11-25)', () => {
    // 菜单暂时隐藏走 navHidden 清单（路径级、对任何角色生效），
    // 不是角色判据 —— 所以 super_admin 也看不到。
    expect(
      canShowNavItem(live2, {
        isSuperAdmin: true,
        isPlatformOps: true,
        isTenantPortal: false,
      }),
    ).toBe(false)
  })

  it('mergeNav drops /live2 from the rendered primary group (menu hidden, route kept)', () => {
    const merged = mergeNav(NAV_PRIMARY_ITEMS, NAV_GROUPS, {
      isSuperAdmin: true,
      isPlatformOps: true,
      isTenantPortal: false,
    })
    const primary = merged.find((g) => g.id === 'primary')
    // /live2 已从菜单消失（2026-11-25 owner 指令）……
    expect(primary?.items.some((i) => i.path === '/live2')).toBe(false)
    // ……但相邻的 /live（实时拓扑）不受影响。
    expect(primary?.items.some((i) => i.path === '/live')).toBe(true)
    // 隐藏 ≠ 删除：真源条目仍在。
    expect(NAV_PRIMARY_ITEMS.some((i) => i.path === '/live2')).toBe(true)
  })
})

// 2026-09-04: 供应商菜单用 providerConsole 标记 —— super_admin 或 default
// 租户 tenant_admin 可见（凭据 API Key 轮换对该角色开放），其余不可见。
describe('providerConsole nav visibility', () => {
  const providersItem = NAV_GROUPS
    .find((g) => g.id === 'models-routing')!
    .items.find((i) => i.path === '/providers')!

  it('marks /providers with providerConsole (not super)', () => {
    expect(providersItem.providerConsole).toBe(true)
    expect(providersItem.super).toBeFalsy()
  })

  it('shows providers for super_admin regardless of tenant view opts', () => {
    expect(canShowNavItem(providersItem, {
      isSuperAdmin: true, isPlatformOps: false, isTenantPortal: true, isProviderConsole: true,
    })).toBe(true)
  })

  it('shows providers for default-tenant tenant_admin (provider console)', () => {
    expect(canShowNavItem(providersItem, {
      isSuperAdmin: false, isPlatformOps: false, isTenantPortal: true, isProviderConsole: true,
    })).toBe(true)
  })

  it('hides providers for non-default tenant_admin and plain users', () => {
    expect(canShowNavItem(providersItem, {
      isSuperAdmin: false, isPlatformOps: false, isTenantPortal: true, isProviderConsole: false,
    })).toBe(false)
  })
})

describe('opsplatform maintain external links', () => {  const ops = NAV_GROUPS.find((g) => g.id === 'opsplatform')!

  it('marks migrated ops items as external /maintain/* paths', () => {
    const migrated = ops.items.filter((i) => i.path.startsWith('/maintain/'))
    expect(migrated.length).toBeGreaterThanOrEqual(6)
    for (const item of migrated) {
      expect(item.external).toBe(true)
    }
  })

  it('keeps vibecoding inside Gateway SPA (not external)', () => {
    const vibe = ops.items.find((i) => i.path === '/ops/vibecoding')
    expect(vibe).toBeTruthy()
    expect(vibe!.external).toBeFalsy()
  })
})

describe('resolveNavItemActivation', () => {
  it('redirects autoupdate to ACTIVATE_REDIRECT_PATH when not activated', () => {
    const autoUpdate = NAV_GROUPS.find((g) => g.id === 'opsplatform')!.items.find(
      (i) => i.path === '/maintain/ops/autoupdate',
    )!
    const resolved = resolveNavItemActivation(autoUpdate, { isActivated: false })
    expect(resolved.path).toBe('/customer/update-activate')
    expect(resolved.activateAction).toBe(true)
    expect(resolved.originalPath).toBe('/maintain/ops/autoupdate')
  })

  it('keeps original path when isActivated is true', () => {
    const autoUpdate = NAV_GROUPS.find((g) => g.id === 'opsplatform')!.items.find(
      (i) => i.path === '/maintain/ops/autoupdate',
    )!
    const resolved = resolveNavItemActivation(autoUpdate, { isActivated: true })
    expect(resolved.path).toBe('/maintain/ops/autoupdate')
    expect(resolved.activateAction).toBe(false)
  })

  it('returns original path for items without activateWhenNotActivated', () => {
    const overview = NAV_GROUPS.find((g) => g.id === 'opsplatform')!.items.find(
      (i) => i.path === '/maintain/ops/overview',
    )!
    const resolved = resolveNavItemActivation(overview, { isActivated: false })
    expect(resolved.path).toBe('/maintain/ops/overview')
    expect(resolved.activateAction).toBe(false)
  })

  it('does not redirect when isActivated is undefined (treats undefined as not false)', () => {
    const autoUpdate = NAV_GROUPS.find((g) => g.id === 'opsplatform')!.items.find(
      (i) => i.path === '/maintain/ops/autoupdate',
    )!
    const resolved = resolveNavItemActivation(autoUpdate, {})
    expect(resolved.path).toBe('/maintain/ops/autoupdate')
    expect(resolved.activateAction).toBe(false)
  })
})

/**
 * 2026-09-04: 接入指南(guide)子菜单回归保护。
 * 验证从 ai-native-maintain 迁移过来的「自动更新」与「赞助与捐赠」菜单项
 * 配置正确：均为 external 跳转,且 donate 不带 opsPlatform 门控(所有登录用户可见)。
 */
describe('guide group migrated items', () => {
  const guide = NAV_GROUPS.find((g) => g.id === 'guide')!

  it('contains the migrated 自动更新 entry pointing to maintain', () => {
    const autoUpdate = guide.items.find((i) => i.path === '/maintain/ops/autoupdate')
    expect(autoUpdate).toBeTruthy()
    expect(autoUpdate!.labelKey).toBe('nav.item.opsAutoUpdate')
    expect(autoUpdate!.external).toBe(true)
    expect(autoUpdate!.opsPlatform).toBe(true)
    expect(autoUpdate!.activateWhenNotActivated).toBe(true)
  })

  it('contains the migrated 赞助与捐赠 entry pointing to maintain', () => {
    const donate = guide.items.find((i) => i.path === '/maintain/donate')
    expect(donate).toBeTruthy()
    expect(donate!.labelKey).toBe('nav.item.supportDonate')
    expect(donate!.external).toBe(true)
    // 捐赠页对所有登录用户开放,不设置 opsPlatform 门控
    expect(donate!.opsPlatform).toBeFalsy()
  })

  it('keeps the original 接入示例 entry as the first item', () => {
    expect(guide.items.length).toBeGreaterThanOrEqual(3)
    expect(guide.items[0].path).toBe('/examples')
  })

  it('links 自动配置 to the generator section on 接入示例', () => {
    // 2026-10-27（第 10 增量）：菜单项不再带硬编码中文 `label`（语言包是唯一真值）
    // ⇒ 断言改成钉 i18n 键；键在 8 语种都存在由 i18n-key-parity-count 门保证。
    expect(guide.items[1]).toMatchObject({
      path: '/examples#auto-config',
      labelKey: 'nav.item.autoConfig',
    })
    expect(guide.items[1].label).toBeUndefined()
  })

  it('redirects guide 自动更新 to ACTIVATE_REDIRECT_PATH when not activated', () => {
    const autoUpdate = guide.items.find((i) => i.path === '/maintain/ops/autoupdate')!
    const resolved = resolveNavItemActivation(autoUpdate, { isActivated: false })
    expect(resolved.path).toBe('/customer/update-activate')
    expect(resolved.activateAction).toBe(true)
    expect(resolved.originalPath).toBe('/maintain/ops/autoupdate')
  })

  // 2026-11-16：帮助手册入口（下载/安装/部署/供应商/凭据/租户/用户/API Key/
  // 客户端调用/常见故障）。对所有登录用户开放 ⇒ 不设角色门。
  it('contains the 帮助手册 entry pointing to /help', () => {
    const help = guide.items.find((i) => i.path === '/help')
    expect(help).toBeTruthy()
    expect(help!.labelKey).toBe('nav.item.helpManual')
    expect(help!.super).toBeFalsy()
    expect(help!.platformOps).toBeFalsy()
    expect(help!.tenantOnly).toBeFalsy()
    expect(help!.hideForTenant).toBeFalsy()
    expect(help!.external).toBeFalsy()
  })

  it('keeps 帮助手册 inside the guide group (not a new top-level group)', () => {
    const help = guide.items.find((i) => i.path === '/help')!
    expect(NAV_GROUPS.some((g) => g.id === 'guide' && g.items.some((i) => i.path === '/help'))).toBe(true)
  })
})

describe('V5.1 plugin nav (remote merge)', () => {
  const asmEntry: RemoteNavEntry = {
    plugin_id: 'ai-session-manager',
    plugin_version: '0.1.1',
    page_path: 'sessions',
    page_type: 'data',
    nav_group: 'requests-sessions',
    label_key: 'nav.item.pluginSessions',
    icon: '💬',
    super: true,
    platform_ops: false,
    tenant_only: false,
    order: 100,
    route_url: '/plugins/ai-session-manager/sessions',
  }

  it('appends plugin entry to the matching static group', () => {
    const merged = mergeRemotePluginNav(NAV_GROUPS, [asmEntry])
    const target = merged.find((g) => g.id === 'requests-sessions')!
    const hasPlugin = target.items.some((it) => it.path === '/plugins/ai-session-manager/sessions')
    expect(hasPlugin).toBe(true)
  })

  it('marks plugin nav items external so AppTopbar renders <a>', () => {
    const items = remoteNavToNavItems([asmEntry])
    expect(items[0].external).toBe(true)
    expect(items[0].plugin).toBe('ai-session-manager')
  })

  it('routes entries with an unknown nav_group into a synthetic plugins group', () => {
    const odd: RemoteNavEntry = { ...asmEntry, nav_group: 'mystery-group', route_url: '/plugins/x/foo', page_path: 'foo' }
    const merged = mergeRemotePluginNav(NAV_GROUPS, [odd])
    const synthetic = merged.find((g) => g.id === 'plugins')
    expect(synthetic).toBeTruthy()
    expect(synthetic!.items.some((it) => it.path === '/plugins/x/foo')).toBe(true)
  })

  it('returns the original groups unchanged when no entries are provided', () => {
    const merged = mergeRemotePluginNav(NAV_GROUPS, [])
    expect(merged).toEqual(NAV_GROUPS)
  })

  it('does NOT inject a hardcoded ai-session-manager entry into NAV_GROUPS', () => {
    // Regression: V5.1 removed the static {path: "/plugins/ai-session-manager/sessions"}
    // entry; ASM nav now flows through the dynamic /api/v1/plugin-nav path.
    const all = NAV_GROUPS.flatMap((g) => g.items)
    const legacy = all.find((it) => it.path === '/plugins/ai-session-manager/sessions' && it.plugin === 'ai-session-manager')
    expect(legacy).toBeUndefined()
  })
})
