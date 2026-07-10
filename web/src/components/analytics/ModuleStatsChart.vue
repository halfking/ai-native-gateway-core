<script setup lang="ts">
/**
 * ModuleStatsChart.vue - 模块执行统计图表
 * 使用 ECharts 展示模块执行次数、成功率、缓存命中率
 */

import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { ModuleStatsItem } from '../../api/dashboard'

const props = defineProps<{
  data: ModuleStatsItem[]
  loading?: boolean
}>()

const { t } = useI18n()
const chartRef = ref<HTMLDivElement>()
let chartInstance: echarts.ECharts | null = null

const chartOptions = computed(() => {
  const modules = props.data.slice(0, 10) // Top 10 模块
  const moduleNames = modules.map(m => m.module_name)
  const executions = modules.map(m => m.total_executions)
  const successRates = modules.map(m => 
    m.total_executions > 0 ? (m.success_count / m.total_executions * 100) : 0
  )
  const cacheHitRates = modules.map(m => m.cache_hit_rate)

  return {
    title: {
      text: t('dashboard.moduleStats.title') || 'Module Execution Statistics',
      left: 'center',
      textStyle: { fontSize: 16, fontWeight: '500' }
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' }
    },
    legend: {
      data: [
        t('dashboard.moduleStats.executions') || 'Executions',
        t('dashboard.moduleStats.successRate') || 'Success Rate',
        t('dashboard.moduleStats.cacheHitRate') || 'Cache Hit Rate'
      ],
      top: 30
    },
    grid: {
      left: '3%',
      right: '4%',
      bottom: '3%',
      top: 80,
      containLabel: true
    },
    xAxis: {
      type: 'category',
      data: moduleNames,
      axisLabel: {
        rotate: 45,
        interval: 0
      }
    },
    yAxis: [
      {
        type: 'value',
        name: t('dashboard.moduleStats.executions') || 'Executions',
        position: 'left'
      },
      {
        type: 'value',
        name: t('dashboard.moduleStats.rate') || 'Rate (%)',
        position: 'right',
        max: 100,
        axisLabel: {
          formatter: '{value}%'
        }
      }
    ],
    series: [
      {
        name: t('dashboard.moduleStats.executions') || 'Executions',
        type: 'bar',
        data: executions,
        itemStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: '#83bff6' },
            { offset: 1, color: '#188df0' }
          ])
        }
      },
      {
        name: t('dashboard.moduleStats.successRate') || 'Success Rate',
        type: 'line',
        yAxisIndex: 1,
        data: successRates,
        itemStyle: { color: '#5cb87a' },
        lineStyle: { width: 2 }
      },
      {
        name: t('dashboard.moduleStats.cacheHitRate') || 'Cache Hit Rate',
        type: 'line',
        yAxisIndex: 1,
        data: cacheHitRates,
        itemStyle: { color: '#e6a23c' },
        lineStyle: { width: 2, type: 'dashed' }
      }
    ]
  }
})

function initChart() {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  updateChart()
}

function updateChart() {
  if (!chartInstance) return
  chartInstance.setOption(chartOptions.value, true)
}

function resizeChart() {
  chartInstance?.resize()
}

watch(() => props.data, updateChart, { deep: true })
watch(() => props.loading, (isLoading) => {
  if (chartInstance) {
    isLoading ? chartInstance.showLoading() : chartInstance.hideLoading()
  }
})

onMounted(() => {
  initChart()
  window.addEventListener('resize', resizeChart)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resizeChart)
  chartInstance?.dispose()
})
</script>

<template>
  <el-card shadow="hover">
    <div ref="chartRef" style="width: 100%; height: 400px;"></div>
  </el-card>
</template>

<style scoped>
:deep(.el-card__body) {
  padding: 20px;
}
</style>
