import { ref, onUnmounted } from 'vue'
import { fetchDashboardBoard, type BoardPayload } from '../api/board'
import { getSetting } from '../api/settings'

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

  async function load(options?: { silent?: boolean }) {
    const silent = options?.silent === true
    if (!silent) {
      loading.value = true
    }
    error.value = null
    try {
      board.value = await fetchDashboardBoard({
        days: days.value,
      })
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
      if (!silent) {
        loading.value = false
      }
    }
  }

  async function startAutoRefresh() {
    if (refreshTimer) clearInterval(refreshTimer)
    refreshMs = await resolveRefreshMs()
    refreshTimer = window.setInterval(() => void load({ silent: true }), refreshMs)
  }

  function stopAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer)
      refreshTimer = undefined
    }
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
