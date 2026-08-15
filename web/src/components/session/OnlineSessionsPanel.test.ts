// OnlineSessionsPanel.test.ts — OBS-FE5 在线会话列表组件测试。
// 覆盖：列表渲染、latency null 不冒充 0、Skeleton、空态、403/网络错误态、
// 游标加载更多、select 事件（下钻入口）。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OnlineSessionsPanel from './OnlineSessionsPanel.vue'
import {
  SessionObsApiError,
  type OnlineSessionsResponse,
} from '../../api/sessionTurnsTree'

const fetchOnlineSessionsMock = vi.fn()

vi.mock('../../api/sessionTurnsTree', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('../../api/sessionTurnsTree')>()
  return {
    ...actual,
    fetchOnlineSessions: (...args: unknown[]) => fetchOnlineSessionsMock(...args),
  }
})

function page(over: Partial<OnlineSessionsResponse> = {}): OnlineSessionsResponse {
  return {
    sessions: [
      {
        session_id: 'sess-aaa',
        title: '调试会话',
        last_request_status: 'success',
        last_model: 'glm-4.7',
        last_latency_ms: 820,
        last_active_at: '2026-08-15T08:00:00Z',
        freshness: { data_source: 'hot', freshness_ms: 100, stale: false },
      },
      {
        session_id: 'sess-bbb',
        last_request_status: 'error',
        last_latency_ms: null, // 未知
        freshness: { data_source: 'hot', freshness_ms: 999999, stale: true },
      },
    ],
    count: 2,
    has_more: false,
    next_cursor: '',
    ...over,
  }
}

function mountPanel() {
  return mount(OnlineSessionsPanel)
}

beforeEach(() => {
  fetchOnlineSessionsMock.mockReset()
})

describe('OnlineSessionsPanel', () => {
  it('renders session rows with key/title/status/model/latency', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(page())
    const w = mountPanel()
    await flushPromises()

    expect(fetchOnlineSessionsMock).toHaveBeenCalledWith({ limit: 20, cursor: undefined })
    expect(w.text()).toContain('sess-aaa')
    expect(w.text()).toContain('调试会话')
    expect(w.text()).toContain('glm-4.7')
    expect(w.text()).toContain('820ms')
    expect(w.text()).toContain('success')
    expect(w.text()).toContain('sess-bbb')
    // 未上线字段不渲染：用户 / 轮次数 / 健康度列不存在
    expect(w.text()).not.toContain('健康度')
    expect(w.text()).not.toContain('用户')
    expect(w.text()).not.toContain('轮次数')
  })

  it('renders unknown latency as a dash, never 0ms', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(page())
    const w = mountPanel()
    await flushPromises()

    const row = w.find('[data-session-id="sess-bbb"]')
    expect(row.exists()).toBe(true)
    expect(row.text()).toContain('—')
    expect(row.text()).not.toContain('0ms')
    // freshness.stale → 过期标记
    expect(row.text()).toContain('过期')
  })

  it('shows a skeleton while the first page is loading', async () => {
    fetchOnlineSessionsMock.mockReturnValue(new Promise(() => {})) // pending
    const w = mountPanel()
    await flushPromises() // onMounted 设置 loading 后需要一次渲染 tick
    expect(w.find('[data-testid="osp-skeleton"]').exists()).toBe(true)
  })

  it('shows an empty state when there are no online sessions', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(
      page({ sessions: [], count: 0, has_more: false })
    )
    const w = mountPanel()
    await flushPromises()
    expect(w.text()).toContain('当前没有在线会话')
  })

  it('shows a tenant-forbidden (403) error distinct from a network error', async () => {
    fetchOnlineSessionsMock.mockRejectedValue(
      new SessionObsApiError('forbidden', 403, 'session belongs to another tenant')
    )
    const w = mountPanel()
    await flushPromises()

    const err = w.find('[data-error-kind="forbidden"]')
    expect(err.exists()).toBe(true)
    expect(err.text()).toContain('无权限访问在线会话列表')

    fetchOnlineSessionsMock.mockRejectedValue(new SessionObsApiError('network', 0, 'boom'))
    await w.find('.osp-retry').trigger('click')
    await flushPromises()
    const netErr = w.find('[data-error-kind="network"]')
    expect(netErr.exists()).toBe(true)
    expect(netErr.text()).toContain('网络错误')
  })

  it('loads the next page with the cursor when clicking load more', async () => {
    fetchOnlineSessionsMock.mockResolvedValueOnce(
      page({ has_more: true, next_cursor: 'cur-1' })
    )
    fetchOnlineSessionsMock.mockResolvedValueOnce(
      page({
        sessions: [
          { session_id: 'sess-ccc', last_request_status: 'success' },
        ],
        count: 1,
        has_more: false,
        next_cursor: '',
      })
    )
    const w = mountPanel()
    await flushPromises()

    await w.find('[data-testid="osp-load-more"]').trigger('click')
    expect(fetchOnlineSessionsMock).toHaveBeenLastCalledWith({
      limit: 20,
      cursor: 'cur-1',
    })
    await flushPromises()

    expect(w.text()).toContain('sess-ccc')
    // 没有更多页时加载更多按钮消失
    expect(w.find('[data-testid="osp-load-more"]').exists()).toBe(false)
  })

  it('emits select with the session id when a row is clicked (drill-down entry)', async () => {
    fetchOnlineSessionsMock.mockResolvedValue(page())
    const w = mountPanel()
    await flushPromises()

    await w.find('[data-session-id="sess-aaa"]').trigger('click')
    expect(w.emitted('select')?.[0]).toEqual(['sess-aaa'])
  })
})
