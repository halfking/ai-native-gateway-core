// AppDrawer.drag.test.ts — 拖拽关闭的**接线层**断言（规范 12 §1 / §2）。
//
// 与 dragDismiss.test.ts 的分工：那份测纯状态机的决策，这份测
// 「DOM 事件有没有正确地喂给它、结果有没有正确地落到 transform 与关闭上」。
// 状态机本身在这里被当作可信依赖，不重复测它的阈值。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppDrawer from './AppDrawer.vue'
import { _resetScrollLockForTests } from '../../composables/useScrollLock'

const bpState = vi.hoisted(() => ({ isTablet: false, reducedMotion: false }))
vi.mock('../../composables/useBreakpoint', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../composables/useBreakpoint')>()
  const { computed, readonly, ref } = await import('vue')
  return {
    ...actual,
    useBreakpoint: () => ({
      isMobile: readonly(ref(!bpState.isTablet)),
      isTablet: readonly(ref(bpState.isTablet)),
      isSmall: readonly(ref(false)),
      isDesktop: computed(() => !bpState.isTablet),
    }),
  }
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { common: { button: { close: '关闭' } } } },
})

/** jsdom 没有 matchMedia；规范 12 §4 要求真的判一次系统设置，不能只看代码。 */
function stubMatchMedia(): void {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (q: string) => ({
      matches: q.includes('prefers-reduced-motion') ? bpState.reducedMotion : false,
      media: q,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }),
  })
}

/** jsdom 的 getBoundingClientRect 全 0，必须给面板一个真实尺寸，否则 panelSize=0。 */
function stubRect(el: Element, rect: Partial<DOMRect>): void {
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0,
    toJSON: () => ({}), ...rect,
  } as DOMRect)
}

function mountDrawer(props: Record<string, unknown> = {}) {
  return mount(AppDrawer, {
    props: { modelValue: true, title: '测试抽屉', ...props },
    slots: { default: '<p>drawer-content</p>' },
    global: { plugins: [i18n], stubs: { Teleport: true } },
    attachTo: document.body,
  })
}

/**
 * ## 为什么样式断言走 `w.html()` 而不是 `el.getAttribute('style')`
 *
 * **jsdom 的 CSSOM 不实现 `transform`。** 实测：同一时刻
 * `wrapper.html()` 里能看到 `style="transform: translateX(40px);"`，而
 * `document.querySelector('.app-drawer__panel').outerHTML` **没有 style 属性**、
 * `el.style.transform` 恒为 `""`、`getAttribute('style')` 恒为 `null`。
 * ⇒ 读 DOM 属性会得到「拖拽完全没生效」的**假结论**，而组件其实是对的。
 * `wrapper.html()` 由 Vue 的样式绑定序列化，不经过 jsdom 的 CSSOM，故可见。
 *
 * ⚠️ 另一条同源陷阱：`AppDrawer` 内容在 `<Teleport to="body">` 里，
 * `wrapper.find()` 在 **mount 之后**取到的才是活节点；mount 之前取到的是
 * 传送前的节点（`isConnected === false`），往它派发事件不会触发处理器。
 * 本文件统一用 `livePanel()` 并在断言前 `await nextTick()`。
 */
/**
 * 必须查**活节点**，不能用 `wrapper.find()` 返回的节点。
 *
 * 实测踩到的坑：`AppDrawer` 的内容在 `<Teleport to="body">` 里，
 * 即便 VTU 用 `stubs: { Teleport: true }`，内容仍被移到 `document.body`。
 * ⇒ `w.find('.app-drawer__panel').element` 拿到的是**传送前**的节点，
 * `isConnected === false`，读它的 `getAttribute('style')` 恒为 `null`，
 * 而同一时刻 `document.querySelector` 读到的是
 * `style="transform: translateX(40px);"`。
 * ⇒ 症状是「拖拽完全没生效」，而组件其实是对的。**先怀疑量具。**
 */
function livePanel(): HTMLElement {
  const el = document.querySelector<HTMLElement>('.app-drawer__panel')
  if (!el) throw new Error('活节点里没有 .app-drawer__panel（Teleport 已把它移到 body）')
  return el
}

function liveGrabArea(root: HTMLElement, selector: string): HTMLElement {
  const el = root.querySelector<HTMLElement>(selector)
  if (!el) throw new Error(`活节点里没有 ${selector}`)
  return el
}

/**
 * 从 `wrapper.html()` 取面板的 class **token 列表**。
 *
 * 两道限制逼出来的写法：
 * 1. 不能读 `el.classList` —— 实测在本 harness 下 jsdom 的 DOM 与 Vue 渲染树
 *    不同步（`style` 与 `class` 都不落到活节点，只有 `wrapper.html()` 能看到）。
 * 2. 不能用 `expect(html).toContain('app-drawer__panel--dragging')` ——
 *    那是**子串**匹配，变异 W6 把类名改成 `--dragging-x` 后仍然命中，判据形同虚设。
 * ⇒ 按 token 切开后用数组 `toContain` 精确比较，两个问题一起解决。
 */
function panelClasses(w: { html: () => string }): string[] {
  const m = w.html().match(/class="app-drawer__panel[^"]*"/)
  if (!m) return []
  return (m[0].match(/[a-z0-9_-]+/g) ?? []).filter((c) => c !== 'class' && c !== 'app-drawer__panel')
}

function pointer(type: string, x: number, y: number, target: Element, timeStamp = 0) {
  const e = new Event(type, { bubbles: true, cancelable: true }) as PointerEvent
  // ⚠️ timeStamp 是 Event 上的**只读 getter**，Object.assign 设不进去（实测
  // TypeError: Cannot set property timeStamp）。必须 defineProperty。
  Object.assign(e, { clientX: x, clientY: y, pointerId: 1, pointerType: 'touch', button: 0 })
  Object.defineProperty(e, 'timeStamp', { configurable: true, value: timeStamp })
  target.dispatchEvent(e)
  return e
}

describe('AppDrawer 拖拽关闭（接线层，规范 12 §1/§2）', () => {
  beforeEach(() => {
    bpState.isTablet = false
    bpState.reducedMotion = false
    stubMatchMedia()
    _resetScrollLockForTests()
  })
  afterEach(() => {
    document.body.innerHTML = ''
    _resetScrollLockForTests()
    vi.restoreAllMocks()
  })

  it('draggable=false 时 pointerdown 不产生任何位移', async () => {
    const w = mountDrawer({ draggable: false, direction: 'bottom' })
    await w.vm.$nextTick()
    const panel = livePanel()
    const grip = liveGrabArea(panel, '.app-drawer__grip')
    stubRect(panel, { width: 400, height: 400, top: 200 })
    pointer('pointerdown', 200, 240, grip)
    pointer('pointermove', 200, 320, panel, 16)
    await w.vm.$nextTick()
    expect(w.html()).not.toMatch(/translate/)
    w.unmount()
  })

  it('§1 仲裁：bottom sheet 从正文起手不接管（把滚动让给内容）', async () => {
    const w = mountDrawer({ direction: 'bottom' })
    await w.vm.$nextTick()
    const panel = livePanel()
    const body = liveGrabArea(panel, '.app-drawer__body')
    stubRect(panel, { width: 400, height: 400, top: 200 })
    pointer('pointerdown', 200, 300, body)
    pointer('pointermove', 200, 380, panel, 16)
    await w.vm.$nextTick()
    expect(w.html()).not.toMatch(/translate/)
    w.unmount()
  })

  it('bottom sheet 从把手起手：跟手位移落到 translateY', async () => {
    const w = mountDrawer({ direction: 'bottom' })
    await w.vm.$nextTick()
    const panel = livePanel()
    const grip = liveGrabArea(panel, '.app-drawer__grip')
    stubRect(panel, { width: 400, height: 400, top: 200 })
    pointer('pointerdown', 200, 210, grip)
    pointer('pointermove', 200, 250, panel, 16)
    await w.vm.$nextTick()
    expect(w.html()).toMatch(/translateY\(40px\)/)
    // 跟手阶段不得有 transition（否则不跟手）。按 token 精确比较，见 panelClasses 头注。
    expect(panelClasses(w)).toContain('app-drawer__panel--dragging')
    w.unmount()
  })

  it('右侧面板：位移落到 translateX，且关闭方向由实测位置决定（面板在右半屏 ⇒ +x）', async () => {
    bpState.isTablet = true
    const w = mountDrawer()
    await w.vm.$nextTick()
    const panel = livePanel()
    stubRect(panel, { width: 400, height: 600, left: 800, right: 1200 })
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(1200)
    pointer('pointerdown', 1000, 300, panel)
    pointer('pointermove', 1040, 300, panel, 16)
    await w.vm.$nextTick()
    expect(w.html()).toMatch(/translateX\(40px\)/)
    w.unmount()
  })

  it('右侧面板在左半屏（RTL）时关闭方向取 -x：向左拖才关闭', async () => {
    bpState.isTablet = true
    const w = mountDrawer()
    await w.vm.$nextTick()
    const panel = livePanel()
    stubRect(panel, { width: 400, height: 600, left: 0, right: 400 })
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(1200)
    // 向右拖 200px：位置够阈值，但方向反了 ⇒ 不应关闭
    pointer('pointerdown', 200, 300, panel)
    for (let i = 1; i <= 10; i++) pointer('pointermove', 200 + i * 20, 300, panel, i * 16)
    pointer('pointerup', 400, 300, panel, 200)
    await w.vm.$nextTick()
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('中断即复位：pointercancel 把位移清零且不关闭', async () => {
    const w = mountDrawer({ direction: 'bottom' })
    await w.vm.$nextTick()
    const panel = livePanel()
    const grip = liveGrabArea(panel, '.app-drawer__grip')
    stubRect(panel, { width: 400, height: 400, top: 200 })
    pointer('pointerdown', 200, 210, grip)
    pointer('pointermove', 200, 350, panel, 16)
    await w.vm.$nextTick()
    expect(w.html()).toMatch(/translateY\(140px\)/)
    pointer('pointercancel', 200, 350, panel)
    await w.vm.$nextTick()
    expect(w.html()).not.toMatch(/translateY\(140px\)/)
    expect(w.emitted('update:modelValue')).toBeUndefined()
    w.unmount()
  })

  it('§4 减少动态效果：直接落终态关闭，不等 transitionend', async () => {
    bpState.reducedMotion = true
    const w = mountDrawer({ direction: 'bottom' })
    await w.vm.$nextTick()
    const panel = livePanel()
    const grip = liveGrabArea(panel, '.app-drawer__grip')
    stubRect(panel, { width: 400, height: 400, top: 200 })
    pointer('pointerdown', 200, 210, grip)
    for (let i = 1; i <= 10; i++) pointer('pointermove', 200, 210 + i * 20, panel, i * 16)
    pointer('pointerup', 200, 410, panel, 200)
    await w.vm.$nextTick()
    // 关闭必须**已经发生**，而不是等一个不会来的 transitionend
    expect(w.emitted('update:modelValue')).toEqual([[false]])
    w.unmount()
  })

  it('关闭后重开不得带着上次的位移出场', async () => {
    const w = mountDrawer({ direction: 'bottom' })
    await w.vm.$nextTick()
    const panel = livePanel()
    const grip = liveGrabArea(panel, '.app-drawer__grip')
    stubRect(panel, { width: 400, height: 400, top: 200 })
    pointer('pointerdown', 200, 210, grip)
    pointer('pointermove', 200, 350, panel, 16)
    await w.vm.$nextTick()
    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true })
    await w.vm.$nextTick()
    expect(w.html()).not.toMatch(/translateY\(140px\)/)
    w.unmount()
  })
})
