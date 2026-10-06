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
  { key: 'turns', to: '/turns', icon: 'clock', titleKey: 'nav.turns' },
  // ★ 排障线里**唯一**的 superAdmin 档：handler.go:1381
  //   `RegisterAutoRouteRoutes(mux, h.superAdmin)`，auth.go:353-357 对非超管 403。
  //   ⇒ 这里必须挡。理由与 /integrity 同：「按 role 分档渲染，不是一律显示再吃后端 403」。
  { key: 'routing-audit', to: '/routing-audit', icon: 'check', titleKey: 'nav.routingAudit', requiresRole: 'super_admin' },
  // ── 凭据监控线（2026-10-07）────────────────────────────────────────────
  // 补 NodesView 答不了的那一问：「哪个模型 × 哪个凭据的组合在坏」。
  // /node-health/:id 是热力图的下钻，不占导航席（从热力图卡片进入）。
  { key: 'heatmap', to: '/heatmap', icon: 'cube', titleKey: 'nav.heatmap' },
  // 探测面：展示 node_probe_state 判定的**过程**（排到第几次、下次何时重试、
  // 供应商直连延时）。adminWrap 档 ⇒ tenant_admin 可用，不设 requiresRole。
  { key: 'probe', to: '/probe', icon: 'refresh', titleKey: 'nav.probe' },
  // 路由覆盖规则：与 /routing-audit 配对（那一页答「谁改的」，这一页答
  // 「现在生效的是什么」）。★ 整条 auto-route 线是 superAdmin
  //（handler.go:1381 RegisterAutoRouteRoutes(mux, h.superAdmin)），必挡。
  { key: 'overrides', to: '/overrides', icon: 'expand', titleKey: 'nav.overrides', requiresRole: 'super_admin' },
  // ── 自动调优面（2026-10-07）────────────────────────────────────────────
  // 与 /overrides 配对闭环：那页答「现在生效的规则是什么」，这两页答
  // 「规则对不对、该怎么调」。
  // ★ 两条都是 superAdmin 档：funnel 走 handler.go:1430 的 h.superAdmin，
  //   proposals 走 handler.go:1381 的 h.superAdmin（auto_route.go:116 的
  //   adminWrap 是它传进去的）。tenant_admin 必 403 ⇒ 这里必须挡。
  { key: 'funnel', to: '/funnel', icon: 'chart', titleKey: 'nav.funnel', requiresRole: 'super_admin' },
  { key: 'proposals', to: '/proposals', icon: 'check', titleKey: 'nav.proposals', requiresRole: 'super_admin' },
  // 全局横向对比面：/funnel 是单模型纵深，这两条是全体模型的横向对比。
  // ★ superAdmin 档：analytics.go:55-58 的 matrix/flow，
  //   而 RegisterAnalyticsRoutes 由 handler.go:1430 用 h.superAdmin 挂载。
  { key: 'matrix', to: '/matrix', icon: 'grid', titleKey: 'nav.matrix', requiresRole: 'super_admin' },
  { key: 'flow', to: '/flow', icon: 'share', titleKey: 'nav.flow', requiresRole: 'super_admin' },
  // 探测系统健康 + 队列快照：答「整个探测系统健康吗、有没有卡住」，
  // 与 /probe（「具体哪个任务/供应商在跑」）配对。
  // ★ admin 档（wrapAdmin）⇒ 不设 requiresRole，对比上面四条超管线。
  { key: 'probe-health', to: '/probe-health', icon: 'refresh', titleKey: 'nav.probeHealth' },
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
