import { beforeEach, describe, expect, it } from 'vitest'
import { computed, ref } from 'vue'
import { _resetPersistState, flushPersist, liveStreamPreferencesStorageKey } from './liveStreamPreferences'
import { useSwimLane } from './useSwimLane'
import type { LiveStreamSnapshot } from './liveStreamStore'

function emptySnapshot(): LiveStreamSnapshot {
  return {
    summary: { total: 0, success: 0, failure: 0 },
    detail_dimensions: { credential: [], vendor: [], provider: [], model: [] },
    dimensions: { credential: [], vendor: [], provider: [], model: [] },
    dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
    status_legends: [],
  }
}

describe('useSwimLane preferences', () => {
  beforeEach(() => {
    localStorage.clear()
    _resetPersistState()
  })

  it('restores group and mode, then persists later selections', () => {
    localStorage.setItem(liveStreamPreferencesStorageKey(), JSON.stringify({
      version: 1,
      groupBy: 'vendor',
      mode: 'large',
      filters: {},
      queue: {},
    }))

    const state = useSwimLane(computed(() => ref(emptySnapshot()).value))
    expect(state.groupBy.value).toBe('vendor')
    expect(state.mode.value).toBe('large')

    state.setGroupBy('model')
    state.setMode('small')
    flushPersist() // 2026-08-24 LP7: writes 已是 debounced
    const saved = JSON.parse(localStorage.getItem(liveStreamPreferencesStorageKey()) || '{}')
    expect(saved.groupBy).toBe('model')
    expect(saved.mode).toBe('small')
  })
})
