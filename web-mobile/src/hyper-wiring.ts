import type { Router } from 'vue-router'
import { initHyper } from '@/hyper'
import { bindAuthContext, setUnauthorizedHandler } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { routeAllowedFor } from '@/config/appNav'
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
      // 批 150：路由层消费 `nav.requiresRole`（合上"meta.requiresRole 全仓无人消费"那个壳层缺口）。
      //   视图内兜底（format-anomalies / model-integrity-drift 见对应 .vue）也独立判断同一个 role，
      //   ⇒ 路由被绕过（程序化导航）时仍有兜底。
      // ★ 与 RestrictedPageTitle 8-4 互证：本行存在 ⇒ beforeEach 真消费 requiresRole ⇒ 缺口合上。
      if (!routeAllowedFor(to.path, auth.role)) {
        return { path: '/restricted', query: { from: to.fullPath } }
      }
      return true
    })
  })
}
