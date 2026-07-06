<template>
  <div class="session-analytics-dashboard">
    <div class="page-header">
      <h2 class="page-title">{{ t('sessions.dashboard.title') }}</h2>
      <p class="page-description">{{ t('sessions.dashboard.subtitle') }}</p>
    </div>

    <!-- 统一过滤器 -->
    <dashboard-filter-bar
      v-model="filters"
      :model-options="filterOptions.models"
      :provider-options="filterOptions.providers"
      @change="handleFilterChange"
    />

    <!-- KPI 卡片行 -->
    <dashboard-stats-row :stats="stats" :loading="statsLoading" />

    <!-- 图表网格 -->
    <div class="charts-grid">
      <!-- 第一行：时间序列图表 -->
      <el-row :gutter="16">
        <el-col :xs="24" :sm="24" :md="12" :lg="12" :xl="12">
          <activity-timeline-chart
            :data="activityData"
            :loading="activityLoading"
            @date-click="handleDateClick"
          />
        </el-col>
        <el-col :xs="24" :sm="24" :md="12" :lg="12" :xl="12">
          <cost-trend-chart
            :data="costData"
            :summary="costSummary"
            :loading="costLoading"
            @date-click="handleDateClick"
          />
        </el-col>
      </el-row>

      <!-- 第二行：延迟和健康趋势 -->
      <el-row :gutter="16" style="margin-top: 16px">
        <el-col :xs="24" :sm="24" :md="12" :lg="12" :xl="12">
          <latency-trend-chart :data="latencyData" :loading="latencyLoading" />
        </el-col>
        <el-col :xs="24" :sm="24" :md="12" :lg="12" :xl="12">
          <health-trend-chart :data="healthData" :loading="healthLoading" />
        </el-col>
      </el-row>

      <!-- 第三行：分布图表 -->
      <el-row :gutter="16" style="margin-top: 16px">
        <el-col :xs="24" :sm="24" :md="8" :lg="8" :xl="8">
          <model-breakdown-chart
            :data="modelBreakdownData"
            :loading="breakdownLoading"
            @model-click="handleModelClick"
          />
        </el-col>
        <el-col :xs="24" :sm="24" :md="8" :lg="8" :xl="8">
          <session-shape-chart
            :data="shapeData"
            :loading="shapeLoading"
            @bucket-click="handleShapeClick"
          />
        </el-col>
        <el-col :xs="24" :sm="24" :md="8" :lg="8" :xl="8">
          <health-distribution-chart
            :data="healthDistribution"
            :loading="healthDistLoading"
            @grade-click="handleGradeClick"
          />
        </el-col>
      </el-row>

      <!-- 第四行：热门会话表格 -->
      <el-row :gutter="16" style="margin-top: 16px">
        <el-col :span="24">
          <top-sessions-table
            :data="topSessions"
            :loading="topSessionsLoading"
            :show-tenant="isSuperAdmin"
          />
        </el-col>
      </el-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import DashboardFilterBar, { type AnalyticsFilters } from '@/components/analytics/DashboardFilterBar.vue'
import DashboardStatsRow, { type DashboardStats } from '@/components/analytics/DashboardStatsRow.vue'
import ActivityTimelineChart, { type ActivityDataPoint } from '@/components/analytics/ActivityTimelineChart.vue'
import CostTrendChart, { type CostDataPoint, type CostSummary } from '@/components/analytics/CostTrendChart.vue'
import LatencyTrendChart, { type LatencyDataPoint } from '@/components/analytics/LatencyTrendChart.vue'
import HealthTrendChart, { type HealthDataPoint } from '@/components/analytics/HealthTrendChart.vue'
import ModelBreakdownChart, { type ModelBreakdownItem } from '@/components/analytics/ModelBreakdownChart.vue'
import SessionShapeChart, { type SessionShapeData } from '@/components/analytics/SessionShapeChart.vue'
import HealthDistributionChart, { type HealthDistributionData } from '@/components/analytics/HealthDistributionChart.vue'
import TopSessionsTable, { type TopSession } from '@/components/analytics/TopSessionsTable.vue'
import { store } from '@/store'
import api from '@/api'

const { t } = useI18n()

const router = useRouter()

// 当前用户是否为超级管理员
const isSuperAdmin = computed(() => store.userInfo?.role === 'super_admin')

// 过滤器状态
const filters = ref<AnalyticsFilters>({
  dateFrom: '',
  dateTo: '',
  model: [],
  provider: [],
  complianceStatus: '',
  userIntent: '',
  healthGrade: '',
  minCost: null,
  maxCost: null,
  latencyMin: null,
  latencyMax: null,
  tokenMin: null,
  requestCountMin: null
})

// 过滤器选项
const filterOptions = reactive({
  models: [] as string[],
  providers: [] as string[]
})

// 数据状态
const statsLoading = ref(false)
const activityLoading = ref(false)
const costLoading = ref(false)
const latencyLoading = ref(false)
const healthLoading = ref(false)
const breakdownLoading = ref(false)
const shapeLoading = ref(false)
const healthDistLoading = ref(false)
const topSessionsLoading = ref(false)

// KPI 统计数据
const stats = ref<DashboardStats>({
  totalSessions: 0,
  totalSessionsChange: null,
  activeSessions: 0,
  totalCost: 0,
  totalCostChange: null,
  complianceRate: 0,
  complianceRateChange: null,
  avgHealthScore: null,
  avgHealthScoreChange: null,
  avgLatency: 0,
  avgLatencyChange: null,
  totalRequests: 0,
  totalRequestsChange: null,
  totalTokens: 0,
  totalTokensChange: null
})

// 图表数据
const activityData = ref<ActivityDataPoint[]>([])
const costData = ref<CostDataPoint[]>([])
const costSummary = ref<CostSummary | undefined>(undefined)
const latencyData = ref<LatencyDataPoint[]>([])
const healthData = ref<HealthDataPoint[]>([])
const modelBreakdownData = ref<ModelBreakdownItem[]>([])
const shapeData = ref<SessionShapeData>({
  requestCountBuckets: []
})
const healthDistribution = ref<HealthDistributionData>({
  gradeDistribution: {},
  outcomeDistribution: {},
  complianceDistribution: {},
  avgHealthScore: null
})
const topSessions = ref<TopSession[]>([])

// 加载过滤器选项
const loadFilterOptions = async () => {
  try {
    const response = await api.get('/api/admin/session-analytics/filter-options')
    filterOptions.models = response.data.models || []
    filterOptions.providers = response.data.providers || []
  } catch (error) {
    console.error('Failed to load filter options:', error)
  }
}

// 构建查询参数
const buildQueryParams = () => {
  const params: Record<string, any> = {
    date_from: filters.value.dateFrom,
    date_to: filters.value.dateTo
  }

  if (filters.value.model.length > 0) {
    params.model = filters.value.model.join(',')
  }
  if (filters.value.provider.length > 0) {
    params.provider = filters.value.provider.join(',')
  }
  if (filters.value.complianceStatus) {
    params.compliance_status = filters.value.complianceStatus
  }
  if (filters.value.userIntent) {
    params.user_intent = filters.value.userIntent
  }
  if (filters.value.healthGrade) {
    params.health_grade = filters.value.healthGrade
  }
  if (filters.value.minCost !== null) {
    params.min_cost = filters.value.minCost
  }
  if (filters.value.maxCost !== null) {
    params.max_cost = filters.value.maxCost
  }
  if (filters.value.latencyMin !== null) {
    params.latency_min = filters.value.latencyMin
  }
  if (filters.value.latencyMax !== null) {
    params.latency_max = filters.value.latencyMax
  }

  return params
}

// 加载 KPI 统计
const loadStats = async () => {
  statsLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/stats', { params })
    
    stats.value = {
      totalSessions: response.data.total_sessions || 0,
      totalSessionsChange: response.data.total_sessions_change,
      activeSessions: response.data.active_sessions || 0,
      totalCost: response.data.total_cost_usd || 0,
      totalCostChange: response.data.total_cost_change,
      complianceRate: response.data.compliance_rate || 0,
      complianceRateChange: response.data.compliance_rate_change,
      avgHealthScore: response.data.avg_health_score,
      avgHealthScoreChange: response.data.avg_health_score_change,
      avgLatency: response.data.avg_latency_ms || 0,
      avgLatencyChange: response.data.avg_latency_change,
      totalRequests: response.data.total_requests || 0,
      totalRequestsChange: response.data.total_requests_change,
      totalTokens: response.data.total_tokens || 0,
      totalTokensChange: response.data.total_tokens_change
    }
  } catch (error: any) {
    console.error('Failed to load stats:', error)
    ElMessage.error(error.response?.data?.message || t('sessions.stats.loadFailed'))
  } finally {
    statsLoading.value = false
  }
}

// 加载活动趋势
const loadActivityData = async () => {
  activityLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/activity', { params })
    activityData.value = (response.data.series || []).map((item: any) => ({
      date: item.date,
      sessionCount: item.session_count || 0,
      requestCount: item.request_count || 0,
      successCount: item.success_count || 0,
      errorCount: item.error_count || 0
    }))
  } catch (error: any) {
    console.error('Failed to load activity data:', error)
    activityData.value = []
  } finally {
    activityLoading.value = false
  }
}

// 加载成本趋势
const loadCostData = async () => {
  costLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/cost-trend', { params })
    costData.value = (response.data.series || []).map((item: any) => ({
      date: item.date,
      inputCost: item.input_cost_usd || 0,
      outputCost: item.output_cost_usd || 0,
      totalCost: item.total_cost_usd || 0
    }))
    
    if (response.data.summary) {
      costSummary.value = {
        totalCost: response.data.summary.total_cost_usd || 0,
        avgDailyCost: response.data.summary.avg_daily_cost || 0,
        costTrend: response.data.summary.cost_trend || 'flat',
        trendPct: response.data.summary.trend_pct || 0
      }
    }
  } catch (error: any) {
    console.error('Failed to load cost data:', error)
    costData.value = []
  } finally {
    costLoading.value = false
  }
}

// 加载延迟趋势
const loadLatencyData = async () => {
  latencyLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/latency-trend', { params })
    latencyData.value = (response.data.series || []).map((item: any) => ({
      date: item.date,
      p50Latency: item.p50_latency_ms || 0,
      p90Latency: item.p90_latency_ms || 0,
      p99Latency: item.p99_latency_ms || 0,
      maxLatency: item.max_latency_ms,
      avgLatency: item.avg_latency_ms
    }))
  } catch (error: any) {
    console.error('Failed to load latency data:', error)
    latencyData.value = []
  } finally {
    latencyLoading.value = false
  }
}

// 加载健康趋势
const loadHealthData = async () => {
  healthLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/health-trend', { params })
    healthData.value = (response.data.series || []).map((item: any) => ({
      date: item.date,
      avgHealthScore: item.avg_health_score,
      gradeA: item.grade_a,
      gradeB: item.grade_b,
      gradeC: item.grade_c,
      gradeD: item.grade_d,
      gradeF: item.grade_f
    }))
  } catch (error: any) {
    console.error('Failed to load health data:', error)
    healthData.value = []
  } finally {
    healthLoading.value = false
  }
}

// 加载模型分解
const loadModelBreakdown = async () => {
  breakdownLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/model-breakdown', { params })
    modelBreakdownData.value = (response.data.by_model || []).map((item: any) => ({
      model: item.model,
      requestCount: item.request_count || 0,
      sessionCount: item.session_count || 0,
      totalCost: item.total_cost_usd || 0,
      totalTokens: item.total_tokens || 0,
      avgLatency: item.avg_latency_ms || 0,
      errorRate: item.error_rate || 0
    }))
  } catch (error: any) {
    console.error('Failed to load model breakdown:', error)
    modelBreakdownData.value = []
  } finally {
    breakdownLoading.value = false
  }
}

// 加载会话形态
const loadShapeData = async () => {
  shapeLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/session-shape', { params })
    shapeData.value = {
      requestCountBuckets: response.data.request_count_buckets || []
    }
  } catch (error: any) {
    console.error('Failed to load shape data:', error)
    shapeData.value = { requestCountBuckets: [] }
  } finally {
    shapeLoading.value = false
  }
}

// 加载健康分布
const loadHealthDistribution = async () => {
  healthDistLoading.value = true
  try {
    const params = buildQueryParams()
    const response = await api.get('/api/admin/session-analytics/health-distribution', { params })
    healthDistribution.value = {
      gradeDistribution: response.data.grade_distribution || {},
      outcomeDistribution: response.data.outcome_distribution || {},
      complianceDistribution: response.data.compliance_distribution || {},
      avgHealthScore: response.data.avg_health_score
    }
  } catch (error: any) {
    console.error('Failed to load health distribution:', error)
    healthDistribution.value = {
      gradeDistribution: {},
      outcomeDistribution: {},
      complianceDistribution: {},
      avgHealthScore: null
    }
  } finally {
    healthDistLoading.value = false
  }
}

// 加载热门会话
const loadTopSessions = async () => {
  topSessionsLoading.value = true
  try {
    const params = { ...buildQueryParams(), metric: 'cost', limit: 10 }
    const response = await api.get('/api/admin/session-analytics/top-sessions', { params })
    topSessions.value = (response.data.sessions || []).map((item: any) => ({
      gwSessionId: item.gw_session_id,
      title: item.title,
      tenantId: item.tenant_id,
      requestCount: item.request_count || 0,
      totalCost: item.total_cost_usd || 0,
      totalTokens: item.total_tokens || 0,
      durationSeconds: item.duration_seconds || 0,
      avgLatency: item.avg_latency_ms || 0,
      healthGrade: item.health_grade,
      primaryModel: item.primary_model
    }))
  } catch (error: any) {
    console.error('Failed to load top sessions:', error)
    topSessions.value = []
  } finally {
    topSessionsLoading.value = false
  }
}

// 加载所有数据
const loadAllData = async () => {
  await Promise.all([
    loadStats(),
    loadActivityData(),
    loadCostData(),
    loadLatencyData(),
    loadHealthData(),
    loadModelBreakdown(),
    loadShapeData(),
    loadHealthDistribution(),
    loadTopSessions()
  ])
}

// 过滤器变更处理
const handleFilterChange = (newFilters: AnalyticsFilters) => {
  filters.value = newFilters
  loadAllData()
}

// 日期点击处理
const handleDateClick = (date: string) => {
  filters.value.dateFrom = date
  filters.value.dateTo = date
  loadAllData()
}

// 模型点击处理
const handleModelClick = (model: string) => {
  filters.value.model = [model]
  loadAllData()
}

// 形态点击处理
const handleShapeClick = (label: string) => {
  console.log('Shape clicked:', label)
  // 可以根据形态跳转到会话列表
}

// 等级点击处理
const handleGradeClick = (grade: string) => {
  console.log('Grade clicked:', grade)
  // 可以根据等级跳转到会话列表
}

// 初始化
onMounted(async () => {
  await loadFilterOptions()
  // loadAllData 会在 DashboardFilterBar 初始化后自动触发
})
</script>

<style scoped>
.session-analytics-dashboard {
  padding: 20px;
  background: #f5f7fa;
  min-height: calc(100vh - 60px);
}

.page-header {
  margin-bottom: 20px;
}

.page-title {
  font-size: 24px;
  font-weight: 600;
  color: #303133;
  margin: 0 0 8px 0;
}

.page-description {
  font-size: 14px;
  color: #909399;
  margin: 0;
}

.charts-grid {
  margin-top: 20px;
}

/* 响应式调整 */
@media (max-width: 768px) {
  .session-analytics-dashboard {
    padding: 12px;
  }

  .page-title {
    font-size: 20px;
  }
}
</style>
