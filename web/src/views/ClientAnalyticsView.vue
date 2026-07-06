<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getClientAnalyticsDetail, type ClientAnalyticsDetail } from '../api/admin'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const clientId = computed(() => route.params.id as string)
const loading = ref(false)
const data = ref<ClientAnalyticsDetail | null>(null)
const errorMessage = ref<string | null>(null)
const days = ref(30)

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null

onMounted(() => {
  void load()
})

async function load() {
  loading.value = true
  errorMessage.value = null
  try {
    data.value = await getClientAnalyticsDetail(clientId.value, days.value)
    renderChart()
  } catch (e: any) {
    console.error('Failed to load client analytics:', e)
    errorMessage.value = e.message || t('sessions.clientAnalytics.loadError')
  } finally {
    loading.value = false
  }
}

function renderChart() {
  if (!data.value || !chartRef.value) return
  
  if (!chartInstance) {
    chartInstance = echarts.init(chartRef.value)
  }

  const dates = data.value.daily_cost_trend.map(p => p.date)
  const costs = data.value.daily_cost_trend.map(p => p.cost)
  const sessions = data.value.daily_cost_trend.map(p => p.sessions)

  const option: EChartsOption = {
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' }
    },
    legend: {
      data: [t('sessions.clientAnalytics.cost'), t('sessions.clientAnalytics.sessions')]
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
        name: t('sessions.clientAnalytics.cost'),
        position: 'left',
        axisLabel: { formatter: '${value}' }
      },
      {
        type: 'value',
        name: t('sessions.clientAnalytics.sessions'),
        position: 'right'
      }
    ],
    series: [
      {
        name: t('sessions.clientAnalytics.cost'),
        type: 'line',
        yAxisIndex: 0,
        data: costs,
        smooth: true,
        itemStyle: { color: '#5470c6' }
      },
      {
        name: t('sessions.clientAnalytics.sessions'),
        type: 'bar',
        yAxisIndex: 1,
        data: sessions,
        itemStyle: { color: '#91cc75' }
      }
    ]
  }

  chartInstance.setOption(option)
}

const healthGradeColor = (grade?: string) => {
  if (!grade) return 'info'
  switch (grade) {
    case 'A': return 'success'
    case 'B': return 'primary'
    case 'C': return 'warning'
    case 'D': return ''
    case 'F': return 'danger'
    default: return 'info'
  }
}

function handleTaskClick(taskId: string) {
  router.push(`/admin/session-analytics/tasks/${taskId}`)
}

function handleSessionClick(sessionId: string) {
  router.push(`/admin/session-analytics/${sessionId}/panorama`)
}

function goBack() {
  router.back()
}
</script>

<template>
  <div v-loading="loading" class="client-analytics-view">
    <div class="page-header">
      <el-button @click="goBack" :icon="'ArrowLeft'">{{ t('common.back') }}</el-button>
      <h2>{{ t('sessions.clientAnalytics.title') }}: {{ clientId }}</h2>
      <div class="page-actions">
        <el-radio-group v-model="days" size="small" @change="load">
          <el-radio-button :label="7">{{ t('sessions.stats.last7Days') }}</el-radio-button>
          <el-radio-button :label="30">{{ t('sessions.stats.last30Days') }}</el-radio-button>
          <el-radio-button :label="90">90{{ t('common.days') }}</el-radio-button>
        </el-radio-group>
      </div>
    </div>

    <el-alert v-if="errorMessage" type="error" :closable="false" style="margin-bottom: 20px;">
      {{ errorMessage }}
    </el-alert>

    <div v-if="data">
      <!-- 统计卡片 -->
      <el-row :gutter="16" style="margin-bottom: 20px;">
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('sessions.clientAnalytics.totalSessions') }}</div>
              <div class="stat-value">{{ data.session_count.toLocaleString() }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('sessions.clientAnalytics.totalCost') }}</div>
              <div class="stat-value">${{ data.total_cost_usd.toFixed(2) }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('sessions.clientAnalytics.avgHealth') }}</div>
              <div class="stat-value">{{ data.avg_health_score ?? '—' }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('sessions.clientAnalytics.successRate') }}</div>
              <div class="stat-value">
                {{ data.total_success + data.total_errors > 0 
                   ? ((data.total_success / (data.total_success + data.total_errors)) * 100).toFixed(1) 
                   : '0.0' }}%
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <!-- 成本趋势图 -->
      <el-card shadow="hover" style="margin-bottom: 20px;">
        <template #header>
          <span>{{ t('sessions.clientAnalytics.costTrend') }}</span>
        </template>
        <div ref="chartRef" style="width: 100%; height: 300px;"></div>
      </el-card>

      <!-- 关联任务和最近会话 -->
      <el-row :gutter="16">
        <el-col :span="12">
          <el-card shadow="hover">
            <template #header>
              <span>{{ t('sessions.clientAnalytics.relatedTasks') }}</span>
            </template>
            <el-table :data="data.related_tasks" style="width: 100%" max-height="400">
              <el-table-column prop="task_id" :label="t('sessions.stats.taskId')" min-width="120">
                <template #default="{ row }">
                  <el-link type="primary" @click="handleTaskClick(row.task_id)">
                    {{ row.task_id }}
                  </el-link>
                </template>
              </el-table-column>
              <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="100" align="right" />
              <el-table-column prop="total_cost_usd" :label="t('sessions.stats.totalCost')" width="100" align="right">
                <template #default="{ row }">
                  ${{ row.total_cost_usd.toFixed(2) }}
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
              <span>{{ t('sessions.clientAnalytics.recentSessions') }}</span>
            </template>
            <el-table :data="data.recent_sessions" style="width: 100%" max-height="400">
              <el-table-column prop="session_id" :label="t('sessions.clientAnalytics.sessionId')" min-width="150">
                <template #default="{ row }">
                  <el-link type="primary" @click="handleSessionClick(row.session_id)">
                    {{ row.session_id.substring(0, 16) }}...
                  </el-link>
                </template>
              </el-table-column>
              <el-table-column prop="request_count" :label="t('sessions.clientAnalytics.requests')" width="80" align="right" />
              <el-table-column prop="cost_usd" :label="t('sessions.clientAnalytics.cost')" width="100" align="right">
                <template #default="{ row }">
                  ${{ row.cost_usd.toFixed(4) }}
                </template>
              </el-table-column>
              <el-table-column prop="health_grade" :label="t('sessions.clientAnalytics.health')" width="80" align="center">
                <template #default="{ row }">
                  <el-tag v-if="row.health_grade" :type="healthGradeColor(row.health_grade)" size="small">
                    {{ row.health_grade }}
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
.client-analytics-view {
  padding: 20px;
}

.page-header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 20px;
}

.page-header h2 {
  flex: 1;
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.stat-item {
  text-align: center;
}

.stat-label {
  font-size: 14px;
  color: #909399;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 28px;
  font-weight: 600;
  color: #303133;
}

:deep(.el-card__header) {
  padding: 12px 20px;
  font-weight: 500;
}
</style>
