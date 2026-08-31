// SessionTurnsTimeline.test.ts — OBS-FE5 轮次时间线组件测试。
// 覆盖：轮次渲染、内联子请求树（T/S/SW 徽标 + 状态 + 彩延迟）、latency null
// 不冒充 0、游标加载更多、404/403/网络错误态、空态。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import SessionTurnsTimeline from './SessionTurnsTimeline.vue'
import {
  SessionObsApiError,
  type SessionTurnsTreeResponse,
} from '../../api/sessionTurnsTree'

const fetchSessionTurnsTreeMock = vi.fn()

vi.mock('../../api/sessionTurnsTree', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('../../api/sessionTurnsTree')>()
  return {
    ...actual,
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

function page(over: Partial<SessionTurnsTreeResponse> = {}): SessionTurnsTreeResponse {
  return {
    session_id: 'sess-aaa',
    turns: [
      {
        turn_number: 1,
        request_id: 'req-0001',
        status: 'success',
        model: 'glm-4.7',
        latency: 1500,
        child_requests: [
          { request_id: 'req-0001-t', request_type: 'title', status: 'success', latency: 300 },
          { request_id: 'req-0001-s', request_type: 'summary', status: 'success', latency: null },
          {
            request_id: 'req-0001-sw',
            request_type: 'sensitive_word',
            status: 'error',
            latency: 12000,
          },
        ],
      },
      {
        turn_number: 2,
        request_id: 'req-0002',
        status: 'error',
        latency: null,
        child_requests: [],
      },
    ],
    count: 2,
    has_more: false,
    next_cursor: '',
    ...over,
  }
}

function mountTimeline(sessionId = 'sess-aaa') {
  return mount(SessionTurnsTimeline, {
    props: { sessionId },
    global: { plugins: [i18n] },
  })
}

beforeEach(() => {
  fetchSessionTurnsTreeMock.mockReset()
})

describe('SessionTurnsTimeline', () => {
  it('renders main turn cards with number/status/model/latency', async () => {
    fetchSessionTurnsTreeMock.mockResolvedValue(page())
    const w = mountTimeline()
    await flushPromises()

    expect(fetchSessionTurnsTreeMock).toHaveBeenCalledWith('sess-aaa', {
      limit: 20,
      cursor: undefined,
    })
    const turn1 = w.find('[data-turn-number="1"]')
    expect(turn1.exists()).toBe(true)
    expect(turn1.text()).toContain('#1')
    expect(turn1.text()).toContain('req-0001')
    expect(turn1.text()).toContain('glm-4.7')
    expect(turn1.text()).toContain('1.50s')

    const turn2 = w.find('[data-turn-number="2"]')
    // 主请求 latency null → 「未知」，绝不渲染 0ms
    expect(turn2.find('.stt-turn-latency').text()).toBe('未知')
  })

  it('renders the inline child-request tree with type badges, status and colored latency', async () => {
    fetchSessionTurnsTreeMock.mockResolvedValue(page())
    const w = mountTimeline()
    await flushPromises()

    const children = w.find('[data-child-count="3"]')
    expect(children.exists()).toBe(true)

    // 子请求类型徽标缩写：T / S / SW
    expect(children.text()).toContain('T')
    expect(children.text()).toContain('S')
    expect(children.text()).toContain('SW')
    // title 提示保留全名
    expect(children.find('.stt-child-type').attributes('title')).toBe('title')
    // 状态与延迟
    expect(children.text()).toContain('300ms')
    expect(children.text()).toContain('error')

    // 延迟分级着色：300ms → ok，null → unknown（文本=「未知」），12s → bad
    const latencies = children.findAll('.stt-turn-latency')
    expect(latencies[0].classes()).toContain('stt-latency--ok')
    expect(latencies[0].text()).toBe('300ms')
    expect(latencies[1].classes()).toContain('stt-latency--unknown')
    expect(latencies[1].text()).toBe('未知')
    expect(latencies[2].classes()).toContain('stt-latency--bad')
    expect(latencies[2].text()).toBe('12.00s')
  })

  it('loads more turns with the cursor when has_more', async () => {
    fetchSessionTurnsTreeMock.mockResolvedValueOnce(
      page({ has_more: true, next_cursor: 'turns-cur-1' })
    )
    fetchSessionTurnsTreeMock.mockResolvedValueOnce(
      page({
        turns: [
          {
            turn_number: 3,
            request_id: 'req-0003',
            status: 'success',
            latency: 900,
            child_requests: [],
          },
        ],
        count: 1,
        has_more: false,
        next_cursor: '',
      })
    )
    const w = mountTimeline()
    await flushPromises()

    await w.find('[data-testid="stt-load-more"]').trigger('click')
    expect(fetchSessionTurnsTreeMock).toHaveBeenLastCalledWith('sess-aaa', {
      limit: 20,
      cursor: 'turns-cur-1',
    })
    await flushPromises()

    expect(w.find('[data-turn-number="3"]').exists()).toBe(true)
    expect(w.find('[data-testid="stt-load-more"]').exists()).toBe(false)
    expect(w.text()).toContain('已全部加载')
  })

  it('distinguishes 404 / 403 / network error states', async () => {
    fetchSessionTurnsTreeMock.mockRejectedValue(
      new SessionObsApiError('not_found', 404, 'session not found')
    )
    let w = mountTimeline('sess-missing')
    await flushPromises()
    expect(w.find('[data-error-kind="not_found"]').text()).toContain('会话不存在')
    w.unmount()

    fetchSessionTurnsTreeMock.mockRejectedValue(
      new SessionObsApiError('forbidden', 403, 'session belongs to another tenant')
    )
    w = mountTimeline('sess-other-tenant')
    await flushPromises()
    expect(w.find('[data-error-kind="forbidden"]').text()).toContain('无权限访问该会话')
    w.unmount()

    fetchSessionTurnsTreeMock.mockRejectedValue(new SessionObsApiError('network', 0, 'boom'))
    w = mountTimeline()
    await flushPromises()
    expect(w.find('[data-error-kind="network"]').text()).toContain('网络错误')

    // 重试按钮存在
    expect(w.find('.stt-retry').exists()).toBe(true)
  })

  it('shows a skeleton on first load and an empty state for zero turns', async () => {
    fetchSessionTurnsTreeMock.mockReturnValue(new Promise(() => {}))
    const w = mountTimeline()
    await flushPromises() // onMounted 设置 loading 后需要一次渲染 tick
    expect(w.find('[data-testid="stt-skeleton"]').exists()).toBe(true)
    w.unmount()

    fetchSessionTurnsTreeMock.mockResolvedValue(
      page({ turns: [], count: 0, has_more: false, next_cursor: '' })
    )
    const w2 = mountTimeline()
    await flushPromises()
    expect(w2.text()).toContain('该会话暂无轮次记录')
  })
})
