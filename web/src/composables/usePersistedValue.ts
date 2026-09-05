// usePersistedValue — single-value localStorage persistence composable (LP8, 2026-08-24)
//
// Why
// ───
// The frontend had N scattered `localStorage.setItem/getItem` sites
// (useChatCompletions / useDashboardBoard / liveStreamStore / useLiveStreamUrl /
// appNav / i18n). Each one:
//   • wrote synchronously on every mutation
//   • had no shared flush policy (visibilitychange / pagehide / beforeunload)
//   • swallowed errors with bare try/catch and no central visibility
//
// This composable consolidates the single-value path with the same primitives
// LP6 wired into useChatSessions:
//   • 300ms debounce coalescing burst mutations (PERSIST_DEBOUNCE_MS)
//   • lifecycle flush: visibilitychange(hidden) / pagehide / beforeunload
//   • onScopeDispose for setup()-bound callers; falls back to page-lifetime
//     listeners when invoked outside an effect scope (e.g. i18n.ts module init)
//   • snapshot short-circuit (converged bursts skip repeat writes)
//   • try/catch error degradation: in-memory value stays authoritative,
//     next flush retries
//
// LP9 (2026-08-24): the debounce + lifecycle + cleanup primitives now live
// in `persistenceShared.ts` so useChatSessions / liveStreamPreferences can
// reuse them too. This file keeps the same external API (Ref<T> + flush /
// remove handles) — only the internals delegate to the shared helper.
//
// Returns a Ref<T> + flush()/remove() handle. Writes are scheduled through
// the debounced path; callers needing immediate persistence can either
// pass `immediate: true` (every mutation writes synchronously) or call
// `flush()` themselves (e.g. on a Save button).

import { getCurrentScope, ref, watch, type Ref } from 'vue'
import {
  createDebouncedFlush,
  installLifecycleFlush,
  installScopeCleanup,
  PERSIST_DEBOUNCE_MS,
} from './persistenceShared'

export { PERSIST_DEBOUNCE_MS }

export interface PersistedValueOptions<T> {
  /**
   * Serialise the in-memory value to the string localStorage will hold.
   * Default: JSON.stringify. Pass a custom function for booleans / numeric
   * preferences that need a compact encoding (e.g. `'0' | '1'`).
   */
  serialize?: (value: T) => string
  /**
   * Parse the localStorage payload back into T. Throw or return a sentinel
   * (undefined) to fall back to `factory()` when the stored value is corrupt
   * or the wrong shape.
   */
  deserialize?: (raw: string) => T | undefined
  /**
   * When true, every mutation writes synchronously (bypasses debounce).
   * Use for low-frequency single writes (sidebar collapse, locale switch,
   * endpoint URL) where there is no burst to coalesce.
   */
  immediate?: boolean
  /** Override the default 300ms debounce. */
  debounceMs?: number
}

export interface PersistedValueHandle<T> {
  /** Reactive in-memory value, primed from localStorage or factory(). */
  value: Ref<T>
  /** Force a synchronous write of the current value. Returns true on success. */
  flush: () => boolean
  /** Drop the key from localStorage and remember the factory default. */
  remove: () => boolean
}

function defaultSerialize<T>(value: T): string {
  return JSON.stringify(value)
}

function defaultDeserialize<T>(raw: string): T | undefined {
  try {
    return JSON.parse(raw) as T
  } catch {
    return undefined
  }
}

/**
 * Read a single-value localStorage key with a factory fallback.
 * Centralises the try/catch around disabled storage / private-mode browsers.
 */
function readInitial<T>(
  key: string,
  factory: () => T,
  deserialize: (raw: string) => T | undefined,
): T {
  if (typeof localStorage === 'undefined') return factory()
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return factory()
    const parsed = deserialize(raw)
    return parsed === undefined ? factory() : parsed
  } catch {
    return factory()
  }
}

function tryRemove(key: string): boolean {
  if (typeof localStorage === 'undefined') return false
  try {
    localStorage.removeItem(key)
    return true
  } catch {
    return false
  }
}

/**
 * Single-value localStorage persistence with debounced writes, lifecycle
 * flush, and Vue-scope-aware cleanup.
 *
 * @example
 *   const seed = usePersistedValue<string>(DEVICE_SEED_KEY, () => crypto.randomUUID())
 *   const locale = usePersistedValue<string>(
 *     'llmgw_locale',
 *     () => 'zh-CN',
 *     { immediate: true },
 *   )
 *   const collapsed = usePersistedValue<boolean>(
 *     SIDEBAR_COLLAPSED_KEY,
 *     () => false,
 *     {
 *       immediate: true,
 *       serialize: (v) => (v ? '1' : '0'),
 *       deserialize: (r) => (r === '1' ? true : r === '0' ? false : undefined),
 *     },
 *   )
 */
export function usePersistedValue<T>(
  key: string,
  factory: () => T,
  opts: PersistedValueOptions<T> = {},
): PersistedValueHandle<T> {
  const serialize = opts.serialize ?? defaultSerialize<T>
  const deserialize = opts.deserialize ?? defaultDeserialize<T>
  const immediate = opts.immediate === true
  const debounceMs = opts.debounceMs ?? PERSIST_DEBOUNCE_MS

  const initial = readInitial(key, factory, deserialize)
  const valueRef = ref<T>(initial) as Ref<T>

  // Build the debounce + snapshot-short-circuit pipeline on top of the
  // shared helper. The write callback owns its own try/catch so quota /
  // private-mode failures degrade silently and the in-memory ref stays
  // authoritative (next flush retries).
  const persist = createDebouncedFlush<T>({
    getSnapshot: () => valueRef.value,
    serialize,
    write: (snapshot) => {
      if (typeof localStorage === 'undefined') return false
      try {
        localStorage.setItem(key, serialize(snapshot))
        return true
      } catch {
        // Quota exceeded / private mode / disabled storage — degrade silently;
        // the in-memory ref remains authoritative and the next mutation retries.
        return false
      }
    },
    debounceMs,
    immediate,
  })

  function flush(): boolean {
    return persist.flush()
  }

  function remove(): boolean {
    persist.cancel()
    valueRef.value = factory()
    return tryRemove(key)
  }

  // Auto-persist on every value change. Snapshot short-circuit inside
  // `flush` means converging bursts (e.g. repeated identical writes) skip
  // the localStorage.setItem call entirely. `flush: 'sync'` mirrors the
  // synchronous schedulePersist used by useChatSessions (LP6) so a burst
  // of mutations lands in the timer queue immediately rather than waiting
  // for the next microtask.
  watch(valueRef, () => {
    persist.schedule()
  }, { flush: 'sync' })

  // Lifecycle handlers + scope-aware cleanup delegate to the shared helper.
  // installScopeCleanup is a no-op outside an active effect scope (e.g.
  // i18n.ts module init), so module-level callers get page-lifetime
  // listeners without the dev-only "no active scope" warning.
  const removeListeners = installLifecycleFlush(flush)
  installScopeCleanup(flush, removeListeners)

  return {
    value: valueRef,
    flush,
    remove,
  }
}
