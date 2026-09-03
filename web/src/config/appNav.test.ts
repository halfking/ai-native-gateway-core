import { describe, it, expect } from 'vitest'
import { canShowNavItem, mergeNav, NAV_GROUPS, NAV_PRIMARY_ITEMS, resolveNavItemActivation, ACTIVATE_REDIRECT_PATH } from './appNav'

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
 * 2026-07-22: 回归保护 — 防止 PUBLIC_NAV_LINKS 指向不存在的路由，
 * 否则路由守卫会重定向到 /?login=1，导致导航栏错位。
 */
describe('PUBLIC_NAV_LINKS regression guard', () => {
  it('all PUBLIC_NAV_LINKS must point to existing public routes', async () => {
    const { PUBLIC_NAV_LINKS } = await import('./navLinks')
    const routerModule = await import('../router')

    // 收集所有声明为 public 的路径
    const publicPaths = new Set<string>()
    for (const route of routerModule.router.options.routes) {
      if (route.meta?.public) {
        publicPaths.add(route.path as string)
      }
    }

    // 所有 PUBLIC_NAV_LINKS 必须在 publicPaths 中
    for (const link of PUBLIC_NAV_LINKS) {
      const isPublic = publicPaths.has(link.path)
      expect(
        isPublic,
        `PUBLIC_NAV_LINKS entry "${link.path}" (${link.labelKey}) is not in the set of public routes — this will trigger a redirect to /?login=1`,
      ).toBe(true)
    }
  })
})
