import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import RequestActionsView from './RequestActionsView.vue'
import { getRequestActions } from '../api/requestActions'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/requestActions', () => ({
  getRequestActions: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 'req-001' } }),
  useRouter: () => ({ push: vi.fn() }),
}))

const mockedGet = vi.mocked(getRequestActions)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE = {
  request_id: 'req-001',
  count: 3,
  actions: [
    {
      request_id: 'req-001',
      seq: 1,
      action: 'request_received',
      ts: '2026-10-17T10:00:00.000Z',
      model: 'gpt-4o',
    },
    {
      request_id: 'req-001',
      seq: 2,
      action: 'credential_pick',
      ts: '2026-10-17T10:00:01.000Z',
      credential_id: 42,
    },
    {
      request_id: 'req-001',
      seq: 3,
      action: 'retry',
      ts: '2026-10-17T10:00:05.000Z',
      retry: true,
      retry_seq: 1,
      error_kind: 'upstream_503',
    },
  ],
}

describe('RequestActionsView', () => {
  it('renders 3 actions with key field coverage (model / credential_id / retry)', async () => {
    mockedGet.mockResolvedValueOnce(SAMPLE)
    const w = mount(RequestActionsView, {
      global: { plugins: [testI18n],  mocks: { $route: { params: { id: 'req-001' } } } },
    })
    await flushPromises()
    // 头部
    expect(w.text()).toContain('共 3 条动作')
    expect(w.text()).toContain('req-001')
    // 3 个事件
    const events = w.findAll('.ra-event')
    expect(events).toHaveLength(3)
    // 第 1 个含 model
    expect(events[0].text()).toContain('#1')
    expect(events[0].text()).toContain('request_received')
    expect(events[0].text()).toContain('gpt-4o')
    // 第 2 个含 credential_id
    expect(events[1].text()).toContain('credential_id')
    expect(events[1].text()).toContain('42')
    // 第 3 个含 retry + retry_seq + error_kind
    expect(events[2].text()).toContain('retry')
    expect(events[2].text()).toContain('第 1 次重试')
    expect(events[2].text()).toContain('upstream_503')
  })

  it('shows empty state when backend returns 0 actions', async () => {
    mockedGet.mockResolvedValueOnce({ request_id: 'req-empty', count: 0, actions: [] })
    const w = mount(RequestActionsView, {
      global: { plugins: [testI18n],  mocks: { $route: { params: { id: 'req-empty' } } } },
    })
    await flushPromises()
    expect(w.text()).toContain('暂无动作记录')
    expect(w.findAll('.ra-event')).toHaveLength(0)
  })

  it('shows error message on fetch failure', async () => {
    mockedGet.mockRejectedValueOnce(new Error('upstream 502'))
    const w = mount(RequestActionsView, {
      global: { plugins: [testI18n],  mocks: { $route: { params: { id: 'req-err' } } } },
    })
    await flushPromises()
    expect(w.find('.ra-error').exists()).toBe(true)
    expect(w.text()).toContain('upstream 502')
    expect(w.findAll('.ra-event')).toHaveLength(0)
  })
})
