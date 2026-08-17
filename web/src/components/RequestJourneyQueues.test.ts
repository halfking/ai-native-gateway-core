import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RequestJourneyQueues from './RequestJourneyQueues.vue'
import requestJourneys from '../locales/en-US/requestJourneys'

const { getQueues, getJourney, superAdmin } = vi.hoisted(() => ({
  getQueues: vi.fn(),
  getJourney: vi.fn(),
  superAdmin: vi.fn(() => false),
}))

vi.mock('../store', () => ({ isSuperAdmin: superAdmin }))

vi.mock('../api/request-journeys', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/request-journeys')>()
  return {
    ...actual,
    getRequestJourneyQueues: getQueues,
    getRequestJourney: getJourney,
  }
})

const i18n = createI18n({
  legacy: false,
  locale: 'en-US',
  messages: { 'en-US': { requestJourneys } },
})

function snapshot(requestId: string, updatedAt: string) {
  return {
    request_id: requestId,
    requested_model: 'auto',
    resolved_model: 'model-a',
    current_stage: 'model_queue' as const,
    observation_status: 'complete' as const,
    updated_at: updatedAt,
  }
}

function mountPanel() {
  return mount(RequestJourneyQueues, { attachTo: document.body, global: { plugins: [i18n], stubs: { Teleport: true } } })
}

async function openPanel(wrapper: ReturnType<typeof mount>) {
  await wrapper.get('.journey-trigger').trigger('click')
  await flushPromises()
}

describe('RequestJourneyQueues', () => {
  beforeEach(() => {
    getQueues.mockReset()
    getJourney.mockReset()
    superAdmin.mockReturnValue(false)
  })

  it('loads the total queue and preserves the server FIFO order', async () => {
    getQueues.mockResolvedValue({
      view: 'total',
      observation_status: 'complete',
      total_snapshot: {
        capacity: 100,
        requests: [
          snapshot('oldest-visible', '2026-08-17T10:00:00Z'),
          snapshot('newest-visible', '2026-08-17T10:01:00Z'),
        ],
      },
    })

    const wrapper = mountPanel()
    await openPanel(wrapper)

    expect(getQueues).toHaveBeenCalledWith('total', undefined)
    expect(wrapper.get('[data-testid="queue-window"]') .text()).toContain('2 / 100')
    const rows = wrapper.findAll('[data-testid="journey-queue-row"]')
    expect(rows.map(row => row.text())).toEqual([
      expect.stringContaining('oldest-visible'),
      expect.stringContaining('newest-visible'),
    ])
    expect(rows[0].text()).toContain('#1')
    expect(rows[1].text()).toContain('#2')
  })

  it('shows super-admin global ingress without exposing tenant detail navigation', async () => {
    superAdmin.mockReturnValue(true)
    getQueues.mockResolvedValue({
      view: 'total',
      scope: 'all',
      observation_status: 'complete',
      observation_scope: 'shared_redis',
      total_snapshot: {
        capacity: 100,
        requests: [{
          request_id: 'anonymous-request',
          gateway_instance_id: 'gateway-1',
          protocol: 'chat',
          path_class: 'chat_completions',
          arrived_at: '2026-08-17T10:00:00Z',
          updated_at: '2026-08-17T10:00:01Z',
          status: 'failed',
          error_kind: 'missing_key',
          http_status: 401,
        }],
      },
    })

    const wrapper = mountPanel()
    await openPanel(wrapper)

    expect(getQueues).toHaveBeenCalledWith('total', 'all')
    const row = wrapper.get('[data-testid="journey-queue-row"]')
    expect(row.attributes('disabled')).toBeDefined()
    await row.trigger('click')
    expect(getJourney).not.toHaveBeenCalled()
  })

  it('shows degraded observation even when the FIFO window is empty', async () => {
    getQueues.mockResolvedValue({
      view: 'total',
      observation_status: 'observation_degraded',
      total_snapshot: { capacity: 100, requests: [] },
    })

    const wrapper = mountPanel()
    await openPanel(wrapper)

    expect(wrapper.text()).toContain('Observation degraded')
    expect(wrapper.text()).toContain('No requests in this FIFO window')
  })

  it('loads model and node snapshots only when their tabs are selected', async () => {
    getQueues
      .mockResolvedValueOnce({ capacity: 100, requests: [] })
      .mockResolvedValueOnce([{
        model: 'model-a',
        capacity: 100,
        requests: [snapshot('model-request', '2026-08-17T10:00:00Z')],
      }])
      .mockResolvedValueOnce([{
        model: 'model-a',
        provider_id: 7,
        credential_id: 11,
        capacity: 100,
        requests: [{
          ...snapshot('node-request', '2026-08-17T10:00:00Z'),
          current_stage: 'upstream',
          attempt: { attempt_id: 'a-2', attempt_no: 2, model: 'model-a', provider_id: 7, credential_id: 11 },
        }],
      }])

    const wrapper = mountPanel()
    await openPanel(wrapper)
    await wrapper.get('[data-view="models"]').trigger('click')
    await flushPromises()

    expect(getQueues).toHaveBeenLastCalledWith('models', undefined)
    expect(wrapper.text()).toContain('model-request')

    await wrapper.get('[data-view="nodes"]').trigger('click')
    await flushPromises()

    expect(getQueues).toHaveBeenLastCalledWith('nodes', undefined)
    expect(wrapper.text()).toContain('Attempt 2')
    expect(wrapper.text()).toContain('Forwarding')
  })

  it('renders every node attempt state from snapshot lifecycle fields', async () => {
    const base = snapshot('state', '2026-08-17T10:00:00Z')
    getQueues
      .mockResolvedValueOnce({ capacity: 100, requests: [] })
      .mockResolvedValueOnce([{
        model: 'model-a',
        provider_id: 7,
        credential_id: 11,
        capacity: 100,
        requests: [
          { ...base, request_id: 'queued' },
          { ...base, request_id: 'forwarding', current_stage: 'upstream', last_event_type: 'attempt_started' },
          { ...base, request_id: 'first-byte', current_stage: 'streaming', last_event_type: 'first_byte' },
          { ...base, request_id: 'success', outcome: 'success' },
          { ...base, request_id: 'failed', outcome: 'failure' },
          { ...base, request_id: 'canceled', outcome: 'canceled' },
        ],
      }])

    const wrapper = mountPanel()
    await openPanel(wrapper)
    await wrapper.get('[data-view="nodes"]').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.queue-status').map(status => status.text())).toEqual([
      'Queued',
      'Forwarding',
      'First byte',
      'Success',
      'Failed',
      'Canceled',
    ])
  })

  it('opens a request journey with retries, switches, errors and final result', async () => {
    getQueues.mockResolvedValue({ capacity: 100, requests: [snapshot('req-journey', '2026-08-17T10:00:00Z')] })
    getJourney.mockResolvedValue({
      tenant_id: 'tenant-a',
      gateway_instance_id: 'gw-a',
      request_id: 'req-journey',
      observation_status: 'observation_degraded',
      started_at: '2026-08-17T10:00:00Z',
      updated_at: '2026-08-17T10:00:06Z',
      events: [
        { seq: 3, event_type: 'attempt_failed', stage: 'retrying', attempt: { attempt_id: 'a-1', attempt_no: 1, model: 'model-a', provider_id: 7, credential_id: 11 }, error_kind: 'timeout', observation_status: 'complete', occurred_at: '2026-08-17T10:00:03Z' },
        { seq: 4, event_type: 'retry_scheduled', stage: 'retrying', attempt: { attempt_id: 'a-1', attempt_no: 1, model: 'model-a', provider_id: 7, credential_id: 11 }, retry_reason: 'timeout', observation_status: 'complete', occurred_at: '2026-08-17T10:00:04Z' },
        { seq: 5, event_type: 'node_switched', stage: 'node_selection', from_credential_id: 11, to_credential_id: 12, switch_reason: 'timeout', observation_status: 'complete', occurred_at: '2026-08-17T10:00:05Z' },
        { seq: 6, event_type: 'model_switched', stage: 'routing', from_model: 'model-a', to_model: 'model-b', switch_reason: 'capacity', observation_status: 'complete', occurred_at: '2026-08-17T10:00:05.500Z' },
        { seq: 7, event_type: 'request_succeeded', stage: 'terminal', outcome: 'success', observation_status: 'observation_degraded', occurred_at: '2026-08-17T10:00:06Z' },
      ],
    })

    const wrapper = mountPanel()
    await openPanel(wrapper)
    await wrapper.get('[data-testid="journey-queue-row"]').trigger('click')
    await flushPromises()

    expect(getJourney).toHaveBeenCalledWith('req-journey')
    expect(wrapper.text()).toContain('Observation degraded')
    expect(wrapper.text()).toContain('Attempt failed')
    expect(wrapper.text()).toContain('Retry scheduled')
    expect(wrapper.text()).toContain('Node switched')
    expect(wrapper.text()).toContain('Model switched')
    expect(wrapper.text()).toContain('Request succeeded')
  })
})
