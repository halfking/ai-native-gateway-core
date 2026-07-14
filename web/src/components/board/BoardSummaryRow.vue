<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatLatency } from '../../types/swimlane'
import type { BoardSummary } from '../../api/board'

defineProps<{
  summary: BoardSummary | null | undefined
}>()

const { t } = useI18n()

function fmt(n: number | undefined, decimals = 0) {
  if (n === undefined || n === null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toFixed(decimals)
}

function fmtCost(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return '$' + Number(v).toFixed(4)
}

function fmtPct(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}
</script>

<template>
  <div v-if="summary" class="stats-row">
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.stat.totalRequests') }}</div>
      <div class="stat-mini__value">{{ fmt(summary.total_requests) }}</div>
    </div>
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.v2.totalTokensShort') }}</div>
      <div class="stat-mini__value">{{ fmt(summary.total_tokens ?? (summary.total_prompt_tokens ?? 0) + (summary.total_completion_tokens ?? 0)) }}</div>
    </div>
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.stat.totalCost') }}</div>
      <div class="stat-mini__value">{{ fmtCost(summary.total_cost_usd) }}</div>
    </div>
    <div class="stat-mini stat-mini--highlight">
      <div class="stat-mini__label">{{ t('dashboard.v2.totalCredits') }}</div>
      <div class="stat-mini__value">{{ fmt(summary.total_credits_charged) }}</div>
      <div class="stat-mini__sub">{{ t('dashboard.v2.creditsSub') }}</div>
    </div>
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.stat.successRate') }}</div>
      <div class="stat-mini__value" :style="{ color: (summary.success_rate ?? 1) > 0.95 ? 'var(--success)' : 'var(--warning)' }">
        {{ fmtPct(summary.success_rate) }}
      </div>
    </div>
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.performance.avgLatency') }}</div>
      <div class="stat-mini__value">{{ formatLatency(summary.avg_latency_ms) || '—' }}</div>
    </div>
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.v2.quickApiKey') }}</div>
      <div class="stat-mini__value">{{ fmt(summary.active_api_keys) }}</div>
    </div>
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.v2.modelCount') }}</div>
      <div class="stat-mini__value">{{ fmt(summary.active_models) }}</div>
    </div>
    <div class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.stat.providers') }}</div>
      <div class="stat-mini__value">{{ fmt(summary.providers) }}</div>
    </div>
  </div>
</template>

<style scoped>
.stats-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.stat-mini {
  flex: 1 1 100px;
  min-width: 90px;
  padding: 10px 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.stat-mini--highlight {
  border-color: var(--accent);
}
.stat-mini__label {
  font-size: 11px;
  color: var(--text-muted);
  margin-bottom: 4px;
}
.stat-mini__value {
  font-size: 18px;
  font-weight: 600;
}
.stat-mini__sub {
  font-size: 10px;
  color: var(--text-muted);
  margin-top: 2px;
}
</style>
