<script setup lang="ts">
// BoardPanel.vue — 看板总装（2026-09-30 重构轮，对齐 docs/design 效果图 A 节）。
// 结构：运维状态条 → 英雄区 4 卡 → 次要指标 8 卡 → 供应商成本采购区 →
//       全局筛选条（统一 KxDateRangePicker）→ 模型分布 + 用量趋势 → 分布分析排行卡。
// 时间范围契约不变（BoardTimeRange/localStorage 偏好/SSE 门控），新增预设（昨天/14天/本月/上月）
// 一律映射为 custom{start,end} 走既有 start/end 查询通道。
import { inject, computed, type Ref } from 'vue'
import { useRouter } from 'vue-router'
import BoardOpsBar from './BoardOpsBar.vue'
import BoardHeroRow from './BoardHeroRow.vue'
import BoardMiniRow from './BoardMiniRow.vue'
import BoardProviderSection from './BoardProviderSection.vue'
import BoardFilterBar from './BoardFilterBar.vue'
import BoardModelDist from './BoardModelDist.vue'
import BoardUsageTrendSection from './BoardUsageTrendSection.vue'
import BoardDistGrid from './BoardDistGrid.vue'
import type { BoardPayload, BoardOperationalPayload } from '../../api/board'
import {
  defaultBoardTimeRange,
  resolveBoardRangeMs,
  usageQueryForBoardRange,
  type BoardTimeRange,
} from '../../utils/boardTimeRange'
import type { KxDateRange } from '../ui/kx-date-types'

const DAY_MS = 86_400_000

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

const router = useRouter()

const operational = computed(() => boardState.operational?.value ?? null)
const board = computed(() => boardState.board.value)
const timeRange = computed(() => boardState.timeRange?.value ?? defaultBoardTimeRange())
// 用量卡走日历日。看板汇总仍由 useDashboardBoard 用 days 预设，两套不要合成一个数。
const timeQuery = computed(() => usageQueryForBoardRange(timeRange.value))
const days = computed(() => boardState.days?.value ?? 1)
const loading = computed(() => boardState.loading?.value ?? false)

async function onTimeRangeChange(next: BoardTimeRange) {
  if (!boardState) return
  boardState.setTimeRange(next)
  await boardState.load()
  await boardState.startAutoRefresh()
}

// ── BoardTimeRange ↔ KxDateRange 映射 ──

function utcDateStr(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10)
}

/** 当前 BoardTimeRange → 面板展示值。 */
const rangeValue = computed<KxDateRange | null>(() => {
  const { startMs, endMs } = resolveBoardRangeMs(timeRange.value)
  return { start: utcDateStr(startMs), end: utcDateStr(endMs - 1) }
})

/** 应用新范围：能对齐内置预设的对齐（保持 SSE 门控与偏好语义），否则 custom。 */
function onApplyRange(range: KxDateRange) {
  const todayMs = resolveBoardRangeMs({ preset: 'today', days: 1 }).startMs
  const spanDays = Math.max(1, Math.round((Date.parse(`${range.end}T00:00:00Z`) - Date.parse(`${range.start}T00:00:00Z`)) / DAY_MS) + 1)
  if (range.start === utcDateStr(todayMs) && range.end === utcDateStr(todayMs)) {
    void onTimeRangeChange({ preset: 'today', days: 1 })
    return
  }
  if (range.start === utcDateStr(todayMs - 6 * DAY_MS) && range.end === utcDateStr(todayMs)) {
    void onTimeRangeChange({ preset: '7d', days: 7 })
    return
  }
  if (range.start === utcDateStr(todayMs - 29 * DAY_MS) && range.end === utcDateStr(todayMs)) {
    void onTimeRangeChange({ preset: '30d', days: 30 })
    return
  }
  void onTimeRangeChange({ preset: 'custom', days: spanDays, start: range.start, end: range.end })
}

function scrollToDist() {
  document.querySelector('.board-panel__dist')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// 「更多」入口跳全页用量趋势时，带上当前看板时间范围（start/end 深链）。
const trendExplorerQuery = computed(() => {
  const { startMs, endMs } = resolveBoardRangeMs(timeRange.value)
  return {
    start: new Date(startMs).toISOString().slice(0, 10),
    end: new Date(Math.max(startMs, endMs - 1)).toISOString().slice(0, 10),
  }
})
</script>

<template>
  <div class="board-panel">
    <BoardOpsBar :operational="operational" @open-selfcheck="router.push('/dashboard?tab=selfcheck')" />
    <BoardHeroRow :board="board" :loading="loading" @open-errors="scrollToDist" />
    <BoardMiniRow :board="board" :time-range="timeRange" :loading="loading" />
    <BoardProviderSection :board="board" :time-range="timeRange" :time-query="timeQuery" :loading="loading" />
    <BoardFilterBar
      :time-range="timeRange"
      :range-value="rangeValue"
      :loading="loading"
      :source="board?.source"
      @apply-range="onApplyRange"
      @refresh="boardState.load()"
      @more="router.push({ path: '/admin/usage-trends', query: trendExplorerQuery })"
    />
    <div class="board-panel__charts">
      <BoardModelDist :board="board" :loading="loading" />
      <BoardUsageTrendSection :board="board" :time-range="timeRange" :loading="loading" />
    </div>
    <div class="board-panel__dist">
      <BoardDistGrid :board="board" :days="days" :loading="loading" />
    </div>
  </div>
</template>

<style scoped>
.board-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.board-panel__charts {
  display: grid;
  grid-template-columns: 1fr 1.35fr;
  gap: 12px;
  align-items: stretch;
}
@media (max-width: 1024px) {
  .board-panel__charts {
    grid-template-columns: 1fr;
  }
}
</style>
