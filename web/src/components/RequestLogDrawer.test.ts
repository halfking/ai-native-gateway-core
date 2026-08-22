import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RequestLogDrawer from './RequestLogDrawer.vue'

const { getRequestLogDetail, getSessionSummary } = vi.hoisted(() => ({
  getRequestLogDetail: vi.fn(),
  getSessionSummary: vi.fn(),
}))

vi.mock('../api', () => ({
  getRequestLogDetail,
  attachmentURL: (path: string) => path,
}))
vi.mock('../api/logs', () => ({ getSessionSummary }))
vi.mock('../store', () => ({ isDefaultTenant: () => true }))
vi.mock('../api/memora', () => ({
  updateSessionTitle: vi.fn(),
  deleteSessionTitle: vi.fn(),
  summarizeSessionTitle: vi.fn(),
}))
vi.mock('../api/sessionAnalytics', () => ({
  getSessionTags: vi.fn().mockResolvedValue({ tags: [] }),
  addSessionTag: vi.fn(),
  updateSessionTag: vi.fn(),
  deleteSessionTag: vi.fn(),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      requests: {
        detail_extra: { attachmentsTab: '附件' },
        list: {
          trace: {
            drawerSummaryAria: '会话摘要',
            drawerSummaryTitle: '查看或生成会话摘要',
            drawerSummaryButton: '摘要',
            generating: '总结中…',
            generate: '生成总结',
            summaryRange: '范围：{from} ~ {to} · {n} 条',
          },
        },
      },
      trace: { modal: { tooltip: '', close: '关闭', openButton: '流程' } },
    },
  },
})

describe('RequestLogDrawer staged load', () => {
  beforeEach(() => {
    getRequestLogDetail.mockReset()
  })

  it('loads meta with omitBody first then merges full body', async () => {
    let resolveFull!: (value: unknown) => void
    getRequestLogDetail
      .mockImplementationOnce(async (_id: string, opts?: { omitBody?: boolean }) => {
        expect(opts?.omitBody).toBe(true)
        return {
          request_id: 'req-1',
          ts: '2026-08-21T00:00:00Z',
          success: true,
          client_model: 'm-1',
          provider_name: 'prov',
          request_body: null,
          response_body: null,
        }
      })
      .mockImplementationOnce(() => new Promise(resolve => { resolveFull = resolve }))

    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-1' },
      global: {
        plugins: [i18n],
        stubs: { Teleport: true, RequestTracePanel: true, RoutingAttemptsTimeline: true },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('prov')
    expect(wrapper.text()).toContain('正文与响应异步加载中')
    expect(getRequestLogDetail).toHaveBeenCalledWith('req-1', { omitBody: true })

    resolveFull({
      request_id: 'req-1',
      ts: '2026-08-21T00:00:00Z',
      success: true,
      client_model: 'm-1',
      provider_name: 'prov',
      request_body: { messages: [{ role: 'user', content: 'hi' }] },
      response_body: { choices: [] },
    })
    await flushPromises()
    expect(wrapper.text()).not.toContain('正文与响应异步加载中')
    expect(getRequestLogDetail).toHaveBeenCalledWith('req-1')
  })
})

describe('RequestLogDrawer title/summary compact row', () => {
  beforeEach(() => {
    getRequestLogDetail.mockReset()
    getSessionSummary.mockReset()
    getRequestLogDetail.mockResolvedValue({
      request_id: 'req-2',
      ts: '2026-08-21T00:00:00Z',
      success: true,
      client_model: 'm-1',
      provider_name: 'prov',
      gw_task_id: 'task-1',
      gw_session_id: 'sess-1',
      session_title: null,
      request_body: null,
      response_body: null,
    })
    getSessionSummary.mockResolvedValue({
      summary: '抽屉内摘要正文',
      key_points: [],
      meta: {
        session_id: 'sess-1',
        log_count: 1,
        data_from: '2026-08-21T00:00:00Z',
        data_to: '2026-08-21T00:01:00Z',
        generated_at: '2026-08-21T00:02:00Z',
        api_key_id: 1,
        model: 'm',
      },
    })
  })

  it('defaults to collapsed title ops and shows 无标题 + 摘要', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-2', mode: 'request-logs' },
      global: {
        plugins: [i18n],
        stubs: { Teleport: true, RequestTracePanel: true, RoutingAttemptsTimeline: true },
      },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('无标题')
    expect(wrapper.text()).toContain('摘要')
    expect(wrapper.text()).toContain('标题详情')
    expect(wrapper.text()).not.toContain('重新生成')
    expect(wrapper.text()).not.toContain('生成标题')
  })

  it('expands title actions and loads summary without emitting jump', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-2', mode: 'request-logs' },
      global: {
        plugins: [i18n],
        stubs: { Teleport: true, RequestTracePanel: true, RoutingAttemptsTimeline: true },
      },
    })
    await flushPromises()
    await wrapper.get('button[aria-expanded="false"]').trigger('click')
    expect(wrapper.text()).toContain('生成标题')
    await wrapper.get('button[aria-label="会话摘要"]').trigger('click')
    await flushPromises()
    expect(getSessionSummary).toHaveBeenCalledWith('sess-1')
    expect(wrapper.text()).toContain('抽屉内摘要正文')
    expect(wrapper.emitted('generateSessionSummary')).toBeUndefined()
  })
})

describe('RequestLogDrawer stackLevel', () => {
  beforeEach(() => {
    getRequestLogDetail.mockReset()
    getRequestLogDetail.mockResolvedValue({
      request_id: 'req-stack',
      ts: '2026-08-21T00:00:00Z',
      success: true,
      client_model: 'm-1',
      provider_name: 'prov',
      request_body: null,
      response_body: null,
    })
  })

  it('does not add nested class by default', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-stack' },
      global: {
        plugins: [i18n],
        stubs: { Teleport: true, RequestTracePanel: true, RoutingAttemptsTimeline: true },
      },
    })
    await flushPromises()
    expect(wrapper.get('.drawer-backdrop').classes()).not.toContain('drawer-backdrop--nested')
  })

  it('adds nested class when stackLevel is nested', async () => {
    const wrapper = mount(RequestLogDrawer, {
      props: { requestId: 'req-stack', stackLevel: 'nested' },
      global: {
        plugins: [i18n],
        stubs: { Teleport: true, RequestTracePanel: true, RoutingAttemptsTimeline: true },
      },
    })
    await flushPromises()
    expect(wrapper.get('.drawer-backdrop').classes()).toContain('drawer-backdrop--nested')
  })
})
