<script setup lang="ts">
// BoardUsageTrendSection.vue — 用量趋势卡（2026-09-30 重构轮精简）。
// 时间范围选择上移至 BoardFilterBar；周期 5 指标与英雄区重复已删；
// ProviderUsageExplorer 移至 BoardProviderSection（更多入口在成本采购区）。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TrendLineChart from '../analytics/TrendLineChart.vue'
import type { BoardPayload } from '../../api/board'
import { boardTrendBucketMinutes, formatBoardRangeLabel, type BoardTimeRange } from '../../utils/boardTimeRange'

const props = defineProps<{
  board: BoardPayload | null | undefined
  timeRange: BoardTimeRange
  loading?: boolean
}>()

const { t } = useI18n()

const periodLabel = computed(() => formatBoardRangeLabel(props.timeRange, t))

const bucketLabel = computed(() => {
  const mins = boardTrendBucketMinutes(props.timeRange)
  if (mins >= 60) return t('dashboard.board.granHour')
  return t('dashboard.board.granMinutes', { n: mins })
})
</script>

<template>
  <section class="trend-sec">
    <div class="trend-sec__head">
      <h5 class="trend-sec__title">{{ t('dashboard.board.trendTitle') }}</h5>
      <span class="trend-sec__cs">{{ periodLabel }} · {{ bucketLabel }}</span>
    </div>
    <TrendLineChart :data="board?.trends ?? []" :loading="loading" />
  </section>
</template>

<style scoped>
.trend-sec {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 13px 15px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.trend-sec__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}
.trend-sec__title {
  font-size: 13.5px;
  font-weight: 700;
  margin: 0;
}
.trend-sec__cs {
  font-size: 11px;
  color: var(--text-muted);
}
</style>
