import { describe, it, expect } from 'vitest'
import { mergeNav, NAV_GROUPS } from './appNav'
import type { NavEntry } from '@/api/plugins'

describe('mergeNav', () => {
  it('inserts plugin item into existing group', () => {
    const items: NavEntry[] = [{
      plugin_id: 'p', plugin_version: '1', page_path: 's', page_type: 'data',
      nav_group: 'requests-sessions', label_key: 'k', super: false,
      platform_ops: false, tenant_only: false, order: 50, route_url: '/plugins/p/s',
    }]
    const merged = mergeNav(NAV_GROUPS, items)
    const g = merged.find((x) => x.id === 'requests-sessions')!
    expect(g.items.some((i) => i.path === '/plugins/p/s')).toBe(true)
  })
  it('creates plugins group for unknown nav_group', () => {
    const items: NavEntry[] = [{
      plugin_id: 'p', plugin_version: '1', page_path: 's', page_type: 'data',
      nav_group: 'nope', label_key: 'k', super: false, platform_ops: false,
      tenant_only: false, order: 1, route_url: '/plugins/p/s',
    }]
    const merged = mergeNav(NAV_GROUPS, items)
    expect(merged.some((g) => g.id === 'plugins')).toBe(true)
  })
})
