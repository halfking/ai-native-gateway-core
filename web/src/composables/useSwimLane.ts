// useSwimLane.ts — thin client-side view state for server-built lanes.
// The backend owns grouping, TopN and summary aggregation. This composable
// only selects the active dimension and exposes the server snapshot to Vue.

import { computed, ref, type ComputedRef } from 'vue'
import type { GroupByDimension, SwimLane, SwimLaneMode } from '../types/swimlane'
import type { LiveRequest, LiveStreamSnapshot, LiveStreamLegendItem } from './liveStreamStore'
import { readLiveStreamPreferences, writeLiveStreamPreferences } from './liveStreamPreferences'

const emptySnapshot: LiveStreamSnapshot = {
  summary: { total: 0, success: 0, failure: 0, in_progress: 0 },
  detail_dimensions: { vendor: [], provider: [], model: [] },
  dimensions: { vendor: [], provider: [], model: [] },
  dimension_legends: { vendor: [], provider: [], model: [] },
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
