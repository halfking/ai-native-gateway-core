<script setup lang="ts">
/**
 * SessionTrendChart.vue
 * ECharts-based session trend visualization (new sessions / active / closed / cost).
 * Uses the useDashboard composable's trend data.
 */
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'

const { t } = useI18n()

export interface TrendDataPoint {
  date: string
  new_sessions: number
  active_sessions: number
  closed_sessions: number
  total_cost: number
  total_requests: number
}

const props = defineProps<{
  data: TrendDataPoint[]
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'dateClick', date: string): void
}>()

const chartRef = ref<HTMLElement | null>(null)
let chartInstance: echarts.ECharts | null = null

const hasData = computed(() => props.data.length > 0)

function initChart() {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  updateChart()

  chartInstance.on('click', (params: any) => {
    if (params.name) {
      emit('dateClick', params.name)
    }
  })
}

function updateChart() {
  if (!chartInstance || !hasData.value) return

  const dates = props.data.map(d => d.date)
  const newSessions = props.data.map(d => d.new_sessions)
  const activeSessions = props.data.map(d => d.active_sessions)
  const closedSessions = props.data.map(d => d.closed_sessions)
  const costs = props.data.map(d => d.total_cost)

  const option: EChartsOption = {
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' },
      backgroundColor: 'rgba(28, 33, 40, 0.95)',
      borderColor: 'rgba(48, 54, 61, 0.8)',
      textStyle: { color: '#e6edf3', fontSize: 12 },
    },
    legend: {
      data: [
        t('dashboard.charts.newSessions'),
        t('dashboard.charts.activeSessions'),
        t('dashboard.charts.closedSessions'),
        t('dashboard.charts.costUSD'),
      ],
      top: 0,
      textStyle: { color: '#8b949e', fontSize: 11 },
    },
    grid: {
      left: 50,
      right: 50,
      top: 40,
      bottom: 30,
    },
    xAxis: {
      type: 'category',
      data: dates,
      axisLabel: {
        color: '#8b949e',
        fontSize: 11,
        formatter: (value: string) => {
          const date = new Date(value)
          return `${date.getMonth() + 1}/${date.getDate()}`
        },
      },
      axisLine: { lineStyle: { color: '#30363d' } },
    },
    yAxis: [
      {
        type: 'value',
        name: t('dashboard.charts.sessionCount'),
        position: 'left',
        axisLabel: { color: '#8b949e', fontSize: 11 },
        splitLine: { lineStyle: { color: 'rgba(48, 54, 61, 0.5)' } },
      },
      {
        type: 'value',
        name: t('dashboard.charts.costUSD'),
        position: 'right',
        axisLabel: {
          color: '#8b949e',
          fontSize: 11,
          formatter: '${value}',
        },
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: t('dashboard.charts.newSessions'),
        type: 'bar',
        stack: 'sessions',
        data: newSessions,
        itemStyle: { color: '#6366f1' },
        barMaxWidth: 24,
      },
      {
        name: t('dashboard.charts.activeSessions'),
        type: 'bar',
        stack: 'sessions',
        data: activeSessions,
        itemStyle: { color: '#3b82f6' },
        barMaxWidth: 24,
      },
      {
        name: t('dashboard.charts.closedSessions'),
        type: 'bar',
        stack: 'sessions',
        data: closedSessions,
        itemStyle: { color: '#8b949e' },
        barMaxWidth: 24,
      },
      {
        name: t('dashboard.charts.costUSD'),
        type: 'line',
        yAxisIndex: 1,
        data: costs,
        smooth: true,
        itemStyle: { color: '#f59e0b' },
        lineStyle: { width: 2 },
        areaStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: 'rgba(245, 158, 11, 0.25)' },
            { offset: 1, color: 'rgba(245, 158, 11, 0.02)' },
          ]),
        },
      },
    ],
  }

  chartInstance.setOption(option, true)
}

function handleResize() {
  chartInstance?.resize()
}

onMounted(() => {
  initChart()
  window.addEventListener('resize', handleResize)
})

onUnmounted(() => {
  window.removeEventListener('resize', handleResize)
  chartInstance?.dispose()
  chartInstance = null
})

watch(() => props.data, updateChart, { deep: true })
</script>

<template>
  <el-card shadow="hover" class="session-trend-chart">
    <template #header>
      <div class="chart-header">
        <span class="chart-title">{{ t('dashboard.charts.sessionTrend') }}</span>
      </div>
    </template>
    <div v-loading="loading" class="chart-container">
      <div v-if="hasData" ref="chartRef" class="chart-inner"></div>
      <div v-else class="chart-empty">
        <el-empty :description="t('dashboard.noData')" :image-size="60" />
      </div>
    </div>
  </el-card>
</template>

<style scoped>
.session-trend-chart {
  margin-bottom: 16px;
}

.chart-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.chart-title {
  font-weight: 600;
  font-size: 15px;
}

.chart-container {
  height: 320px;
  position: relative;
}

.chart-inner {
  width: 100%;
  height: 100%;
}

.chart-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
}

:deep(.el-card) {
  background: var(--card, #1c2128);
  border-color: var(--border, #30363d);
  color: var(--text, #e6edf3);
}

:deep(.el-card__header) {
  padding: 12px 20px;
  border-bottom-color: var(--border, #30363d);
}
</style>
