// navHidden.test.ts — 菜单隐藏清单钉测（2026-11-23 owner 指令）
//
// owner 指令：「将『接入指南』与『运维中心』下的『赞助与捐赠』菜单隐藏」，
// 随后澄清：**只隐藏「赞助与捐赠」这一个菜单项，不要把「接入指南」整组藏掉**。
// 本文件钉四件事：
//   ① 隐藏生效：两处「赞助与捐赠」都不出现在菜单里；
//   ② 不误伤：「接入指南」组与其余四项（接入示例/自动配置/帮助手册/自动更新）
//      **仍然可见**（这是 owner 澄清的核心）；
//   ③ 隐藏 ≠ 删除：真源条目仍在（口径门 navCounts 按真源计，不受影响）；
//   ④ 隐藏不挑人：路径级隐藏对任何角色组合都生效（藏了就是藏了）。
//
// ★ 远端运维菜单那条链路（ai-native-maintain 下发）的钉测在
//   `composables/useAppNav.test.ts`（mergeRemoteOps 需要 mock 远端菜单）。
import { describe, it, expect } from 'vitest'
import { NAV_GROUPS, canShowNavItem, mergeNav, visibleNavGroups } from './appNav'
import { buildSimpleNav } from './simpleNav'
import { HIDDEN_NAV_PATHS, isHiddenNavPath } from './navHidden'

const SUPER = { isSuperAdmin: true, isPlatformOps: true, isTenantPortal: false }
const TENANT = { isSuperAdmin: false, isPlatformOps: false, isTenantPortal: true }

/** 渲染后的全部可见路径（跨分组展平）。 */
function renderedPaths(groups: { items: { path: string }[] }[]): string[] {
  return groups.flatMap((g) => g.items.map((i) => i.path))
}

describe('菜单隐藏清单（2026-11-23 owner 指令）', () => {
  it('隐藏清单只含两处捐赠入口的路径', () => {
    expect(HIDDEN_NAV_PATHS).toContain('/maintain/donate')
    // 两个来源、同名文案：本地真源 + 远端运维中心下发
    expect(HIDDEN_NAV_PATHS).toContain('/maintain/ops/donations')
    expect(isHiddenNavPath('/maintain/ops/donations')).toBe(true)
    expect(isHiddenNavPath('/maintain/donate')).toBe(true)
  })

  it('两处「赞助与捐赠」都不出现在渲染结果里', () => {
    const paths = renderedPaths(visibleNavGroups(NAV_GROUPS, SUPER))
    expect(paths).not.toContain('/maintain/donate')
    expect(paths).not.toContain('/maintain/ops/donations')
  })

  it('「接入指南」组仍然可见，其余入口不被误伤', () => {
    const source = NAV_GROUPS.find((g) => g.id === 'guide')!
    const guide = visibleNavGroups(NAV_GROUPS, SUPER).find((g) => g.id === 'guide')
    expect(guide).toBeTruthy()
    const paths = guide!.items.map((i) => i.path)
    // 接入示例 / 自动配置 / 帮助手册：任何环境下都应可见
    expect(paths).toContain('/examples')
    expect(paths).toContain('/examples#auto-config')
    expect(paths).toContain('/help')
    // 本组内唯一被隐藏的条目是捐赠项
    expect(paths).not.toContain('/maintain/donate')
  })

  it('「接入指南」组内未渲染的每一项都有明确归因（无附带误伤）', () => {
    const source = NAV_GROUPS.find((g) => g.id === 'guide')!
    const guide = visibleNavGroups(NAV_GROUPS, SUPER).find((g) => g.id === 'guide')!
    const rendered = new Set(guide!.items.map((i) => i.path))
    const missing = source.items.filter((i) => !rendered.has(i.path)).map((i) => i.path)
    // 两项未渲染各有归因：捐赠项＝隐藏清单；「自动更新」＝opsPlatform 可用性闸门
    // （maintain 不可达时不渲染，生产上可见），**不是**被隐藏清单带走的
    expect(missing.sort()).toEqual(['/maintain/donate', '/maintain/ops/autoupdate'])
    expect(isHiddenNavPath('/maintain/donate')).toBe(true)
    expect(isHiddenNavPath('/maintain/ops/autoupdate')).toBe(false)
    expect(source.items.find((i) => i.path === '/maintain/ops/autoupdate')!.opsPlatform).toBe(true)
  })

  it('顶栏合并（mergeNav）里「接入指南」组仍在、捐赠项已不在', () => {
    const merged = mergeNav([], NAV_GROUPS, SUPER)
    const guide = merged.find((g) => g.id === 'guide')
    expect(guide).toBeTruthy()
    expect(guide!.items.map((i) => i.path)).not.toContain('/maintain/donate')
  })

  it('简易模式顶栏仍保留「接入指南」下拉（与方案文档口径一致）', () => {
    const simple = buildSimpleNav([], visibleNavGroups(NAV_GROUPS, SUPER))
    const guide = simple.groups.find((g) => g.id === 'guide')
    expect(guide).toBeTruthy()
    expect(guide!.items.map((i) => i.path)).toEqual([
      '/examples',
      '/examples#auto-config',
      '/help',
    ])
  })

  it('隐藏 ≠ 删除：真源里捐赠条目还在（口径门按真源计数）', () => {
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
