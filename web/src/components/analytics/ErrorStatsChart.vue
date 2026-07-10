<script setup lang="ts">
/**
 * ErrorStatsChart.vue - 错误统计图表
 * 展示错误趋势、错误分布
 */

import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { ErrorStatsData } from '../../api/dashboard'

const props = defineProps<{
  data: ErrorStatsData | null
  loading?: boolean
}>()

const { t } = useI18n()
const chartRef = ref<HTMLDivElement>()
let chartInstance: echarts.ECharts | null = null

const chartOptions = computed(() => {
  if (!props.data) return {}

  const dates = props.data.trend.map(item => item.date)
  const errorCounts = props.data.trend.map(item => item.error_count)
  const totalCounts = props.data.trend.map(item => item.total_count)
  const errorRates = props.data.trend.map(item => 
    item.total_count > 0 ? (item.error_count / item.total_count * 100) : 0
  )

  return {
    title: {
      text: t('dashboard.errors.trendTitle') || 'Error Trend',
      left: 'center',
      textStyle: { fontSize: 16, fontWeight: '500' }
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' }
    },
    legend: {
      data: [
        t('dashboard.errors.errorCount') || 'Error Count',
        t('dashboard.errors.totalCount') || 'Total Count',
        t('dashboard.errors.errorRate') || 'Error Rate'
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
      data: dates,
      boundaryGap: false
    },
    yAxis: [
      {
        type: 'value',
        name: t('dashboard.errors.count') || 'Count',
        position: 'left'
      },
      {
        type: 'value',
        name: t('dashboard.errors.rate') || 'Rate (%)',
        position: 'right',
        max: 100,
        axisLabel: {
          formatter: '{value}%'
        }
      }
    ],
    series: [
      {
        name: t('dashboard.errors.errorCount') || 'Error Count',
        type: 'line',
        data: errorCounts,
        itemStyle: { color: '#f56c6c' },
        areaStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: 'rgba(245, 108, 108, 0.3)' },
            { offset: 1, color: 'rgba(245, 108, 108, 0.05)' }
          ])
        },
        smooth: true
      },
      {
        name: t('dashboard.errors.totalCount') || 'Total Count',
        type: 'line',
        data: totalCounts,
        itemStyle: { color: '#909399' },
        lineStyle: { type: 'dashed' },
        smooth: true
      },
      {
        name: t('dashboard.errors.errorRate') || 'Error Rate',
        type: 'line',
        yAxisIndex: 1,
        data: errorRates,
        itemStyle: { color: '#e6a23c' },
        lineStyle: { width: 2 },
        smooth: true
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
    <template #header>
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <span>{{ t('dashboard.errors.title') || 'Error Statistics' }}</span>
        <el-tag v-if="data" :type="data.summary.error_rate > 5 ? 'danger' : 'success'" size="small">
          {{ t('dashboard.errors.errorRate') }}: {{ data.summary.error_rate.toFixed(2) }}%
        </el-tag>
      </div>
    </template>
    <div ref="chartRef" style="width: 100%; height: 350px;"></div>
    
    <!-- 错误摘要 -->
    <el-row v-if="data" :gutter="16" style="margin-top: 20px;">
      <el-col :span="6">
        <el-statistic :title="t('dashboard.errors.totalErrors') || 'Total Errors'" :value="data.summary.total_errors">
          <template #prefix>
            <el-icon color="#f56c6c"><WarningFilled /></el-icon>
          </template>
        </el-statistic>
      </el-col>
      <el-col :span="6">
        <el-statistic :title="t('dashboard.errors.totalRequests') || 'Total Requests'" :value="data.summary.total_requests" />
      </el-col>
      <el-col :span="6">
        <el-statistic :title="t('dashboard.errors.errorRate') || 'Error Rate'" :value="data.summary.error_rate" :precision="2" suffix="%" />
      </el-col>
      <el-col :span="6">
        <el-statistic :title="t('dashboard.errors.avgLatency') || 'Avg Error Latency'" :value="data.summary.avg_error_latency_ms" :precision="0" suffix="ms" />
      </el-col>
    </el-row>
  </el-card>
</template>

<style scoped>
:deep(.el-card__body) {
  padding: 20px;
}
:deep(.el-card__header) {
  padding: 12px 20px;
}
</style>
