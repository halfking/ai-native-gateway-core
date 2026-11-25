<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, computed, nextTick, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getClientAnalyticsDetail, type ClientAnalyticsDetail } from '../api/admin'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElAlert, ElButton, ElCard, ElCol, ElLink, ElRow, ElTable, ElTableColumn, ElTag } from 'element-plus'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import { useRangeDays } from '../composables/useSpanDaysRange'
import { exactRangeFromQuery, rangeFromQuery } from './admin/session-analytics/saQueryParams'
import { systemTzDateStr } from '../components/ui/kxDatePresets'
import type { KxDateRange } from '../components/ui/kx-date-types'
const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const clientId = computed(() => route.params.id as string)
const loading = ref(false)
const data = ref<ClientAnalyticsDetail | null>(null)
const errorMessage = ref<string | null>(null)
const days = ref(30)
const { presets: dayPresets, rangeValue, applyRange, setCustomRange } = useRangeDays(days, [
  { days: 7, labelKey: 'common.dateRange.preset.last7d' },
  { days: 30, labelKey: 'common.dateRange.preset.last30d' },
  { days: 90, labelKey: 'dashboard.range.last90d' },
])
const tenantId = computed(() => typeof route.query.tenant_id === 'string' ? route.query.tenant_id : undefined)
const includeProbes = computed(() => route.query.include_probes === 'true')
const exactWindow = ref<KxDateRange>()
function applyRouteWindow() {
  const window = exactRangeFromQuery(route.query)
  exactWindow.value = window
  setCustomRange(window ? { start: systemTzDateStr(Date.parse(window.start)), end: systemTzDateStr(Date.parse(window.end) - 1) } : rangeFromQuery(route.query))
}
applyRouteWindow()

function onRangeApply(range: KxDateRange) {
  exactWindow.value = undefined
  applyRange(range)
  void load()
}

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null

// ★ 2026-11-23 审计轮（P1-2）：ECharts 实例**必须**随组件卸载一起销毁。
//   此前本页与 TaskAnalyticsView 是全仓仅剩的两个「init 了但从不 dispose」的
//   视图（BoardOperationalView / UserProfileView / analytics/*Chart 组件都做了）
//   ⇒ 路由来回切换时实例泄漏、canvas 叠加、旧数据留在内存里。
//   ⇒ 写法沿用 BoardOperationalView.vue:327-331 的既有范式（dispose + 置空）。
function disposeChart() {
  if (chartInstance) {
    chartInstance.dispose()
    chartInstance = null
  }
}

onBeforeUnmount(() => {
  disposeChart()
})

onMounted(() => {
  void load()
})

// ★ 2026-11-23 审计轮（P1-2）：本页读 route.params.id 但此前**没有 watch**
//   ⇒ 换路由参数后图表还是上一个 client 的（标题已经变了、数据没变）。
//   ⇒ 这里补 watch：换 id ⇒ 丢弃旧实例（旧 series 会被 setOption 按 index
//   合并残留）⇒ 重新取数并重画。
watch([clientId, tenantId, includeProbes, () => route.query.start_date, () => route.query.end_date, () => route.query.start_time, () => route.query.end_time], () => {
  applyRouteWindow()
  disposeChart()
  data.value = null
  void load()
})

// ★ 2026-11-23 审计轮（P1-2）：有了 watch 之后可能有多轮加载在飞
//   （快速切换 client）⇒ 旧响应会盖掉新数据（标题是新的、数据是旧的）。
//   ⇒ 用请求序号丢弃过期响应（写法同 views/admin/UsageTrendExplorer.vue 的 seriesToken）。
let loadToken = 0

async function load() {
  const token = ++loadToken
  loading.value = true
  errorMessage.value = null
  try {
    const resp = await getClientAnalyticsDetail(clientId.value, days.value, tenantId.value, exactWindow.value ? { ...exactWindow.value, exact: true } : rangeValue.value ?? undefined, includeProbes.value)
    // 已被更新的一轮取代 ⇒ 丢弃本次响应（不写任何状态）。
    if (token !== loadToken) return
    data.value = resp
    // ★ 首次加载必等 nextTick：chartRef 所在的 div 在 v-if="data" 块内，
    //   data.value 刚赋值时 Vue 还没打 DOM 补丁 ⇒ chartRef.value 仍是 undefined
    //   ⇒ renderChart() 的守卫直接 return ⇒ 图表永远不初始化（空框缺陷）。
    await nextTick()
    if (token !== loadToken) return
    renderChart()
  } catch (e: any) {
    if (token !== loadToken) return
    data.value = null
    disposeChart()
    console.error('Failed to load client analytics:', e)
    errorMessage.value = e.message || t('sessions.clientAnalytics.loadError')
  } finally {
    if (token === loadToken) loading.value = false
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
        formatter: (value: string) => value.slice(5).replace('-', '/')
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
  if (!taskId || !rangeValue.value) return
  void router.push({
    path: `/admin/session-analytics/tasks/${encodeURIComponent(taskId)}`,
    query: { tenant_id: tenantId.value, start_date: rangeValue.value.start, end_date: rangeValue.value.end,
      start_time: exactWindow.value?.start, end_time: exactWindow.value?.end, include_probes: includeProbes.value ? 'true' : undefined },
  })
}

function handleSessionClick(sessionId: string) {
  // SessionPanoramaView 已迁移至 ai-session-manager plugin，全页面跳转插件会话详情。
  window.location.assign(`/plugins/ai-session-manager/sessions/${encodeURIComponent(sessionId)}`)
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
        <KxDateRangePicker
          :model-value="rangeValue"
          :presets="dayPresets"
          :max-span-days="90"
          @apply="onRangeApply"
        />
      </div>
    </div>

    <el-alert v-if="errorMessage" type="error" :closable="false" style="margin-bottom: 16px;">
      {{ errorMessage }}
    </el-alert>

    <div v-if="data">
      <el-alert v-if="data.pending_since" type="warning" :closable="false" :title="t('reports.projectedPending')" />
      <p v-if="data.usd_unmeasured_requests">{{ t('reports.projectedMissingUSD', { n: data.usd_unmeasured_requests }) }}</p>
      <!-- 统计卡片 -->
      <el-row :gutter="16" style="margin-bottom: 16px;">
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
              <div class="stat-value">{{ data.total_cost_usd == null ? '—' : '$' + data.total_cost_usd.toFixed(4) }}</div>
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
      <el-card shadow="hover" style="margin-bottom: 16px;">
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
                <template #default="scope">
                  <el-link v-if="scope?.row" type="primary" @click="handleTaskClick(scope.row.task_id)">
                    {{ scope.row.task_id }}
                  </el-link>
                </template>
              </el-table-column>
              <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="100" align="right" />
              <el-table-column prop="total_cost_usd" :label="t('sessions.stats.totalCost')" width="100" align="right">
                <template #default="scope">
                  {{ scope?.row?.total_cost_usd == null ? '—' : '$' + scope.row.total_cost_usd.toFixed(4) }}
                </template>
              </el-table-column>
              <el-table-column prop="avg_health" :label="t('sessions.stats.avgHealth')" width="80" align="center">
                <template #default="scope">
                  <el-tag v-if="scope?.row?.avg_health" size="small">{{ scope.row.avg_health }}</el-tag>
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
                <template #default="scope">
                  <el-link type="primary" @click="handleSessionClick(scope?.row?.session_id)">
                    {{ scope?.row?.session_id?.substring(0, 16) }}...
                  </el-link>
                </template>
              </el-table-column>
              <el-table-column prop="request_count" :label="t('sessions.clientAnalytics.requests')" width="80" align="right" />
              <el-table-column prop="cost_usd" :label="t('sessions.clientAnalytics.cost')" width="100" align="right">
                <template #default="scope">
                  {{ scope?.row?.cost_usd == null ? '—' : '$' + scope.row.cost_usd.toFixed(4) }}
                </template>
              </el-table-column>
              <el-table-column prop="health_grade" :label="t('sessions.clientAnalytics.health')" width="80" align="center">
                <template #default="scope">
                  <el-tag v-if="scope?.row?.health_grade" :type="healthGradeColor(scope.row.health_grade) || undefined" size="small">
                    {{ scope.row.health_grade }}
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
  padding: 16px;
}

.page-header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
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
  color: var(--text-secondary);
  margin-bottom: 8px;
}

.stat-value {
  font-size: 24px;
  font-weight: 600;
  color: var(--text);
}

:deep(.el-card__header) {
  padding: 12px 16px;
  font-weight: 500;
}
</style>
