import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import SessionDetailPage from './SessionDetailPage.vue'

const getSessionSnapshotMock = vi.fn()
const routeState = reactive<{ params: Record<string, string>; query: Record<string, string> }>({
  params: { id: 'session-a' },
  query: {},
})

vi.mock('../../api/sessions_v2', () => ({
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
  useRouter: () => ({ resolve: vi.fn(), push: vi.fn() }),
}))

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

function mountPage() {
  return mount(SessionDetailPage, {
    global: {
      plugins: [i18n],
      stubs: {
        SessionSummaryBar: { template: '<div data-testid="summary-bar" />' },
        SessionTurnsTimeline: { props: ['sessionId'], template: '<div data-testid="turns-timeline">{{ sessionId }}</div>' },
        TurnDigestDrawer: { props: ['modelValue', 'sessionId', 'turnNo'], template: '<div data-testid="digest-drawer" />' },
      },
    },
  })
}

describe('SessionDetailPage', () => {
  beforeEach(() => {
    routeState.params = { id: 'session-a' }
    routeState.query = {}
    getSessionSnapshotMock.mockReset()
    getSessionSnapshotMock.mockResolvedValue({ title: 'Session A' })
  })

  it('renders turn timeline for the route session id', async () => {
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.get('[data-testid="turns-timeline"]').text()).toBe('session-a')
  })

  it('shows snapshot errors without hiding the timeline', async () => {
    getSessionSnapshotMock.mockRejectedValue(new Error('snapshot unavailable'))
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.snapshot-error').text()).toContain('snapshot unavailable')
    expect(wrapper.find('[data-testid="turns-timeline"]').exists()).toBe(true)
  })

  it('reloads timeline key when route session changes', async () => {
    const wrapper = mountPage()
    await flushPromises()
    routeState.params = { id: 'session-b' }
    await flushPromises()
    expect(wrapper.get('[data-testid="turns-timeline"]').text()).toBe('session-b')
  })
})
