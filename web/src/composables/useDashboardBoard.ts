import { ref, computed, watch, onUnmounted } from 'vue'
import { fetchDashboardBoard, fetchBoardOperational, type BoardPayload, type BoardOperationalPayload } from '../api/board'
import { getSetting } from '../api/settings'
import { subscribeTerminalRequests, connectionRef } from './liveStreamStore'
import { applyLiveRequestToBoard } from './boardLiveMerge'
import {
  defaultBoardTimeRange,
  boardRangeIncludesToday,
  toBoardTimeQuery,
  type BoardTimeRange,
} from '../utils/boardTimeRange'

const DEFAULT_REFRESH_MS = 10_000
const SSE_RECONCILE_MIN_MS = 30_000
const OPERATIONAL_REFRESH_MS = 30_000

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

function sseConnectionActive(): boolean {
  const state = connectionRef.value
  return state === 'open' || state === 'connecting' || state === 'reconnecting'
}

export function useDashboardBoard() {
  const timeRange = ref<BoardTimeRange>(defaultBoardTimeRange())
  const days = computed(() => timeRange.value.days)
  const liveUpdatesEnabled = computed(() => boardRangeIncludesToday(timeRange.value))

  const loading = ref(false)
  const error = ref<string | null>(null)
  const board = ref<BoardPayload | null>(null)
  const operational = ref<BoardOperationalPayload | null>(null)

  let refreshTimer: number | undefined
  let operationalTimer: number | undefined
  let refreshMs = DEFAULT_REFRESH_MS
  let unsubscribeTerminal: (() => void) | null = null
  let loadInFlight = false
  const appliedTerminalIds = new Set<string>()

  function mergeTerminalRequest(requestId: string, req: Parameters<typeof applyLiveRequestToBoard>[1]) {
    if (!liveUpdatesEnabled.value) return
    if (appliedTerminalIds.has(requestId)) return
    if (!board.value) return
    appliedTerminalIds.add(requestId)
    board.value = applyLiveRequestToBoard(board.value, req, timeRange.value)
  }

  function adoptBoardPayload(fresh: BoardPayload, opts?: { silentReconcile?: boolean }) {
    const local = board.value
    const localTotal = local?.summary?.total_requests ?? 0
    const remoteTotal = fresh.summary?.total_requests ?? 0

    if (opts?.silentReconcile && liveUpdatesEnabled.value && local && remoteTotal < localTotal) {
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

  async function loadOperational() {
    if (document.hidden) return
    try {
      operational.value = await fetchBoardOperational()
    } catch {
      // operational cards are non-critical; keep last snapshot
    }
  }

  async function load(options?: { silent?: boolean }) {
    if (loadInFlight) return
    const silent = options?.silent === true
    if (document.hidden && silent) return
    loadInFlight = true
    if (!silent) {
      loading.value = true
    }
    error.value = null
    const operationalPromise = loadOperational()
    try {
      const fresh = await fetchDashboardBoard(toBoardTimeQuery(timeRange.value))
      adoptBoardPayload(fresh, { silentReconcile: silent })
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
      await operationalPromise
      loadInFlight = false
      if (!silent) {
        loading.value = false
      }
    }
  }

  function wireLiveUpdates() {
    if (!liveUpdatesEnabled.value) return
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
  }

  async function resolvePollIntervalMs(): Promise<number> {
    const base = await resolveRefreshMs()
    if (liveUpdatesEnabled.value && sseConnectionActive()) {
      return Math.max(base, SSE_RECONCILE_MIN_MS)
    }
    return base
  }

  function scheduleOperationalPoll() {
    if (operationalTimer) clearInterval(operationalTimer)
    operationalTimer = window.setInterval(() => {
      void loadOperational()
    }, OPERATIONAL_REFRESH_MS)
  }

  function stopOperationalPoll() {
    if (operationalTimer) {
      clearInterval(operationalTimer)
      operationalTimer = undefined
    }
  }

  function schedulePoll() {
    if (refreshTimer) clearInterval(refreshTimer)
    refreshTimer = window.setInterval(() => {
      if (document.hidden) return
      void load({ silent: true })
    }, refreshMs)
  }

  function onVisibilityChange() {
    if (document.hidden) {
      if (refreshTimer) {
        clearInterval(refreshTimer)
        refreshTimer = undefined
      }
      return
    }
    if (liveUpdatesEnabled.value) {
      void load({ silent: true })
      void loadOperational()
      schedulePoll()
    }
  }

  async function startAutoRefresh() {
    if (refreshTimer) clearInterval(refreshTimer)
    document.removeEventListener('visibilitychange', onVisibilityChange)
    if (liveUpdatesEnabled.value) {
      wireLiveUpdates()
      refreshMs = await resolvePollIntervalMs()
      void loadOperational()
      scheduleOperationalPoll()
      schedulePoll()
      document.addEventListener('visibilitychange', onVisibilityChange)
    } else {
      unwireLiveUpdates()
      stopOperationalPoll()
    }
  }

  function stopAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer)
      refreshTimer = undefined
    }
    stopOperationalPoll()
    document.removeEventListener('visibilitychange', onVisibilityChange)
    unwireLiveUpdates()
  }

  function setTimeRange(next: BoardTimeRange) {
    timeRange.value = next
  }

  watch(liveUpdatesEnabled, (enabled) => {
    if (enabled) {
      void startAutoRefresh()
    } else {
      if (refreshTimer) {
        clearInterval(refreshTimer)
        refreshTimer = undefined
      }
      unwireLiveUpdates()
    }
  })

  onUnmounted(() => stopAutoRefresh())

  return {
    timeRange,
    days,
    liveUpdatesEnabled,
    loading,
    error,
    board,
    operational,
    load,
    loadOperational,
    setTimeRange,
    startAutoRefresh,
    stopAutoRefresh,
  }
}
