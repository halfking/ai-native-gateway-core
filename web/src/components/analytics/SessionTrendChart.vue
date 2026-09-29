<script setup lang="ts">
/**
 * SessionTrendChart.vue
 * ECharts-based session trend visualization (new sessions / active / closed / cost).
 * Uses the useDashboard composable's trend data.
 */
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'
import { getChartTheme } from '../../composables/useChart'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElCard, ElEmpty } from 'element-plus'
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
const isDestroyed = ref(false)
let themeObserver: MutationObserver | null = null

const hasData = computed(() => props.data.length > 0)

function initChart() {
  if (!chartRef.value || isDestroyed.value) return

  // 清理旧实例
  if (chartInstance) {
    chartInstance.dispose()
    chartInstance = null
  }

  chartInstance = echarts.init(chartRef.value)
  updateChart()

  chartInstance.on('click', (params: any) => {
    if (params.name) {
      emit('dateClick', params.name)
    }
  })
}

function updateChart() {
  if (!chartInstance || !hasData.value || isDestroyed.value) return

  // 2026-09-29 暗色修复：原 option 全部硬编码 GitHub 暗色 hex（#e6edf3/#8b949e/#30363d 等），
  // 亮色皮肤下文字与背景几乎同色、看不清。每次 setOption 前从 CSS 读当前主题，
  // 并在 onMounted 里挂 data-theme 监听 → 主题切换时再次 setOption 重画。
  const theme = getChartTheme()

  const dates = props.data.map(d => d.date)
  const newSessions = props.data.map(d => d.new_sessions)
  const activeSessions = props.data.map(d => d.active_sessions)
  const closedSessions = props.data.map(d => d.closed_sessions)
  const costs = props.data.map(d => d.total_cost)

  const option: EChartsOption = {
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' },
      backgroundColor: theme.cardBorder,
      borderColor: theme.grid,
      textStyle: { color: theme.text, fontSize: 12 },
    },
    legend: {
      data: [
        t('dashboard.charts.newSessions'),
        t('dashboard.charts.activeSessions'),
        t('dashboard.charts.closedSessions'),
        t('dashboard.charts.costUSD'),
      ],
      top: 0,
      textStyle: { color: theme.muted, fontSize: 11 },
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
        color: theme.muted,
        fontSize: 11,
        formatter: (value: string) => {
          const date = new Date(value)
          return `${date.getMonth() + 1}/${date.getDate()}`
        },
      },
      axisLine: { lineStyle: { color: theme.cardBorder } },
    },
    yAxis: [
      {
        type: 'value',
        name: t('dashboard.charts.sessionCount'),
        position: 'left',
        axisLabel: { color: theme.muted, fontSize: 11 },
        splitLine: { lineStyle: { color: theme.grid } },
      },
      {
        type: 'value',
        name: t('dashboard.charts.costUSD'),
        position: 'right',
        axisLabel: {
          color: theme.muted,
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
        itemStyle: { color: '#3b82f6' },
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
        itemStyle: { color: '#3b82f6' },
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
  if (isDestroyed.value) return
  chartInstance?.resize()
}

function cleanupChart() {
  isDestroyed.value = true
  window.removeEventListener('resize', handleResize)
  themeObserver?.disconnect()
  themeObserver = null
  if (chartInstance) {
    chartInstance.dispose()
    chartInstance = null
  }
}

onMounted(() => {
  initChart()
  window.addEventListener('resize', handleResize)
  // 2026-09-29：data-theme 变化时重画，让 chart 颜色随皮肤切换
  themeObserver = new MutationObserver(() => {
    if (isDestroyed.value) return
    nextTick(() => updateChart())
  })
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['data-theme']
  })
})

onUnmounted(() => {
  cleanupChart()
})

watch(() => props.data, () => {
  nextTick(() => updateChart())
}, { deep: true })
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
  background: var(--card);
  border-color: var(--border);
  color: var(--text);
}

:deep(.el-card__header) {
  padding: 12px 20px;
  border-bottom-color: var(--border);
}
</style>
