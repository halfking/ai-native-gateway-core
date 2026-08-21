import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RequestLogDrawer from './RequestLogDrawer.vue'

const { getRequestLogDetail } = vi.hoisted(() => ({
  getRequestLogDetail: vi.fn(),
}))

vi.mock('../api', () => ({
  getRequestLogDetail,
  attachmentURL: (path: string) => path,
}))
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

const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': { requests: { detail_extra: { attachmentsTab: '附件' } }, trace: { modal: { tooltip: '', close: '关闭', openButton: '流程' } } } } })

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
