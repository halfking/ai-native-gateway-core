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
