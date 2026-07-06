<template>
  <div class="cost-trend-chart">
    <el-card shadow="never">
      <template #header>
        <div class="chart-header">
          <span class="chart-title">成本趋势</span>
          <el-radio-group v-model="chartType" size="small" style="margin-left: auto">
            <el-radio-button label="area">堆叠面积</el-radio-button>
            <el-radio-button label="line">折线</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <div v-loading="loading" class="chart-container">
        <canvas ref="chartCanvas"></canvas>
      </div>
      <div v-if="!loading && summary" class="chart-summary">
        <el-tag type="info">总计: ${{ summary.totalCost.toFixed(2) }}</el-tag>
        <el-tag :type="trendType" style="margin-left: 8px">
          趋势: {{ trendText }}
        </el-tag>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useChart, createStackedAreaConfig, createTimeSeriesConfig, chartColors } from '@/composables/useChart'

export interface CostDataPoint {
  date: string
  inputCost: number
  outputCost: number
  totalCost: number
}

export interface CostSummary {
  totalCost: number
  avgDailyCost: number
  costTrend: 'up' | 'down' | 'flat'
  trendPct: number
}

const props = defineProps<{
  data: CostDataPoint[]
  summary?: CostSummary
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'dateClick', date: string): void
}>()

const chartCanvas = ref<HTMLCanvasElement | null>(null)
const chartType = ref<'area' | 'line'>('area')

// 趋势类型
const trendType = computed(() => {
  if (!props.summary) return 'info'
  switch (props.summary.costTrend) {
    case 'up':
      return 'danger'
    case 'down':
      return 'success'
    default:
      return 'info'
  }
})

const trendText = computed(() => {
  if (!props.summary) return '-'
  const trend = props.summary.costTrend
  const pct = Math.abs(props.summary.trendPct).toFixed(1)
  
  if (trend === 'up') return `↑ ${pct}%`
  if (trend === 'down') return `↓ ${pct}%`
  return '持平'
})

// Chart 配置
const chartConfig = computed(() => {
  const labels = props.data.map(d => d.date)
  const inputData = props.data.map(d => d.inputCost)
  const outputData = props.data.map(d => d.outputCost)

  const datasets = [
    {
      label: '输入成本',
      data: inputData,
      backgroundColor: chartColors.blue + (chartType.value === 'area' ? '60' : ''),
      borderColor: chartColors.blue,
      fill: chartType.value === 'area'
    },
    {
      label: '输出成本',
      data: outputData,
      backgroundColor: chartColors.green + (chartType.value === 'area' ? '60' : ''),
      borderColor: chartColors.green,
      fill: chartType.value === 'area'
    }
  ]

  if (chartType.value === 'area') {
    return createStackedAreaConfig(labels, datasets, {
      responsive: true,
      maintainAspectRatio: false,
      onClick: (event: any, elements: any) => {
        if (elements.length > 0) {
          const index = elements[0].index
          emit('dateClick', props.data[index].date)
        }
      }
    })
  } else {
    return createTimeSeriesConfig('line', labels, datasets, {
      responsive: true,
      maintainAspectRatio: false,
      scales: {
        y: {
          beginAtZero: true,
          ticks: {
            callback: function(value: any) {
              return '$' + value.toFixed(2)
            }
          }
        }
      },
      onClick: (event: any, elements: any) => {
        if (elements.length > 0) {
          const index = elements[0].index
          emit('dateClick', props.data[index].date)
        }
      }
    })
  }
})

const { chartInstance } = useChart(chartCanvas, chartConfig)

// 监听图表类型切换
watch(chartType, () => {
  if (chartInstance.value) {
    chartInstance.value.destroy()
    // 重新初始化会由 useChart 的 watch 处理
  }
})

// 监听数据变化
watch(
  () => props.data,
  () => {
    if (chartInstance.value && props.data.length > 0) {
      const labels = props.data.map(d => d.date)
      const inputData = props.data.map(d => d.inputCost)
      const outputData = props.data.map(d => d.outputCost)
      
      chartInstance.value.data.labels = labels
      chartInstance.value.data.datasets[0].data = inputData
      chartInstance.value.data.datasets[1].data = outputData
      chartInstance.value.update()
    }
  },
  { deep: true }
)
</script>

<style scoped>
.cost-trend-chart {
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

.chart-summary {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid #ebeef5;
}
</style>
