<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { getSessionOverview, type SessionOverviewResponse } from '../api/admin'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'

const router = useRouter()
const { t } = useI18n()

const loading = ref(false)
const error = ref<string | null>(null)
const data = ref<SessionOverviewResponse | null>(null)
const days = ref(7)

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null

onMounted(() => {
  void load()
})

async function load() {
  loading.value = true
  error.value = null
  try {
    data.value = await getSessionOverview(days.value)
    renderChart()
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e)
    error.value = msg
    console.error('Failed to load session overview:', e)
    ElMessage.error(t('sessions.stats.loadFailed'))
  } finally {
    loading.value = false
  }
}

function renderChart() {
  if (!data.value || !chartRef.value) return
  
  if (!chartInstance) {
    chartInstance = echarts.init(chartRef.value)
  }

  const dates = data.value.cost_trend.map(p => p.date)
  const costs = data.value.cost_trend.map(p => p.cost)
  const sessions = data.value.cost_trend.map(p => p.sessions)

  const option: EChartsOption = {
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' }
    },
    legend: {
      data: [t('sessions.stats.cost'), t('sessions.stats.sessionCount')]
    },
    xAxis: {
      type: 'category',
      data: dates,
      axisLabel: {
        rotate: 45,
        formatter: (value: string) => {
          const date = new Date(value)
          return `${date.getMonth() + 1}/${date.getDate()}`
        }
      }
    },
    yAxis: [
      {
        type: 'value',
        name: t('sessions.stats.cost'),
        position: 'left',
        axisLabel: { formatter: '${value}' }
      },
      {
        type: 'value',
        name: t('sessions.stats.sessionCount'),
        position: 'right'
      }
    ],
    series: [
      {
        name: t('sessions.stats.cost'),
        type: 'line',
        yAxisIndex: 0,
        data: costs,
        smooth: true,
        itemStyle: { color: '#5470c6' }
      },
      {
        name: t('sessions.stats.sessionCount'),
        type: 'bar',
        yAxisIndex: 1,
        data: sessions,
        itemStyle: { color: '#91cc75' }
      }
    ]
  }

  chartInstance.setOption(option)
}

// 健康度分数统一为 0-10（与 domains/sessionaudit/types.go 一致）。
// 阈值：>=8 绿、6-7 黄、<6 红。
function healthScoreColor(score: number | null | undefined): 'success' | 'warning' | 'danger' | 'info' {
  if (score === null || score === undefined) return 'info'
  if (score >= 8) return 'success'
  if (score >= 6) return 'warning'
  return 'danger'
}

const healthTotal = computed(() => {
  if (!data.value) return 0
  const dist = data.value.health_distribution
  return dist.a + dist.b + dist.c + dist.d + dist.f
})

function handleClientClick(clientId: string) {
  router.push(`/admin/session-analytics/clients/${clientId}`)
}

function handleTaskClick(taskId: string) {
  router.push(`/admin/session-analytics/tasks/${taskId}`)
}
</script>

<template>
  <div class="session-stats-panel">
    <div v-if="error" class="alert alert-danger" role="alert">
      <span>{{ error }}</span>
      <button class="btn btn-sm" @click="load">{{ t('sessions.stats.loadFailed') }}</button>
    </div>
    <div v-loading="loading" :class="{ 'session-stats-panel--has-error': error }">
    <!-- 统计卡片 -->
    <el-row :gutter="16" style="margin-bottom: 20px;">
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-icon total">
              <el-icon :size="32"><Collection /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">{{ t('sessions.stats.totalSessions') }}</div>
              <div class="stat-value">{{ data?.total_sessions?.toLocaleString() || 0 }}</div>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-icon active">
              <el-icon :size="32"><Check /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">{{ t('sessions.stats.activeSessions') }}</div>
              <div class="stat-value">{{ data?.active_sessions?.toLocaleString() || 0 }}</div>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-icon health">
              <el-icon :size="32"><TrendCharts /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">{{ t('sessions.stats.healthDistribution') }}</div>
              <div class="stat-value health-badges">
                <el-tag v-if="healthTotal > 0" :type="healthScoreColor(10)" size="small">≥8: {{ (data?.health_distribution?.a || 0) }}</el-tag>
                <el-tag v-if="healthTotal > 0" :type="healthScoreColor(6)" size="small">6-7: {{ (data?.health_distribution?.b || 0) + (data?.health_distribution?.c || 0) }}</el-tag>
                <el-tag v-if="healthTotal > 0" :type="healthScoreColor(0)" size="small">&lt;6: {{ (data?.health_distribution?.d || 0) + (data?.health_distribution?.f || 0) }}</el-tag>
                <span v-else>—</span>
              </div>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-icon cost">
              <el-icon :size="32"><TrendCharts /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">{{ t('sessions.stats.costTrend') }}</div>
              <div class="stat-value">
                {{ data?.cost_trend?.[data.cost_trend.length - 1]?.cost?.toFixed(2) || '0.00' }} USD
              </div>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 成本趋势图 -->
    <el-card shadow="hover" style="margin-bottom: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>{{ t('sessions.stats.trendChart') }}</span>
          <el-radio-group v-model="days" size="small" @change="load">
            <el-radio-button :label="7">{{ t('sessions.stats.last7Days') }}</el-radio-button>
            <el-radio-button :label="30">{{ t('sessions.stats.last30Days') }}</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <div ref="chartRef" style="width: 100%; height: 300px;"></div>
    </el-card>

    <!-- Top 5 排行榜 -->
    <el-row :gutter="16">
      <el-col :span="12">
        <el-card shadow="hover">
          <template #header>
            <span>{{ t('sessions.stats.topClients') }}</span>
          </template>
          <el-table :data="data?.top_clients || []" style="width: 100%" max-height="300">
            <el-table-column prop="client_id" :label="t('sessions.stats.clientId')" min-width="120">
              <template #default="{ row }">
                <el-link type="primary" @click="handleClientClick(row.client_id)">
                  {{ row.client_id }}
                </el-link>
              </template>
            </el-table-column>
            <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="100" align="right" />
            <el-table-column prop="total_cost" :label="t('sessions.stats.totalCost')" width="100" align="right">
              <template #default="{ row }">
                ${{ row.total_cost.toFixed(2) }}
              </template>
            </el-table-column>
            <el-table-column prop="avg_health" :label="t('sessions.stats.avgHealth')" width="80" align="center">
              <template #default="{ row }">
                <el-tag v-if="row.avg_health" size="small">{{ row.avg_health }}</el-tag>
                <span v-else>—</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card shadow="hover">
          <template #header>
            <span>{{ t('sessions.stats.topTasks') }}</span>
          </template>
          <el-table :data="data?.top_tasks || []" style="width: 100%" max-height="300">
            <el-table-column prop="task_id" :label="t('sessions.stats.taskId')" min-width="120">
              <template #default="{ row }">
                <el-link type="primary" @click="handleTaskClick(row.task_id)">
                  {{ row.task_id }}
                </el-link>
              </template>
            </el-table-column>
            <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="100" align="right" />
            <el-table-column prop="total_cost" :label="t('sessions.stats.totalCost')" width="100" align="right">
              <template #default="{ row }">
                ${{ row.total_cost.toFixed(2) }}
              </template>
            </el-table-column>
            <el-table-column prop="avg_health" :label="t('sessions.stats.avgHealth')" width="90" align="center">
              <template #default="{ row }">
                <el-tag v-if="row.avg_health !== null && row.avg_health !== undefined" :type="healthScoreColor(row.avg_health)" size="small">
                  {{ row.avg_health.toFixed(1) }}/10
                </el-tag>
                <span v-else>—</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>
    </div>
  </div>
</template>

<style scoped>
.session-stats-panel {
  padding: 20px;
}

.stat-card {
  display: flex;
  align-items: center;
  gap: 16px;
}

.stat-icon {
  width: 60px;
  height: 60px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.stat-icon.total {
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  color: white;
}

.stat-icon.active {
  background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
  color: white;
}

.stat-icon.health {
  background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
  color: white;
}

.stat-icon.cost {
  background: linear-gradient(135deg, #43e97b 0%, #38f9d7 100%);
  color: white;
}

.stat-content {
  flex: 1;
  min-width: 0;
}

.stat-label {
  font-size: 14px;
  color: #909399;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 24px;
  font-weight: 600;
  color: #303133;
}

.stat-value.health-badges {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  font-size: 14px;
}

:deep(.el-card__header) {
  padding: 12px 20px;
  font-weight: 500;
}
</style>
