<script setup lang="ts">
/**
 * HealthGradeChart.vue
 * ECharts pie/doughnut chart for session health grade distribution.
 * Displays A/B/C/D/F grade breakdown with optional avg score overlay.
 */
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'

const { t } = useI18n()

export interface HealthDistribution {
  a: number
  b: number
  c: number
  d: number
  f: number
}

const props = defineProps<{
  distribution: HealthDistribution | null
  avgScore?: number
  loading?: boolean
}>()

const chartRef = ref<HTMLElement | null>(null)
let chartInstance: echarts.ECharts | null = null

const gradeColors: Record<string, string> = {
  A: '#3fb950',
  B: '#6366f1',
  C: '#d29922',
  D: '#f85149',
  F: '#8b949e',
}

const hasData = computed(() => {
  if (!props.distribution) return false
  return props.distribution.a + props.distribution.b + props.distribution.c +
    props.distribution.d + props.distribution.f > 0
})

const total = computed(() => {
  if (!props.distribution) return 0
  return props.distribution.a + props.distribution.b + props.distribution.c +
    props.distribution.d + props.distribution.f
})

function initChart() {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  updateChart()
}

function updateChart() {
  if (!chartInstance || !props.distribution) return

  const gradeKeys = ['a', 'b', 'c', 'd', 'f'] as const
  const gradeLabels: Record<string, string> = {
    a: t('dashboard.charts.gradeA'),
    b: t('dashboard.charts.gradeB'),
    c: t('dashboard.charts.gradeC'),
    d: t('dashboard.charts.gradeD'),
    f: t('dashboard.charts.gradeF'),
  }

  const pieData = gradeKeys
    .filter(k => props.distribution![k] > 0)
    .map(k => ({
      name: gradeLabels[k],
      value: props.distribution![k],
      itemStyle: { color: gradeColors[k.toUpperCase()] },
    }))

  const option: EChartsOption = {
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(28, 33, 40, 0.95)',
      borderColor: 'rgba(48, 54, 61, 0.8)',
      textStyle: { color: '#e6edf3', fontSize: 12 },
      formatter: (params: any) => {
        const pct = total.value > 0 ? ((params.value / total.value) * 100).toFixed(1) : '0'
        return `${params.marker} ${params.name}: ${params.value} (${pct}%)`
      },
    },
    legend: {
      orient: 'vertical',
      right: 10,
      top: 'center',
      textStyle: { color: '#8b949e', fontSize: 11 },
    },
    graphic: props.avgScore !== undefined && props.avgScore !== null
      ? [
          {
            type: 'text',
            left: 'center',
            top: '40%',
            style: {
              text: props.avgScore.toFixed(1),
              textAlign: 'center',
              fill: '#e6edf3',
              fontSize: 28,
              fontWeight: 700,
            },
          },
          {
            type: 'text',
            left: 'center',
            top: '56%',
            style: {
              text: t('dashboard.charts.avgScore'),
              textAlign: 'center',
              fill: '#8b949e',
              fontSize: 12,
            },
          },
        ]
      : undefined,
    series: [
      {
        type: 'pie',
        radius: ['45%', '70%'],
        center: ['40%', '50%'],
        avoidLabelOverlap: false,
        label: { show: false },
        emphasis: {
          label: { show: true, fontSize: 14, fontWeight: 'bold', color: '#e6edf3' },
        },
        labelLine: { show: false },
        data: pieData,
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

watch(() => props.distribution, updateChart, { deep: true })
watch(() => props.avgScore, updateChart)
</script>

<template>
  <el-card shadow="hover" class="health-grade-chart">
    <template #header>
      <div class="chart-header">
        <span class="chart-title">{{ t('dashboard.charts.healthDistribution') }}</span>
        <el-tag v-if="hasData" size="small" type="info">
          {{ t('dashboard.charts.totalSessions', { n: total }) }}
        </el-tag>
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
.health-grade-chart {
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
  height: 280px;
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
