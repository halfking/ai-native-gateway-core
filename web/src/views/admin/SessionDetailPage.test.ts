import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import SessionDetailPage from './SessionDetailPage.vue'

const getSessionSnapshotMock = vi.fn()
const getSessionDetailV2Mock = vi.fn()
const routeState = reactive<{ params: Record<string, string>; query: Record<string, string> }>({
  params: { id: 'session-a' },
  query: {},
})

vi.mock('../../api/sessions_v2', async (importOriginal) => ({
  // isRecordingUnavailable 走真实现：consent 409 的判定必须与生产同一份
  // 口径（instanceof ApiError + status/code），桩了它等于自己测自己。
  ...(await importOriginal<typeof import('../../api/sessions_v2')>()),
  getSessionSnapshot: (...args: unknown[]) => getSessionSnapshotMock(...args),
  getSessionDetailV2: (...args: unknown[]) => getSessionDetailV2Mock(...args),
}))
vi.mock('../../api/_core', () => ({
  ApiError: class ApiError extends Error {
    status: number
    detail: string
    code: string
    constructor(status: number, detail: string, code = '') {
      super(detail)
      this.status = status
      this.detail = detail
      this.code = code
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
      requestDetail: {
        bodyStatus: {
          unavailableTitle: '部分轮次没有请求/响应正文',
          unavailableBody: '这些轮次没有采集到正文。',
          affectedTurns: '涉及轮次：{list}',
          dismiss: '知道了',
          consentOffTitle: '会话正文录制未开启',
          consentOffBody: '该租户未开启会话正文录制：轮次正文、摘要与取证内容不可用。',
          consentOffInline: '会话正文录制未开启，正文与摘要不可用',
        },
      },
    },
  },
})

function mountPage() {
  return mount(SessionDetailPage, {
    global: {
      plugins: [i18n],
      stubs: {
        SessionSummaryBar: { template: '<div data-testid="summary-bar" />' },
        SessionTurnsTimeline: {
          props: ['sessionId'],
          template: '<div data-testid="turns-timeline">{{ sessionId }}<button data-testid="tl-consent-off" @click="$emit(\'recordingConsent\', false)" /><button data-testid="tl-consent-on" @click="$emit(\'recordingConsent\', true)" /></div>',
        },
        TurnDigestDrawer: { props: ['modelValue', 'sessionId', 'turnNo'], template: '<div data-testid="digest-drawer" />' },
      },
    },
  })
}

describe('SessionDetailPage', { timeout: 20_000 }, () => {
  beforeEach(() => {
    routeState.params = { id: 'session-a' }
    routeState.query = {}
    getSessionSnapshotMock.mockReset()
    getSessionSnapshotMock.mockResolvedValue({ title: 'Session A' })
    getSessionDetailV2Mock.mockReset()
    // 默认无异常轮次 —— 横幅不应出现。
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [{ turn_no: 1, body_status: 'available' }],
    })
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

  // ── Subtask 3 (handoff §5): body_status 三态横幅 ──────────────────────
  it('shows no banner when every turn body is available', async () => {
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [
        { turn_no: 1, body_status: 'available' },
        { turn_no: 2, body_status: 'available' },
      ],
    })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(false)
  })


  it('shows the banner when some turns have no body', async () => {
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [
        { turn_no: 2, body_status: 'available' },
        { turn_no: 4, body_status: 'unavailable' },
        { turn_no: 6, body_status: 'unavailable' },
      ],
    })
    const wrapper = mountPage()
    await flushPromises()
    const banner = wrapper.get('.body-status-banner')
    expect(banner.classes()).toContain('body-status-unavailable')
    expect(banner.text()).toContain('部分轮次没有请求/响应正文')
    expect(banner.text()).toContain('4, 6')
  })

  // 后端只发两态。前端若把 dropped 当成一类渲染，就等于在替后端编造一个
  // 它从未承诺的语义（且该语义当前是假的：没有保留期开关，也没有清理任务）。
  it('ignores a dropped status the backend does not emit', async () => {
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [{ turn_no: 3, body_status: 'dropped' }],
    })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(false)
  })

  it('dismisses the banner on demand', async () => {
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [{ turn_no: 8, body_status: 'unavailable' }],
    })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(true)
    await wrapper.get('.body-status-dismiss').trigger('click')
    expect(wrapper.find('.body-status-banner').exists()).toBe(false)
  })

  it('clears a stale banner when the route session changes', async () => {
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [{ turn_no: 8, body_status: 'unavailable' }],
    })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(true)
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [{ turn_no: 1, body_status: 'available' }],
    })
    routeState.params = { id: 'session-b' }
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(false)
  })

  // 关闭状态必须随会话重置：否则在会话 A 上点过「知道了」，切到同样有
  // 缺正文轮次的会话 B 时横幅会永远不出现。
  it('re-shows the banner on a new session even after dismissing on the previous one', async () => {
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [{ turn_no: 8, body_status: 'unavailable' }],
    })
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.get('.body-status-dismiss').trigger('click')
    expect(wrapper.find('.body-status-banner').exists()).toBe(false)

    routeState.params = { id: 'session-b' }
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(true)
  })

  // 横幅是增强信息：详情端点挂了也不能把整个详情页带崩。
  it('keeps the page usable when the detail endpoint fails', async () => {
    getSessionDetailV2Mock.mockRejectedValue(new Error('detail unavailable'))
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(false)
    expect(wrapper.get('[data-testid="turns-timeline"]').text()).toBe('session-a')
  })

  // /turns 列表端点是 metadata-only，不回传 body_status；详情端点不回传时
  // 也不能凭空造一个横幅出来。
  it('ignores turns without a body_status field', async () => {
    getSessionDetailV2Mock.mockResolvedValue({
      turns: [{ turn_no: 1 }, { turn_no: 2, body_status: '' }],
    })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.body-status-banner').exists()).toBe(false)
  })

  // ── W1.3（24h 审计第 7 轮遗留）：consent=off 是中性状态，不是错误 ──────
  // 信号源①：detail 端点 409 + recording_unavailable（列表没回来时的兜底，
  // 或旧后端列表没带字段时的唯一信号）。
  it('shows the consent-off banner when detail answers 409 recording_unavailable', async () => {
    const { ApiError } = await import('../../api/_core')
    getSessionDetailV2Mock.mockRejectedValue(
      new ApiError(409, 'session body recording is off', 'recording_unavailable'),
    )
    const wrapper = mountPage()
    await flushPromises()
    const banner = wrapper.get('[data-testid="consent-off-banner"]')
    expect(banner.text()).toContain('会话正文录制未开启')
    // 「部分轮次没有正文」在 consent=off 下是误导（全部轮次都没正文），
    // 两条横幅不许同屏。
    expect(wrapper.find('.body-status-unavailable').exists()).toBe(false)
  })

  // 信号源②：turns 列表显式透出的 recording_consent 字段（元数据主路不打
  // 409 门，这是常态下的第一信号）。
  it('shows the consent-off banner when the turns list reports consent=false', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.get('[data-testid="tl-consent-off"]').trigger('click')
    expect(wrapper.find('[data-testid="consent-off-banner"]').exists()).toBe(true)
    expect(wrapper.find('.body-status-unavailable').exists()).toBe(false)
  })

  it('hides the consent-off banner again when the list reports consent=true', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.get('[data-testid="tl-consent-off"]').trigger('click')
    expect(wrapper.find('[data-testid="consent-off-banner"]').exists()).toBe(true)
    await wrapper.get('[data-testid="tl-consent-on"]').trigger('click')
    expect(wrapper.find('[data-testid="consent-off-banner"]').exists()).toBe(false)
  })

  // 2026-10-31 批判式审计补：/detail 的 409 在途时切走会话 ⇒ 旧会话的
  // consent 状态不许点亮新会话的横幅（成功路径有 id 守卫，catch 曾漏）。
  it('drops a stale 409 when the user navigated to another session mid-flight', async () => {
    const { ApiError } = await import('../../api/_core')
    let rejectFirst!: (cause: unknown) => void
    getSessionDetailV2Mock.mockImplementationOnce(
      () => new Promise((_, reject) => { rejectFirst = reject }),
    )
    const wrapper = mountPage()
    // 409 还在途 ⇒ 切到下一个会话（新会话的 detail 正常返回）。
    routeState.params = { id: 'session-b' }
    await flushPromises()
    rejectFirst(new ApiError(409, 'session body recording is off', 'recording_unavailable'))
    await flushPromises()
    expect(wrapper.find('[data-testid="consent-off-banner"]').exists()).toBe(false)
  })
})
