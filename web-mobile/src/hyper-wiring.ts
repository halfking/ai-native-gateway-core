import type { Router } from 'vue-router'
import { initHyper } from '@/hyper'
import { bindAuthContext, setUnauthorizedHandler } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'

// 应用装配：Hyper 运行时 + 鉴权上下文 + 401 单飞 + 路由守卫。
// 从 main.ts 单点调用，避免各组件重复绑定。

export function wireApp(router: Router): void {
  const auth = useAuthStore()

  bindAuthContext({
    getUserInfo: () => auth.userInfo,
    getBearer: () => auth.bearer(),
    isAuthenticated: () => auth.isAuthenticated,
  })

  setUnauthorizedHandler((currentPath) => {
    // 401 → 登录 = replace（06 §4 语义表），带回 redirect
    void router.replace({ path: '/login', query: { redirect: currentPath } })
  })

  initHyper(router, {
    appName: t('app.name'),
    getAccountId: () => auth.accountId,
    translate: (key) => t(key),
    getFallback: () => '/',
  })

  // 守卫：先等水合（App.vue onMounted 已发出 /api/auth/me 探测），
  // 未登录访问受保护页 → 登录页（deepLink 场景常见）。
  let hydration: Promise<void> | null = null
  router.beforeEach((to) => {
    if (to.meta.requiresAuth === false) return true
    if (!hydration) {
      hydration = auth.authHydrated ? Promise.resolve() : auth.hydrate()
    }
    return hydration.then(() => {
      if (!auth.isAuthenticated) {
        return { path: '/login', query: { redirect: to.fullPath } }
      }
      return true
    })
  })
}
