// 导航 SSOT（UI规范 02 §4 + 17 §2 席位映射）。
import type { IconName } from '@/components/common/AppIcon.vue'

export interface NavItem {
  key: string
  to: string
  icon: IconName
  titleKey: string
  exact?: boolean
}

/** 底栏 4 席直达 + 「更多」固定席（≤5 席约束）。 */
export const BOTTOM_NAV: readonly NavItem[] = [
  { key: 'home', to: '/', icon: 'home', titleKey: 'nav.home', exact: true },
  { key: 'nodes', to: '/nodes', icon: 'server', titleKey: 'nav.nodes' },
  { key: 'models', to: '/models', icon: 'cube', titleKey: 'nav.models' },
  { key: 'keys', to: '/keys', icon: 'key', titleKey: 'nav.keys' },
] as const

/** 抽屉二级导航。 */
export const DRAWER_NAV: readonly NavItem[] = [
  { key: 'alerts', to: '/alerts', icon: 'alert', titleKey: 'nav.alerts' },
  { key: 'usage', to: '/usage', icon: 'chart', titleKey: 'nav.usage' },
  // 2026-10-06：路由检查（只读 explain）从 desktopOnly 收进移动端，抽屉席位。
  // 不占底栏（02 §4 底栏 ≤5 席已满），与「告警/用量」同级。
  { key: 'routing', to: '/routing', icon: 'search', titleKey: 'nav.routing' },
  // 2026-10-06：供应商维度可见性（谁挂了/谁没绑模型/谁被手动停用）。
  { key: 'providers', to: '/providers', icon: 'globe', titleKey: 'nav.providers' },
  // 2026-10-06：模型完整性异常（superAdmin 档）。表现为「请求失败/结果诡异」
  // 但不落在节点健康上——移动端此前完全没这个面，排查只能开电脑。
  { key: 'integrity', to: '/integrity', icon: 'alert', titleKey: 'nav.integrity' },
] as const

const ROOT_PATHS = new Set<string>([...BOTTOM_NAV, ...DRAWER_NAV].map((n) => n.to))

/** 根级页面（返回钮不出现，06 §7「返回/根级导航」切换）。 */
export function isRootRoute(path: string): boolean {
  return ROOT_PATHS.has(path)
}

export function isNavItemActive(item: NavItem, path: string): boolean {
  if (item.exact) return path === item.to
  return path === item.to || path.startsWith(item.to + '/')
}
