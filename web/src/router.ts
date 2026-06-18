import { createRouter, createWebHistory } from 'vue-router'
import { store, isDefaultTenant } from './store'

import LoginView              from './views/LoginView.vue'
import HomeView               from './views/HomeView.vue'
import ProvidersView          from './views/ProvidersView.vue'
import KeysView               from './views/KeysView.vue'
import KeyDetailView          from './views/KeyDetailView.vue'
import KeyApplicationsView    from './views/KeyApplicationsView.vue'
import ExamplesView           from './views/ExamplesView.vue'
import ChatView               from './views/ChatView.vue'
import RoutingOverviewView    from './views/RoutingOverviewView.vue'
import RoutingPolicyView      from './views/RoutingPolicyView.vue'
import DecisionsView          from './views/DecisionsView.vue'
import CorrelationsView       from './views/CorrelationsView.vue'
import RoutingOverrideView   from './views/RoutingOverrideView.vue'
import QualityCorrelationsView from './views/QualityCorrelationsView.vue'
import RoutingAuditView from './views/RoutingAuditView.vue'
import RequestLogsView        from './views/RequestLogsView.vue'
import ModelsView             from './views/ModelsView.vue'
import ProviderDetailView     from './views/ProviderDetailView.vue'
import PricingManagementView  from './views/PricingManagementView.vue'
import StandardModelPricingView from './views/StandardModelPricingView.vue'
import FreePoolView           from './views/FreePoolView.vue'
import TenantsView            from './views/TenantsView.vue'
import TenantDetailView       from './views/TenantDetailView.vue'
import RoutingDashboardView   from './views/RoutingDashboardView.vue'
import WorkTypesView          from './views/WorkTypesView.vue'
import UsersView              from './views/UsersView.vue'
import AuditLogView          from './views/AuditLogView.vue'
import SessionContextLayout      from './layouts/SessionContextLayout.vue'
import SessionContextListView    from './views/session-context/SessionContextListView.vue'
import SessionContextDetailView  from './views/session-context/SessionContextDetailView.vue'
import ForbiddenView          from './views/ForbiddenView.vue'
import MaaSAccountView        from './views/tenant/MaaSAccountView.vue'
import MaaSPricingView        from './views/tenant/MaaSPricingView.vue'
import MaaSUsageView          from './views/tenant/MaaSUsageView.vue'
import MaaSOrderView          from './views/tenant/MaaSOrderView.vue'
import TenantModelsView       from './views/tenant/TenantModelsView.vue'

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
    { path: '/routing-v2/work-types',         component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/settings', component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/:key',     component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-policy',     component: RoutingPolicyView,   meta: { requiresSuper: true } },
    { path: '/free-pool',          component: FreePoolView,        meta: { requiresSuper: true } },
    { path: '/tenants',            component: TenantsView,         meta: { requiresSuper: true } },
    { path: '/tenants/:tenantId',  component: TenantDetailView,    meta: { requiresSuper: true } },
    { path: '/audit-logs',        component: AuditLogView,         meta: { requiresSuper: true } },
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
