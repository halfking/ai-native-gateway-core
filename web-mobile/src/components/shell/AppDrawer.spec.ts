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

async function mountDrawerAs(role: string, startPath = '/') {
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
  await router.push(startPath)
  await router.isReady()
  const w = mount(AppDrawer, {
    props: { modelValue: true },
    attachTo: document.body,
    global: { plugins: [pinia, router] },
  })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return Object.assign(w, { __router: router })
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
      // ★ 2026-10-08：自动路由读面（整条 auto-route 线由 handler.go:1381 / :1430
      //   的 h.superAdmin 挂载）。注意形参名 `adminWrap` 是假名 ——
      //   权限得读到调用处实际传的那个值。
      'auto-route',
      // ★ 2026-10-07 第四十九轮：凭据×模型实时状态（credential_state_handlers.go:175
      //   `wrap := h.superAdmin`）—— 这一条**要** super_admin，与前几批 admin 档页面相反。
      'credential-model-state',
      'flow',
      // ★ 2026-10-14 第一百零九批：响应格式异常
      //   （admin/handler.go:913-914 两条都是 h.superAdmin）⇒ 这一条**要** super_admin。
      //   ★ 本族虽是第六次「不隔离」，但档位本身就是那道门 ⇒ 视图内无需段落级门控。
      'format-anomalies',
      'funnel',
      'integrity',
      'maas-admin-catalog',
      'maas-orders',
      'maas-rates',
      'maas-tenant-ops',
      'matrix',
      // ★ 2026-10-14 第一百一十批：指纹漂移
      //   （admin/handler.go:942-943 两条都是 h.superAdmin）⇒ 这一条**要** super_admin。
      //   ★ 与批 109 的 format-anomalies 同档 ⇒ 两条合起来把 superAdmin 档的席钉死。
      'model-integrity-drift',
      'model-policies',
      'model-policy-audit',
      'overrides',
      'proposals',
      'reconciliation',
      // ★ 2026-10-14 第一百一十一批：对账报表（admin/handler.go:1086 h.superAdmin）
      //   ⇒ **要** super_admin。⚠️ 与上面 reconciliation 是两个不同功能，别接错。
      'report-rollup',
      'request-anomalies',
      'routing-audit',
      // ★ 2026-10-08 第一百零一批：路由策略配置面（`admin/handler.go:1200 :1201 :1215`
      //   是 h.superAdmin）。同族的 `featured-models`（`:1216`）其实是 admin 档，
      //   但这一席整体按 superAdmin 挡 —— 混档会让「谁能看哪块」不可解释。
      'routing-policy',
      'task-index',
      'tenants',
      // ★ 2026-10-08 第一百零三批：工作类型配置面。`admin/handler.go:1434`
      //   `wtH.RegisterWorkTypeRoutes(mux, h.superAdmin)` ⇒ 整族四条 GET 都是 superAdmin。
      //   ★ 与批 90 的 settings 同前缀邻域却是 admin 档，权限**不得**按前缀判。
      'work-types',
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

/**
 * 抽屉导航后**必须收起**（2026-10-11 真机/模拟器端到端实测缺陷）。
 *
 * 现象：iOS 26 模拟器上 Maestro 连跑两轮同一段「打开抽屉 → 点供应商」，
 * 一轮正常收起，另一轮**抽屉压在供应商列表上不消失**（抽屉里「供应商」
 * 已高亮 ⇒ 路由确实变了，但面板还在）。后续点击全打在抽屉上，整页不可用。
 *
 * 根因边界：`watch(route.fullPath)` 单独作为收口存在竞态（路由变了但
 * modelValue 没落下去）。判据因此钉在**点击本身**这条确定性路径上：
 * 点导航项 ⇒ 立刻 emit('update:modelValue', false)，不依赖 watcher。
 */
describe('AppDrawer 导航后收起', () => {
  beforeEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
    document.body.innerHTML = ''
    vi.resetAllMocks()
  })

  it('点导航项立即 emit update:modelValue=false（不依赖路由 watcher）', async () => {
    const w = await mountDrawerAs('super_admin')
    const link = Array.from(document.body.querySelectorAll<HTMLAnchorElement>('a.drawer__link'))
      .find((a) => a.getAttribute('href') === '/providers')
    expect(link, '抽屉里应有指向 /providers 的入口').toBeTruthy()
    link!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()
    expect(w.emitted('update:modelValue')).toContainEqual([false])
  })

  it('★ 点不同路由的导航项：既收起抽屉，也**真的导航过去了**', async () => {
    // 钉两条不变量：抽屉收起 **且** 路由真的变了。
    // 只断言 emit(false) 是不够的 —— 「抽屉关了但人还停在原页」这种状态
    // emit 也是 false，却完全不可用。
    //
    // ⚠️ 但必须说清楚这条**能覆盖什么、不能覆盖什么**：
    // 真机上出现过的那个回归是「同步 emit 导致 Teleport 里的 RouterLink 在
    // navigate() 之前被卸载」。jsdom 复现不了 —— Vue 的 scheduler 在微任务里
    // 刷新，同步 emit 也不会同步卸载（已实测：把 onNavClick 改回同步 emit，
    // 本文件 15 条照样全绿）。所以这一条能钉「结果」，**钉不住时序**；
    // 时序那一半的判据是模拟器上的 flow 08，不是本文件。
    const w = await mountDrawerAs('super_admin')
    const link = Array.from(document.body.querySelectorAll<HTMLAnchorElement>('a.drawer__link'))
      .find((a) => a.getAttribute('href') === '/providers')
    link!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()
    await flushPromises()
    expect(w.__router.currentRoute.value.path).toBe('/providers')
    expect(w.emitted('update:modelValue')).toContainEqual([false])
  })

  it('★ 点「当前已经在的」那个导航项也必须收起 —— 路由没变，watcher 不会响', async () => {
    // 本组里能区分「点击收口」与「路由 watcher 收口」的那一条：
    // 已经停在 /logs 时再点 /logs，route.fullPath **不变**，watcher 不触发，
    // 只有 onNavClick 会 emit。
    // 刻意用 /logs 而不是 /providers —— 证明实现里没写死某条路径。
    // 把 onNavClick 整个删掉，这一条会红（已实测）。
    const w = await mountDrawerAs('super_admin', '/logs')
    const self = Array.from(document.body.querySelectorAll<HTMLAnchorElement>('a.drawer__link'))
      .find((a) => a.getAttribute('href') === '/logs')
    expect(self, '应在 /logs 页仍能看到抽屉入口').toBeTruthy()
    const before = (w.emitted('update:modelValue') ?? []).length
    self!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()
    const after = (w.emitted('update:modelValue') ?? []).slice(before)
    expect(after, '路由未变化时点击也必须收起抽屉').toContainEqual([false])
  })

  it('★ 穷举：站在每个导航项自己的路由上点它，都必须收起（不抽样、不写死路径）', async () => {
    // 守卫是 `route.path === to`，一旦实现里写死某条路径，这里就会红。
    // ⚠️ 注意这条不能靠「从 / 出发点每个链接」来写 —— 那条路径上
    // route.fullPath **会变**，watcher 会替 onNavClick 把活干完，
    // 把 onNavClick 整个删掉也照样全绿（已实测）。必须站在该项自己的路由上点它。
    for (const item of DRAWER_NAV) {
      const w = await mountDrawerAs('super_admin', item.to)
      // 只取 <a>：抽屉里「我的」也是 .drawer__link，但它是 <button>，
      // emit 的是 account 而非「收起」，混进来会让计数对不上。
      const self = Array.from(document.body.querySelectorAll<HTMLAnchorElement>('a.drawer__link'))
        .find((a) => a.getAttribute('href') === item.to)
      expect(self, `抽屉里应有指向 ${item.to} 的入口`).toBeTruthy()
      const before = (w.emitted('update:modelValue') ?? []).length
      self!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await flushPromises()
      const after = (w.emitted('update:modelValue') ?? []).slice(before)
      expect(after, `在 ${item.to} 上点自己没收起抽屉`).toContainEqual([false])
      w.unmount()
      document.body.innerHTML = ''
      mounted = []
    }
  })
})
