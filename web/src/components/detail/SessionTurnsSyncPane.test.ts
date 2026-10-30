// SessionTurnsSyncPane.test.ts — consent-off / bodies-error states（24h 审计第 9 轮批判式审计补）
//
// 覆盖缺口：本组件在 W1.3 consent 收口轮（第 7/9 轮）新增了 v2ConsentOff
// 分支，但**此前从未有过组件测试**——「分支存在」不等于「分支工作」。
// 本文件钉三件事：
//   ① bodies 409 recording_unavailable ⇒ 轮次视图渲染「录制未开启」提示
//      （不冒充「没有轮次」的 generic empty）；
//   ② bodies 其它错误 ⇒ 不渲染 consent 提示（判码不许吞掉真错误）；
//   ③ consent-off 但 V1 派生轮次有内容 ⇒ 列表照常渲染（提示是注记不是替代）。
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zhCN from '../../locales/zh-CN'
import { ApiError } from '../../api/_core'
import SessionTurnsSyncPane from './SessionTurnsSyncPane.vue'

const fetchSessionTurnsTreeMock = vi.fn()
const fetchSessionTurnsBodiesMock = vi.fn()

vi.mock('../../api/sessionTurnsTree', () => ({
  fetchSessionTurnsTree: (...args: unknown[]) => fetchSessionTurnsTreeMock(...args),
}))
vi.mock('../../api/sessions_v2', async (importOriginal) => ({
  // isRecordingUnavailable 走真实现：判码必须与生产同一份口径。
  ...(await importOriginal<typeof import('../../api/sessions_v2')>()),
  fetchSessionTurnsBodies: (...args: unknown[]) => fetchSessionTurnsBodiesMock(...args),
}))
vi.mock('../../store', () => ({
  store: { userInfo: null, apiKey: '', jwtToken: '', locale: 'zh-CN' },
  getCurrentTenantId: () => 'default',
  clearApiKey: () => undefined,
  clearAll: () => undefined,
  authBearer: () => '',
  getLocale: () => 'zh-CN',
  isAuthenticated: () => false,
  isSuperAdmin: () => false,
}))

// 挂真实 zh-CN 语言包（LoginModal.test.ts 先例）：断言「用户真正看到的字」。
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN as any },
})

const TREE_ROW = {
  turn_number: 1,
  request_id: 'req-1',
  status: 'success',
  latency: 100,
  child_requests: [],
}

function mountPane(props: Record<string, unknown> = {}) {
  return mount(SessionTurnsSyncPane, {
    props: {
      sessionId: 'sess-1',
      activeRequestId: null,
      requestBody: null,
      responseBody: null,
      ...props,
    },
    global: {
      plugins: [i18n],
      stubs: {
        Teleport: true,
        // 被测的是左栏轮次列表的空/提示态；右侧消息面板与抽屉不参与断言。
        ConversationMessagesPanel: { template: '<div class="cmp-stub" />' },
        TurnDigestDrawer: { template: '<div class="tdd-stub" />' },
      },
    },
  })
}

describe('SessionTurnsSyncPane consent-off states', () => {
  beforeEach(() => {
    fetchSessionTurnsTreeMock.mockReset()
    fetchSessionTurnsBodiesMock.mockReset()
    fetchSessionTurnsTreeMock.mockResolvedValue({
      session_id: 'sess-1',
      turns: [TREE_ROW],
      count: 1,
      has_more: false,
      next_cursor: '',
    })
  })

  it('shows the consent-off note when bodies answers 409 recording_unavailable', async () => {
    fetchSessionTurnsBodiesMock.mockRejectedValue(
      new ApiError(409, 'session body recording is off for this tenant', 'recording_unavailable'),
    )
    const w = mountPane()
    await flushPromises()

    const note = w.find('[data-testid="sync-pane-consent-off"]')
    expect(note.exists()).toBe(true)
    expect(note.text()).toContain('会话正文录制未开启')
    // 英文后端原文不许上屏。
    expect(note.text()).not.toContain('session body recording')
    // V1 派生轮次为空（requestBody=null）⇒ 提示替代 generic empty，
    // 「录制没开」≠「没有轮次」。
    expect(w.text()).not.toContain('暂无轮次')
  })

  it('keeps ordinary bodies errors out of the consent-off branch', async () => {
    fetchSessionTurnsBodiesMock.mockRejectedValue(new ApiError(503, 'db degraded', 'storage_degraded'))
    const w = mountPane()
    await flushPromises()

    expect(w.find('[data-testid="sync-pane-consent-off"]').exists()).toBe(false)
  })

  it('renders derived turns alongside the consent-off note when V1 body exists', async () => {
    fetchSessionTurnsBodiesMock.mockRejectedValue(
      new ApiError(409, 'session body recording is off', 'recording_unavailable'),
    )
    const w = mountPane({
      requestBody: { messages: [{ role: 'user', content: '帮我查一下订单' }] },
    })
    await flushPromises()

    // 提示是注记，不是替代：V1 派生轮次照常渲染。
    expect(w.find('[data-testid="sync-pane-consent-off"]').exists()).toBe(true)
    expect(w.text()).toContain('帮我查一下订单')
  })
})
