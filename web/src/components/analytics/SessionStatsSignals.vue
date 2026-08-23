<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ErrorSummary, PerformanceSummary } from '../../api/dashboard'

defineProps<{
  pipeline: PerformanceSummary | null
  errors: ErrorSummary | null
}>()

const { t } = useI18n()

function formatMs(ms: number) {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`
  return `${Math.round(ms)}ms`
}
</script>

<template>
  <div class="signal-row">
    <div class="detail-card">
      <div class="detail-card__header">
        {{ t('sessions.stats.pipelineLatency') }}
        <span class="detail-card__hint">{{ t('sessions.stats.pipelineLatencyHint') }}</span>
      </div>
      <template v-if="pipeline && (pipeline.total_requests > 0 || pipeline.avg_latency_ms > 0)">
        <div class="signal-metrics">
          <div><span class="signal-label">p50</span><span class="signal-value">{{ formatMs(pipeline.p50_latency_ms) }}</span></div>
          <div><span class="signal-label">p95</span><span class="signal-value">{{ formatMs(pipeline.p95_latency_ms) }}</span></div>
          <div><span class="signal-label">p99</span><span class="signal-value">{{ formatMs(pipeline.p99_latency_ms) }}</span></div>
          <div><span class="signal-label">max</span><span class="signal-value">{{ formatMs(pipeline.max_latency_ms) }}</span></div>
        </div>
      </template>
      <p v-else class="empty-hint">{{ t('sessions.stats.pipelineLatencyEmpty') }}</p>
    </div>
    <div class="detail-card">
      <div class="detail-card__header">{{ t('sessions.stats.errorSummary') }}</div>
      <template v-if="errors && (errors.total_errors > 0 || errors.error_rate > 0)">
        <div class="signal-metrics">
          <div>
            <span class="signal-label">{{ t('sessions.stats.errorRate') }}</span>
            <span class="signal-value">{{ errors.error_rate.toFixed(2) }}%</span>
          </div>
          <div>
            <span class="signal-label">{{ t('sessions.stats.totalErrors') }}</span>
            <span class="signal-value">{{ errors.total_errors.toLocaleString() }}</span>
          </div>
        </div>
      </template>
      <p v-else class="empty-hint">{{ errors ? t('sessions.stats.errorsEmpty') : t('dashboard.noData') }}</p>
    </div>
  </div>
</template>

<style scoped>
.signal-row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin-bottom: 16px;
  width: 100%;
}
@media (max-width: 900px) { .signal-row { grid-template-columns: 1fr; } }
.detail-card {
  border: 1px solid var(--border, #e5e7eb);
  border-radius: 10px;
  background: var(--card, #fff);
  padding: 14px 16px;
  min-width: 0;
}
.detail-card__header {
  font-weight: 600; font-size: 14px; margin-bottom: 12px;
  display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px;
}
.detail-card__hint { font-weight: 400; font-size: 12px; color: var(--muted, #909399); }
.signal-metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(100px, 1fr)); gap: 12px; }
.signal-label { display: block; font-size: 12px; color: var(--muted, #909399); }
.signal-value { font-size: 18px; font-weight: 600; }
.empty-hint { margin: 0; font-size: 13px; color: var(--muted, #909399); }
</style>
