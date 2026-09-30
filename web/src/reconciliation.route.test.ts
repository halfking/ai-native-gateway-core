// reconciliation.route.test.ts —— 「登录后的外壳」这一段的回归门（2026-09-30）。
//
// 背景：ReconciliationReport.test.ts 只证明了**组件**能渲染，但页面真正能不能
// 被打开，取决于 router 守卫链。这里有**两个静默改道**，组件测试完全看不见：
//   ① `!to.meta.public && !isAuthed()` → 弹回 `/?login=1&redirect=…`
//   ② `to.meta.requiresSuper && !isSuperAdmin()` → 弹去 `/forbidden`
// 而 `/admin/reconciliation` 的 meta 正是 `{ requiresSuper: true }`。
//
// 真实故障形态：组件好好的，用户打开却是首页或 /forbidden，页面上一个字都不
// 解释——从组件侧看「一切正常」。这道门把「谁能进、进的到底是不是对账页、
// 深链参数有没有被守卫吃掉」钉住。
//
// 为什么用轻量 stub 而不导入真的 ReconciliationReport.vue：实测在 jsdom 里
// 「真 vue-router + 真 .vue 组件」同在一个测试文件会挂住 vitest 进程（既有的
// 两个组件测试都 mock 掉了 vue-router，所以从没暴露）。而本门要验的是**守卫链**
// 与**注册关系**，不是组件渲染——组件渲染已由 ReconciliationReport.test.ts 覆盖。
// 于是这里用 stub 组件跑守卫，路由到底绑的是哪个组件则读 router.ts 源码核对。
import { describe, expect, it } from 'vitest'
import { createRouter, createMemoryHistory } from 'vue-router'
import { h } from 'vue'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/** 复刻 router.ts 的守卫里与本路由相关的两步（auth + 角色）。 */
interface FakeUser {
  jwtToken: string
  role?: string
}
function installGuards(router: ReturnType<typeof makeRouter>, user: FakeUser | null) {
  router.beforeEach((to) => {
    // ① 未登录：弹回首页，redirect 必须原样带上目标（登录完要能回来）
    if (!to.meta.public && !user) {
      return { path: '/', query: { login: '1', redirect: to.fullPath } }
    }
    // ② requiresSuper：非超管弹去 /forbidden
    if (to.meta.requiresSuper && user?.role !== 'super_admin') {
      return { path: '/forbidden' }
    }
  })
}

const Stub = { render: () => h('div', 'stub') }

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      // public 标记必须与 router.ts 一致（router.ts:175/176/179）。漏掉它会让
      // 守卫①把未登录用户弹到 `/` 之后，`/` 又被守卫①弹回 `/` —— 无限重定向，
      // 表现为 vitest 进程 CPU 打满 100% 却不退出。生产上 `/` 确实是 public。
      { path: '/', component: { render: () => h('div', 'home') }, meta: { public: true } },
      { path: '/forbidden', component: { render: () => h('div', 'forbidden') }, meta: { public: true } },
      { path: '/admin/reconciliation', component: Stub, meta: { requiresSuper: true } },
    ],
  })
}

const routerSrc = readFileSync(resolve(process.cwd(), 'src/router.ts'), 'utf8')
const reconLine = routerSrc.split('\n').find((l) => l.includes("path: '/admin/reconciliation'")) ?? ''

describe('/admin/reconciliation 的可达性', () => {
  it('超管访问 → 真的落到对账路由上（不是"守卫放行但停在别处"）', async () => {
    const router = makeRouter()
    installGuards(router, { jwtToken: 't', role: 'super_admin' })
    router.push('/admin/reconciliation')
    await router.isReady()

    expect(router.currentRoute.value.path).toBe('/admin/reconciliation')
    expect(router.currentRoute.value.matched[0].components?.default).toBe(Stub)
  })

  it('未登录 → 弹回首页，且 redirect 原样保留目标（登录后能回来）', async () => {
    const router = makeRouter()
    installGuards(router, null)
    router.push('/admin/reconciliation?view=internal')
    await router.isReady()

    const cur = router.currentRoute.value
    expect(cur.path).toBe('/')
    expect(cur.query.login).toBe('1')
    // 这条最容易被改坏：redirect 丢了的话，登录完用户就回不到对账页。
    expect(cur.query.redirect).toBe('/admin/reconciliation?view=internal')
  })

  it('租户管理员 → /forbidden（requiresSuper 是真在拦，不是摆设）', async () => {
    const router = makeRouter()
    installGuards(router, { jwtToken: 't', role: 'tenant_admin' })
    router.push('/admin/reconciliation')
    await router.isReady()

    expect(router.currentRoute.value.path).toBe('/forbidden')
  })

  it('深链 ?view=internal 原样透传（守卫不吃 query）', async () => {
    const router = makeRouter()
    installGuards(router, { jwtToken: 't', role: 'super_admin' })
    router.push('/admin/reconciliation?view=internal')
    await router.isReady()

    expect(router.currentRoute.value.query.view).toBe('internal')
  })

  it('真实 router.ts：这条路由存在、绑定的是对账组件、且带 requiresSuper', () => {
    // 上面四条验的是本文件复刻的守卫；若有人**同时**改掉 router.ts 的注册并
    // 改掉这里的复刻，两边会一起变绿。这条直接盯真实注册处，堵住这个漏网。
    expect(reconLine, 'router.ts 里找不到 /admin/reconciliation 的注册行').not.toBe('')
    expect(reconLine).toContain('ReconciliationReportView')
    expect(reconLine).toContain('requiresSuper: true')
  })

  it('真实 router.ts：`/` 与 `/forbidden` 仍是 public（守卫不会把自己弹成死循环）', () => {
    // 上一轮这个门第一次跑直接把 vitest 进程 CPU 打满到超时：stub router 漏了
    // public 标记，未登录用户被弹到 `/` 后又被同一条守卫弹回，形成无限重定向。
    // 生产之所以没事，只因为 router.ts 给 `/` 标了 public——所以这条把那两个
    // 标记也钉住，谁摘掉谁红，而不是等到线上才发现「页面打不开」。
    for (const p of ["path: '/'", "path: '/forbidden'"]) {
      const line = routerSrc.split('\n').find((l) => l.includes(p))
      expect(line, `router.ts 里找不到 ${p}`).toBeTruthy()
      expect(line).toContain('public: true')
    }
  })
})
