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
