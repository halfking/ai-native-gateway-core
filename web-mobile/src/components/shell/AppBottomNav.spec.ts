import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import AppBottomNav from './AppBottomNav.vue'
import { BOTTOM_NAV } from '@/config/appNav'

/**
 * 底栏导航契约（12h-audit 20261017 订正 454740b35）。
 *
 * ★ 454740b35 在 safeNavigate 里**抢先** `e.preventDefault()` 再调
 *   `navigate(e)`。vue-router 5.2.0 的 navigate 第一件事是 guardEvent(e)，
 *   而 guardEvent 见 `defaultPrevented === true` 直接 return
 *   （vue-router.cjs:2113）⇒ `router.push` 永不执行、底栏点击全灭。
 *   旧顺序下本文件的「路由必须变化」断言是红的。
 *
 * 判据钉三件事：
 * ① plain tap 后路由**真的**变化（push 执行）；
 * ② 原生 `<a>` 默认导航被阻止（guardEvent 自己 preventDefault）；
 * ③ 「更多」按钮仍走 onMore。
 */

let mounted: Array<{ unmount(): void }> = []

async function mountNav(startPath = '/') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div/>' } },
      ...BOTTOM_NAV.map((n) => ({ path: n.to, component: { template: '<div/>' } })),
    ],
  })
  await router.push(startPath)
  await router.isReady()
  const w = mount(AppBottomNav, {
    props: { onMore: vi.fn() },
    attachTo: document.body,
    global: { plugins: [router], stubs: { AppIcon: true } },
  })
  mounted.push(w)
  return Object.assign(w, { __router: router })
}

describe('AppBottomNav tap→push 契约', () => {
  beforeEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
    document.body.innerHTML = ''
  })

  it('plain tap：路由变化（push 执行）且原生导航被 guardEvent 阻止', async () => {
    const w = await mountNav('/')
    const targetTo = BOTTOM_NAV.find((n) => n.to !== '/')?.to
    expect(targetTo).toBeTruthy()
    const idx = BOTTOM_NAV.findIndex((n) => n.to === targetTo)
    const link = w.findAll('a.bottomnav__item')[idx]
    if (!link) throw new Error(`BOTTOM_NAV[${idx}] (${targetTo}) 未渲染出 <a>`)

    const ev = new MouseEvent('click', { bubbles: true, cancelable: true })
    ;(link.element as HTMLAnchorElement).dispatchEvent(ev)
    await flushPromises()
    await flushPromises()

    // ② guardEvent 决定 push 时自己 preventDefault —— 原生导航不会跑。
    expect(ev.defaultPrevented).toBe(true)
    // ① 承重断言：旧代码（抢先 preventDefault）在这里红 —— push 永不执行。
    expect(w.__router.currentRoute.value.path).toBe(targetTo)
    w.unmount()
  })

  it('「更多」按钮点击走 onMore', async () => {
    const onMore = vi.fn()
    const router = createRouter({
      history: createMemoryHistory(),
      routes: BOTTOM_NAV.map((n) => ({ path: n.to, component: { template: '<div/>' } })),
    })
    await router.push('/')
    await router.isReady()
    const w = mount(AppBottomNav, {
      props: { onMore },
      attachTo: document.body,
      global: { plugins: [router], stubs: { AppIcon: true } },
    })
    mounted.push(w)
    await w.find('button.bottomnav__item').trigger('click')
    expect(onMore).toHaveBeenCalledTimes(1)
    w.unmount()
  })
})
