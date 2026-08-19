import { beforeEach, describe, expect, it } from 'vitest'
import { computed, ref } from 'vue'
import { liveStreamPreferencesStorageKey } from './liveStreamPreferences'
import { useSwimLane } from './useSwimLane'
import type { LiveStreamSnapshot } from './liveStreamStore'

function emptySnapshot(): LiveStreamSnapshot {
  return {
    summary: { total: 0, success: 0, failure: 0 },
    detail_dimensions: { vendor: [], provider: [], model: [] },
    dimensions: { vendor: [], provider: [], model: [] },
    dimension_legends: { vendor: [], provider: [], model: [] },
    status_legends: [],
  }
}

describe('useSwimLane preferences', () => {
  beforeEach(() => {
    localStorage.clear()
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
    const saved = JSON.parse(localStorage.getItem(liveStreamPreferencesStorageKey()) || '{}')
    expect(saved.groupBy).toBe('model')
    expect(saved.mode).toBe('small')
  })
})
