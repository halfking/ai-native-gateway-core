import { createRouter, createWebHistory } from 'vue-router'
import { store } from './store'

import LoginView              from './views/LoginView.vue'
import DashboardView          from './views/DashboardView.vue'
import ProvidersView          from './views/ProvidersView.vue'
import KeysView               from './views/KeysView.vue'
import KeyDetailView          from './views/KeyDetailView.vue'
import KeyApplicationsView    from './views/KeyApplicationsView.vue'
import CatalogView            from './views/CatalogView.vue'
import ExamplesView           from './views/ExamplesView.vue'
import RoutingOverviewView    from './views/RoutingOverviewView.vue'
import RoutingPolicyView      from './views/RoutingPolicyView.vue'
import DecisionsView          from './views/DecisionsView.vue'
import CorrelationsView       from './views/CorrelationsView.vue'
import RequestLogsView        from './views/RequestLogsView.vue'
import ModelsView             from './views/ModelsView.vue'
import ProviderDetailView     from './views/ProviderDetailView.vue'
import PricingManagementView  from './views/PricingManagementView.vue'
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
import MaaSModelsView         from './views/maas/MaaSModelsView.vue'
import MaaSPricingView        from './views/maas/MaaSPricingView.vue'
import MaaSUsageView          from './views/maas/MaaSUsageView.vue'

function isAuthed(): boolean {
  return !!(store.jwtToken || store.apiKey)
}

function isSuperAdmin(): boolean {
  // Legacy API key auth: no JWT but has apiKey → super_admin
  if (!store.jwtToken && store.apiKey) return true
  return store.userInfo?.role === 'super_admin'
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login',              component: LoginView, meta: { public: true } },
    { path: '/forbidden',          component: ForbiddenView, meta: { public: true } },
    { path: '/',                   component: DashboardView },

    // super_admin only — providers, catalog, free pool, tenants, audit logs
    { path: '/providers',          component: ProvidersView,       meta: { requiresSuper: true } },
    { path: '/providers/:id',      component: ProviderDetailView,  meta: { requiresSuper: true } },
    { path: '/key-applications',   component: KeyApplicationsView, meta: { requiresSuper: true } },
    { path: '/catalog',            component: CatalogView,         meta: { requiresSuper: true } },
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
      meta: { requiresSuper: true },
      children: [
        { path: '', component: SessionContextListView },
        { path: ':taskId', component: SessionContextDetailView },
      ],
    },

    // Both super_admin and tenant_admin (read-only for tenant_admin)
    { path: '/users',              component: UsersView },
    { path: '/models',             component: ModelsView },
    { path: '/pricing',            component: PricingManagementView },

    // MaaS customer-facing (any authenticated user)
    { path: '/maas/models',        component: MaaSModelsView },
    { path: '/maas/pricing',       component: MaaSPricingView },
    { path: '/maas/usage',         component: MaaSUsageView },

    // Tenant-isolated (any authenticated user, scoped to own tenant for tenant_admin)
    { path: '/keys',               component: KeysView },
    { path: '/keys/:id',           component: KeyDetailView },
    { path: '/routing',            redirect: { path: '/routing-v2', query: { tab: 'resolve' } } },
    { path: '/routing-overview',   component: RoutingOverviewView },
    { path: '/routing-decisions',  component: DecisionsView },
    { path: '/correlations',       component: CorrelationsView, meta: { requiresSuperAdmin: true } },
    { path: '/request-logs',       component: RequestLogsView },
    { path: '/examples',           component: ExamplesView },

    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach((to) => {
  // 1. Auth check
  if (!to.meta.public && !isAuthed()) {
    return { path: '/login' }
  }
  // 2. Bounce authed users away from /login
  if (to.path === '/login' && isAuthed()) {
    return { path: '/' }
  }
  // 3. Super-admin role check
  if (to.meta.requiresSuper && !isSuperAdmin()) {
    return { path: '/forbidden' }
  }
})
