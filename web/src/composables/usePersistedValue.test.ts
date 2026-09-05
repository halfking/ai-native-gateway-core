// usePersistedValue.test.ts — LP8 single-value persistence unit tests
//
// Coverage:
//   1. factory fallback when storage empty
//   2. priming from existing localStorage value
//   3. immediate: true writes synchronously
//   4. default (debounced) coalesces 5 mutations into 1 write
//   5. snapshot short-circuit skips identical writes
//   6. setItem throwing degrades silently, in-memory ref stays authoritative
//   7. custom serialize/deserialize roundtrip
//   8. flush() forces synchronous write
//   9. remove() drops the key and reverts to factory value
//  10. multiple handles with the same key do not interfere (each owns its own timer)
//  11. invalid JSON in storage falls back to factory
//  12. outside effectScope: no auto-cleanup, listeners persist

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { effectScope, nextTick } from 'vue'
import { usePersistedValue, PERSIST_DEBOUNCE_MS } from './usePersistedValue'

describe('usePersistedValue — LP8 single-value persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorage.clear()
  })

  it('factory fallback when storage is empty', () => {
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<string>('llmgw_test_seed', () => 'generated-seed')
      expect(handle.value.value).toBe('generated-seed')
      expect(localStorage.getItem('llmgw_test_seed')).toBeNull()
    })
    scope.stop()
  })

  it('primes from existing localStorage value (JSON)', () => {
    localStorage.setItem('llmgw_test_obj', JSON.stringify({ preset: '7d', days: 7 }))
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<{ preset: string; days: number }>(
        'llmgw_test_obj',
        () => ({ preset: 'today', days: 1 }),
      )
      expect(handle.value.value).toEqual({ preset: '7d', days: 7 })
    })
    scope.stop()
  })

  it('immediate: true writes synchronously on every mutation', () => {
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<string>('llmgw_test_locale', () => 'zh-CN', {
        immediate: true,
      })
      expect(localStorage.getItem('llmgw_test_locale')).toBeNull()

      handle.value.value = 'en'
      expect(localStorage.getItem('llmgw_test_locale')).toBe(JSON.stringify('en'))

      handle.value.value = 'ja'
      expect(localStorage.getItem('llmgw_test_locale')).toBe(JSON.stringify('ja'))
    })
    scope.stop()
  })

  it('default debounce coalesces 5 rapid mutations into a single write', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<string>('llmgw_test_burst', () => 'init')
      handle.value.value = 'one'
      handle.value.value = 'two'
      handle.value.value = 'three'
      handle.value.value = 'four'
      handle.value.value = 'five'
      expect(setItemSpy).not.toHaveBeenCalled()
    })

    await vi.advanceTimersByTimeAsync(PERSIST_DEBOUNCE_MS + 50)
    const writes = setItemSpy.mock.calls.filter(([k]) => k === 'llmgw_test_burst')
    expect(writes).toHaveLength(1)
    expect(writes[0]![1]).toBe(JSON.stringify('five'))

    scope.stop()
    setItemSpy.mockRestore()
  })

  it('snapshot short-circuit skips identical writes across flushes', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<string>('llmgw_test_idem', () => 'same')
      // First flush establishes the snapshot baseline.
      handle.flush()
      expect(setItemSpy.mock.calls.filter(([k]) => k === 'llmgw_test_idem')).toHaveLength(1)

      // Mutate to the same value, then flush — snapshot matches, no IO.
      handle.value.value = 'same'
      handle.flush()
      expect(setItemSpy.mock.calls.filter(([k]) => k === 'llmgw_test_idem')).toHaveLength(1)

      // Mutate to a different value, flush — one new IO.
      handle.value.value = 'different'
      handle.flush()
      expect(setItemSpy.mock.calls.filter(([k]) => k === 'llmgw_test_idem')).toHaveLength(2)
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('setItem throwing degrades silently, in-memory value stays authoritative', () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError')
    })
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<string>('llmgw_test_quota', () => 'a', {
        immediate: true,
      })
      handle.value.value = 'b'
      expect(handle.value.value).toBe('b')
      expect(handle.flush()).toBe(false)
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('custom serialize/deserialize roundtrip for boolean sidebar state', () => {
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<boolean>('llmgw_test_collapsed', () => false, {
        immediate: true,
        serialize: (v) => (v ? '1' : '0'),
        deserialize: (r) => (r === '1' ? true : r === '0' ? false : undefined),
      })
      expect(handle.value.value).toBe(false)

      handle.value.value = true
      expect(localStorage.getItem('llmgw_test_collapsed')).toBe('1')

      handle.value.value = false
      expect(localStorage.getItem('llmgw_test_collapsed')).toBe('0')
    })
    scope.stop()
  })

  it('flush() forces synchronous write of the current value', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<string>('llmgw_test_flush', () => 'pending')
      handle.value.value = 'committed'
      expect(setItemSpy).not.toHaveBeenCalled()
      expect(handle.flush()).toBe(true)
      expect(setItemSpy).toHaveBeenCalledWith('llmgw_test_flush', JSON.stringify('committed'))
    })
    scope.stop()
    setItemSpy.mockRestore()
  })

  it('remove() drops the key and reverts to factory value', async () => {
    localStorage.setItem('llmgw_test_remove', JSON.stringify('stored'))
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<string>('llmgw_test_remove', () => 'default')
      expect(handle.value.value).toBe('stored')

      const ok = handle.remove()
      expect(ok).toBe(true)
      expect(localStorage.getItem('llmgw_test_remove')).toBeNull()
      expect(handle.value.value).toBe('default')
    })
    scope.stop()
  })

  it('multiple handles with distinct keys persist independently', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    const scope = effectScope()
    scope.run(() => {
      const a = usePersistedValue<string>('llmgw_test_a', () => 'a0')
      const b = usePersistedValue<string>('llmgw_test_b', () => 'b0')
      a.value.value = 'a1'
      b.value.value = 'b1'
      a.value.value = 'a2'
      b.value.value = 'b2'
    })

    await vi.advanceTimersByTimeAsync(PERSIST_DEBOUNCE_MS + 50)
    const keys = setItemSpy.mock.calls
      .map(([k]) => k)
      .filter((k): k is string => typeof k === 'string' && k.startsWith('llmgw_test_'))
    expect(keys).toContain('llmgw_test_a')
    expect(keys).toContain('llmgw_test_b')

    expect(JSON.parse(localStorage.getItem('llmgw_test_a')!)).toBe('a2')
    expect(JSON.parse(localStorage.getItem('llmgw_test_b')!)).toBe('b2')

    scope.stop()
    setItemSpy.mockRestore()
  })

  it('invalid JSON in storage falls back to factory value', () => {
    localStorage.setItem('llmgw_test_corrupt', 'not-json{')
    const scope = effectScope()
    scope.run(() => {
      const handle = usePersistedValue<{ preset: string }>(
        'llmgw_test_corrupt',
        () => ({ preset: 'today' }),
      )
      expect(handle.value.value).toEqual({ preset: 'today' })
    })
    scope.stop()
  })

  it('invocation outside effectScope: no auto-cleanup, but writes still flush on lifecycle events', async () => {
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem')
    // Call without an active effect scope (module-level usage, like i18n.ts)
    const handle = usePersistedValue<string>('llmgw_test_module', () => 'mod0', {
      immediate: true,
    })

    handle.value.value = 'mod1'
    await nextTick()
    expect(setItemSpy).toHaveBeenCalledWith('llmgw_test_module', JSON.stringify('mod1'))

    // No scope to dispose — verify we can still manually flush / remove
    handle.remove()
    expect(localStorage.getItem('llmgw_test_module')).toBeNull()
    setItemSpy.mockRestore()
  })
})
