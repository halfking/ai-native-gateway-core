import { createRouter, createWebHistory } from 'vue-router'
import { store, isDefaultTenant } from './store'
import { throttledFetch } from './api/_core'
import { showOpsPlatform } from './config/edition'
import { waitForAuthHydration } from './router/authHydration'
import { locationFromInternalPath } from './utils/safeRedirect'

// Critical views loaded immediately (login, home, layout)
import LoginView from './views/LoginView.vue'
import HomeView from './views/HomeView.vue'
import LiveTopologyView from './views/LiveTopologyView.vue'
// 2026-11-16: 请求阶段树（live2）—— 与 /live 并存，可回退。
import LiveStageTreeView from './views/LiveStageTreeView.vue'
import RequestDetailPage from './views/RequestDetailPage.vue'
import ForbiddenView from './views/ForbiddenView.vue'

// All other views are lazy-loaded to reduce initial bundle size
const MaintainUnavailableView = () => import('./views/MaintainUnavailableView.vue')
// 2026-11-17：简易/专家同路径包装（ProvidersModeView），避免 /providers 与
// /providers-simple 双路由分裂深链。
const ProvidersView = () => import('./views/ProvidersModeView.vue')
const KeysView = () => import('./views/KeysView.vue')
const KeyDetailView = () => import('./views/KeyDetailView.vue')
const KeyApplicationsView = () => import('./views/KeyApplicationsView.vue')
const SimpleKeysView = () => import('./views/SimpleKeysView.vue')
const ExamplesView = () => import('./views/ExamplesView.vue')
const HelpManualView = () => import('./views/HelpManualView.vue')
const ChatView = () => import('./views/ChatView.vue')
const RoutingOverviewView = () => import('./views/RoutingOverviewView.vue')
const RoutingPolicyView = () => import('./views/RoutingPolicyView.vue')
const DecisionsView = () => import('./views/DecisionsView.vue')
const CorrelationsView = () => import('./views/CorrelationsView.vue')
const RoutingOverrideView = () => import('./views/RoutingOverrideView.vue')
const QualityCorrelationsView = () => import('./views/QualityCorrelationsView.vue')
const RoutingAuditView = () => import('./views/RoutingAuditView.vue')
const RequestLogsView = () => import('./views/RequestLogsView.vue')
const RequestDetailFullscreenView = () => import('./views/RequestDetailFullscreenView.vue')
const DispatchWaterfallView = () => import('./views/DispatchWaterfallView.vue')
const ModelsView = () => import('./views/ModelsView.vue')
const ProviderDetailView = () => import('./views/ProviderDetailView.vue')
const PricingManagementView = () => import('./views/PricingManagementView.vue')
const StandardModelPricingView = () => import('./views/StandardModelPricingView.vue')
const FreePoolView = () => import('./views/FreePoolView.vue')
const FreeDiscoveryView = () => import('./views/FreeDiscoveryView.vue')
const TenantsView = () => import('./views/TenantsView.vue')
const TenantDetailView = () => import('./views/TenantDetailView.vue')
const RoutingDashboardView = () => import('./views/RoutingDashboardView.vue')
const WorkTypesView = () => import('./views/WorkTypesView.vue')
const UsersView = () => import('./views/UsersView.vue')
const AuditLogView = () => import('./views/AuditLogView.vue')
const CompressionView = () => import('./views/CompressionView.vue')
const DataLifecycleView = () => import('./views/DataLifecycleView.vue')
const SettingsView = () => import('./views/SettingsView.vue')
const MaaSAccountView = () => import('./views/tenant/MaaSAccountView.vue')
const MaaSBillsView = () => import('./views/tenant/MaaSBillsView.vue')
const MaaSPricingView = () => import('./views/tenant/MaaSPricingView.vue')
const MaaSUsageView = () => import('./views/tenant/MaaSUsageView.vue')
const MaaSOrderView = () => import('./views/tenant/MaaSOrderView.vue')
const TenantModelsView = () => import('./views/tenant/TenantModelsView.vue')
const CredentialMonitorView = () => import('./views/CredentialMonitorWithTabs.vue')
const ProbeHealthDetailView = () => import('./views/ProbeHealthDetailView.vue')
const AgentRegistryView = () => import('./views/AgentRegistryView.vue')
const FormatAnomaliesView = () => import('./views/FormatAnomaliesView.vue')
const ModelIntegrityView = () => import('./views/ModelIntegrityView.vue')
const ModelCredibilityView = () => import('./views/ModelCredibilityView.vue')
const ModulesView = () => import('./views/ModulesView.vue')
const PromptInjectionSettingsView = () => import('./views/PromptInjectionSettingsView.vue')
const PIAnalysisView = () => import('./views/admin/PIAnalysisView.vue')
// 错误处理手册（2026-11-21）：列表 + 条目详情
const ErrorPlaybookListView = () => import('./views/admin/error-playbook/ErrorPlaybookListView.vue')
const ErrorPlaybookDetailView = () => import('./views/admin/error-playbook/ErrorPlaybookDetailView.vue')
// 会话分析系统（2026-11-20）：后端 admin/session_analytics 的 7 个端点。
// ★ 权限闸门用 requiresAuth 而非 requiresSuper：后端 isAdminFromRequest 放行
//   super_admin / tenant_admin / admin_key，且专门为 tenant_admin 做了租户隔离
//   ⇒ 前端粗粒度闸门不该把 tenant_admin 挡在门外（权限真源在后端）。
const SessionAnalyticsUserRankingView = () => import('./views/admin/session-analytics/SessionAnalyticsUserRankingView.vue')
const SessionAnalyticsUserDetailView = () => import('./views/admin/session-analytics/SessionAnalyticsUserDetailView.vue')
const SessionAnalyticsAgentRankingView = () => import('./views/admin/session-analytics/SessionAnalyticsAgentRankingView.vue')
const SessionAnalyticsModelRankingView = () => import('./views/admin/session-analytics/SessionAnalyticsModelRankingView.vue')
// 2026-11-23：会话分析用户页面（整合 Dashboard 的会话统计 + Top10 用户排行）
const SessionAnalyticsUsersView = () => import('./views/admin/session-analytics/SessionAnalyticsUsersView.vue')
const ApprovalConfigView = () => import('./views/ApprovalConfigView.vue')
const ApprovalListView = () => import('./views/ApprovalListView.vue')
const ApprovalDetailView = () => import('./views/ApprovalDetailView.vue')
const SanitizeRulesView = () => import('./views/SanitizeRulesView.vue')
const OutputComplianceView = () => import('./views/OutputComplianceView.vue')
const UsageCostView = () => import('./views/admin/UsageCost.vue')
const UsageTrendExplorerView = () => import('./views/admin/UsageTrendExplorer.vue')
// 批 158b：P1 usageEnhanced 孤儿接前端，组合 summary + cost-trend + period-compare + cache-economics + hot-api-keys
const UsageEnhancedView = () => import('./views/admin/UsageEnhancedView.vue')
// 2026-11-16：自更新管理页（自动检索最新版本 → 提醒 → 点击安装升级）。
// 后端契约见 web/src/api/selfUpdate.ts（/api/admin/releases 族，super admin 档）。
const SelfUpdateView = () => import('./views/admin/SelfUpdateView.vue')
// 批 158c：P2 boardOperational 独立详细页（独立深链，呈现 background_tasks + selfcheck 全字段）
// ⚠️ 后端 tenant 隔离未落实（属主拍板），前端仅"呈现已就位数据"，不参与授权判断
const BoardOperationalView = () => import('./views/BoardOperationalView.vue')
// 2026-11-20：运营看板列表页（参数化复用：models/providers/clients/tenants）
const BoardOperationalListView = () => import('./views/BoardOperationalListView.vue')
// 2026-11-20：分析详情页（参数化复用：model/provider/client/tenant/:id）
const AnalyticsDetailView = () => import('./views/analytics/AnalyticsDetailView.vue')
// 批 158e：P4 opsOverview 独立详细页（12 路 payload 综合视图；后端中间件降级 admin → superAdmin）
const OpsOverviewView = () => import('./views/OpsOverviewView.vue')
// 批 200：P5 跨仓孤儿接前端 subscriptionTiers（license-authority 独立服务，nginx 反代 /api/admin/tiers）
const SubscriptionTiersView = () => import('./views/SubscriptionTiersView.vue')
// 批 201：加 router/nav 让 turnsFilterOptions 深链可达（API 已有，缺独立详细页）
const TurnsFilterOptionsView = () => import('./views/TurnsFilterOptionsView.vue')
// 批 202：加 router/nav 让 storageMigrationState 深链可达（API + StorageConfig 已有，缺独立详细页）
const StorageMigrationStateView = () => import('./views/StorageMigrationStateView.vue')
// 批 203：P2 requestActions 孤儿接前端（Redis LIST by request_id）
const RequestActionsView = () => import('./views/RequestActionsView.vue')
// 批 204：P0 storageAndLogConfig 孤儿接前端（3 段只读摘要：storage + filesystem + log）
const StorageAndLogConfigView = () => import('./views/StorageAndLogConfigView.vue')
const ReconciliationReportView = () => import('./views/admin/ReconciliationReport.vue')
const PluginSettingsPage = () => import('./views/management/PluginSettingsPage.vue')
// 2026-11-23 W2-Q2：团队层级管理（团队列表 + 团队详情）。
// 后端契约：admin/team_handlers.go 注册 /api/admin/teams*（全部走 h.admin(...)）。
const TeamListView = () => import('./views/TeamListView.vue')
const TeamDetailView = () => import('./views/TeamDetailView.vue')
// 2026-11-05 W1-T2 前端轮：报价系统 MVP 工作台（供给池/需求池/报价单三页签；
// 后端 sales 包 942–949 + admin/sales_handlers.go；内部运营面 ⇒ requiresAuth，
// 数据权限由应用层角色闸承载）
const SalesWorkbenchView = () => import('./views/operations/SalesWorkbenchView.vue')
const OperationsMarketingView = () => import('./views/operations/OperationsMarketingView.vue') // 2026-11-09 运营审计轮项4
const MarketPricesView = () => import('./views/operations/MarketPricesView.vue') // 2026-11-13 A-17 收口 M3-T1
const PublicQuoteView = () => import('./views/PublicQuoteView.vue') // 客户版报价落地页（2026-11-08 W2）
// 2026-07-24: V2-P4 admin session detail page (dual-column turns + drawer).
const SessionDetailView = () => import('./views/admin/SessionDetailPage.vue')
// 2026-08-09: 跨会话轮次列表页
const TurnsListView = () => import('./views/TurnsListView.vue')
// 2026-08-29: 代理管理
const ProxyView = () => import('./views/ProxyView.vue')
const ClientAnalyticsView = () => import('./views/ClientAnalyticsView.vue')
const TaskAnalyticsView = () => import('./views/TaskAnalyticsView.vue')
const UserProfileListView = () => import('./views/UserProfileListView.vue')
const UserProfileView = () => import('./views/UserProfileView.vue')

// P2.1+ Human Annotation Web workflow (2026-09-06) → 2026-10-02 整合轮:
// AnnotationView / AnnotationStatsView / TaskProfileView / AutoTuningView
// 四个子面板由 AutoRoutingOpsView 直接 import（无独立路由），此处只保留
// 宿主懒加载入口。
const AutoRoutingOpsView = () => import('./views/AutoRoutingOpsView.vue')

// T9 — 请求注册表 / Journey 详情 / 连接注册台 / 节点恢复时间线（mock stage）
const RequestRegistryView = () => import('./views/RequestRegistryView.vue')
const RequestJourneyDetailView = () => import('./views/RequestJourneyDetailView.vue')
const ConnectionRegistryView = () => import('./views/ConnectionRegistryView.vue')
const NodeHealthTimelineView = () => import('./views/NodeHealthTimelineView.vue')

// Customer lifecycle — merged「更新与激活」+ offline fallback
const CustomerUpdateActivateView = () => import('./views/lifecycle/UpdateActivateView.vue')
const CustomerOfflineActivationView = () => import('./views/lifecycle/OfflineActivationView.vue')
const BootstrapWizardView = () => import('./views/bootstrap/BootstrapWizardView.vue')

// Operations Platform — vibecoding still lives in Gateway; other /ops/*
// pages redirect to ai-native-maintain SPA under /maintain/ops/*.
const VibeCodingView = () => import('./views/ops/VibeCodingView.vue')

/** Full-page jump to maintain SPA (nginx serves /maintain/* separately). */
function externalMaintainRedirect(path: string, target: string) {
  return {
    path,
    component: { render: () => null },
    beforeEnter() {
      if (typeof window !== 'undefined') {
        window.location.replace(target)
      }
    },
  }
}

function isAuthed(): boolean {
  if (store.jwtToken || store.apiKey || store.userInfo) return true
  // 2026-07-09 (handoff task UI verification): localStorage fallback.
  // store.userInfo is the in-memory source of truth but it only re-inits
  // from localStorage at module load. If the user lands on the SPA via deep
  // link (or after a stale tab), the in-memory store can be empty while
  // localStorage still has valid credentials. Read it directly without
  // mutating the store, so we don't trigger reactivity in the guard.
  try {
    const raw = typeof localStorage !== 'undefined'
      ? localStorage.getItem('llmgw_user_info')
      : null
    if (raw) {
      const parsed = JSON.parse(raw)
      if (parsed && parsed.role && parsed.id) return true
    }
  } catch { /* corrupt cache */ }
  return false
}

function isSuperAdmin(): boolean {
  // Legacy API key auth: no JWT but has apiKey → super_admin
  if (!store.jwtToken && store.apiKey) return true
  // 2026-07-09 (handoff task UI verification): read role from
  // localStorage when the in-memory store hasn't been hydrated yet,
  // mirroring the localStorage fallback in isAuthed().
  let role = store.userInfo?.role
  if (!role) {
    try {
      const raw = typeof localStorage !== 'undefined'
        ? localStorage.getItem('llmgw_user_info')
        : null
      if (raw) {
        const parsed = JSON.parse(raw)
        role = parsed?.role
        if (parsed && !store.userInfo) store.userInfo = parsed
      }
    } catch { /* corrupt cache */ }
  }
  return role === 'super_admin'
}

function isPlatformOpsView(): boolean {
  return isSuperAdmin() && isDefaultTenant()
}

// 2026-09-04: 供应商控制台 —— super_admin，或 default 租户的 tenant_admin
// （凭据 API Key 修改对该角色开放，见 store.isProviderConsoleView）。
// 与 isSuperAdmin 一样带 localStorage 兜底，防深链进入时 userInfo 未水合。
function isProviderConsoleView(): boolean {
  if (isSuperAdmin()) return true
  let info = store.userInfo
  if (!info?.role) {
    try {
      const raw = typeof localStorage !== 'undefined'
        ? localStorage.getItem('llmgw_user_info')
        : null
      if (raw) info = JSON.parse(raw)
    } catch { /* corrupt cache */ }
  }
  return info?.role === 'tenant_admin' && info?.tenant_id === 'default'
}

export const router = createRouter({
  history: createWebHistory(),
  scrollBehavior(to, _from, savedPosition) {
    if (savedPosition) return savedPosition
    if (to.hash) return { el: to.hash, behavior: 'smooth' }
    return { top: 0 }
  },
  routes: [
    { path: '/login',              component: LoginView, meta: { public: true } },
    { path: '/forbidden',          component: ForbiddenView, meta: { public: true } },
    // 2026-07-21: 总览搬到 /dashboard；根路径 / 仍走 HomeView（未登录 redirect 到 maintain，已登录显示 DashboardView）。
    { path: '/dashboard',          component: HomeView, meta: { requiresAuth: true } },
    { path: '/',                   component: HomeView, meta: { public: true } },
    // 2026-10-16: 实时拓扑新首页（/live）。与旧首页**并存**：`/` 与
    // /dashboard 一行未改，本页只新增一个入口，不接管根路径。
    // 权限与 /dashboard 同级（requiresAuth），租户管理员进入即按会话租户收窄。
    { path: '/live',               component: LiveTopologyView, meta: { requiresAuth: true } },
    // 2026-11-16: 请求阶段树（live2）。与 /live **并存**一个版本周期：
    // 过滤条件常驻顶层、统计点开是全页浮层、逐段显示请求走过的管线。
    // 权限与 /live 同级（requiresAuth），可回退到 /live。
    { path: '/live2',              component: LiveStageTreeView, meta: { requiresAuth: true } },
    // 2026-10-18: 请求详情**全页**（owner 要求：面板可整页打开某条请求的
    // 详情）。非导航项（navCoverage 按孤儿登记）；数据来自实时滚动窗口，
    // 直链/刷新时可能已滚出（页面如实说明，不冒充空详情）。
    // ★ 2026-10-31 注释订正：/live 左右面板的行内入口早已不走本路由 ——
    //   点行弹摘要浮层（RequestZoomPanel），「查看全景」/行尾 ↗ 走
    //   openRequestDetailPage → /request-detail/:id（RequestDetailFullscreenView）。
    //   本路由目前无仓内调用方（保留：直链兼容，删路由属产品裁决）。
    { path: '/live/request/:requestId', component: RequestDetailPage, meta: { requiresAuth: true } },

    // super_admin only — providers, catalog, free pool, tenants, audit logs
    // 2026-09-04: 供应商列表/详情改挂 providerConsole —— super_admin，或
    // default 租户 tenant_admin（凭据 API Key 修改对其开放）。
    { path: '/providers',          component: ProvidersView,       meta: { requiresProviderConsole: true } },
    { path: '/providers/:id',      component: ProviderDetailView,  meta: { requiresProviderConsole: true } },
    { path: '/key-applications',   component: KeyApplicationsView, meta: { requiresSuper: true } },
    { path: '/catalog',            redirect: (to) => ({ path: '/models', query: { ...to.query, tab: 'catalog' } }) },
    { path: '/routing-v2',         component: RoutingDashboardView, meta: { requiresSuper: true } },
    { path: '/routing-v2/credentials', component: CredentialMonitorView }, // 2026-07-04: 允许 tenant_admin 访问
    // 2026-09-07 probe-health integration: the standalone page folds into the
    // credential monitor's super-only「探测健康」tab; keep /probe-health/detail
    // (model drill-down) as-is.
    { path: '/probe-health',       redirect: { path: '/routing-v2/credentials', query: { tab: 'probe-health' } } },
    { path: '/probe-health/detail', component: ProbeHealthDetailView, meta: { requiresSuper: true } },
    // 2026-07-23: 系统监测面板（v1）—— 入站需 super_admin 才能操作。
    // 设计依据 docs/会话优化v2/32-系统监测模块设计.md §5
    { path: '/system-monitor', redirect: { path: '/dashboard', query: { tab: 'selfcheck' } } },
    { path: '/routing-v2/work-types',         component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/settings', component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/:key',     component: WorkTypesView, meta: { requiresSuper: true } },
    // P2.1+ Human annotation Web workflow (2026-09-06): accessible by any authenticated user.
    // 2026-10-02 整合轮：四页收敛为 AutoRoutingOpsView 统一工作台
    // （标注工作台/标注统计 全员；任务档案/调参审批 super_admin，宿主内
    // normalizeTab 双保险）。旧四路由 redirect 保深链兼容。
    { path: '/routing-v2/auto-ops',           component: AutoRoutingOpsView },
    { path: '/routing-v2/annotations',        redirect: { path: '/routing-v2/auto-ops', query: { tab: 'annotate' } } },
    { path: '/routing-v2/annotations/stats',  redirect: { path: '/routing-v2/auto-ops', query: { tab: 'stats' } } },
    // v2 closed-loop P0③: 档案/调参页。两页的写端点都是超管面：
    // tuning 端点挂 h.superAdmin（admin/auto_route_tuning.go）；task-profile 的
    // 变更端点（apply-tier-config / reload，TaskProfileView 有写操作）后端暂挂
    // 普通 admin 中间件（admin/handler.go RegisterTaskProfileRoutes）。前端门控
    // 落在宿主页签级（R64，2026-09-25）：redirect 后守卫落在目标路由，非 super
    // 深链 ?tab=profiles|tuning 会被 normalizeTab 弹回标注页签。
    { path: '/routing-v2/task-profile',       redirect: { path: '/routing-v2/auto-ops', query: { tab: 'profiles' } } },
    { path: '/routing-v2/auto-tuning',        redirect: { path: '/routing-v2/auto-ops', query: { tab: 'tuning' } } },
    { path: '/routing-policy',     component: RoutingPolicyView,   meta: { requiresSuper: true } },
    { path: '/free-pool',          component: FreePoolView,        meta: { requiresSuper: true } },
    { path: '/free-discovery',     component: FreeDiscoveryView,   meta: { requiresSuper: true } },
    { path: '/tenants',            component: TenantsView,         meta: { requiresSuper: true } },
    { path: '/tenants/:tenantId',  component: TenantDetailView,    meta: { requiresSuper: true } },
    { path: '/audit-logs',        component: AuditLogView,         meta: { requiresSuper: true } },
    { path: '/format-anomalies',  component: FormatAnomaliesView,  meta: { requiresSuper: true } },
    { path: '/model-integrity',  component: ModelIntegrityView,   meta: { requiresSuper: true } },
    { path: '/model-credibility', component: ModelCredibilityView, meta: { requiresSuper: true } },

    // Platform ops only (super_admin + default tenant)
    { path: '/users',              component: UsersView },
    { path: '/models',             component: ModelsView, meta: { requiresPlatformOps: true } },
    { path: '/pricing',            component: PricingManagementView, meta: { requiresPlatformOps: true } },
    { path: '/model-pricing',      component: StandardModelPricingView, meta: { requiresPlatformOps: true } },

    // Tenant portal (non-default tenant self-service; admin uses ?tenant=code)
    { path: '/tenant/models',      component: TenantModelsView },
    { path: '/tenant/account',     component: MaaSAccountView },
    { path: '/tenant/bills',       component: MaaSBillsView },
    { path: '/tenant/pricing',     component: MaaSPricingView },
    { path: '/tenant/orders/:id',  component: MaaSOrderView },
    { path: '/tenant/usage',       component: MaaSUsageView },

    // Legacy MaaS paths → tenant portal
    { path: '/maas/models',        redirect: (to) => ({ path: '/tenant/models', query: to.query }) },
    { path: '/maas/account',       redirect: (to) => ({ path: '/tenant/account', query: to.query }) },
    { path: '/maas/pricing',       redirect: (to) => ({ path: '/tenant/pricing', query: to.query }) },
    { path: '/maas/orders/:id',    redirect: (to) => ({ path: `/tenant/orders/${to.params.id}`, query: to.query }) },
    { path: '/maas/usage',         redirect: (to) => ({ path: '/tenant/usage', query: to.query }) },

    // Tenant-isolated (any authenticated user, scoped to own tenant for tenant_admin)
    { path: '/keys',               component: KeysView },
    { path: '/keys/:id',           component: KeyDetailView },
    { path: '/simple/keys',        component: SimpleKeysView }, // L3: 简易模式密钥签发（两字段）
    { path: '/routing',            redirect: { path: '/routing-v2', query: { tab: 'resolve' } } },
    { path: '/routing-overview',   component: RoutingOverviewView, meta: { requiresPlatformOps: true } },
    { path: '/routing-decisions',  component: DecisionsView, meta: { requiresPlatformOps: true } },
    { path: '/correlations',       component: CorrelationsView, meta: { requiresSuper: true } },
    { path: '/routing/overrides',  component: RoutingOverrideView, meta: { requiresSuper: true } },
    { path: '/routing/overrides/audit', component: RoutingAuditView, meta: { requiresSuper: true } },
    { path: '/quality-correlations',  component: QualityCorrelationsView, meta: { requiresSuper: true } },
    { path: '/request-logs',       component: RequestLogsView },
    {
      path: '/request-detail/:requestId',
      name: 'request-detail',
      component: RequestDetailFullscreenView,
    },
    { path: '/dispatch/waterfall', component: DispatchWaterfallView, meta: { requiresPlatformOps: true } },
    // 2026-11-23 L14 轮：★ 用户画像**列表页**挂回原 URL（★ 修上游 95e44dc15 的
    // 「路由改指、依赖没跟着改」三层脱钩）。★ 上游把本 URL 改指整合页
    // （SessionAnalyticsUsersView）⇒ ⇒ 列表页能力（★ 数字出处行 / 窗口内用量五列 /
    // 统计说明抽屉 / 展开行模型矩阵）在现役页上消失 ⇒ ⇒ maestro
    // 13-session-analytics-users.yaml 的列表页段恒红。
    // ★ 判据（★ 上游 flow 断言点逐条读过）：★ 断言的是**列表页独有形态**
    // （`request_logs_with_current_month` 出处行 ＋ 五列窗口用量 ＋ 抽屉里的
    // 「画像列与用量列的口径区别」）⇒ ★ 整合页**没有**这些（★ 实测 grep 0 命中）
    // ⇒ ⇒ 挂回列表页是正解。
    { path: '/admin/session-analytics/users', alias: '/admin/session-analytics/user-profiles', component: UserProfileListView, meta: { requiresAuth: true } },
    // ★ 整合页（★ 上游 95e44dc15 新增：Dashboard 会话统计 ＋ Top10 用户排行）
    // **另找落点**，不与列表页抢同一个 URL ⇒ ★ 上游新增的 8 语种键
    // `nav.item.sessionAnalyticsUsers` 本就只被它用作副标题、★ 从未做菜单项
    // ⇒ ★ 这里用它做菜单标签（★ 不新增 i18n 键）。
    { path: '/admin/session-analytics/users/overview', component: SessionAnalyticsUsersView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/users/:owner', component: UserProfileView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/clients/:id', component: ClientAnalyticsView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/tasks/:id', component: TaskAnalyticsView, meta: { requiresAuth: true } },
    {
      path: '/admin/session-analytics/filter-options',
      name: 'session-analytics-filter-options',
      component: () => import('./views/SessionAnalyticsFilterOptionsView.vue'),
      meta: { requiresAuth: true },
    },
    { path: '/admin/compression',   component: CompressionView, meta: { requiresPlatformOps: true } },
    { path: '/admin/data-lifecycle', component: DataLifecycleView, meta: { requiresPlatformOps: true } },
    { path: '/admin/settings',     component: SettingsView, meta: { requiresSuper: true } },
    { path: '/admin/agents',       component: AgentRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/modules',      component: ModulesView, meta: { requiresSuper: true } },
    { path: '/admin/prompt-injection', component: PromptInjectionSettingsView, meta: { requiresSuper: true } },
    { path: '/admin/pi-analysis', component: PIAnalysisView, meta: { requiresSuper: true } },
    // 错误处理手册（2026-11-21）：列表 + 条目详情（执行留痕/快照回滚/方案编辑）。
    // ★ 权限档与 pi-analysis 同族（管理面处置工具）：requiresSuper 只是体验层，
    //   真源在后端 AdminMiddleware（鉴权材料齐备才注册路由，fail-closed）。
    { path: '/admin/error-playbook', name: 'error-playbook-list', component: ErrorPlaybookListView, meta: { requiresSuper: true } },
    { path: '/admin/error-playbook/:id', name: 'error-playbook-detail', component: ErrorPlaybookDetailView, meta: { requiresSuper: true } },
    // 会话分析（2026-11-20）：总览 / 人员排行 / 人员详情 / 智能体排行 / 模型排行
    // 2026-11-23：重定向 overview 到整合后的 users 页面（整合任务：将 overview 内容移到 /admin/session-analytics/users）
    // ★ 2026-11-23 L30 轮·补②④：★ 重定向目标由 `/admin/session-analytics/users`
    //   改为 **`/admin/session-analytics/users/overview`**（★ 整合页的现居址）。
    //   ★ 缘由：★ 这条重定向的**原意**是「旧书签 ⇒ 整合页」（★ 原会话分析总览页按
    //   owner 指令取消后、内容迁入整合页）⇒ ★ L14 轮把整合页迁到 /users/overview、
    //   而这条重定向仍指向 /users ⇒ ★★ **⇒ 旧 URL 落到【列表页】而不是【整合页】**
    //   ⇒ ★★★ **⇒ 连带打断 `16-session-analytics-system.yaml` 的 3 条断言**
    //   （★ `sa-page-users` / `sa-top5-section` / `sa-users-col-rank` ⇒ ★ 它们住在
    //   整合页里、★ 列表页没有）⇒ ★ 改指后 ⇒ **旧 URL 的行为与 L14 之前一致**。
    { path: '/admin/session-analytics', redirect: '/admin/session-analytics/users/overview', meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/user-ranking', name: 'session-analytics-user-ranking', component: SessionAnalyticsUserRankingView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/user-ranking/:owner', name: 'session-analytics-user-detail', component: SessionAnalyticsUserDetailView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/agent-ranking', name: 'session-analytics-agent-ranking', component: SessionAnalyticsAgentRankingView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/model-ranking', name: 'session-analytics-model-ranking', component: SessionAnalyticsModelRankingView, meta: { requiresAuth: true } },
    { path: '/admin/approval-config', component: ApprovalConfigView, meta: { requiresSuper: true } },
    { path: '/admin/approvals',    component: ApprovalListView, meta: { requiresSuper: true } },
    { path: '/admin/approvals/:id', component: ApprovalDetailView, meta: { requiresSuper: true } },
    { path: '/admin/sanitize-rules', component: SanitizeRulesView, meta: { requiresAuth: true } },
    { path: '/admin/output-compliance', component: OutputComplianceView, meta: { requiresSuper: true } },
    { path: '/admin/usage',        component: UsageCostView, meta: { requiresSuper: true } }, // 用量成本视图 (T2.4)；R34: 与相邻 admin 路由对齐补权限 meta
    { path: '/admin/usage-trends', component: UsageTrendExplorerView, meta: { requiresAuth: true, fillViewport: true } }, // 全屏用量趋势（2026-10-02：铺满主区；模型用 ModelPicker）
    { path: '/admin/usage-enhanced', component: UsageEnhancedView, meta: { requiresSuper: true } }, // 用量增强分析（批 158b：组合 usage.ts 5 端点综合视图；同档 UsageCost 走 super_admin）
    { path: '/admin/board-operational', component: BoardOperationalView, meta: { requiresSuper: true } }, // 运营面板（批 158c：独立详细页；★ 2026-11-04 12h 审计轮 D-1：后端已降为 superAdmin 档 ⇒ 前端守卫同步为 requiresSuper，与同族 ops-overview 一致）
    // 2026-11-20：运营看板扩展路由（完整列表 + 详情分析）
    { path: '/admin/board-operational/:dimension', component: BoardOperationalListView, meta: { requiresSuper: true } }, // 完整列表页：models/providers/clients/tenants
    { path: '/admin/analytics/:dimension/:id', component: AnalyticsDetailView, meta: { requiresAuth: true } }, // 详情分析页：model/provider/client/tenant/:id (backend AdminMiddleware handles tenant isolation)
    { path: '/admin/ops-overview', component: OpsOverviewView, meta: { requiresSuper: true } }, // 运维总览（批 158e：12 路 payload 卡片布局；与 appNav.ts:229 maintain SPA 平级，web/ 内部独立深链）
    { path: '/admin/subscription-tiers', component: SubscriptionTiersView, meta: { requiresSuper: true } }, // 订阅层级（批 200：P5 跨仓 license-authority 独立服务，nginx 反代 /api/admin/tiers）
    { path: '/admin/turns/filter-options', component: TurnsFilterOptionsView, meta: { requiresSuper: true } }, // 轮次筛选选项（批 201：API 已有 listTurnsFilterOptions，缺独立深链详细页）
    { path: '/admin/data-lifecycle/migration-state', component: StorageMigrationStateView, meta: { requiresPlatformOps: true } }, // 存储迁移状态（批 202：API + StorageConfig 已有，缺独立深链；与父 /admin/data-lifecycle 端点同档 platformOps）
    { path: '/admin/request-actions/:id', name: 'request-actions', component: RequestActionsView, meta: { requiresSuper: true } }, // 请求动作时间线（批 203：Redis LIST by request_id；admin 档 endpoint）
    { path: '/admin/storage-and-log-config', component: StorageAndLogConfigView, meta: { requiresSuper: true } }, // 存储与日志配置（批 204：3 段只读摘要；子视图 StorageConfig/FilesystemMaintenance/LogManagement 全在 DataLifecycleView 父视图下）
    { path: '/admin/reconciliation', component: ReconciliationReportView, meta: { requiresSuper: true } }, // 对账报表（供应商/内部双视角 + Excel 导出，2026-09-25）
    { path: '/admin/plugin-settings', component: PluginSettingsPage, meta: { requiresSuper: true } }, // 插件提供商配置（Native/Casdoor 切换，2026-11-15）
    // 2026-11-23 W2-Q2：团队层级管理（列表 + 详情）。租户内资源。
    // ★ 权限档选 requiresAuth（与 /users、/admin/session-analytics/* 同档），理由：
    //   ① 后端 admin/team_handlers.go 的全部端点走 `h.admin(...)` = **AdminMiddleware**
    //      （只要求有效 JWT，**不要求 super_admin**）；团队内的 owner/admin/member 授权
    //      由 admin/team_middleware.go 的 requireTeamMember 承担 ⇒ **权限真源在后端**
    //      （同 router.ts 会话分析一族的口径，router.ts:73-75）。
    //   ② 团队是**租户内**资源（team.tenant_id 取会话租户，列表按 owner_id 收窄）
    //      ⇒ super_admin 粗粒度闸门会把「租户内团队的真正主人」tenant_admin 挡在门外。
    { path: '/admin/teams', name: 'team-list', component: TeamListView, meta: { requiresAuth: true } },
    { path: '/admin/teams/:id', name: 'team-detail', component: TeamDetailView, meta: { requiresAuth: true } },
    { path: '/admin/self-update', component: SelfUpdateView, meta: { requiresSuper: true } }, // 自更新与升级（检索最新版本 → 提醒 → 安装升级，2026-11-16）
    { path: '/operations/sales', component: SalesWorkbenchView, meta: { requiresAuth: true } }, // 报价系统 MVP 工作台（2026-11-05 W1-T2；供给/需求/报价三页签；内部运营面 requiresAuth，写权限应用层闸）
    { path: '/operations/marketing', component: OperationsMarketingView, meta: { requiresPlatformOps: true } }, // 2026-11-09 运营审计轮项4：营销管理（套餐/加油包/优惠码/活动）；2026-11-12 A-8 权限对齐（定稿 M3-T4）：super-only → platformOps（与 /pricing 同档；服务端仍 superAdmin 闸——platformOps=super_admin@default，中间件无需改动）
    { path: '/operations/market-prices', component: MarketPricesView, meta: { requiresPlatformOps: true } }, // 2026-11-13 A-17 收口 M3-T1：市场价格采集面（平台注册表 958 管理 + 940 同步台账；服务端 superAdmin 闸，与 /operations/marketing 同档）
 { path: '/operations/price-compare', redirect: { path: '/model-pricing' } }, //2026-11-19 合并轮：价格对比面并入 /model-pricing（比较与定价页，四 tab），原路径保留为重定向（收藏链接不断）
    { path: '/admin/sessions/:id', component: SessionDetailView, meta: { requiresSuper: true } }, // 2026-07-24: V2-P4 session detail
    { path: '/admin/turns',        component: TurnsListView, meta: { requiresSuper: true } }, // 2026-08-09: 跨会话轮次列表
    { path: '/admin/proxy',        component: ProxyView, meta: { requiresSuper: true } }, // 2026-08-29: 代理管理
    // T9 — 请求注册表 / Journey 详情 / 连接注册台 / 节点恢复时间线
    { path: '/admin/request-registry', component: RequestRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/request-registry/journey/:requestId', name: 'request-journey-detail', component: RequestJourneyDetailView, meta: { requiresSuper: true } },
    { path: '/admin/connection-registry', component: ConnectionRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/connection-registry/:credentialId/recovery', name: 'node-health-timeline', component: NodeHealthTimelineView, meta: { requiresSuper: true } },
    { path: '/examples',           component: ExamplesView },
    // 2026-11-16：帮助手册（下载/安装/部署/供应商/凭据/租户/用户/API Key/
    // 客户端调用/常见故障）。正文来自 web/content/help-manual.zh-CN.md，
    // 菜单入口见 appNav.ts 的 guide 组。
    { path: '/help',               component: HelpManualView, meta: { requiresAuth: true } },
    { path: '/chat',               component: ChatView },

    // Local first-boot bootstrap (public; works offline)
    { path: '/bootstrap', component: BootstrapWizardView, meta: { public: true } },
    // 客户版报价落地页（2026-11-08 W2 客户版轮）：分享 token 外链直开，无登录
    // （风暴档 §8-3 裁决；后端 hash 库存/TTL/撤销/访问审计兜底）。
    { path: '/public/quote/:token', component: PublicQuoteView, meta: { public: true } },

    // Customer lifecycle — merged update & activate (public for guest homepage flow)
    { path: '/customer/update-activate', component: CustomerUpdateActivateView, meta: { public: true } },
    { path: '/customer/site', redirect: '/customer/update-activate' },
    { path: '/customer/activate', redirect: '/customer/update-activate' },
    { path: '/customer/license', redirect: '/customer/update-activate' },
    { path: '/customer/agreement', redirect: '/customer/update-activate' },
    { path: '/customer/offline-activation', component: CustomerOfflineActivationView, meta: { public: true } },

    // Maintain paths must never fall through to the Gateway catch-all.
    { path: '/maintain', component: MaintainUnavailableView, meta: { public: true } },
    { path: '/maintain/:pathMatch(.*)*', component: MaintainUnavailableView, meta: { public: true } },

    // Operations Platform — legacy /ops/* bookmarks → maintain SPA (full page)
    externalMaintainRedirect('/ops/licenses', '/maintain/ops/licenses'),
    externalMaintainRedirect('/ops/faults', '/maintain/ops/faults'),
    externalMaintainRedirect('/ops/autoupdate', '/maintain/ops/autoupdate'),
    externalMaintainRedirect('/ops/center', '/maintain/ops/center'),
    externalMaintainRedirect('/ops', '/maintain/ops/overview'),
    { path: '/ops/vibecoding', component: VibeCodingView, meta: { requiresSuper: true, requiresOpsPlatform: true } },

    ...(import.meta.env.DEV
      ? [{ path: '/dev/waterfall-preview', component: () => import('./views/DispatchWaterfallPreview.vue'), meta: { public: true } }]
      : []),
    // 2026-10-16: 实时拓扑的 DEV 夹具预览页（动效/排版的肉眼验收入口，
    // 生产构建不注册）。同 DispatchWaterfallPreview 的既有做法。
    ...(import.meta.env.DEV
      ? [{ path: '/dev/live-topology-preview', component: () => import('./views/LiveTopologyPreview.vue'), meta: { public: true } }]
      : []),
    // 2026-10-18: 请求详情全页的 DEV 免登录别名（浏览器验收用；生产路由
    // /live/request/:id 保持 requiresAuth —— 详情含 token/费用/会话，不许匿名）。
    ...(import.meta.env.DEV
      ? [{ path: '/dev/live-request-preview/:requestId', component: RequestDetailPage, meta: { public: true } }]
      : []),
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

/** Soft bootstrap gate: fail-open when status cannot be fetched. */
let bootstrapGateCache: { at: number; redirect: boolean } | null = null
const BOOTSTRAP_GATE_TTL_MS = 15_000

async function shouldRedirectToBootstrap(toPath: string): Promise<boolean> {
  if (toPath === '/bootstrap' || toPath === '/forbidden' || toPath === '/login') return false
  if (toPath.startsWith('/customer/')) return false
  if (import.meta.env.DEV && toPath.startsWith('/dev/')) return false
  try {
    if (localStorage.getItem('llmgw_require_bootstrap') === '0') return false
    if (localStorage.getItem('llmgw_activated') === '1') return false
  } catch {
    return false
  }
  const now = Date.now()
  if (bootstrapGateCache && now - bootstrapGateCache.at < BOOTSTRAP_GATE_TTL_MS) {
    return bootstrapGateCache.redirect
  }
  try {
    const controller = typeof AbortController !== 'undefined' ? new AbortController() : null
    const timer = controller ? window.setTimeout(() => controller.abort(), 2500) : 0
    const res = await throttledFetch('/api/system/bootstrap/status', {
      method: 'GET',
      credentials: 'same-origin',
      cache: 'no-store',
      signal: controller?.signal,
    })
    if (timer) window.clearTimeout(timer)
    if (!res.ok) {
      bootstrapGateCache = { at: now, redirect: false }
      return false // fail-open
    }
    const body = await res.json().catch(() => null)
    if (!body || typeof body !== 'object') {
      bootstrapGateCache = { at: now, redirect: false }
      return false
    }
    if (body.activated) {
      try {
        localStorage.setItem('llmgw_activated', '1')
      } catch { /* ignore */ }
      bootstrapGateCache = { at: now, redirect: false }
      return false
    }
    bootstrapGateCache = { at: now, redirect: true }
    return true
  } catch {
    bootstrapGateCache = { at: now, redirect: false }
    return false // fail-open on network / missing API
  }
}

router.beforeEach(async (to) => {
  // Soft redirect: local first-boot when not activated (fail-open).
  if (await shouldRedirectToBootstrap(to.path)) {
    return { path: '/bootstrap', query: { redirect: to.fullPath } }
  }

  // 2026-07-09: auth probe 还没完成时不要做任何 redirect，否则会在 cookie 登录后
  // 把用户弹回首页 / login，App.vue 的 hydration 永远没机会切到 app-layout。
  // 让 /api/auth/me 先 settle（store.authHydrated=true）再评估 auth。
  if (!store.authHydrated) {
    // 把目标 path 保存到 query，hydration 完成后会重定向过去
    if (to.path === '/' && to.query.login) {
      // Already going to home with login=1, allow
      return
    }
    // 第一次访问：等 hydration 完成。
    // 加超时上限：若 auth 始终未就绪（接口挂掉 / 竞态），30ms 轮询会永远
    // 卡住、Promise 永不 resolve，导航死锁。超过阈值则 fail-open 放行，
    // 由后续 isAuthed() 检查兜底（未登录走 login 流程），不再无限轮询。
    //
    // ★ 2026-10-28（R176 实测修正）：等的是「**auth 探测 settle**」，
    //   **不再要求 userInfo.id**。旧判据把「访客」（永远没有 userInfo）
    //   判成「永远不就绪」⇒ 唯一出口是 10 秒上限
    //   ⇒ **每个访客页面都白等满 10 秒才渲染正文**
    //   （实测：/forbidden 10131ms、/bootstrap 10108ms、/customer/update-activate 10116ms；
    //     而这些页面 DOMContentLoaded 只要 90ms、FCP 只要 100ms —— 慢的不是渲染，是等）。
    //   登录态不受影响（登录后 userInfo 在位 ⇒ 立即放行）。
    //   判据与等待循环已抽到 ./router/authHydration（可单测）。
    return waitForAuthHydration({
      snapshot: () => ({ authHydrated: store.authHydrated, userInfo: store.userInfo }),
    })
  }
  // 1. Auth check — unauthenticated users land on home, not full-page login
  if (!to.meta.public && !isAuthed()) {
    return { path: '/', query: { login: '1', redirect: to.fullPath } }
  }
  // 2. Bounce authed users away from /login, restoring the original page
  // when a 401 bounce (or login modal) left `redirect` on the URL.
  if ((to.path === '/login' || (to.path === '/' && (to.query.login === '1' || to.query.login === 'true'))) && isAuthed()) {
    const restored = locationFromInternalPath(to.query.redirect)
    if (restored) return restored
    if (to.path === '/login') return { path: '/' }
  }
  // 3. Super-admin role check
  if (to.meta.requiresSuper && !isSuperAdmin()) {
    return { path: '/forbidden' }
  }
  // 3a. Provider console (super_admin 或 default 租户 tenant_admin —
  // 凭据 API Key 修改 2026-09-04)
  if (to.meta.requiresProviderConsole && !isProviderConsoleView()) {
    return { path: '/forbidden' }
  }
  // 3b. Ops-center routes only when Maintain is available (or forced on)
  if (to.meta.requiresOpsPlatform && !showOpsPlatform()) {
    return { path: '/customer/update-activate' }
  }
  if (to.path.startsWith('/ops') && !showOpsPlatform()) {
    return { path: '/customer/update-activate' }
  }
  // 4. Platform ops (super_admin on default tenant) for运维向页面
  if (to.meta.requiresPlatformOps && !isPlatformOpsView()) {
    return { path: '/' }
  }
  // 5. Default-tenant ops must not browse tenant portal without ?tenant= context
  if (
    to.path.startsWith('/tenant/') &&
    isPlatformOpsView() &&
    typeof to.query.tenant !== 'string'
  ) {
    return { path: '/' }
  }
})
