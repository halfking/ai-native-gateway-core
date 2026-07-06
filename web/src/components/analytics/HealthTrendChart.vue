<template>
  <div class="health-trend-chart">
    <el-card shadow="never">
      <template #header>
        <div class="chart-header">
          <span class="chart-title">健康趋势</span>
          <el-radio-group v-model="viewMode" size="small" style="margin-left: auto">
            <el-radio-button label="score">平均分</el-radio-button>
            <el-radio-button label="grade">等级分布</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <div v-loading="loading" class="chart-container">
        <canvas ref="chartCanvas"></canvas>
      </div>
      <div v-if="!loading && !hasData" class="chart-empty">
        <el-empty description="健康评分建设中" />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useChart, createTimeSeriesConfig, createStackedAreaConfig, chartColors } from '@/composables/useChart'

export interface HealthDataPoint {
  date: string
  avgHealthScore: number
  gradeA?: number
  gradeB?: number
  gradeC?: number
  gradeD?: number
  gradeF?: number
}

const props = defineProps<{
  data: HealthDataPoint[]
  loading?: boolean
}>()

const chartCanvas = ref<HTMLCanvasElement | null>(null)
const viewMode = ref<'score' | 'grade'>('score')
const hasData = computed(() => props.data && props.data.length > 0 && props.data.some(d => d.avgHealthScore !== null))

// Chart 配置
const chartConfig = computed(() => {
  const labels = props.data.map(d => d.date)

  if (viewMode.value === 'score') {
    // 平均分折线图
    const scoreData = props.data.map(d => d.avgHealthScore || 0)

    return createTimeSeriesConfig(
      'line',
      labels,
      [
        {
          label: '平均健康分',
          data: scoreData,
          borderColor: chartColors.blue,
          backgroundColor: chartColors.blue + '20',
          fill: true
        }
      ],
      {
        responsive: true,
        maintainAspectRatio: false,
        scales: {
          y: {
            min: 0,
            max: 100,
            ticks: {
              stepSize: 20
            }
          }
        },
        plugins: {
          annotation: {
            annotations: {
              line60: {
                type: 'line',
                yMin: 60,
                yMax: 60,
                borderColor: chartColors.orange,
                borderWidth: 1,
                borderDash: [5, 5],
                label: {
                  display: true,
                  content: 'C级 (60)',
                  position: 'start'
                }
              },
              line40: {
                type: 'line',
                yMin: 40,
                yMax: 40,
                borderColor: chartColors.red,
                borderWidth: 1,
                borderDash: [5, 5],
                label: {
                  display: true,
                  content: 'F级 (40)',
                  position: 'start'
                }
              }
            }
          }
        }
      }
    )
  } else {
    // 等级堆叠面积图
    const gradeColors = {
      A: '#67c23a',
      B: '#409eff',
      C: '#e6a23c',
      D: '#f56c6c',
      F: '#909399'
    }

    return createStackedAreaConfig(
      labels,
      [
        {
          label: 'A (优秀)',
          data: props.data.map(d => d.gradeA || 0),
          backgroundColor: gradeColors.A + '80',
          borderColor: gradeColors.A
        },
        {
          label: 'B (良好)',
          data: props.data.map(d => d.gradeB || 0),
          backgroundColor: gradeColors.B + '80',
          borderColor: gradeColors.B
        },
        {
          label: 'C (一般)',
          data: props.data.map(d => d.gradeC || 0),
          backgroundColor: gradeColors.C + '80',
          borderColor: gradeColors.C
        },
        {
          label: 'D (较差)',
          data: props.data.map(d => d.gradeD || 0),
          backgroundColor: gradeColors.D + '80',
          borderColor: gradeColors.D
        },
        {
          label: 'F (异常)',
          data: props.data.map(d => d.gradeF || 0),
          backgroundColor: gradeColors.F + '80',
          borderColor: gradeColors.F
        }
      ]
    )
  }
})

const { chartInstance } = useChart(chartCanvas, chartConfig)

// 监听视图模式切换
watch(viewMode, () => {
  if (chartInstance.value) {
    chartInstance.value.destroy()
  }
})

// 监听数据变化
watch(
  () => props.data,
  () => {
    if (chartInstance.value && hasData.value) {
      chartInstance.value.data.labels = props.data.map(d => d.date)
      
      if (viewMode.value === 'score') {
        chartInstance.value.data.datasets[0].data = props.data.map(d => d.avgHealthScore || 0)
      } else {
        chartInstance.value.data.datasets[0].data = props.data.map(d => d.gradeA || 0)
        chartInstance.value.data.datasets[1].data = props.data.map(d => d.gradeB || 0)
        chartInstance.value.data.datasets[2].data = props.data.map(d => d.gradeC || 0)
        chartInstance.value.data.datasets[3].data = props.data.map(d => d.gradeD || 0)
        chartInstance.value.data.datasets[4].data = props.data.map(d => d.gradeF || 0)
      }
      
      chartInstance.value.update()
    }
  },
  { deep: true }
)
</script>

<style scoped>
.health-trend-chart {
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

.chart-empty {
  padding: 40px 20px;
  text-align: center;
}
</style>
