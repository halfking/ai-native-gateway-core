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
