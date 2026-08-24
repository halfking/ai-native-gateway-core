// persistenceShared.test.ts — LP9 共享 debounce + lifecycle flush primitives
//
// Covers the helper contract:
//   1. createDebouncedFlush: schedule coalesces bursts into a single write
//   2. createDebouncedFlush: flush() returns true on success, false on
//      serialisation failure / write failure
//   3. createDebouncedFlush: snapshot short-circuit (idempotent writes
//      skip redundant IO even when burst mutates converged state)
//   4. createDebouncedFlush: cancel() clears the timer AND the snapshot
//      cache (test isolation)
//   5. createDebouncedFlush: getSnapshot returning null skips the write
//   6. installLifecycleFlush: visibilitychange (hidden) flushes, visible
//      does not
//   7. installLifecycleFlush: pagehide / beforeunload always flush
//   8. installScopeCleanup: scoped disposal flushes + removes listeners

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import {
  createDebouncedFlush,
  installLifecycleFlush,
  installScopeCleanup,
  PERSIST_DEBOUNCE_MS,
} from './persistenceShared'

describe('persistenceShared — PERSIST_DEBOUNCE_MS constant', () => {
  it('exposes 300ms as the shared coalescing window', () => {
    expect(PERSIST_DEBOUNCE_MS).toBe(300)
  })
})

describe('createDebouncedFlush — coalescing + short-circuit', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('schedules a debounced write that lands PERSIST_DEBOUNCE_MS after the last call', async () => {
    let snapshot = 'init'
    const writes: string[] = []
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => snapshot,
      serialize: (v) => v,
      write: (v) => {
        writes.push(v)
        return true
      },
    })

    handle.schedule()
    snapshot = 'a'
    handle.schedule()
    snapshot = 'b'
    handle.schedule()
    snapshot = 'c'
    handle.schedule()
    snapshot = 'final'

    // 5 rapid schedules coalesce into 1 write, after the debounce window.
    expect(writes).toEqual([])
    await vi.advanceTimersByTimeAsync(PERSIST_DEBOUNCE_MS + 50)
    expect(writes).toEqual(['final'])
  })

  it('flush() writes synchronously and cancels any pending timer', () => {
    let snapshot = 'init'
    const writes: string[] = []
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => snapshot,
      serialize: (v) => v,
      write: (v) => { writes.push(v); return true },
    })

    handle.schedule()
    const ok = handle.flush()
    expect(ok).toBe(true)
    expect(writes).toEqual(['init'])

    // Second flush without a new schedule — short-circuit fires because
    // lastPersistedSnapshot now matches.
    const ok2 = handle.flush()
    expect(ok2).toBe(true)
    expect(writes).toEqual(['init'])
  })

  it('returns false when serialise() throws — in-memory state stays authoritative', () => {
    let snapshot: { id: number } | null = { id: 1 }
    let writeCalled = false
    const handle = createDebouncedFlush<{ id: number }>({
      getSnapshot: () => snapshot,
      serialize: () => { throw new Error('circular ref') },
      write: () => { writeCalled = true; return true },
    })

    expect(handle.flush()).toBe(false)
    expect(writeCalled).toBe(false)
  })

  it('returns false when write() returns false — caller owns failure semantics', () => {
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => 'value',
      serialize: (v) => v,
      write: () => false,
    })
    expect(handle.flush()).toBe(false)
  })

  it('catches exceptions thrown by write() and returns false', () => {
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => 'value',
      serialize: (v) => v,
      write: () => { throw new Error('storage blew up') },
    })
    expect(handle.flush()).toBe(false)
  })

  it('short-circuits when the serialised snapshot matches the last persisted shape', () => {
    let snapshot = 'unchanged'
    let writeCount = 0
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => snapshot,
      serialize: (v) => v,
      write: () => { writeCount++; return true },
    })

    handle.flush() // First write — short-circuit cache empty.
    expect(writeCount).toBe(1)

    // Second flush with the same value — short-circuit must skip the call.
    handle.flush()
    expect(writeCount).toBe(1)

    // Mutate to a new value, flush — write must happen again.
    snapshot = 'changed'
    handle.flush()
    expect(writeCount).toBe(2)
  })

  it('cancel() clears the pending timer AND the snapshot short-circuit cache', async () => {
    let snapshot = 'first'
    let writeCount = 0
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => snapshot,
      serialize: (v) => v,
      write: () => { writeCount++; return true },
    })

    handle.flush()
    expect(writeCount).toBe(1)
    handle.cancel()
    await vi.advanceTimersByTimeAsync(PERSIST_DEBOUNCE_MS + 50)

    // After cancel() the short-circuit cache is empty — a fresh flush
    // must commit, even though the snapshot shape hasn't changed.
    handle.flush()
    expect(writeCount).toBe(2)
  })

  it('skips the write when getSnapshot() returns null (no staged state)', () => {
    let snapshot: string | null = null
    const writes: string[] = []
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => snapshot,
      serialize: (v) => v,
      write: (v) => { writes.push(v); return true },
    })
    expect(handle.flush()).toBe(true)
    expect(writes).toEqual([])

    snapshot = 'now-staged'
    expect(handle.flush()).toBe(true)
    expect(writes).toEqual(['now-staged'])
  })

  it('immediate mode writes synchronously on every schedule()', () => {
    let snapshot = 'a'
    const writes: string[] = []
    const handle = createDebouncedFlush<string>({
      getSnapshot: () => snapshot,
      serialize: (v) => v,
      write: (v) => { writes.push(v); return true },
      immediate: true,
    })

    handle.schedule()
    handle.schedule()
    handle.schedule()
    expect(writes).toEqual(['a'])

    snapshot = 'b'
    handle.schedule()
    expect(writes).toEqual(['a', 'b'])
  })
})

describe('installLifecycleFlush — browser listeners', () => {
  afterEach(() => {
    // Sanity: no leftover listeners from failed test runs.
    // (Each test installs its own; the cleanup is exercised by the dispose
    // test below.)
  })

  it('flushes on pagehide and beforeunload (always, regardless of visibility)', () => {
    let flushes = 0
    const flush = () => { flushes++ }
    const remove = installLifecycleFlush(flush)

    window.dispatchEvent(new Event('pagehide'))
    window.dispatchEvent(new Event('beforeunload'))
    expect(flushes).toBe(2)

    remove()
  })

  it('flushes on visibilitychange when visibilityState transitions to hidden', () => {
    let flushes = 0
    const flush = () => { flushes++ }
    const remove = installLifecycleFlush(flush)

    const originalDescriptor = Object.getOwnPropertyDescriptor(document, 'visibilityState')
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    try {
      window.dispatchEvent(new Event('visibilitychange'))
      expect(flushes).toBe(1)
    } finally {
      if (originalDescriptor) {
        Object.defineProperty(document, 'visibilityState', originalDescriptor)
      } else {
        delete (document as { visibilityState?: unknown }).visibilityState
      }
      remove()
    }
  })

  it('does NOT flush on visibilitychange when the page is still visible', () => {
    let flushes = 0
    const flush = () => { flushes++ }
    const remove = installLifecycleFlush(flush)

    window.dispatchEvent(new Event('visibilitychange'))
    expect(flushes).toBe(0)

    remove()
  })

  it('remove() detaches all three listeners', () => {
    let flushes = 0
    const flush = () => { flushes++ }
    const remove = installLifecycleFlush(flush)

    remove()
    window.dispatchEvent(new Event('pagehide'))
    window.dispatchEvent(new Event('beforeunload'))
    window.dispatchEvent(new Event('visibilitychange'))
    expect(flushes).toBe(0)
  })
})

describe('installScopeCleanup — Vue effect scope teardown', () => {
  it('flushes and removes listeners when the surrounding effectScope stops', () => {
    let flushes = 0
    const flush = () => { flushes++ }
    const remove = installLifecycleFlush(flush)
    const scope = effectScope()

    scope.run(() => {
      installScopeCleanup(flush, remove)
    })

    // Outside the scope: lifecycle events still flush (listener still attached).
    window.dispatchEvent(new Event('pagehide'))
    expect(flushes).toBe(1)

    // Stop the scope — dispose hook fires: flush (count → 2) + remove listeners.
    scope.stop()
    expect(flushes).toBe(2)

    // After dispose: further events must NOT trigger flush (listeners gone).
    window.dispatchEvent(new Event('beforeunload'))
    window.dispatchEvent(new Event('pagehide'))
    expect(flushes).toBe(2) // unchanged
  })

  it('is a no-op when called outside any active effect scope (module init)', () => {
    let flushes = 0
    const flush = () => { flushes++ }
    const remove = installLifecycleFlush(flush)

    // Must not throw, must not register a dispose hook.
    expect(() => installScopeCleanup(flush, remove)).not.toThrow()

    // Listeners still attached — lifecycle events still flush.
    window.dispatchEvent(new Event('pagehide'))
    expect(flushes).toBe(1)

    remove()
  })
})
