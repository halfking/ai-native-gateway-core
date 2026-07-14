<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useChart, createTimeSeriesConfig } from '../../composables/useChart'
import type { BoardTrendPoint } from '../../api/board'

const props = defineProps<{
  data: BoardTrendPoint[]
  loading?: boolean
}>()

const { t } = useI18n()
const canvasRef = ref<HTMLCanvasElement | null>(null)

const labels = computed(() =>
  (props.data ?? []).map((p) => {
    const d = new Date(p.bucket)
    return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }),
)

const chartConfig = computed(() =>
  createTimeSeriesConfig('line', labels.value, [
    {
      label: t('dashboard.board.trendRequests'),
      data: (props.data ?? []).map((p) => p.requests),
      borderColor: '#58a6ff',
      backgroundColor: 'rgba(88,166,255,0.1)',
      yAxisID: 'y',
    },
    {
      label: t('dashboard.board.trendTokens'),
      data: (props.data ?? []).map((p) => p.tokens),
      borderColor: '#3fb950',
      backgroundColor: 'rgba(63,185,80,0.1)',
      yAxisID: 'y1',
    },
    {
      label: t('dashboard.board.trendCredits'),
      data: (props.data ?? []).map((p) => p.credits),
      borderColor: '#d29922',
      backgroundColor: 'rgba(210,153,34,0.1)',
      yAxisID: 'y1',
    },
    {
      label: t('dashboard.board.trendCost'),
      data: (props.data ?? []).map((p) => p.cost_usd),
      borderColor: '#f85149',
      backgroundColor: 'rgba(248,81,73,0.1)',
      yAxisID: 'y1',
    },
  ], {
    scales: {
      y: { type: 'linear', position: 'left', beginAtZero: true },
      y1: { type: 'linear', position: 'right', beginAtZero: true, grid: { drawOnChartArea: false } },
    },
  }),
)

const { initChart, destroyChart } = useChart(canvasRef, chartConfig)

async function refreshChart() {
  if (!hasData.value) {
    destroyChart()
    return
  }
  await nextTick()
  initChart()
}

watch(chartConfig, () => void refreshChart(), { deep: true })
watch(() => props.data?.length, () => void refreshChart())
onMounted(() => void refreshChart())
onBeforeUnmount(() => destroyChart())

const hasData = computed(() => (props.data?.length ?? 0) > 0)
</script>

<template>
  <div class="trend-card">
    <div class="trend-card__header">
      <span class="trend-card__title">{{ $t('dashboard.board.trendTitle') }}</span>
      <slot name="filters" />
    </div>
    <div v-loading="loading" class="trend-card__body">
      <canvas v-show="hasData" ref="canvasRef" />
      <div v-if="!loading && !hasData" class="trend-card__empty">{{ $t('dashboard.board.empty') }}</div>
    </div>
  </div>
</template>

<style scoped>
.trend-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
}
.trend-card__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  flex-wrap: wrap;
  gap: 8px;
}
.trend-card__title {
  font-weight: 600;
  font-size: 14px;
}
.trend-card__body {
  min-height: 280px;
}
.trend-card__body canvas {
  width: 100% !important;
  height: 280px !important;
}
.trend-card__empty {
  color: var(--text-muted);
  text-align: center;
  padding: 80px 12px;
}
</style>
