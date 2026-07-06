<template>
  <div class="latency-trend-chart">
    <el-card shadow="never">
      <template #header>
        <div class="chart-header">
          <span class="chart-title">延迟趋势</span>
          <el-checkbox v-model="showLog" size="small" style="margin-left: auto">
            对数坐标
          </el-checkbox>
        </div>
      </template>
      <div v-loading="loading" class="chart-container">
        <canvas ref="chartCanvas"></canvas>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useChart, createTimeSeriesConfig, chartColors } from '@/composables/useChart'

export interface LatencyDataPoint {
  date: string
  p50Latency: number
  p90Latency: number
  p99Latency: number
  maxLatency?: number
  avgLatency?: number
}

const props = defineProps<{
  data: LatencyDataPoint[]
  loading?: boolean
}>()

const chartCanvas = ref<HTMLCanvasElement | null>(null)
const showLog = ref(false)

// Chart 配置
const chartConfig = computed(() => {
  const labels = props.data.map(d => d.date)
  const p50Data = props.data.map(d => d.p50Latency)
  const p90Data = props.data.map(d => d.p90Latency)
  const p99Data = props.data.map(d => d.p99Latency)

  return createTimeSeriesConfig(
    'line',
    labels,
    [
      {
        label: 'P50',
        data: p50Data,
        borderColor: chartColors.blue,
        backgroundColor: chartColors.blue + '20',
        fill: false
      },
      {
        label: 'P90',
        data: p90Data,
        borderColor: chartColors.orange,
        backgroundColor: chartColors.orange + '20',
        fill: false
      },
      {
        label: 'P99',
        data: p99Data,
        borderColor: chartColors.red,
        backgroundColor: chartColors.red + '20',
        fill: false
      }
    ],
    {
      responsive: true,
      maintainAspectRatio: false,
      scales: {
        y: {
          type: showLog.value ? 'logarithmic' : 'linear',
          beginAtZero: true,
          ticks: {
            callback: function(value: any) {
              if (value >= 1000) {
                return (value / 1000).toFixed(1) + 's'
              }
              return value + 'ms'
            }
          }
        }
      },
      plugins: {
        annotation: {
          annotations: {
            line1: {
              type: 'line',
              yMin: 10000,
              yMax: 10000,
              borderColor: chartColors.red,
              borderWidth: 2,
              borderDash: [5, 5],
              label: {
                display: true,
                content: '10s 阈值',
                position: 'end'
              }
            }
          }
        }
      }
    }
  )
})

const { chartInstance } = useChart(chartCanvas, chartConfig)

// 监听对数坐标切换
watch(showLog, () => {
  if (chartInstance.value && chartInstance.value.options.scales?.y) {
    chartInstance.value.options.scales.y.type = showLog.value ? 'logarithmic' : 'linear'
    chartInstance.value.update()
  }
})

// 监听数据变化
watch(
  () => props.data,
  () => {
    if (chartInstance.value && props.data.length > 0) {
      const labels = props.data.map(d => d.date)
      const p50Data = props.data.map(d => d.p50Latency)
      const p90Data = props.data.map(d => d.p90Latency)
      const p99Data = props.data.map(d => d.p99Latency)
      
      chartInstance.value.data.labels = labels
      chartInstance.value.data.datasets[0].data = p50Data
      chartInstance.value.data.datasets[1].data = p90Data
      chartInstance.value.data.datasets[2].data = p99Data
      chartInstance.value.update()
    }
  },
  { deep: true }
)
</script>

<style scoped>
.latency-trend-chart {
  height: 100%;
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
  height: 300px;
  position: relative;
}
</style>
