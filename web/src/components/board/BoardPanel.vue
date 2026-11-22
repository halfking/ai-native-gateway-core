<script setup lang="ts">
// BoardPanel.vue — 看板总装（2026-09-30 重构轮，对齐 docs/design 效果图 A 节）。
// 结构：运维状态条 → 英雄区 4 卡 → 次要指标 8 卡 → 供应商成本采购区 →
//       全局筛选条（统一 KxDateRangePicker）→ 模型分布 + 用量趋势 → 分布分析排行卡。
// 时间范围契约不变（BoardTimeRange/localStorage 偏好/SSE 门控），新增预设（昨天/14天/本月/上月）
// 一律映射为 custom{start,end} 走既有 start/end 查询通道。
import { inject, computed, type Ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import BoardOpsBar from './BoardOpsBar.vue'
import BoardHeroRow from './BoardHeroRow.vue'
import BoardMiniRow from './BoardMiniRow.vue'
import BoardProviderSection from './BoardProviderSection.vue'
import BoardFilterBar from './BoardFilterBar.vue'
import BoardModelDist from './BoardModelDist.vue'
import BoardUsageTrendSection from './BoardUsageTrendSection.vue'
import BoardDistGrid from './BoardDistGrid.vue'
import PageHelpDrawer from '../help/PageHelpDrawer.vue'
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
  includeProbes?: Ref<boolean>
  loading: Ref<boolean>
  load: () => Promise<void>
  setTimeRange: (next: BoardTimeRange) => void
  setIncludeProbes?: (next: boolean) => void
  startAutoRefresh: () => Promise<void>
}>('dashboardBoard')

if (!boardState) {
  throw new Error('dashboardBoard inject missing — mount BoardPanel under DashboardView')
}

const router = useRouter()
const { t } = useI18n()

// 2026-11-23 统计标准轮：页面内嵌的「统计标准」帮助入口。
// 看板与「会话与统计」的请求数/费用口径不同（请求级 vs 会话级）、窗口不同、
// 探测剔除面不同 ⇒ 两页数字本来就不该相等。这枚按钮打开
// 《看板与会话统计的统计标准》正文（含两页逐项口径对照表）。
const statsStandardsHelpId = 'dashboard-stats-standards'

const operational = computed(() => boardState.operational?.value ?? null)
const board = computed(() => boardState.board.value)
const timeRange = computed(() => boardState.timeRange?.value ?? defaultBoardTimeRange())
// 2026-11-20 口径对齐轮：英雄区「含/不含探测」切换（对拍内部对账 vs 供应商对账）。
const includeProbes = computed(() => boardState.includeProbes?.value ?? false)

function onProbeScopeChange(next: boolean) {
  if (!boardState) return
  boardState.setIncludeProbes?.(next)
}
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

// 时区审计轮（2026-11-21）F2：日期串出口改系统时区日历（原先 toISOString
// 硬编码 UTC ⇒ +08 下面板显示与深链带出的 start/end 全是 UTC 日历，与后端
// 新解析口径错位一天）。导入处统一走 systemTzDateStr。
import { systemTzDateStr as boardDateStr } from '@/components/ui/kxDatePresets'

function utcDateStr(ms: number): string {
  return boardDateStr(ms)
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
    start: boardDateStr(startMs),
    end: boardDateStr(Math.max(startMs, endMs - 1)),
  }
})
</script>

<template>
  <div class="board-panel">
    <!-- 2026-11-20 合并行轮（owner）：筛选条与运维状态（后台任务/系统自检）合成一行
         ——顶部条隐藏「数据源」徽标，行尾交给 BoardOpsBar 的两枚 chips，
         压掉独立 ops-bar 行，降低标题区高度。
         2026-11-19 二轮契约不变：本条与下方（模型分布块上方）的筛选条是同一份
         BoardTimeRange 状态的两个入口，任一处改范围两处与全部数据同步。 -->
    <BoardFilterBar
      compact
      hide-source
      :time-range="timeRange"
      :range-value="rangeValue"
      :loading="loading"
      :source="board?.source"
      :include-probes="includeProbes"
      @apply-range="onApplyRange"
      @refresh="boardState.load()"
      @more="router.push({ path: '/admin/usage-trends', query: trendExplorerQuery })"
      @update:include-probes="onProbeScopeChange"
    >
      <template #ops>
        <BoardOpsBar :operational="operational" @open-selfcheck="router.push('/dashboard?tab=selfcheck')" />
      </template>
      <!-- 2026-11-23 统计标准轮：只挂在顶部（紧凑）这条筛选条上——
           同一个筛选条在本页被实例化两次，两处都放会重复。 -->
      <template #help>
        <PageHelpDrawer
          route-path="/dashboard"
          :content-id="statsStandardsHelpId"
          :label="t('pageHelp.button.standards')"
        />
      </template>
    </BoardFilterBar>
    <BoardHeroRow :board="board" :loading="loading" @open-errors="scrollToDist" />
    <BoardMiniRow :board="board" :time-range="timeRange" :loading="loading" />
    <BoardProviderSection :board="board" :time-range="timeRange" :time-query="timeQuery" :loading="loading" />
    <!-- 2026-11-20 口径对齐轮：探测口径切换两条筛选条都有（同一份状态的两个
         入口，与 BoardTimeRange 同一纪律）；1440 视口实测顶部行仍单行。 -->
    <BoardFilterBar
      :time-range="timeRange"
      :range-value="rangeValue"
      :loading="loading"
      :source="board?.source"
      :include-probes="includeProbes"
      @apply-range="onApplyRange"
      @refresh="boardState.load()"
      @more="router.push({ path: '/admin/usage-trends', query: trendExplorerQuery })"
      @update:include-probes="onProbeScopeChange"
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
