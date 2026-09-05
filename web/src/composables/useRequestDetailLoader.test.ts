import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  clearRequestDetailCache,
  mapLogRoutingAttempts,
  mergeRequestAttempts,
  useRequestDetailLoader,
} from './useRequestDetailLoader'

const {
  getUnifiedRequestDetail,
  getRequestLogDetail,
  fetchWaterfallByRequestId,
  getSessionSnapshot,
  getRequestJourney,
} = vi.hoisted(() => ({
  getUnifiedRequestDetail: vi.fn(),
  getRequestLogDetail: vi.fn(),
  fetchWaterfallByRequestId: vi.fn(),
  getSessionSnapshot: vi.fn(),
  getRequestJourney: vi.fn(),
}))

vi.mock('../api/requestDetail', () => ({ getUnifiedRequestDetail }))
vi.mock('../api/logs', () => ({ getRequestLogDetail }))
vi.mock('../api/dispatch', () => ({ fetchWaterfallByRequestId }))
vi.mock('../api/sessions_v2', () => ({ getSessionSnapshot }))
vi.mock('../api/request-journeys', () => ({ getRequestJourney }))

describe('mapLogRoutingAttempts', () => {
  it('maps log attempts to waterfall shape', () => {
    const out = mapLogRoutingAttempts([
      {
        seq: 2,
        provider_id: 1,
        credential_id: 9,
        raw_model: 'm',
        upstream_url: 'u',
        result: 'fail',
        latency_ms: 10,
        error_message: 'boom',
      },
    ])
    expect(out).toHaveLength(1)
    expect(out[0].attempt_no).toBe(2)
    expect(out[0].credential_id).toBe(9)
    expect(out[0].error_kind).toBe('boom')
  })
})

describe('mergeRequestAttempts', () => {
  it('uses durable journey refs over waterfall and synthesized routing attempts', () => {
    const out = mergeRequestAttempts(
      {
        events: [
          {
            tenant_id: 't', gateway_instance_id: 'g', request_id: 'r', stage: 'upstream', observation_status: 'complete',
            seq: 2,
            event_type: 'attempt_started',
            occurred_at: '2026-09-03T00:00:01Z',
            attempt: { attempt_id: 'j-1', attempt_no: 1, model: 'journey-model', provider_id: 7, credential_id: 8 },
          },
          {
            tenant_id: 't', gateway_instance_id: 'g', request_id: 'r', stage: 'retrying', observation_status: 'complete',
            seq: 3,
            event_type: 'attempt_failed',
            occurred_at: '2026-09-03T00:00:02Z',
            error_kind: 'timeout',
            outcome: 'failure',
            attempt: { attempt_id: 'j-1', attempt_no: 1 },
          },
        ],
      },
      [{ attempt_id: 'wf-1', attempt_no: 1, credential_id: 99, model: 'waterfall-model', outcome: 'success' }],
      [{ seq: 1, provider_id: 10, credential_id: 11, raw_model: 'routing-model', upstream_url: 'u', result: 'fail', latency_ms: 3 }],
    )

    expect(out).toEqual([{
      attempt_id: 'j-1',
      attempt_no: 1,
      model: 'journey-model',
      provider_id: 7,
      credential_id: 8,
      started_at: '2026-09-03T00:00:01Z',
      ended_at: '2026-09-03T00:00:02Z',
      outcome: 'failure',
      error_kind: 'timeout',
      source: 'journey',
    }])
  })

  it('keeps distinct attempts from lower-priority sources and marks their source', () => {
    const out = mergeRequestAttempts(
      { events: [{ tenant_id: 't', gateway_instance_id: 'g', request_id: 'r', stage: 'upstream', observation_status: 'complete', seq: 1, event_type: 'attempt_started', occurred_at: 't', attempt: { attempt_id: 'j-2', attempt_no: 2 } }] },
      [{ attempt_id: 'wf-3', attempt_no: 3, credential_id: 4 }],
      [{ seq: 4, provider_id: 5, credential_id: 6, raw_model: 'm', upstream_url: 'u', result: 'fail', latency_ms: 1 }],
    )

    expect(out.map((a) => [a.attempt_no, a.source])).toEqual([
      [2, 'journey'],
      [3, 'waterfall'],
      [4, 'synthesized'],
    ])
  })

  it('does not interpret request-level timing fields as per-attempt timing', () => {
    const out = mergeRequestAttempts(null, [], [{ seq: 1, provider_id: 1, credential_id: 2, raw_model: 'm', upstream_url: 'u', result: 'success', latency_ms: 1 }])
    expect(out[0]).not.toHaveProperty('started_at')
    expect(out[0]).not.toHaveProperty('first_byte_at')
    expect(out[0]).not.toHaveProperty('ended_at')
  })
})


describe('useRequestDetailLoader', () => {
  beforeEach(() => {
    clearRequestDetailCache()
    getUnifiedRequestDetail.mockReset()
    getRequestLogDetail.mockReset()
    fetchWaterfallByRequestId.mockReset()
    getSessionSnapshot.mockReset()
    getRequestJourney.mockReset()
    getSessionSnapshot.mockResolvedValue({ title: 'T', summary: 'S' })
    getRequestJourney.mockRejectedValue(new Error('journey unavailable'))
  })

  it('Phase A loads omit_body only and does not fetch waterfall', async () => {
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'r1', gw_session_id: 's1' },
    })
    getRequestLogDetail.mockResolvedValue({
      request_id: 'r1',
      gw_session_id: 's1',
      request_status: 'success',
    })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('r1')

    expect(getUnifiedRequestDetail).toHaveBeenCalledWith('r1', { omitBody: true })
    expect(getRequestLogDetail).toHaveBeenCalledWith('r1', { omitBody: true })
    expect(fetchWaterfallByRequestId).not.toHaveBeenCalled()
    expect(loader.sessionId.value).toBe('s1')
    expect(loader.metaLoading.value).toBe(false)
  })

  it('reuses short TTL cache on second loadMeta', async () => {
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'r2', gw_session_id: 's2' },
    })
    getRequestLogDetail.mockResolvedValue({ request_id: 'r2', gw_session_id: 's2' })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('r2')
    const n1 = getRequestLogDetail.mock.calls.length
    await loader.loadMeta('r2')
    expect(getRequestLogDetail.mock.calls.length).toBe(n1)
    expect(loader.log.value?.request_id).toBe('r2')
  })

  it('overview section ensures bodies', async () => {
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'r3' },
    })
    getRequestLogDetail
      .mockResolvedValueOnce({ request_id: 'r3' })
      .mockResolvedValueOnce({
        request_id: 'r3',
        request_body: { messages: [{ role: 'user', content: 'hi' }] },
        response_body: { choices: [{ message: { content: 'yo' } }] },
      })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('r3')
    await loader.onSectionNeed('r3', 'overview')
    expect(loader.bodiesLoaded.value).toBe(true)
    expect(loader.requestBody.value).toEqual({
      messages: [{ role: 'user', content: 'hi' }],
    })
  })

  it('switching request bumps seq and ignores stale waterfall', async () => {
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'memory',
      persistence: 'in_flight',
      meta: { request_id: 'a' },
    })
    getRequestLogDetail.mockResolvedValue({ request_id: 'a' })

    let resolveWf: (v: unknown) => void = () => {}
    fetchWaterfallByRequestId.mockImplementation(
      () => new Promise((resolve) => { resolveWf = resolve }),
    )

    const loader = useRequestDetailLoader()
    await loader.loadMeta('a')
    const p = loader.ensureWaterfall('a')
    await loader.loadMeta('b')
    resolveWf({
      request: {
        request_id: 'a',
        result: 'success',
        waiting_in_total_ms: 0,
        waiting_in_model_ms: 0,
        waiting_in_node_ms: 0,
        routing_ms: 0,
        acquire_ms: 0,
        upstream_latency_ms: 0,
        streaming_duration_ms: 0,
        queue_wait_ms: 0,
        total_ms: 0,
      },
      source: 'memory',
    })
    await p
    expect(loader.waterfall.value).toBeNull()
  })

  it('switching requests clears waterfall error so UI does not show stale error', async () => {
    getUnifiedRequestDetail
      .mockResolvedValueOnce({
        source: 'request_logs',
        persistence: 'persisted',
        meta: { request_id: 'r1' },
      })
      .mockResolvedValueOnce({
        source: 'request_logs',
        persistence: 'persisted',
        meta: { request_id: 'r2' },
      })
    getRequestLogDetail
      .mockResolvedValueOnce({ request_id: 'r1' })
      .mockResolvedValueOnce({ request_id: 'r2' })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('r1')
    await loader.ensureWaterfall('r1').catch(() => undefined)
    // Inject an error so we can verify it's cleared on the next loadMeta.
    loader.waterfallError.value = 'simulated upstream failure'

    await loader.loadMeta('r2')
    expect(loader.waterfallError.value).toBe('')
  })

  it('cache hit on the same request preserves cached waterfall and error', async () => {
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'cached' },
    })
    getRequestLogDetail.mockResolvedValue({ request_id: 'cached' })
    fetchWaterfallByRequestId.mockResolvedValue({
      request: {
        request_id: 'cached',
        result: 'success',
        waiting_in_total_ms: 0,
        waiting_in_model_ms: 0,
        waiting_in_node_ms: 0,
        routing_ms: 0,
        acquire_ms: 0,
        upstream_latency_ms: 0,
        streaming_duration_ms: 0,
        queue_wait_ms: 0,
        total_ms: 0,
      },
      source: 'memory',
    })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('cached')
    await loader.ensureWaterfall('cached')
    expect(loader.waterfall.value?.request_id).toBe('cached')

    const wfCallsBefore = fetchWaterfallByRequestId.mock.calls.length
    // Second loadMeta with the same request_id should reuse cache and
    // NOT re-fetch the waterfall.
    await loader.loadMeta('cached')
    expect(fetchWaterfallByRequestId.mock.calls.length).toBe(wfCallsBefore)
    expect(loader.waterfall.value?.request_id).toBe('cached')
  })

  it('switching requests mid-snapshot prevents the stale snap from polluting the next view', async () => {
    // P2-10: the in-flight session-snap for request A must not resolve into
    // sessionSnap.value after the user already navigated to request B.
    // Before the fix the closure-captured abort variable pointed at the new
    // (fresh) controller, so the A-side fetch kept running and could write
    // a stale value into the new view.
    getUnifiedRequestDetail
      .mockResolvedValueOnce({
        source: 'request_logs',
        persistence: 'persisted',
        meta: { request_id: 'a', gw_session_id: 'sA' },
      })
      .mockResolvedValueOnce({
        source: 'request_logs',
        persistence: 'persisted',
        meta: { request_id: 'b', gw_session_id: 'sB' },
      })
    getRequestLogDetail
      .mockResolvedValueOnce({ request_id: 'a', gw_session_id: 'sA' })
      .mockResolvedValueOnce({ request_id: 'b', gw_session_id: 'sB' })

    let resolveA: (v: unknown) => void = () => {}
    getSessionSnapshot.mockImplementation((sid: string) => {
      if (sid === 'sA') {
        return new Promise((resolve) => { resolveA = resolve })
      }
      return Promise.resolve({ title: `snap-${sid}`, summary: '' })
    })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('a') // schedules sA fetch, awaiting
    await loader.loadMeta('b') // switches to b, sessionSnap cleared, sB resolves fast
    await Promise.resolve()
    await Promise.resolve()
    expect(loader.sessionSnap.value).toEqual({ title: 'snap-sB', summary: '' })

    resolveA({ title: 'snap-sA-late', summary: 'STALE' })
    await new Promise((r) => setTimeout(r, 0))
    // sA is stale — the loader must not let it overwrite sB.
    expect(loader.sessionSnap.value).toEqual({ title: 'snap-sB', summary: '' })
  })

  it('getSessionSnapshot is called with an AbortSignal that is honoured', async () => {
    // P2-10: ensureSessionSnap passes the per-loader abort signal down to
    // the session-snap fetch, so external cancellation propagates.
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'r', gw_session_id: 's' },
    })
    getRequestLogDetail.mockResolvedValue({ request_id: 'r', gw_session_id: 's' })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('r')

    expect(getSessionSnapshot).toHaveBeenCalledTimes(1)
    const callArgs = getSessionSnapshot.mock.calls[0]?.[1] as
      | { signal?: AbortSignal }
      | undefined
    expect(callArgs?.signal).toBeInstanceOf(AbortSignal)
    // The signal must NOT be already aborted at fetch time (the loader
    // hands out a fresh controller on every loadMeta).
    expect(callArgs?.signal?.aborted).toBe(false)
  })

  it('cachePut preserves untouched fields on subsequent patches via the public path', async () => {
    // P2-10: the per-field ternary chain in cachePut is intentionally not
    // collapsed to `{ ...prev, ...patch }`. Verify the merge contract via
    // the public surface: after loadMeta populates log+unified, switching
    // requests and back must still find the original log+unified (cache
    // hit path restores from the same CacheEntry, no field is lost).
    clearRequestDetailCache()
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'm' },
    })
    getRequestLogDetail.mockResolvedValue({ request_id: 'm' })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('m')
    expect(loader.log.value?.request_id).toBe('m')

    // Trigger a second loadMeta on the same id — should be cache hit
    // (TTL not yet expired). The merged entry must still carry log+unified
    // because cachePut only refreshes `at` and any patch fields.
    await loader.loadMeta('m')
    expect(loader.log.value?.request_id).toBe('m')
    expect(loader.unified.value?.meta.request_id).toBe('m')
    expect(loader.bodiesLoaded.value).toBe(false)
  })
})
