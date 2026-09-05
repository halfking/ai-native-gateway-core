import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RequestLogDrawer from './RequestLogDrawer.vue'
import ConversationMessagesPanel from './detail/ConversationMessagesPanel.vue'
import SessionTurnsSyncPane from './detail/SessionTurnsSyncPane.vue'
import { clearRequestDetailCache } from '../composables/useRequestDetailLoader'

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

// ---------------------------------------------------------------------------
// F2-#5 regression suite (2026-09-05): the unified drawer migrated its
// loading logic to useRequestDetailLoader. These tests pin the drawer-level
// behavior that used to break when it self-managed loading:
//   1. concurrent override — bodyless meta responses must not clobber
//      fetched bodies, and switching turns must never render the previous
//      turn's bodies (cross-request residue);
//   2. bodies loading must not get stuck after a turn switch mid-fetch.
// ---------------------------------------------------------------------------

const A_BODY = { messages: [{ role: 'user', content: 'from-turn-a' }] }
const B_BODY = { messages: [{ role: 'user', content: 'from-turn-b' }] }

function metaFor(id: string) {
  return {
    source: 'request_logs',
    persistence: 'persisted',
    meta: { request_id: id, gw_session_id: 'sess-1', request_status: 'success' },
  }
}

function metaLogFor(id: string) {
  return { request_id: id, gw_session_id: 'sess-1', request_status: 'success' }
}

async function clickButtonText(
  wrapper: { findAll: (selector: string) => Array<{ text(): string; trigger(event: string): Promise<unknown> }> },
  text: string,
) {
  const btn = wrapper.findAll('button').find((b) => b.text() === text)
  if (!btn) throw new Error(`button "${text}" not found`)
  await btn.trigger('click')
}

describe('UnifiedRequestSessionDrawer turn-switch regressions (F2-#5)', () => {
  beforeEach(() => {
    clearRequestDetailCache()
    getUnifiedRequestDetail.mockReset()
    getRequestLogDetail.mockReset()
    getUnifiedRequestDetail.mockImplementation((id: string, opts?: { omitBody?: boolean }) => {
      if (opts?.omitBody) return Promise.resolve(metaFor(id))
      return Promise.resolve({
        ...metaFor(id),
        bodies: { request_body: id === 'req-a' ? A_BODY : B_BODY },
      })
    })
    getRequestLogDetail.mockImplementation((id: string, opts?: { omitBody?: boolean }) => {
      if (opts?.omitBody) return Promise.resolve(metaLogFor(id))
      return Promise.resolve({
        ...metaLogFor(id),
        request_body: id === 'req-a' ? A_BODY : B_BODY,
      })
    })
  })

  afterEach(() => { vi.clearAllMocks() })

  it('shows the new turn bodies after a session-pane turn switch (no previous-turn residue)', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-a' },
      global: { plugins: [i18n] },
    })
    await flushPromises()

    // Chat tab triggers the phased body fetch for req-a.
    await clickButtonText(wrapper, '对话')
    await flushPromises()
    expect(wrapper.findComponent(ConversationMessagesPanel).props('body')).toEqual(A_BODY)

    // Switch to the session pane and select turn B (old bug: the residue
    // guard saw turn A's body and marked turn B's bodies as loaded, so
    // turn A's content rendered under turn B).
    await clickButtonText(wrapper, '会话轮次')
    await flushPromises()
    const pane = wrapper.findComponent(SessionTurnsSyncPane)
    expect(pane.exists()).toBe(true)
    pane.vm.$emit('select-request', 'req-b', 2)
    await flushPromises()

    // Back to the single-request view: chat tab is still active and must
    // now show turn B's own freshly fetched body.
    await clickButtonText(wrapper, '单请求')
    await flushPromises()
    expect(wrapper.findComponent(ConversationMessagesPanel).props('body')).toEqual(B_BODY)
  })

  it('bodies loading indicator clears after a turn switch resolves mid-fetch (not sticky)', async () => {
    // Turn B's full-body fetch hangs until we resolve it manually. Call
    // order: (a, omitBody) → (a, full) → (b, omitBody) → (b, full)=pending.
    let resolveBLog: (v: unknown) => void = () => {}
    let resolveBUnified: (v: unknown) => void = () => {}
    getUnifiedRequestDetail.mockImplementation((id: string, opts?: { omitBody?: boolean }) => {
      if (opts?.omitBody) return Promise.resolve(metaFor(id))
      if (id === 'req-b') return new Promise((resolve) => { resolveBUnified = resolve })
      return Promise.resolve({ ...metaFor(id), bodies: { request_body: A_BODY } })
    })
    getRequestLogDetail.mockImplementation((id: string, opts?: { omitBody?: boolean }) => {
      if (opts?.omitBody) return Promise.resolve(metaLogFor(id))
      if (id === 'req-b') return new Promise((resolve) => { resolveBLog = resolve })
      return Promise.resolve({ ...metaLogFor(id), request_body: A_BODY })
    })

    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-a' },
      global: { plugins: [i18n] },
    })
    await flushPromises()
    await clickButtonText(wrapper, '对话')
    await flushPromises()
    expect(wrapper.findComponent(ConversationMessagesPanel).props('body')).toEqual(A_BODY)
    expect(wrapper.find('.drawer-body-scroll .drawer-loading').exists()).toBe(false)

    // Switch turns while turn B's body fetch is still pending, then return
    // to the single-request view: the loading state must show while the
    // fetch is in flight and MUST clear once it resolves (old bug: the
    // seq-captured finally left bodiesLoading stuck on true forever).
    await clickButtonText(wrapper, '会话轮次')
    await flushPromises()
    wrapper.findComponent(SessionTurnsSyncPane).vm.$emit('select-request', 'req-b', 2)
    await flushPromises()
    await clickButtonText(wrapper, '单请求')
    await flushPromises()
    expect(wrapper.find('.drawer-body-scroll .drawer-loading').exists()).toBe(true)

    resolveBLog({ ...metaLogFor('req-b'), request_body: B_BODY })
    resolveBUnified({ ...metaFor('req-b'), bodies: { request_body: B_BODY } })
    await flushPromises()
    expect(wrapper.find('.drawer-body-scroll .drawer-loading').exists()).toBe(false)
    expect(wrapper.findComponent(ConversationMessagesPanel).props('body')).toEqual(B_BODY)
  })
})
