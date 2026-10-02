<script setup lang="ts">
// ModelTrendChart.vue — 按模型拆分的用量趋势多线图（2026-10-02 看板轮）。
// 看板「用量趋势」卡与全页用量趋势视图共用：单指标多模型线，
// '__others__'（长尾折叠线）灰色虚线垫底；legend 点击可隐藏单模型。
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { ElLoadingDirective } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useChart, createTimeSeriesConfig, generateColors } from '../../composables/useChart'
import type { UsageTrendMetric, UsageTrendModelSeries } from '../../api/usage'
import {
  USAGE_TREND_OTHERS,
  compactTickValue,
  pivotUsageTrendSeries,
} from '../../utils/usageTrend'

// v-loading is not globally registered in this project — bind the directive locally.
const vLoading = ElLoadingDirective

const props = withDefaults(defineProps<{
  series: UsageTrendModelSeries[]
  metric: UsageTrendMetric
  bucketMinutes: number
  loading?: boolean
  /** 画布高度（px），看板卡 280 / 全页 460。 */
  height?: number
}>(), {
  loading: false,
  height: 280,
})

const { t } = useI18n()
const canvasRef = ref<HTMLCanvasElement | null>(null)

const pivot = computed(() => pivotUsageTrendSeries(props.series ?? [], props.metric, props.bucketMinutes))

// legend 隐藏态按模型名记忆：initChart 是销毁重建（useChart 实现），数据刷新
// （如看板卡 60s 节流跟随）后默认隐藏态全丢——「只看某模型」会被打回。
// 这里在重建后回放隐藏集，跨刷新保住用户的聚焦选择。
const hiddenModels = ref(new Set<string>())

// ChartDataset 未覆盖的 chart.js 透传属性（虚线/点半径）在此扩展，避免字面量
// excess-property 检查报错；createTimeSeriesConfig 展开时原样透传。
type ModelTrendDataset = Parameters<typeof createTimeSeriesConfig>[2][number] & {
  borderDash?: number[]
  pointRadius?: number
  pointHitRadius?: number
}

const chartConfig = computed(() => {
  const colors = generateColors(pivot.value.rows.length)
  const datasets: ModelTrendDataset[] = pivot.value.rows.map((row, i) => {
    const isOthers = row.model === USAGE_TREND_OTHERS
    const color = isOthers ? '#95a5a6' : colors[i % colors.length]
    return {
      label: isOthers ? t('dashboard.board.trendOthers') : row.model,
      data: row.data,
      borderColor: color,
      backgroundColor: `${color}1a`,
      yAxisID: 'y',
      borderDash: isOthers ? [6, 4] : undefined,
      pointRadius: 0,
      pointHitRadius: 8,
    }
  })
  return createTimeSeriesConfig('line', pivot.value.labels, datasets, {
      scales: {
        y: {
          type: 'linear',
          position: 'left',
          beginAtZero: true,
          ticks: { callback: (v: string | number) => compactTickValue(Number(v)) },
        },
      },
      plugins: {
        legend: {
          // 默认 onClick 只改 meta.hidden，重建后即丢；这里同步进 hiddenModels。
          onClick: (_e: unknown, legendItem: { datasetIndex?: number }, legend: { chart: { getDatasetMeta: (i: number) => { hidden: null | boolean }; data: { datasets: unknown[] }; update: (mode?: string) => void } }) => {
            const idx = Number(legendItem.datasetIndex)
            const model = pivot.value.rows[idx]?.model
            if (!model) return
            const set = new Set(hiddenModels.value)
            if (set.has(model)) {
              set.delete(model)
            } else {
              set.add(model)
            }
            hiddenModels.value = set
            const meta = legend.chart.getDatasetMeta(idx)
            meta.hidden = set.has(model) ? true : null
            legend.chart.update()
          },
        },
        tooltip: {
          callbacks: {
            label: (ctx: { datasetIndex: number; parsed: { y: number }; dataset: { label?: string } }) =>
              `${ctx.dataset.label}: ${compactTickValue(ctx.parsed.y)}`,
          },
        },
      },
    })
})

const { chartInstance, initChart, destroyChart, isDisposed } = useChart(canvasRef, chartConfig)

let alive = true
const hasData = computed(() => (props.series?.length ?? 0) > 0)

// initChart 销毁重建后，把隐藏集回放到新实例的 dataset meta 上。
function applyHiddenState() {
  const chart = chartInstance.value
  if (!chart) return
  pivot.value.rows.forEach((row, i) => {
    const meta = chart.getDatasetMeta(i)
    meta.hidden = hiddenModels.value.has(row.model)
  })
  chart.update('none')
}

async function refreshChart() {
  if (!alive || isDisposed()) return
  if (!hasData.value) {
    destroyChart()
    return
  }
  await nextTick()
  if (!alive || isDisposed()) return
  initChart()
  applyHiddenState()
}

watch(chartConfig, () => void refreshChart(), { deep: true })
watch(() => [props.series?.length, props.metric, props.bucketMinutes], () => void refreshChart())
onMounted(() => void refreshChart())
onBeforeUnmount(() => {
  alive = false
  destroyChart()
})
</script>

<template>
  <!-- 画布高度走 CSS var + !important（与 TrendLineChart 同模式）：
       Chart.js responsive 会改写 canvas 内联 style，只有 !important 声明压得住。 -->
  <div class="mtc" v-loading="loading" :style="{ '--mtc-h': `${props.height}px`, minHeight: `${props.height + 16}px` }">
    <canvas v-show="hasData" ref="canvasRef" />
    <div v-if="!loading && !hasData" class="mtc__empty" :style="{ paddingTop: `${props.height / 2.6}px` }">
      {{ t('dashboard.board.empty') }}
    </div>
  </div>
</template>

<style scoped>
.mtc {
  position: relative;
  width: 100%;
  min-width: 0;
}
.mtc canvas {
  width: 100% !important;
  height: var(--mtc-h, 280px) !important;
  display: block;
}
.mtc__empty {
  color: var(--text-muted);
  text-align: center;
  padding-bottom: 40px;
}
</style>
