import { ref, computed, watch, onUnmounted } from 'vue'
import { fetchDashboardBoard, fetchBoardOperational, type BoardPayload, type BoardOperationalPayload } from '../api/board'
import { resolveDashboardRefreshMs } from './dashboardRefreshSettings'
import { subscribeTerminalRequests, connectionRef } from './liveStreamStore'
import { applyLiveRequestToBoard } from './boardLiveMerge'
import { dashboardPreferenceStorageKey } from './liveStreamPreferences'
import {
  defaultBoardTimeRange,
  boardRangeIncludesToday,
  toBoardTimeQuery,
  type BoardTimeRange,
} from '../utils/boardTimeRange'

const DEFAULT_REFRESH_MS = 10_000
const SSE_RECONCILE_MIN_MS = 30_000
const LEGACY_TIME_RANGE_STORAGE_KEY = 'dashboard_board_time_range_v1'

function isDateOnly(value: unknown): value is string {
  return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) && !Number.isNaN(Date.parse(`${value}T00:00:00Z`))
}

function normalizeStoredTimeRange(value: unknown): BoardTimeRange | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const raw = value as Partial<BoardTimeRange>
  if (raw.preset === 'today' || raw.preset === '7d' || raw.preset === '30d') {
    const days = raw.preset === 'today' ? 1 : raw.preset === '7d' ? 7 : 30
    return { preset: raw.preset, days }
  }
  if (raw.preset === 'custom' && isDateOnly(raw.start) && isDateOnly(raw.end) && raw.start <= raw.end) {
    const days = Math.max(1, Math.round((Date.parse(`${raw.end}T00:00:00Z`) - Date.parse(`${raw.start}T00:00:00Z`)) / 86_400_000) + 1)
    return { preset: 'custom', days, start: raw.start, end: raw.end }
  }
  return null
}

function readStoredTimeRange(): BoardTimeRange {
  try {
    const raw = localStorage.getItem(dashboardPreferenceStorageKey('board-range'))
      ?? localStorage.getItem(LEGACY_TIME_RANGE_STORAGE_KEY)
    return normalizeStoredTimeRange(raw ? JSON.parse(raw) : null) ?? defaultBoardTimeRange()
  } catch {
    return defaultBoardTimeRange()
  }
}

function persistTimeRange(range: BoardTimeRange) {
  try {
    localStorage.setItem(dashboardPreferenceStorageKey('board-range'), JSON.stringify(range))
  } catch {
    // Browser storage can be unavailable; the board still works in-memory.
  }
}

async function resolveRefreshMs(): Promise<number> {
  return resolveDashboardRefreshMs()
}

function sseConnectionActive(): boolean {
  const state = connectionRef.value
  return state === 'open' || state === 'connecting' || state === 'reconnecting'
}

export function useDashboardBoard() {
  const timeRange = ref<BoardTimeRange>(readStoredTimeRange())
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
    try {
      const fresh = await fetchDashboardBoard(toBoardTimeQuery(timeRange.value))
      adoptBoardPayload(fresh, { silentReconcile: silent })
      if (fresh.operational) {
        operational.value = fresh.operational
      } else {
        await loadOperational()
      }
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
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
    // operational is bundled into board; no separate poll needed
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
    const normalized = normalizeStoredTimeRange(next) ?? defaultBoardTimeRange()
    timeRange.value = normalized
    persistTimeRange(normalized)
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
