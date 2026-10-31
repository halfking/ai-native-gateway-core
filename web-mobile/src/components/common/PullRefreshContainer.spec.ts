import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PullRefreshContainer from './PullRefreshContainer.vue'

/**
 * 2026-10-28 回归守卫：总览页「滑下去、滑不回来」。
 *
 * 根因：**到顶判据读错了元素**。总览页的真实滚动宿主是插槽里的
 * `.home__page`（`.page` 自带 `overflow-y:auto`）；外层 `.ptr-container`
 * 因为 `.home` 是 flex 列、被 `.home__page`（flex:1）正好填满 ⇒
 * **它永远不滚、scrollTop 恒为 0**。若下拉刷新仍拿它当判据，页面滚到
 * 任何位置后，**任何向下拖动**都会被认领并 `preventDefault()` ⇒ 往回滑被吃掉。
 *
 * 修法：组件新增 `scroller` prop，让调用方显式指认真实滚动宿主；
 * HomeView 传 `.home__page`。
 *
 * 断言口径（两条都有牙，且合起来正好覆盖修复前后的差别）：
 *   ① 传了 `scroller` 且它不在顶部 ⇒ 向下拖**不**被认领（原生滚动不被吃掉）；
 *   ② 不传 `scroller`（= 修复前的接线）⇒ 判据读容器自身（scrollTop 恒 0）
 *      ⇒ 向下拖**被**认领 ⇒ defaultPrevented=true —— 这正是原来的缺陷形态。
 */
function touchEvent(type: 'touchstart' | 'touchmove', clientY: number): TouchEvent {
  return new TouchEvent(type, {
    touches: [{ clientX: 50, clientY } as Touch],
    bubbles: true,
    cancelable: true,
  })
}

describe('PullRefreshContainer —— 到顶判据必须读真实滚动宿主', () => {
  it('传入 scroller 且它不在顶部：向下拖不被认领 ⇒ 原生滚动不被吃掉', async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const inner = document.createElement('div')
    Object.defineProperty(inner, 'scrollTop', { value: 320, writable: true })

    const wrapper = mount(PullRefreshContainer, {
      props: { onRefresh, scroller: () => inner },
      slots: { default: '<div class="page" />' },
      attachTo: document.body,
    })
    await wrapper.vm.$nextTick()

    const root = wrapper.element as HTMLElement
    root.dispatchEvent(touchEvent('touchstart', 100))
    const ev = touchEvent('touchmove', 160)
    root.dispatchEvent(ev)

    expect(ev.defaultPrevented).toBe(false)
    wrapper.unmount()
  })

  it('不传 scroller（修复前的接线）：判据读容器自身 ⇒ 向下拖被无条件抢走', async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const wrapper = mount(PullRefreshContainer, {
      props: { onRefresh },
      slots: { default: '<div class="page" />' },
      attachTo: document.body,
    })
    await wrapper.vm.$nextTick()

    const root = wrapper.element as HTMLElement
    root.dispatchEvent(touchEvent('touchstart', 100))
    const ev = touchEvent('touchmove', 160)
    root.dispatchEvent(ev)

    // ★ 这就是缺陷形态：容器自身 scrollTop 恒 0 ⇒ 任何向下拖动都被抢走
    expect(ev.defaultPrevented).toBe(true)
    wrapper.unmount()
  })
})
