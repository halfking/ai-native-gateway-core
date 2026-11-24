import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { fetchDashboardBoard } from '../api/board'
import StatsDateRangePicker from '../components/analytics/StatsDateRangePicker.vue'
import { testI18n } from '../test-utils/i18n'

// ★ 2026-11-25 并入统一轮：运营看板列表页（/admin/board-operational/:dimension）
//   从「第四套预设」（裸 KxDateRangePicker + 4 档 + 365 天上限）并入**共享组件**。
//   ⇒ 本测试钉三件事：
//     ① 用的是**共享组件**（形态与其余页同构）；
//     ② 默认仍是「近 7 天」（days 口径没被改）；
//     ③ ★ **URL 深链（start/end）原样透传** ⇒ 外部链接仍能钉到某段历史区间
//        （★ 这一条是本页独有的语义 ⇒ 并入时最容易被悄悄弄丢 ⇒ 必须钉住）。

const routeState = {
  params: { dimension: 'models' } as Record<string, string>,
  query: {} as Record<string, string>,
}
const pushMock = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: pushMock, replace: vi.fn() }),
}))

vi.mock('../api/board', () => ({
  fetchDashboardBoard: vi.fn(),
}))

const mockedFetchBoard = vi.mocked(fetchDashboardBoard)

const EMPTY_BOARD = {
  summary: {},
  pies: { models: [], providers: [], clients: [], tenants: [] },
}

beforeEach(() => {
  vi.clearAllMocks()
  routeState.params = { dimension: 'models' }
  routeState.query = {}
  mockedFetchBoard.mockResolvedValue(EMPTY_BOARD as never)
})

describe('BoardOperationalListView —— 统一轮（共享组件 + 深链透传）', () => {
  it('① 用的是共享封装 StatsDateRangePicker（不是裸控件）', async () => {
    const w = mount((await import('./BoardOperationalListView.vue')).default, {
      global: { plugins: [testI18n] },
    })
    await flushPromises()
    expect(w.findComponent(StatsDateRangePicker).exists()).toBe(true)
  })

  it('② 默认是「近 7 天」⇒ 请求带 days=7', async () => {
    mount((await import('./BoardOperationalListView.vue')).default, {
      global: { plugins: [testI18n] },
    })
    await flushPromises()
    expect(mockedFetchBoard).toHaveBeenCalledWith({ days: 7 })
  })

  it('③ ★ URL 深链（start/end）⇒ 原样透传，**不被压成 days**', async () => {
    routeState.query = { start: '2026-09-01', end: '2026-09-10' }
    mount((await import('./BoardOperationalListView.vue')).default, {
      global: { plugins: [testI18n] },
    })
    await flushPromises()
    expect(mockedFetchBoard).toHaveBeenCalledWith({
      start: '2026-09-01',
      end: '2026-09-10',
      days: 10,
    })
  })

  it('④ 深链优先于 URL 里的 days（start/end 才是当前区间）', async () => {
    routeState.query = { days: '30', start: '2026-09-01', end: '2026-09-10' }
    mount((await import('./BoardOperationalListView.vue')).default, {
      global: { plugins: [testI18n] },
    })
    await flushPromises()
    expect(mockedFetchBoard).toHaveBeenCalledWith({
      start: '2026-09-01',
      end: '2026-09-10',
      days: 10,
    })
  })
})
