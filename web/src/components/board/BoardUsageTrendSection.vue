<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TrendLineChart from '../analytics/TrendLineChart.vue'
import ProviderUsageExplorer from '../ProviderUsageExplorer.vue'
import BoardPeriodSelector from './BoardPeriodSelector.vue'
import type { BoardPayload } from '../../api/board'
import type { BoardTimeRange } from '../../utils/boardTimeRange'
import { formatBoardRangeLabel, toBoardTimeQuery } from '../../utils/boardTimeRange'

const props = defineProps<{
  board: BoardPayload | null | undefined
  timeRange: BoardTimeRange
  loading?: boolean
}>()

const emit = defineEmits<{
  'update:timeRange': [value: BoardTimeRange]
  'time-range-change': [value: BoardTimeRange]
}>()

const { t } = useI18n()
const explorerOpen = ref(false)

const summary = computed(() => props.board?.summary)
const periodLabel = computed(() => formatBoardRangeLabel(props.timeRange, t))
const timeQuery = computed(() => toBoardTimeQuery(props.timeRange))

function onRangeChange(next: BoardTimeRange) {
  emit('update:timeRange', next)
  emit('time-range-change', next)
}

function fmt(n: number | undefined) {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toLocaleString()
}

function fmtCost(v: number | undefined) {
  if (v == null) return '—'
  return '$' + Number(v).toFixed(4)
}
</script>

<template>
  <section class="usage-trend-section">
    <div class="section-head">
      <div class="section-head-left">
        <div class="title-row">
          <h3 class="section-title">{{ t('dashboard.board.trendTitle') }}</h3>
          <BoardPeriodSelector
            :model-value="timeRange"
            @update:model-value="onRangeChange"
            @change="onRangeChange"
          />
        </div>
        <p class="section-sub">{{ t('dashboard.providerUsage.periodHint', { period: periodLabel }) }}</p>
      </div>
      <button type="button" class="btn-more" @click="explorerOpen = true">
        {{ t('dashboard.providerUsage.more') }} →
      </button>
    </div>

    <div v-if="summary" class="period-stats">
      <div class="period-stat">
        <span>{{ t('dashboard.stat.totalRequests') }}</span>
        <strong>{{ fmt(summary.total_requests) }}</strong>
      </div>
      <div class="period-stat">
        <span>{{ t('dashboard.v2.totalTokensShort') }}</span>
        <strong>{{ fmt(summary.total_tokens ?? (summary.total_prompt_tokens ?? 0) + (summary.total_completion_tokens ?? 0)) }}</strong>
      </div>
      <div class="period-stat">
        <span>{{ t('dashboard.stat.totalCost') }}</span>
        <strong>{{ fmtCost(summary.total_cost_usd) }}</strong>
      </div>
      <div class="period-stat">
        <span>{{ t('dashboard.v2.totalCredits') }}</span>
        <strong>{{ fmt(summary.total_credits_charged) }}</strong>
      </div>
      <div class="period-stat">
        <span>{{ t('dashboard.stat.successRate') }}</span>
        <strong>{{ summary.success_rate != null ? (summary.success_rate * 100).toFixed(1) + '%' : '—' }}</strong>
      </div>
    </div>

    <TrendLineChart :data="board?.trends ?? []" :loading="loading" />

    <ProviderUsageExplorer
      :open="explorerOpen"
      :time-range="timeRange"
      :time-query="timeQuery"
      @close="explorerOpen = false"
    />
  </section>
</template>

<style scoped>
.usage-trend-section {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 12px;
}
.section-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}
.section-head-left {
  flex: 1;
  min-width: 0;
}
.title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}
.section-title { margin: 0; font-size: 15px; font-weight: 600; }
.section-sub { margin: 6px 0 0; font-size: 12px; color: var(--text-muted); }
.btn-more {
  flex-shrink: 0;
  font-size: 13px;
  padding: 6px 12px;
  border-radius: 8px;
  border: 1px solid var(--accent);
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  color: var(--accent);
  cursor: pointer;
  font-weight: 600;
}
.period-stats {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 8px;
}
.period-stat {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--text-muted);
}
.period-stat strong { font-size: 15px; color: var(--text); }
@media (max-width: 900px) {
  .period-stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .title-row { flex-direction: column; align-items: flex-start; }
}
</style>
