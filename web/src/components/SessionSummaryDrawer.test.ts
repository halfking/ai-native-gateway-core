import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SessionSummaryDrawer from './SessionSummaryDrawer.vue'

const { getRequestLogs, getSessionSummary } = vi.hoisted(() => ({
  getRequestLogs: vi.fn(),
  getSessionSummary: vi.fn(),
}))

vi.mock('../api/logs', () => ({
  getRequestLogs,
  getSessionSummary,
  sessionSummaryToMemora: vi.fn(),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      requests: {
        list: {
          trace: {
            generating: '总结中…',
            generate: '生成总结',
            exportMd: '导出 Markdown',
            exportTxt: '导出 TXT',
            writingMemora: '写入中…',
            writeMemora: '写入 Memora',
            memoraWritten: '已写入 Memora：{n} 条（{status}）',
            summaryRange: '范围：{from} ~ {to} · {n} 条',
          },
          summary: {
            title: '# 会话总结',
            summaryHeading: '## 摘要',
            keyPointsHeading: '## 关键要点',
          },
        },
      },
    },
  },
})

const summaryPayload = {
  summary: '独立抽屉摘要正文',
  key_points: ['要点一'],
  meta: {
    session_id: 'sess-drawer',
    log_count: 2,
    data_from: '2026-08-21T00:00:00Z',
    data_to: '2026-08-21T01:00:00Z',
    generated_at: '2026-08-21T02:00:00Z',
    api_key_id: 1,
    model: 'test-model',
  },
}

describe('SessionSummaryDrawer', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    getRequestLogs.mockReset()
    getSessionSummary.mockReset()
    getRequestLogs.mockResolvedValue({
      items: [{
        request_id: 'req-a',
        ts: '2026-08-21T00:30:00Z',
        success: true,
        canonical_name: 'gpt-test',
        request_preview: 'hello',
      }],
      total: 1,
    })
    getSessionSummary.mockResolvedValue(summaryPayload)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  async function flushDrawerLoad() {
    await flushPromises()
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
  }

  it('loads logs and summary when opened', async () => {
    const wrapper = mount(SessionSummaryDrawer, {
      props: { open: false, sessionId: 'sess-drawer' },
      global: { plugins: [i18n], stubs: { Teleport: true } },
    })
    expect(getRequestLogs).not.toHaveBeenCalled()

    await wrapper.setProps({ open: true })
    await flushDrawerLoad()
    expect(getRequestLogs).toHaveBeenCalledWith({
      gw_session_id: 'sess-drawer',
      chrono: true,
      page: 1,
      page_size: 80,
    })
    expect(getSessionSummary).toHaveBeenCalledWith('sess-drawer')
    expect(wrapper.text()).toContain('独立抽屉摘要正文')
    expect(wrapper.text()).toContain('要点一')
    expect(wrapper.text()).toContain('会话脉络')
  })

  it('emits filterSession and openRequest from toolbar actions', async () => {
    const wrapper = mount(SessionSummaryDrawer, {
      props: { open: true, sessionId: 'sess-drawer', sessionTitle: '测试会话' },
      global: { plugins: [i18n], stubs: { Teleport: true } },
    })
    await flushDrawerLoad()

    const filterBtn = wrapper.findAll('button').find(b => b.text().includes('在请求日志中筛选'))
    expect(filterBtn).toBeTruthy()
    await filterBtn!.trigger('click')
    expect(wrapper.emitted('filterSession')).toEqual([['sess-drawer']])

    const logBtn = wrapper.find('.ssd-log-btn')
    await logBtn.trigger('click')
    expect(wrapper.emitted('openRequest')).toEqual([['req-a']])
  })

  it('resets when closed', async () => {
    const wrapper = mount(SessionSummaryDrawer, {
      props: { open: true, sessionId: 'sess-drawer' },
      global: { plugins: [i18n], stubs: { Teleport: true } },
    })
    await flushDrawerLoad()
    expect(wrapper.text()).toContain('独立抽屉摘要正文')

    await wrapper.setProps({ open: false })
    await flushPromises()
    await wrapper.setProps({ open: true, sessionId: 'sess-other' })
    getSessionSummary.mockResolvedValueOnce({
      ...summaryPayload,
      summary: '另一会话摘要',
      meta: { ...summaryPayload.meta, session_id: 'sess-other' },
    })
    await flushDrawerLoad()
    expect(getSessionSummary).toHaveBeenLastCalledWith('sess-other')
    expect(wrapper.text()).toContain('另一会话摘要')
  })
})
