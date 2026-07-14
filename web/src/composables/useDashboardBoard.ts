import { ref, onUnmounted } from 'vue'
import { fetchDashboardBoard, type BoardPayload } from '../api/board'
import { getSetting } from '../api/settings'
import { acquireLiveStream, subscribeTerminalRequests } from './liveStreamStore'
import { applyLiveRequestToBoard } from './boardLiveMerge'

const DEFAULT_REFRESH_MS = 1000

async function resolveRefreshMs(): Promise<number> {
  try {
    const foldUnit = await getSetting('dashboard.stats.fold_unit')
    if (foldUnit.value === 'minute') {
      const interval = await getSetting('dashboard.stats.fold_interval')
      const n = Number(interval.value)
      return Math.max(1, Number.isFinite(n) ? n : 1) * 60_000
    }
    const interval = await getSetting('dashboard.stats.fold_interval')
    const n = Number(interval.value)
    return Math.max(1, Number.isFinite(n) ? n : 1) * 1000
  } catch {
    return DEFAULT_REFRESH_MS
  }
}

export function useDashboardBoard() {
  const days = ref(7)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const board = ref<BoardPayload | null>(null)

  let refreshTimer: number | undefined
  let refreshMs = DEFAULT_REFRESH_MS
  let releaseLiveStream: (() => void) | null = null
  let unsubscribeTerminal: (() => void) | null = null
  const appliedTerminalIds = new Set<string>()

  function mergeTerminalRequest(requestId: string, req: Parameters<typeof applyLiveRequestToBoard>[1]) {
    if (appliedTerminalIds.has(requestId)) return
    if (!board.value) return
    appliedTerminalIds.add(requestId)
    board.value = applyLiveRequestToBoard(board.value, req, days.value)
  }

  function adoptBoardPayload(fresh: BoardPayload, opts?: { silentReconcile?: boolean }) {
    const local = board.value
    const localTotal = local?.summary?.total_requests ?? 0
    const remoteTotal = fresh.summary?.total_requests ?? 0

    if (opts?.silentReconcile && local && remoteTotal < localTotal) {
      board.value = {
        ...fresh,
        summary: local.summary,
        pies: local.pies,
        trends: local.trends,
        source: local.source ?? 'live_sse_delta',
        cache_meta: local.cache_meta ?? fresh.cache_meta,
      }
      return
    }

    board.value = fresh
    appliedTerminalIds.clear()
  }

  async function load(options?: { silent?: boolean }) {
    const silent = options?.silent === true
    if (!silent) {
      loading.value = true
    }
    error.value = null
    try {
      const fresh = await fetchDashboardBoard({ days: days.value })
      adoptBoardPayload(fresh, { silentReconcile: silent })
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
      if (!silent) {
        loading.value = false
      }
    }
  }

  function wireLiveUpdates() {
    if (!releaseLiveStream) {
      releaseLiveStream = acquireLiveStream()
    }
    if (!unsubscribeTerminal) {
      unsubscribeTerminal = subscribeTerminalRequests((req) => {
        if (!req.request_id) return
        mergeTerminalRequest(req.request_id, req)
      })
    }
  }

  function unwireLiveUpdates() {
    unsubscribeTerminal?.()
    unsubscribeTerminal = null
    releaseLiveStream?.()
    releaseLiveStream = null
  }

  async function startAutoRefresh() {
    if (refreshTimer) clearInterval(refreshTimer)
    wireLiveUpdates()
    refreshMs = await resolveRefreshMs()
    refreshTimer = window.setInterval(() => void load({ silent: true }), refreshMs)
  }

  function stopAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer)
      refreshTimer = undefined
    }
    unwireLiveUpdates()
  }

  onUnmounted(() => stopAutoRefresh())

  return {
    days,
    loading,
    error,
    board,
    load,
    startAutoRefresh,
    stopAutoRefresh,
  }
}
