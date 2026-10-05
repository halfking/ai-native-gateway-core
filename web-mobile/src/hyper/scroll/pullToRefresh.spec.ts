import { describe, expect, it, vi } from 'vitest'
import { PTR_CONFIG, PullToRefreshMachine, isVerticalPull } from './pullToRefresh'

describe('PullToRefreshMachine 状态机（07 §2）', () => {
  it('未达阈值松手 → 回弹不刷新', async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const m = new PullToRefreshMachine({ onRefresh })
    expect(m.begin()).toBe(true)
    m.move(30)
    expect(m.state).toBe('pulling')
    expect(m.release()).toBe(false)
    expect(onRefresh).not.toHaveBeenCalled()
    expect(m.state).toBe('settling')
  })

  it('达 64px 阈值松手 → armed → refreshing 单飞一次', async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const m = new PullToRefreshMachine({ onRefresh })
    m.begin()
    const visual = m.move(PTR_CONFIG.thresholdPx + 10)
    expect(m.state).toBe('armed')
    // 超阈值部分按 0.4 阻尼，且封顶 96px
    expect(visual).toBe(PTR_CONFIG.thresholdPx + 10 * PTR_CONFIG.resistance)
    expect(m.move(1000)).toBe(PTR_CONFIG.maxVisualPx)
    expect(m.release()).toBe(true)
    expect(m.state).toBe('refreshing')
    expect(onRefresh).toHaveBeenCalledTimes(1)
    await vi.waitFor(() => expect(m.state).toBe('idle'))
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('刷新中拒绝新下拉（single-flight）', async () => {
    let release: () => void = () => {}
    const onRefresh = () =>
      new Promise<void>((resolve) => {
        release = () => resolve()
      })
    const m = new PullToRefreshMachine({ onRefresh })
    m.begin()
    m.move(PTR_CONFIG.thresholdPx)
    m.release()
    expect(m.begin()).toBe(false)
    release()
    await vi.waitFor(() => expect(m.state).toBe('idle'))
    expect(m.begin()).toBe(true)
  })

  it('刷新失败 → error 态（调用方保留旧数据提示）', async () => {
    const onRefresh = vi.fn().mockRejectedValue(new Error('boom'))
    const m = new PullToRefreshMachine({ onRefresh })
    m.begin()
    m.move(PTR_CONFIG.thresholdPx)
    m.release()
    await vi.waitFor(() => expect(m.state).toBe('error'))
  })

  it('手势仲裁转移 → abandon 静默归位', () => {
    const m = new PullToRefreshMachine({ onRefresh: vi.fn().mockResolvedValue(undefined) })
    m.begin()
    m.move(40)
    m.abandon()
    expect(m.state).toBe('settling')
  })

  it('方向锁：|dx| > |dy|/1.3 时不认领（07 §7）', () => {
    expect(isVerticalPull(30, 10)).toBe(false)
    expect(isVerticalPull(3, 30)).toBe(true)
    expect(isVerticalPull(5, 4)).toBe(true) // 未过 12px 锁定窗不抢
  })
})
