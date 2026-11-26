import { ref, computed, watch, onUnmounted } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { fetchDashboardBoard, fetchBoardOperational, type BoardPayload, type BoardOperationalPayload } from '../api/board'
import { resolveDashboardRefreshMs } from './dashboardRefreshSettings'
import { subscribeTerminalRequests, connectionRef } from './liveStreamStore'
import { applyLiveRequestToBoard } from './boardLiveMerge'
import { dashboardPreferenceStorageKey } from './liveStreamPreferences'
import { usePersistedValue } from './usePersistedValue'
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

// LP8 (2026-08-24): board-range preference goes through usePersistedValue so
// it shares the lifecycle-flush / error-degrade primitives. Each call site
// mutates the ref; the composable handles serialisation + immediate write.
const boardRangePersisted = usePersistedValue<BoardTimeRange>(
  dashboardPreferenceStorageKey('board-range'),
  defaultBoardTimeRange,
  { immediate: true },
)

function persistTimeRange(range: BoardTimeRange) {
  boardRangePersisted.value.value = range
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

  // 2026-11-20 口径对齐轮：英雄区汇总的探测口径开关（持久化）。
  // exclude（缺省）= 内部对账口径；include = 供应商对账口径（usage_facts 全量）。
  // include 模式下关闭 SSE 实时增量：live 流推的是业务终态请求，往「含探测」
  // 的总数上叠业务增量会让 hero 与对账口径漂移（静默拉大差值），
  // 轮询静默对账（adoptBoardPayload）会兜住数字。
  const includeProbes = ref(false)
  let probeScopePersisted: ReturnType<typeof usePersistedValue<boolean>> | null = null
  try {
    probeScopePersisted = usePersistedValue<boolean>(
      dashboardPreferenceStorageKey('board-probe-scope'),
      () => false,
      { immediate: true },
    )
    const storedProbe = probeScopePersisted.value.value
    if (typeof storedProbe === 'boolean') includeProbes.value = storedProbe
  } catch {
    probeScopePersisted = null
  }
  function persistProbeScope(v: boolean) {
    includeProbes.value = v
    if (probeScopePersisted) probeScopePersisted.value.value = v
  }
  const liveMergeAllowed = computed(() => liveUpdatesEnabled.value && !includeProbes.value)

  const loading = ref(false)
  const error = ref<string | null>(null)
  const board = ref<BoardPayload | null>(null)
  const operational = ref<BoardOperationalPayload | null>(null)

  let refreshTimer: number | undefined
  let refreshMs = DEFAULT_REFRESH_MS
  let unsubscribeTerminal: (() => void) | null = null
  let loadInFlight = false
  let loadController: AbortController | null = null
  let loadGeneration = 0
  let refreshGeneration = 0
  let refreshActive = false
  const appliedTerminalIds = new Set<string>()

  function mergeTerminalRequest(requestId: string, req: Parameters<typeof applyLiveRequestToBoard>[1]) {
    if (!liveMergeAllowed.value) return
    if (appliedTerminalIds.has(requestId)) return
    if (!board.value || board.value.source === 'request_metrics') return
    appliedTerminalIds.add(requestId)
    board.value = applyLiveRequestToBoard(board.value, req, timeRange.value)
  }

  function adoptBoardPayload(fresh: BoardPayload, opts?: { silentReconcile?: boolean }) {
    const local = board.value
    const localTotal = local?.summary?.total_requests ?? 0
    const remoteTotal = fresh.summary?.total_requests ?? 0

    if (opts?.silentReconcile && fresh.source !== 'request_metrics' && liveMergeAllowed.value && local && remoteTotal < localTotal) {
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
    if (fresh.source === 'request_metrics') unwireLiveUpdates()
  }

  async function loadOperational() {
    if (document.hidden || board.value?.source === 'request_metrics') return
    try {
      const value = await fetchBoardOperational()
      if (board.value?.source !== 'request_metrics') operational.value = value
    } catch {
      // operational cards are non-critical; keep last snapshot
    }
  }

  async function load(options?: { silent?: boolean }) {
    const silent = options?.silent === true
    if (document.hidden && silent) return
    const generation = ++loadGeneration
    loadController?.abort()
    const controller = new AbortController()
    loadController = controller
    loadInFlight = true
    if (!silent) loading.value = true
    error.value = null
    const query = toBoardTimeQuery(timeRange.value)
    if (includeProbes.value) query.include_probes = true
    try {
      const fresh = await fetchDashboardBoard(query, controller.signal)
      if (controller.signal.aborted || generation !== loadGeneration) return
      adoptBoardPayload(fresh, { silentReconcile: silent })
      if (fresh.operational) {
        operational.value = fresh.operational
      } else if (fresh.source === 'request_metrics') {
        operational.value = null
      } else {
        await loadOperational()
      }
    } catch (e: unknown) {
      if (controller.signal.aborted || generation !== loadGeneration) return
      error.value = surfaceError(e, { key: 'common.feedback.loadFailed' })
    } finally {
      if (generation === loadGeneration) {
        loadInFlight = false
        loadController = null
        if (!silent) loading.value = false
      }
    }
  }

  function wireLiveUpdates() {
    if (!liveUpdatesEnabled.value || board.value?.source === 'request_metrics') return
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
    if (liveUpdatesEnabled.value && board.value?.source !== 'request_metrics' && sseConnectionActive()) {
      return Math.max(base, SSE_RECONCILE_MIN_MS)
    }
    return base
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
    // Only restart polling if auto-refresh is still active
    if (refreshActive && liveUpdatesEnabled.value) {
      void load({ silent: true })
      void loadOperational()
      schedulePoll()
    }
  }

  async function startAutoRefresh() {
    const generation = ++refreshGeneration
    refreshActive = true
    // Clean up any existing timers and listeners first
    if (refreshTimer) clearInterval(refreshTimer)
    document.removeEventListener('visibilitychange', onVisibilityChange)
    
    if (!liveUpdatesEnabled.value) {
      refreshActive = false
      unwireLiveUpdates()
      return
    }
    const resolvedMs = await resolvePollIntervalMs()
    if (!refreshActive || generation !== refreshGeneration || !liveUpdatesEnabled.value) return
    refreshMs = resolvedMs
    wireLiveUpdates()
    void loadOperational()
    schedulePoll()
    document.addEventListener('visibilitychange', onVisibilityChange)
  }

  function stopAutoRefresh() {
    refreshActive = false
    refreshGeneration += 1
    loadController?.abort()
    loadController = null
    loadGeneration += 1
    loadInFlight = false
    if (refreshTimer) {
      clearInterval(refreshTimer)
      refreshTimer = undefined
    }
    // Always remove the visibility listener when stopping
    document.removeEventListener('visibilitychange', onVisibilityChange)
    unwireLiveUpdates()
  }

  function setTimeRange(next: BoardTimeRange) {
    const normalized = normalizeStoredTimeRange(next) ?? defaultBoardTimeRange()
    timeRange.value = normalized
    persistTimeRange(normalized)
  }

  /** 切换探测口径后立即重载：英雄区数字要跟上新口径（对拍场景下用户就
   *  停在这页等两边数字相等）。切换时清掉本地 SSE 增量的痕迹——
   *  appliedTerminalIds 里记的是业务请求，含探测口径下它们的贡献不成立。 */
  function setIncludeProbes(next: boolean) {
    if (includeProbes.value === next) return
    persistProbeScope(next)
    appliedTerminalIds.clear()
    void load()
  }

  watch(liveUpdatesEnabled, (enabled) => {
    if (enabled && refreshActive) {
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
    includeProbes,
    loading,
    error,
    board,
    operational,
    load,
    loadOperational,
    setTimeRange,
    setIncludeProbes,
    startAutoRefresh,
    stopAutoRefresh,
  }
}
