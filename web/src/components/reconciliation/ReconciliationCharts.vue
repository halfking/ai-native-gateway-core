<script setup lang="ts">
// ReconciliationCharts.vue — request/cost bars + token composition (chart.js).
// Series numbers come from reconTrendSeries so uncovered zero-fill days stay off the axis.
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { chartColors, createComboChartConfig, useChart } from '../../composables/useChart'
import { reconTrendSeries, type TrendDay } from './chartSeries'

const props = defineProps<{
  days: TrendDay[]
  coveredDates?: string[]
  money: 'cost' | 'credits'
}>()

const { t } = useI18n()
const reqCanvas = ref<HTMLCanvasElement | null>(null)
const tokCanvas = ref<HTMLCanvasElement | null>(null)

const series = computed(() => reconTrendSeries(props.days, props.coveredDates, props.money))
const hasPoints = computed(() => series.value.labels.length > 0)

const reqConfig = computed(() => createComboChartConfig('bar', series.value.labels, [
  {
    label: t('reports.legendSuccess'),
    data: series.value.success,
    type: 'bar',
    stack: 'req',
    backgroundColor: chartColors.blue,
    yAxisID: 'y',
  },
  {
    label: t('reports.legendFail'),
    data: series.value.fail,
    type: 'bar',
    stack: 'req',
    backgroundColor: chartColors.red,
    yAxisID: 'y',
  },
  {
    label: props.money === 'cost' ? t('reports.legendCost') : t('reports.legendCredits'),
    data: series.value.money,
    type: 'line',
    borderColor: chartColors.orange,
    backgroundColor: chartColors.orange,
    yAxisID: 'y1',
    pointRadius: 2,
    fill: false,
  },
], {
  scales: {
    x: { stacked: true, grid: { display: false } },
    y: { stacked: true, beginAtZero: true },
    y1: { beginAtZero: true, position: 'right', grid: { drawOnChartArea: false } },
  },
}))

const tokConfig = computed(() => createComboChartConfig('line', series.value.labels, [
  { label: t('reports.legendIn'), data: series.value.input, stack: 'tok', borderColor: chartColors.blue, backgroundColor: `${chartColors.blue}66`, fill: true, yAxisID: 'y' },
  { label: t('reports.legendOut'), data: series.value.output, stack: 'tok', borderColor: chartColors.green, backgroundColor: `${chartColors.green}66`, fill: true, yAxisID: 'y' },
  { label: t('reports.legendCacheRead'), data: series.value.cacheRead, stack: 'tok', borderColor: chartColors.cyan, backgroundColor: `${chartColors.cyan}66`, fill: true, yAxisID: 'y' },
  { label: t('reports.legendCacheWrite'), data: series.value.cacheWrite, stack: 'tok', borderColor: chartColors.purple, backgroundColor: `${chartColors.purple}66`, fill: true, yAxisID: 'y' },
  {
    label: t('reports.legendHitRate'),
    data: series.value.hit,
    type: 'line',
    borderColor: chartColors.orange,
    backgroundColor: chartColors.orange,
    borderDash: [5, 4],
    yAxisID: 'y1',
    pointRadius: 2,
    fill: false,
  },
], {
  scales: {
    x: { stacked: true, grid: { display: false } },
    y: { stacked: true, beginAtZero: true },
    y1: { beginAtZero: true, max: 100, position: 'right', grid: { drawOnChartArea: false } },
  },
}))

const reqChart = useChart(reqCanvas, reqConfig)
const tokChart = useChart(tokCanvas, tokConfig)

async function redraw() {
  await nextTick()
  // v-if, not v-show: chart.destroy() runs after this tick and restores the
  // canvas inline style, which would undo display:none and leave a blank 260px box.
  if (!hasPoints.value) {
    reqChart.destroyChart()
    tokChart.destroyChart()
    return
  }
  reqChart.initChart()
  tokChart.initChart()
}

watch([reqConfig, tokConfig], () => void redraw(), { deep: true })
onMounted(() => void redraw())
</script>

<template>
  <div class="charts">
    <section class="chart-card">
      <header>{{ money === 'cost' ? t('reports.reqCostTrend') : t('reports.reqCreditsTrend') }}</header>
      <div class="chart-box">
        <canvas v-if="hasPoints" ref="reqCanvas" />
        <p v-else class="empty">{{ t('reports.noData') }}</p>
      </div>
    </section>
    <section class="chart-card">
      <header>{{ t('reports.tokenTrend') }}</header>
      <div class="chart-box">
        <canvas v-if="hasPoints" ref="tokCanvas" />
        <p v-else class="empty">{{ t('reports.noData') }}</p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.charts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 12px;
  margin-bottom: 8px;
}
.chart-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  background: var(--card);
  min-width: 0;
}
.chart-card header { font-weight: 600; font-size: 14px; margin-bottom: 8px; }
.chart-box { position: relative; height: 260px; }
.chart-box canvas { width: 100% !important; height: 260px !important; }
.empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  margin: 0;
  color: var(--text-muted, var(--el-text-color-secondary));
  text-align: center;
}
</style>
