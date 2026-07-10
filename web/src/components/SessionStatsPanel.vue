<script setup lang="ts">
/**
 * SessionStatsPanel.vue — Session statistics overview panel.
 *
 * Refactored (2026-07-10):
 *   - Switched from direct admin API to useDashboard composable
 *   - Integrated ECharts-based SessionTrendChart and HealthGradeChart
 *   - Full i18n support via vue-i18n
 *   - Vue 3 Composition API + TypeScript
 */

import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useDashboard } from '../composables/useDashboard'
import SessionTrendChart from './analytics/SessionTrendChart.vue'
import HealthGradeChart from './analytics/HealthGradeChart.vue'

const router = useRouter()
const { t } = useI18n()

const {
  loading,
  error,
  overview,
  trend,
  healthDistribution,
  refresh,
  changeDays,
  days,
} = useDashboard()

// Health score color helper (0-10 scale)
function healthScoreColor(score: number | null | undefined): 'success' | 'warning' | 'danger' | 'info' {
  if (score === null || score === undefined) return 'info'
  if (score >= 8) return 'success'
  if (score >= 6) return 'warning'
  return 'danger'
}

// Health distribution combined for badges (6-7 = B+C)
const healthGood = computed(() => overview.value?.health_distribution?.a ?? 0)
const healthFair = computed(() =>
  (overview.value?.health_distribution?.b ?? 0) + (overview.value?.health_distribution?.c ?? 0)
)
const healthPoor = computed(() =>
  (overview.value?.health_distribution?.d ?? 0) + (overview.value?.health_distribution?.f ?? 0)
)

// Latest cost from cost_trend
const latestCost = computed(() => {
  const trend = overview.value?.cost_trend
  if (!trend || trend.length === 0) return 0
  return trend[trend.length - 1]?.cost ?? 0
})

// Trend data for SessionTrendChart
const trendChartData = computed(() => trend.value?.trend ?? [])

// Navigation
function handleClientClick(clientId: string) {
  router.push(`/admin/session-analytics/clients/${clientId}`)
}

function handleTaskClick(taskId: string) {
  router.push(`/admin/session-analytics/tasks/${taskId}`)
}
</script>

<template>
  <div class="session-stats-panel">
    <!-- Error state -->
    <div v-if="error" class="alert alert-danger" role="alert">
      <span class="alert-icon" aria-hidden="true">&#x26A0;&#xFE0F;</span>
      <span class="alert-text">{{ error }}</span>
      <button
        type="button"
        class="btn btn-sm alert-retry"
        :disabled="loading"
        @click="refresh"
      >
        <span v-if="loading">&#x23F3;</span>
        <span v-else>{{ t('sessions.stats.retry') || t('dashboard.refresh') }}</span>
      </button>
    </div>

    <div v-loading="loading" :class="{ 'session-stats-panel--has-error': error }">
      <!-- Stat cards row -->
      <el-row :gutter="16" style="margin-bottom: 20px;">
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-card">
              <div class="stat-icon total">
                <el-icon :size="32"><Collection /></el-icon>
              </div>
              <div class="stat-content">
                <div class="stat-label">{{ t('sessions.stats.totalSessions') }}</div>
                <div class="stat-value">{{ overview?.total_sessions?.toLocaleString() || 0 }}</div>
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
                <div class="stat-value">{{ overview?.active_sessions?.toLocaleString() || 0 }}</div>
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
                  <el-tag
                    v-if="(healthGood + healthFair + healthPoor) > 0"
                    :type="healthScoreColor(10)"
                    size="small"
                  >
                    &ge;8: {{ healthGood }}
                  </el-tag>
                  <el-tag
                    v-if="(healthGood + healthFair + healthPoor) > 0"
                    :type="healthScoreColor(6)"
                    size="small"
                  >
                    6-7: {{ healthFair }}
                  </el-tag>
                  <el-tag
                    v-if="(healthGood + healthFair + healthPoor) > 0"
                    :type="healthScoreColor(0)"
                    size="small"
                  >
                    &lt;6: {{ healthPoor }}
                  </el-tag>
                  <span v-else>&mdash;</span>
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
                  {{ latestCost.toFixed(2) }} {{ t('dashboard.costSuffix') }}
                </div>
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <!-- Time range selector -->
      <div style="display: flex; justify-content: flex-end; margin-bottom: 12px;">
        <el-radio-group v-model="days" size="small" @change="changeDays">
          <el-radio-button :value="7">{{ t('sessions.stats.last7Days') }}</el-radio-button>
          <el-radio-button :value="30">{{ t('sessions.stats.last30Days') }}</el-radio-button>
        </el-radio-group>
      </div>

      <!-- Charts row: trend + health -->
      <el-row :gutter="16" style="margin-bottom: 20px;">
        <el-col :span="16">
          <SessionTrendChart
            :data="trendChartData"
            :loading="loading"
          />
        </el-col>
        <el-col :span="8">
          <HealthGradeChart
            :distribution="overview?.health_distribution ?? null"
            :avg-score="overview?.health_distribution?.avg_score"
            :loading="loading"
          />
        </el-col>
      </el-row>

      <!-- Top 5 rankings -->
      <el-row :gutter="16">
        <el-col :span="12">
          <el-card shadow="hover">
            <template #header>
              <span>{{ t('sessions.stats.topClients') }}</span>
            </template>
            <el-table :data="overview?.top_clients || []" style="width: 100%" max-height="300">
              <el-table-column prop="client_id" :label="t('sessions.stats.clientId')" min-width="120">
                <template #default="scope">
                  <el-link type="primary" @click="handleClientClick(scope?.row?.client_id)">
                    {{ scope?.row?.client_id }}
                  </el-link>
                </template>
              </el-table-column>
              <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="100" align="right" />
              <el-table-column prop="total_cost" :label="t('sessions.stats.totalCost')" width="100" align="right">
                <template #default="scope">
                  ${{ (scope?.row?.total_cost ?? 0).toFixed(2) }}
                </template>
              </el-table-column>
              <el-table-column prop="avg_health" :label="t('sessions.stats.avgHealth')" width="80" align="center">
                <template #default="scope">
                  <el-tag v-if="scope?.row?.avg_health" size="small">{{ scope?.row?.avg_health }}</el-tag>
                  <span v-else>&mdash;</span>
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
            <el-table :data="overview?.top_tasks || []" style="width: 100%" max-height="300">
              <el-table-column prop="task_id" :label="t('sessions.stats.taskId')" min-width="120">
                <template #default="scope">
                  <el-link type="primary" @click="handleTaskClick(scope?.row?.task_id)">
                    {{ scope?.row?.task_id }}
                  </el-link>
                </template>
              </el-table-column>
              <el-table-column prop="session_count" :label="t('sessions.stats.sessionCount')" width="100" align="right" />
              <el-table-column prop="total_cost" :label="t('sessions.stats.totalCost')" width="100" align="right">
                <template #default="scope">
                  ${{ (scope?.row?.total_cost ?? 0).toFixed(2) }}
                </template>
              </el-table-column>
              <el-table-column prop="avg_health" :label="t('sessions.stats.avgHealth')" width="90" align="center">
                <template #default="scope">
                  <el-tag
                    v-if="scope?.row?.avg_health !== null && scope?.row?.avg_health !== undefined"
                    :type="healthScoreColor(scope?.row?.avg_health)"
                    size="small"
                  >
                    {{ scope?.row?.avg_health?.toFixed(1) }}/10
                  </el-tag>
                  <span v-else>&mdash;</span>
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

.session-stats-panel--has-error {
  opacity: 0.6;
  pointer-events: none;
}

.alert {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  margin-bottom: 16px;
  border-radius: 6px;
  background: rgba(239, 68, 68, 0.1);
  border: 1px solid rgba(239, 68, 68, 0.3);
  color: #dc2626;
}

.alert-icon {
  font-size: 18px;
  flex-shrink: 0;
}

.alert-text {
  flex: 1;
  font-size: 14px;
  line-height: 1.5;
}

.alert-retry {
  flex-shrink: 0;
  padding: 4px 12px;
  border: 1px solid rgba(239, 68, 68, 0.3);
  border-radius: 4px;
  background: white;
  color: #dc2626;
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s ease;
}

.alert-retry:hover:not(:disabled) {
  background: rgba(239, 68, 68, 0.05);
  border-color: #dc2626;
}

.alert-retry:disabled {
  opacity: 0.5;
  cursor: not-allowed;
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
