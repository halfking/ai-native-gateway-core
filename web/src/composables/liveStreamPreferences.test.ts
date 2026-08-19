import { beforeEach, describe, expect, it } from 'vitest'
import {
  defaultLiveStreamPreferences,
  LIVE_STREAM_PREFERENCES_STORAGE_KEY,
  readLiveStreamPreferences,
  writeLiveStreamPreferences,
} from './liveStreamPreferences'

describe('liveStreamPreferences', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('returns defaults when storage is empty or malformed', () => {
    expect(readLiveStreamPreferences()).toEqual(defaultLiveStreamPreferences())

    localStorage.setItem(LIVE_STREAM_PREFERENCES_STORAGE_KEY, '{bad json')
    expect(readLiveStreamPreferences()).toEqual(defaultLiveStreamPreferences())
  })

  it('normalizes invalid fields while preserving valid selections', () => {
    localStorage.setItem(LIVE_STREAM_PREFERENCES_STORAGE_KEY, JSON.stringify({
      version: 99,
      groupBy: 'not-a-dimension',
      mode: 'large',
      filters: {
        requestTypes: ['probe', 'invalid'],
        agents: [' ZCODE ', 12],
        statuses: ['success', 12],
      },
      queue: {
        depthOpen: true,
        statusFilter: { active: false, exhausted: 'yes' },
      },
    }))

    expect(readLiveStreamPreferences()).toMatchObject({
      groupBy: 'queue',
      mode: 'large',
      filters: {
        requestTypes: ['probe'],
        agents: ['zcode'],
        statuses: ['success'],
      },
      queue: {
        depthOpen: true,
        statusFilter: { active: false, exhausted: true },
      },
    })
  })

  it('merges partial updates without losing other dashboard selections', () => {
    writeLiveStreamPreferences({
      groupBy: 'model',
      filters: { providers: ['openai'] },
      queue: { statusFilter: { exhausted: false } },
    })
    writeLiveStreamPreferences({ mode: 'large', queue: { depthOpen: true } })

    expect(readLiveStreamPreferences()).toMatchObject({
      groupBy: 'model',
      mode: 'large',
      filters: { providers: ['openai'] },
      queue: {
        depthOpen: true,
        statusFilter: { exhausted: false },
      },
    })
  })

  it('migrates the legacy swimlane mode key', () => {
    localStorage.setItem('llmgw_swimlane_mode', 'large')
    expect(readLiveStreamPreferences().mode).toBe('large')

    writeLiveStreamPreferences({ groupBy: 'vendor' })
    expect(localStorage.getItem('llmgw_swimlane_mode')).toBe('large')
  })
})
