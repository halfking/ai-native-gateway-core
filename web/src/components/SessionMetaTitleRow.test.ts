import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SessionMetaTitleRow from './SessionMetaTitleRow.vue'

const { getSessionSummary, summarizeSessionTitle } = vi.hoisted(() => ({
  getSessionSummary: vi.fn(),
  summarizeSessionTitle: vi.fn(),
}))

vi.mock('../api/logs', () => ({ getSessionSummary }))
vi.mock('../api/memora', () => ({
  updateSessionTitle: vi.fn(),
  deleteSessionTitle: vi.fn(),
  summarizeSessionTitle,
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      requests: {
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
    },
  },
})

describe('SessionMetaTitleRow', () => {
  beforeEach(() => {
    getSessionSummary.mockReset()
    summarizeSessionTitle.mockReset()
  })

  it('defaults to collapsed title ops and shows 无标题 + 摘要', () => {
    const wrapper = mount(SessionMetaTitleRow, {
      props: { taskId: 'task-1', sessionId: 'sess-1', title: null },
      global: { plugins: [i18n] },
    })
    expect(wrapper.text()).toContain('无标题')
    expect(wrapper.text()).toContain('摘要')
    expect(wrapper.text()).toContain('标题详情')
    expect(wrapper.text()).not.toContain('生成标题')
    expect(wrapper.text()).not.toContain('重新生成')
  })

  it('expands title actions and loads summary inline', async () => {
    getSessionSummary.mockResolvedValue({
      summary: '整段会话摘要正文',
      key_points: ['要点A'],
      meta: {
        session_id: 'sess-1',
        log_count: 3,
        data_from: '2026-08-21T00:00:00Z',
        data_to: '2026-08-21T01:00:00Z',
        generated_at: '2026-08-21T02:00:00Z',
        api_key_id: 1,
        model: 'm',
      },
    })
    const wrapper = mount(SessionMetaTitleRow, {
      props: { taskId: 'task-1', sessionId: 'sess-1', title: null },
      global: { plugins: [i18n] },
    })
    await wrapper.get('button[aria-expanded="false"]').trigger('click')
    expect(wrapper.text()).toContain('生成标题')

    await wrapper.get('button[aria-label="会话摘要"]').trigger('click')
    await flushPromises()
    expect(getSessionSummary).toHaveBeenCalledWith('sess-1')
    expect(wrapper.text()).toContain('整段会话摘要正文')
    expect(wrapper.text()).toContain('要点A')
  })

  it('keeps summary panel open when title prop updates', async () => {
    getSessionSummary.mockResolvedValue({
      summary: '摘要保持可见',
      key_points: [],
      meta: {
        session_id: 'sess-1',
        log_count: 1,
        data_from: '2026-08-21T00:00:00Z',
        data_to: '2026-08-21T01:00:00Z',
        generated_at: '2026-08-21T02:00:00Z',
        api_key_id: 1,
        model: 'm',
      },
    })
    const wrapper = mount(SessionMetaTitleRow, {
      props: { taskId: 'task-1', sessionId: 'sess-1', title: null },
      global: { plugins: [i18n] },
    })
    await wrapper.get('button[aria-label="会话摘要"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('摘要保持可见')

    await wrapper.setProps({ title: '更新后的标题' })
    await flushPromises()
    expect(wrapper.text()).toContain('摘要保持可见')
    expect(wrapper.find('.session-summary-panel').isVisible()).toBe(true)
  })

  it('passes extended hours when generating title', async () => {
    summarizeSessionTitle.mockResolvedValue({ title: '测试标题', meta: {} })
    const wrapper = mount(SessionMetaTitleRow, {
      props: { taskId: 'task-1', sessionId: 'sess-1', title: null },
      global: { plugins: [i18n] },
    })
    await wrapper.get('button[aria-expanded="false"]').trigger('click')
    const genBtn = wrapper.findAll('button').find(b => b.text() === '生成标题')
    expect(genBtn).toBeTruthy()
    await genBtn!.trigger('click')
    await flushPromises()
    expect(summarizeSessionTitle).toHaveBeenCalledWith('task-1', {
      session_id: 'sess-1',
      hours: 168,
    })
  })
})
