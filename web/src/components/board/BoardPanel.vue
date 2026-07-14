<script setup lang="ts">
import { inject, computed, type Ref } from 'vue'
import BoardSummaryRow from './BoardSummaryRow.vue'
import BoardStatusCards from './BoardStatusCards.vue'
import BoardPieGrid from './BoardPieGrid.vue'
import BoardUsageTrendSection from './BoardUsageTrendSection.vue'
import type { BoardPayload, BoardOperationalPayload } from '../../api/board'
import { defaultBoardTimeRange, type BoardTimeRange } from '../../utils/boardTimeRange'

const boardState = inject<{
  board: Ref<BoardPayload | null>
  operational?: Ref<BoardOperationalPayload | null>
  days: Ref<number>
  timeRange: Ref<BoardTimeRange>
  loading: Ref<boolean>
  load: () => Promise<void>
  setTimeRange: (next: BoardTimeRange) => void
  startAutoRefresh: () => Promise<void>
}>('dashboardBoard')

if (!boardState) {
  throw new Error('dashboardBoard inject missing — mount BoardPanel under DashboardView')
}

const dashboardTab = inject<{
  switchTab: (tab: 'board' | 'stream' | 'stats' | 'selfcheck') => void
}>('dashboardTab')!

const operational = computed(() => boardState.operational?.value ?? null)
const board = computed(() => boardState.board.value)
const timeRange = computed(() => boardState.timeRange?.value ?? defaultBoardTimeRange())
const days = computed(() => boardState.days?.value ?? 1)
const loading = computed(() => boardState.loading?.value ?? false)

async function onTimeRangeChange(next: BoardTimeRange) {
  if (!boardState) return
  boardState.setTimeRange(next)
  await boardState.load()
  await boardState.startAutoRefresh()
}
</script>

<template>
  <div class="board-panel">
    <BoardStatusCards :operational="operational" @open-selfcheck="dashboardTab.switchTab('selfcheck')" />
    <BoardSummaryRow :summary="board?.summary" :loading="loading" />
    <BoardUsageTrendSection
      :board="board"
      :time-range="timeRange"
      :loading="loading"
      @time-range-change="onTimeRangeChange"
    />
    <BoardPieGrid
      :board="board"
      :days="days"
      :loading="loading"
    />
  </div>
</template>

<style scoped>
.board-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
</style>
