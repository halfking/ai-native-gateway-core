import { describe, expect, it, vi } from 'vitest'
import { BackDispatcher } from './backDispatcher'
import type { OverlayHandle } from '../types'

function makeOverlay(id: string, opts: Partial<OverlayHandle> = {}): OverlayHandle {
  return {
    id,
    presentation: 'sheet',
    close: opts.close ?? vi.fn(),
    beforeClose: opts.beforeClose,
    title: opts.title,
  }
}

function makeDispatcher(deps: {
  overlays?: OverlayHandle[]
  canPop?: boolean
  pop?: () => void
  fallback?: () => void
}) {
  const dismissed: string[] = []
  const d = new BackDispatcher({
    getOverlays: () => deps.overlays ?? [],
    dismissOverlay: (id) => dismissed.push(id),
    canPopInApp: () => deps.canPop ?? false,
    popInApp: deps.pop ?? (() => {}),
    fallback: deps.fallback ?? (() => {}),
  })
  return { d, dismissed }
}

describe('BackDispatcher 单一仲裁（06 §5 / 11 §3）', () => {
  it('有覆盖层 → 只关最上层', async () => {
    const overlays = [makeOverlay('a'), makeOverlay('b')]
    const dismissed: string[] = []
    const live = new BackDispatcher({
      getOverlays: () => overlays,
      dismissOverlay: (id) => dismissed.push(id),
      canPopInApp: () => true,
      popInApp: () => {},
      fallback: () => {},
    })
    expect(await live.back()).toBe(true)
    expect(dismissed).toEqual(['b'])
  })

  it('beforeClose 拒绝 → 消费返回且不关层、不跳路由', async () => {
    const overlays = [makeOverlay('a', { beforeClose: () => false })]
    const pop = vi.fn()
    const fallback = vi.fn()
    const live = new BackDispatcher({
      getOverlays: () => overlays,
      dismissOverlay: (id) => {
        throw new Error(`不应关闭: ${id}`)
      },
      canPopInApp: () => true,
      popInApp: pop,
      fallback,
    })
    expect(await live.back()).toBe(true)
    expect(pop).not.toHaveBeenCalled()
    expect(fallback).not.toHaveBeenCalled()
  })

  it('beforeClose 可为异步', async () => {
    const overlays = [makeOverlay('a', { beforeClose: async () => true })]
    const dismissed: string[] = []
    const live = new BackDispatcher({
      getOverlays: () => overlays,
      dismissOverlay: (id) => dismissed.push(id),
      canPopInApp: () => false,
      popInApp: () => {},
      fallback: () => {},
    })
    await live.back()
    expect(dismissed).toEqual(['a'])
  })

  it('无覆盖层 + 可 pop → 应用内返回（不直接 fallback）', async () => {
    const pop = vi.fn()
    const fallback = vi.fn()
    const { d } = makeDispatcher({ canPop: true, pop, fallback })
    await d.back()
    expect(pop).toHaveBeenCalledTimes(1)
    expect(fallback).not.toHaveBeenCalled()
  })

  it('根页（无可 pop）→ fallback', async () => {
    const fallback = vi.fn()
    const { d } = makeDispatcher({ canPop: false, fallback })
    await d.back()
    expect(fallback).toHaveBeenCalledTimes(1)
  })

  it('并发第二击被单飞闩锁拒绝（11 §4 快速连按不双 pop）', async () => {
    let releaseBeforeClose: () => void = () => {}
    const overlays = [
      makeOverlay('a', {
        beforeClose: () =>
          new Promise<boolean>((resolve) => {
            releaseBeforeClose = () => resolve(true)
          }),
      }),
    ]
    const pop = vi.fn()
    const live = new BackDispatcher({
      getOverlays: () => overlays,
      dismissOverlay: () => {},
      canPopInApp: () => true,
      popInApp: pop,
      fallback: () => {},
    })
    const first = live.back()
    const second = live.back()
    expect(await second).toBe(false)
    releaseBeforeClose()
    expect(await first).toBe(true)
  })
})
