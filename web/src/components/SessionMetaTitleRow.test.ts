import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SessionMetaTitleRow from './SessionMetaTitleRow.vue'

const { summarizeSessionTitle } = vi.hoisted(() => ({
  summarizeSessionTitle: vi.fn(),
}))

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
          },
        },
      },
    },
  },
})

describe('SessionMetaTitleRow', () => {
  beforeEach(() => {
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

  it('emits openSummary when clicking 摘要 button', async () => {
    const wrapper = mount(SessionMetaTitleRow, {
      props: { taskId: 'task-1', sessionId: 'sess-1', title: null },
      global: { plugins: [i18n] },
    })
    await wrapper.get('button[aria-label="会话摘要"]').trigger('click')
    expect(wrapper.emitted('openSummary')).toEqual([['sess-1']])
  })

  it('does not emit openSummary without sessionId', async () => {
    const wrapper = mount(SessionMetaTitleRow, {
      props: { taskId: 'task-1', sessionId: null, title: null },
      global: { plugins: [i18n] },
    })
    expect(wrapper.find('button[aria-label="会话摘要"]').exists()).toBe(false)
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
