<script setup lang="ts">
// BoardModelDist.vue — 模型分布表（2026-09-30 重构轮）。
// 数据源 = board.pies.models。列是模型 / 请求 / Token / 占比（效果图 A 节，不另造费用列）。
// 按当前指标排序取 Top 6，附占比条（相对 Top1）；保留「请求数 / Token 数」指标切换。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BoardPayload, BoardPieItem } from '../../api/board'

const props = defineProps<{
  board: BoardPayload | null | undefined
  loading?: boolean
}>()

const { t } = useI18n()
const metric = ref<'requests' | 'tokens'>('requests')

const rows = computed(() => {
  const items: BoardPieItem[] = [...(props.board?.pies?.models ?? [])]
  items.sort((a, b) => (b[metric.value] ?? 0) - (a[metric.value] ?? 0))
  return items.slice(0, 6)
})

const maxMetric = computed(() => Math.max(...rows.value.map((r) => r[metric.value] ?? 0), 1))

const totalMetric = computed(() => (props.board?.pies?.models ?? []).reduce((acc, p) => acc + (p[metric.value] ?? 0), 0) || 1)

function fmtCompact(n: number | undefined) {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toLocaleString()
}

</script>

<template>
  <div class="mdist">
    <div class="mdist__head">
      <h5 class="mdist__title">
        {{ t('dashboard.board.modelDistTitle') }}
        <span class="mdist__cs">{{ t('dashboard.board.modelTop', { n: 6 }) }}</span>
      </h5>
      <div class="mdist__metric" role="radiogroup" :aria-label="t('dashboard.board.metricToggle')">
        <button
          v-for="m in (['requests', 'tokens'] as const)"
          :key="m"
          type="button"
          class="mdist__metric-btn"
          :class="{ active: metric === m }"
          :aria-pressed="metric === m"
          @click="metric = m"
        >
          {{ m === 'requests' ? t('dashboard.board.metricRequests') : t('dashboard.board.metricTokens') }}
        </button>
      </div>
    </div>

    <div v-if="loading && !rows.length" class="mdist__skeleton" />
    <div v-else-if="!rows.length" class="mdist__empty">{{ t('dashboard.board.empty') }}</div>
    <div v-else class="mdist__rows">
      <div class="mdist__row mdist__row--th" aria-hidden="true">
        <span class="mdist__name">{{ t('dashboard.table.colModel') }}</span>
        <span class="mdist__num">{{ t('dashboard.providerUsage.colRequests') }}</span>
        <span class="mdist__num">{{ t('dashboard.v2.totalTokensShort') }}</span>
        <span class="mdist__num">{{ t('dashboard.board.colShare') }}</span>
      </div>
      <div v-for="row in rows" :key="row.key" class="mdist__row">
        <span class="mdist__name" :title="row.key">{{ row.key }}</span>
        <span class="mdist__num">{{ fmtCompact(row.requests) }}</span>
        <span class="mdist__num">{{ fmtCompact(row.tokens) }}</span>
        <span class="mdist__num">{{ (((row[metric] ?? 0) / totalMetric) * 100).toFixed(1) }}%</span>
        <span class="mdist__bar" aria-hidden="true">
          <i :style="{ width: Math.max(2, ((row[metric] ?? 0) / maxMetric) * 100).toFixed(1) + '%' }"></i>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mdist {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 13px 15px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.mdist__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}
.mdist__title {
  font-size: 13.5px;
  font-weight: 700;
  margin: 0;
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.mdist__cs {
  font-size: 11px;
  color: var(--text-muted);
  font-weight: 500;
}
.mdist__metric {
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: var(--bg-subtle);
}
.mdist__metric-btn {
  border: 0;
  background: transparent;
  color: var(--text-muted);
  font-size: 11.5px;
  padding: 3px 9px;
  border-radius: 5px;
  cursor: pointer;
  font-weight: 600;
}
.mdist__metric-btn.active {
  background: color-mix(in srgb, var(--accent) 14%, transparent);
  color: var(--accent);
}
.mdist__rows {
  display: flex;
  flex-direction: column;
}
.mdist__row {
  display: grid;
  grid-template-columns: minmax(150px, 1.5fr) 62px 74px 74px;
  gap: 8px;
  align-items: center;
  padding: 6px 4px;
  border-bottom: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
  font-size: 12px;
}
.mdist__row:last-child {
  border-bottom: 0;
}
.mdist__row--th {
  color: var(--text-muted);
  font-size: 10.5px;
  font-weight: 600;
  border-bottom: 1px solid var(--border);
}
.mdist__name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mdist__num {
  text-align: right;
  font-variant-numeric: tabular-nums;
  color: var(--text);
}
.mdist__row--th .mdist__num {
  color: var(--text-muted);
}
.mdist__bar {
  grid-column: 1 / -1;
  height: 3.5px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--text-muted) 15%, transparent);
  overflow: hidden;
  margin-top: -2px;
}
.mdist__bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--accent);
}
.mdist__empty {
  font-size: 12px;
  color: var(--text-muted);
  text-align: center;
  padding: 16px 0;
}
.mdist__skeleton {
  min-height: 140px;
  background: linear-gradient(90deg, var(--border) 25%, transparent 37%, var(--border) 63%);
  background-size: 400% 100%;
  animation: mdist-shimmer 1.2s ease infinite;
  border-radius: 8px;
}
@keyframes mdist-shimmer {
  0% { background-position: 100% 0; }
  100% { background-position: 0 0; }
}
@media (max-width: 768px) {
  .mdist__row {
    grid-template-columns: minmax(100px, 1.4fr) 56px 64px 56px;
  }
}
</style>
