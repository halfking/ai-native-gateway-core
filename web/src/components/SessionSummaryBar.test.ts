import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, afterEach, vi } from 'vitest'
import SessionSummaryBar from './SessionSummaryBar.vue'

const triggerInstantSummaryMock = vi.fn()
const getSessionSnapshotMock = vi.fn()

vi.mock('../api/sessions_v2', () => ({
  triggerInstantSummary: (...args: unknown[]) => triggerInstantSummaryMock(...args),
  getSessionSnapshot: (...args: unknown[]) => getSessionSnapshotMock(...args),
}))

function mountBar(props: Record<string, unknown> = {}) {
  return mount(SessionSummaryBar, {
    props: { sessionId: 'session-a', ...props },
    global: {
      stubs: {
        'el-button': {
          props: ['loading', 'disabled'],
          template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
        },
      },
    },
  })
}

describe('SessionSummaryBar', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    triggerInstantSummaryMock.mockReset()
    getSessionSnapshotMock.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('emits an inline summary result without polling', async () => {
    const result = { summary: '即时摘要', summary_generated_at: '2026-08-21T00:00:00Z' }
    triggerInstantSummaryMock.mockResolvedValue(result)
    const wrapper = mountBar()

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(triggerInstantSummaryMock).toHaveBeenCalledWith('session-a')
    expect(getSessionSnapshotMock).not.toHaveBeenCalled()
    expect(wrapper.emitted('summary-updated')).toEqual([[result]])
    expect(wrapper.text()).toContain('已总结')
  })

  it('ignores a completed trigger from a previous session', async () => {
    let resolveTrigger!: (value: unknown) => void
    triggerInstantSummaryMock.mockReturnValue(new Promise(resolve => { resolveTrigger = resolve }))
    const wrapper = mountBar()

    await wrapper.get('button').trigger('click')
    await wrapper.setProps({ sessionId: 'session-b' })
    resolveTrigger({ summary: '过期摘要' })
    await flushPromises()

    expect(wrapper.emitted('summary-updated')).toBeUndefined()
    expect(wrapper.text()).not.toContain('已总结')
    expect(wrapper.text()).not.toContain('过期摘要')
  })

  it('shows trigger errors for the current generation', async () => {
    triggerInstantSummaryMock.mockRejectedValue(new Error('summary unavailable'))
    const wrapper = mountBar()

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('summary unavailable')
  })

  // ── sessions.duration_ms 的消费点 ──────────────────────────────────
  //
  // 这条断言守的是"877 修好的 duration_ms 真的有用户可见收益"：
  // 之前它只写不读，审计只能记成"诚实，但没有用户可见收益"。
  it('shows the elapsed duration from sessions.duration_ms', () => {
    const wrapper = mountBar({ durationMs: 90000 })

    expect(wrapper.text()).toContain('1m30s')
  })

  it('formats sub-minute durations in seconds and milliseconds', () => {
    expect(mountBar({ durationMs: 250 }).text()).toContain('250ms')
    expect(mountBar({ durationMs: 4200 }).text()).toContain('4s')
    expect(mountBar({ durationMs: 3_600_000 }).text()).toContain('1h')
  })

  // ★ 缺失 ≠ 0ms。0 是"瞬时完成"，是后端给的**真实值**；
  //   undefined 是"后端没给"。两者不能渲染成同一个东西。
  it('does not render a duration when the backend did not provide one', () => {
    const wrapper = mountBar()

    expect(wrapper.text()).not.toContain('0ms')
  })

  it('renders a genuine zero duration as 0ms', () => {
    const wrapper = mountBar({ durationMs: 0 })

    expect(wrapper.text()).toContain('0ms')
  })

  it('polls until a newer snapshot is returned', async () => {
    triggerInstantSummaryMock.mockResolvedValue({})
    const result = { summary: '轮询摘要', summary_generated_at: '2026-08-21T00:00:01Z' }
    getSessionSnapshotMock.mockResolvedValue(result)
    const wrapper = mountBar({ summaryGeneratedAt: '2026-08-21T00:00:00Z' })

    await wrapper.get('button').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(getSessionSnapshotMock).toHaveBeenCalledWith('session-a')
    expect(wrapper.emitted('summary-updated')).toEqual([[result]])
  })
})
