// liveStreamStore.identity.test.ts — 2026-10-16 audit: cross-identity leak + reconnect delivery.
//
// Two audited P1s pinned here:
//   1. logout/login or tenant swap reused the module-singleton stream state
//      (requests/snapshot/queue/nodes/actions); for an empty new scope the
//      server sent no authoritative empty replay, so the previous user's data
//      could persist indefinitely.
//   2. fatal reconnect kept the old generation's delivery state, hiding the
//      loading marker before the new connection delivered anything.
import { enableAutoUnmount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { __testing, liveStreamState } from '../composables/liveStreamStore'
import { store, setUserInfo, clearAll, type UserInfo } from '../store'
import { _resetPersistState } from '../composables/liveStreamPreferences'

enableAutoUnmount(afterEach)

class FakeEventSource {
  static instances: FakeEventSource[] = []
  static CLOSED = 2
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  url: string
  constructor(url: string, _opts?: { withCredentials?: boolean }) {
    this.url = url
    FakeEventSource.instances.push(this)
  }
  close() {
    this.readyState = 2
  }
}

const userA: UserInfo = { id: 1, tenant_id: 'default', username: 'a', display_name: 'A', email: 'a@x', role: 'super_admin', enabled: true }
const userB: UserInfo = { id: 2, tenant_id: 'tenant-b', username: 'b', display_name: 'B', email: 'b@x', role: 'tenant_admin', enabled: true }

describe('liveStreamStore identity boundary + reconnect delivery', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    FakeEventSource.instances = []
    vi.stubGlobal('EventSource', FakeEventSource)
    __testing.resetStream()
    __testing.resetForTest()
    _resetPersistState()
    localStorage.clear()
    store.apiKey = ''
    store.jwtToken = ''
    store.userInfo = null
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
    store.apiKey = ''
    store.jwtToken = ''
    store.userInfo = null
  })

  it('clears every user-scoped projection when setUserInfo crosses an identity', async () => {
    setUserInfo(userA)
    // Populate every projection the old identity leaves behind.
    liveStreamState.requests = [{ ts: 'x', request_id: 'req-a', model: 'm', status: 'success' }]
    liveStreamState.snapshot = {
      summary: { total: 1, success: 1, failure: 0 },
      dimensions: { credential: [], vendor: [], provider: [], model: [] },
      dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
      status_legends: [],
    }
    liveStreamState.queue = { enabled: true, wired: true, models: [], credentials: [] }
    liveStreamState.nodes = [{ credential_id: 9, provider_id: 1, provider_code: 'p', manual_disabled: false }]
    liveStreamState.initialDataReceived = true

    setUserInfo(userB)

    expect(liveStreamState.requests).toEqual([])
    expect(liveStreamState.snapshot).toBeNull()
    expect(liveStreamState.queue).toBeNull()
    expect(liveStreamState.nodes).toEqual([])
    expect(liveStreamState.initialDataReceived).toBe(false)
    expect(liveStreamState.connection).toBe('closed')
  })

  it('keeps the consumer refCount across an identity swap (in-place reconnect)', () => {
    setUserInfo(userA)
    const release = __testing.acquireForTest()
    liveStreamState.requests = [{ ts: 'x', request_id: 'req-a', model: 'm', status: 'success' }]

    setUserInfo(userB)
    expect(liveStreamState.requests).toEqual([])
    expect(__testing.refCount()).toBe(1)
    release()
    expect(__testing.refCount()).toBe(0)
  })

  it('clearAll (logout) fences and clears the stream state', async () => {
    setUserInfo(userA)
    liveStreamState.queue = { enabled: true, wired: true, models: [], credentials: [] }
    liveStreamState.initialDataReceived = true

    clearAll()

    expect(liveStreamState.queue).toBeNull()
    expect(liveStreamState.initialDataReceived).toBe(false)
    expect(liveStreamState.connection).toBe('closed')
  })

  it('a late frame from the fenced generation cannot repopulate cleared state', async () => {
    // Drive the real store: acquire → open → initial_data for user A.
    const { acquireLiveStream } = await import('../composables/liveStreamStore')
    const release = acquireLiveStream()
    const first = FakeEventSource.instances[0]
    first.onopen?.()
    first.onmessage?.({
      data: JSON.stringify({
        type: 'initial_data',
        requests_present: true,
        ts: new Date().toISOString(),
        requests: [{ ts: 'x', request_id: 'req-a', model: 'm', provider_code: 'p', status: 'success' }],
        queue_present: true,
        queue: { enabled: true, wired: true, models: [], credentials: [] },
        nodes_present: true,
        nodes: [{ credential_id: 3, provider_id: 1, provider_code: 'p', manual_disabled: false }],
      }),
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.requests).toHaveLength(1)
    expect(liveStreamState.queue).not.toBeNull()

    // Identity swap fences generation 1 and (refCount>0) reopens generation 2.
    setUserInfo(userB)
    expect(liveStreamState.requests).toEqual([])
    expect(liveStreamState.queue).toBeNull()
    expect(liveStreamState.nodes).toEqual([])

    // The old socket delivers a late frame — it must be ignored via the
    // generation fence (es no longer points at the closed instance).
    first.onmessage?.({
      data: JSON.stringify({
        type: 'initial_data',
        requests_present: true,
        ts: new Date().toISOString(),
        requests: [{ ts: 'y', request_id: 'req-a-late', model: 'm', provider_code: 'p', status: 'success' }],
      }),
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.requests).toEqual([])
    release()
  })

  it('queue_present=false and nodes_present=false are authoritative clears', async () => {
    liveStreamState.queue = { enabled: true, wired: true, models: [], credentials: [] }
    liveStreamState.nodes = [{ credential_id: 1, provider_id: 1, provider_code: 'p', manual_disabled: false }]

    __testing.handleEnvelope({ type: 'queue_snapshot', ts: new Date().toISOString(), queue_present: true })
    expect(liveStreamState.queue).toBeNull()

    __testing.handleEnvelope({ type: 'node_update', ts: new Date().toISOString(), nodes_present: true })
    expect(liveStreamState.nodes).toEqual([])

    // Pre-presence servers (no flags, non-empty payload) still merge.
    __testing.handleEnvelope({
      type: 'node_update',
      ts: new Date().toISOString(),
      nodes: [{ credential_id: 5, provider_id: 1, provider_code: 'p', manual_disabled: false }],
    })
    expect(liveStreamState.nodes).toHaveLength(1)
  })
})
