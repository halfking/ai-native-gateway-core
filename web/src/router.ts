import { createRouter, createWebHistory } from 'vue-router'
import { store, isDefaultTenant, canAccessMaintain } from './store'

// Critical views loaded immediately (login, home, layout)
import LoginView from './views/LoginView.vue'
import HomeView from './views/HomeView.vue'
import SessionContextLayout from './layouts/SessionContextLayout.vue'
import ForbiddenView from './views/ForbiddenView.vue'

// All other views are lazy-loaded to reduce initial bundle size
const ProvidersView = () => import('./views/ProvidersView.vue')
const KeysView = () => import('./views/KeysView.vue')
const KeyDetailView = () => import('./views/KeyDetailView.vue')
const KeyApplicationsView = () => import('./views/KeyApplicationsView.vue')
const ExamplesView = () => import('./views/ExamplesView.vue')
const ChatView = () => import('./views/ChatView.vue')
const RoutingOverviewView = () => import('./views/RoutingOverviewView.vue')
const RoutingPolicyView = () => import('./views/RoutingPolicyView.vue')
const DecisionsView = () => import('./views/DecisionsView.vue')
const CorrelationsView = () => import('./views/CorrelationsView.vue')
const RoutingOverrideView = () => import('./views/RoutingOverrideView.vue')
const QualityCorrelationsView = () => import('./views/QualityCorrelationsView.vue')
const RoutingAuditView = () => import('./views/RoutingAuditView.vue')
const RequestLogsView = () => import('./views/RequestLogsView.vue')
const ModelsView = () => import('./views/ModelsView.vue')
const ProviderDetailView = () => import('./views/ProviderDetailView.vue')
const PricingManagementView = () => import('./views/PricingManagementView.vue')
const StandardModelPricingView = () => import('./views/StandardModelPricingView.vue')
const FreePoolView = () => import('./views/FreePoolView.vue')
const TenantsView = () => import('./views/TenantsView.vue')
const TenantDetailView = () => import('./views/TenantDetailView.vue')
const RoutingDashboardView = () => import('./views/RoutingDashboardView.vue')
const WorkTypesView = () => import('./views/WorkTypesView.vue')
const UsersView = () => import('./views/UsersView.vue')
const AuditLogView = () => import('./views/AuditLogView.vue')
const CompressionView = () => import('./views/CompressionView.vue')
const DataLifecycleView = () => import('./views/DataLifecycleView.vue')
const SettingsView = () => import('./views/SettingsView.vue')
const SessionContextListView = () => import('./views/session-context/SessionContextListView.vue')
const SessionContextDetailView = () => import('./views/session-context/SessionContextDetailView.vue')
const SessionCompareView = () => import('./views/SessionCompareView.vue')
const SessionListView = () => import('./views/SessionListView.vue')
const SessionAnalyticsDashboardView = () => import('./views/SessionAnalyticsDashboardView.vue')
const SessionPanoramaView = () => import('./views/SessionPanoramaView.vue')
const SessionClustersView = () => import('./views/SessionClustersView.vue')
const MaaSAccountView = () => import('./views/tenant/MaaSAccountView.vue')
const MaaSPricingView = () => import('./views/tenant/MaaSPricingView.vue')
const MaaSUsageView = () => import('./views/tenant/MaaSUsageView.vue')
const MaaSOrderView = () => import('./views/tenant/MaaSOrderView.vue')
const TenantModelsView = () => import('./views/tenant/TenantModelsView.vue')
const CredentialMonitorView = () => import('./views/CredentialMonitorView.vue')
const ProbeHealthView = () => import('./views/ProbeHealthView.vue')
const ProbeHealthDetailView = () => import('./views/ProbeHealthDetailView.vue')
const AgentRegistryView = () => import('./views/AgentRegistryView.vue')
const FormatAnomaliesView = () => import('./views/FormatAnomaliesView.vue')
const ModulesView = () => import('./views/ModulesView.vue')
const PromptInjectionSettingsView = () => import('./views/PromptInjectionSettingsView.vue')
const ApprovalConfigView = () => import('./views/ApprovalConfigView.vue')
const ApprovalListView = () => import('./views/ApprovalListView.vue')
const ApprovalDetailView = () => import('./views/ApprovalDetailView.vue')
const SessionManagementView = () => import('./views/SessionManagementView.vue')
const SessionAuditView = () => import('./views/SessionAuditView.vue')
const OutputComplianceView = () => import('./views/OutputComplianceView.vue')
const UsageCostView = () => import('./views/admin/UsageCost.vue')
const ClientAnalyticsView = () => import('./views/ClientAnalyticsView.vue')
const TaskAnalyticsView = () => import('./views/TaskAnalyticsView.vue')
const UserProfileListView = () => import('./views/UserProfileListView.vue')
const UserProfileView = () => import('./views/UserProfileView.vue')
const SessionConfigView = () => import('./views/SessionConfigView.vue')
const SessionReplayView = () => import('./views/SessionReplayView.vue')
const OpsOverviewView = () => import('./views/ops/OpsOverviewView.vue')
const LicenseManagementView = () => import('./views/ops/LicenseManagementView.vue')
const FaultManagementView = () => import('./views/ops/FaultManagementView.vue')
const AutoUpdateView = () => import('./views/ops/AutoUpdateView.vue')
const DistributionReleaseView = () => import('./views/ops/DistributionReleaseView.vue')
const CenterOpsView = () => import('./views/ops/CenterOpsView.vue')
const IpBlocklistView = () => import('./views/ops/IpBlocklistView.vue')
const VibeCodingView = () => import('./views/ops/VibeCodingView.vue')
const TenantLicenseView = () => import('./views/tenant/TenantLicenseView.vue')
const TenantAutoUpdateView = () => import('./views/tenant/TenantAutoUpdateView.vue')
const ActivationWizard = () => import('./views/ActivationWizard.vue')
const LicenseInfoView = () => import('./views/LicenseInfoView.vue')
const UpgradePanel = () => import('./views/UpgradePanel.vue')
const PluginMount = () => import('./components/PluginMount.vue')
const DownloadView = () => import('./views/public/DownloadView.vue')
const SupportView = () => import('./views/public/SupportView.vue')
const OfflineActivationView = () => import('./views/public/OfflineActivationView.vue')

// Operations Platform views. Platform management remains super-admin only;
// tenant routes below expose read-only, tenant-scoped status views.

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

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login',              component: LoginView, meta: { public: true } },
    { path: '/forbidden',          component: ForbiddenView, meta: { public: true } },
    { path: '/',                   component: HomeView, meta: { public: true } },

    // super_admin only — providers, catalog, free pool, tenants, audit logs
    { path: '/providers',          component: ProvidersView,       meta: { requiresSuper: true } },
    { path: '/providers/:id',      component: ProviderDetailView,  meta: { requiresSuper: true } },
    { path: '/key-applications',   component: KeyApplicationsView, meta: { requiresSuper: true } },
    { path: '/catalog',            redirect: (to) => ({ path: '/models', query: { ...to.query, tab: 'catalog' } }) },
    { path: '/routing-v2',         component: RoutingDashboardView, meta: { requiresSuper: true } },
    { path: '/routing-v2/credentials', component: CredentialMonitorView }, // 2026-07-04: 允许 tenant_admin 访问
    // SessionManagementView（会话管理 v2.1，super-only）原占用 /sessions，
    // 与下方 SessionListView（会话列表，所有登录用户可用）冲突——Vue Router
    // 只匹配第一条，导致 SessionListView 成为死代码、且菜单「会话列表」对非
    // super 用户显示却跳 /forbidden。现拆分：管理功能走 /admin/sessions。
    { path: '/admin/sessions',     component: SessionManagementView, meta: { requiresSuper: true } },
    { path: '/probe-health',       component: ProbeHealthView,      meta: { requiresSuper: true } },
    { path: '/probe-health/detail', component: ProbeHealthDetailView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types',         component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/settings', component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/:key',     component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-policy',     component: RoutingPolicyView,   meta: { requiresSuper: true } },
    { path: '/free-pool',          component: FreePoolView,        meta: { requiresSuper: true } },
    { path: '/tenants',            component: TenantsView,         meta: { requiresSuper: true } },
    { path: '/tenants/:tenantId',  component: TenantDetailView,    meta: { requiresSuper: true } },
    { path: '/audit-logs',        component: AuditLogView,         meta: { requiresSuper: true } },
    { path: '/format-anomalies',  component: FormatAnomaliesView,  meta: { requiresSuper: true } },
    {
      path: '/session-context',
      component: SessionContextLayout,
      children: [
        { path: '', component: SessionContextListView },
        { path: ':taskId', component: SessionContextDetailView },
      ],
    },

    // Platform ops only (super_admin + default tenant)
    { path: '/users',              component: UsersView },
    { path: '/models',             component: ModelsView, meta: { requiresPlatformOps: true } },
    { path: '/pricing',            component: PricingManagementView, meta: { requiresPlatformOps: true } },
    { path: '/model-pricing',      component: StandardModelPricingView, meta: { requiresPlatformOps: true } },

    // Tenant portal (non-default tenant self-service; admin uses ?tenant=code)
    { path: '/tenant/models',      component: TenantModelsView },
    { path: '/tenant/account',     component: MaaSAccountView },
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
    { path: '/routing',            redirect: { path: '/routing-v2', query: { tab: 'resolve' } } },
    { path: '/routing-overview',   component: RoutingOverviewView, meta: { requiresPlatformOps: true } },
    { path: '/routing-decisions',  component: DecisionsView, meta: { requiresPlatformOps: true } },
    { path: '/correlations',       component: CorrelationsView, meta: { requiresSuper: true } },
    { path: '/routing/overrides',  component: RoutingOverrideView, meta: { requiresSuper: true } },
    { path: '/routing/overrides/audit', component: RoutingAuditView, meta: { requiresSuper: true } },
    { path: '/routing/defaults',  component: () => import('./views/RoutingDefaultsView.vue'), meta: { requiresSuper: true } },
    { path: '/quality-correlations',  component: QualityCorrelationsView, meta: { requiresSuper: true } },
    { path: '/provider-quality',  redirect: '/providers' },
    { path: '/request-logs',       component: RequestLogsView },
    { path: '/session-compare',    component: SessionCompareView },
    { path: '/sessions',           component: SessionListView },
    { path: '/admin/session-analytics', component: SessionAnalyticsDashboardView, meta: { requiresSuper: true } },
    { path: '/admin/session-analytics/:id/panorama', component: SessionPanoramaView, meta: { requiresSuper: true } },
    { path: '/admin/session-clusters', component: SessionClustersView, meta: { requiresSuper: true } },
    { path: '/admin/session-audit', component: SessionAuditView, meta: { requiresSuper: true } },
    { path: '/admin/session-analytics/users', component: UserProfileListView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/users/:owner', component: UserProfileView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/clients/:id', component: ClientAnalyticsView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/tasks/:id', component: TaskAnalyticsView, meta: { requiresAuth: true } },
    { path: '/admin/session-config', component: SessionConfigView, meta: { requiresSuper: true } },
    { path: '/admin/session-replay', component: SessionReplayView, meta: { requiresSuper: true } },
    { path: '/admin/compression',   component: CompressionView, meta: { requiresPlatformOps: true } },
    { path: '/admin/data-lifecycle', component: DataLifecycleView, meta: { requiresSuper: true } },
    { path: '/admin/settings',     component: SettingsView, meta: { requiresSuper: true } },
    { path: '/admin/agents',       component: AgentRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/modules',      component: ModulesView, meta: { requiresSuper: true } },
    { path: '/admin/prompt-injection', component: PromptInjectionSettingsView, meta: { requiresSuper: true } },
    { path: '/admin/approval-config', component: ApprovalConfigView, meta: { requiresSuper: true } },
    { path: '/admin/approvals',    component: ApprovalListView, meta: { requiresSuper: true } },
    { path: '/admin/approvals/:id', component: ApprovalDetailView, meta: { requiresSuper: true } },
    { path: '/admin/output-compliance', component: OutputComplianceView, meta: { requiresSuper: true } },
    { path: '/admin/usage',        component: UsageCostView }, // 用量成本视图 (T2.4)
    { path: '/examples',           component: ExamplesView },
    { path: '/chat',               component: ChatView },

    // Operations Platform — now owned by the maintain SPA. These Gateway
    // routes redirect to /maintain/ops/* (served by the maintain-web build
    // via the Gateway reverse proxy). The component imports are kept for
    // now so the redirect can be reverted during B0; B1+ removes them once
    // the maintain pages are verified. See docs/优化v1/01 §3.
    { path: '/ops',                redirect: (to) => ({ path: '/maintain/ops/overview', query: to.query }) },
    { path: '/ops/overview',       redirect: (to) => ({ path: '/maintain/ops/overview', query: to.query }) },
    { path: '/ops/licenses',       redirect: (to) => ({ path: '/maintain/ops/licenses', query: to.query }) },
    { path: '/ops/downloads',      redirect: (to) => ({ path: '/maintain/ops/downloads', query: to.query }) },
    { path: '/ops/faults',         redirect: (to) => ({ path: '/maintain/ops/faults', query: to.query }) },
    { path: '/ops/autoupdate',     redirect: (to) => ({ path: '/maintain/ops/autoupdate', query: to.query }) },
    { path: '/ops/center',         redirect: (to) => ({ path: '/maintain/ops/center', query: to.query }) },
    { path: '/ops/blocklist',      component: IpBlocklistView, meta: { requiresSuper: true } },
    { path: '/ops/vibecoding',     redirect: (to) => ({ path: '/maintain/ops/vibecoding', query: to.query }) },

    // Tenant license/autoupdate self-service now lives under the maintain
    // namespace too. Other /tenant/* routes (models, account, pricing, ...)
    // stay on the Gateway SPA.
    { path: '/tenant/license',     redirect: (to) => ({ path: '/maintain/tenant/license', query: to.query }) },
    { path: '/tenant/autoupdate',  redirect: (to) => ({ path: '/maintain/tenant/autoupdate', query: to.query }) },

    // Customer-facing activation/license/upgrade — owned by maintain SPA.
    { path: '/activate', redirect: (to) => ({ path: '/maintain/activate', query: to.query }) },
    { path: '/license',  redirect: (to) => ({ path: '/maintain/license', query: to.query }) },
    { path: '/upgrade',  redirect: (to) => ({ path: '/maintain/upgrade', query: to.query }) },

    // Public distribution portal — owned by maintain SPA.
    { path: '/download',           redirect: (to) => ({ path: '/maintain/download', query: to.query }) },
    { path: '/support',            redirect: (to) => ({ path: '/maintain/support', query: to.query }) },
    { path: '/offline-activation', redirect: (to) => ({ path: '/maintain/offline-activation', query: to.query }) },

    // Plugin runtime — iframe-mounted plugin web assets (reverse-proxied at /plugins/<id>/)
    { path: '/plugins/:pluginId/:page(.*)*', component: PluginMount, meta: { requiresAuth: true } },

    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach((to) => {
  // 2026-07-09: auth probe 还没完成时不要做任何 redirect，否则会在 cookie 登录后
  // 把用户弹回首页 / login，App.vue 的 hydration 永远没机会切到 app-layout。
  // 让 /api/auth/me 先 settle（store.authHydrated=true）再评估 auth。
  if (!store.authHydrated) {
    // App.vue performs the cookie probe from onMounted. A guard promise here
    // would block that mount and leave RouterView as an empty comment node.
    // Evaluate the currently persisted credentials and let App.vue settle the
    // final layout once hydration completes. App.vue redirects an unauthenticated
    // user after the probe settles.
    return
  }
  // 1. Auth check — unauthenticated users land on home, not full-page login
  if (!to.meta.public && !isAuthed()) {
    return { path: '/', query: { login: '1', redirect: to.fullPath } }
  }
  // 2. Bounce authed users away from /login
  if (to.path === '/login' && isAuthed()) {
    return { path: '/' }
  }
  // 3. Super-admin role check
  if (to.meta.requiresSuper && !isSuperAdmin()) {
    return { path: '/forbidden' }
  }
  // 4. Platform ops (super_admin on default tenant) for运维向页面
  if (to.meta.requiresPlatformOps && !isPlatformOpsView()) {
    return { path: '/' }
  }
  // 4.5. Maintain service routes (only default tenant can access)
  if (to.meta.requiresMaintain && !canAccessMaintain()) {
    return { path: '/forbidden' }
  }
  if (to.meta.tenantOps && isPlatformOpsView() && typeof to.query.tenant !== 'string') {
    return { path: '/', query: { tenant: 'required' } }
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
