import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { __testing, acquireLiveStream, liveStreamState, reconnectStream, resetLiveStreamIdentity } from './liveStreamStore'

class FakeEventSource {
  static instances: FakeEventSource[] = []
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  constructor(_url: string) { FakeEventSource.instances.push(this) }
  close() { this.readyState = 2 }
  message(payload: object) { this.onmessage?.({ data: JSON.stringify(payload) }) }
}

describe('live stream visibility transport lifecycle', () => {
  let hidden = false
  const releases: (() => void)[] = []
  const acquire = () => { releases.push(acquireLiveStream()); return FakeEventSource.instances.at(-1)! }
  const visibility = (value: boolean) => { hidden = value; document.dispatchEvent(new Event('visibilitychange')) }
  beforeEach(() => {
    hidden = false
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
    vi.stubGlobal('EventSource', FakeEventSource)
    FakeEventSource.instances = []
    __testing.resetForTest()
    __testing.resetStream()
  })
  afterEach(() => {
    releases.splice(0).forEach(release => release())
    __testing.resetForTest()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('closes while hidden without waiting for a frame, then replays the current authoritative state', () => {
    const old = acquire()
    old.message({ type: 'initial_data', requests_present: true, queue_present: true,
      requests: [{ request_id: 'old', ts: '2026-10-19T00:00:00Z', model: 'old', status: 'success' }],
      nodes_present: true, nodes: [{ credential_id: 1, manual_disabled: false }],
      queue: { enabled: true, wired: true, models: [{ model: 'old', depth: 2 }], credentials: [] } })
    visibility(true)
    expect(old.readyState).toBe(2)
    visibility(false)
    expect(FakeEventSource.instances).toHaveLength(2)
    old.onopen?.()
    expect(liveStreamState.connection).toBe('connecting')
    expect(liveStreamState.initialDataReceived).toBe(false)
    FakeEventSource.instances[1].message({ type: 'initial_data', requests_present: true, queue_present: true, nodes_present: true })
    expect(liveStreamState.initialDataReceived).toBe(true)
    expect(liveStreamState.requests).toEqual([])
    expect(liveStreamState.queue).toBeNull()
    expect(liveStreamState.nodes).toEqual([])
    old.message({ type: 'node_update', nodes: [{ credential_id: 99 }] })
    expect(liveStreamState.nodes).toEqual([])
  })

  it('defers the first hidden mount and manual or identity reconnect until visible', () => {
    hidden = true
    acquire()
    expect(FakeEventSource.instances).toHaveLength(0)
    reconnectStream()
    liveStreamState.nodes = [{ credential_id: 99, manual_disabled: false }]
    resetLiveStreamIdentity()
    expect(liveStreamState.nodes).toEqual([])
    expect(FakeEventSource.instances).toHaveLength(0)
    visibility(false)
    visibility(false)
    expect(FakeEventSource.instances).toHaveLength(1)
  })

  it('keeps one shared transport and never resumes after the last hidden consumer releases', () => {
    const first = acquire()
    acquire()
    expect(FakeEventSource.instances).toHaveLength(1)
    releases[0]()
    releases[0]()
    expect(first.readyState).toBe(0)
    visibility(true)
    expect(first.readyState).toBe(2)
    releases[1]()
    visibility(false)
    expect(__testing.refCount()).toBe(0)
    expect(FakeEventSource.instances).toHaveLength(1)
  })

  it('cancels fatal backoff while hidden and fences late errors before visible recovery', async () => {
    const first = acquire()
    first.readyState = 2
    first.onerror?.()
    visibility(true)
    await vi.advanceTimersByTimeAsync(60_000)
    first.onerror?.()
    reconnectStream()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(FakeEventSource.instances).toHaveLength(1)
    visibility(false)
    expect(FakeEventSource.instances).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(FakeEventSource.instances).toHaveLength(2)
  })

  it('keeps acquireForTest release idempotent and its visibility listener alive for other refs', () => {
    const add = vi.spyOn(document, 'addEventListener'), remove = vi.spyOn(document, 'removeEventListener')
    const a = __testing.acquireForTest()
    const b = __testing.acquireForTest()
    const added = add.mock.calls.filter(([name]) => name === 'visibilitychange')
    expect(added).toHaveLength(1)
    a()
    a()
    expect(__testing.refCount()).toBe(1)
    visibility(true)
    visibility(false)
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(remove).not.toHaveBeenCalledWith('visibilitychange', added[0][1])
    b()
    expect(remove).toHaveBeenCalledWith('visibilitychange', added[0][1])
    expect(FakeEventSource.instances[0].readyState).toBe(2)
    visibility(true)
    visibility(false)
    expect(FakeEventSource.instances).toHaveLength(1)
  })
})
