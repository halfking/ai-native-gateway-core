import { describe, expect, it, vi } from 'vitest'
import { usePullToRefreshGesture } from './usePullToRefreshGesture'

/**
 * 2026-10-28 回归守卫：总览页「滑下去、滑不回来」。
 *
 * 缺陷机理（真机 vivo2411DRN47C + 静态审计双向证实）：
 *   · 总览页的**真实滚动宿主**是插槽里的 `.home__page`（`.page` 自带
 *     `overflow-y:auto`），外层 `.ptr-container` 因为 `.home` 是 flex 列
 *     而被 `.home__page`（flex:1）正好填满 ⇒ **它永远不滚、scrollTop 恒为 0**；
 *   · 而下拉刷新的「到顶」判据读的就是 `getScroller()` 返回的那个元素
 *     （`scroller.scrollTop <= 2`）。
 *   ⇒ 页面滚到任何位置之后，**任何向下拖动**都会被下拉刷新认领并
 *     `preventDefault()` ⇒ 往回滑被吃掉 ⇒ 只能滑下去、滑不回来。
 *
 * 修法：把「到顶判据」指向真正的滚动宿主（PullRefreshContainer 新增
 * `scroller` prop，HomeView 传 `.home__page`）。
 *
 * 断言口径（两条都有牙）：
 *   ① 真实宿主不在顶部（scrollTop > 2）⇒ 向下拖**不得**被认领
 *      （不 preventDefault ⇒ 原生滚动不被吃掉）；
 *   ② 真实宿主在顶部（scrollTop ≤ 2）⇒ 向下拖**必须**被认领（保留下拉刷新）。
 */
function fakeScroller(scrollTop: number): HTMLElement {
  const el = document.createElement('div')
  Object.defineProperty(el, 'scrollTop', { value: scrollTop, writable: true })
  return el
}

function touchAt(clientY: number): TouchEvent {
  const touch = { clientX: 50, clientY } as Touch
  return new TouchEvent('touchmove', {
    touches: [touch],
    bubbles: true,
    cancelable: true,
  })
}

describe('usePullToRefreshGesture —— 到顶判据必须读真正的滚动宿主', () => {
  it('宿主不在顶部：向下拖不被认领 ⇒ 不 preventDefault（原生滚动不被吃掉）', () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const scroller = fakeScroller(320)
    const gesture = usePullToRefreshGesture({ onRefresh, getScroller: () => scroller })
    const el = document.createElement('div')
    gesture.bind(el)

    el.dispatchEvent(new TouchEvent('touchstart', {
      touches: [{ clientX: 50, clientY: 100 } as Touch],
      bubbles: true,
      cancelable: true,
    }))
    const ev = touchAt(160)
    el.dispatchEvent(ev)

    // ★ 不认领 ⇒ 不 preventDefault ⇒ 浏览器的原生滚动继续 ⇒ 能滑回来
    expect(ev.defaultPrevented).toBe(false)
    expect(gesture.machine.state).not.toBe('pulling')
    expect(gesture.machine.state).not.toBe('armed')
  })

  it('宿主在顶部：向下拖被认领 ⇒ 保留下拉刷新', () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const scroller = fakeScroller(0)
    const gesture = usePullToRefreshGesture({ onRefresh, getScroller: () => scroller })
    const el = document.createElement('div')
    gesture.bind(el)

    el.dispatchEvent(new TouchEvent('touchstart', {
      touches: [{ clientX: 50, clientY: 100 } as Touch],
      bubbles: true,
      cancelable: true,
    }))
    const ev = touchAt(160)
    el.dispatchEvent(ev)

    expect(ev.defaultPrevented).toBe(true)
    expect(gesture.machine.state).toBe('pulling')
  })

  it('认领后松手达阈值 ⇒ 触发一次刷新', async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const scroller = fakeScroller(0)
    const gesture = usePullToRefreshGesture({ onRefresh, getScroller: () => scroller })
    const el = document.createElement('div')
    gesture.bind(el)

    el.dispatchEvent(new TouchEvent('touchstart', {
      touches: [{ clientX: 50, clientY: 100 } as Touch],
      bubbles: true,
      cancelable: true,
    }))
    el.dispatchEvent(touchAt(200))
    el.dispatchEvent(new TouchEvent('touchend', { bubbles: true, cancelable: true }))

    await vi.waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1))
  })
})
