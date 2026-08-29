import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  clearRequestDetailCache,
  mapLogRoutingAttempts,
  useRequestDetailLoader,
} from './useRequestDetailLoader'

const {
  getUnifiedRequestDetail,
  getRequestLogDetail,
  fetchWaterfallByRequestId,
  getSessionSnapshot,
} = vi.hoisted(() => ({
  getUnifiedRequestDetail: vi.fn(),
  getRequestLogDetail: vi.fn(),
  fetchWaterfallByRequestId: vi.fn(),
  getSessionSnapshot: vi.fn(),
}))

vi.mock('../api/requestDetail', () => ({ getUnifiedRequestDetail }))
vi.mock('../api/logs', () => ({ getRequestLogDetail }))
vi.mock('../api/dispatch', () => ({ fetchWaterfallByRequestId }))
vi.mock('../api/sessions_v2', () => ({ getSessionSnapshot }))

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

describe('useRequestDetailLoader', () => {
  beforeEach(() => {
    clearRequestDetailCache()
    getUnifiedRequestDetail.mockReset()
    getRequestLogDetail.mockReset()
    fetchWaterfallByRequestId.mockReset()
    getSessionSnapshot.mockReset()
    getSessionSnapshot.mockResolvedValue({ title: 'T', summary: 'S' })
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
})
