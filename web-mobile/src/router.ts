import { createRouter, createWebHistory } from 'vue-router'
import { nextSessionEpoch } from './runtime/epochs'

// router.ts — Vue Router 是路由所有权三真源（UI 规范 06 §4）。
// base '/m' 与 vite base、MobileStaticHandler 三处同门。
const router = createRouter({
  history: createWebHistory('/m'),
  routes: [
    { path: '/login', component: () => import('./views/LoginView.vue'), meta: { titleKey: 'login.title', public: true } },
    { path: '/', component: () => import('./views/OverviewView.vue'), meta: { titleKey: 'nav.overview' } },
    { path: '/nodes', component: () => import('./views/NodesView.vue'), meta: { titleKey: 'nodes.title' } },
    { path: '/models', component: () => import('./views/ModelsView.vue'), meta: { titleKey: 'models.title' } },
    { path: '/keys', component: () => import('./views/KeysView.vue'), meta: { titleKey: 'keys.title' } },
    { path: '/alerts', component: () => import('./views/AlertsView.vue'), meta: { titleKey: 'alerts.title' } },
    { path: '/usage', component: () => import('./views/UsageView.vue'), meta: { titleKey: 'usage.title' } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

// 登录页守卫：非 public 路由在未水合时放行（/api/auth/me 401 由 _core 兜底
// 跳登录）；这里只负责登录成功后的代次翻新。
router.afterEach((to) => {
  if (to.path === '/login') {
    // 进入登录页 = 会话边界：旧代次的在途响应/缓存全部作废（R2）。
    nextSessionEpoch()
  }
})

export default router
