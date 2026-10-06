// 导航 SSOT（UI规范 02 §4 + 17 §2 席位映射）。
import type { IconName } from '@/components/common/AppIcon.vue'

export interface NavItem {
  key: string
  to: string
  icon: IconName
  titleKey: string
  exact?: boolean
  /**
   * 可见所需的最低角色。缺省 = 登录即可见（对应后端 h.admin 或纯 GET）。
   *
   * ★ 2026-10-06 新增。原因：`/integrity` 整段是 h.superAdmin
   *   （admin/handler.go:924-925），tenant_admin 进去必然 403。
   *   而 AppDrawer 此前**无条件渲染** DRAWER_NAV ⇒ 等于给 tenant_admin 一个
   *   必然失败的入口 —— 这正是 17 §11.1 对凭据操作区定的规矩（「按 role 分档
   *   渲染，不是一律显示再吃后端 403」）。导航是同一个问题的上游，
   *   在抽屉层就该挡住，而不是等用户点进去看报错。
   */
  requiresRole?: 'admin' | 'super_admin'
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
  // 不占底栏（02 §4 底栏 ≤5 席已满），与「告警/用量」同级。admin 档。
  { key: 'routing', to: '/routing', icon: 'search', titleKey: 'nav.routing' },
  // 2026-10-06：供应商维度可见性（谁挂了/谁没绑模型/谁被手动停用）。admin 档。
  { key: 'providers', to: '/providers', icon: 'globe', titleKey: 'nav.providers' },
  // 2026-10-06：模型完整性异常。表现为「请求失败/结果诡异」但不落在节点健康上
  // ——移动端此前完全没这个面，排查只能开电脑。
  // ★ requiresRole: 后端整段 h.superAdmin（handler.go:924-925），tenant_admin 403。
  { key: 'integrity', to: '/integrity', icon: 'alert', titleKey: 'nav.integrity', requiresRole: 'super_admin' },
  // 2026-10-06：请求日志。回答「刚才那次到底发生了什么」——排障起点。admin 档。
  { key: 'logs', to: '/logs', icon: 'clock', titleKey: 'nav.logs' },
  // ── 运维排障线（2026-10-07）────────────────────────────────────────────
  // 这三条构成排障闭环：链路（发生了什么）→ 详情（为什么）→ 流水 / 瀑布（凭据侧与时间侧佐证）。
  // 全部走 AdminMiddleware（只认证不判角色）⇒ tenant_admin 可用，
  // 所以 requiresRole **一律不设** —— 按 appNav.ts 末尾的口径，
  // 只有 super_admin 档端点才需要在这里挡（对比 /integrity）。
  { key: 'journey', to: '/journey', icon: 'search', titleKey: 'nav.journey' },
  { key: 'routing-log', to: '/routing-log', icon: 'expand', titleKey: 'nav.routingLog' },
  { key: 'waterfall', to: '/waterfall', icon: 'play', titleKey: 'nav.waterfall' },
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

/**
 * 按当前角色过滤导航项。
 *
 * 角色判定口径与 UI规范 17 §11.1 一致：`authStore.role` 为 'super_admin' 才算
 * 超管。⚠️ 注意 `admin` 是**独立角色**、不等于超管 —— 后端 `h.admin` 允许
 * tenant_admin，`h.superAdmin` 不允许（admin/handler.go:880-888）。所以
 * `requiresRole: 'admin'` 在这里**不额外过滤**：移动端所有非 superAdmin 端点
 * 走的都是 h.admin，登录用户都能进，挡在这里反而会误伤。
 * 需要真过滤的只有 super_admin 档。
 */
export function navItemsFor(items: readonly NavItem[], role: string | null | undefined): NavItem[] {
  const isSuper = role === 'super_admin'
  return items.filter((item) => item.requiresRole !== 'super_admin' || isSuper)
}
