import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import SessionCatalogPanel from './SessionCatalogPanel.vue'

const listCatalogSessionsMock = vi.fn()

vi.mock('../../api/session', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/session')>()
  return {
    ...actual,
    listCatalogSessions: (...args: unknown[]) => listCatalogSessionsMock(...args),
  }
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      sessions: {
        catalog: {
          searchPlaceholder: '搜索',
          searchHint: '只过滤已加载结果',
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

const rows = {
  sessions: [
    {
      session_id: 'sess-keep',
      title: '账单核对',
      status: 'active',
      current_model: 'glm-4.7',
      total_turns: 4,
      total_prompt_tokens: 100,
      total_completion_tokens: 40,
      total_cost_usd: 0.125,
      tags: 'billing',
      tenant_id: 'default',
      health_grade: 'B',
      last_active: '2026-09-30T01:00:00Z',
      last_request_at: '2026-09-30T01:00:00Z',
    },
    {
      session_id: 'sess-drop',
      title: '无关会话',
      status: 'stopped',
      current_model: 'other',
      total_turns: 1,
      total_prompt_tokens: 1,
      total_completion_tokens: 1,
      total_cost_usd: 0,
      tags: '',
      tenant_id: 'default',
      last_active: '2026-09-29T01:00:00Z',
      last_request_at: '2026-09-29T01:00:00Z',
    },
  ],
  total: 2,
}

beforeEach(() => {
  vi.useFakeTimers()
  listCatalogSessionsMock.mockReset()
  listCatalogSessionsMock.mockResolvedValue(rows)
})

afterEach(() => {
  vi.useRealTimers()
})

function mountPanel() {
  return mount(SessionCatalogPanel, { global: { plugins: [i18n] } })
}

describe('SessionCatalogPanel', () => {
  it('loads every status and filters the loaded rows by title', async () => {
    const w = mountPanel()
    await flushPromises()

    expect(listCatalogSessionsMock).toHaveBeenCalledWith({ status: 'all', limit: 200, q: undefined })
    expect(w.text()).toContain('账单核对')
    expect(w.text()).toContain('无关会话')
    expect(w.text()).toContain('B')

    await w.get('[data-testid="catalog-search"]').setValue('账单')
    expect(w.text()).toContain('账单核对')
    expect(w.text()).not.toContain('无关会话')

    await vi.advanceTimersByTimeAsync(300)
    expect(listCatalogSessionsMock).toHaveBeenLastCalledWith({ status: 'all', limit: 200, q: '账单' })
  })

  it('refetches when the status chip changes and emits the selected session', async () => {
    const w = mountPanel()
    await flushPromises()

    await w.get('[data-status="stopped"]').trigger('click')
    await flushPromises()
    expect(listCatalogSessionsMock).toHaveBeenLastCalledWith({ status: 'stopped', limit: 200, q: undefined })

    await w.get('[data-session-id="sess-keep"]').trigger('click')
    expect(w.emitted('select')?.[0]).toEqual(['sess-keep'])
  })

  it('shows the empty state when the filter matches nothing', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.get('[data-testid="catalog-search"]').setValue('不存在的标题')
    expect(w.get('[data-testid="catalog-empty"]').text()).toContain('没有匹配的会话')
  })
})
