// SessionDrilldownPanel.test.ts — OBS-FE5 下钻容器测试。
// 覆盖：列表 → 点击会话进入轮次时间线 → 返回列表的完整下钻链路。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import SessionDrilldownPanel from './SessionDrilldownPanel.vue'
import {
  SessionObsApiError,
  type OnlineSessionsResponse,
  type SessionTurnsTreeResponse,
} from '../../api/sessionTurnsTree'

const fetchOnlineSessionsMock = vi.fn()
const fetchSessionTurnsTreeMock = vi.fn()
const listCatalogSessionsMock = vi.fn()

vi.mock('../../api/sessionTurnsTree', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('../../api/sessionTurnsTree')>()
  return {
    ...actual,
    fetchOnlineSessions: (...args: unknown[]) => fetchOnlineSessionsMock(...args),
    fetchSessionTurnsTree: (...args: unknown[]) => fetchSessionTurnsTreeMock(...args),
  }
})

vi.mock('../../api/session', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/session')>()
  return {
    ...actual,
    listCatalogSessions: (...args: unknown[]) => listCatalogSessionsMock(...args),
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
      sessions: {
        catalog: {
          online: '在线',
          directory: '目录',
          backToList: '返回会话列表',
          searchPlaceholder: '搜索',
          searchHint: '提示',
          loading: '加载中',
          refresh: '刷新',
          retry: '重试',
          empty: '没有匹配的会话',
          colTitle: '标题',
          colStatus: '状态',
          colModel: '模型',
          colTurns: '轮次',
          colTokens: 'Token',
          colCost: '费用',
          colHealth: '健康',
          colLastActive: '最近活动',
          status: { all: '全部', active: '活跃', stopped: '已停止' },
        },
      },
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

async function mountPanel(query: Record<string, string> = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }],
  })
  await router.push({ path: '/', query })
  await router.isReady()
  const w = mount(SessionDrilldownPanel, { global: { plugins: [i18n, router] } })
  return { w, router }
}

beforeEach(() => {
  fetchOnlineSessionsMock.mockReset()
  fetchSessionTurnsTreeMock.mockReset()
  listCatalogSessionsMock.mockReset()
  listCatalogSessionsMock.mockResolvedValue({ sessions: [], total: 0 })
})

describe('SessionDrilldownPanel', () => {
  it('drills from the online list into the turns timeline and back', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(onlinePage)
    fetchSessionTurnsTreeMock.mockResolvedValue(turnsPage)

    const { w, router } = await mountPanel({ tab: 'stats' })
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
    expect(router.currentRoute.value.query.session).toBe('sess-drill')

    // 返回列表
    await w.find('.sdp-back').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="online-sessions-panel"]').exists()).toBe(true)
    expect(w.find('[data-testid="session-turns-timeline"]').exists()).toBe(false)
    expect(router.currentRoute.value.query.session).toBeUndefined()
  })

  it('keeps the timeline error state visible when the session has no records (404)', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(onlinePage)
    fetchSessionTurnsTreeMock.mockRejectedValue(
      new SessionObsApiError('not_found', 404, 'session not found')
    )

    const { w } = await mountPanel()
    await flushPromises()
    await w.find('[data-session-id="sess-drill"]').trigger('click')
    await flushPromises()

    expect(w.find('[data-error-kind="not_found"]').exists()).toBe(true)
    expect(w.text()).toContain('会话不存在')
  })

  it('opens a catalog session from the URL and returns to the directory', async () => {
    listCatalogSessionsMock.mockResolvedValue({
      sessions: [{
        session_id: 'sess-cat',
        title: '目录会话',
        status: 'stopped',
        current_model: 'glm-4.7',
        total_turns: 3,
        total_prompt_tokens: 10,
        total_completion_tokens: 20,
        total_cost_usd: 0.01,
        tags: '',
        tenant_id: 'default',
        last_active: '2026-09-30T00:00:00Z',
        last_request_at: '2026-09-30T00:00:00Z',
      }],
      total: 1,
    })
    fetchSessionTurnsTreeMock.mockResolvedValue({ ...turnsPage, session_id: 'sess-cat' })

    const { w, router } = await mountPanel({ tab: 'stats', slist: 'catalog', session: 'sess-cat' })
    await flushPromises()

    expect(w.find('[data-testid="session-turns-timeline"]').exists()).toBe(true)
    expect(fetchSessionTurnsTreeMock).toHaveBeenCalledWith('sess-cat', {
      limit: 20,
      cursor: undefined,
    })

    await w.find('.sdp-back').trigger('click')
    await flushPromises()

    expect(w.find('[data-testid="session-catalog-panel"]').exists()).toBe(true)
    expect(w.text()).toContain('目录会话')
    expect(router.currentRoute.value.query.slist).toBe('catalog')
    expect(router.currentRoute.value.query.session).toBeUndefined()
    expect(router.currentRoute.value.query.tab).toBe('stats')
  })
})
