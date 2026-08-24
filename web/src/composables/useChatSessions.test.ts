// useChatSessions.test.ts — LP6 全树 localStorage 防抖写入单测
//
// 覆盖：
//   1. 连续 mutate 触发单次写入（debounce coalesces）
//   2. flushPersist() 立即同步落盘
//   3. setItem 抛错时不崩，内存状态保留
//   4. 同一 snapshot short-circuit 跳过重复写
//   5. beforeunload / pagehide 在卸载时强制 flush
//   6. visibilitychange (hidden) 强制 flush
//   7. onScopeDispose 清理监听器 + 最终 flush

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { effectScope } from 'vue'
import { useChatSessions } from './useChatSessions'

/** Filter Storage.setItem / removeItem spy calls down to the main v2 chat key.
 *  Typed loosely to avoid coupling the helper to vitest's MockInstance
 *  overload (which differs between 1.x minor versions). */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function mainKeyCalls(spy: any): number {
  return spy.mock.calls.filter(
    ([k]: [unknown]) => typeof k === 'string' && k.startsWith('llmgw_chat_v2'),
  ).length
}

describe('useChatSessions — LP6 debounced persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorage.clear()
  })

  it('debounces 5 rapid mutations into a single localStorage write', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const api = useChatSessions()
      api.updateActive({ model: 'gpt-4' })
      api.updateActive({ model: 'gpt-4o' })
      api.updateActive({ model: 'gpt-4-turbo' })
      api.updateActive({ model: 'claude-sonnet' })
      api.updateActive({ model: 'auto' })

      // 立即断言：尚未到 debounce 窗口，未写入
      expect(mainKeyCalls(setItemSpy)).toBe(0)
    })

    // 推进到 debounce 窗口之外
    await vi.advanceTimersByTimeAsync(350)

    // 只应该有一次 mainKey 写入（legacy cleanup 是 removeItem，单独算）
    expect(mainKeyCalls(setItemSpy)).toBe(1)

    scope.stop()
    setItemSpy.mockRestore()
  })

  it('flushPersist() writes synchronously even before the debounce timer fires', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const api = useChatSessions()
      api.updateActive({ model: 'gpt-4o' })

      // debounce 尚未到 → flush 立即触发写入
      const ok = api.flushPersist()
      expect(ok).toBe(true)

      expect(mainKeyCalls(setItemSpy)).toBe(1)
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('survives localStorage.setItem throwing: in-memory state intact, no crash', () => {
    const setItemSpy = vi
      .spyOn(Storage.prototype, 'setItem')
      .mockImplementation(() => {
        throw new Error('QuotaExceededError')
      })

    const scope = effectScope()
    let apiRef: ReturnType<typeof useChatSessions> | null = null
    scope.run(() => {
      apiRef = useChatSessions()
      // 不应抛
      expect(() => apiRef!.createSession('auto')).not.toThrow()
      expect(() => apiRef!.updateActive({ model: 'gpt-4o' })).not.toThrow()
    })

    // 内存中 session 仍在
    expect(apiRef!.sessions.value.length).toBeGreaterThan(0)
    expect(apiRef!.sessions.value[0]!.model).toBe('gpt-4o')

    scope.stop()
    setItemSpy.mockRestore()
  })

  it('short-circuits when snapshot is unchanged (idempotent writes skip redundant IO)', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const api = useChatSessions()
      // 首次写入
      api.updateActive({ model: 'auto' })
      api.flushPersist()
      expect(mainKeyCalls(setItemSpy)).toBe(1)

      // 再写一次同样的内容 → snapshot 一致 → short-circuit
      api.updateActive({ model: 'auto' })
      api.flushPersist()
      expect(mainKeyCalls(setItemSpy)).toBe(1) // 没增加
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('beforeunload listener flushes pending writes synchronously', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const api = useChatSessions()
      api.updateActive({ model: 'gpt-4o' })

      // 模拟浏览器 beforeunload
      window.dispatchEvent(new Event('beforeunload'))

      // 立即触发写入，不需等 debounce
      expect(mainKeyCalls(setItemSpy)).toBeGreaterThanOrEqual(1)
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('pagehide listener flushes pending writes', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const api = useChatSessions()
      api.updateActive({ model: 'claude-sonnet' })

      window.dispatchEvent(new Event('pagehide'))

      expect(mainKeyCalls(setItemSpy)).toBeGreaterThanOrEqual(1)
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('visibilitychange (hidden) flushes pending writes', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    // Override document.visibilityState getter on the instance (shadows the
    // prototype getter; jsdom defines it as configurable on Document.prototype).
    const originalDescriptor = Object.getOwnPropertyDescriptor(document, 'visibilityState')
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => 'hidden',
    })

    try {
      const scope = effectScope()
      scope.run(() => {
        const api = useChatSessions()
        api.updateActive({ model: 'claude-sonnet' })

        // 注意：jsdom 的 visibilitychange 不会从 document bubble 到 window，
        // useChatSessions 注册在 window 上，因此必须从 window 派发。
        window.dispatchEvent(new Event('visibilitychange'))

        expect(mainKeyCalls(setItemSpy)).toBeGreaterThanOrEqual(1)
      })
      scope.stop()
    } finally {
      if (originalDescriptor) {
        Object.defineProperty(document, 'visibilityState', originalDescriptor)
      } else {
        delete (document as { visibilityState?: unknown }).visibilityState
      }
      setItemSpy.mockRestore()
    }
  })

  it('visibilitychange (visible) does NOT flush — idle tab should not thrash storage', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')

    const scope = effectScope()
    scope.run(() => {
      const api = useChatSessions()
      api.updateActive({ model: 'auto' })

      window.dispatchEvent(new Event('visibilitychange'))
      // visible 时不 flush → 0 次 main key 写入
      expect(mainKeyCalls(setItemSpy)).toBe(0)
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('onScopeDispose stops debounce + removes listeners', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')

    const scope = effectScope()
    scope.run(() => {
      useChatSessions()
    })
    scope.stop()

    // scope.stop 已触发最终 flush，spy 计数应 ≥1
    expect(mainKeyCalls(setItemSpy)).toBeGreaterThanOrEqual(1)
    const countAfterStop = mainKeyCalls(setItemSpy)

    // 卸载后再触发事件 → 不应再有新写入
    window.dispatchEvent(new Event('beforeunload'))
    window.dispatchEvent(new Event('pagehide'))
    expect(mainKeyCalls(setItemSpy)).toBe(countAfterStop)

    setItemSpy.mockRestore()
  })
})
