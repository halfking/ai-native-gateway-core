<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatLatency } from '../../types/swimlane'
import { formatBytes } from '../../utils/format'
import type { BoardSummary, BodySizeStats } from '../../api/board'

defineProps<{
  summary: BoardSummary | null | undefined
  bodyStats?: BodySizeStats | null
  loading?: boolean
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
  <div v-if="loading && !summary" class="stats-row stats-row--skeleton">
    <div v-for="i in 11" :key="i" class="stat-mini stat-mini--skeleton" />
  </div>
  <div v-else-if="summary" class="stats-row">
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
    <!-- 2026-07-25: Body size statistics -->
    <div v-if="bodyStats" class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.stat.avgRequestSize') }}</div>
      <div class="stat-mini__value">{{ formatBytes(bodyStats.avg_request_bytes) }}</div>
      <div class="stat-mini__sub">{{ t('dashboard.stat.maxLabel') }}: {{ formatBytes(bodyStats.max_request_bytes) }}</div>
    </div>
    <div v-if="bodyStats" class="stat-mini">
      <div class="stat-mini__label">{{ t('dashboard.stat.avgResponseSize') }}</div>
      <div class="stat-mini__value">{{ formatBytes(bodyStats.avg_response_bytes) }}</div>
      <div class="stat-mini__sub">{{ t('dashboard.stat.maxLabel') }}: {{ formatBytes(bodyStats.max_response_bytes) }}</div>
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
.stats-row--skeleton {
  min-height: 72px;
}
.stat-mini--skeleton {
  flex: 1 1 120px;
  min-height: 56px;
  background: linear-gradient(90deg, var(--border) 25%, transparent 37%, var(--border) 63%);
  background-size: 400% 100%;
  animation: board-shimmer 1.2s ease infinite;
}
@keyframes board-shimmer {
  0% { background-position: 100% 0; }
  100% { background-position: 0 0; }
}
</style>
