<template>
  <div class="activity-timeline-chart">
    <el-card shadow="never">
      <template #header>
        <div class="chart-header">
          <span class="chart-title">活动趋势</span>
          <el-tooltip content="会话数和请求数随时间的变化" placement="top">
            <el-icon><QuestionFilled /></el-icon>
          </el-tooltip>
        </div>
      </template>
      <div v-loading="loading" class="chart-container">
        <canvas ref="chartCanvas"></canvas>
      </div>
      <div v-if="error" class="chart-error">
        <el-alert type="error" :title="error" :closable="false" />
      </div>
      <div v-if="!loading && !error && !hasData" class="chart-empty">
        <el-empty description="所选范围内无会话数据" />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { QuestionFilled } from '@element-plus/icons-vue'
import { useChart, createTimeSeriesConfig, chartColors } from '@/composables/useChart'

export interface ActivityDataPoint {
  date: string
  sessionCount: number
  requestCount: number
  successCount: number
  errorCount: number
}

const props = defineProps<{
  data: ActivityDataPoint[]
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'dateClick', date: string): void
}>()

const chartCanvas = ref<HTMLCanvasElement | null>(null)
const error = ref<string | null>(null)
const hasData = computed(() => props.data && props.data.length > 0)

// Chart 配置
const chartConfig = computed(() => {
  const labels = props.data.map(d => d.date)
  const sessionData = props.data.map(d => d.sessionCount)
  const requestData = props.data.map(d => d.requestCount)

  return createTimeSeriesConfig(
    'bar',
    labels,
    [
      {
        label: '会话数',
        data: sessionData,
        backgroundColor: chartColors.blue + '80',
        borderColor: chartColors.blue,
        yAxisID: 'y'
      },
      {
        label: '请求数',
        data: requestData,
        backgroundColor: chartColors.green + '80',
        borderColor: chartColors.green,
        yAxisID: 'y1'
      }
    ],
    {
      responsive: true,
      maintainAspectRatio: false,
      interaction: {
        mode: 'index',
        intersect: false
      },
      plugins: {
        legend: {
          display: true,
          position: 'top'
        },
        tooltip: {
          enabled: true,
          callbacks: {
            footer: (tooltipItems: any) => {
              const index = tooltipItems[0].dataIndex
              const point = props.data[index]
              return `成功: ${point.successCount} | 错误: ${point.errorCount}`
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
          type: 'linear',
          display: true,
          position: 'left',
          beginAtZero: true,
          title: {
            display: true,
            text: '会话数'
          }
        },
        y1: {
          type: 'linear',
          display: true,
          position: 'right',
          beginAtZero: true,
          grid: {
            drawOnChartArea: false
          },
          title: {
            display: true,
            text: '请求数'
          }
        }
      },
      onClick: (event: any, elements: any) => {
        if (elements.length > 0) {
          const index = elements[0].index
          const date = props.data[index].date
          emit('dateClick', date)
        }
      }
    }
  )
})

// 初始化 Chart
const { updateChart, chartInstance } = useChart(chartCanvas, chartConfig)

// 监听数据变化
watch(
  () => props.data,
  () => {
    if (chartInstance.value && hasData.value) {
      const labels = props.data.map(d => d.date)
      const sessionData = props.data.map(d => d.sessionCount)
      const requestData = props.data.map(d => d.requestCount)
      
      chartInstance.value.data.labels = labels
      chartInstance.value.data.datasets[0].data = sessionData
      chartInstance.value.data.datasets[1].data = requestData
      chartInstance.value.update()
    }
  },
  { deep: true }
)
</script>

<style scoped>
.activity-timeline-chart {
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
  height: 300px;
  position: relative;
}

.chart-error,
.chart-empty {
  padding: 20px;
}

/* 暗色系：el-card 默认白底，与全局卡片色对齐 */
:deep(.el-card) {
  background: var(--card);
  border-color: var(--border);
  color: var(--text);
}

:deep(.el-empty__description p) {
  color: var(--muted);
}
</style>
