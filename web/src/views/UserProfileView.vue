<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getUserProfile, type UserProfileDetail } from '../api/admin'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => route.params.owner as string)
const loading = ref(false)
const data = ref<UserProfileDetail | null>(null)
const days = ref(30)

const costChartRef = ref<HTMLElement>()
let costChart: echarts.ECharts | null = null

onMounted(() => void load())

watch(days, () => void load())

onUnmounted(() => {
  costChart?.dispose()
})

async function load() {
  loading.value = true
  try {
    data.value = await getUserProfile(owner.value, days.value)
    await nextTick()
    renderCostChart()
  } catch {
    data.value = null
  } finally {
    loading.value = false
  }
}

function renderCostChart() {
  if (!costChartRef.value || !data.value?.daily_cost_trend?.length) return
  if (!costChart) {
    costChart = echarts.init(costChartRef.value)
  }
  const trend = data.value.daily_cost_trend
  const dates = trend.map(d => d.date)
  const costs = trend.map(d => d.cost)
  const sessions = trend.map(d => d.sessions)

  const option: EChartsOption = {
    tooltip: { trigger: 'axis' },
    legend: { data: ['Cost', 'Sessions'] },
    xAxis: { type: 'category', data: dates },
    yAxis: [
      { type: 'value', name: 'Cost ($)' },
      { type: 'value', name: 'Sessions' },
    ],
    series: [
      {
        name: 'Cost',
        type: 'line',
        data: costs,
        smooth: true,
        yAxisIndex: 0,
        itemStyle: { color: '#409EFF' },
      },
      {
        name: 'Sessions',
        type: 'bar',
        data: sessions,
        yAxisIndex: 1,
        itemStyle: { color: '#67C23A' },
      },
    ],
  }
  costChart.setOption(option, true)
}

function healthGradeColor(grade?: string): string {
  if (!grade) return ''
  switch (grade) {
    case 'A': return 'success'
    case 'B': return 'primary'
    case 'C': return 'warning'
    case 'D': return 'info'
    case 'F': return 'danger'
    default: return ''
  }
}
</script>

<template>
  <div class="user-profile-detail">
    <div class="page-header">
      <el-button text @click="router.back()">← {{ t('common.back') }}</el-button>
      <h2>{{ t('sessions.userProfile.detailTitle') }}: {{ owner }}</h2>
      <el-radio-group v-model="days" size="small" @change="load">
        <el-radio-button :value="7">7{{ t('common.days') }}</el-radio-button>
        <el-radio-button :value="30">30{{ t('common.days') }}</el-radio-button>
        <el-radio-button :value="90">90{{ t('common.days') }}</el-radio-button>
      </el-radio-group>
    </div>

    <div v-loading="loading">
      <div v-if="!loading && !data" class="empty">{{ t('sessions.userProfile.empty') }}</div>

      <!-- Stat cards -->
      <el-row v-if="data" :gutter="16" class="stat-row">
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.sessionCount') }}</div>
              <div class="stat-value">{{ data?.session_count ?? '-' }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.totalCost') }}</div>
              <div class="stat-value">${{ (data?.total_cost_usd ?? 0).toFixed(4) }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.avgHealth') }}</div>
              <div class="stat-value">{{ data?.avg_health_score ?? '-' }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-label">{{ t('sessions.userProfile.successRate') }}</div>
              <div class="stat-value">
                {{
                  data && (data.total_success + data.total_errors) > 0
                    ? ((data.total_success / (data.total_success + data.total_errors)) * 100).toFixed(1) + '%'
                    : '-'
                }}
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <!-- Cost trend chart -->
      <el-card class="chart-card">
        <template #header>{{ t('sessions.userProfile.costTrend') }}</template>
        <div class="chart-container" style="height: 300px" />
      </el-card>

      <!-- Top tasks -->
      <el-card class="section-card">
        <template #header>{{ t('sessions.userProfile.topTasks') }}</template>
        <el-table :data="data?.top_tasks ?? []" stripe size="small">
          <el-table-column prop="task_id" :label="t('sessions.userProfile.taskId')" min-width="200" />
          <el-table-column prop="session_count" :label="t('sessions.userProfile.sessionCount')" width="90" align="right" />
          <el-table-column prop="total_cost" :label="t('sessions.userProfile.totalCost')" width="120" align="right">
            <template #default="scope">${{ (scope?.row?.total_cost ?? 0).toFixed(4) }}</template>
          </el-table-column>
          <el-table-column prop="avg_health" :label="t('sessions.userProfile.avgHealth')" width="80" align="right" />
        </el-table>
      </el-card>

      <!-- Top end users -->
      <el-card class="section-card">
        <template #header>{{ t('sessions.userProfile.topEndUsers') }}</template>
        <el-table :data="data?.top_end_users ?? []" stripe size="small">
          <el-table-column prop="end_user_id" :label="t('sessions.userProfile.endUserId')" min-width="200" />
          <el-table-column prop="session_count" :label="t('sessions.userProfile.sessionCount')" width="90" align="right" />
          <el-table-column prop="total_cost_usd" :label="t('sessions.userProfile.totalCost')" width="120" align="right">
            <template #default="scope">${{ (scope?.row?.total_cost_usd ?? 0).toFixed(4) }}</template>
          </el-table-column>
          <el-table-column prop="last_activity" :label="t('sessions.userProfile.lastSeenAt')" width="170" />
        </el-table>
      </el-card>

      <!-- Recent sessions -->
      <el-card class="section-card">
        <template #header>{{ t('sessions.userProfile.recentSessions') }}</template>
        <el-table :data="data?.recent_sessions ?? []" stripe size="small">
          <el-table-column prop="session_id" :label="t('sessions.userProfile.sessionId')" min-width="200">
            <template #default="scope">
              <router-link :to="`/admin/session-analytics/${scope?.row?.session_id}/panorama`" class="session-link">
                {{ scope?.row?.session_id?.slice(0, 16) }}...
              </router-link>
            </template>
          </el-table-column>
          <el-table-column prop="request_count" :label="t('sessions.userProfile.requestCount')" width="80" align="right" />
          <el-table-column prop="cost_usd" :label="t('sessions.userProfile.totalCost')" width="100" align="right">
            <template #default="scope">${{ (scope?.row?.cost_usd ?? 0).toFixed(4) }}</template>
          </el-table-column>
          <el-table-column prop="health_grade" :label="t('sessions.userProfile.avgHealthGrade')" width="80" align="center">
            <template #default="scope">
              <el-tag v-if="scope?.row?.health_grade" :type="healthGradeColor(scope?.row?.health_grade)" size="small">
                {{ scope?.row?.health_grade }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="created_at" :label="t('sessions.userProfile.createdAt')" width="170" />
        </el-table>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.user-profile-detail { padding: 20px; }
.page-header { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.stat-row { margin-bottom: 16px; }
.stat-card { text-align: center; }
.stat-label { font-size: 13px; color: #999; margin-bottom: 4px; }
.stat-value { font-size: 24px; font-weight: 700; }
.chart-card { margin-bottom: 16px; }
.section-card { margin-bottom: 16px; }
.chart-container { width: 100%; }
.session-link { color: var(--el-color-primary); text-decoration: none; }
.session-link:hover { text-decoration: underline; }
</style>
