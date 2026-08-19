import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api/board', () => ({
  fetchDashboardBoard: vi.fn(),
  fetchBoardOperational: vi.fn(),
}))
vi.mock('./dashboardRefreshSettings', () => ({ resolveDashboardRefreshMs: vi.fn().mockResolvedValue(10_000) }))
vi.mock('./liveStreamStore', () => ({
  connectionRef: { value: 'closed' },
  subscribeTerminalRequests: vi.fn(() => () => {}),
}))
vi.mock('./boardLiveMerge', () => ({ applyLiveRequestToBoard: vi.fn() }))

import { useDashboardBoard } from './useDashboardBoard'

const TIME_RANGE_STORAGE_KEY = 'dashboard_board_time_range_v1'

describe('useDashboardBoard persistence', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('restores a valid custom date range', () => {
    localStorage.setItem(TIME_RANGE_STORAGE_KEY, JSON.stringify({
      preset: 'custom',
      days: 1,
      start: '2026-08-01',
      end: '2026-08-03',
    }))

    const board = useDashboardBoard()
    expect(board.timeRange.value).toEqual({
      preset: 'custom',
      days: 3,
      start: '2026-08-01',
      end: '2026-08-03',
    })
  })

  it('rejects malformed stored ranges and writes normalized changes', () => {
    localStorage.setItem(TIME_RANGE_STORAGE_KEY, JSON.stringify({ preset: 'custom', start: 'bad', end: '2026-08-01' }))
    const board = useDashboardBoard()
    expect(board.timeRange.value).toEqual({ preset: 'today', days: 1 })

    board.setTimeRange({ preset: '7d', days: 99 })
    expect(board.timeRange.value).toEqual({ preset: '7d', days: 7 })
    expect(JSON.parse(localStorage.getItem(TIME_RANGE_STORAGE_KEY) || '{}')).toEqual({ preset: '7d', days: 7 })
  })
})
