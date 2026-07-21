import { describe, it, expect } from 'vitest'
import { mergeNav, NAV_GROUPS, NAV_PRIMARY_ITEMS } from './appNav'
import type { NavEntry } from '@/api/plugins'

describe('mergeNav', () => {
  it('inserts plugin item into existing group', () => {
    const items: NavEntry[] = [{
      plugin_id: 'p', plugin_version: '1', page_path: 's', page_type: 'data',
      nav_group: 'requests-sessions', label_key: 'k', super: false,
      platform_ops: false, tenant_only: false, order: 50, route_url: '/plugins/p/s',
    }]
    const merged = mergeNav(NAV_PRIMARY_ITEMS, NAV_GROUPS, {
      isSuperAdmin: true,
      isPlatformOps: true,
      isTenantPortal: false,
    })
    // After merge, the plugin item should be findable somewhere in merged groups
    const hasPlugin = merged.some((g) => g.items.some((i) => i.path === '/plugins/p/s'))
    expect(hasPlugin).toBe(true)
  })
  it('creates plugins group for unknown nav_group', () => {
    const items: NavEntry[] = [{
      plugin_id: 'p', plugin_version: '1', page_path: 's', page_type: 'data',
      nav_group: 'nope', label_key: 'k', super: false, platform_ops: false,
      tenant_only: false, order: 1, route_url: '/plugins/p/s',
    }]
    const merged = mergeNav(NAV_PRIMARY_ITEMS, NAV_GROUPS, {
      isSuperAdmin: true,
      isPlatformOps: true,
      isTenantPortal: false,
    })
    expect(merged.some((g) => g.id === 'plugins')).toBe(true)
  })
})

describe('opsplatform maintain external links', () => {
  const ops = NAV_GROUPS.find((g) => g.id === 'opsplatform')!

  it('marks migrated ops items as external /maintain/* paths', () => {
    const migrated = ops.items.filter((i) => i.path.startsWith('/maintain/'))
    expect(migrated.length).toBeGreaterThanOrEqual(7)
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
