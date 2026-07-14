<script setup lang="ts">
import { inject, type Ref } from 'vue'
import BoardSummaryRow from './BoardSummaryRow.vue'
import BoardStatusCards from './BoardStatusCards.vue'
import BoardPieGrid from './BoardPieGrid.vue'
import TrendLineChart from '../analytics/TrendLineChart.vue'
import type { BoardPayload } from '../../api/board'

const boardState = inject<{
  board: Ref<BoardPayload | null>
  days: Ref<number>
  loading: Ref<boolean>
  filterProviderId: Ref<number | null>
  load: () => Promise<void>
}>('dashboardBoard')!

const dashboardTab = inject<{
  switchTab: (tab: 'board' | 'stream' | 'stats' | 'selfcheck') => void
}>('dashboardTab')!

function onProviderFilter(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  boardState.filterProviderId.value = v === '' ? null : Number(v)
  void boardState.load()
}
</script>

<template>
  <div class="board-panel">
    <BoardStatusCards :board="boardState.board.value" @open-selfcheck="dashboardTab.switchTab('selfcheck')" />
    <BoardSummaryRow :summary="boardState.board.value?.summary" />
    <BoardPieGrid
      :board="boardState.board.value"
      :days="boardState.days.value"
      :loading="boardState.loading.value"
    />
    <TrendLineChart :data="boardState.board.value?.trends ?? []" :loading="boardState.loading.value">
      <template #filters>
        <label class="filter-label">
          {{ $t('dashboard.board.filterProvider') }}
          <input
            type="number"
            class="filter-input"
            :placeholder="$t('dashboard.board.allProviders')"
            :value="boardState.filterProviderId.value ?? ''"
            min="0"
            @change="onProviderFilter"
          />
        </label>
      </template>
    </TrendLineChart>
  </div>
</template>

<style scoped>
.board-panel {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.filter-label {
  font-size: 12px;
  color: var(--text-muted);
  display: flex;
  align-items: center;
  gap: 6px;
}
.filter-input {
  width: 100px;
  font-size: 12px;
  padding: 4px 6px;
}
</style>
