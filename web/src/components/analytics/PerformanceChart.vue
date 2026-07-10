<script setup lang="ts">
/**
 * PerformanceChart.vue - 性能指标图表
 * 展示延迟分布、吞吐量趋势
 */

import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import * as echarts from 'echarts'
import type { PerformanceData } from '../../api/dashboard'

const props = defineProps<{
  data: PerformanceData | null
  loading?: boolean
}>()

const { t } = useI18n()
const chartRef = ref<HTMLDivElement>()
let chartInstance: echarts.ECharts | null = null
const isDestroyed = ref(false)

const chartOptions = computed(() => {
  if (!props.data) return {}

  const dates = props.data.throughput.map(item => item.date)
  const requestCounts = props.data.throughput.map(item => item.request_count)
  const avgLatencies = props.data.throughput.map(item => item.avg_latency_ms)

  return {
    title: {
      text: t('dashboard.performance.title') || 'Performance Metrics',
      left: 'center',
      textStyle: { fontSize: 16, fontWeight: '500' }
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' }
    },
    legend: {
      data: [
        t('dashboard.performance.throughput') || 'Throughput',
        t('dashboard.performance.avgLatency') || 'Avg Latency'
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
        name: t('dashboard.performance.requests') || 'Requests',
        position: 'left'
      },
      {
        type: 'value',
        name: t('dashboard.performance.latency') || 'Latency (ms)',
        position: 'right',
        axisLabel: {
          formatter: '{value} ms'
        }
      }
    ],
    series: [
      {
        name: t('dashboard.performance.throughput') || 'Throughput',
        type: 'bar',
        data: requestCounts,
        itemStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: '#409EFF' },
            { offset: 1, color: '#79bbff' }
          ])
        }
      },
      {
        name: t('dashboard.performance.avgLatency') || 'Avg Latency',
        type: 'line',
        yAxisIndex: 1,
        data: avgLatencies,
        itemStyle: { color: '#67C23A' },
        lineStyle: { width: 2 },
        smooth: true
      }
    ]
  }
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
  if (!chartInstance || isDestroyed.value) return
  chartInstance.setOption(chartOptions.value, true)
}

function resizeChart() {
  if (isDestroyed.value) return
  chartInstance?.resize()
}

function cleanupChart() {
  isDestroyed.value = true
  window.removeEventListener('resize', resizeChart)
  if (chartInstance) {
    chartInstance.dispose()
    chartInstance = null
  }
}

watch(() => props.data, () => {
  nextTick(() => updateChart())
}, { deep: true })
watch(() => props.loading, (isLoading) => {
  if (chartInstance && !isDestroyed.value) {
    isLoading ? chartInstance.showLoading() : chartInstance.hideLoading()
  }
})

onMounted(() => {
  initChart()
  window.addEventListener('resize', resizeChart)
})

onBeforeUnmount(() => {
  cleanupChart()
})
</script>

<template>
  <el-card shadow="hover">
    <div ref="chartRef" style="width: 100%; height: 350px;"></div>
    
    <!-- 性能摘要 -->
    <el-row v-if="data" :gutter="16" style="margin-top: 20px;">
      <el-col :span="6">
        <el-statistic 
          :title="t('dashboard.performance.p50') || 'P50 Latency'" 
          :value="data.summary.p50_latency_ms" 
          :precision="0" 
          suffix="ms">
          <template #prefix>
            <el-icon color="#67C23A"><Timer /></el-icon>
          </template>
        </el-statistic>
      </el-col>
      <el-col :span="6">
        <el-statistic 
          :title="t('dashboard.performance.p95') || 'P95 Latency'" 
          :value="data.summary.p95_latency_ms" 
          :precision="0" 
          suffix="ms" />
      </el-col>
      <el-col :span="6">
        <el-statistic 
          :title="t('dashboard.performance.p99') || 'P99 Latency'" 
          :value="data.summary.p99_latency_ms" 
          :precision="0" 
          suffix="ms" />
      </el-col>
      <el-col :span="6">
        <el-statistic 
          :title="t('dashboard.performance.throughput') || 'Avg Throughput'" 
          :value="data.summary.avg_throughput_rps" 
          :precision="2" 
          suffix="rps" />
      </el-col>
    </el-row>

    <!-- 延迟分布 -->
    <el-divider v-if="data">{{ t('dashboard.performance.latencyDist') || 'Latency Distribution' }}</el-divider>
    <el-row v-if="data" :gutter="16">
      <el-col :span="4">
        <el-statistic title="< 100ms" :value="data.latency_distribution.under_100ms">
          <template #suffix>
            <span style="font-size: 12px; color: #67C23A;">
              ({{ ((data.latency_distribution.under_100ms / data.summary.total_requests) * 100).toFixed(1) }}%)
            </span>
          </template>
        </el-statistic>
      </el-col>
      <el-col :span="4">
        <el-statistic title="100-500ms" :value="data.latency_distribution.under_500ms" />
      </el-col>
      <el-col :span="4">
        <el-statistic title="500ms-1s" :value="data.latency_distribution.under_1000ms" />
      </el-col>
      <el-col :span="4">
        <el-statistic title="1-5s" :value="data.latency_distribution.under_5000ms" />
      </el-col>
      <el-col :span="4">
        <el-statistic title="> 5s" :value="data.latency_distribution.over_5000ms">
          <template #suffix>
            <span style="font-size: 12px; color: #F56C6C;">
              ({{ ((data.latency_distribution.over_5000ms / data.summary.total_requests) * 100).toFixed(1) }}%)
            </span>
          </template>
        </el-statistic>
      </el-col>
    </el-row>
  </el-card>
</template>

<style scoped>
:deep(.el-card__body) {
  padding: 20px;
}
:deep(.el-divider) {
  margin: 20px 0 16px;
}
</style>
