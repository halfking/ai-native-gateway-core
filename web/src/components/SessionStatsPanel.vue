<script setup lang="ts">
/**
 * SessionStatsPanel — 首页 stats tab 会话基础统计总览
 * KPI / 趋势对齐 session_summaries + days；细分块复用已拉的 perf/error。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Minus, Money, CircleCheck } from '@element-plus/icons-vue'
import { useDashboard } from '../composables/useDashboard'
import DashboardStatsRow, { type DashboardStats } from './analytics/DashboardStatsRow.vue'
import SessionTrendChart from './analytics/SessionTrendChart.vue'
import HealthGradeChart from './analytics/HealthGradeChart.vue'
import SessionStatsSignals from './analytics/SessionStatsSignals.vue'
import SessionStatsRankings from './analytics/SessionStatsRankings.vue'
import type { TrendDataPoint } from './analytics/SessionTrendChart.vue'

const { t } = useI18n()

const {
  loading,
  error,
  overview,
  trend,
  performanceStats,
  errorStats,
  lastUpdated,
  responseTime,
  refresh,
  changeDays,
  days,
} = useDashboard({ autoRefresh: true, refreshInterval: 5 * 60 * 1000, defaultDays: 30 })

const dashboardStats = computed<DashboardStats>(() => {
  const ov = overview.value
  const cost = ov?.cost_stats
  const fromTrend = ov?.cost_trend?.reduce((sum, p) => sum + (p.requests ?? 0), 0) ?? 0
  return {
    totalSessions: ov?.total_sessions ?? 0,
    totalSessionsChange: (() => {
      const g = trend.value?.summary?.growth_rate
      return g != null && Math.abs(g) > 1e-9 ? g : null
    })(),
    activeSessions: ov?.active_sessions ?? 0,
    totalCost: cost?.total_cost_usd ?? 0,
    totalCostChange: (() => {
      const g = cost?.cost_growth_pct
      return g != null && Math.abs(g) > 1e-9 ? g : null
    })(),
    complianceRate: ov?.compliance_stats?.compliance_rate ?? 0,
    complianceRateChange: null,
    avgHealthScore: ov?.health_distribution?.avg_score ?? null,
    avgHealthScoreChange: null,
    avgLatency: cost?.avg_latency_ms ?? 0,
    avgLatencyChange: null,
    totalRequests: cost?.total_requests ?? fromTrend,
    totalRequestsChange: null,
    totalTokens: 0,
    totalTokensChange: null,
  }
})

const periodLabel = computed(() => {
  const ov = overview.value
  if (!ov?.period_start || !ov?.period_end) return ''
  return `${new Date(ov.period_start).toLocaleDateString()} → ${new Date(ov.period_end).toLocaleDateString()}`
})

const trendSummary = computed(() => trend.value?.summary ?? null)
const pipelineLatency = computed(() => performanceStats.value?.summary ?? null)
const errorSummary = computed(() => errorStats.value?.summary ?? null)

const trendChartData = computed<TrendDataPoint[]>(() => {
  if (trend.value?.trend?.length) {
    return trend.value.trend.map((p) => ({
      date: p.date,
      new_sessions: p.new_sessions,
      active_sessions: p.active_sessions,
      closed_sessions: p.closed_sessions,
      total_cost: p.total_cost ?? 0,
      total_requests: p.total_requests ?? 0,
    }))
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

function formatUsd(value: number) {
  return `$${value.toFixed(4)}`
}

function formatPct(value: number) {
  const sign = value > 0 ? '+' : ''
  return `${sign}${value.toFixed(1)}%`
}

function onDaysClick(next: number) {
  if (days.value === next) return
  changeDays(next)
}
</script>

<template>
  <div class="session-stats-panel">
    <div v-if="error" class="alert alert-danger" role="alert">
      <span class="alert-text">{{ error }}</span>
      <button type="button" class="alert-retry" :disabled="loading" @click="refresh">
        {{ t('sessions.stats.retry') }}
      </button>
    </div>

    <div :class="{ 'session-stats-panel--has-error': error, 'session-stats-panel--loading': loading }">
      <div class="panel-toolbar">
        <div class="panel-toolbar__meta">
          <span v-if="lastUpdated" class="meta-text">
            {{ t('sessions.stats.lastUpdated') }}: {{ lastUpdated.toLocaleString() }}
            <span v-if="responseTime"> · {{ responseTime }}ms</span>
          </span>
          <span v-if="periodLabel" class="period-banner">
            {{ t('sessions.stats.periodRange') }}: {{ periodLabel }}
            <template v-if="trendSummary">
              · {{ t('sessions.stats.periodNew') }}: {{ trendSummary.total_new.toLocaleString() }}
              · {{ t('sessions.stats.avgDailyNew') }}: {{ trendSummary.avg_daily_new?.toFixed(1) ?? '—' }}
            </template>
          </span>
        </div>
        <div class="days-switch" role="group">
          <button type="button" class="days-btn" :class="{ 'days-btn--active': days === 7 }" @click="onDaysClick(7)">
            {{ t('sessions.stats.last7Days') }}
          </button>
          <button type="button" class="days-btn" :class="{ 'days-btn--active': days === 30 }" @click="onDaysClick(30)">
            {{ t('sessions.stats.last30Days') }}
          </button>
        </div>
      </div>

      <DashboardStatsRow :stats="dashboardStats" :loading="loading" :show-tokens="false" />

      <div class="secondary-kpi-row">
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--new"><el-icon :size="24"><Plus /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.newSessions24h') }}</div>
            <div class="mini-kpi__value">{{ overview?.new_sessions_24h?.toLocaleString() ?? 0 }}</div>
          </div>
        </div>
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--closed"><el-icon :size="24"><Minus /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.closedSessions24h') }}</div>
            <div class="mini-kpi__value">{{ overview?.closed_sessions_24h?.toLocaleString() ?? 0 }}</div>
          </div>
        </div>
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--cost"><el-icon :size="24"><Money /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.avgCostPerSession') }}</div>
            <div class="mini-kpi__value">{{ formatUsd(overview?.cost_stats?.avg_cost_per_session ?? 0) }}</div>
          </div>
        </div>
        <div class="mini-kpi">
          <div class="mini-kpi__icon mini-kpi__icon--compliance"><el-icon :size="24"><CircleCheck /></el-icon></div>
          <div>
            <div class="mini-kpi__label">{{ t('sessions.stats.violations') }}</div>
            <div class="mini-kpi__value">{{ overview?.compliance_stats?.violation?.toLocaleString() ?? 0 }}</div>
          </div>
        </div>
      </div>

      <div class="charts-row">
        <div class="charts-row__trend">
          <SessionTrendChart :data="trendChartData" :loading="loading" />
        </div>
        <div class="charts-row__health">
          <HealthGradeChart
            :distribution="overview?.health_distribution ?? null"
            :avg-score="overview?.health_distribution?.avg_score"
            :loading="loading"
          />
        </div>
      </div>

      <div class="detail-grid">
        <div class="detail-card">
          <div class="detail-card__header">{{ t('sessions.stats.costBreakdown') }}</div>
          <dl class="kv-list">
            <div class="kv-row">
              <dt>{{ t('sessions.stats.totalCost') }}</dt>
              <dd>
                {{ formatUsd(overview?.cost_stats?.total_cost_usd ?? 0) }}
                <span
                  v-if="overview?.cost_stats?.cost_growth_pct != null"
                  class="growth-tag"
                  :class="(overview?.cost_stats?.cost_growth_pct ?? 0) > 0 ? 'growth-tag--bad' : 'growth-tag--good'"
                >{{ formatPct(overview?.cost_stats?.cost_growth_pct ?? 0) }}</span>
              </dd>
            </div>
            <div class="kv-row"><dt>{{ t('sessions.stats.inputCost') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.input_cost_usd ?? 0) }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.outputCost') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.output_cost_usd ?? 0) }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.avgCostPerRequest') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.avg_cost_per_request ?? 0) }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.maxCostSession') }}</dt><dd>{{ formatUsd(overview?.cost_stats?.max_cost_session ?? 0) }}</dd></div>
          </dl>
        </div>
        <div class="detail-card">
          <div class="detail-card__header">{{ t('sessions.stats.complianceBreakdown') }}</div>
          <dl class="kv-list">
            <div class="kv-row"><dt>{{ t('sessions.stats.compliant') }}</dt><dd>{{ overview?.compliance_stats?.compliant?.toLocaleString() ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.warnings') }}</dt><dd>{{ overview?.compliance_stats?.warning?.toLocaleString() ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.violations') }}</dt><dd>{{ overview?.compliance_stats?.violation?.toLocaleString() ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.promptInjection') }}</dt><dd>{{ overview?.compliance_stats?.prompt_injection_detected ?? 0 }}</dd></div>
            <div class="kv-row"><dt>{{ t('sessions.stats.piiDetected') }}</dt><dd>{{ overview?.compliance_stats?.pii_detected ?? 0 }}</dd></div>
          </dl>
        </div>
      </div>

      <SessionStatsSignals :pipeline="pipelineLatency" :errors="errorSummary" />

      <SessionStatsRankings
        v-if="overview"
        :top-clients="overview.top_clients || []"
        :top-tasks="overview.top_tasks || []"
        :model-usage="overview.model_usage || []"
      />
    </div>
  </div>
</template>

<style scoped>
.session-stats-panel { padding: 0 0 20px; width: 100%; }
.session-stats-panel--has-error { opacity: 0.6; pointer-events: none; }
.session-stats-panel--loading { opacity: 0.85; }
.panel-toolbar { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; flex-wrap: wrap; gap: 12px; }
.panel-toolbar__meta { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.meta-text, .period-banner { font-size: 12px; color: var(--muted, #909399); }
.period-banner { color: var(--text, #303133); }
.days-switch { display: inline-flex; border: 1px solid var(--border, #dcdfe6); border-radius: 6px; overflow: hidden; }
.days-btn { border: 0; background: transparent; padding: 6px 14px; font-size: 13px; cursor: pointer; color: var(--muted, #606266); }
.days-btn--active { background: var(--accent, #409eff); color: #fff; }
.alert { display: flex; align-items: center; gap: 12px; padding: 12px 16px; margin-bottom: 16px; border-radius: 6px; background: rgba(239,68,68,.1); border: 1px solid rgba(239,68,68,.3); color: #dc2626; }
.alert-retry { padding: 4px 12px; border: 1px solid rgba(239,68,68,.3); border-radius: 4px; background: white; cursor: pointer; }
.secondary-kpi-row {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 16px; margin-bottom: 16px; width: 100%;
}
.mini-kpi {
  display: flex; align-items: center; gap: 12px; padding: 14px 16px; border-radius: 10px;
  border: 1px solid var(--border, #e5e7eb); background: var(--card, #fff);
}
.mini-kpi__icon { width: 44px; height: 44px; border-radius: 10px; display: flex; align-items: center; justify-content: center; color: white; flex-shrink: 0; }
.mini-kpi__icon--new { background: linear-gradient(135deg, #667eea, #764ba2); }
.mini-kpi__icon--closed { background: linear-gradient(135deg, #868f96, #596164); }
.mini-kpi__icon--cost { background: linear-gradient(135deg, #43e97b, #38f9d7); }
.mini-kpi__icon--compliance { background: linear-gradient(135deg, #fa709a, #fee140); }
.mini-kpi__label { font-size: 13px; color: var(--muted, #909399); }
.mini-kpi__value { font-size: 22px; font-weight: 600; }
.charts-row {
  display: grid; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
  gap: 16px; margin-bottom: 16px; width: 100%;
}
@media (max-width: 960px) { .charts-row { grid-template-columns: 1fr; } }
.detail-grid {
  display: grid; grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px; margin-bottom: 16px; width: 100%;
}
@media (max-width: 900px) { .detail-grid { grid-template-columns: 1fr; } }
.detail-card {
  border: 1px solid var(--border, #e5e7eb); border-radius: 10px;
  background: var(--card, #fff); padding: 14px 16px; min-width: 0;
}
.detail-card__header { font-weight: 600; font-size: 14px; margin-bottom: 12px; }
.kv-list { margin: 0; }
.kv-row { display: flex; justify-content: space-between; gap: 12px; padding: 8px 0; border-bottom: 1px solid var(--border, #f0f0f0); font-size: 13px; }
.kv-row:last-child { border-bottom: 0; }
.kv-row dt { color: var(--muted, #909399); }
.kv-row dd { margin: 0; font-weight: 500; }
.growth-tag { margin-left: 8px; font-size: 12px; }
.growth-tag--bad { color: #dc2626; }
.growth-tag--good { color: #16a34a; }
</style>
