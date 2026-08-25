import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mapLogRoutingAttempts, useRequestDetailLoader } from './useRequestDetailLoader'

const { getUnifiedRequestDetail, getRequestLogDetail, fetchWaterfallByRequestId } = vi.hoisted(() => ({
  getUnifiedRequestDetail: vi.fn(),
  getRequestLogDetail: vi.fn(),
  fetchWaterfallByRequestId: vi.fn(),
}))

vi.mock('../api/requestDetail', () => ({ getUnifiedRequestDetail }))
vi.mock('../api/logs', () => ({ getRequestLogDetail }))
vi.mock('../api/dispatch', () => ({ fetchWaterfallByRequestId }))

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
    getUnifiedRequestDetail.mockReset()
    getRequestLogDetail.mockReset()
    fetchWaterfallByRequestId.mockReset()
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
    resolveWf({ request: { request_id: 'a', result: 'success', waiting_in_total_ms: 0, waiting_in_model_ms: 0, waiting_in_node_ms: 0, routing_ms: 0, acquire_ms: 0, upstream_latency_ms: 0, streaming_duration_ms: 0, queue_wait_ms: 0, total_ms: 0 }, source: 'memory' })
    await p
    expect(loader.waterfall.value).toBeNull()
  })
})
