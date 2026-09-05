// persistenceShared.ts — shared debounce + lifecycle flush primitives (LP9, 2026-08-24)
//
// Why
// ───
// LP6 (useChatSessions), LP7 (liveStreamPreferences), and LP8
// (usePersistedValue) all independently reimplemented the same persistence
// primitives — debounce timer + snapshot short-circuit + 3 lifecycle
// listeners + scope-aware cleanup. The implementations diverged in trivial
// ways (different listener names, slightly different short-circuit shapes,
// different immediate-mode behaviour) which made the surface area harder
// to audit.
//
// This file is the single source of truth for those primitives:
//   • PERSIST_DEBOUNCE_MS — shared 300ms coalescing window
//   • installLifecycleFlush — 3 browser listeners (visibilitychange,
//     pagehide, beforeunload) with a cleanup function for scope teardown
//   • installScopeCleanup — Vue onScopeDispose wrapper that flushes once
//     and tears down listeners; a no-op outside an active effect scope
//   • createDebouncedFlush — debounce timer + snapshot short-circuit
//     that callers compose with their own getSnapshot / write / serialize
//
// API contract
// ────────────
// • All three exports are SSR-safe (no-ops when `window`/`localStorage` are
//   undefined).
// • Error handling is delegated to the caller's `write` callback: the helper
//   does not catch exceptions thrown by `write` (the caller owns error
//   degradation), but it does catch exceptions thrown by `serialize` and
//   treats them as a soft flush failure (returns false from flush()).
// • snapshot short-circuit is enabled by default — callers that need
//   forced writes can pass `disableSnapshotShortCircuit: true`.
//
// Consumers (LP9 migration targets):
//   • web/src/composables/usePersistedValue.ts (LP8)
//   • web/src/composables/useChatSessions.ts (LP6)
//   • web/src/composables/liveStreamPreferences.ts (LP7)

import { getCurrentScope, onScopeDispose } from 'vue'

/** Shared debounce window. Kept in sync across LP6 / LP7 / LP8. */
export const PERSIST_DEBOUNCE_MS = 300

/** Events that should trigger a synchronous flush of pending writes. */
export const LIFECYCLE_FLUSH_EVENTS = [
  'visibilitychange',
  'pagehide',
  'beforeunload',
] as const

/**
 * A callback that synchronously writes the in-memory state to localStorage.
 * Returns true on success, false on quota/private-mode/disabled-storage
 * failure. The caller owns the actual try/catch around its write logic —
 * this helper does not wrap exceptions thrown inside the callback.
 *
 * Implementations that only care about side effects (e.g. a counter bump
 * in a unit test) may return void; the helper treats void the same as
 * true. Callers that need to signal failure must return boolean.
 */
export type WriteCallback = () => boolean | void

/**
 * Install browser lifecycle listeners that force a flush on tab hide /
 * background / unload. Returns a cleanup function that removes all three
 * listeners. Safe to call in SSR (no-op when `window` is undefined).
 */
export function installLifecycleFlush(flush: WriteCallback): () => void {
  if (typeof window === 'undefined') return () => {}

  function handleVisibilityFlush() {
    if (
      typeof document !== 'undefined'
      && document.visibilityState === 'hidden'
    ) {
      flush()
    }
  }
  function handlePageHide() {
    flush()
  }
  function handleBeforeUnload() {
    flush()
  }

  window.addEventListener('visibilitychange', handleVisibilityFlush)
  window.addEventListener('pagehide', handlePageHide)
  window.addEventListener('beforeunload', handleBeforeUnload)

  return () => {
    window.removeEventListener('visibilitychange', handleVisibilityFlush)
    window.removeEventListener('pagehide', handlePageHide)
    window.removeEventListener('beforeunload', handleBeforeUnload)
  }
}

/**
 * Register a Vue effect-scope cleanup that flushes pending writes and tears
 * down listeners. No-op when called outside an active effect scope (e.g.
 * module-level init) — callers in that context should let the listeners
 * live until page unload, which is also their actual lifetime.
 *
 * Pass an already-installed `flush` and the cleanup function returned by
 * installLifecycleFlush. Both are invoked from the dispose hook.
 */
export function installScopeCleanup(
  flush: WriteCallback,
  removeListeners: () => void,
): void {
  if (typeof window === 'undefined') return
  if (!getCurrentScope()) return
  onScopeDispose(() => {
    flush()
    removeListeners()
  })
}

/**
 * Returned by `createDebouncedFlush`. The `flush` method returns true when
 * the caller-visible state of the underlying write was committed (or when
 * the snapshot matched the last persisted shape and no IO was needed),
 * false when serialisation or write failed.
 */
export interface DebouncedFlushHandle {
  /** Synchronously write pending state. Cancels any pending timer. */
  flush: () => boolean
  /** Schedule a debounced flush. Coalesces bursts into a single write. */
  schedule: () => void
  /**
   * Cancel any pending timer without writing, and clear the snapshot
   * short-circuit cache so the next write is forced through. Useful for
   * test isolation when module-level state is shared across suites.
   */
  cancel: () => void
}

export interface CreateDebouncedFlushOptions<T> {
  /**
   * Read the current snapshot to write. Return `null` to skip this flush
   * entirely (no write, no short-circuit update). Useful for module-level
   * state where a flush may be requested before any value has been staged.
   */
  getSnapshot: () => T | null
  /**
   * Serialise the snapshot for short-circuit comparison. Throw (or return
   * an unparseable value) to abort the flush and return false.
   */
  serialize: (snapshot: T) => string
  /**
   * Persist the snapshot. Should return true on success, false on failure
   * (quota / private mode). Exceptions thrown here are NOT caught by the
   * helper — callers that need swallowed errors must catch them inside
   * their own write implementation.
   */
  write: (snapshot: T) => boolean
  /** Override the default 300ms debounce window. */
  debounceMs?: number
  /**
   * When true, every `schedule()` call writes synchronously (bypasses the
   * debounce timer). Use for low-frequency single writes — sidebar
   * collapse, locale toggle, endpoint URL edit — where there is no
   * burst to coalesce.
   */
  immediate?: boolean
}

/**
 * Create a debounced flush helper with snapshot short-circuit. The returned
 * handle owns one timer + one lastSnapshot string; callers must invoke
 * `schedule()` whenever they mutate the in-memory state and `flush()` when
 * they need a guaranteed-on-return persistence (e.g. test cleanup, explicit
 * Save button).
 */
export function createDebouncedFlush<T>(
  opts: CreateDebouncedFlushOptions<T>,
): DebouncedFlushHandle {
  const debounceMs = opts.debounceMs ?? PERSIST_DEBOUNCE_MS
  const immediate = opts.immediate === true

  let persistTimer: ReturnType<typeof setTimeout> | null = null
  let lastPersistedSnapshot: string | null = null

  function flush(): boolean {
    if (persistTimer != null) {
      clearTimeout(persistTimer)
      persistTimer = null
    }
    const snapshot = opts.getSnapshot()
    if (snapshot === null) return true

    let serialized: string
    try {
      serialized = opts.serialize(snapshot)
    } catch {
      // Serialisation failure (circular ref, BigInt, etc.) — keep the last
      // persisted value; the next valid mutation will retry.
      return false
    }
    if (serialized === lastPersistedSnapshot) return true

    let ok: boolean
    try {
      const result = opts.write(snapshot)
      ok = result === undefined ? true : result
    } catch {
      // Caller didn't catch — treat as a write failure so callers don't
      // need to defensively wrap their write callbacks. In-memory state
      // stays authoritative; the next flush retries.
      return false
    }
    if (ok) lastPersistedSnapshot = serialized
    return ok
  }

  function schedule(): void {
    if (immediate) {
      flush()
      return
    }
    if (persistTimer != null) clearTimeout(persistTimer)
    persistTimer = setTimeout(() => {
      persistTimer = null
      flush()
    }, debounceMs)
  }

  function cancel(): void {
    if (persistTimer != null) {
      clearTimeout(persistTimer)
      persistTimer = null
    }
    // Also clear the short-circuit cache: test isolation for module-level
    // callers (e.g. liveStreamPreferences) needs `_resetPersistState()` to
    // restore a clean "no prior write" state between suites. Without this
    // a cached snapshot from a previous test would suppress the next write
    // even though localStorage was cleared by `localStorage.clear()`.
    lastPersistedSnapshot = null
  }

  return { flush, schedule, cancel }
}
