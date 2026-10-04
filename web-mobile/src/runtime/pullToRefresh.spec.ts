import { describe, expect, it, vi } from 'vitest'
import { PullToRefresh, PTR_MAX_PULL_PX, PTR_TRIGGER_PX } from './pullToRefresh'

describe('pullToRefresh（07 §2 状态机）', () => {
  it('64px 触发 / 12px 方向锁 / 96px 上限 / 松手单发刷新', async () => {
    const onRefresh = vi.fn(async () => {})
    const ptr = new PullToRefresh({ onRefresh })

    // 顶部开始 → 下拉过方向锁 → armed。
    ptr.touchStart(0)
    ptr.touchMove(6, 0)
    expect(ptr.current().state).toBe('pulling')
    ptr.touchMove(40, 0)
    expect(ptr.current().state).toBe('pulling')
    ptr.touchMove(PTR_TRIGGER_PX + 10, 0)
    expect(ptr.current().state).toBe('armed')

    // 松手 → refreshing → 回调恰一次 → 回 idle。
    await ptr.touchEnd()
    expect(onRefresh).toHaveBeenCalledTimes(1)
    expect(ptr.current().state).toBe('idle')
  })

  it('阻尼：位移逼近 96px 上限不越界', () => {
    const ptr = new PullToRefresh({ onRefresh: async () => {} })
    ptr.touchStart(0)
    ptr.touchMove(500, 0)
    expect(ptr.current().pull).toBeLessThanOrEqual(PTR_MAX_PULL_PX)
  })

  it('上滑（dy<0）触发方向锁后放弃', () => {
    const ptr = new PullToRefresh({ onRefresh: async () => {} })
    ptr.touchStart(0)
    ptr.touchMove(-20, 0)
    expect(ptr.current().state).toBe('idle')
    // 锁定后再下拉也不复活（本次触摸序列已放弃）。
    ptr.touchMove(100, 0)
    expect(ptr.current().state).toBe('idle')
  })

  it('非顶部（scrollTop>0）不进入 pulling', () => {
    const ptr = new PullToRefresh({ onRefresh: async () => {} })
    ptr.touchStart(100)
    ptr.touchMove(100, 100)
    expect(ptr.current().state).toBe('idle')
  })

  it('追加加载中互斥（isLoadingMore）', () => {
    const ptr = new PullToRefresh({ onRefresh: async () => {}, isLoadingMore: () => true })
    ptr.touchStart(0)
    ptr.touchMove(100, 0)
    expect(ptr.current().state).toBe('idle')
  })

  it('reduced-motion：位移恒 0、状态机照常', () => {
    const ptr = new PullToRefresh({ onRefresh: async () => {}, reducedMotion: () => true })
    ptr.touchStart(0)
    ptr.touchMove(100, 0)
    expect(ptr.current().state).toBe('armed')
    expect(ptr.current().pull).toBe(0)
  })

  it('未达触发点松手回 idle 不发刷新', async () => {
    const onRefresh = vi.fn(async () => {})
    const ptr = new PullToRefresh({ onRefresh })
    ptr.touchStart(0)
    ptr.touchMove(30, 0)
    await ptr.touchEnd()
    expect(onRefresh).not.toHaveBeenCalled()
    expect(ptr.current().state).toBe('idle')
  })
})
