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
  getLocale: () => 'zh-CN',
  store: { locale: 'zh-CN', userInfo: null },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      requestDetail: {
        drawer: {
          title: '请求/会话详情', single: '单请求', turns: '会话轮次',
          openFullPage: '打开全页', close: '关闭', filterSession: '筛选会话',
          tabs: { overview: '概览', chat: '对话', flow: '流程', compress: '压缩与脱敏', routing: '路由', raw: '原始JSON' },
        },
        flow: {
          total: '总计', status: '状态 {value}', source: '来源 {src}',
          viewWaterfall: '调度瀑布', viewRoutingRetry: '路由与重试', inProgress: '处理中',
        },
        overview: {
          thisTurn: '本轮问答', viewFullConversation: '查看完整对话', userLabel: '用户', assistantLabel: '模型回复',
          labels: {
            requestId: '请求ID', requestTime: '请求时间', status: '状态', latency: '延迟', tenant: 'Tenant', success: 'Success',
            clientModel: '客户端模型', canonicalModel: '出站/规范模型', provider: '供应商', credential: '凭据', session: 'Session', sessionTitle: '会话标题',
            taskId: '任务 ID', endUser: 'End User', token: 'Token', total: '总', cache: 'Cache', costCredits: 'Cost / Credits', finishReason: 'finish_reason',
          },
          persisted: '已落库',
        },
      },
    },
  },
})

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
