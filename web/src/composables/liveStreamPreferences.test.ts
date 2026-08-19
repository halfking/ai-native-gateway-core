import { beforeEach, describe, expect, it } from 'vitest'
import {
  defaultLiveStreamPreferences,
  liveStreamPreferencesStorageKey,
  readLiveStreamPreferences,
  writeLiveStreamPreferences,
} from './liveStreamPreferences'
import { store } from '../store'

describe('liveStreamPreferences', () => {
  beforeEach(() => {
    localStorage.clear()
    store.userInfo = null
  })

  it('returns defaults when storage is empty or malformed', () => {
    expect(readLiveStreamPreferences()).toEqual(defaultLiveStreamPreferences())

    localStorage.setItem(liveStreamPreferencesStorageKey(), '{bad json')
    expect(readLiveStreamPreferences()).toEqual(defaultLiveStreamPreferences())
  })

  it('normalizes invalid fields while preserving valid selections', () => {
    localStorage.setItem(liveStreamPreferencesStorageKey(), JSON.stringify({
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

  it('migrates the legacy swimlane mode only without a valid v1 mode', () => {
    localStorage.setItem('llmgw_swimlane_mode', 'large')
    expect(readLiveStreamPreferences().mode).toBe('large')

    localStorage.setItem(liveStreamPreferencesStorageKey(), JSON.stringify({
      ...defaultLiveStreamPreferences(),
      mode: 'small',
    }))
    expect(readLiveStreamPreferences().mode).toBe('small')
  })

  it('isolates preferences by the active user and tenant scope', () => {
    store.userInfo = { id: 7, tenant_id: 'tenant-a', username: 'a', display_name: 'A', email: '', role: 'tenant_admin', enabled: true }
    writeLiveStreamPreferences({ filters: { providers: ['provider-a'] } })
    const tenantAKey = liveStreamPreferencesStorageKey()

    store.userInfo = { id: 8, tenant_id: 'tenant-b', username: 'b', display_name: 'B', email: '', role: 'tenant_admin', enabled: true }
    expect(readLiveStreamPreferences().filters.providers).toEqual([])
    writeLiveStreamPreferences({ filters: { agents: ['zcode'] } })
    const tenantBKey = liveStreamPreferencesStorageKey()

    expect(tenantAKey).not.toBe(tenantBKey)
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
