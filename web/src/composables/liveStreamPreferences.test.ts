// liveStreamPreferences.test.ts — dashboard 偏好持久化 + LP7 debounce 测试
//
// LP7 (2026-08-24): 复用 LP6 useChatSessions pattern —
//   300ms debounce + snapshot short-circuit + lifecycle flush + cleanup。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  _resetPersistState,
  defaultLiveStreamPreferences,
  flushPersist,
  liveStreamPreferencesStorageKey,
  readLiveStreamPreferences,
  writeLiveStreamPreferences,
} from './liveStreamPreferences'
import { store } from '../store'

/** Filter Storage.setItem calls down to the live-stream preference keys.
 *  Typed loosely to avoid coupling to vitest's MockInstance overload. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function liveStreamKeyCalls(spy: any): number {
  return spy.mock.calls.filter(
    ([k]: [unknown]) => typeof k === 'string'
      && (k.startsWith('llmgw_live_stream_preferences_v1') || k === 'llmgw_swimlane_mode'),
  ).length
}

function setUser(id: number, tenant: string) {
  store.userInfo = {
    id, tenant_id: tenant, username: `u${id}`, display_name: `U${id}`,
    email: '', role: 'tenant_admin', enabled: true,
  }
}

describe('liveStreamPreferences — synchronous behavior', () => {
  beforeEach(() => {
    localStorage.clear()
    store.userInfo = null
    _resetPersistState()
    vi.useRealTimers()
  })

  it('returns normalized defaults for first-time users', () => {
    expect(readLiveStreamPreferences()).toEqual(defaultLiveStreamPreferences())
  })

  it('migrates legacy mode-only value into the scoped v1 storage', () => {
    setUser(10, 'tenant-x')
    localStorage.setItem('llmgw_swimlane_mode', 'large')
    const read = readLiveStreamPreferences()
    expect(read.mode).toBe('large')
    expect(localStorage.getItem(liveStreamPreferencesStorageKey())).not.toBeNull()
  })

  it('ignores invalid mode strings instead of corrupting persisted preferences', () => {
    setUser(11, 'tenant-y')
    localStorage.setItem(liveStreamPreferencesStorageKey(), JSON.stringify({ mode: 'broken' }))
    const read = readLiveStreamPreferences()
    expect(read.mode).toBe('small')
  })

  it('isolates preferences by the active user and tenant scope', () => {
    setUser(7, 'tenant-a')
    writeLiveStreamPreferences({ filters: { providers: ['provider-a'] } })
    const tenantAKey = liveStreamPreferencesStorageKey()

    setUser(8, 'tenant-b')
    expect(readLiveStreamPreferences().filters.providers).toEqual([])
    writeLiveStreamPreferences({ filters: { agents: ['zcode'] } })
    const tenantBKey = liveStreamPreferencesStorageKey()

    expect(tenantAKey).not.toBe(tenantBKey)
    // Explicit flush before final assertions — the second writeLiveStreamPreferences
    // for tenantB is debounced and would not yet be persisted.
    flushPersist()
    expect(JSON.parse(localStorage.getItem(tenantAKey) || '{}').filters.providers).toEqual(['provider-a'])
    expect(JSON.parse(localStorage.getItem(tenantBKey) || '{}').filters.agents).toEqual(['zcode'])
  })

  it('merges independent field writes without stale sibling filters', () => {
    writeLiveStreamPreferences({ filters: { providers: ['openai'] } })
    writeLiveStreamPreferences({ filters: { agents: ['zcode'] } })
    writeLiveStreamPreferences({ queue: { statusFilter: { active: false } } })
    writeLiveStreamPreferences({ queue: { statusFilter: { exhausted: false } } })

    expect(readLiveStreamPreferences()).toMatchObject({
      filters: { providers: ['openai'], agents: ['zcode'] },
      queue: { statusFilter: { active: false, exhausted: false } },
    })
  })
})

describe('liveStreamPreferences — LP7 debounced persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    store.userInfo = null
    _resetPersistState()
    vi.useFakeTimers()
  })

  it('debounces 5 rapid mutations into a single localStorage write', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    setUser(1, 't1')

    writeLiveStreamPreferences({ groupBy: 'vendor' })
    writeLiveStreamPreferences({ groupBy: 'provider' })
    writeLiveStreamPreferences({ groupBy: 'model' })
    writeLiveStreamPreferences({ mode: 'large' })
    writeLiveStreamPreferences({ filters: { providers: ['openai'] } })

    // 立即断言：debounce 窗口未到，未写入
    expect(liveStreamKeyCalls(setItemSpy)).toBe(0)

    await vi.advanceTimersByTimeAsync(350)

    // 多次 mutate 合并为单次（包含主 key + legacy swimlane mode）
    expect(liveStreamKeyCalls(setItemSpy)).toBe(2)

    setItemSpy.mockRestore()
  })

  it('flushPersist() writes synchronously even before the debounce timer fires', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    setUser(2, 't2')

    writeLiveStreamPreferences({ mode: 'large' })

    // debounce 尚未到 → flush 立即触发写入
    const ok = flushPersist()
    expect(ok).toBe(true)

    // 主 key + legacy swimlane mode 都已落盘
    expect(liveStreamKeyCalls(setItemSpy)).toBe(2)
    setItemSpy.mockRestore()
  })

  it('survives localStorage.setItem throwing: in-memory state intact, no crash', () => {
    const setItemSpy = vi
      .spyOn(Storage.prototype, 'setItem')
      .mockImplementation(() => { throw new Error('QuotaExceededError') })
    setUser(3, 't3')

    // 不应抛
    expect(() => writeLiveStreamPreferences({ mode: 'large' })).not.toThrow()
    expect(() => writeLiveStreamPreferences({ filters: { providers: ['openai'] } })).not.toThrow()

    // in-memory state 保留（writeLiveStreamPreferences 返回 normalized tree）
    const mem = readLiveStreamPreferences()
    expect(mem.mode).toBe('large')
    expect(mem.filters.providers).toEqual(['openai'])

    setItemSpy.mockRestore()
  })

  it('short-circuits when snapshot is unchanged (idempotent writes skip redundant IO)', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    setUser(4, 't4')

    writeLiveStreamPreferences({ mode: 'large' })
    flushPersist()
    expect(liveStreamKeyCalls(setItemSpy)).toBe(2)

    // 同样的内容再写一次 → snapshot 一致 → short-circuit
    writeLiveStreamPreferences({ mode: 'large' })
    flushPersist()
    expect(liveStreamKeyCalls(setItemSpy)).toBe(2)

    setItemSpy.mockRestore()
  })

  it('beforeunload listener flushes pending writes synchronously', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    setUser(5, 't5')

    writeLiveStreamPreferences({ mode: 'large' })
    // 模拟浏览器 beforeunload
    window.dispatchEvent(new Event('beforeunload'))

    // 立即触发写入，不需等 debounce
    expect(liveStreamKeyCalls(setItemSpy)).toBeGreaterThanOrEqual(2)
    setItemSpy.mockRestore()
  })

  it('pagehide listener flushes pending writes', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    setUser(6, 't6')

    writeLiveStreamPreferences({ mode: 'large' })
    window.dispatchEvent(new Event('pagehide'))

    expect(liveStreamKeyCalls(setItemSpy)).toBeGreaterThanOrEqual(2)
    setItemSpy.mockRestore()
  })

  it('visibilitychange (hidden) flushes pending writes', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    setUser(7, 't7')

    // Override document.visibilityState getter on the instance (jsdom 默认 visible)
    const originalDescriptor = Object.getOwnPropertyDescriptor(document, 'visibilityState')
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })

    try {
      writeLiveStreamPreferences({ mode: 'large' })
      // jsdom 的 visibilitychange 不会从 document bubble 到 window，listener 注册在 window 上
      window.dispatchEvent(new Event('visibilitychange'))

      expect(liveStreamKeyCalls(setItemSpy)).toBeGreaterThanOrEqual(2)
    } finally {
      if (originalDescriptor) {
        Object.defineProperty(document, 'visibilityState', originalDescriptor)
      } else {
        delete (document as { visibilityState?: unknown }).visibilityState
      }
      setItemSpy.mockRestore()
    }
  })

  it('multiple writes within debounce window keep timer alive (no premature flush)', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    setUser(8, 't8')

    writeLiveStreamPreferences({ groupBy: 'vendor' })
    await vi.advanceTimersByTimeAsync(200) // 中段，未到 300ms
    expect(liveStreamKeyCalls(setItemSpy)).toBe(0)

    writeLiveStreamPreferences({ groupBy: 'provider' })
    await vi.advanceTimersByTimeAsync(200) // 又 200ms，但因 timer reset 应仍未到
    expect(liveStreamKeyCalls(setItemSpy)).toBe(0)

    // 等到首个 write 之后 300ms → 应 flush（合并了第二个）
    await vi.advanceTimersByTimeAsync(100)
    expect(liveStreamKeyCalls(setItemSpy)).toBe(2)

    setItemSpy.mockRestore()
  })
})
