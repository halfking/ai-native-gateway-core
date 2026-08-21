import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TurnsListView from './TurnsListView.vue'

const listTurnsSessionsMock = vi.fn()
const listTurnsFilterOptionsMock = vi.fn()
const pushMock = vi.fn()

vi.mock('../api/turns', () => ({
  listTurnsSessions: (...args: unknown[]) => listTurnsSessionsMock(...args),
  listTurnsFilterOptions: (...args: unknown[]) => listTurnsFilterOptionsMock(...args),
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: pushMock }),
}))

const session = {
  session_id: 'session-a',
  tenant_id: 'tenant-a',
  title: 'Session A',
  status: 'closed',
  created_at: '2026-08-21T00:00:00Z',
  updated_at: '2026-08-21T00:00:00Z',
  total_turns: 1,
  total_tokens: 3,
  total_cost_usd: 0.01,
  models_used: ['model-a'],
  failover_count: 0,
  error_count: 0,
  duration_ms: 100,
  compression: { applied_count: 0, tokens_saved: 0, strategies: [] },
  user_tags: [],
  turns: [],
}

function mountView() {
  return mount(TurnsListView, {
    global: {
      stubs: {
        'el-select': { template: '<select><slot /></select>' },
        'el-option': { template: '<option><slot /></option>' },
      },
    },
  })
}

describe('TurnsListView', () => {
  beforeEach(() => {
    listTurnsSessionsMock.mockReset()
    listTurnsFilterOptionsMock.mockReset()
    pushMock.mockReset()
    listTurnsFilterOptionsMock.mockResolvedValue({
      projects: [], tasks: [], owners: [], clients: [], tags: [], models: [], providers: [], status_codes: [],
    })
  })

  it('shows an initial-load error and keeps load-more error empty', async () => {
    listTurnsSessionsMock.mockRejectedValue(new Error('sessions unavailable'))
    const wrapper = mountView()

    await flushPromises()

    expect(wrapper.find('[data-testid="turns-error-state"]').exists()).toBe(true)
    expect(wrapper.find('.load-more-error').exists()).toBe(false)
    expect(wrapper.find('.error-banner').text()).toContain('sessions unavailable')
  })

  it('keeps initial results visible and surfaces load-more failure separately', async () => {
    listTurnsSessionsMock
      .mockResolvedValueOnce({ items: [session], has_more: true, next_cursor: 'next' })
      .mockRejectedValueOnce(new Error('older sessions unavailable'))
    const wrapper = mountView()

    await flushPromises()
    expect(wrapper.text()).toContain('Session A')
    await wrapper.get('.load-more button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Session A')
    expect(wrapper.find('.load-more-error').text()).toContain('older sessions unavailable')
    expect(wrapper.find('[data-testid="turns-error-state"]').exists()).toBe(false)
  })

  it('ignores an older response after a reset starts a newer request', async () => {
    let resolveOld!: (value: unknown) => void
    listTurnsSessionsMock
      .mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
      .mockResolvedValueOnce({ items: [session], has_more: false, next_cursor: '' })
    const wrapper = mountView()

    await wrapper.get('.btn-primary').trigger('click')
    resolveOld({ items: [{ ...session, title: 'old response' }], has_more: false, next_cursor: '' })
    await flushPromises()

    expect(wrapper.text()).not.toContain('old response')
    expect(wrapper.text()).toContain('Session A')
  })

  it('does not render an AbortError as a user-facing failure', async () => {
    listTurnsSessionsMock.mockRejectedValue(new DOMException('aborted', 'AbortError'))
    const wrapper = mountView()

    await flushPromises()

    expect(wrapper.find('[data-testid="turns-error-state"]').exists()).toBe(false)
    expect(wrapper.find('.error-banner').exists()).toBe(false)
  })
})
