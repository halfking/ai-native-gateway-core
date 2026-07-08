<template>
  <div class="health-distribution-chart">
    <el-card shadow="never">
      <template #header>
        <div class="chart-header">
          <span class="chart-title">健康分布</span>
          <el-radio-group v-model="viewType" size="small" style="margin-left: auto">
            <el-radio-button label="grade">等级</el-radio-button>
            <el-radio-button label="outcome">结果</el-radio-button>
            <el-radio-button label="compliance">合规</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <div v-loading="loading" class="chart-container">
        <canvas ref="chartCanvas"></canvas>
      </div>
      <div v-if="!loading && avgHealthScore !== null" class="chart-summary">
        <el-statistic title="平均健康分" :value="avgHealthScore" :precision="1" />
      </div>
      <div v-if="!loading && !hasData" class="chart-empty">
        <el-empty description="健康评分建设中" />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useChart, createDoughnutConfig } from '@/composables/useChart'

export interface HealthDistributionData {
  gradeDistribution: Record<string, number>
  outcomeDistribution: Record<string, number>
  complianceDistribution: Record<string, number>
  avgHealthScore: number | null
}

const props = defineProps<{
  data: HealthDistributionData
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'gradeClick', grade: string): void
}>()

const chartCanvas = ref<HTMLCanvasElement | null>(null)
const viewType = ref<'grade' | 'outcome' | 'compliance'>('grade')

const hasData = computed(() => {
  if (viewType.value === 'grade') {
    return Object.values(props.data.gradeDistribution).some(v => v > 0)
  } else if (viewType.value === 'outcome') {
    return Object.values(props.data.outcomeDistribution).some(v => v > 0)
  } else {
    return Object.values(props.data.complianceDistribution).some(v => v > 0)
  }
})

const avgHealthScore = computed(() => props.data.avgHealthScore)

// 颜色方案（与全局暗色语义色对齐）
const gradeColors: Record<string, string> = {
  A: '#3fb950',
  B: '#6366f1',
  C: '#d29922',
  D: '#f85149',
  F: '#8b949e'
}

const outcomeColors: Record<string, string> = {
  completed: '#3fb950',
  error: '#f85149',
  abandoned: '#d29922',
  unknown: '#8b949e'
}

const complianceColors: Record<string, string> = {
  compliant: '#3fb950',
  warning: '#d29922',
  violation: '#f85149'
}

// Chart 配置
const chartConfig = computed(() => {
  let labels: string[] = []
  let data: number[] = []
  let colors: string[] = []

  if (viewType.value === 'grade') {
    const dist = props.data.gradeDistribution
    const gradeOrder = ['A', 'B', 'C', 'D', 'F']
    labels = gradeOrder.filter(g => dist[g] > 0).map(g => `${g} 级`)
    data = gradeOrder.filter(g => dist[g] > 0).map(g => dist[g])
    colors = gradeOrder.filter(g => dist[g] > 0).map(g => gradeColors[g])
  } else if (viewType.value === 'outcome') {
    const dist = props.data.outcomeDistribution
    const outcomeLabels: Record<string, string> = {
      completed: '正常完成',
      error: '错误',
      abandoned: '被放弃',
      unknown: '未知'
    }
    labels = Object.keys(dist).filter(k => dist[k] > 0).map(k => outcomeLabels[k] || k)
    data = Object.keys(dist).filter(k => dist[k] > 0).map(k => dist[k])
    colors = Object.keys(dist).filter(k => dist[k] > 0).map(k => outcomeColors[k] || '#8b949e')
  } else {
    const dist = props.data.complianceDistribution
    const complianceLabels: Record<string, string> = {
      compliant: '合规',
      warning: '警告',
      violation: '违规'
    }
    labels = Object.keys(dist).filter(k => dist[k] > 0).map(k => complianceLabels[k] || k)
    data = Object.keys(dist).filter(k => dist[k] > 0).map(k => dist[k])
    colors = Object.keys(dist).filter(k => dist[k] > 0).map(k => complianceColors[k] || '#8b949e')
  }

  return createDoughnutConfig(labels, data, colors, {
    responsive: true,
    maintainAspectRatio: false,
    plugins: {
      legend: {
        display: true,
        position: 'right'
      }
    },
    onClick: (event: any, elements: any) => {
      if (elements.length > 0) {
        const index = elements[0].index
        const label = labels[index]
        emit('gradeClick', label)
      }
    }
  })
})

const { chartInstance } = useChart(chartCanvas, chartConfig)

// 监听视图类型切换
watch(viewType, () => {
  if (chartInstance.value) {
    chartInstance.value.destroy()
  }
})

// 监听数据变化
watch(
  () => props.data,
  () => {
    if (chartInstance.value) {
      chartInstance.value.destroy()
    }
  },
  { deep: true }
)
</script>

<style scoped>
.health-distribution-chart {
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
  height: 280px;
  position: relative;
}

.chart-summary {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
  text-align: center;
}

.chart-empty {
  padding: 40px 20px;
  text-align: center;
}

/* 暗色系：el-card / el-statistic 默认白底，与全局卡片色对齐 */
:deep(.el-card) {
  background: var(--card);
  border-color: var(--border);
  color: var(--text);
}

:deep(.el-statistic__head) {
  font-size: 13px;
  color: var(--muted);
}

:deep(.el-statistic__content) {
  font-size: 28px;
  font-weight: 600;
  color: var(--text);
}

:deep(.el-empty__description p) {
  color: var(--muted);
}
</style>
