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
  // 自动路由读面：/proposals 答「系统认为该怎么调」，本页答「现在跑得怎么样、
  // 花了多少钱」—— 建议与效果之间缺的就是这一页。
  // ★ superAdmin 档：整条 auto-route 线由 handler.go:1381 / :1430 的
  //   h.superAdmin 挂载（形参名 adminWrap 是假名，值在调用处才定）⇒ 必挡。
  { key: 'auto-route', to: '/auto-route', icon: 'chart', titleKey: 'nav.autoRoute', requiresRole: 'super_admin' },
  // 全局横向对比面：/funnel 是单模型纵深，这两条是全体模型的横向对比。
  // ★ superAdmin 档：analytics.go:55-58 的 matrix/flow，
  //   而 RegisterAnalyticsRoutes 由 handler.go:1430 用 h.superAdmin 挂载。
  { key: 'matrix', to: '/matrix', icon: 'grid', titleKey: 'nav.matrix', requiresRole: 'super_admin' },
  { key: 'flow', to: '/flow', icon: 'share', titleKey: 'nav.flow', requiresRole: 'super_admin' },
  // 探测系统健康 + 队列快照：答「整个探测系统健康吗、有没有卡住」，
  // 与 /probe（「具体哪个任务/供应商在跑」）配对。
  // ★ admin 档（wrapAdmin）⇒ 不设 requiresRole，对比上面四条超管线。
  { key: 'probe-health', to: '/probe-health', icon: 'refresh', titleKey: 'nav.probeHealth' },
  // 模型级健康：答「哪个模型整体在坏」。与 /heatmap（模型×凭据）、
  // /probe（任务级 + 节点队列）构成从粗到细的完整粒度。admin 档。
  { key: 'probe-model', to: '/probe-model', icon: 'cube', titleKey: 'nav.probeModel' },
  // 时间线回答「**它是什么时候开始坏的**」，是 /probe-model 的快照的另一半。
  { key: 'timeline', to: '/timeline', icon: 'clock', titleKey: 'nav.timeline' },
  // Redis 缓存快照回答「探测说它健康，路由为什么没选它」。admin 档。
  { key: 'cache-state', to: '/cache-state', icon: 'globe', titleKey: 'nav.cache' },
  // ★ superAdmin 档：auto-route 整线由 h.superAdmin 挂载（handler.go:1430），
  //   tenant_admin 进去必然 403 ⇒ 抽屉层就要挡住。
  {
    key: 'task-index',
    to: '/task-index',
    icon: 'chart',
    titleKey: 'nav.taskIndex',
    requiresRole: 'super_admin',
  },
  // 会话审计清单 + 逐轮明细。admin 档（wrapAdmin）⇒ tenant_admin 可用，不设 requiresRole。
  { key: 'session-audit', to: '/session-audit', icon: 'grid', titleKey: 'nav.sessionAudit' },
  // 活跃会话（游标分页）。★ superAdmin 后端不加租户过滤 ⇒ 页内按角色说明作用域。
  { key: 'sessions-online', to: '/sessions-online', icon: 'play', titleKey: 'nav.onlineSessions' },
  // 系统监控：答「监控器这层自己在不在干活」。admin 档（adminWrap）⇒ 不设 requiresRole。
  { key: 'system-monitor', to: '/system-monitor', icon: 'cpu', titleKey: 'nav.sysmon' },
  // 数据生命周期：分区清单 + 体积榜。admin 档（admin()）⇒ 不设 requiresRole。
  { key: 'data-lifecycle', to: '/data-lifecycle', icon: 'grid', titleKey: 'nav.lifecycle' },
  // 路由优化器：准确率 + 激活参数 + 5 分钟明细。admin 档（admin(...)）⇒ 不设 requiresRole。
  { key: 'routing-opt', to: '/routing-opt', icon: 'cpu', titleKey: 'nav.routingOpt' },
  // 待处理响应：有没有卡住的请求。admin 档（admin(...)）⇒ 不设 requiresRole。
  { key: 'pending-responses', to: '/pending-responses', icon: 'play', titleKey: 'nav.pending' },
  // 请求侧异常：上游在拒绝我们的什么请求。**superAdmin 档**（h.superAdmin）
  // ⇒ tenant_admin 403，必须设 requiresRole 并同步 AppDrawer.spec.ts 白名单。
  // 输出合规命中与复核。admin 档（AdminMiddleware）⇒ 不设 requiresRole。
  { key: 'compliance-hits', to: '/compliance-hits', icon: 'alert', titleKey: 'nav.compliance' },
  // 输出合规策略与词库。admin 档 ⇒ 不设 requiresRole。
  { key: 'compliance-policy', to: '/compliance-policy', icon: 'key', titleKey: 'nav.compliancePolicy' },
  // 提示词注入现象面。admin 档（AdminMiddleware）⇒ 不设 requiresRole。
  { key: 'injection', to: '/injection', icon: 'alert', titleKey: 'nav.injection' },
  // 提示词注入配置面。admin 档 ⇒ 不设 requiresRole。
  { key: 'injection-config', to: '/injection-config', icon: 'key', titleKey: 'nav.injectionConfig' },
  // MaaS 积分价。**superAdmin 档**（admin/maas_handlers.go:14-24 全部 h.superAdmin）⇒ 须设 requiresRole。
  {
    key: 'maas-rates',
    to: '/maas-rates',
    icon: 'key',
    titleKey: 'nav.maasRates',
    requiresRole: 'super_admin',
  },
  // 租户名录。**superAdmin 档**（admin/handler.go:926-927）⇒ 须设。
  // ★ 这族 handler 内部还额外放行 `admin_key` 角色，中间件那道 requiresRole
  //   没有建模 ⇒ 移动端无法只用角色字符串判权限，只能靠后端的 403。
  {
    key: 'tenants',
    to: '/tenants',
    icon: 'user',
    titleKey: 'nav.tenants',
    requiresRole: 'super_admin',
  },
  // MaaS 租户运维面。**superAdmin 档**（/api/admin/maas/tenants/** 全 superAdmin）⇒ 须设。
  {
    key: 'maas-tenant-ops',
    to: '/maas-tenant-ops',
    icon: 'user',
    titleKey: 'nav.maasTenantOps',
    requiresRole: 'super_admin',
  },
  // MaaS 配置面。**superAdmin 档** ⇒ 须设。
  {
    key: 'maas-admin-catalog',
    to: '/maas-admin-catalog',
    icon: 'key',
    titleKey: 'nav.maasAdminCatalog',
    requiresRole: 'super_admin',
  },
  // MaaS 订单。**superAdmin 档**（admin/maas_handlers.go:14-24）⇒ 须设 requiresRole。
  // ★ 订单详情页 /maas-orders/:id 不占席，只有列表页进 DRAWER_NAV。
  {
    key: 'maas-orders',
    to: '/maas-orders',
    icon: 'grid',
    titleKey: 'nav.maasOrders',
    requiresRole: 'super_admin',
  },
  // MaaS 租户目录与价目。**admin 档**（/api/maas/** 是 h.admin(...)）⇒ 不设 requiresRole。
  { key: 'maas-catalog', to: '/maas-catalog', icon: 'cube', titleKey: 'nav.maasCatalog' },
  // MaaS 本租户钱包。admin 档 ⇒ 不设 requiresRole。
  { key: 'maas-wallet', to: '/maas-wallet', icon: 'user', titleKey: 'nav.maasWallet' },
  // 会话分析面。admin 档（admin/handler.go:1091-1094 全是 admin(...)）⇒ 不设 requiresRole。
  { key: 'session-analytics', to: '/session-analytics', icon: 'chart', titleKey: 'nav.sessionAnalytics' },
  {
    key: 'request-anomalies',
    to: '/request-anomalies',
    icon: 'alert',
    titleKey: 'nav.anomalies',
    requiresRole: 'super_admin',
  },
  // 租户模型策略（superAdmin 档）：admin/handler.go:926-927 两条注册都是
  // h.superAdmin，而 handleTenantModelPolicies 只有这一个调用点
  // ⇒ 整棵子树（含 check 与 audit）都是 superAdmin 硬门槛。
  // ★ 后端文件头把 check 标成 `(admin)`，那是过时的。
  {
    key: 'model-policies',
    to: '/model-policies',
    icon: 'cube',
    titleKey: 'nav.modelPolicies',
    requiresRole: 'super_admin',
  },
  // 租户模型策略审计。★ 无 OFFSET 无游标 ⇒ 页面刻意不放翻页控件。
  {
    key: 'model-policy-audit',
    to: '/model-policy-audit',
    icon: 'clock',
    titleKey: 'nav.modelPolicyAudit',
    requiresRole: 'super_admin',
  },
  // 租户审批配置。★ **admin 档**（wrapAdmin = admin.AdminMiddleware）⇒ tenant_admin 可用
  // ⇒ **故意不设** requiresRole；设成 super_admin 必须让判据红。
  {
    key: 'approval-config',
    to: '/approval-config',
    icon: 'check',
    titleKey: 'nav.approvalConfig',
  },
  // 审批人与规则。同样 admin 档，同样**故意不设** requiresRole。
  {
    key: 'approval-rules',
    to: '/approval-rules',
    icon: 'grid',
    titleKey: 'nav.approvalRules',
  },
  // 附件留存清单。**admin 档**（handler.go:998-1008 六条全是 admin(...)）
  // ⇒ **故意不设** requiresRole；设成 super_admin 必须让判据红。
  // ★ 同一前缀下混着两条 superAdmin（cleanup/execute、filesystem/cleanup），本页两个都不碰。
  {
    key: 'attachments',
    to: '/attachments',
    icon: 'copy',
    titleKey: 'nav.attachments',
  },
  // 功能模块面。**admin 档**（`admin/modules.go:1129-1134` 的两条 `h.admin(...)`
  //   —— 注册在 modules.go 自己里，**不在** handler.go）
  // ⇒ **故意不设** requiresRole；设成 super_admin 必须让判据红。
  // ★ 详情页 `/modules/:key` **不占抽屉席**（不设 ROOT_PATHS，返回钮行为与列表页不同）。
  {
    key: 'modules',
    to: '/modules',
    icon: 'cube',
    titleKey: 'nav.modules',
  },
  // 日志管理读面。四条只读**全是 admin 档**（handler.go:959,1115,1116,1119）
  // ⇒ **故意不设** requiresRole；设成 super_admin 必须让判据红。
  // ★ 同一前缀下混着三条 superAdmin（config/archive/cleanup）⇒ 不能按前缀判权限。
  {
    key: 'log-admin',
    to: '/log-admin',
    icon: 'clock',
    titleKey: 'nav.logAdmin',
  },
  // 会话上下文读面。两条只读**全是 h.admin(...)**（handler.go:1296）
  // ⇒ **故意不设** requiresRole；设成 super_admin 必须让判据红。
  // ★ API 前缀是 `/api/system/…`（不是 `/api/admin/`）—— 命名空间与抽屉路径不一致。
  // ★ `titles/batch` 只读语义却只能 POST；四个写端点本页一律不碰。
  {
    key: 'session-context',
    to: '/session-context',
    icon: 'grid',
    titleKey: 'nav.sessionContext',
  },
  // 免费资源自动发现读面。六条只读**全是 h.admin(...)**（handler.go:1302-1313）
  // ⇒ **故意不设** requiresRole；设成 super_admin 必须让判据红。
  // ★★ 第七条 scan-scheduler/status 是 **h.superAdmin(...)**（handler.go:1316）
  //   —— 它**不占**抽屉席（否则 admin 档页面被 superAdmin 门挡住），
  //   由本页面内单独探测并单列 403 态。
  // ★★★★★★ 同一个 handler、同一条路径**按方法分档**：GET=admin、
  //   POST/PUT/PATCH/DELETE=superAdmin（RequireSuperAdminForWrite）。
  //   ⇒ 又一条「不能按路径或前缀判权限」的证据。
  // ★ 五个写端点（建模板/改模板/删模板/scan/import/import-orbi）本页一律不碰。
  {
    key: 'free-discovery',
    to: '/free-discovery',
    icon: 'search',
    titleKey: 'nav.freeDiscovery',
  },
  // 凭据 × 模型实时状态。**这一条是 superAdmin 档**
  // （`registerStateRoutes` 里 `wrap := h.superAdmin`，credential_state_handlers.go:175）
  // ⇒ 抽屉席**必须**设 requiresRole: 'super_admin'（与前几批 admin 档页面相反）。
  // ★ 加这一席时必须同步 `src/components/shell/AppDrawer.spec.ts` 的白名单。
  // ★★ 同族三个 POST（单测/批测/按模型测）都**真的触发一次探测** ⇒ 本页一律不碰。
  {
    key: 'credential-model-state',
    to: '/credential-model-state',
    icon: 'cpu',
    titleKey: 'nav.credModelState',
    requiresRole: 'super_admin',
  },
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
