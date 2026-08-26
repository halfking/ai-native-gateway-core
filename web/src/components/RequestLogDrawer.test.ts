import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RequestLogDrawer from './RequestLogDrawer.vue'

const { getRequestLogDetail, getUnifiedRequestDetail, getRequestTrace, getSessionCompare } = vi.hoisted(() => ({
  getRequestLogDetail: vi.fn(),
  getUnifiedRequestDetail: vi.fn(),
  getRequestTrace: vi.fn(),
  getSessionCompare: vi.fn(),
}))

vi.mock('../api', () => ({
  getRequestLogDetail,
  attachmentURL: (path: string) => path,
}))
vi.mock('../api/logs', () => ({ getRequestLogDetail }))
vi.mock('../api/requestDetail', () => ({ getUnifiedRequestDetail }))
vi.mock('../api/trace', () => ({
  getRequestTrace,
  buildAIPrompt: vi.fn(),
}))
vi.mock('../api/session', () => ({ getSessionCompare }))
vi.mock('../api/sessionTurnsTree', () => ({
  fetchSessionTurnsTree: vi.fn().mockResolvedValue({ turns: [], count: 0, has_more: false, next_cursor: '' }),
  SessionObsApiError: class extends Error {},
}))
vi.mock('../store', () => ({
  isDefaultTenant: () => true,
  isSuperAdmin: () => true,
}))

const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })

describe('RequestLogDrawer compatibility shell', () => {
  beforeEach(() => {
    getUnifiedRequestDetail.mockResolvedValue({
      source: 'request_logs',
      persistence: 'persisted',
      meta: { request_id: 'req-1', gw_session_id: 'sess-1', request_status: 'success' },
      bodies: { request_body: { messages: [{ role: 'user', content: 'hi' }] } },
    })
    getRequestLogDetail.mockResolvedValue({
      request_id: 'req-1',
      gw_session_id: 'sess-1',
      request_status: 'success',
      success: true,
      latency_ms: 12,
      client_model: 'gpt-4o',
      prompt_tokens: 1,
      completion_tokens: 2,
      request_body: { messages: [{ role: 'user', content: 'hi' }] },
      response_body: null,
      outbound_body: null,
    })
    getRequestTrace.mockResolvedValue({
      request_id: 'req-1',
      events: [{ seq: 1, stage: 'arrive', duration_ms: 5, status: 'success', module: 'gw', timestamp: '' }],
      final_status: 'success',
      total_duration_ms: 5,
      source: 'postgres',
    })
  })

  afterEach(() => { vi.clearAllMocks() })

  it('renders unified drawer and loads detail', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-1', mode: 'request-logs' },
      global: { plugins: [i18n] },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('请求/会话详情')
    expect(wrapper.text()).toContain('单请求')
    expect(getUnifiedRequestDetail).toHaveBeenCalled()
    expect(wrapper.find('.drawer-backdrop').exists()).toBe(true)
  })

  it('raises nested stack class', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-1', stackLevel: 'nested' },
      global: { plugins: [i18n] },
    })
    await flushPromises()
    expect(wrapper.find('.drawer-backdrop--nested').exists()).toBe(true)
  })

  it('opens flow tab when initialTraceOpen', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-1', initialTraceOpen: true },
      global: { plugins: [i18n] },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('流程')
    expect(getRequestTrace).toHaveBeenCalled()
  })
})
