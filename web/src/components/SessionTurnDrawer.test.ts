import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SessionTurnDrawer from './SessionTurnDrawer.vue'

const getSessionTurnMock = vi.fn()

vi.mock('../api/sessions_v2', () => ({
  getSessionTurn: (...args: unknown[]) => getSessionTurnMock(...args),
}))

function mountDrawer(props: Record<string, unknown> = {}) {
  return mount(SessionTurnDrawer, {
    props: { sessionId: 'session-a', turnNo: 1, ...props },
    global: {
      stubs: {
        'el-drawer': { template: '<div><slot name="header" /><slot /></div>' },
        'el-tabs': { template: '<div><slot /></div>' },
        'el-tab-pane': { template: '<div><slot /></div>' },
        'el-empty': { template: '<div />' },
      },
    },
  })
}

describe('SessionTurnDrawer', () => {
  beforeEach(() => {
    getSessionTurnMock.mockReset()
  })

  it('loads the selected turn and forwards an AbortSignal', async () => {
    const detail = { model: 'model-a', request: { prompt: 'hello' } }
    getSessionTurnMock.mockResolvedValue(detail)
    const wrapper = mountDrawer()

    await flushPromises()

    expect(getSessionTurnMock).toHaveBeenCalledWith(
      'session-a',
      1,
      { signal: expect.any(AbortSignal) },
    )
    expect(wrapper.text()).toContain('hello')
    expect(wrapper.text()).toContain('model-a')
  })

  it('does not let an older turn response overwrite the newer selection', async () => {
    let resolveFirst!: (value: unknown) => void
    let resolveSecond!: (value: unknown) => void
    getSessionTurnMock
      .mockReturnValueOnce(new Promise(resolve => { resolveFirst = resolve }))
      .mockReturnValueOnce(new Promise(resolve => { resolveSecond = resolve }))
    const wrapper = mountDrawer()

    await wrapper.setProps({ turnNo: 2 })
    resolveFirst({ model: 'old', request: { value: 'old' } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('old')

    resolveSecond({ model: 'new', request: { value: 'new' } })
    await flushPromises()
    expect(wrapper.text()).toContain('new')
    expect(wrapper.text()).not.toContain('old')
  })

  it('keeps AbortError out of the visible error state', async () => {
    const abortError = new DOMException('aborted', 'AbortError')
    getSessionTurnMock.mockRejectedValue(abortError)
    const wrapper = mountDrawer()

    await flushPromises()

    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('无数据')
  })

  it('shows an ordinary request error', async () => {
    getSessionTurnMock.mockRejectedValue(new Error('turn unavailable'))
    const wrapper = mountDrawer()

    await flushPromises()

    expect(wrapper.find('[role="alert"]').text()).toContain('turn unavailable')
  })
})
