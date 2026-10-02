<script setup lang="ts">
/**
 * HealthGradeChart.vue
 * ECharts pie/doughnut chart for session health grade distribution.
 * Displays A/B/C/D/F grade breakdown with optional avg score overlay.
 */
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'
import { getChartTheme } from '../../composables/useChart'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElCard, ElEmpty, ElTag } from 'element-plus'
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
const isDestroyed = ref(false)
let themeObserver: MutationObserver | null = null

const gradeColors: Record<string, string> = {
  A: '#3fb950',
  B: '#3b82f6',
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
  if (!chartRef.value || isDestroyed.value) return

  // 清理旧实例
  if (chartInstance) {
    chartInstance.dispose()
    chartInstance = null
  }

  chartInstance = echarts.init(chartRef.value)
  updateChart()
}

function updateChart() {
  if (!chartInstance || !props.distribution || isDestroyed.value) return

  // 2026-09-29 暗色修复：tooltip/legend/text 默认色全部从 CSS token 读，
  // 亮色皮肤下用 `--kx-text`（深蓝灰）替代原硬编码 `#e6edf3`（暗色白）。
  const theme = getChartTheme()

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
      backgroundColor: theme.cardBorder,
      borderColor: theme.grid,
      textStyle: { color: theme.text, fontSize: 12 },
      formatter: (params: any) => {
        const pct = total.value > 0 ? ((params.value / total.value) * 100).toFixed(1) : '0'
        return `${params.marker} ${params.name}: ${params.value} (${pct}%)`
      },
    },
    legend: {
      orient: 'vertical',
      right: 10,
      top: 'center',
      textStyle: { color: theme.muted, fontSize: 11 },
    },
    graphic: (props.avgScore !== undefined && props.avgScore !== null
      ? [
          {
            type: 'text',
            left: 'center',
            top: '40%',
            style: {
              text: props.avgScore.toFixed(1),
              textAlign: 'center',
              fill: theme.text,
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
              fill: theme.muted,
              fontSize: 12,
            },
          },
        ]
      : undefined) as any,
    series: [
      {
        type: 'pie',
        radius: ['45%', '70%'],
        center: ['40%', '50%'],
        avoidLabelOverlap: false,
        label: { show: false },
        emphasis: {
          label: { show: true, fontSize: 14, fontWeight: 'bold', color: theme.text },
        },
        labelLine: { show: false },
        data: pieData,
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

watch(() => props.distribution, () => {
  nextTick(() => updateChart())
}, { deep: true })
watch(() => props.avgScore, () => {
  nextTick(() => updateChart())
})
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
  background: var(--card);
  border-color: var(--border);
  color: var(--text);
}

:deep(.el-card__header) {
  padding: 12px 20px;
  border-bottom-color: var(--border);
}
</style>
