<script setup lang="ts">
/**
 * SessionStatsPanel — 首页 stats tab 会话基础统计总览
 * 对接 DASHBOARD_API session-overview + 趋势/性能/合规等子 API
 */
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Plus, Minus, Money, CircleCheck } from '@element-plus/icons-vue'
import { useDashboard } from '../composables/useDashboard'
import DashboardStatsRow, { type DashboardStats } from './analytics/DashboardStatsRow.vue'
import SessionTrendChart from './analytics/SessionTrendChart.vue'
import HealthGradeChart from './analytics/HealthGradeChart.vue'
import type { TrendDataPoint } from './analytics/SessionTrendChart.vue'

const router = useRouter()
const { t } = useI18n()

const {
  loading,
  error,
  overview,
  trend,
  performanceStats,
  lastUpdated,
  responseTime,
  refresh,
  changeDays,
  days,
} = useDashboard({ autoRefresh: true, refreshInterval: 5 * 60 * 1000 })

const dashboardStats = computed<DashboardStats>(() => {
  const ov = overview.value
  const perf = performanceStats.value
  const totalRequestsFromTrend = ov?.cost_trend?.reduce((sum, p) => sum + (p.requests ?? 0), 0) ?? 0
  return {
    totalSessions: ov?.total_sessions ?? 0,
    totalSessionsChange: trend.value?.summary?.growth_rate ?? null,
    activeSessions: ov?.active_sessions ?? 0,
    totalCost: ov?.cost_stats?.total_cost_usd ?? 0,
    totalCostChange: ov?.cost_stats?.cost_growth_pct ?? null,
    complianceRate: ov?.compliance_stats?.compliance_rate ?? 0,
    complianceRateChange: null,
    avgHealthScore: ov?.health_distribution?.avg_score ?? null,
    avgHealthScoreChange: null,
    avgLatency: perf?.summary?.avg_latency_ms ?? 0,
    avgLatencyChange: null,
    totalRequests: perf?.summary?.total_requests ?? totalRequestsFromTrend,
    totalRequestsChange: null,
    totalTokens: 0,
    totalTokensChange: null,
  }
})

const trendChartData = computed<TrendDataPoint[]>(() => {
  if (trend.value?.trend?.length) {
    return trend.value.trend
  }
  const ov = overview.value
  if (!ov) return []
  const costByDate = new Map(ov.cost_trend.map(p => [p.date, p]))
  return ov.session_trend.map(st => {
    const cost = costByDate.get(st.date)
    return {
      date: st.date,
      new_sessions: st.new_sessions,
      active_sessions: st.active_count,
      closed_sessions: st.closed_count,
      total_cost: cost?.cost ?? 0,
      total_requests: cost?.requests ?? 0,
    }
  })
})

function healthTagType(grade: string): 'success' | 'warning' | 'danger' | 'info' {
  if (grade === 'A' || grade === 'B') return 'success'
  if (grade === 'C') return 'warning'
  if (grade === 'D' || grade === 'F') return 'danger'
  return 'info'
}

function handleClientClick(clientId: string) {
  router.push(`/admin/session-analytics/clients/${clientId}`)
}

function handleTaskClick(taskId: string) {
  router.push(`/admin/session-analytics/tasks/${taskId}`)
}

function formatUsd(value: number) {
  return `$${value.toFixed(4)}`
}

function formatPct(value: number) {
  const sign = value > 0 ? '+' : ''
  return `${sign}${value.toFixed(1)}%`
}
</script>

<template>
  <div class="session-stats-panel">
    <div v-if="error" class="alert alert-danger" role="alert">
      <span class="alert-icon" aria-hidden="true">&#x26A0;&#xFE0F;</span>
      <span class="alert-text">{{ error }}</span>
      <button type="button" class="btn btn-sm alert-retry" :disabled="loading" @click="refresh">
        {{ t('sessions.stats.retry') }}
      </button>
    </div>

    <div v-loading="loading" :class="{ 'session-stats-panel--has-error': error }">
      <div class="panel-toolbar">
        <div class="panel-toolbar__meta">
          <span v-if="lastUpdated" class="meta-text">
            {{ t('sessions.stats.lastUpdated') }}: {{ lastUpdated.toLocaleString() }}
            <span v-if="responseTime"> · {{ responseTime }}ms</span>
          </span>
        </div>
        <el-radio-group v-model="days" size="small" @change="changeDays">
          <el-radio-button :value="7">{{ t('sessions.stats.last7Days') }}</el-radio-button>
          <el-radio-button :value="30">{{ t('sessions.stats.last30Days') }}</el-radio-button>
        </el-radio-group>
      </div>

      <DashboardStatsRow :stats="dashboardStats" :loading="loading" :show-tokens="false" />

      <el-row :gutter="16" class="secondary-kpi-row">
        <el-col :xs="24" :sm="12" :md="6">
          <el-card shadow="hover" class="mini-kpi">
            <div class="mini-kpi__icon mini-kpi__icon--new"><el-icon :size="24"><Plus /></el-icon></div>
            <div>
              <div class="mini-kpi__label">{{ t('sessions.stats.newSessions24h') }}</div>
              <div class="mini-kpi__value">{{ overview?.new_sessions_24h?.toLocaleString() ?? 0 }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="24" :sm="12" :md="6">
          <el-card shadow="hover" class="mini-kpi">
            <div class="mini-kpi__icon mini-kpi__icon--closed"><el-icon :size="24"><Minus /></el-icon></div>
            <div>
              <div class="mini-kpi__label">{{ t('sessions.stats.closedSessions24h') }}</div>
              <div class="mini-kpi__value">{{ overview?.closed_sessions_24h?.toLocaleString() ?? 0 }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="24" :sm="12" :md="6">
          <el-card shadow="hover" class="mini-kpi">
            <div class="mini-kpi__icon mini-kpi__icon--cost"><el-icon :size="24"><Money /></el-icon></div>
            <div>
              <div class="mini-kpi__label">{{ t('sessions.stats.avgCostPerSession') }}</div>
              <div class="mini-kpi__value">{{ formatUsd(overview?.cost_stats?.avg_cost_per_session ?? 0) }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="24" :sm="12" :md="6">
          <el-card shadow="hover" class="mini-kpi">
            <div class="mini-kpi__icon mini-kpi__icon--compliance"><el-icon :size="24"><CircleCheck /></el-icon></div>
            <div>
              <div class="mini-kpi__label">{{ t('sessions.stats.violations') }}</div>
              <div class="mini-kpi__value">{{ overview?.compliance_stats?.violation?.toLocaleString() ?? 0 }}</div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <el-row :gutter="16" class="charts-row">
        <el-col :span="16">
          <SessionTrendChart :data="trendChartData" :loading="loading" />
        </el-col>
        <el-col :span="8">
          <HealthGradeChart
            :distribution="overview?.health_distribution ?? null"
            :avg-score="overview?.health_distribution?.avg_score"
            :loading="loading"
          />
        </el-col>
      </el-row>

      <el-row :gutter="16" class="detail-row">
        <el-col :span="12">
          <el-card shadow="hover">
            <template #header><span>{{ t('sessions.stats.costBreakdown') }}</span></template>
            <el-descriptions :column="1" border size="small">
              <el-descriptions-item :label="t('sessions.stats.totalCost')">
                {{ formatUsd(overview?.cost_stats?.total_cost_usd ?? 0) }}
                <el-tag v-if="overview?.cost_stats?.cost_growth_pct != null" size="small" class="growth-tag"
                  :type="(overview?.cost_stats?.cost_growth_pct ?? 0) > 0 ? 'danger' : 'success'">
                  {{ formatPct(overview?.cost_stats?.cost_growth_pct ?? 0) }}
                </el-tag>
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.inputCost')">
                {{ formatUsd(overview?.cost_stats?.input_cost_usd ?? 0) }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.outputCost')">
                {{ formatUsd(overview?.cost_stats?.output_cost_usd ?? 0) }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.avgCostPerRequest')">
                {{ formatUsd(overview?.cost_stats?.avg_cost_per_request ?? 0) }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.maxCostSession')">
                {{ formatUsd(overview?.cost_stats?.max_cost_session ?? 0) }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>
        <el-col :span="12">
          <el-card shadow="hover">
            <template #header><span>{{ t('sessions.stats.complianceBreakdown') }}</span></template>
            <el-descriptions :column="1" border size="small">
              <el-descriptions-item :label="t('sessions.stats.compliant')">
                {{ overview?.compliance_stats?.compliant?.toLocaleString() ?? 0 }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.warnings')">
                {{ overview?.compliance_stats?.warning?.toLocaleString() ?? 0 }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.violations')">
                {{ overview?.compliance_stats?.violation?.toLocaleString() ?? 0 }}
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.promptInjection')">
                <el-tag size="small" :type="(overview?.compliance_stats?.prompt_injection_detected ?? 0) > 0 ? 'warning' : 'success'">
                  {{ overview?.compliance_stats?.prompt_injection_detected ?? 0 }}
                </el-tag>
              </el-descriptions-item>
              <el-descriptions-item :label="t('sessions.stats.piiDetected')">
                {{ overview?.compliance_stats?.pii_detected ?? 0 }}
              </el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-col>
      </el-row>

      <el-card v-if="overview?.model_usage?.length" shadow="hover" class="model-card">
        <template #header><span>{{ t('sessions.stats.topModels') }}</span></template>
        <el-table :data="overview.model_usage" size="small" max-height="280">
          <el-table-column prop="model" :label="t('sessions.stats.model')" min-width="140" />
          <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="90" align="right" />
          <el-table-column prop="request_count" :label="t('sessions.stats.requestCount')" width="90" align="right" />
          <el-table-column :label="t('sessions.stats.totalCost')" width="100" align="right">
            <template #default="{ row }">{{ formatUsd(row.total_cost ?? 0) }}</template>
          </el-table-column>
          <el-table-column :label="t('sessions.stats.successRate')" width="90" align="right">
            <template #default="{ row }">{{ ((row.success_rate ?? 0) * 100).toFixed(1) }}%</template>
          </el-table-column>
          <el-table-column :label="t('sessions.stats.avgLatency')" width="90" align="right">
            <template #default="{ row }">{{ Math.round(row.avg_latency_ms ?? 0) }}ms</template>
          </el-table-column>
        </el-table>
      </el-card>

      <el-row :gutter="16" class="rankings-row">
        <el-col :span="12">
          <el-card shadow="hover">
            <template #header><span>{{ t('sessions.stats.topClients') }}</span></template>
            <el-table :data="overview?.top_clients || []" max-height="300">
              <el-table-column prop="client_id" :label="t('sessions.stats.clientId')" min-width="120">
                <template #default="{ row }">
                  <el-link type="primary" @click="handleClientClick(row.client_id)">{{ row.client_id }}</el-link>
                </template>
              </el-table-column>
              <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="90" align="right" />
              <el-table-column :label="t('sessions.stats.totalCost')" width="100" align="right">
                <template #default="{ row }">{{ formatUsd(row.total_cost ?? 0) }}</template>
              </el-table-column>
              <el-table-column :label="t('sessions.stats.avgHealth')" width="80" align="center">
                <template #default="{ row }">
                  <el-tag v-if="row.avg_health != null" size="small">{{ row.avg_health }}</el-tag>
                  <span v-else>—</span>
                </template>
              </el-table-column>
            </el-table>
          </el-card>
        </el-col>
        <el-col :span="12">
          <el-card shadow="hover">
            <template #header><span>{{ t('sessions.stats.topTasks') }}</span></template>
            <el-table :data="overview?.top_tasks || []" max-height="300">
              <el-table-column prop="task_id" :label="t('sessions.stats.taskId')" min-width="120">
                <template #default="{ row }">
                  <el-link type="primary" @click="handleTaskClick(row.task_id)">{{ row.task_id }}</el-link>
                </template>
              </el-table-column>
              <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="90" align="right" />
              <el-table-column :label="t('sessions.stats.totalCost')" width="100" align="right">
                <template #default="{ row }">{{ formatUsd(row.total_cost ?? 0) }}</template>
              </el-table-column>
              <el-table-column :label="t('sessions.stats.avgHealth')" width="90" align="center">
                <template #default="{ row }">
                  <el-tag v-if="row.avg_health != null" :type="healthTagType(row.avg_health >= 75 ? 'A' : row.avg_health >= 60 ? 'C' : 'F')" size="small">
                    {{ row.avg_health }}/100
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
.session-stats-panel { padding: 0 0 20px; }
.session-stats-panel--has-error { opacity: 0.6; pointer-events: none; }
.panel-toolbar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; flex-wrap: wrap; gap: 8px; }
.meta-text { font-size: 12px; color: var(--muted, #909399); }
.alert { display: flex; align-items: center; gap: 12px; padding: 12px 16px; margin-bottom: 16px; border-radius: 6px; background: rgba(239,68,68,.1); border: 1px solid rgba(239,68,68,.3); color: #dc2626; }
.alert-retry { padding: 4px 12px; border: 1px solid rgba(239,68,68,.3); border-radius: 4px; background: white; cursor: pointer; }
.secondary-kpi-row, .charts-row, .detail-row, .rankings-row { margin-bottom: 16px; }
.mini-kpi { display: flex; align-items: center; gap: 12px; }
.mini-kpi :deep(.el-card__body) { display: flex; align-items: center; gap: 12px; width: 100%; }
.mini-kpi__icon { width: 44px; height: 44px; border-radius: 10px; display: flex; align-items: center; justify-content: center; color: white; flex-shrink: 0; }
.mini-kpi__icon--new { background: linear-gradient(135deg, #667eea, #764ba2); }
.mini-kpi__icon--closed { background: linear-gradient(135deg, #868f96, #596164); }
.mini-kpi__icon--cost { background: linear-gradient(135deg, #43e97b, #38f9d7); }
.mini-kpi__icon--compliance { background: linear-gradient(135deg, #fa709a, #fee140); }
.mini-kpi__label { font-size: 13px; color: var(--muted, #909399); }
.mini-kpi__value { font-size: 22px; font-weight: 600; }
.model-card { margin-bottom: 16px; }
.growth-tag { margin-left: 8px; }
</style>
