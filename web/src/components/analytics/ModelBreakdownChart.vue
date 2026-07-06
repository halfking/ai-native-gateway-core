<template>
  <div class="model-breakdown-chart">
    <el-card shadow="never">
      <template #header>
        <div class="chart-header">
          <span class="chart-title">模型/提供商分布</span>
          <el-radio-group v-model="metric" size="small" style="margin-left: auto">
            <el-radio-button label="requests">请求数</el-radio-button>
            <el-radio-button label="cost">成本</el-radio-button>
            <el-radio-button label="tokens">Token</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <div v-loading="loading" class="chart-container">
        <canvas ref="chartCanvas"></canvas>
      </div>
      <div v-if="!loading && !hasData" class="chart-empty">
        <el-empty description="暂无数据" />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useChart, createDoughnutConfig, generateColors } from '@/composables/useChart'

export interface ModelBreakdownItem {
  model: string
  requestCount: number
  sessionCount: number
  totalCost: number
  totalTokens: number
  avgLatency: number
  errorRate: number
}

const props = defineProps<{
  data: ModelBreakdownItem[]
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'modelClick', model: string): void
}>()

const chartCanvas = ref<HTMLCanvasElement | null>(null)
const metric = ref<'requests' | 'cost' | 'tokens'>('requests')
const hasData = computed(() => props.data && props.data.length > 0)

// Chart 配置
const chartConfig = computed(() => {
  // 合并占比小于2%的为"其他"
  const threshold = 0.02
  let items = [...props.data]
  
  // 按选择的指标排序
  items.sort((a, b) => {
    switch (metric.value) {
      case 'cost':
        return b.totalCost - a.totalCost
      case 'tokens':
        return b.totalTokens - a.totalTokens
      default:
        return b.requestCount - a.requestCount
    }
  })

  const total = items.reduce((sum, item) => {
    switch (metric.value) {
      case 'cost':
        return sum + item.totalCost
      case 'tokens':
        return sum + item.totalTokens
      default:
        return sum + item.requestCount
    }
  }, 0)

  const mainItems: ModelBreakdownItem[] = []
  let othersValue = 0

  items.forEach(item => {
    const value = metric.value === 'cost' ? item.totalCost : 
                  metric.value === 'tokens' ? item.totalTokens : 
                  item.requestCount
    const ratio = value / total

    if (ratio >= threshold) {
      mainItems.push(item)
    } else {
      othersValue += value
    }
  })

  const labels = mainItems.map(item => item.model)
  const data = mainItems.map(item => {
    switch (metric.value) {
      case 'cost':
        return item.totalCost
      case 'tokens':
        return item.totalTokens
      default:
        return item.requestCount
    }
  })

  if (othersValue > 0) {
    labels.push('其他')
    data.push(othersValue)
  }

  const colors = generateColors(labels.length)

  return createDoughnutConfig(labels, data, colors, {
    responsive: true,
    maintainAspectRatio: false,
    plugins: {
      legend: {
        display: true,
        position: 'right'
      },
      tooltip: {
        enabled: true,
        callbacks: {
          label: function(context: any) {
            const label = context.label || ''
            const value = context.parsed
            const total = (context.dataset.data as number[]).reduce((a: number, b: number) => a + b, 0)
            const percentage = ((value / total) * 100).toFixed(1)
            
            let formattedValue = value.toString()
            if (metric.value === 'cost') {
              formattedValue = '$' + value.toFixed(2)
            } else if (metric.value === 'tokens') {
              formattedValue = (value >= 1000000 ? (value / 1000000).toFixed(1) + 'M' : 
                               value >= 1000 ? (value / 1000).toFixed(1) + 'K' : value.toString())
            }
            
            return `${label}: ${formattedValue} (${percentage}%)`
          }
        }
      }
    },
    onClick: (event: any, elements: any) => {
      if (elements.length > 0) {
        const index = elements[0].index
        const model = labels[index]
        if (model !== '其他') {
          emit('modelClick', model)
        }
      }
    }
  })
})

const { chartInstance } = useChart(chartCanvas, chartConfig)

// 监听指标切换
watch(metric, () => {
  if (chartInstance.value) {
    chartInstance.value.destroy()
  }
})

// 监听数据变化
watch(
  () => props.data,
  () => {
    if (chartInstance.value && hasData.value) {
      // 数据变化时需要重新计算，简单处理：销毁重建
      chartInstance.value.destroy()
    }
  },
  { deep: true }
)
</script>

<style scoped>
.model-breakdown-chart {
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
  height: 320px;
  position: relative;
}

.chart-empty {
  padding: 40px 20px;
  text-align: center;
}
</style>
