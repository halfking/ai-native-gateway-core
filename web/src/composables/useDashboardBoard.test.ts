import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'

vi.mock('../api/board', () => ({
  fetchDashboardBoard: vi.fn().mockResolvedValue({
    summary: { total_requests: 0, success: 0, failure: 0 },
    pies: [],
    trends: [],
    source: 'mock',
  }),
  fetchBoardOperational: vi.fn().mockResolvedValue(null),
}))
vi.mock('./dashboardRefreshSettings', () => ({ resolveDashboardRefreshMs: vi.fn().mockResolvedValue(10_000) }))
vi.mock('./liveStreamStore', () => ({
  connectionRef: { value: 'closed' },
  subscribeTerminalRequests: vi.fn(() => () => {}),
}))
vi.mock('./boardLiveMerge', () => ({ applyLiveRequestToBoard: vi.fn() }))

import { useDashboardBoard } from './useDashboardBoard'
import { dashboardPreferenceStorageKey } from './liveStreamPreferences'

const legacyTimeRangeStorageKey = 'dashboard_board_time_range_v1'

describe('useDashboardBoard persistence', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('restores a valid custom date range', () => {
    localStorage.setItem(legacyTimeRangeStorageKey, JSON.stringify({
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
    localStorage.setItem(legacyTimeRangeStorageKey, JSON.stringify({ preset: 'custom', start: 'bad', end: '2026-08-01' }))
    const board = useDashboardBoard()
    expect(board.timeRange.value).toEqual({ preset: 'today', days: 1 })

    board.setTimeRange({ preset: '7d', days: 99 })
    expect(board.timeRange.value).toEqual({ preset: '7d', days: 7 })
    expect(JSON.parse(localStorage.getItem(dashboardPreferenceStorageKey('board-range')) || '{}')).toEqual({ preset: '7d', days: 7 })
  })
})

// 2026-09-01 regression guard: commit 82e324b79 wired visibility listener
// cleanup into startAutoRefresh()/stopAutoRefresh(). Before that fix,
// repeated start/stop cycles stacked visibilitychange handlers —
// exactly the failure mode that broke dashboard navigation in production.
describe('useDashboardBoard startAutoRefresh idempotency', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  // addEventListener has overloaded signatures; vi.spyOn's generic
  // inference narrows to one overload. We only read .mock.calls
  // (string[][] shape) so the untyped alias is fine here.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let attachSpy: any
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let removeSpy: any

  beforeEach(() => {
    attachSpy = vi.spyOn(document, 'addEventListener')
    removeSpy = vi.spyOn(document, 'removeEventListener')
  })

  function visAttachCount(): number {
    return attachSpy.mock.calls.filter((c: unknown[]) => c[0] === 'visibilitychange').length
  }

  function visRemoveCount(): number {
    return removeSpy.mock.calls.filter((c: unknown[]) => c[0] === 'visibilitychange').length
  }

  function visNet(): number {
    return visAttachCount() - visRemoveCount()
  }

  // The composable calls onUnmounted() at setup, which requires an
  // active Vue component instance. Wrap each test in a minimal host
  // component so lifecycle hooks resolve cleanly.
  function withBoard<T>(fn: (board: ReturnType<typeof useDashboardBoard>) => Promise<T> | T): Promise<T> {
    return new Promise((resolve, reject) => {
      const host = defineComponent({
        setup() {
          const board = useDashboardBoard()
          // Default timeRange (today) makes liveUpdatesEnabled true so
          // startAutoRefresh reaches schedulePoll() + visibility listener
          // attach. Without this the timer/listener never install and
          // the assertions would be vacuous.
          board.setTimeRange({ preset: 'today', days: 1 })
          nextTick(async () => {
            try {
              resolve(await fn(board))
            } catch (e) {
              reject(e)
            }
          })
          return () => null
        },
      })
      mount(host)
    })
  }

  it('repeated startAutoRefresh does not stack visibilitychange listeners', async () => {
    await withBoard(async (board) => {
      const baselineAttach = visAttachCount()
      const baselineRemove = visRemoveCount()

      await board.startAutoRefresh()
      await board.startAutoRefresh()
      await board.startAutoRefresh()

      // Three attaches; at least two removes (start always calls
      // removeEventListener defensively before adding the new handler).
      expect(visAttachCount() - baselineAttach).toBe(3)
      expect(visRemoveCount() - baselineRemove).toBeGreaterThanOrEqual(2)
      // Net stays bounded: pre-fix code stacked without removing, so the
      // delta grew linearly with start calls. The bound "net ≤ 1" is the
      // invariant we want to protect.
      expect(visNet()).toBeLessThanOrEqual(1)
    })
  })

  it('stopAutoRefresh detaches the visibility listener it installed', async () => {
    let baselineAttach = 0
    let baselineRemove = 0
    await withBoard(async (board) => {
      baselineAttach = visAttachCount()
      baselineRemove = visRemoveCount()
      await board.startAutoRefresh()
      // Exactly one new listener attached since the host mounted.
      expect(visAttachCount() - baselineAttach).toBeGreaterThanOrEqual(1)
    })
    // After host unmount, onUnmounted() → stopAutoRefresh() removed
    // the listener; net add/remove on visibilitychange is balanced.
    expect(visAttachCount() - baselineAttach).toBeGreaterThanOrEqual(1)
    expect(visRemoveCount() - baselineRemove).toBeGreaterThanOrEqual(1)
    expect(visNet()).toBeLessThanOrEqual(0)
  })

  it('start → stop → start keeps the net visibility listener at ≤ 1 (no leak)', async () => {
    await withBoard(async (board) => {
      const baselineAttach = visAttachCount()
      const baselineRemove = visRemoveCount()

      await board.startAutoRefresh()
      board.stopAutoRefresh()
      await board.startAutoRefresh()

      // After a full start/stop/start cycle the listener count is bounded
      // by 1 — defensive against the pre-fix bug that stacked handlers
      // instead of replacing them. The exact attach/remove count varies
      // with watch() side-effects so we assert the upper bound instead.
      expect(visNet()).toBeLessThanOrEqual(1)
      // At least one remove happened (from stopAutoRefresh).
      expect(visRemoveCount() - baselineRemove).toBeGreaterThanOrEqual(1)
    })
  })
})
