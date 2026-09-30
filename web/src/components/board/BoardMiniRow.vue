<script setup lang="ts">
// BoardMiniRow.vue — 看板次要指标条 8 卡（2026-09-30 重构轮）。
// 平均延迟 / RPM / TPM（窗口现算）+ API Key / 模型数 / 供应商计数 + 请求体/响应体（含峰值）。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatLatency } from '../../types/swimlane'
import { formatBytes } from '../../utils/format'
import type { BoardPayload } from '../../api/board'
import { resolveBoardRangeMs, type BoardTimeRange } from '../../utils/boardTimeRange'

const props = defineProps<{
  board: BoardPayload | null | undefined
  timeRange: BoardTimeRange
  loading?: boolean
}>()

const { t } = useI18n()

const summary = computed(() => props.board?.summary)
const bodyStats = computed(() => props.board?.body_stats)

/** 窗口分钟数：预设取 utc 滚动起点→now，custom 取首日 00:00 → 末日次日 00:00。 */
const rangeMinutes = computed(() => {
  const { startMs, endMs } = resolveBoardRangeMs(props.timeRange)
  return Math.max(1, (endMs - startMs) / 60_000)
})

const rpm = computed(() => {
  const total = summary.value?.total_requests
  if (total == null) return undefined
  return total / rangeMinutes.value
})

const tpm = computed(() => {
  const s = summary.value
  const total = s?.total_tokens ?? (s ? (s.total_prompt_tokens ?? 0) + (s.total_completion_tokens ?? 0) : undefined)
  if (total == null) return undefined
  return total / rangeMinutes.value
})

function fmtCompact(n: number | undefined) {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toFixed(n < 10 ? 1 : 0)
}
</script>

<template>
  <div v-if="loading && !summary" class="mini-row">
    <div v-for="i in 8" :key="i" class="mini-card mini-card--skeleton" />
  </div>
  <div v-else-if="summary" class="mini-row">
    <div class="mini-card">
      <div class="mini-card__label">{{ t('dashboard.performance.avgLatency') }}</div>
      <div class="mini-card__value">{{ formatLatency(summary.avg_latency_ms) || '—' }}</div>
    </div>
    <div class="mini-card" :title="t('dashboard.board.rpmTitle')">
      <div class="mini-card__label">{{ t('dashboard.board.rpmLabel') }}</div>
      <div class="mini-card__value">{{ fmtCompact(rpm) }}<small> req/min</small></div>
    </div>
    <div class="mini-card" :title="t('dashboard.board.tpmTitle')">
      <div class="mini-card__label">{{ t('dashboard.board.tpmLabel') }}</div>
      <div class="mini-card__value">{{ fmtCompact(tpm) }}<small> tok/min</small></div>
    </div>
    <div class="mini-card">
      <div class="mini-card__label">{{ t('dashboard.v2.quickApiKey') }}</div>
      <div class="mini-card__value">{{ summary.active_api_keys ?? '—' }}</div>
    </div>
    <div class="mini-card">
      <div class="mini-card__label">{{ t('dashboard.v2.modelCount') }}</div>
      <div class="mini-card__value">{{ summary.active_models ?? '—' }}</div>
    </div>
    <div class="mini-card">
      <div class="mini-card__label">{{ t('dashboard.providerUsage.colName') }}</div>
      <div class="mini-card__value">{{ summary.providers ?? '—' }}</div>
    </div>
    <div class="mini-card">
      <div class="mini-card__label">{{ t('dashboard.stat.avgRequestSize') }}</div>
      <div class="mini-card__value">
        <template v-if="bodyStats">{{ formatBytes(bodyStats.avg_request_bytes) }}<small> {{ t('dashboard.stat.maxLabel') }} {{ formatBytes(bodyStats.max_request_bytes) }}</small></template>
        <template v-else>—</template>
      </div>
    </div>
    <div class="mini-card">
      <div class="mini-card__label">{{ t('dashboard.stat.avgResponseSize') }}</div>
      <div class="mini-card__value">
        <template v-if="bodyStats">{{ formatBytes(bodyStats.avg_response_bytes) }}<small> {{ t('dashboard.stat.maxLabel') }} {{ formatBytes(bodyStats.max_response_bytes) }}</small></template>
        <template v-else>—</template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mini-row {
  display: grid;
  grid-template-columns: repeat(8, 1fr);
  gap: 8px;
}
.mini-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 8px 11px;
  min-width: 0;
}
.mini-card__label {
  font-size: 10.5px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mini-card__value {
  font-size: 15px;
  font-weight: 700;
  margin-top: 2px;
  font-variant-numeric: tabular-nums;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mini-card__value small {
  font-size: 10px;
  color: var(--text-muted);
  font-weight: 500;
}
.mini-card--skeleton {
  min-height: 48px;
  background: linear-gradient(90deg, var(--border) 25%, transparent 37%, var(--border) 63%);
  background-size: 400% 100%;
  animation: mini-shimmer 1.2s ease infinite;
}
@keyframes mini-shimmer {
  0% { background-position: 100% 0; }
  100% { background-position: 0 0; }
}
@media (max-width: 1440px) {
  .mini-row { grid-template-columns: repeat(4, 1fr); }
}
@media (max-width: 768px) {
  .mini-row { grid-template-columns: repeat(2, 1fr); }
}
</style>
