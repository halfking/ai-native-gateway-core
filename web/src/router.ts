import { createRouter, createWebHistory } from 'vue-router'
import { store } from './store'

import LoginView              from './views/LoginView.vue'
import DashboardView          from './views/DashboardView.vue'
import ProvidersView          from './views/ProvidersView.vue'
import ProviderDetailView     from './views/ProviderDetailView.vue'
import KeysView               from './views/KeysView.vue'
import KeyDetailView          from './views/KeyDetailView.vue'
import KeyApplicationsView    from './views/KeyApplicationsView.vue'
import CatalogView            from './views/CatalogView.vue'
import ExamplesView           from './views/ExamplesView.vue'
import RoutingTestView        from './views/RoutingTestView.vue'
import RoutingOverviewView    from './views/RoutingOverviewView.vue'
import RoutingPolicyView      from './views/RoutingPolicyView.vue'
import DecisionsView          from './views/DecisionsView.vue'
import RequestLogsView        from './views/RequestLogsView.vue'
import ModelsView             from './views/ModelsView.vue'
import FreePoolView           from './views/FreePoolView.vue'
import PricingManagementView  from './views/PricingManagementView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login',              component: LoginView, meta: { public: true } },
    { path: '/',                   component: DashboardView },
    { path: '/providers',          component: ProvidersView },
    { path: '/providers/:id',      component: ProviderDetailView },
    { path: '/keys',               component: KeysView },
    { path: '/keys/:id',           component: KeyDetailView },
    { path: '/key-applications',   component: KeyApplicationsView },
    { path: '/catalog',            component: CatalogView },
    { path: '/models',             component: ModelsView },
    { path: '/examples',           component: ExamplesView },
    { path: '/routing',            component: RoutingTestView },
    { path: '/routing-overview',   component: RoutingOverviewView },
    { path: '/routing-policy',     component: RoutingPolicyView },
    { path: '/routing-decisions',  component: DecisionsView },
    { path: '/free-pool',          component: FreePoolView },
    { path: '/request-logs',       component: RequestLogsView },
    { path: '/pricing',            component: PricingManagementView },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach((to) => {
  if (!to.meta.public && !store.apiKey) {
    return { path: '/login' }
  }
  if (to.path === '/login' && store.apiKey) {
    return { path: '/' }
  }
})
