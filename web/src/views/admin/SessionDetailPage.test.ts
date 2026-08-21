import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SessionDetailPage from './SessionDetailPage.vue'

const listSessionTurnsMock = vi.fn()
const getSessionSnapshotMock = vi.fn()
const routeState = reactive<{ params: Record<string, string>; query: Record<string, string> }>({
  params: { id: 'session-a' },
  query: {},
})
const routerMock = { replace: vi.fn() }

vi.mock('../../api/sessions_v2', () => ({
  listSessionTurns: (...args: unknown[]) => listSessionTurnsMock(...args),
  getSessionSnapshot: (...args: unknown[]) => getSessionSnapshotMock(...args),
}))
vi.mock('../../api/_core', () => ({
  ApiError: class ApiError extends Error {
    status: number
    detail: string
    constructor(status: number, detail: string) {
      super(detail)
      this.status = status
      this.detail = detail
    }
  },
}))
vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => routerMock,
}))

const turn = {
  turn_no: 1,
  ts: '2026-08-21T00:00:00Z',
  title: 'request',
  summary: 'response',
  request_tokens: 1,
  response_tokens: 2,
  cost_usd: 0.01,
  model: 'model-a',
  provider: 'provider-a',
  status_code: 200,
  submit_mode: 'full',
  injection_verdict: 'pass',
  output_verdict: 'pass',
  attachment_count: 0,
}

function mountPage() {
  return mount(SessionDetailPage, {
    global: {
      stubs: {
        SessionSummaryBar: { template: '<div data-testid="summary-bar" />' },
        SessionTurnListItem: { template: '<div data-testid="turn-item" />' },
        SessionTurnDrawer: { template: '<div data-testid="drawer" />' },
        'el-button': { template: '<button @click="$emit(\'click\')"><slot /></button>' },
      },
    },
  })
}

describe('SessionDetailPage', () => {
  beforeEach(() => {
    routeState.params = { id: 'session-a' }
    routeState.query = {}
    routerMock.replace.mockReset()
    listSessionTurnsMock.mockReset()
    getSessionSnapshotMock.mockReset()
    getSessionSnapshotMock.mockResolvedValue({ title: 'Session A' })
  })

  it('keeps snapshot errors separate from turn-list errors', async () => {
    listSessionTurnsMock.mockResolvedValue({ turns: [turn], has_more: false, next_cursor: '' })
    getSessionSnapshotMock.mockRejectedValue(new Error('snapshot unavailable'))
    const wrapper = mountPage()

    await flushPromises()

    expect(wrapper.find('.snapshot-error').text()).toContain('snapshot unavailable')
    expect(wrapper.find('[data-testid="turn-item"]').exists()).toBe(true)
    expect(wrapper.find('.list .error:not(.snapshot-error)').exists()).toBe(false)
  })

  it('shows a load-more error while preserving already loaded turns', async () => {
    let resolveInitial!: (value: unknown) => void
    const initial = new Promise(resolve => { resolveInitial = resolve })
    listSessionTurnsMock
      .mockReturnValueOnce(initial)
      .mockRejectedValueOnce(new Error('older turns unavailable'))
    const wrapper = mountPage()

    resolveInitial({ turns: [turn], has_more: true, next_cursor: 'next' })
    await flushPromises()
    await wrapper.find('.load-more button').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('[data-testid="turn-item"]')).toHaveLength(1)
    expect(wrapper.find('.list > .error').text()).toContain('older turns unavailable')
  })

  it('ignores an outdated turn-list response after the route session changes', async () => {
    let resolveOld!: (value: unknown) => void
    listSessionTurnsMock.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mountPage()

    routeState.params = { id: 'session-b' }
    await flushPromises()
    listSessionTurnsMock.mockResolvedValueOnce({ turns: [turn], has_more: false, next_cursor: '' })
    resolveOld({ turns: [{ ...turn, title: 'old response' }], has_more: false, next_cursor: '' })
    await flushPromises()

    expect(wrapper.text()).not.toContain('old response')
    expect(listSessionTurnsMock).toHaveBeenCalledWith(
      'session-b',
      { limit: 50 },
      { signal: expect.any(AbortSignal) },
    )
  })
})
