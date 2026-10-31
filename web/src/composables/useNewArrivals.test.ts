// useNewArrivals.test.ts —— 「新到达」标记判据（2026-10-28 live 页动效轮）
//
// 钉住的行为：
//  1. 首帧（挂载）不标记任何行 —— 数据初始化不是活动；
//  2. 头部新增的行被标记，且 lifetime 后自动清除；
//  3. 无变化（头对头相同）不产生新标记；
//  4. 大换血（上一帧头部滚出列表）只标头部一段，不把整页标新；
//  5. 空列表 → 再有数据：恢复数据时头部新增被正常标记
//     （断流恢复后的首批请求就是真实活动）。
//
// 挂在宿主组件上测（mount Host）：watch / onBeforeUnmount 需要真实
// 组件实例 —— 用裸 effectScope 跑会让 onBeforeUnmount 发「no active
// instance」警告，等于把生命周期的正确性藏进噪声里。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { useNewArrivals } from './useNewArrivals'

const Host = defineComponent({
  props: {
    ids: { type: Array as () => string[], required: true },
    lifetime: { type: Number, default: 700 },
  },
  setup(props) {
    const { fresh } = useNewArrivals(() => props.ids, props.lifetime)
    return { fresh }
  },
  render: () => null,
})

function harness(initial: string[], lifetimeMs = 700) {
  let wrapper: VueWrapper = mount(Host, { props: { ids: [...initial], lifetime: lifetimeMs } })
  return {
    get fresh(): ReadonlySet<string> {
      return (wrapper.vm as unknown as { fresh: ReadonlySet<string> }).fresh
    },
    async set(next: string[]) {
      await wrapper.setProps({ ids: [...next] })
    },
    unmount() {
      wrapper.unmount()
    },
  }
}

describe('useNewArrivals', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('首帧不标记（挂载时整页都是「新」的 ⇒ 全闪是噪声）', async () => {
    const h = harness(['a', 'b', 'c'])
    expect(h.fresh.size).toBe(0)
    await h.set(['a', 'b'])
    expect(h.fresh.size).toBe(0)
    h.unmount()
  })

  it('头部新增被标记；lifetime 后自动清除', async () => {
    const h = harness(['a', 'b', 'c'])
    await h.set(['n1', 'a', 'b', 'c'])
    expect([...h.fresh]).toEqual(['n1'])
    await vi.advanceTimersByTimeAsync(700)
    expect(h.fresh.size).toBe(0)
    h.unmount()
  })

  it('多条新增全部标记；既有行不动', async () => {
    const h = harness(['a', 'b'])
    await h.set(['n3', 'n2', 'n1', 'a', 'b'])
    expect([...h.fresh]).toEqual(['n3', 'n2', 'n1'])
    h.unmount()
  })

  it('大换血（上一帧头部滚出）只标头部一段', async () => {
    const h = harness(['a', 'b', 'c'])
    const big = Array.from({ length: 60 }, (_, i) => `x${i}`)
    await h.set(big)
    // 头部 40 条封顶标记，其余（x40+）不算新
    expect(h.fresh.size).toBe(40)
    expect(h.fresh.has('x0')).toBe(true)
    expect(h.fresh.has('x39')).toBe(true)
    expect(h.fresh.has('x40')).toBe(false)
    h.unmount()
  })

  it('空列表重置头指针；恢复数据时按大换血兜底标头部（断流恢复是真实活动）', async () => {
    const h = harness(['a', 'b'])
    await h.set([])
    expect(h.fresh.size).toBe(0)
    await h.set(['r1', 'r2', 'a'])
    // 权衡（有意行为）：断流重连后列表是全量重灌，此时按「大换血」兜底
    // 标头部 ≤40 条 —— 断流期间真实积累的新请求有到达反馈，代价是
    // 少量滚回头部的旧 id 被多标一次（轻微噪音，可接受；反过来「恢复
    // 后整页静默无反馈」更糟）。
    expect([...h.fresh]).toEqual(['r1', 'r2', 'a'])
    h.unmount()
  })

  it('标记到期只清自己那批，不清后来批次', async () => {
    const h = harness(['a'], 700)
    await h.set(['n1', 'a'])
    await vi.advanceTimersByTimeAsync(300)
    await h.set(['n2', 'n1', 'a'])
    expect([...h.fresh]).toEqual(['n1', 'n2'])
    await vi.advanceTimersByTimeAsync(450) // n1 满 750ms 到期，n2 只过 450ms
    expect([...h.fresh]).toEqual(['n2'])
    await vi.advanceTimersByTimeAsync(300)
    expect(h.fresh.size).toBe(0)
    h.unmount()
  })

  it('卸载时清掉全部计时器（离开页面后不再有挂起的 setTimeout）', async () => {
    const clearSpy = vi.spyOn(globalThis, 'clearTimeout')
    const h = harness(['a'], 700)
    await h.set(['n1', 'a'])
    h.unmount()
    expect(clearSpy).toHaveBeenCalled()
    clearSpy.mockRestore()
  })

  it('★ 挂起句柄有界：任意多批次最多 1 个清扫定时器，到期后归零', async () => {
    // 2026-10-29 统计同步轮泄漏审计：旧实现每批 push 一个 setTimeout 句柄
    // 且数组只增不减 —— 长驻 /live 页面上万批次的死句柄无限累积（句柄本身
    // 已触发，数组纯属内存泄漏）。清扫式重设计后：单一定时器按最早到期
    // 触发，批次数与挂起句柄数解耦。
    const h = harness(['a'], 700)
    for (let i = 0; i < 50; i += 1) {
      await h.set([`k${i}`, 'a'])
      await vi.advanceTimersByTimeAsync(50) // 每批间隔 50ms < lifetime：上一批还没到期
    }
    expect(vi.getTimerCount()).toBeLessThanOrEqual(1)
    await vi.advanceTimersByTimeAsync(700)
    expect(vi.getTimerCount()).toBe(0)
    expect(h.fresh.size).toBe(0)
    h.unmount()
  })
})
