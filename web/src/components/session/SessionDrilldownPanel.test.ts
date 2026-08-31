// SessionDrilldownPanel.test.ts — OBS-FE5 下钻容器测试。
// 覆盖：列表 → 点击会话进入轮次时间线 → 返回列表的完整下钻链路。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import SessionDrilldownPanel from './SessionDrilldownPanel.vue'
import {
  SessionObsApiError,
  type OnlineSessionsResponse,
  type SessionTurnsTreeResponse,
} from '../../api/sessionTurnsTree'

const fetchOnlineSessionsMock = vi.fn()
const fetchSessionTurnsTreeMock = vi.fn()

vi.mock('../../api/sessionTurnsTree', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('../../api/sessionTurnsTree')>()
  return {
    ...actual,
    fetchOnlineSessions: (...args: unknown[]) => fetchOnlineSessionsMock(...args),
    fetchSessionTurnsTree: (...args: unknown[]) => fetchSessionTurnsTreeMock(...args),
  }
})

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'en',
  messages: {
    'zh-CN': {
      turnDigest: { view: '查看摘要' },
    },
  },
})

const onlinePage: OnlineSessionsResponse = {
  sessions: [
    {
      session_id: 'sess-drill',
      title: '下钻会话',
      last_request_status: 'success',
      last_model: 'glm-4.7',
      last_latency_ms: 500,
    },
  ],
  count: 1,
  has_more: false,
  next_cursor: '',
}

const turnsPage: SessionTurnsTreeResponse = {
  session_id: 'sess-drill',
  turns: [
    {
      turn_number: 1,
      request_id: 'req-d1',
      status: 'success',
      model: 'glm-4.7',
      latency: 700,
      child_requests: [
        { request_id: 'req-d1-t', request_type: 'title', status: 'success', latency: 120 },
      ],
    },
  ],
  count: 1,
  has_more: false,
  next_cursor: '',
}

beforeEach(() => {
  fetchOnlineSessionsMock.mockReset()
  fetchSessionTurnsTreeMock.mockReset()
})

describe('SessionDrilldownPanel', () => {
  it('drills from the online list into the turns timeline and back', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(onlinePage)
    fetchSessionTurnsTreeMock.mockResolvedValue(turnsPage)

    const w = mount(SessionDrilldownPanel, { global: { plugins: [i18n] } })
    await flushPromises()

    // 初始：在线会话列表
    expect(w.find('[data-testid="online-sessions-panel"]').exists()).toBe(true)
    expect(w.text()).toContain('sess-drill')
    // 未下钻时不请求轮次
    expect(fetchSessionTurnsTreeMock).not.toHaveBeenCalled()

    // 点击会话行 → 轮次时间线
    await w.find('[data-session-id="sess-drill"]').trigger('click')
    await flushPromises()

    expect(w.find('[data-testid="session-turns-timeline"]').exists()).toBe(true)
    expect(fetchSessionTurnsTreeMock).toHaveBeenCalledWith('sess-drill', {
      limit: 20,
      cursor: undefined,
    })
    expect(w.text()).toContain('#1')
    // 内联子请求树渲染
    expect(w.text()).toContain('T')

    // 返回列表
    await w.find('.sdp-back').trigger('click')
    expect(w.find('[data-testid="online-sessions-panel"]').exists()).toBe(true)
    expect(w.find('[data-testid="session-turns-timeline"]').exists()).toBe(false)
  })

  it('keeps the timeline error state visible when the session has no records (404)', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(onlinePage)
    fetchSessionTurnsTreeMock.mockRejectedValue(
      new SessionObsApiError('not_found', 404, 'session not found')
    )

    const w = mount(SessionDrilldownPanel, { global: { plugins: [i18n] } })
    await flushPromises()
    await w.find('[data-session-id="sess-drill"]').trigger('click')
    await flushPromises()

    expect(w.find('[data-error-kind="not_found"]').exists()).toBe(true)
    expect(w.text()).toContain('会话不存在')
  })
})
