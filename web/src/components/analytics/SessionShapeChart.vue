<template>
  <div class="session-shape-chart">
    <el-card shadow="never">
      <template #header>
        <div class="chart-header">
          <span class="chart-title">会话形态分布</span>
          <el-tooltip content="按请求数和时长分桶，了解用户使用深度" placement="top">
            <el-icon><QuestionFilled /></el-icon>
          </el-tooltip>
        </div>
      </template>
      <div v-loading="loading" class="chart-container">
        <canvas ref="chartCanvas"></canvas>
      </div>
      <div class="shape-legend">
        <el-tag type="success" size="small">Quick (1-5请求)</el-tag>
        <el-tag type="primary" size="small">Standard (6-20请求)</el-tag>
        <el-tag type="warning" size="small">Deep (21-50请求)</el-tag>
        <el-tag type="danger" size="small">Marathon (>50请求)</el-tag>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { QuestionFilled } from '@element-plus/icons-vue'
import { useChart, createHistogramConfig, chartColors } from '@/composables/useChart'

export interface SessionShapeBucket {
  range: string
  label: string
  count: number
}

export interface SessionShapeData {
  requestCountBuckets: SessionShapeBucket[]
  durationBuckets?: SessionShapeBucket[]
}

const props = defineProps<{
  data: SessionShapeData
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'bucketClick', label: string): void
}>()

const chartCanvas = ref<HTMLCanvasElement | null>(null)

// 形态颜色映射
const shapeColors: Record<string, string> = {
  quick: chartColors.green,
  standard: chartColors.blue,
  deep: chartColors.orange,
  marathon: chartColors.red
}

// Chart 配置
const chartConfig = computed(() => {
  const buckets = props.data.requestCountBuckets || []
  const labels = buckets.map(b => `${b.label}\n(${b.range})`)
  const data = buckets.map(b => b.count)
  const colors = buckets.map(b => shapeColors[b.label] || chartColors.gray)

  return {
    type: 'bar' as const,
    data: {
      labels,
      datasets: [
        {
          data,
          backgroundColor: colors,
          borderColor: colors,
          borderWidth: 1
        }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          display: false
        },
        tooltip: {
          enabled: true,
          callbacks: {
            label: function(context: any) {
              const value = context.parsed.y
              const total = (context.dataset.data as number[]).reduce((a: number, b: number) => a + b, 0)
              const percentage = ((value / total) * 100).toFixed(1)
              return `会话数: ${value} (${percentage}%)`
            }
          }
        }
      },
      scales: {
        x: {
          grid: {
            display: false
          }
        },
        y: {
          beginAtZero: true,
          grid: {
            color: 'rgba(0, 0, 0, 0.05)'
          },
          title: {
            display: true,
            text: '会话数'
          }
        }
      },
      onClick: (event: any, elements: any) => {
        if (elements.length > 0) {
          const index = elements[0].index
          const label = buckets[index].label
          emit('bucketClick', label)
        }
      }
    }
  }
})

const { chartInstance } = useChart(chartCanvas, chartConfig)

// 监听数据变化
watch(
  () => props.data,
  () => {
    if (chartInstance.value && props.data.requestCountBuckets.length > 0) {
      const buckets = props.data.requestCountBuckets
      const labels = buckets.map(b => `${b.label}\n(${b.range})`)
      const data = buckets.map(b => b.count)
      
      chartInstance.value.data.labels = labels
      chartInstance.value.data.datasets[0].data = data
      chartInstance.value.update()
    }
  },
  { deep: true }
)
</script>

<style scoped>
.session-shape-chart {
  height: 100%;
}

.chart-header {
  display: flex;
  align-items: center;
  gap: 8px;
}

.chart-title {
  font-weight: 600;
  font-size: 15px;
}

.chart-container {
  height: 280px;
  position: relative;
}

.shape-legend {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

/* 暗色系：el-card 默认白底，与全局卡片色对齐 */
:deep(.el-card) {
  background: var(--card);
  border-color: var(--border);
  color: var(--text);
}
</style>
