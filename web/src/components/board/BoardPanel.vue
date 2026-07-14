<script setup lang="ts">
import { inject, type Ref } from 'vue'
import BoardSummaryRow from './BoardSummaryRow.vue'
import BoardStatusCards from './BoardStatusCards.vue'
import BoardPieGrid from './BoardPieGrid.vue'
import BoardUsageTrendSection from './BoardUsageTrendSection.vue'
import type { BoardPayload } from '../../api/board'

const boardState = inject<{
  board: Ref<BoardPayload | null>
  days: Ref<number>
  loading: Ref<boolean>
  load: () => Promise<void>
}>('dashboardBoard')!

const dashboardTab = inject<{
  switchTab: (tab: 'board' | 'stream' | 'stats' | 'selfcheck') => void
}>('dashboardTab')!
</script>

<template>
  <div class="board-panel">
    <BoardStatusCards :board="boardState.board.value" @open-selfcheck="dashboardTab.switchTab('selfcheck')" />
    <BoardSummaryRow :summary="boardState.board.value?.summary" />
    <BoardUsageTrendSection
      :board="boardState.board.value"
      :days="boardState.days.value"
      :loading="boardState.loading.value"
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
