import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import AppDrawer from './AppDrawer.vue'
import { DRAWER_NAV, BOTTOM_NAV, navItemsFor, isNavItemActive } from '@/config/appNav'
import { useAuthStore } from '@/stores/auth'

/**
 * 导航权限门控（2026-10-06）。
 *
 * ★ 这条是被**自查**发现的，不是用户报的：`/integrity` 整段是 h.superAdmin
 *   （后端 admin/handler.go:924-925），而 AppDrawer 此前 `v-for="item in DRAWER_NAV"`
 *   **无条件渲染全量** ⇒ tenant_admin 抽屉里躺着一个点进去必然 403 的入口。
 *   这与 17 §11.1 对凭据操作区定的规矩是同一个问题的上下游两端：
 *   「按 role 分档渲染，不是一律显示再吃后端 403」。导航是最上游那一端。
 *
 * 判据钉三件事：
 * ① tenant_admin 的抽屉里**没有** /integrity；
 * ② super_admin 的抽屉里**有** /integrity；
 * ③ admin 档页面（/logs、/providers、/routing）对两者**都**可见 ——
 *    它们走 h.admin，tenant_admin 后端是允许的，挡掉反而是误伤。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

let mounted: Array<{ unmount(): void }> = []

async function mountDrawerAs(role: string) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().userInfo = {
    id: 1, tenant_id: 't1', username: 'u', display_name: 'U',
    email: 'e', role, enabled: true,
  }
  // AppDrawer 里 useRoute() + watch(route.fullPath)，必须挂真 router 上下文。
  // 用 memory history 而不是 mock useRoute —— 后者会让 isNavItemActive
  // 与真实路由表脱节，测出来的东西不成立。
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div/>' } },
      ...DRAWER_NAV.map((n) => ({ path: n.to, component: { template: '<div/>' } })),
    ],
  })
  await router.push('/')
  await router.isReady()
  const w = mount(AppDrawer, {
    props: { modelValue: true },
    attachTo: document.body,
    global: { plugins: [pinia, router] },
  })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

describe('AppDrawer 权限门控', () => {
  beforeEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
    document.body.innerHTML = ''
    vi.resetAllMocks()
  })
  afterEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
  })

  it('tenant_admin 抽屉里没有 /integrity（superAdmin 档，后端必 403）', async () => {
    await mountDrawerAs('tenant_admin')
    const hrefs = Array.from(document.body.querySelectorAll('.drawer__link'))
      .map((a) => a.getAttribute('href') ?? '')
    expect(hrefs.some((h) => h.includes('/integrity'))).toBe(false)
    // Teleport 到 body ⇒ 用 document 文本，不用 wrapper.text()
    expect(document.body.textContent).not.toContain('Model integrity')
  })

  it('super_admin 抽屉里有 /integrity', async () => {
    await mountDrawerAs('super_admin')
    const hrefs = Array.from(document.body.querySelectorAll('.drawer__link'))
      .map((a) => a.getAttribute('href') ?? '')
    expect(hrefs.some((h) => h.includes('/integrity'))).toBe(true)
  })

  it('★ admin 档页面（logs/providers/routing）对 tenant_admin 也可见（挡掉是误伤）', async () => {
    await mountDrawerAs('tenant_admin')
    const hrefs = Array.from(document.body.querySelectorAll('.drawer__link'))
      .map((a) => a.getAttribute('href') ?? '')
    for (const p of ['/logs', '/providers', '/routing']) {
      expect(hrefs.some((h) => h.includes(p))).toBe(true)
    }
  })
})

describe('navItemsFor', () => {
  it('super_admin 看到全部', () => {
    expect(navItemsFor(DRAWER_NAV, 'super_admin').length).toBe(DRAWER_NAV.length)
  })

  it('tenant_admin 少掉的**只有** superAdmin 档那些', () => {
    const filtered = navItemsFor(DRAWER_NAV, 'tenant_admin')
    const dropped = DRAWER_NAV.filter((i) => !filtered.some((f) => f.key === i.key))
    // ★ 白名单式断言：把 superAdmin 档的 key 逐个列出来。排序后再比，
    //   避免依赖 DRAWER_NAV 的书写顺序（顺序不是契约）。
    //   加新 superAdmin 档页面时这条会红 —— 那正是它该做的。
    expect(dropped.map((d) => d.key).sort()).toEqual([
      'flow',
      'funnel',
      'integrity',
      'matrix',
      'overrides',
      'proposals',
      'routing-audit',
    ])
  })

  // ★ 不变量本身：被挡掉的项**必须**都是 requiresRole:'super_admin'。
  //   只断言「挡了哪几个」会漏掉反向错误——比如某项被 requiresRole 误标成
  //   super_admin 从而对所有 admin 档用户消失，而白名单里还有它。
  it('被挡掉的项全部确实是 superAdmin 档（不得误伤 admin 档）', () => {
    const filtered = navItemsFor(DRAWER_NAV, 'tenant_admin')
    for (const i of filtered) {
      expect(i.requiresRole ?? 'admin').not.toBe('super_admin')
    }
  })

  it('role 为空（未 hydrate 完成）也照样只挡 superAdmin 档', () => {
    // 登录前 role 是 ''，此时不该因为「读不到角色」就把 admin 档页面全藏起来
    const filtered = navItemsFor(DRAWER_NAV, '')
    expect(filtered.some((f) => f.key === 'logs')).toBe(true)
    expect(filtered.some((f) => f.key === 'integrity')).toBe(false)
  })

  it('底栏当前无 superAdmin 档，过滤前后一致（防将来加错）', () => {
    expect(navItemsFor(BOTTOM_NAV, 'tenant_admin').length).toBe(BOTTOM_NAV.length)
  })
})

describe('isNavItemActive', () => {
  it('exact 项只匹配自身', () => {
    const home = BOTTOM_NAV.find((n) => n.key === 'home')!
    expect(isNavItemActive(home, '/')).toBe(true)
    expect(isNavItemActive(home, '/nodes')).toBe(false)
  })

  it('非 exact 项匹配自身与子路径', () => {
    const nodes = BOTTOM_NAV.find((n) => n.key === 'nodes')!
    expect(isNavItemActive(nodes, '/nodes')).toBe(true)
    expect(isNavItemActive(nodes, '/nodes/123')).toBe(true)
    expect(isNavItemActive(nodes, '/models')).toBe(false)
  })

  it('★ 前缀相同但不是子路径的不算激活（/logs 不激活 /logs-archive）', () => {
    const logs = DRAWER_NAV.find((n) => n.key === 'logs')!
    expect(isNavItemActive(logs, '/logs-archive')).toBe(false)
  })
})
