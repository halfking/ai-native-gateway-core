import { createRouter, createWebHistory } from 'vue-router'
import { store, isDefaultTenant } from './store'

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
const ApprovalConfigView = () => import('./views/ApprovalConfigView.vue')
const ApprovalListView = () => import('./views/ApprovalListView.vue')
const ApprovalDetailView = () => import('./views/ApprovalDetailView.vue')

function isAuthed(): boolean {
  return !!(store.jwtToken || store.apiKey)
}

function isSuperAdmin(): boolean {
  // Legacy API key auth: no JWT but has apiKey → super_admin
  if (!store.jwtToken && store.apiKey) return true
  return store.userInfo?.role === 'super_admin'
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
    { path: '/probe-health',       component: ProbeHealthView, meta: { requiresSuper: true } },
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
    { path: '/routing/overrides',  component: RoutingOverrideView, meta: { requiresSuperAdmin: true } },
    { path: '/routing/overrides/audit', component: RoutingAuditView, meta: { requiresSuperAdmin: true } },
    { path: '/quality-correlations',  component: QualityCorrelationsView, meta: { requiresSuperAdmin: true } },
    { path: '/request-logs',       component: RequestLogsView },
    { path: '/session-compare',    component: SessionCompareView },
    { path: '/sessions',           component: SessionListView },
    { path: '/admin/session-analytics', component: SessionAnalyticsDashboardView },
    { path: '/admin/session-analytics/:id/panorama', component: SessionPanoramaView },
    { path: '/admin/session-clusters', component: SessionClustersView },
    { path: '/admin/compression',   component: CompressionView, meta: { requiresPlatformOps: true } },
    { path: '/admin/data-lifecycle', component: DataLifecycleView, meta: { requiresPlatformOps: true } },
    { path: '/admin/settings',     component: SettingsView, meta: { requiresSuper: true } },
    { path: '/admin/agents',       component: AgentRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/modules',      component: ModulesView, meta: { requiresSuper: true } },
    { path: '/admin/approval-config', component: ApprovalConfigView, meta: { requiresSuper: true } },
    { path: '/admin/approvals',    component: ApprovalListView, meta: { requiresSuper: true } },
    { path: '/admin/approvals/:id', component: ApprovalDetailView, meta: { requiresSuper: true } },
    { path: '/examples',           component: ExamplesView },
    { path: '/chat',               component: ChatView },

    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach((to) => {
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
  // 5. Default-tenant ops must not browse tenant portal without ?tenant= context
  if (
    to.path.startsWith('/tenant/') &&
    isPlatformOpsView() &&
    typeof to.query.tenant !== 'string'
  ) {
    return { path: '/' }
  }
})
