import { beforeEach, describe, expect, it, vi } from 'vitest'

const { req } = vi.hoisted(() => ({ req: vi.fn() }))

vi.mock('./_core', () => ({ req }))

import { getRequestJourney, getRequestJourneyQueues } from './request-journeys'

describe('request journey API', () => {
  beforeEach(() => req.mockReset())

  it('requests the global ingress FIFO for super-admin total view', async () => {
    req.mockResolvedValue({ view: 'total', scope: 'all', observation_status: 'complete' })

    await getRequestJourneyQueues('total', 'all')

    expect(req).toHaveBeenCalledWith(
      'GET',
      '/api/admin/request-journeys/queues?view=total&scope=all',
    )
  })

  it.each(['total', 'models', 'nodes'] as const)('requests the %s FIFO view', async (view) => {
    req.mockResolvedValue({ view, observation_status: 'complete' })

    await getRequestJourneyQueues(view)

    expect(req).toHaveBeenCalledWith(
      'GET',
      `/api/admin/request-journeys/queues?view=${view}`,
    )
  })

  it('URL-encodes the request id used by the detail endpoint', async () => {
    req.mockResolvedValue({ request_id: 'req/with spaces', events: [] })

    await getRequestJourney('req/with spaces')

    expect(req).toHaveBeenCalledWith(
      'GET',
      '/api/admin/request-journeys/req%2Fwith%20spaces',
    )
  })

  it('unwraps the contract detail envelope', async () => {
    req.mockResolvedValue({
      observation_status: 'observation_degraded',
      journey: { request_id: 'req-1', observation_status: 'observation_degraded', events: [] },
    })

    await expect(getRequestJourney('req-1')).resolves.toMatchObject({ request_id: 'req-1' })
  })
})
