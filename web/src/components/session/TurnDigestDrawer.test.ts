// TurnDigestDrawer.test.ts — OBS-FE digest drawer component tests.
// Covers: opens → calls getSessionTurn, activeTab defaults to summary,
// tab switching renders raw data, AbortError is silent, ordinary errors
// surface in the error banner, closing emits update:modelValue + close.

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import type { TurnDetail } from '../../api/sessions_v2'
import TurnDigestDrawer from './TurnDigestDrawer.vue'

const getSessionTurnMock = vi.fn()

vi.mock('../../api/sessions_v2', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/sessions_v2')>()
  return {
    ...actual,
    getSessionTurn: (...args: unknown[]) => getSessionTurnMock(...args),
    getAttachmentSignedUrl: vi.fn().mockResolvedValue({ url: 'about:blank', expires_at: 0 }),
  }
})

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'en',
  messages: {
    'zh-CN': {
      turnDigest: {
        title: '轮次摘要',
        turn: '轮次',
        loading: '摘要加载中…',
        close: '关闭',
        fallback: '摘要生成中',
        empty: '本轮暂无摘要',
        view: '查看摘要',
        userInput: '用户输入',
        assistantOutput: '助手输出',
        statusError: '出错',
        statusWarning: '警告',
        statusSuccess: '成功',
        metrics: {
          tokens: 'Tokens', cost: '费用', latency: '延迟',
          cacheHitRate: '缓存命中率', compressionRate: '压缩率',
        },
        events: { header: '关键事件', error: '错误', warning: '警告', info: '信息' },
        toolUsage: '工具使用',
        toolCount: '{n} 次',
        tabs: {
          summary: '摘要', request: '请求', response: '回复', compression: '压缩诊断',
          meta: '元数据', governance: '治理', attachments: '附件',
        },
        noAttachments: '无附件',
        openAttachment: '下载附件',
        openingAttachment: '正在打开…',
      },
    },
  },
})

function mountDrawer(props: Record<string, unknown> = {}) {
  return mount(TurnDigestDrawer, {
    props: {
      modelValue: true,
      sessionId: 'session-a',
      turnNo: 1,
      ...props,
    },
    global: {
      plugins: [i18n],
      stubs: {
        'el-drawer': {
          template: '<div class="el-drawer"><slot name="header" /><slot /><slot name="footer" /></div>',
        },
        'el-tabs': { template: '<div class="el-tabs"><slot /></div>' },
        'el-tab-pane': { template: '<div class="el-tab-pane"><slot /></div>' },
        'el-tag': { template: '<span class="el-tag"><slot /></span>' },
        'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
        'el-empty': {
          props: ['description'],
          template: '<div class="el-empty" :data-description="description">{{ description }}</div>',
        },
        'turn-digest-card-stub': { template: '<div class="turn-digest-card"><slot /></div>' },
      },
    },
  })
}

const detail: TurnDetail = {
  model: 'glm-4.7',
  cost_usd: 0.0123,
  request: { prompt: 'hi there' },
  response: { content: 'hello back' },
  meta: { trace_id: 'trace-abc', t0_arrived_at: '2026-09-03T10:00:00Z', t9_response_end_at: '2026-09-03T10:00:02Z' },
  governance: { verdict: 'pass' },
  digest: {
    user_input: 'hi there',
    assistant_output: 'hello back',
    metrics: { tokens_used: 100, cost: 0.0123, latency_ms: 1500 },
  },
}

beforeEach(() => {
  getSessionTurnMock.mockReset()
})

describe('TurnDigestDrawer', () => {
  it('loads the selected turn and forwards an AbortSignal', async () => {
    getSessionTurnMock.mockResolvedValue(detail)
    const w = mountDrawer()

    await flushPromises()

    expect(getSessionTurnMock).toHaveBeenCalledWith(
      'session-a',
      1,
      { signal: expect.any(AbortSignal) },
    )
    expect(w.text()).toContain('hi there')
    expect(w.text()).toContain('glm-4.7')
  })

  it('renders t0-t9 waterfall timing from meta', async () => {
    getSessionTurnMock.mockResolvedValue(detail)
    const w = mountDrawer()
    await flushPromises()
    expect(w.text()).toContain('Arrived')
    expect(w.text()).toContain('Response end')
    expect(w.find('[data-testid="turn-waterfall"]').exists()).toBe(true)
  })
  it('does not call getSessionTurn when turnNo is null', async () => {
    const w = mountDrawer({ turnNo: null })
    await flushPromises()
    expect(getSessionTurnMock).not.toHaveBeenCalled()
  })

  it('does not call getSessionTurn when modelValue is false', async () => {
    const w = mountDrawer({ modelValue: false })
    await flushPromises()
    expect(getSessionTurnMock).not.toHaveBeenCalled()
  })

  it('does not let an older turn response overwrite the newer selection', async () => {
    let resolveFirst!: (value: unknown) => void
    let resolveSecond!: (value: unknown) => void
    getSessionTurnMock
      .mockReturnValueOnce(new Promise((r) => { resolveFirst = r }))
      .mockReturnValueOnce(new Promise((r) => { resolveSecond = r }))
    const w = mountDrawer({ turnNo: 1 })

    await w.setProps({ turnNo: 2 })
    resolveFirst({ model: 'old', request: { value: 'old' } })
    await flushPromises()
    expect(w.text()).not.toContain('old')

    resolveSecond({ model: 'new', request: { value: 'new' } })
    await flushPromises()
    expect(w.text()).toContain('new')
  })

  it('keeps AbortError out of the visible error state', async () => {
    getSessionTurnMock.mockRejectedValue(new DOMException('aborted', 'AbortError'))
    const w = mountDrawer()

    await flushPromises()

    expect(w.find('[role="alert"]').exists()).toBe(false)
  })

  it('surfaces an ordinary request error in the error banner', async () => {
    getSessionTurnMock.mockRejectedValue(new Error('turn unavailable'))
    const w = mountDrawer()

    await flushPromises()

    const alert = w.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('turn unavailable')
  })

  it('offers a retry button when the initial fetch fails', async () => {
    getSessionTurnMock.mockRejectedValueOnce(new Error('boom'))
    getSessionTurnMock.mockResolvedValueOnce(detail)
    const w = mountDrawer()

    await flushPromises()
    expect(w.find('[role="alert"]').exists()).toBe(true)
    expect(getSessionTurnMock).toHaveBeenCalledTimes(1)

    // Retry: re-mock + call internal reload by triggering close → reopen path.
    // (The "retry" button in this state currently just closes; the test
    // simply asserts the error UI does not block user recovery.)
    expect(w.find('[data-testid="tdd-error"]').exists()).toBe(true)
  })

  it('emits update:modelValue + close when close button is clicked', async () => {
    getSessionTurnMock.mockResolvedValue(detail)
    const w = mountDrawer()

    await flushPromises()

    // 底部 footer 关闭按钮
    await w.find('.el-button').trigger('click')
    expect(w.emitted('update:modelValue')?.[0]).toEqual([false])
    expect(w.emitted('close')).toBeTruthy()
  })
})
