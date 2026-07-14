<script setup lang="ts">
import { inject, type Ref } from 'vue'
import BoardSummaryRow from './BoardSummaryRow.vue'
import BoardStatusCards from './BoardStatusCards.vue'
import BoardPieGrid from './BoardPieGrid.vue'
import BoardUsageTrendSection from './BoardUsageTrendSection.vue'
import type { BoardPayload } from '../../api/board'
import type { BoardTimeRange } from '../../utils/boardTimeRange'

const boardState = inject<{
  board: Ref<BoardPayload | null>
  days: Ref<number>
  timeRange: Ref<BoardTimeRange>
  loading: Ref<boolean>
  load: () => Promise<void>
  setTimeRange: (next: BoardTimeRange) => void
  startAutoRefresh: () => Promise<void>
}>('dashboardBoard')!

const dashboardTab = inject<{
  switchTab: (tab: 'board' | 'stream' | 'stats' | 'selfcheck') => void
}>('dashboardTab')!

async function onTimeRangeChange(next: BoardTimeRange) {
  boardState.setTimeRange(next)
  await boardState.load()
  await boardState.startAutoRefresh()
}
</script>

<template>
  <div class="board-panel">
    <BoardStatusCards :board="boardState.board.value" @open-selfcheck="dashboardTab.switchTab('selfcheck')" />
    <BoardSummaryRow :summary="boardState.board.value?.summary" />
    <BoardUsageTrendSection
      :board="boardState.board.value"
      :time-range="boardState.timeRange.value"
      :loading="boardState.loading.value"
      @time-range-change="onTimeRangeChange"
    />
    <BoardPieGrid
      :board="boardState.board.value"
      :days="boardState.days.value"
      :loading="boardState.loading.value"
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
