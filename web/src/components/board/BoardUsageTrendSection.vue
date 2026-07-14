<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TrendLineChart from '../analytics/TrendLineChart.vue'
import ProviderUsageExplorer from '../ProviderUsageExplorer.vue'
import type { BoardPayload } from '../../api/board'

const props = defineProps<{
  board: BoardPayload | null | undefined
  days: number
  loading?: boolean
}>()

const { t } = useI18n()
const explorerOpen = ref(false)

const summary = computed(() => props.board?.summary)

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
      <div>
        <h3 class="section-title">{{ t('dashboard.board.trendTitle') }}</h3>
        <p class="section-sub">{{ t('dashboard.providerUsage.periodHint', { days }) }}</p>
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

    <ProviderUsageExplorer :open="explorerOpen" :days="days" @close="explorerOpen = false" />
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
.section-title { margin: 0; font-size: 15px; font-weight: 600; }
.section-sub { margin: 4px 0 0; font-size: 12px; color: var(--text-muted); }
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
}
</style>
