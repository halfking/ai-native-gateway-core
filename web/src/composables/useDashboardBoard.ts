import { ref, onUnmounted } from 'vue'
import { fetchDashboardBoard, type BoardPayload } from '../api/board'

const REFRESH_MS = 5 * 60 * 1000

export function useDashboardBoard() {
  const days = ref(7)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const board = ref<BoardPayload | null>(null)
  const filterProviderId = ref<number | null>(null)

  let refreshTimer: number | undefined

  async function load() {
    loading.value = true
    error.value = null
    try {
      board.value = await fetchDashboardBoard({
        days: days.value,
        provider_id: filterProviderId.value ?? undefined,
      })
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
      loading.value = false
    }
  }

  function startAutoRefresh() {
    if (refreshTimer) clearInterval(refreshTimer)
    refreshTimer = window.setInterval(() => void load(), REFRESH_MS)
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
    filterProviderId,
    load,
    startAutoRefresh,
    stopAutoRefresh,
  }
}
