// navHidden.test.ts — 菜单隐藏清单钉测（2026-11-23 owner 指令）
//
// owner 指令：「将『接入指南』与『运维中心』下的『赞助与捐赠』菜单隐藏」。
// 本文件钉三件事：
//   ① 隐藏生效：渲染结果里没有「接入指南」组，也没有两处捐赠入口；
//   ② 隐藏 ≠ 删除：真源条目仍在（口径门 navCounts 按真源计，不受影响）；
//   ③ 隐藏不挑人：路径级隐藏对任何角色组合都生效（藏了就是藏了）。
//
// ★ 远端运维菜单那条链路（ai-native-maintain 下发）的钉测在
//   `composables/useAppNav.test.ts`（mergeRemoteOps 需要 mock 远端菜单）。
import { describe, it, expect } from 'vitest'
import { NAV_GROUPS, canShowNavItem, mergeNav, visibleNavGroups } from './appNav'
import { buildSimpleNav } from './simpleNav'
import { HIDDEN_NAV_GROUP_IDS, HIDDEN_NAV_PATHS, isHiddenNavGroup, isHiddenNavPath } from './navHidden'

const SUPER = { isSuperAdmin: true, isPlatformOps: true, isTenantPortal: false }
const TENANT = { isSuperAdmin: false, isPlatformOps: false, isTenantPortal: true }

/** 渲染后的全部可见路径（跨分组展平）。 */
function renderedPaths(groups: { items: { path: string }[] }[]): string[] {
  return groups.flatMap((g) => g.items.map((i) => i.path))
}

describe('菜单隐藏清单（2026-11-23 owner 指令）', () => {
  it('隐藏清单本身覆盖 guide 组与两处捐赠路径', () => {
    expect(HIDDEN_NAV_GROUP_IDS).toContain('guide')
    // 两个来源、同名文案：本地真源 + 远端运维中心下发
    expect(HIDDEN_NAV_PATHS).toContain('/maintain/donate')
    expect(HIDDEN_NAV_PATHS).toContain('/maintain/ops/donations')
    expect(isHiddenNavGroup('guide')).toBe(true)
    expect(isHiddenNavPath('/maintain/ops/donations')).toBe(true)
  })

  it('「接入指南」组整组不再出现在渲染结果里', () => {
    const groups = visibleNavGroups(NAV_GROUPS, SUPER)
    expect(groups.some((g) => g.id === 'guide')).toBe(false)
  })

  it('「接入指南」组内的入口（含同名捐赠项）都不再出现在菜单里', () => {
    const paths = renderedPaths(visibleNavGroups(NAV_GROUPS, SUPER))
    // ★「自动更新」(/maintain/ops/autoupdate) 另有 opsplatform 组同名入口，
    //   不在本次隐藏范围 ⇒ 不在此断言（否则会把「运维中心」也误判成被藏）。
    for (const path of ['/examples', '/examples#auto-config', '/help', '/maintain/donate']) {
      expect(paths).not.toContain(path)
    }
  })

  it('顶栏合并（mergeNav）同样不含 guide 组', () => {
    const merged = mergeNav([], NAV_GROUPS, SUPER)
    expect(merged.some((g) => g.id === 'guide')).toBe(false)
  })

  it('简易模式顶栏也不再出现「接入指南」下拉', () => {
    const simple = buildSimpleNav([], visibleNavGroups(NAV_GROUPS, SUPER))
    expect(simple.groups.some((g) => g.id === 'guide')).toBe(false)
    expect(simple.groups.some((g) => g.labelKey === 'nav.group.guide')).toBe(false)
  })

  it('隐藏 ≠ 删除：真源里组与条目都还在（口径门按真源计数）', () => {
    const guide = NAV_GROUPS.find((g) => g.id === 'guide')!
    expect(guide).toBeTruthy()
    expect(guide.items.map((i) => i.path)).toContain('/maintain/donate')
    expect(guide.items.length).toBe(5)
  })

  it('路径级隐藏不挑人：任何角色组合下捐赠入口都不可见', () => {
    for (const path of HIDDEN_NAV_PATHS) {
      expect(canShowNavItem({ path, icon: '💖' }, SUPER)).toBe(false)
      expect(canShowNavItem({ path, icon: '💖' }, TENANT)).toBe(false)
    }
  })
})
