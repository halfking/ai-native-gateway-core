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
  { key: 'users', to: '/users', icon: 'user', titleKey: 'nav.users', requiresRole: 'admin' },
  { key: 'reconciliation', to: '/reconciliation', icon: 'chart', titleKey: 'nav.reconciliation', requiresRole: 'super_admin' },
  // ★ 2026-10-14 第一百一十一批：对账报表（区间多维汇总 + 筛选栏候选）。
  //   `admin/handler.go:1086` `h.superAdmin(h.handleReportRollup)` ⇒ **要** super_admin。
  //   ⚠️ ★★ 与上面那张 `reconciliation` 席是**两个不同功能**，标题必须能区分：
  //     · reconciliation → CostReconciliationView → @/api/costReconciliation（费用对账）
  //     · report-rollup  → ReportRollupView       → @/api/reportRollup（对账报表）
  //     两张席都叫「对账」⇒ 靠 nav 标题区分，接线时别接错模块。
  {
    key: 'report-rollup',
    to: '/report-rollup',
    icon: 'chart',
    titleKey: 'nav.reportRollup',
    requiresRole: 'super_admin',
  },
  // ★ 2026-10-14 第一百一十二批：单请求调度轨迹。
  //   ★★ **admin 档，不是 superAdmin**（可执行注册处 `cmd/gateway/main.go:7143-7144`
  //   传的是 `requestJourneyWrapAdmin` → `admin.AdminMiddleware`，后者**不检查 role**）
  //   ⇒ **不设** `requiresRole`（tenant_admin 也够用）
  //   ⇒ 且**不得**进 `AppDrawer.spec.ts` 的 superAdmin 白名单 —— 加进去那条立刻红。
  //   ⚠️ 这与批 109/110/111 连续三批相反，接线时别照抄那一套。
  //   ★ 但**租户隔离是真做了**的（`admin/journal_handlers.go:59-67`：
  //   「角色 × 路径租户」二维，越权返 **404 而非 403**）⇒ 前端无需段落级门控。
  { key: 'dispatch-journal', to: '/dispatch-journal', icon: 'search', titleKey: 'nav.dispatchJournal' },
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
  // 工作类型配置面。★ **整族 h.superAdmin**（`admin/handler.go:1434`
  //   `wtH.RegisterWorkTypeRoutes(mux, h.superAdmin)`，四条 GET 全挂这一个中间件）
  //   ⇒ 须设 requiresRole。
  // ★ 与批 90 的 settings **同前缀邻域但档位相反** —— 别照抄那一席。
  {
    key: 'work-types',
    to: '/work-types',
    icon: 'cpu',
    titleKey: 'nav.workTypes',
    requiresRole: 'super_admin',
  },
  // 系统自检面。⚠️ 后端六条全是 `admin(...)`（admin/self_check_handlers.go:60-68），
  //   **席本身不设 requiresRole**；但页面内部对**跨租户的三段**
  //   （runs / stats / models —— self_check_runs 有 tenant_id 列却从不引用）
  //   另按 super_admin 门控，settings 与 trigger/availability 保持 admin 档。
  { key: 'self-check', to: '/self-check', icon: 'check', titleKey: 'nav.selfCheck' },
  // 凭据探测三态队列。**admin 档**（admin/probe_dashboard.go:1907 的
  //   `adminWrap(h.handleProbeTaskRoute)`）⇒ tenant_admin 可用 ⇒ **不设** requiresRole。
  // ★ 该路由是**方法多路复用**（GET/POST/DELETE 同一条），本页只发 GET。
  { key: 'probe-queue', to: '/probe-queue', icon: 'clock', titleKey: 'nav.probeQueue' },
  // 流式连接注册台（live + 注销审计）。**admin 档**
  // （admin/handler.go:1024-1025 两条都是 `admin(...)`）⇒ tenant_admin 可用
  // ⇒ **不设** requiresRole。
  // ⚠️ 桌面把这条路由标成 `requiresSuper: true`（web/src/router.ts:284）/
  //   `super: true`（web/src/config/appNav.ts:195）—— **前端比后端严**，
  //   按纪律以可执行的注册为准。
  // ★ 数据源是**进程内**状态（domains/streaming.ConnectionRegistry），不是数据库
  //   ⇒ 本族**没有跨租户查询**（对比批 104/106 那两个真正跨租户的族）。
  { key: 'connection-registry', to: '/connection-registry', icon: 'server', titleKey: 'nav.connectionRegistry' },
  // 用量趋势线。**admin 档**（admin/handler.go:1267 的 `h.admin(h.HandleUsageAdmin)`
  //   前缀子路由，分派 admin/usage.go:60-64）⇒ tenant_admin 可用 ⇒ **不设** requiresRole。
  // ★ 本族的租户口径是本仓第四种：`statsTenantScope`（admin/stats.go:87-97）对非
  //   super_admin 强制用 auth.TenantID 并**忽略 tenant_id 参数** ⇒ tenant_admin 天然隔离，
  //   与批 104（硬编码 tenant）与批 106（有列不接线）相反 ⇒ 不需要段落级门控。
  { key: 'usage-trend-series', to: '/usage-trend-series', icon: 'chart', titleKey: 'nav.usageTrendSeries' },
  // 响应格式异常（明细 + 按小时汇总）。★★ **superAdmin 档**
  //   （admin/handler.go:913-914 两条都是 `h.superAdmin`）⇒ 抽屉席**须设** requiresRole，
  //   并同步 `src/components/shell/AppDrawer.spec.ts` 的排序白名单。
  // ★★ 本族是本仓第六次「不隔离」（withAllTenantReadOnlyTx，
  //   admin/format_anomalies.go:106/:143/:213）：两张表都有 tenant_id 列、
  //   list 会 SELECT 出来，但 WHERE 从不过滤。
  //   ★ 与批 104/106 的处置**相反**：那两族是 admin 档 + 不隔离
  //     ⇒ tenant_admin 能读到跨租户 ⇒ 必须在视图内按段落门控；
  //     本族是 superAdmin 档 ⇒ **档位本身就挡住了** ⇒ 无需任何段落级门控。
  { key: 'format-anomalies', to: '/format-anomalies', icon: 'alert', titleKey: 'nav.formatAnomalies', requiresRole: 'super_admin' },
  // ★ 2026-10-14 第一百一十批：指纹漂移。`admin/handler.go:942-943` 两条都是
  //   `h.superAdmin`（整个 `/api/admin/model-integrity/` 前缀都是 superAdmin 档）
  //   ⇒ 这一席**要** `requiresRole: 'super_admin'`。
  //   ★ 本族读路径跨全部租户（`admin/model_integrity.go:328` 的 `withAllTenantReadOnlyTx`
  //     且 WHERE 无租户条件），但**档位本身就是那道门** ⇒ 视图内无需段落级门控。
  //   ⚠️ 与 `integrity`（同前缀的 summary/events/resolve 那一面）是**两张席**：
  //     同一个前缀上既有 admin 面也有 superAdmin 面 ⇒ 权限**不得**按前缀判。
  {
    key: 'model-integrity-drift',
    to: '/model-integrity-drift',
    icon: 'alert',
    titleKey: 'nav.modelIntegrityDrift',
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
  // 看板读面（dashboard 九条）。★ admin 档（handler.go:1052-1069 全部 `admin(...)`）
  // ⇒ **不设** requiresRole。
  // ★ 与上面 /auto-route 的 super_admin 档相反 —— 这是本仓最容易照抄错的一处：
  //   两条都叫「路由/看板面」，权限档却完全相反。
  { key: 'dashboard-ops', to: '/dashboard-ops', icon: 'grid', titleKey: 'nav.dashboardOps' },
  // 日志运维面。★ admin 档（handler.go:959/1115/1116 三条都是 admin(...)）
  // ⇒ **不设** requiresRole。
  // ⚠️ 同前缀下 config / archive / cleanup 是 h.superAdmin ——
  //   那是**前缀级的巧合**，不是整族的档位；按前缀判权限会判错。
  // 图标用 clock（时间轴语义）：IconName 里没有 file，类型门会红。
  { key: 'log-ops', to: '/log-ops', icon: 'clock', titleKey: 'nav.logOps' },
  // 人工标注工作台。★ admin 档（handler.go:1386/1389/1390 全是 admin(...)）
  // ⇒ **不设** requiresRole。
  { key: 'annotations', to: '/annotations', icon: 'check', titleKey: 'nav.annotations' },
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
  // 审批队列（运行中的审批实例）。★ **admin 档**（main.go:7384-7385 两条
  // 都是 wrapAdmin）⇒ tenant_admin 可用 ⇒ **故意不设** requiresRole；
  // 设成 super_admin 必须让判据红。
  // ★ 不碰 /api/v1/approvals/* 的 approve/reject/resume（真的改审批状态）。
  {
    key: 'approval-queue',
    to: '/approval-queue',
    icon: 'clock',
    titleKey: 'nav.approvalQueue',
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
  // 压缩可观测（stats/sessions，2026-10-08）。admin 档 ⇒ **不设** requiresRole。
  // 与 /data-flow 配对：那页答「记录怎么分布」，本页答「压缩实际压了多少」。
  // ⚠️ 本页压缩率是 **0-1 比例**，data-flow 的是 **0-100 百分数**，两者不可并列。
  {
    key: 'compression',
    to: '/compression',
    icon: 'cube',
    titleKey: 'nav.compression',
  },
// 数据生命周期**记录级**（stats/metrics/jobs/blobs/top，2026-10-08）。
  // 与 /data-lifecycle 按「答什么」划界：那页答「表级存储与保留」（分区 + 体积榜），
  // 本页答「记录怎么分布 / 有没有在清理 / 大字段占多少」。粒度不同，不合并。
  // ★ 四条注册（handler.go:960/962/979/994）全部是 `admin(...)`
  //   ⇒ tenant_admin 可用 ⇒ **不设** requiresRole。
  // ⚠️ 但本页的 `metrics` 是**全表口径**（SQL 无 WHERE，data_lifecycle_metrics.go:63），
  //   而文件头注释写着「super-admin only」—— **与注册矛盾，以注册为准**。
  {
    key: 'data-flow',
    to: '/data-flow',
    icon: 'chart',
    titleKey: 'nav.dataFlow',
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
  // 2026-10-08 第一百批：任务类型档案 + 人工修正反馈闭环。
  // ★ admin 档（`admin/handler.go:1413` 用 `admin` 挂载 `RegisterTaskProfileRoutes`）
  //   ⇒ **不设** requiresRole（与上面那席相反）。
  // ★ 这一席同时接上批 98/99 的两个 API 模块 —— 它们此前**没有任何 UI 引用**，
  //   全仓 94 个 API 模块里有 32 个是这种「做完了但用户点不到」的孤儿。
  // ⚠️★ 同族的三个 POST（apply-tier-config / reload / import）**都有写副作用**，本页一律不碰。
  // ⚠️★ 无需同步 `AppDrawer.spec.ts` 的 superAdmin 白名单 —— 那一席只列 super_admin 档的 key。
  {
    key: 'task-profile',
    to: '/task-profile',
    icon: 'grid',
    titleKey: 'nav.taskProfile',
  },
  // 2026-10-08 第一百零一批：路由策略配置面（**superAdmin 档**）。
  // ★ 同族**三档一档**（`admin/handler.go:1200 :1201 :1215` 是 h.superAdmin，
  //   而 `:1216` 的 featured-models 是 admin）⇒ 席设 super_admin。
  // ⇒ ★ 后果：featured-models 在移动端被顺带收严成 superAdmin（前端严于后端）。
  //   这是刻意的取舍 —— 同一页混档会让「谁能看哪块」不可解释。记在视图文件头。
  // ★★ 供应商错误趋势（2026-10-14 第一百一十三批）——
  //   注册 `admin/handler.go:1270` `admin(h.errorsTrendHandlers.getErrorsTrend)`
  //   ⇒ 是 **admin 档**，`tenant_admin` 本该可用（后端 R49-C1 已把它收口到本租户）。
  // ⚠️⚠️ 但本席要设 **`requiresRole: 'admin'`**，不是缺省 —— 这是本仓**第二个**
  //   该档席（第一个是 `users`），推导见 `api/errorsTrend.ts` 文件头：
  //   `AdminMiddleware` 不检 role + `getErrorsTrend` 自己也不检 +
  //   `EffectiveTenantIDAll` 对**非 tenant_admin 一律返空串**（= 查全租户）
  //   ⇒ effective role 为 `user` 的主体能读到**全租户**。
  //   ⇒ 缺省会命中 `navItemsFor` 的默认分支 `return true`，等于把那个口子做成一键入口。
  //   ⇒ `'super_admin'` 也**不行**：那会连 `tenant_admin` 的本租户数据一起挡掉。
  // ⚠️ 它**不得**进 `AppDrawer.spec.ts` 的 superAdmin 白名单（那是 tenant_admin 掉的那批）。
  {
    key: 'errors-trend',
    to: '/errors-trend',
    icon: 'chart',
    titleKey: 'nav.errorsTrend',
    requiresRole: 'admin',
  },
  // ★★ 凭据模型状态变更历史（2026-10-14 第一百一十四批）——
  //   挂载 `admin/handler.go:1475` `monitorH.RegisterMonitorRoutes(mux, h.admin)`
  //   ⇒ 是 **admin 档**，与上面 errors-trend **同档同处置**。
  // ⚠️ 同样要设 `requiresRole: 'admin'` 而不是缺省：这一族的租户键是
  //   **手写内联**的 `tenantID := ""; if IsTenantAdmin(r) { … }`
  //   （admin/credential_monitor.go:1532-1535），与 `EffectiveTenantIDAll` 语义相同
  //   但**不叫那个名字** ⇒ ★★ grep `EffectiveTenantID` 找不到这一处。
  // ⇒ 缺省会让 effective role 为 `user` 的主体读到全租户模型状态历史。
  // ⚠️ 它**不得**进 `AppDrawer.spec.ts` 的 superAdmin 白名单（tenant_admin 要能看到）。
  {
    key: 'model-history',
    to: '/model-history',
    icon: 'server',
    titleKey: 'nav.modelHistory',
    requiresRole: 'admin',
  },
  // ★★ 凭据模型滑窗调用明细（2026-10-14 第一百一十五批）——
  //   与 errors-trend / model-history **同注册处**，但**处置相反**。
  // ⚠️⚠️ 这里要设 **`requiresRole: 'super_admin'`** 而不是 `'admin'`：
  //   `handleSlidingWindow`（admin/credential_monitor.go:704-782）里
  //   **既没有 `tenantID` 变量也没有 `IsTenantAdmin`** ⇒ Redis 与
  //   `_request_logs` 回退两条腿的 WHERE 都**只有 credential_id**，
  //   而 `credential_id` 是**全局唯一**主键
  //   ⇒ 任何通过认证的主体报出别的租户的 credential_id 就能读到它的逐次调用明细。
  //   ⇒ `'admin'` 档会给出「按钮写着我的租户、后端根本不理这句」的**虚假安全感**。
  // ⇒ ★ 所以它**必须**进 `AppDrawer.spec.ts` 的 superAdmin 白名单（与上面两席相反）。
  {
    key: 'sliding-window',
    to: '/sliding-window',
    icon: 'chart',
    titleKey: 'nav.slidingWindow',
    requiresRole: 'super_admin',
  },
  // ★ 模型智商（2026-10-14 第一百一十六批）——
  //   注册 `admin/handler.go:1492-1495`，**四条全部** `h.superAdmin(...)`。
  // ⇒ 本族**必须**设 `requiresRole: 'super_admin'`，
  //   并同步 `src/components/shell/AppDrawer.spec.ts` 的排序白名单。
  // ⚠️ 与批 113/114 那两族**相反**：那两族走 `AdminMiddleware` 只认证不检 role，
  //   所以用 `'admin'`；本族走 `SuperAdminMiddleware`（auth.go:376-409）
  //   **逐字比对 claims.Role** ⇒ 档位本身是真门。
  // ★★ 但「抽屉隐藏 ≠ 路由拦截」：`meta.requiresRole` 全仓无人消费，
  //   守卫只查 `requiresAuth` 与登录态 ⇒ 深链照样打开 ⇒ 视图内还要兜一层。
  {
    key: 'model-iq',
    to: '/model-iq',
    icon: 'chart',
    titleKey: 'nav.modelIQ',
    requiresRole: 'super_admin',
  },
  // ★ 运维总览（2026-10-14 第一百一十七批）——
  //   注册 `admin/handler.go:1091` 的 `admin(...)`，而 `AdminMiddleware`
  //   （`admin/auth.go:85-140`）**只认证不检 role** ⇒ 后端 tenant_admin **能**读。
  // ★★★★ 但 `handleOpsOverview` 自己不检 role，而 11 条子查询**全是全局表**
  //   （取租户键的代码不存在）⇒ 归第三类：**完全没有租户维度** ⇒ 过度限制。
  // ⇒ 本席**必须**设 `requiresRole: 'super_admin'`，并同步
  //   `src/components/shell/AppDrawer.spec.ts` 的排序白名单。
  // ⚠️ 这一族与批 113/114 那两席（'admin' 档）的**成因相反**：那一族服务端**有**租户维度，
  //   只是租户键可能返空串；本族是**一个租户键都没有**。
  // ⚠️ 前端严于后端是兜底不是修复：后端这一族**没有门**。
  {
    key: 'ops-overview',
    to: '/ops-overview',
    icon: 'cpu',
    titleKey: 'nav.opsOverview',
    requiresRole: 'super_admin',
  },
  // ★ 请求动作时间线（2026-10-14 第一百一十八批）——
  //   注册 `admin/handler.go:1045` 的 `admin(...)` ⇒ 后端 tenant_admin **能**读，
  //   但该端点**完全没有租户维度**（只按 URL 里的 `request_id` 精确匹配），
  //   而读**同一个** store 的 `/api/admin/live-stream` **有**租户过滤
  //   ⇒ 后端不一致 ⇒ 归第三类，只能**过度限制**到 super_admin。
  // ⇒ 本席**必须**设 `requiresRole: 'super_admin'` 并同步 `AppDrawer.spec.ts` 的排序白名单。
  // ⚠️ 与批 115 `sliding-window` 同形态：`credential_id` 是全局主键、`request_id` 是
  //   全局随机 id，两边都是「拿到那个标识就能读到明细」。
  {
    key: 'request-actions',
    to: '/request-actions',
    icon: 'search',
    titleKey: 'nav.requestActions',
    requiresRole: 'super_admin',
  },
  // ★ v1 数据地平线（2026-10-14 第一百一十九批）——
  //   注册 `admin/handler.go:1109` 的 `admin(...)` ⇒ **admin 档**（不设 requiresRole）。
  // ⚠️ 与批 115/117/118 三席**相反**，而它们并没错：本族没有租户维度，
  //   但载荷是**平台级全局事实**（七个键无一带租户）
  //   ⇒ 过度限制的判据是「载荷里有没有别人的东西」，不是「有没有 tenant 条件」。
  // ⇒ 且**不得**进 `AppDrawer.spec.ts` 的 superAdmin 白名单 —— 加进去那条立刻红。
  { key: 'v1-data-horizon', to: '/v1-data-horizon', icon: 'clock', titleKey: 'nav.v1DataHorizon' },
  // ★ 订阅档（2026-10-14 第一百二十批）——
  //   ★★★★ 权限档**第四类**，与前三席都不同：
  //   它**同样没有租户维度**（读全局 `subscription_tiers`），
  //   **但**中间件 `requireSuperAdmin`（`main_jwt_middleware.go:65-73`）逐字
  //   `if role != "super_admin" { 403 }` ⇒ **真门**。
  // ⇒ 前四席的超管限制分别是「后端不一致/载荷含别人的数据/全局事实」三种理由，
  //   本席的理由是「**服务端自己就是那道门**」⇒ 不必再为它补第四种过度限制论证。
  // ⚠️ 第三种注册机制：它在 echo group 上（`main.go:7027` → `licensing.RegisterModuleRoutes`），
  //   **不在** `admin/handler.go` 的 mux 里 ⇒ 只 grep mux 会误判成死端点。
  {
    key: 'subscription-tiers',
    to: '/subscription-tiers',
    icon: 'grid',
    titleKey: 'nav.subscriptionTiers',
    requiresRole: 'super_admin',
  },
  // ★ 平台设置（2026-10-14 第一百二十一批）——
  //   ★★★★ 第三类（过度限制），但**理由与批 115/117/118 三席都不同**，且更硬：
  //   ① 那三席是「载荷含别人的数据」；本族**注册处包的是一个 router**，
  //      `h.admin` 只是 `AdminMiddleware`（handler.go:894 → auth.go:85，只认证不检 role），
  //      `settingsGet` / `settingsHistory` **全文零 role 检查**
  //      ⇒ 按注册处读会读出「admin 档」，那是**错的**（模块头 (1) 已订正）。
  //   ② 过度限制的**两条独立理由**：
  //      · history 传 `tid=""` 且 `operator_user`/`operator_role` 是恒在键
  //        ⇒ tenant_admin 能读到**别的操作者**的身份；
  //      · `POST {key}/rollback` **完全没有 DangerLevel 门**，而 PUT 有
  //        ⇒ 能 PUT 不了的档能直接 rollback（**后端缺陷，本批不动后端**）。
  // ⇒ 前端严于后端是**兜底不是修复**：真正该补的是服务端那道门。
  {
    key: 'platform-settings',
    to: '/platform-settings',
    icon: 'grid',
    titleKey: 'nav.platformSettings',
    requiresRole: 'super_admin',
  },
  // ★ 用量成本增强（2026-10-14 第一百二十二批）——
  //   ★★★★ 权限档**第一类**（有租户维度）：三个端点都按租户过滤
  //   ⇒ 各租户互相隔离，不是过度限制。
  //   ★ 但**必须**设 `requiresRole: 'admin'`（不能像同族的 trend-series 那样不设）：
  //   `h.admin` 无角色下限 + `supportedRole` 接受 `user`
  //   ⇒ 服务端会把「全部租户的合计」发给普通 user。
  //   ⇒ `navItemsFor` 的 'admin' 档放行 admin 三档、挡掉 user
  //   ⇒ ★★ 在后端补上门之前，**这是唯一挡住普通 user 的东西**。
  {
    key: 'usage-enhanced',
    to: '/usage-enhanced',
    icon: 'chart',
    titleKey: 'nav.usageEnhanced',
    requiresRole: 'admin',
  },
  // ★ 存储总览（2026-10-14 第一百二十三批）——
  //   ★★★ 'admin' 档，**两条独立理由**（与批 122 同源但结论不同）：
  //   （甲）**不过度限制**：载荷是**纯平台基础设施事实**（零 tenant 命中）⇒ 无理由上 super_admin；
  //   （乙）**但需要 admin**：服务端**没有角色下限**（`admin := h.admin` → AdminMiddleware
  //         + supportedRole 接受 "user"）⇒ 本机磁盘用量没理由给普通用户看。
  //   ★ 连续三批（121/122/123）都栽在同一句「admin 档 ⇒ 不设 requiresRole」上，
  //     根因是同一个：「admin 档」这个标签**掩盖了服务端没有角色下限**。
  {
    key: 'data-lifecycle-storage',
    to: '/data-lifecycle-storage',
    icon: 'grid',
    titleKey: 'nav.dataLifecycleStorage',
    requiresRole: 'admin',
  },
  // ★ 存储与日志配置（2026-10-14 第一百二十四批）——
  //   `h.superAdmin` ⇒ 服务端**真门** ⇒ 抽屉席必须设 super_admin 并进 AppDrawer 白名单。
  //   ★ 第九十三批写这个模块时，模块头**一开始就把权限结论写对了**
  //     （它顺手看了相邻那一行 `storage/migration-state` 是 admin 档，
  //     并写下「绝不能按 /api/admin/storage/ 前缀推权限」）——
  //     本批要改的只是那几处**漂了 21 行的行号**，
  //     结论一个字都不用改（这在九批里是第一次）。
  {
    key: 'storage-log-config',
    to: '/storage-log-config',
    icon: 'grid',
    titleKey: 'nav.storageAndLogConfig',
    requiresRole: 'super_admin',
  },
  // ★ 附件目录迁移进度（2026-10-15 第一百二十五批）——
  //   `admin/handler.go:1132` 的 `admin(...)` ⇒ **服务端没有角色下限**
  //   （`admin := h.admin` → AdminMiddleware，`pkg/identity` 的 supportedRole 接受 "user"）
  //   ⇒ 与**上一席只隔一行**的 `storage-log-config`（`:1129` h.superAdmin，真门）
  //      **档位正好相反** ⇒ 绝不能按 `/api/admin/storage/` 前缀推权限。
  //   ★ 'admin' 档的两条独立理由：零租户维度（不过度限制）＋ 需要角色下限（不挡 user）。
  //   ⚠️ 'admin' 档**不得**进 AppDrawer.spec.ts 的 superAdmin 白名单（那是 tenant_admin 掉的）。
  {
    key: 'storage-migration-state',
    to: '/storage-migration-state',
    icon: 'grid',
    titleKey: 'nav.storageMigrationState',
    requiresRole: 'admin',
  },
  // ★ 加这一席**必须**同步 `src/components/shell/AppDrawer.spec.ts` 的白名单。
  {
    key: 'routing-policy',
    to: '/routing-policy',
    icon: 'chart',
    titleKey: 'nav.routingPolicy',
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
 * `admin` 表示管理员工作台角色（tenant_admin / super_admin / legacy admin_key）；
 * `super_admin` 表示仅超级管理员。其余用户不能访问管理入口。
 */
export function navItemsFor(items: readonly NavItem[], role: string | null | undefined): NavItem[] {
  const isSuper = role === 'super_admin'
  const isAdmin = isSuper || role === 'tenant_admin' || role === 'admin_key'
  return items.filter((item) => item.requiresRole === 'super_admin'
    ? isSuper
    : item.requiresRole === 'admin'
      ? isAdmin
      : true)
}
