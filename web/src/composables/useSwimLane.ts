// useSwimLane.ts — thin client-side view state for server-built lanes.
// The backend owns grouping, TopN and summary aggregation. This composable
// only selects the active dimension and exposes the server snapshot to Vue.

import { computed, ref, type ComputedRef } from 'vue'
import type { GroupByDimension, SwimLane, SwimLaneMode } from '../types/swimlane'
import type { LiveRequest, LiveStreamSnapshot, LiveStreamLegendItem } from './liveStreamStore'
import { readLiveStreamPreferences, writeLiveStreamPreferences } from './liveStreamPreferences'

const emptySnapshot: LiveStreamSnapshot = {
  summary: { total: 0, success: 0, failure: 0, in_progress: 0 },
  dimensions: { credential: [], vendor: [], provider: [], model: [] },
  dimension_legends: { credential: [], vendor: [], provider: [], model: [] },
  status_legends: [],
}

export function useSwimLane(snapshotRef?: ComputedRef<LiveStreamSnapshot | null>) {
  // Restore the user's overview preference before the first render.
  const savedPreferences = readLiveStreamPreferences()
  const groupBy = ref<GroupByDimension>(savedPreferences.groupBy)
  const selectedLegends = ref<Set<string>>(new Set(savedPreferences.selectedLegends))
  const localSnapshot = ref<LiveStreamSnapshot>(emptySnapshot)
  const mode = ref<SwimLaneMode>(savedPreferences.mode)

  function setMode(next: SwimLaneMode) {
    mode.value = next
    writeLiveStreamPreferences({ mode: next })
  }

  const snapshot = computed(() => snapshotRef?.value || localSnapshot.value)
  // 2026-08-14 V3.2: 'queue' 维度不使用泳道，返回空数组
  const lanes = computed<SwimLane[]>(() => {
    if (groupBy.value === 'queue') return []
    return (snapshot.value.dimensions[groupBy.value] || []) as SwimLane[]
  })
  // 2026-08-29: 筛选可选项数据源。'queue' 维度下 lanes 故意为空（泳道区不渲染，
  // 改由 QueuePerspectivePanel 展示），但如果筛选弹窗仍从 lanes 取可选项，
  // 按处理队列视图点开筛选时模型/供应商/原厂/客户端列表会全空。
  // 因此 queue 维度聚合四个维度的全部泳道作为可选项来源，非 queue 维度沿用当前维度 lanes。
  const filterSourceLanes = computed<SwimLane[]>(() => {
    if (groupBy.value !== 'queue') return lanes.value
    const dims = snapshot.value.dimensions
    return [
      ...(dims.credential || []),
      ...(dims.vendor || []),
      ...(dims.provider || []),
      ...(dims.model || []),
    ] as SwimLane[]
  })
  const legendItems = computed<LiveStreamLegendItem[]>(() => {
    if (groupBy.value === 'queue') return []
    return snapshot.value.dimension_legends[groupBy.value] || []
  })
  const statusLegendItems = computed<LiveStreamLegendItem[]>(() => snapshot.value.status_legends || [])
  const dimensionStats = computed(() => ({
    credential: (snapshot.value.dimension_legends.credential || []).map(toDimensionStat),
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
    writeLiveStreamPreferences({ groupBy: dimension })
  }

  function toggleLegend(key: string) {
    const next = new Set(selectedLegends.value)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    selectedLegends.value = next
    writeLiveStreamPreferences({ selectedLegends: Array.from(next) })
  }

  function clearLegendSelection() {
    selectedLegends.value = new Set()
    writeLiveStreamPreferences({ selectedLegends: [] })
  }

  function applySnapshot(next: LiveStreamSnapshot | null) {
    localSnapshot.value = next || emptySnapshot
  }

  return {
    groupBy,
    mode,
    setMode,
    lanes,
    filterSourceLanes,
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
