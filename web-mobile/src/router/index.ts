import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { appBase } from '@/utils/base'

// 路由表（06 §4：Vue Router = 路由真源）。
// base '/m/'：生产经网关 /m/* 挂载；dev 下 vite 同样以 /m/ 提供（vite.config
// base '/m-assets/' + SPA fallback）。titleKey 驱动 TitleResolver 优先级 4。

declare module 'vue-router' {
  interface RouteMeta {
    titleKey?: string
    headerless?: boolean
    requiresAuth?: boolean
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { titleKey: 'login.title', headerless: true, requiresAuth: false },
  },
  {
    path: '/',
    name: 'home',
    component: () => import('@/views/HomeView.vue'),
    meta: { titleKey: 'home.title', requiresAuth: true },
  },
  {
    path: '/nodes',
    name: 'nodes',
    component: () => import('@/views/NodesView.vue'),
    meta: { titleKey: 'nodes.title', requiresAuth: true },
  },
  {
    path: '/models',
    name: 'models',
    component: () => import('@/views/ModelsView.vue'),
    meta: { titleKey: 'models.title', requiresAuth: true },
  },
  {
    path: '/keys',
    name: 'keys',
    component: () => import('@/views/KeysView.vue'),
    meta: { titleKey: 'keys.title', requiresAuth: true },
  },
  {
    path: '/alerts',
    name: 'alerts',
    component: () => import('@/views/AlertsView.vue'),
    meta: { titleKey: 'alerts.title', requiresAuth: true },
  },
  {
    path: '/usage',
    name: 'usage',
    component: () => import('@/views/UsageView.vue'),
    meta: { titleKey: 'usage.title', requiresAuth: true },
  },
  {
    path: '/logs',
    name: 'logs',
    component: () => import('@/views/RequestLogsView.vue'),
    meta: { titleKey: 'logs.title', requiresAuth: true },
  },
  {
    path: '/integrity',
    name: 'integrity',
    component: () => import('@/views/IntegrityView.vue'),
    meta: { titleKey: 'integrity.title', requiresAuth: true },
  },
  {
    path: '/providers',
    name: 'providers',
    component: () => import('@/views/ProvidersView.vue'),
    meta: { titleKey: 'providers.title', requiresAuth: true },
  },
  {
    path: '/routing',
    name: 'routing',
    component: () => import('@/views/RoutingCheckView.vue'),
    meta: { titleKey: 'routing.title', requiresAuth: true },
  },
  // 运维排障线（2026-10-07）。三条都走 AdminMiddleware ⇒ tenant_admin 可用，
  // 所以 nav 的 requiresRole 一律不设（只有 super_admin 档才设，见 /integrity）。
  {
    path: '/journey',
    name: 'journey',
    component: () => import('@/views/RequestJourneyView.vue'),
    meta: { titleKey: 'journey.title', requiresAuth: true },
  },
  {
    // ★ 详情必须排在 /journey 之前？—— 不需要：vue-router 5 的静态段优先于
    //   动态段，且此处用的是命名子路径而非通配，不存在 /journey/:id 吃掉
    //   /journey 本身的问题。request_id 已在 fetchJourneyDetail 里 encodeURIComponent。
    path: '/journey/:id',
    name: 'journey-detail',
    component: () => import('@/views/RequestJourneyDetailView.vue'),
    meta: { titleKey: 'journey.detailTitle', requiresAuth: true },
  },
  {
    path: '/routing-log',
    name: 'routing-log',
    component: () => import('@/views/RoutingLogView.vue'),
    meta: { titleKey: 'routingLog.title', requiresAuth: true },
  },
  {
    path: '/waterfall',
    name: 'waterfall',
    component: () => import('@/views/DispatchWaterfallView.vue'),
    meta: { titleKey: 'waterfall.title', requiresAuth: true },
  },
  {
    path: '/turns',
    name: 'turns',
    component: () => import('@/views/TurnsView.vue'),
    meta: { titleKey: 'turns.title', requiresAuth: true },
  },
  {
    path: '/routing-audit',
    name: 'routing-audit',
    component: () => import('@/views/RoutingAuditView.vue'),
    meta: { titleKey: 'routingAudit.title', requiresAuth: true },
  },
  // 凭据监控线（2026-10-07）：热力图 + 单凭据健康时间线。两条都 admin 档。
  {
    path: '/heatmap',
    name: 'heatmap',
    component: () => import('@/views/CredentialHeatmapView.vue'),
    meta: { titleKey: 'heatmap.title', requiresAuth: true },
  },
  {
    path: '/node-health/:id',
    name: 'node-health',
    component: () => import('@/views/NodeHealthView.vue'),
    meta: { titleKey: 'nodeHealth.title', requiresAuth: true },
  },
  // 探测面（2026-10-07）：探测队列 + 供应商探测延时。adminWrap 档。
  {
    path: '/probe',
    name: 'probe',
    component: () => import('@/views/ProbeView.vue'),
    meta: { titleKey: 'probe.title', requiresAuth: true },
  },
  // 路由覆盖规则（2026-10-07）。★ superAdmin 档：handler.go:1381 把
  // RegisterAutoRouteRoutes 挂在 h.superAdmin 下 ⇒ tenant_admin 必 403。
  {
    path: '/overrides',
    name: 'overrides',
    component: () => import('@/views/RoutingOverridesView.vue'),
    meta: { titleKey: 'overrides.title', requiresAuth: true },
  },
  // ── 自动调优面（2026-10-07）────────────────────────────────────────────
  // 三页凑成一个闭环：/overrides 答「现在生效的规则是什么」，
  // /funnel 答「请求进来后被筛掉了多少、这数可不可信」，
  // /proposals 答「系统认为规则该怎么调、哪些已经落地」。
  // 只看规则不知道规则对不对；只看漏斗不知道该怎么改；只看建议不知道哪些已生效。
  // ★ funnel 走 analytics.go:58 的 adminWrap（handler.go:1430 用 h.superAdmin
  //   调 RegisterAnalyticsRoutes）；proposals 走 auto_route.go:116 的 adminWrap，
  //   而 RegisterAutoRouteRoutes 本身在 handler.go:1381 就是 h.superAdmin
  //   ⇒ 两条都必挡。
  {
    path: '/funnel',
    name: 'funnel',
    component: () => import('@/views/RouteFunnelView.vue'),
    meta: { titleKey: 'funnel.title', requiresAuth: true },
  },
  {
    path: '/proposals',
    name: 'proposals',
    component: () => import('@/views/TuningProposalsView.vue'),
    meta: { titleKey: 'proposals.title', requiresAuth: true },
  },
  // 自动路由读面：答「现在跑得怎么样、花了多少钱」——
  // /proposals 答建议，本页答建议落地后的效果。两者之间缺的就是这一页。
  // 五段：audit 聚合 / 凭据×模型索引 / 客户成本 / 模型成本 / 调优准确率。
  // ★ 整条 auto-route 线是 superAdmin（handler.go:1381 与 :1430 都是
  //   h.superAdmin；注意形参名 adminWrap 是假名，值在调用处才定）
  //   ⇒ 抽屉席必须挡，tenant_admin 进去必然 403。
  {
    path: '/auto-route',
    name: 'auto-route',
    component: () => import('@/views/AutoRouteView.vue'),
    meta: { titleKey: 'autoRoute.title', requiresAuth: true },
  },
  // 决策回放是**详情页，不占抽屉席**（口径同 /maas-orders/:id）。
  // id 可选：从 /request-detail 带进来，或用户直接粘一个 id。
  // ★ 端点是 superAdmin 档（同整条 auto-route 线）⇒ 抽屉层挡的是列表入口，
  //   详情页靠页面内 403 文案 + /request-detail 入口按角色隐藏。
  {
    path: '/auto-route-decision/:id?',
    name: 'auto-route-decision',
    component: () => import('@/views/AutoRouteDecisionView.vue'),
    meta: { titleKey: 'autoRouteDecision.title', requiresAuth: true },
  },
  // 看板读面（第九条 dashboard 端点线的 UI 入口）。
  // ★ admin 档：handler.go:1052-1069 九条注册全部是 `admin(...)`
  //   ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** requiresRole。
  // ★ 与 /auto-route 相反（那条整族是 h.superAdmin）—— 别照抄它。
  {
    path: '/dashboard-ops',
    name: 'dashboard-ops',
    component: () => import('@/views/DashboardOpsView.vue'),
    meta: { titleKey: 'dashboardOps.title', requiresAuth: true },
  },
  // 日志运维面（logOps 三条只读端点）。
  // ★ admin 档：`/api/admin/logs/{stats,files,body-cache-stats}` 都是 admin(...)
  //   ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** requiresRole。
  // ⚠️ 同前缀下 config / archive / cleanup 是 h.superAdmin ——
  //   往后往本页加任何一条，整页档位必须跟着升。
  {
    path: '/log-ops',
    name: 'log-ops',
    component: () => import('@/views/LogOpsView.vue'),
    meta: { titleKey: 'logOps.title', requiresAuth: true },
  },
  // 人工标注工作台（三条**只读**端点；写操作三条一律不碰）。
  // ★ admin 档：handler.go:1386/1389/1390 三条注册全是 admin(...)
  //   ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** requiresRole。
  // ⚠️ 同前缀的 batch / annotations/{id}(DELETE) / annotations(POST)
  //   也都是 admin 档，但它们**改标注事实**（删一条会改变 accuracy 口径），
  //   本页刻意不接。
  {
    path: '/annotations',
    name: 'annotations',
    component: () => import('@/views/AnnotationsView.vue'),
    meta: { titleKey: 'annotations.title', requiresAuth: true },
  },
  // 审批队列（运行中的审批**实例**，只读）。
  // ★ admin 档：cmd/gateway/main.go:7384-7385 两条注册都是 wrapAdmin(...)
  //   ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** requiresRole。
  // ★ 与 /approval-config（配置）、/approval-rules（审批人与规则）不重叠：
  //   那两页答「该问谁 / 什么条件下拦」，本页答「现在有几条在等」。
  {
    path: '/approval-queue',
    name: 'approval-queue',
    component: () => import('@/views/ApprovalQueueView.vue'),
    meta: { titleKey: 'approvalQueue.title', requiresAuth: true },
  },
  // ── 全局横向对比面（2026-10-07）──────────────────────────────────────
  // 与 /funnel 配对：漏斗是单模型纵深，矩阵/流量是全体模型横向对比。
  // ★ 两条都是 superAdmin：matrix/flow 在 RegisterAnalyticsRoutes 里
  //   （analytics.go:55-58），而该注册由 handler.go:1430 用 h.superAdmin 调。
  {
    path: '/matrix',
    name: 'matrix',
    component: () => import('@/views/RouteMatrixView.vue'),
    meta: { titleKey: 'matrix.title', requiresAuth: true },
  },
  {
    path: '/flow',
    name: 'flow',
    component: () => import('@/views/RouteFlowView.vue'),
    meta: { titleKey: 'flow.title', requiresAuth: true },
  },
  // 探测系统健康 + 队列快照（2026-10-07）。admin 档：两条都在
  // RegisterProbeDashboardRoutes(mux, wrapAdmin)（probe_dashboard.go:1900-1909，
  // 注册点 cmd/gateway/main.go:7298）⇒ tenant_admin 可用，不设 requiresRole。
  // ★ 这两个端点是双轨形状（顶层字段全是 legacy 的），实现见 probeHealth.ts。
  {
    path: '/probe-health',
    name: 'probe-health',
    component: () => import('@/views/ProbeHealthView.vue'),
    meta: { titleKey: 'probeHealth.title', requiresAuth: true },
  },
  // 模型级健康总览（2026-10-07）。admin 档，同 RegisterProbeDashboardRoutes。
  {
    path: '/probe-model',
    name: 'probe-model',
    component: () => import('@/views/ProbeModelHealthView.vue'),
    meta: { titleKey: 'probeModel.title', requiresAuth: true },
  },
  // 可用性时间线 + Redis 可用性缓存快照（2026-10-07）。admin 档。
  {
    path: '/timeline',
    name: 'timeline',
    component: () => import('@/views/AvailabilityTimelineView.vue'),
    meta: { titleKey: 'timeline.title', requiresAuth: true },
  },
  {
    path: '/cache-state',
    name: 'cache-state',
    component: () => import('@/views/CacheStateView.vue'),
    meta: { titleKey: 'cache.title', requiresAuth: true },
  },
  // 自动路由的「模型 × 任务」表现索引（2026-10-07）。**superAdmin 档**
  // （admin/handler.go:1430 RegisterAnalyticsRoutes 用 h.superAdmin）。
  {
    path: '/task-index',
    name: 'task-index',
    component: () => import('@/views/ModelTaskIndexView.vue'),
    meta: { titleKey: 'taskIndex.title', requiresAuth: true },
  },
  // 会话运维面（2026-10-06）。三条都挂 wrapAdmin / admin(...) ⇒ **admin 档**，
  // tenant_admin 可用 ⇒ 导航不设 requiresRole。
  // ★ 注意同族 `/sessions/summary` 是 **POST-only**，本页不碰它。
  {
    path: '/session-audit',
    name: 'session-audit',
    component: () => import('@/views/SessionAuditView.vue'),
    meta: { titleKey: 'sessions.title', requiresAuth: true },
  },
  {
    path: '/sessions-online',
    name: 'sessions-online',
    component: () => import('@/views/OnlineSessionsView.vue'),
    meta: { titleKey: 'online.title', requiresAuth: true },
  },
  // 系统监控（stats + recent-runs，2026-10-06）。**admin 档**（adminWrap）
  // ⇒ tenant_admin 可用，导航不设 requiresRole。
  // ★ 同族的 by-credential / by-provider / by-model / concurrency 是 superAdmin，
  //   且 concurrency 是 PATCH 写操作 ⇒ 本页都不碰。
  {
    path: '/system-monitor',
    name: 'system-monitor',
    component: () => import('@/views/SystemMonitorView.vue'),
    meta: { titleKey: 'sysmon.title', requiresAuth: true },
  },
  // 数据生命周期（partitions + storage/tables，2026-10-06）。**admin 档**（admin()）
  // ⇒ tenant_admin 可用，导航不设 requiresRole。
  // ★ 同族其余全是 superAdmin 且多数是写操作（archive/drop/vacuum/reindex/
  //   promote/blobs-cleanup-execute/degradation-control），本页一条都不碰。
  {
    path: '/data-lifecycle',
    name: 'data-lifecycle',
    component: () => import('@/views/DataLifecycleView.vue'),
    meta: { titleKey: 'lifecycle.title', requiresAuth: true },
  },
  // 数据流与大字段（stats/metrics/jobs/blobs-top，2026-10-08）。**admin 档**（admin(...)）
  // ⇒ tenant_admin 可用，导航不设 requiresRole。
  // ★★★ 与上面的 /data-lifecycle 按「答什么」划界，不合并：
  //   /data-lifecycle 答「数据库这一层什么状态」（分区清单 + 表体积榜，**表级**），
  //   本页答「记录怎么分布 / 有没有在清理 / 大字段占多少」（**记录级**）。
  //   合并会把两种粒度混在一起，且本页的 `metrics` 是**全表口径**
  //   （data_lifecycle_metrics.go:63 的 FROM request_logs 无 WHERE），
  //   与 /data-lifecycle 的租户/榜内口径并排会给出错误对比。
  // ★ 同族的 cleanup/preview（POST 且 action 含 delete）、blobs/cleanup/*、
  //   partitions/*、hot/promote 是写操作或 superAdmin，本页一条都不碰。
  {
    path: '/data-flow',
    name: 'data-flow',
    component: () => import('@/views/DataFlowView.vue'),
    meta: { titleKey: 'dataFlow.title', requiresAuth: true },
  },
  // 路由优化器（stats/accuracy/parameters/metrics，2026-10-07）。**admin 档**（admin(...)）
  // ⇒ tenant_admin 可用，导航不设 requiresRole。
  // ★ 同族的 proposals/{approve,reject}、probe/cache-rebuild、
  //   hot/cron/stats 是 superAdmin 或写操作，本页一条都不碰。
  {
    path: '/routing-opt',
    name: 'routing-opt',
    component: () => import('@/views/RoutingOptView.vue'),
    meta: { titleKey: 'routingOpt.title', requiresAuth: true },
  },
  // 待处理响应（list + stats + detail，2026-10-07）。**admin 档**（admin(...)）
  // ⇒ tenant_admin 可用，导航不设 requiresRole。
  // ★ 写操作 DELETE /{sessionID}（手动清理挂起条目）本页不碰。
  {
    path: '/pending-responses',
    name: 'pending-responses',
    component: () => import('@/views/PendingResponsesView.vue'),
    meta: { titleKey: 'pending.title', requiresAuth: true },
  },
  // 请求侧异常（list + count，2026-10-07）。**superAdmin 档**（h.superAdmin）
  // ⇒ tenant_admin 直接 403，抽屉席**必须**设 requiresRole。
  // ★ 写操作 POST /{id}/resolve 与 POST /batch-resolve 本页不碰。
  {
    path: '/request-anomalies',
    name: 'request-anomalies',
    component: () => import('@/views/RequestAnomaliesView.vue'),
    meta: { titleKey: 'anomalies.title', requiresAuth: true, requiresRole: 'super_admin' },
  },
  // 输出合规「命中与复核」面（stats/records/review-queue，2026-10-07）。**admin 档**
  // （AdminMiddleware）⇒ tenant_admin 可用，导航不设 requiresRole。
  // ★ 写操作 approve/reject 本页不碰。
  {
    path: '/compliance-hits',
    name: 'compliance-hits',
    component: () => import('@/views/ComplianceHitsView.vue'),
    meta: { titleKey: 'compliance.title', requiresAuth: true },
  },
  // 输出合规「策略与词库」面（policy/keywords，2026-10-07）。**admin 档**。
  // ★ 没配策略时后端返回**合成的默认策略**而不是 404（fetchPolicy 的 ErrNoRows 分支）。
  {
    path: '/compliance-policy',
    name: 'compliance-policy',
    component: () => import('@/views/CompliancePolicyView.vue'),
    meta: { titleKey: 'compliancePolicy.title', requiresAuth: true },
  },
  // 提示词注入「现象面」（stats/detections/attack-vectors，2026-10-07）。**admin 档**
  // （AdminMiddleware）⇒ 不设 requiresRole。
  // ★ risk_level 是库里的 integer(1..10)，不是等级名。
  {
    path: '/injection',
    name: 'injection',
    component: () => import('@/views/InjectionView.vue'),
    meta: { titleKey: 'injection.title', requiresAuth: true },
  },
  // 提示词注入「配置面」（rules/engines/severity-matrix/canary-tokens，2026-10-07）。
  // ★ rules/engines/canary-tokens **没有分页**（后端 SQL 无 LIMIT）。
  {
    path: '/injection-config',
    name: 'injection-config',
    component: () => import('@/views/InjectionConfigView.vue'),
    meta: { titleKey: 'injectionConfig.title', requiresAuth: true },
  },
  // 会话分析面：客户端维度 / 任务维度（admin 档，2026-10-08）。
  // ★ 数据源是**没有周期刷新路径**的物化视图，页面顶部必须显示 refreshed_at。
  {
    path: '/session-analytics',
    name: 'session-analytics',
    component: () => import('@/views/SessionAnalyticsView.vue'),
    meta: { titleKey: 'sa.title', requiresAuth: true },
  },
  // 统一请求详情（admin 档，2026-10-08）。id 可选：从列表点进来时由 query/param 带入。
  {
    path: '/request-detail/:id?',
    name: 'request-detail',
    component: () => import('@/views/RequestDetailView.vue'),
    meta: { titleKey: 'rd.title', requiresAuth: true },
  },
  // MaaS 模型积分价（**superAdmin 档**，2026-10-08：maas_handlers.go 全部 11 条注册都是 h.superAdmin）。
  // ★ 页面必须逐维标注生效价来源：响应里的 credits_per_1m_* 是算出来的，不是库里存的值。
  {
    path: '/maas-rates',
    name: 'maas-rates',
    component: () => import('@/views/MaasRatesView.vue'),
    meta: { titleKey: 'maas.title', requiresAuth: true },
  },
  // MaaS 订单列表（**superAdmin 档**，2026-10-08）。
  // ★ 这条端点没有 offset/cursor，只有 limit ⇒ 页面不能有「下一页」控件。
  // ★ 列表恒无 payment_hint / stub_mode（enrich 只在 GetOrder）⇒ 必须指向详情页。
  {
    path: '/maas-orders',
    name: 'maas-orders',
    component: () => import('@/views/MaasOrdersView.vue'),
    meta: { titleKey: 'mo.title', requiresAuth: true },
  },
  // MaaS 订单详情（superAdmin 档）。响应是**裸对象**；404 身兼「不存在」与「查询失败」两职。
  // ★ 详情页不占抽屉席（只有列表页进 DRAWER_NAV）。
  {
    path: '/maas-orders/:id',
    name: 'maas-order-detail',
    component: () => import('@/views/MaasOrderDetailView.vue'),
    meta: { titleKey: 'mo.detailTitle', requiresAuth: true },
  },
  // MaaS 租户/客户面（**admin 档**，2026-10-08：/api/maas/** 是 h.admin，不是 superAdmin）。
  // ★ 与 /api/admin/maas/** 那一族同域不同档 —— 抽屉席**不能**设 requiresRole。
  // ★ 模型价目这条端点**只有 4 维**，没有 image/audio/video ⇒ 页面不许画七维。
  {
    path: '/maas-catalog',
    name: 'maas-catalog',
    component: () => import('@/views/MaasCatalogView.vue'),
    meta: { titleKey: 'mpol.title', requiresAuth: true },
  },
  // MaaS 本租户钱包。★ 这条 GET 端点**会写库**（ensureWalletDirect 建行）。
  {
    path: '/maas-wallet',
    name: 'maas-wallet',
    component: () => import('@/views/MaasWalletView.vue'),
    meta: { titleKey: 'mw.title', requiresAuth: true },
  },
  // MaaS 租户运维面（**superAdmin 档**，2026-10-08：/api/admin/maas/tenants/** 全 superAdmin）。
  // ★★ 这两个 usage 端点**按 days 换物理表**（≤7 读 request_logs_hot）。
  {
    path: '/maas-tenant-ops',
    name: 'maas-tenant-ops',
    component: () => import('@/views/MaasTenantOpsView.vue'),
    meta: { titleKey: 'mt.title', requiresAuth: true },
  },
  // MaaS 配置面（superAdmin 档）：完整 settings + 含停用行的套餐/充值包。
  // ★ 与 admin 档的 /maas-catalog 形状相近但**内容差异大**（折扣、基价、停用行）。
  {
    path: '/maas-admin-catalog',
    name: 'maas-admin-catalog',
    component: () => import('@/views/MaasAdminCatalogView.vue'),
    meta: { titleKey: 'mac.title', requiresAuth: true },
  },
  // 租户名录（**superAdmin 档**，2026-10-08：admin/handler.go:926-927 两条注册
  // 都是 h.superAdmin）。★ 响应是**裸数组**，不是 {items}。
  // ★ 列表里的 7 天用量**可能不可信**：后端 attachTenantUsage7d 有独立 1.5s 预算，
  //   失败只 slog.Warn 然后把四个字段留在 0。
  {
    path: '/tenants',
    name: 'tenants',
    component: () => import('@/views/TenantsView.vue'),
    meta: { titleKey: 'tn.title', requiresAuth: true },
  },
  // 租户详情：基本信息 + 用户 + 密钥 + 用量统计。详情页不占抽屉席。
  // ★★ stats 失败会返回 **504**（调小 days 重试），不是「查不到」。
  {
    path: '/tenant-detail/:code',
    name: 'tenant-detail',
    component: () => import('@/views/TenantDetailView.vue'),
    meta: { titleKey: 'tnd.title', requiresAuth: true },
  },
  // 租户模型策略（**superAdmin 档**，2026-10-08：admin/handler.go:926-927 两条注册
  // 都是 h.superAdmin，而 handleTenantModelPolicies 只有这一个调用点 ⇒ 整棵子树
  // 都是 superAdmin 硬门槛。后端文件头把 check 标成 `(admin)` 是**过时的**。
  // ★★ check 端点**不做租户隔离**：SQL 里只有 canonical_name，tenantCode 没参与。
  {
    path: '/model-policies',
    name: 'model-policies',
    component: () => import('@/views/ModelPoliciesView.vue'),
    meta: { titleKey: 'mpol.title', requiresAuth: true },
  },
  // 租户模型策略审计。★ ORDER BY ts DESC LIMIT n，**无 OFFSET 无游标** ⇒ 没有「更早」。
  // ★★ 空审计不能当成「没变更过」：租户码拼错也是 200 + 空（withTenantTx 不验租户）。
  {
    path: '/model-policy-audit',
    name: 'model-policy-audit',
    component: () => import('@/views/ModelPolicyAuditView.vue'),
    meta: { titleKey: 'mpolAudit.title', requiresAuth: true },
  },
  // 租户审批配置（**admin 档**，2026-10-08：走 wrapAdmin = admin.AdminMiddleware，
  // 档位与 model-policies 相反 ⇒ tenant_admin 可用，抽屉席**不设** requiresRole）。
  // ★★ 路径是 `tenant-approval-config`（**单数** tenant）：打复数会落进
  //   handleTenants 的 `unknown sub-resource` 404。
  // ★★★★★★ 无配置行时 GetConfig 返回**合成默认配置**（不是 404），
  //   其中 timeout_seconds=3600 与 auto_reject=true 是**凭空造的**。
  {
    path: '/approval-config',
    name: 'approval-config',
    component: () => import('@/views/ApprovalConfigView.vue'),
    meta: { titleKey: 'acfg.title', requiresAuth: true },
  },
  // 审批人与规则。★ 这两个端点的 SQL **都带 AND enabled = true** ⇒ 停用的不在这儿。
  // ★ 空列表序列化成 **null**（nil 切片），不是 []。
  {
    path: '/approval-rules',
    name: 'approval-rules',
    component: () => import('@/views/ApprovalRulesView.vue'),
    meta: { titleKey: 'acfgRules.title', requiresAuth: true },
  },
  // 附件留存清单（**admin 档**，2026-10-08：handler.go:998-1008 六条全是 admin(...)）。
  // ★ 但**同一前缀下混着两条 superAdmin**（cleanup/execute、filesystem/cleanup）
  //   ⇒ 移动端不能按前缀判权限。
  // ★★ 同一个 `attachments` 字段，list 侧无 COALESCE（可为 JSON 标量 null，18k+ 行），
  //   item 侧 COALESCE 成 [] ⇒ 两处 nullability 不同。
  // ★ 时间参数解析失败被**静默丢弃**（后端 if err == nil 才赋值）⇒ 返回全时间范围且不报错。
  {
    path: '/attachments',
    name: 'attachments',
    component: () => import('@/views/AttachmentsView.vue'),
    meta: { titleKey: 'att.title', requiresAuth: true },
  },
  // 功能模块面（**admin 档**，2026-10-08：注册在 `admin/modules.go:1129-1134`
  //   的 `registerModuleRoutes` 里，**不在** handler.go —— grep 路由必须限全仓）。
  // ★★★★★★ `enabled:true` 可能是 `resolveModuleEnabled` 五条失败路径的兜底，
  //   而且 `EffectiveValue` 回落 spec 默认值时**也**返回 `source:"default"`
  //   ⇒ `source` 只有 {db, env, default} 三个值，只有 db/env 算「真读到」。
  // ★★ `POST /{key}/test` **真给飞书机器人发消息**（有外部副作用）⇒ 移动端绝不调用；
  //   `PUT /{key}/toggle` 是写操作 ⇒ 也不碰。
  {
    path: '/modules',
    name: 'modules',
    component: () => import('@/views/ModulesView.vue'),
    meta: { titleKey: 'mods.title', requiresAuth: true },
  },
  // 详情页**不占抽屉席**。`/config` 子端点只有 feishu_bot 实现，其余一律 501
  //   ⇒ 本仓首次出现的状态码，页面单列渲染。
  {
    path: '/modules/:key',
    name: 'module-detail',
    component: () => import('@/views/ModuleDetailView.vue'),
    meta: { titleKey: 'modsDetail.title', requiresAuth: true },
  },
  // 日志管理读面（**admin 档**，2026-10-08：handler.go:959,1115,1116,1119 四条只读
  //   全是 `admin(...)`）。★ 但**同一前缀下混着三条 superAdmin**
  //   （config/archive/cleanup）⇒ 移动端不能按前缀判权限。
  // ★★ 同一个「文件日志没启用」在三个端点上是三个判据：
  //   files `dir === ''` / stats `log_dir === ''` / archive-list **`dir` 键不存在**。
  // ★★ `archive/list` 是异形端点：`dir`/`exists` 条件存在。
  // ★★ `files` 的 `is_archived` 恒 false（后端硬编码），且列表不含归档。
  {
    path: '/log-admin',
    name: 'log-admin',
    component: () => import('@/views/LogAdminView.vue'),
    meta: { titleKey: 'logsAdmin.title', requiresAuth: true },
  },
  // 会话上下文读面（**admin 档**，2026-10-07：handler.go:1296 两条只读）。
  // ⚠️★ 前缀是 **`/api/system/session-context/`**，**不是** `/api/admin/`。
  // ★★★★★★ extraction-status 是**异形端点**：未抽取只有 2 个键、已抽取 8 个键。
  // ★★★★★★ `extracted:false` 三种成因（不属于你的租户 / 没抽过 / **DB 查询出错**）
  //   **完全分不开**，且数据库故障也返回 200（从不 404）。
  // ★★★★★★★ titles/batch 的 map 键含**字面 NUL**（`taskId + "\0" + scoped.trim()`）
  //   ⇒ 按 taskId 直查永远 miss；且它是**只读语义但只收 POST**。
  {
    path: '/session-context',
    name: 'session-context',
    component: () => import('@/views/SessionContextView.vue'),
    meta: { titleKey: 'sctx.title', requiresAuth: true },
  },
  // 免费资源自动发现读面（**admin 档**，2026-10-07：handler.go:1302-1313 六条只读）。
  // ⚠️★ 抽屉席是 **admin 档**（不设 requiresRole），但同族第七条
  //   `scan-scheduler/status` 是 **h.superAdmin(...)**（handler.go:1316）
  //   ⇒ 由页面内单独探测、单列 403 态，不占抽屉席。
  // ★★★★★★★★★★ `ListTasks` 的 SELECT **漏了 `updated_at`**（discovery_engine.go:460-464）
  //   ⇒ `GET /tasks` 里每个任务的 `updated_at` 恒为 `"0001-01-01T00:00:00Z"`，
  //   而 `GET /tasks/{id}` 给真值 ⇒ 同字段两端点两个值都 200。
  // ★★★★★★★★ `templates` 有 nil-guard（永不为 null）、`presets` **没有**
  //   （`var out []presetView`）⇒ 「空」在本族有两种表示。
  // ★★★★★★★ `fdTenant` = `EffectiveTenantID` ⇒ **super_admin 看到的也只是
  //   `default` 一个租户**，不是全部租户。
  // ★ `limit` 超上界是**回落 50 而不是 clamp 到 200**（注释写的是 capped at 200）。
  // ★ 五个写端点（templates POST / {id} PUT|PATCH|DELETE / scan / import / import-orbi）本页不碰。
  {
    path: '/free-discovery',
    name: 'free-discovery',
    component: () => import('@/views/FreeDiscoveryView.vue'),
    meta: { titleKey: 'fd.title', requiresAuth: true },
  },
  // 凭据 × 模型实时状态（**superAdmin 档**，2026-10-07：
  // credential_state_handlers.go:175 `wrap := h.superAdmin`）。
  // ⚠️★ 与前几批相反：这一条**要**设 requiresRole，抽屉席也**要**设 super_admin。
  // ★★★★★★★★★★ `state` **可以是 `null`** —— 三层缓存（内存→Redis→DB）全 miss，
  //   manager.go:763 返回 `(nil, nil)`，handler 只判 `err != nil`
  //   ⇒ **200 + state:null**，**不是** 404。
  // ★★★★★★ 五条指标在**两条 DB 分支里根本没被赋值** ⇒ 恒 0；
  //   缓存命中时才有真值 ⇒ 同一个 (凭据,模型) 两次查询可能给不同数字，都是 200。
  // ★★★★ 错误响应是 **text/plain**（`http.Error`）且**带尾换行**，不是 JSON 信封。
  {
    path: '/credential-model-state',
    name: 'credential-model-state',
    component: () => import('@/views/CredentialStateView.vue'),
    meta: { titleKey: 'cs.title', requiresAuth: true },
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { titleKey: 'common.notFound', requiresAuth: true },
  },
]

const router = createRouter({
  history: createWebHistory(appBase()),
  routes,
  scrollBehavior: () => ({ top: 0 }), // 内层容器恢复由 Hyper adapter 负责（06 §6）
})

export default router
