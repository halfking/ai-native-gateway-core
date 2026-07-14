<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useChart, createDoughnutConfig, generateColors } from '../../composables/useChart'
import type { BoardPieItem } from '../../api/board'

const props = defineProps<{
  title: string
  data: BoardPieItem[]
  metric?: 'requests' | 'tokens'
  loading?: boolean
  truncateKeys?: boolean
}>()

const emit = defineEmits<{
  sliceClick: [key: string]
}>()

const metric = computed(() => props.metric ?? 'requests')
const canvasRef = ref<HTMLCanvasElement | null>(null)

function labelFor(key: string) {
  if (!props.truncateKeys || key.length <= 12) return key
  return `${key.slice(0, 8)}…${key.slice(-4)}`
}

const chartConfig = computed(() => {
  const items = props.data ?? []
  const values = items.map((i) => (metric.value === 'tokens' ? i.tokens : i.requests))
  const labels = items.map((i) => labelFor(i.key))
  return createDoughnutConfig(labels, values, generateColors(items.length), {
    onClick: (_e, elements) => {
      if (!elements?.length) return
      const idx = elements[0].index
      const key = items[idx]?.key
      if (key) emit('sliceClick', key)
    },
  })
})

const { initChart, destroyChart } = useChart(canvasRef, chartConfig)

watch(chartConfig, () => initChart(), { deep: true })
onMounted(() => initChart())
onBeforeUnmount(() => destroyChart())

const hasData = computed(() => (props.data?.length ?? 0) > 0)
</script>

<template>
  <div class="pie-card">
    <div class="pie-card__header">
      <span class="pie-card__title">{{ title }}</span>
      <slot name="metric-toggle" />
    </div>
    <div v-loading="loading" class="pie-card__body">
      <canvas v-show="hasData" ref="canvasRef" />
      <div v-if="!loading && !hasData" class="pie-card__empty">{{ $t('dashboard.board.empty') }}</div>
    </div>
  </div>
</template>

<style scoped>
.pie-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  min-height: 280px;
  display: flex;
  flex-direction: column;
}
.pie-card__header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}
.pie-card__title {
  font-weight: 600;
  font-size: 14px;
}
.pie-card__body {
  flex: 1;
  min-height: 220px;
  position: relative;
}
.pie-card__body canvas {
  width: 100% !important;
  height: 220px !important;
}
.pie-card__empty {
  color: var(--text-muted);
  text-align: center;
  padding: 48px 12px;
  font-size: 13px;
}
</style>
