// useSwimLane.ts — thin client-side view state for server-built lanes.
// The backend owns grouping, TopN and summary aggregation. This composable
// only selects the active dimension and exposes the server snapshot to Vue.

import { computed, ref, type ComputedRef } from 'vue'
import type { GroupByDimension, SwimLane, SwimLaneMode } from '../types/swimlane'
import type { LiveRequest, LiveStreamSnapshot, LiveStreamLegendItem } from './liveStreamStore'

const emptySnapshot: LiveStreamSnapshot = {
  summary: { total: 0, success: 0, failure: 0, in_progress: 0 },
  detail_dimensions: { vendor: [], provider: [], model: [] },
  dimensions: { vendor: [], provider: [], model: [] },
  dimension_legends: { vendor: [], provider: [], model: [] },
  status_legends: [],
}

export function useSwimLane(snapshotRef?: ComputedRef<LiveStreamSnapshot | null>) {
  // 2026-08-14 V3.2: 默认选中"按处理队列"维度
  const groupBy = ref<GroupByDimension>('queue')
  const selectedLegends = ref<Set<string>>(new Set())
  const localSnapshot = ref<LiveStreamSnapshot>(emptySnapshot)
  // 2026-07-23: 小模式（竖条）作为默认展示模式
  const mode = ref<SwimLaneMode>('small')
  const MODE_STORAGE_KEY = 'llmgw_swimlane_mode'

  // 从 localStorage 恢复用户上次选择的模式
  try {
    const saved = localStorage.getItem(MODE_STORAGE_KEY)
    if (saved === 'small' || saved === 'large') mode.value = saved
  } catch { /* localStorage 不可用，忽略 */ }

  function setMode(next: SwimLaneMode) {
    mode.value = next
    try { localStorage.setItem(MODE_STORAGE_KEY, next) } catch { /* ignore */ }
  }

  const snapshot = computed(() => snapshotRef?.value || localSnapshot.value)
  // 2026-08-14 V3.2: 'queue' 维度不使用泳道，返回空数组
  const lanes = computed<SwimLane[]>(() => {
    if (groupBy.value === 'queue') return []
    return (snapshot.value.dimensions[groupBy.value] || []) as SwimLane[]
  })
  const legendItems = computed<LiveStreamLegendItem[]>(() => {
    if (groupBy.value === 'queue') return []
    return snapshot.value.dimension_legends[groupBy.value] || []
  })
  const statusLegendItems = computed<LiveStreamLegendItem[]>(() => snapshot.value.status_legends || [])
  const dimensionStats = computed(() => ({
    vendor: (snapshot.value.dimension_legends.vendor || []).map(toDimensionStat),
    provider: (snapshot.value.dimension_legends.provider || []).map(toDimensionStat),
    model: (snapshot.value.dimension_legends.model || []).map(toDimensionStat),
  }))

  function initializeLanes(_initialRequests: LiveRequest[]) {
    // Snapshot data arrives through liveStreamStore. Kept for component API compatibility.
  }

  function queueRequest(_req: LiveRequest) {
    // Deltas are already folded into the server snapshot included in SSE envelopes.
  }

  function setGroupBy(dimension: GroupByDimension) {
    groupBy.value = dimension
  }

  function toggleLegend(key: string) {
    const next = new Set(selectedLegends.value)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    selectedLegends.value = next
  }

  function clearLegendSelection() {
    selectedLegends.value = new Set()
  }

  function applySnapshot(next: LiveStreamSnapshot | null) {
    localSnapshot.value = next || emptySnapshot
  }

  return {
    groupBy,
    mode,
    setMode,
    lanes,
    dimensionStats,
    selectedLegends,
    legendItems,
    statusLegendItems,
    initializeLanes,
    queueRequest,
    setGroupBy,
    toggleLegend,
    clearLegendSelection,
    applySnapshot,
  }
}

function toDimensionStat(item: LiveStreamLegendItem) {
  return {
    key: item.key,
    requestCount: item.count,
    successCount: 0,
    failureCount: 0,
    lastSeen: '',
  }
}
