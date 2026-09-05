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

  // ------------------------------------------------------------------
  // F2-#5 regression suite (drawer migration, 2026-09-05). The drawer
  // previously self-managed loading and hit three defect classes; these
  // tests pin the composable guarantees the migrated drawer relies on.
  // ------------------------------------------------------------------

  it('F2-#5 concurrent override: a later meta-only reload must not clobber fetched bodies', async () => {
    getUnifiedRequestDetail
      .mockResolvedValueOnce({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 'c1' } })
      .mockResolvedValueOnce({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 'c1' }, bodies: { request_body: { messages: ['full'] } } })
      .mockResolvedValue({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 'c1' } })
    getRequestLogDetail
      .mockResolvedValueOnce({ request_id: 'c1' })
      .mockResolvedValueOnce({ request_id: 'c1', request_body: { messages: ['full'] } })
      .mockResolvedValue({ request_id: 'c1' })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('c1')
    await loader.ensureBodies('c1')
    expect(loader.requestBody.value).toEqual({ messages: ['full'] })

    // Re-entering loadMeta for the same request (drawer: re-selecting the
    // turn / requestId watcher re-fire) must restore the cached full-body
    // entry — the bodyless meta projection of the reload must not overwrite
    // the already-fetched bodies (old drawer bug: bodyless `unified` landed
    // after ensureBodies and the chat tab never re-fetched).
    await loader.loadMeta('c1')
    expect(loader.requestBody.value).toEqual({ messages: ['full'] })
    expect(loader.bodiesLoaded.value).toBe(true)
  })

  it('F2-#5 concurrency: an in-flight body fetch is discarded after the meta reload re-enters', async () => {
    getUnifiedRequestDetail
      .mockResolvedValueOnce({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 'c2' } })
    getRequestLogDetail
      .mockResolvedValueOnce({ request_id: 'c2' })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('c2')

    let resolveLog: (v: unknown) => void = () => {}
    let resolveUnified: (v: unknown) => void = () => {}
    getRequestLogDetail.mockImplementationOnce(
      () => new Promise((resolve) => { resolveLog = resolve }),
    )
    getUnifiedRequestDetail.mockImplementationOnce(
      () => new Promise((resolve) => { resolveUnified = resolve }),
    )

    const pending = loader.ensureBodies('c2')
    // Meta-only reload for the same request re-enters (bumps seq) while the
    // body fetch is still in flight.
    await loader.loadMeta('c2')

    resolveLog({ request_id: 'c2', request_body: { stale: true } })
    resolveUnified({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'c2' },
      bodies: { request_body: { stale: true } },
    })
    await pending

    // The stale fetch (captured pre-bump seq) must not write into the
    // reloaded view; bodies stay unloaded so the active section re-requests.
    expect(loader.requestBody.value).toBeNull()
    expect(loader.bodiesLoaded.value).toBe(false)
  })

  it('F2-#5 switch residue: switching requests never shows the previous request bodies', async () => {
    getUnifiedRequestDetail.mockImplementation((id: string, opts?: { omitBody?: boolean }) => {
      const meta = { source: 'request_logs', persistence: 'persisted', meta: { request_id: id } }
      if (opts?.omitBody) return Promise.resolve(meta)
      return Promise.resolve({ ...meta, bodies: { request_body: { owner: id } } })
    })
    getRequestLogDetail.mockImplementation((id: string, opts?: { omitBody?: boolean }) => {
      const meta = { request_id: id }
      if (opts?.omitBody) return Promise.resolve(meta)
      return Promise.resolve({ ...meta, request_body: { owner: id } })
    })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('req-a')
    await loader.ensureBodies('req-a')
    expect(loader.requestBody.value).toEqual({ owner: 'req-a' })

    // Switch to req-b: shared state must be cleared immediately — req-a's
    // body must not be visible under req-b, and ensureBodies('req-b') must
    // actually fetch req-b's own body instead of early-returning on the
    // residue (old drawer guard `unified?.bodies || log?.request_body` saw
    // the previous turn's body and marked the new turn as loaded).
    await loader.loadMeta('req-b')
    expect(loader.requestBody.value).toBeNull()
    expect(loader.bodiesLoaded.value).toBe(false)

    await loader.ensureBodies('req-b')
    expect(loader.requestBody.value).toEqual({ owner: 'req-b' })
  })

  it('F2-#5 sticky loading: bodiesLoading resets when the request switches mid-fetch', async () => {
    let resolveBodyLog: (v: unknown) => void = () => {}
    let resolveBodyUnified: (v: unknown) => void = () => {}
    getUnifiedRequestDetail
      .mockResolvedValueOnce({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 's1' } })
      .mockImplementationOnce(() => new Promise((resolve) => { resolveBodyUnified = resolve }))
      .mockResolvedValue({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 's2' } })
    getRequestLogDetail
      .mockResolvedValueOnce({ request_id: 's1' })
      .mockImplementationOnce(() => new Promise((resolve) => { resolveBodyLog = resolve }))
      .mockResolvedValue({ request_id: 's2' })

    const loader = useRequestDetailLoader()
    await loader.loadMeta('s1')
    const pending = loader.ensureBodies('s1')
    expect(loader.bodiesLoading.value).toBe(true)

    // Switch requests while the body fetch is in flight: bodiesLoading must
    // reset immediately (old drawer: the finally block only reset it when
    // `seq === loadSeq`, so after the switch bumped the seq it stayed true
    // forever and the chat tab showed a perpetual loading state).
    await loader.loadMeta('s2')
    expect(loader.bodiesLoading.value).toBe(false)

    resolveBodyLog({ request_id: 's1', request_body: { stale: true } })
    resolveBodyUnified({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 's1' },
      bodies: { request_body: { stale: true } },
    })
    await pending
    expect(loader.bodiesLoading.value).toBe(false)
    expect(loader.requestBody.value).toBeNull()
  })

  // ------------------------------------------------------------------
  // round2-followup audit (pkg5, 2026-09-05) regression suite.
  // ------------------------------------------------------------------

  it('round2 P2-1: a stale endpoint failure after switching requests does not leak into the new warnings', async () => {
    let rejectAUnified: (e: unknown) => void = () => {}
    getUnifiedRequestDetail.mockImplementation((id: string) => {
      if (id === 'warn-a') {
        return new Promise((_resolve, reject) => { rejectAUnified = reject })
      }
      return Promise.resolve({ source: 'request_logs', persistence: 'persisted', meta: { request_id: id } })
    })
    getRequestLogDetail.mockImplementation((id: string) => Promise.resolve({ request_id: id }))

    const loader = useRequestDetailLoader()
    const pendingA = loader.loadMeta('warn-a')
    // Switch to B while A's omitBody unified fetch is still in flight:
    // B's loadMeta has already cleared the warnings list.
    await loader.loadMeta('warn-b')
    expect(loader.metaWarnings.value).toEqual([])
    expect(loader.log.value?.request_id).toBe('warn-b')

    // A's endpoint now fails — the stale failure must not be recorded
    // against B's freshly-loaded view.
    rejectAUnified(new Error('boom'))
    await pendingA
    await new Promise((r) => setTimeout(r, 0))
    expect(loader.metaWarnings.value).toEqual([])
    expect(loader.log.value?.request_id).toBe('warn-b')
  })

  it('round2 P2-2: same-seq parallel loadMeta + ensureBodies keeps bodies when ensureBodies lands first', async () => {
    // Fullscreen fan-out race: the requestId watcher's loadMeta (omitBody,
    // pending below) and the viewMode watcher's ensureBodies run in the
    // same-seq window, and ensureBodies resolves first.
    let resolveMetaLog: (v: unknown) => void = () => {}
    let resolveMetaUnified: (v: unknown) => void = () => {}
    getUnifiedRequestDetail.mockImplementation((_id: string, opts?: { omitBody?: boolean }) => {
      if (opts?.omitBody) {
        return new Promise((resolve) => { resolveMetaUnified = resolve })
      }
      return Promise.resolve({
        source: 'request_logs',
        persistence: 'persisted',
        meta: { request_id: 'par' },
        bodies: { request_body: { full: true } },
      })
    })
    getRequestLogDetail.mockImplementation((_id: string, opts?: { omitBody?: boolean }) => {
      if (opts?.omitBody) {
        return new Promise((resolve) => { resolveMetaLog = resolve })
      }
      return Promise.resolve({ request_id: 'par', request_body: { full: true } })
    })

    const loader = useRequestDetailLoader()
    const metaPending = loader.loadMeta('par')
    // Same seq: ensureBodies starts inside loadMeta's in-flight window.
    const bodiesPending = loader.ensureBodies('par')
    await bodiesPending
    expect(loader.requestBody.value).toEqual({ full: true })

    // The bodyless meta landing must not clobber the bodies ensureBodies
    // just delivered (view state) nor reset the cached bodiesLoaded entry.
    resolveMetaLog({ request_id: 'par' })
    resolveMetaUnified({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 'par' } })
    await metaPending
    expect(loader.requestBody.value).toEqual({ full: true })
    expect(loader.bodiesLoaded.value).toBe(true)

    // The cache entry keeps the bodies too — a re-entry restores them.
    await loader.loadMeta('par')
    expect(loader.requestBody.value).toEqual({ full: true })
  })

  it('round2 P3-3: a slow journey does not delay meta first paint and lands out-of-band', async () => {
    getUnifiedRequestDetail.mockResolvedValue({ source: 'request_logs', persistence: 'persisted', meta: { request_id: 'j1' } })
    getRequestLogDetail.mockResolvedValue({ request_id: 'j1' })
    let resolveJourney: (v: unknown) => void = () => {}
    getRequestJourney.mockImplementationOnce(() => new Promise((resolve) => { resolveJourney = resolve }))

    const loader = useRequestDetailLoader()
    await loader.loadMeta('j1')
    // Meta first paint must not wait on the journey cold path.
    expect(loader.metaLoading.value).toBe(false)
    expect(loader.log.value?.request_id).toBe('j1')
    expect(loader.attempts.value).toEqual([])

    resolveJourney({
      events: [{
        tenant_id: 't', gateway_instance_id: 'g', request_id: 'j1', stage: 'upstream', observation_status: 'complete',
        seq: 1,
        event_type: 'attempt_started',
        occurred_at: '2026-09-05T00:00:00Z',
        attempt: { attempt_id: 'j-9', attempt_no: 1, model: 'journey-model' },
      }],
    })
    await new Promise((r) => setTimeout(r, 0))
    // The out-of-band journey feeds the merged attempts once resolved.
    expect(loader.attempts.value.map((a) => a.attempt_id)).toEqual(['j-9'])
    expect(loader.attempts.value[0]?.source).toBe('journey')
  })

  it('round2 P3-3: a stale journey landing after a request switch is discarded', async () => {
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'jr-b' },
    })
    getRequestLogDetail.mockResolvedValue({ request_id: 'jr-b' })
    let resolveAJourney: (v: unknown) => void = () => {}
    getRequestJourney.mockImplementationOnce(() => new Promise((resolve) => { resolveAJourney = resolve }))

    const loader = useRequestDetailLoader()
    await loader.loadMeta('jr-a') // jr-a journey fetch stays pending
    await loader.loadMeta('jr-b') // switch: journey cleared, seq bumped

    resolveAJourney({
      events: [{
        tenant_id: 't', gateway_instance_id: 'g', request_id: 'jr-a', stage: 'upstream', observation_status: 'complete',
        seq: 1, event_type: 'attempt_started', occurred_at: 't',
        attempt: { attempt_id: 'stale', attempt_no: 1 },
      }],
    })
    await new Promise((r) => setTimeout(r, 0))
    // The stale journey must not bleed into jr-b's view.
    expect(loader.attempts.value).toEqual([])
  })
})
